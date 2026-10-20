package store

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"
)

func TestV11MigrationEligibilityPreservation(t *testing.T) {
	for _, tc := range []struct {
		name         string
		agent, child any
		eligible     bool
	}{
		{"claude main", AgentClaude, nil, true}, {"codex main", AgentCodex, nil, true},
		{"legacy null agent main", nil, nil, true}, {"malformed agent main", "historical", nil, true},
		{"claude child", AgentClaude, "child", false}, {"legacy null agent child", nil, "child", false},
		{"cursor unknown", AgentCursor, nil, false}, {"cursor child", AgentCursor, "child", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 10)
			seedV10EligibilityFixture(t, path, tc.agent, tc.child)
			before := dbDump(t, path)
			var priorCols, spawnCols []string
			withRaw(t, path, func(db *sql.DB) {
				priorCols = tableColumnNames(t, db, "permission_requests")
				spawnCols = tableColumnNames(t, db, "spawns")
			})
			priorSpawns := readQuotedRows(t, path, "spawns", "pre", "instance_id", spawnCols)
			priorRequests := readQuotedRows(t, path, "permission_requests", "pre", "request_id", priorCols)
			id := assertOneStoreID(t, path)
			writeSentinel(t, dir, 10, schemaVersion)
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			pr, err := s.GetPermissionRequest("pre", tokenA)
			if err != nil {
				t.Fatal(err)
			}
			if pr.MainTurnEndEligible != tc.eligible {
				t.Errorf("eligibility=%v; want %v", pr.MainTurnEndEligible, tc.eligible)
			}
			if s.StoreID() != id {
				t.Fatal("store identity changed")
			}
			fresh, _ := openTempStore(t)
			gotShape, wantShape := schemaShape(t, s.db), schemaShape(t, fresh.db)
			if tc.agent == nil {
				delete(gotShape, "table spawns")
				delete(wantShape, "table spawns")
			}
			if !reflect.DeepEqual(gotShape, wantShape) {
				t.Fatal("fresh/migrated shape differs")
			}
			if err := migrateV10toV11(s.db); err != nil {
				t.Fatal(err)
			}
			if err := migrateV11toV12(s.db); err != nil {
				t.Fatal(err)
			}
			if err := migrateV12toV13(s.db); err != nil {
				t.Fatal(err)
			}
			if err := runMigrationChain(s.db, 13); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			after := dbDump(t, path)
			if got := readQuotedRows(t, path, "permission_requests", "pre", "request_id", priorCols); !reflect.DeepEqual(got, priorRequests) {
				t.Fatal("migration changed preexisting request columns")
			}
			if got := readQuotedRows(t, path, "spawns", "pre", "instance_id", spawnCols); !reflect.DeepEqual(got, priorSpawns) {
				t.Fatal("migration changed preexisting spawn columns")
			}
			for _, table := range []string{"session_history", "store_meta", "cursor_runtimes"} {
				if !reflect.DeepEqual(before["rows "+table], after["rows "+table]) {
					t.Errorf("%s changed", table)
				}
			}
			assertSentinel(t, dir, false)
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			reopened, err := s.GetPermissionRequest("pre", tokenA)
			if err != nil || !reflect.DeepEqual(pr, reopened) {
				t.Fatalf("reopen changed request: %+v %v", reopened, err)
			}
		})
	}
}

func TestV11MigrationExistingColumn(t *testing.T) {
	for _, tc := range []struct {
		name, definition, value string
		child                   any
		refusal                 bool
	}{
		{"explicit false", "main_turn_end_eligible INTEGER NOT NULL DEFAULT 0", "0", nil, false},
		{"explicit true", "main_turn_end_eligible INTEGER NOT NULL DEFAULT 0", "1", nil, false},
		{"child true", "main_turn_end_eligible INTEGER NOT NULL DEFAULT 0", "1", "child", true},
		{"case conflict", "Main_Turn_End_Eligible INTEGER NOT NULL DEFAULT 0", "0", nil, true},
		{"wrong type", "main_turn_end_eligible TEXT NOT NULL DEFAULT 0", "0", nil, true},
		{"nullable", "main_turn_end_eligible INTEGER DEFAULT 0", "0", nil, true},
		{"wrong default", "main_turn_end_eligible INTEGER NOT NULL DEFAULT 1", "0", nil, true},
		{"absent default", "main_turn_end_eligible INTEGER NOT NULL", "0", nil, true},
		{"integer outside range", "main_turn_end_eligible INTEGER NOT NULL DEFAULT 0", "2", nil, true},
		{"real value", "main_turn_end_eligible INTEGER NOT NULL DEFAULT 0", "0.5", nil, true},
		{"text value", "main_turn_end_eligible INTEGER NOT NULL DEFAULT 0", "'bad'", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 10)
			// Add before seeding, so even a NOT NULL column without default is constructible.
			withRaw(t, path, func(db *sql.DB) { mustExec(t, db, "ALTER TABLE permission_requests ADD COLUMN "+tc.definition) })
			seedPreMigrationRow(t, path, 10)
			withRaw(t, path, func(db *sql.DB) {
				mustExec(t, db, `INSERT INTO permission_requests(instance_id,request_token,tool_name,tool_input,agent_id,main_turn_end_eligible) VALUES('pre',?,'Bash','{}',?,`+tc.value+`)`, tokenA, tc.child)
			})
			before := dbDump(t, path)
			writeSentinel(t, dir, 10, schemaVersion)
			s, err := Open(path)
			if tc.refusal {
				if s != nil {
					s.Close()
				}
				if !errors.Is(err, ErrSchemaMismatch) {
					t.Errorf("Open=%v; want schema mismatch", err)
				}
				if !reflect.DeepEqual(before, dbDump(t, path)) {
					t.Error("refused hop changed data/schema/version")
				}
				assertSentinel(t, dir, true)
				if readUserVersion(t, path) != 10 {
					t.Error("refused hop stamped a version")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			pr, err := s.GetPermissionRequest("pre", tokenA)
			if err != nil || pr.MainTurnEndEligible != (tc.value == "1") {
				t.Fatalf("request %+v %v", pr, err)
			}
			if err = migrateV10toV11(s.db); err != nil {
				t.Fatal(err)
			}
			if err = migrateV11toV12(s.db); err != nil {
				t.Fatal(err)
			}
			if err = migrateV12toV13(s.db); err != nil {
				t.Fatal(err)
			}
			if err = runMigrationChain(s.db, 13); err != nil {
				t.Fatal(err)
			}
			s.Close()
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			again, err := s.GetPermissionRequest("pre", tokenA)
			if err != nil || !reflect.DeepEqual(pr, again) {
				t.Fatalf("reentry/reopen %+v %v", again, err)
			}
		})
	}
}

func TestV11MigrationBackfillRollbackAndRetry(t *testing.T) {
	dir := t.TempDir()
	path := makeVersionedDB(t, dir, 10)
	seedV10EligibilityFixture(t, path, AgentClaude, nil)
	withRaw(t, path, func(db *sql.DB) {
		mustExec(t, db, `CREATE TRIGGER fail_eligibility BEFORE UPDATE ON permission_requests BEGIN SELECT RAISE(ABORT,'injected backfill failure'); END`)
	})
	before := dbDump(t, path)
	id := assertOneStoreID(t, path)
	writeSentinel(t, dir, 10, schemaVersion)
	s, err := Open(path)
	if s != nil {
		s.Close()
	}
	if err == nil || errors.Is(err, ErrSchemaMigrationRequired) {
		t.Fatalf("Open=%v; want hop failure", err)
	}
	if !reflect.DeepEqual(before, dbDump(t, path)) {
		t.Fatal("failed backfill retained mutation")
	}
	assertSentinel(t, dir, true)
	if readUserVersion(t, path) != 10 {
		t.Fatal("failed mutation stamped a version")
	}
	withRaw(t, path, func(db *sql.DB) { mustExec(t, db, "DROP TRIGGER fail_eligibility") })
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pr, err := s.GetPermissionRequest("pre", tokenA)
	if err != nil || !pr.MainTurnEndEligible || s.StoreID() != id {
		t.Fatalf("corrected retry %+v %v", pr, err)
	}
	assertSentinel(t, dir, false)
}

// NULL agent is a supported historical malformed row, normalized as non-Cursor by readers.
func TestLegacyNullAgentGatedWritesAndProof(t *testing.T) {
	for _, tc := range []struct {
		name, writer, child string
		explicit            *bool
		want                bool
	}{
		{name: "relay", writer: "relay", want: true},
		{name: "legacy", writer: "legacy", want: true},
		{name: "proof", writer: "proof"},
		{name: "relay child overrides true", writer: "relay", child: "child", explicit: eligibility(true)},
		{name: "relay explicit false", writer: "relay", explicit: eligibility(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 10)
			seedV10EligibilityFixture(t, path, nil, nil)
			writeSentinel(t, dir, 10, schemaVersion)
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			withRaw(t, path, func(db *sql.DB) {
				mustExec(t, db, `UPDATE spawns SET pane_pid=?,pane_starttime=?,launch_token=? WHERE instance_id='pre'`, testPane().PanePID, testPane().PaneStarttime, testLaunchToken)
				mustExec(t, db, `UPDATE permission_requests SET main_turn_end_eligible=1,proven_gone_at=NULL,proven_gone_how=NULL,created_at='2026-01-01 00:00:00' WHERE instance_id='pre'`)
			})
			gate := agentGate("PermissionRequest", "")
			switch tc.writer {
			case "relay":
				r := mainReq(tokenB)
				r.AgentID, r.MainTurnEndEligible = tc.child, tc.explicit
				_, a, err := s.InsertRelayRequest("pre", gate, r, 0, DefaultLockWait)
				if err != nil || !a.Applied {
					t.Fatalf("historical non-Cursor relay %+v %v", a, err)
				}
			case "legacy":
				a, err := s.UpsertOpenPermissionRequest("pre", gate, tokenB, "Bash", "{}", 0, "")
				if err != nil || !a.Applied {
					t.Fatalf("historical non-Cursor legacy %+v %v", a, err)
				}
			case "proof":
				n, err := s.ProveRequestsGone("pre", gate, turnEndProof())
				if err != nil || n != 1 {
					t.Fatalf("historical non-Cursor proof %d %v; want 1", n, err)
				}
			}
			if tc.writer == "proof" {
				return
			}
			pr, err := s.GetPermissionRequest("pre", tokenB)
			if err != nil || pr.MainTurnEndEligible != tc.want || pr.AgentID != tc.child {
				t.Fatalf("historical NULL-agent request %+v %v", pr, err)
			}
			s.Close()
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if got, err := s.GetPermissionRequestByToken(tokenB); err != nil || !reflect.DeepEqual(got, pr) {
				t.Fatalf("historical NULL-agent reopen %+v %v", got, err)
			}
			wantProof := int64(1) // The existing eligible request A is covered.
			if tc.want {
				wantProof++
			}
			gate.Event = "Stop"
			if n, err := s.ProveRequestsGone("pre", gate, turnEndProof()); err != nil || n != wantProof {
				t.Fatalf("historical NULL-agent main proof %d %v; want %d", n, err, wantProof)
			}
			if got, err := s.GetPermissionRequest("pre", tokenB); err != nil || got.ProvenGone() != tc.want || got.MainTurnEndEligible != tc.want {
				t.Fatalf("historical NULL-agent proof metadata %+v %v", got, err)
			}
			if !tc.want {
				gate.Event = "PostToolUse"
				if n, err := s.ProveRequestsGone("pre", gate, RequestProof{How: ProvenGoneToolRan, ToolUseID: pr.ToolUseID, At: proofAt}); err != nil || n != 1 {
					t.Fatalf("historical NULL-agent correlated proof %d %v", n, err)
				}
			}
		})
	}
}

func TestV11MigrationOrphanDoesNotAcquireMainProof(t *testing.T) {
	dir := t.TempDir()
	path := makeVersionedDB(t, dir, 10)
	withRaw(t, path, func(db *sql.DB) {
		mustExec(t, db, `INSERT INTO permission_requests(instance_id,request_token,tool_name,tool_input) VALUES('orphan',?,'Bash','{}')`, tokenA)
	})
	writeSentinel(t, dir, 10, schemaVersion)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pr, err := s.GetPermissionRequest("orphan", tokenA)
	if err != nil || pr.MainTurnEndEligible {
		t.Fatalf("orphan %+v %v", pr, err)
	}
}
