package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestV14MigrationPreservationAndReentry(t *testing.T) {
	full := len(v14ColumnSpecs)
	for _, tc := range []struct {
		prefix                  int
		name, observation, exit string
	}{
		{0, "no columns", "", ""}, {1, "first epoch", "", ""}, {6, "partial pane", "", ""}, {8, "spawn columns", "", ""},
		{9, "partial owner", "", ""}, {10, "both owner columns", "", ""},
		{full, "pending candidate", `{"state":"check_permission","candidate":"needs_intelligence","candidate_since_ms":1792411208000,"observed_at_ms":1792411208001,"permission_epoch":8}`, ""},
		{full, "displayed needs_intelligence", `{"state":"needs_intelligence","since_ms":1792411208000,"request_token":"req-kept","tool_name":"Read","candidate":"check_permission","candidate_since_ms":1792411209000,"observed_at_ms":1792411209001,"permission_epoch":8}`, ""},
		{full, "displayed idle_screen", `{"state":"idle_screen","since_ms":1792411208000,"unchanged_minutes":20,"candidate":"idle_screen","candidate_since_ms":1792411208000,"observed_at_ms":1792411209001,"permission_epoch":8}`, ""},
		{full, "move candidate", `{"state":"needs_intelligence","since_ms":1792411208000,"request_token":"req-kept","tool_name":"Read","candidate":"idle_screen","candidate_since_ms":1792411209000,"observed_at_ms":1792411209001,"permission_epoch":8}`, ""},
		{full, "observation without epoch", `{"state":"check_permission","candidate":"needs_intelligence","candidate_since_ms":1792411208000,"observed_at_ms":1792411208001}`, ""},
		{full, "pending needs_intelligence exit", "", `{"prior_state":"needs_intelligence","new_state":"waiting","at_ms":1792411209000}`},
		{full, "pending idle_screen exit", "", `{"prior_state":"idle_screen","new_state":"working","at_ms":1792411209000}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := tc.prefix
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 13)
			seedV13PermissionFixture(t, path)
			var spawnCols, requestCols []string
			withRaw(t, path, func(db *sql.DB) {
				spawnCols, requestCols = tableColumnNames(t, db, "spawns"), tableColumnNames(t, db, "permission_requests")
			})
			spawn := readRawSpawn(t, path, "pre", spawnCols)
			requests := readQuotedRows(t, path, "permission_requests", "pre", "request_id", requestCols)
			preAddV14Columns(t, path, v14ColumnSpecs[:prefix])
			if prefix == full {
				withRaw(t, path, func(db *sql.DB) {
					mustExec(t, db, `UPDATE spawns SET permission_epoch=8,observation_version=4,display_observation=?,display_exit=?,
						idle_screen_timeout_seconds=90,pane_sha256=?,pane_since_ms=1792411208000,pane_epoch=8`,
						nullableStringArg(tc.observation), nullableStringArg(tc.exit), strings.Repeat("a", 64))
					mustExec(t, db, `UPDATE permission_requests SET owner_launch_token=?,owner_permission_epoch=8,
						admitted_at=1792411201123,fallback_at=1792411206000,fallback_basis='process'
						WHERE request_token=?`, testLaunchToken, tokenA)
				})
			}
			before := dbDump(t, path)
			id := assertOneStoreID(t, path)
			writeSentinel(t, dir, 13, schemaVersion)
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			fresh, _ := openTempStore(t)
			if !reflect.DeepEqual(columnsByName(schemaShape(t, s.db)), columnsByName(schemaShape(t, fresh.db))) {
				t.Fatal("fresh and upgraded schemas differ")
			}
			if s.StoreID() != id {
				t.Fatal("store identity changed")
			}
			if !reflect.DeepEqual(spawn, readRawSpawn(t, path, "pre", spawnCols)) ||
				!reflect.DeepEqual(requests, readQuotedRows(t, path, "permission_requests", "pre", "request_id", requestCols)) {
				t.Fatal("migration changed existing lifecycle, identity, hold, answer, proof or timing data")
			}
			after := dbDump(t, path)
			if prefix == len(v14ColumnSpecs) && !reflect.DeepEqual(before, after) {
				t.Fatal("migration replaced retained ownership, clocks or display candidate")
			}
			for _, table := range []string{"session_history", "store_meta", "cursor_runtimes", "subagents", "subagent_launches"} {
				if !reflect.DeepEqual(before["rows "+table], after["rows "+table]) {
					t.Errorf("changed %s", table)
				}
			}
			if prefix == 0 {
				got := readRawSpawn(t, path, "pre", v14ColumnNames("spawns"))
				for _, c := range v14ColumnSpecs {
					if c.table == "spawns" && got[c.name] != c.migratedValue() {
						t.Fatalf("fabricated historic display, observation, pane baseline or timeout override: %v", got)
					}
				}
				for _, r := range readQuotedRows(t, path, "permission_requests", "pre", "request_id", v14ColumnNames("permission_requests")) {
					if r["owner_launch_token"] != "'"+testLaunchToken+"'" || r["owner_permission_epoch"] != "0" || r["fallback_at"] != "NULL" || r["admitted_at"] != "NULL" || r["fallback_basis"] != "NULL" {
						t.Fatalf("ownership/defaults: %v", r)
					}
				}
			}
			for run := 0; run < 2; run++ {
				if err := migrateV13toV14(s.db); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(after, dbDump(t, path)) {
					t.Fatal("reentry changed retained data")
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			assertSentinel(t, dir, false)
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, dbDump(t, path)) {
				t.Fatal("reopen changed retained data")
			}
		})
	}
}

func TestV14MigrationCommitRollback(t *testing.T) {
	dir := t.TempDir()
	path := makeVersionedDB(t, dir, 13)
	seedV13PermissionFixture(t, path)
	before := dbDump(t, path)
	writeSentinel(t, dir, 13, schemaVersion)
	db := openRaw(t, path)
	mustExec(t, db, "PRAGMA journal_mode=DELETE")
	mustExec(t, db, "PRAGMA busy_timeout=1")
	reader := openRaw(t, path)
	tx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow("SELECT count(*) FROM spawns").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := migrateV13toV14(db); err == nil {
		t.Fatal("commit succeeded with blocking reader")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if readUserVersion(t, path) != 13 || !reflect.DeepEqual(before, dbDump(t, path)) {
		t.Fatal("failed commit retained DDL, ownership backfill, display data or version")
	}
	assertSentinel(t, dir, true)
	withRaw(t, path, func(db *sql.DB) { mustExec(t, db, "PRAGMA journal_mode=WAL") })
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	assertSentinel(t, dir, false)
}

func TestV14MigrationDoesNotEnrollEarlierPermissionIntervals(t *testing.T) {
	for _, tc := range []struct {
		name, state, created string
		startUnknown         bool
	}{
		{"earlier interval", StateCheckPermission, "2026-10-19 11:59:59", false},
		{"authoritative state moved on", StateWorking, "2026-10-19 12:00:01", false},
		// A zero state_changed_ms means the interval start is unknown, so no
		// creation time can prove the request is inside it.
		{"unknown interval start", StateCheckPermission, "2026-10-19 12:00:01", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 13)
			seedV13PermissionFixture(t, path)
			withRaw(t, path, func(db *sql.DB) {
				mustExec(t, db, `UPDATE spawns SET state=?,settled_state=?`, tc.state, tc.state)
				if tc.startUnknown {
					mustExec(t, db, `UPDATE spawns SET state_changed_ms=0`)
				}
				mustExec(t, db, `UPDATE permission_requests SET created_at=?`, tc.created)
			})
			writeSentinel(t, dir, 13, schemaVersion)
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			for _, row := range readQuotedRows(t, path, "permission_requests", "pre", "request_id", []string{"owner_launch_token", "owner_permission_epoch"}) {
				if row["owner_launch_token"] != "NULL" || row["owner_permission_epoch"] != "NULL" {
					t.Fatalf("enrolled retired request: %v", row)
				}
			}
		})
	}
}

// v14RefusalShape turns a refusal naming one table.column into the generic
// "<table>.<column>" form the install skill's advice quotes.
var v14RefusalShape = regexp.MustCompile(`^v13→v14 (incompatible|invalid) [a-z_]+\.[a-z0-9_]+$`)

// v14InstallSkillRefusalAdvice is the whitespace-normalized "Authorized
// migration refuses permission observation or ownership facts" paragraph of
// skills/install-agent-director/SKILL.md, which quotes the refusal names.
func v14InstallSkillRefusalAdvice(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../skills/install-agent-director/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(b), "**Authorized migration refuses permission observation or ownership facts**")
	if !ok {
		t.Fatal("install skill lacks the v13→v14 refusal advice paragraph")
	}
	para, _, _ := strings.Cut(rest, "\n\n")
	return strings.Join(strings.Fields(para), " ")
}

func TestV14MigrationRefusesIncompatiblePartialState(t *testing.T) {
	for _, tc := range []struct{ name, ddl, update, msg string }{
		{"case conflict", `ALTER TABLE permission_requests ADD COLUMN Fallback_Basis TEXT`, "", "v13→v14 incompatible permission_requests.fallback_basis"},
		{"wrong type", `ALTER TABLE permission_requests ADD COLUMN fallback_at TEXT`, "", "v13→v14 incompatible permission_requests.fallback_at"},
		{"nullable epoch", `ALTER TABLE spawns ADD COLUMN permission_epoch INTEGER DEFAULT 0`, "", "v13→v14 incompatible spawns.permission_epoch"},
		{"wrong default", `ALTER TABLE spawns ADD COLUMN observation_version INTEGER NOT NULL DEFAULT 1`, "", "v13→v14 incompatible spawns.observation_version"},
		{"unexpected default", `ALTER TABLE spawns ADD COLUMN display_observation TEXT DEFAULT NULL`, "", "v13→v14 incompatible spawns.display_observation"},
		{"unexpected not null", `ALTER TABLE permission_requests ADD COLUMN owner_launch_token TEXT NOT NULL DEFAULT ''`, "", "v13→v14 incompatible permission_requests.owner_launch_token"},
		// A default would give every legacy row a timeout override.
		{"defaulted override", `ALTER TABLE spawns ADD COLUMN idle_screen_timeout_seconds INTEGER DEFAULT 1200`, "", "v13→v14 incompatible spawns.idle_screen_timeout_seconds"},
		{"negative epoch", "", `UPDATE spawns SET permission_epoch=-1`, "v13→v14 invalid spawns.permission_epoch"},
		{"fractional version", "", `UPDATE spawns SET observation_version=0.5`, "v13→v14 invalid spawns.observation_version"},
		{"text admission", "", `UPDATE permission_requests SET admitted_at='bad'`, "v13→v14 invalid permission_requests.admitted_at"},
		{"negative fallback", "", `UPDATE permission_requests SET fallback_at=-1,fallback_basis='process'`, "v13→v14 invalid permission_requests.fallback_at"},
		{"blob owner", "", `UPDATE permission_requests SET owner_launch_token=x'6162'`, "v13→v14 invalid permission_requests.owner_launch_token"},
		{"negative owner", "", `UPDATE permission_requests SET owner_permission_epoch=-1`, "v13→v14 invalid permission_requests.owner_permission_epoch"},
		{"negative override", "", `UPDATE spawns SET idle_screen_timeout_seconds=-1`, "v13→v14 invalid spawns.idle_screen_timeout_seconds"},
		{"blob fingerprint", "", `UPDATE spawns SET pane_sha256=x'6162'`, "v13→v14 invalid spawns.pane_sha256"},
		{"text pane start", "", `UPDATE spawns SET pane_since_ms='soon'`, "v13→v14 invalid spawns.pane_since_ms"},
		{"fractional pane epoch", "", `UPDATE spawns SET pane_epoch=0.5`, "v13→v14 invalid spawns.pane_epoch"},
		{"unknown basis", "", `UPDATE permission_requests SET fallback_at=123,fallback_basis='guessed'`, "v13→v14 invalid fallback basis"},
		{"unpaired clock", "", `UPDATE permission_requests SET fallback_at=123`, "v13→v14 incomplete fallback clock"},
		{"unpaired basis", "", `UPDATE permission_requests SET fallback_basis='deadline'`, "v13→v14 incomplete fallback clock"},
		{"malformed observation", "", `UPDATE spawns SET display_observation='{'`, "v13→v14 invalid display_observation"},
		{"trailing observation", "", `UPDATE spawns SET display_observation='{}{}'`, "v13→v14 invalid display_observation"},
		{"negative observation epoch", "", `UPDATE spawns SET display_observation='{"state":"check_permission","candidate":"needs_intelligence","candidate_since_ms":123,"observed_at_ms":124,"permission_epoch":-1}'`, "v13→v14 invalid display_observation"},
		{"fractional observation epoch", "", `UPDATE spawns SET display_observation='{"state":"check_permission","candidate":"needs_intelligence","candidate_since_ms":123,"observed_at_ms":124,"permission_epoch":0.5}'`, "v13→v14 invalid display_observation"},
		{"unknown observation key", "", `UPDATE spawns SET display_observation='{"state":"working","candidate":"working","invented":true}'`, "v13→v14 invalid display_observation"},
		{"unknown displayed state", "", `UPDATE spawns SET display_observation='{"state":"guessed","candidate":"guessed","since_ms":123,"request_token":"x"}'`, "v13→v14 invalid display_observation"},
		{"idle_screen detail on needs_intelligence", "", `UPDATE spawns SET display_observation='{"state":"needs_intelligence","candidate":"needs_intelligence","since_ms":123,"request_token":"x","unchanged_minutes":1}'`, "v13→v14 invalid display_observation"},
		{"needs_intelligence detail on idle_screen", "", `UPDATE spawns SET display_observation='{"state":"idle_screen","candidate":"idle_screen","since_ms":123,"tool_name":"Read"}'`, "v13→v14 invalid display_observation"},
		{"negative unchanged minutes", "", `UPDATE spawns SET display_observation='{"state":"idle_screen","candidate":"idle_screen","since_ms":123,"unchanged_minutes":-1}'`, "v13→v14 invalid display_observation"},
		{"earlier encoding's display", "", `UPDATE spawns SET display_observation='{"state":"stuck","reason":"permission_unanswered","since_ms":123,"request_token":"x","candidate":"stuck"}'`, "v13→v14 invalid display_observation"},
		{"blob observation", "", `UPDATE spawns SET display_observation=x'7b7d'`, "v13→v14 invalid spawns.display_observation"},
		{"oversized observation", "", `UPDATE spawns SET display_observation=printf('%17000s','{}')`, "v13→v14 invalid display_observation"},
		{"exit into a Displayed State", "", `UPDATE spawns SET display_exit='{"prior_state":"needs_intelligence","new_state":"idle_screen","at_ms":123}'`, "v13→v14 invalid display_exit"},
		{"exit from an ordinary state", "", `UPDATE spawns SET display_exit='{"prior_state":"working","new_state":"waiting","at_ms":123}'`, "v13→v14 invalid display_exit"},
		{"earlier encoding's exit", "", `UPDATE spawns SET display_exit='{"new_state":"working","reason":"permission_unanswered","at_ms":123}'`, "v13→v14 invalid display_exit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 13)
			seedV13PermissionFixture(t, path)
			if tc.ddl != "" {
				withRaw(t, path, func(db *sql.DB) { mustExec(t, db, tc.ddl) })
			} else {
				preAddV14Columns(t, path, v14ColumnSpecs)
				withRaw(t, path, func(db *sql.DB) { mustExec(t, db, tc.update) })
			}
			before := dbDump(t, path)
			writeSentinel(t, dir, 13, schemaVersion)
			s, err := Open(path)
			if s != nil {
				s.Close()
			}
			if !errors.Is(err, ErrSchemaMismatch) {
				t.Fatalf("Open=%v; want schema mismatch", err)
			}
			if !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("Open=%v; want it to name %q, as the install skill's refusal advice quotes", err, tc.msg)
			}
			quoted := v14RefusalShape.ReplaceAllString(tc.msg, "v13→v14 $1 <table>.<column>")
			advice := v14InstallSkillRefusalAdvice(t)
			if !strings.Contains(advice, `"`+quoted+`"`) && !strings.Contains(advice, `"`+strings.TrimPrefix(quoted, "v13→v14 ")+`"`) {
				t.Fatalf("install skill's v13→v14 refusal advice does not quote %q", quoted)
			}
			if readUserVersion(t, path) != 13 || !reflect.DeepEqual(before, dbDump(t, path)) {
				t.Fatal("refusal changed schema, data or version before validation completed")
			}
			assertSentinel(t, dir, true)
		})
	}
}

func TestV14MigrationRollbackAndCorrectedRetry(t *testing.T) {
	for _, from := range []int{12, 13} {
		t.Run(fmt.Sprintf("from v%d", from), func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, from)
			seedPreMigrationRow(t, path, from)
			withRaw(t, path, func(db *sql.DB) {
				mustExec(t, db, `UPDATE spawns SET state='check_permission',launch_token=?,state_changed_ms=1`, testLaunchToken)
				mustExec(t, db, `INSERT INTO permission_requests(instance_id,request_token,tool_name,tool_input) VALUES('pre',?,'Bash','{}')`, tokenA)
				mustExec(t, db, `CREATE TRIGGER fail_ownership BEFORE UPDATE ON permission_requests BEGIN SELECT RAISE(ABORT,'test ownership failure'); END`)
			})
			before := dbDump(t, path)
			writeSentinel(t, dir, from, schemaVersion)
			s, err := Open(path)
			if s != nil {
				s.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "test ownership failure") {
				t.Fatalf("Open=%v", err)
			}
			if got := readUserVersion(t, path); got != 13 {
				t.Fatalf("failed hop version=%d; want committed prior hops through v13", got)
			}
			if from == 13 && !reflect.DeepEqual(before, dbDump(t, path)) {
				t.Fatal("failed hop retained mutations")
			}
			withRaw(t, path, func(db *sql.DB) {
				for _, c := range tableColumnNames(t, db, "spawns") {
					if c == "permission_epoch" {
						t.Fatal("failed ownership backfill retained DDL")
					}
				}
				mustExec(t, db, `DROP TRIGGER fail_ownership`)
			})
			assertSentinel(t, dir, true)
			if from != 13 {
				if _, err := Open(path); !errors.Is(err, ErrSchemaMigrationRequired) {
					t.Fatalf("stale authorization=%v", err)
				}
				writeSentinel(t, dir, 13, schemaVersion)
			}
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if userVersion(t, s.db) != schemaVersion {
				t.Fatal("corrected retry did not finish")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			assertSentinel(t, dir, false)
		})
	}
}

func TestV14MigrationHistoricFallbackUsability(t *testing.T) {
	for _, agent := range []string{AgentClaude, AgentCodex, AgentCursor} {
		for _, basis := range []string{FallbackDeadline, FallbackProcess, FallbackAdmission} {
			if (basis == FallbackAdmission && agent != AgentCursor) || (basis == FallbackProcess && agent == AgentCursor) {
				continue
			}
			for _, tc := range []struct {
				name, created string
				owned         bool
			}{
				{"ambiguous same second", "2026-10-19 12:00:00", false},
				{"provable following second", "2026-10-19 12:00:01", true},
			} {
				t.Run(agent+"/"+basis+"/"+tc.name, func(t *testing.T) {
					dir := t.TempDir()
					path := makeVersionedDB(t, dir, 13)
					seedV13PermissionFixture(t, path)
					withRaw(t, path, func(db *sql.DB) {
						mustExec(t, db, `DELETE FROM permission_requests`)
						mustExec(t, db, `UPDATE spawns SET agent=?,relay_mode=?`, agent, map[bool]string{true: "off", false: "on"}[basis == FallbackAdmission])
						mustExec(t, db, `INSERT INTO permission_requests(instance_id,request_token,tool_name,tool_input,created_at,settled_at,hook_gone_at)
							VALUES('pre',?,'Bash','{}',?,1792411205000,1792411206000)`, tokenB, tc.created)
					})
					var requestCols []string
					withRaw(t, path, func(db *sql.DB) { requestCols = tableColumnNames(t, db, "permission_requests") })
					before := readQuotedRows(t, path, "permission_requests", "pre", "request_id", requestCols)
					writeSentinel(t, dir, 13, schemaVersion)
					s, err := Open(path)
					if err != nil {
						t.Fatal(err)
					}
					defer s.Close()
					row, err := s.GetSpawn("pre")
					if err != nil {
						t.Fatal(err)
					}
					pr, err := s.GetPermissionRequest("pre", tokenB)
					if err != nil {
						t.Fatal(err)
					}
					if pr.SettledAt.UnixMilli() != 1792411205000 || pr.HookGoneAt.UnixMilli() != 1792411206000 || !pr.FallbackAt.IsZero() || row.DisplayObservation != "" {
						t.Fatal("migration changed trustworthy timing or fabricated observations")
					}
					if tc.owned {
						if pr.OwnerLaunchToken != row.Identity.Token || pr.OwnerPermissionEpoch != row.PermissionEpoch {
							t.Fatalf("provably current history lost ownership: %+v", pr)
						}
					} else if pr.OwnerLaunchToken != "" || pr.OwnerPermissionEpoch != -1 {
						t.Fatalf("ambiguous history acquired fabricated ownership: %+v", pr)
					}
					initialRequest := pr
					start := pr.SettledAt
					if basis == FallbackProcess {
						start = pr.HookGoneAt
					}
					if basis == FallbackAdmission {
						start = pr.CreatedAt
					}
					mark := TrailMark(t)
					for _, offset := range []time.Duration{30*time.Second - time.Millisecond, 30 * time.Second, time.Hour} {
						now := start.Add(offset)
						row, err = s.GetSpawn("pre")
						if err != nil {
							t.Fatal(err)
						}
						pr, err = s.GetPermissionRequest("pre", tokenB)
						if err != nil {
							t.Fatal(err)
						}
						display, err := s.ObserveNeedsIntelligence(row, []PermissionSample{{Request: pr, FallbackAt: start, Basis: basis}}, now, 30*time.Second, SettleRead{NowMs: now.UnixMilli()})
						want := StateCheckPermission
						if tc.owned && offset >= 30*time.Second {
							want = StateNeedsIntelligence
						}
						if err != nil || display.State != want || (want == StateNeedsIntelligence && display.RequestToken != tokenB) {
							t.Fatalf("historic %s at %s: display=%+v err=%v; want %s", basis, offset, display, err, want)
						}
					}
					if !reflect.DeepEqual(before, readQuotedRows(t, path, "permission_requests", "pre", "request_id", requestCols)) {
						t.Fatal("upgrade or observations changed the historical request, hold or fallback facts")
					}
					retained, err := s.GetPermissionRequest("pre", tokenB)
					if err != nil || !retained.AwaitsAnswer() || retained.ProvenGone() {
						t.Fatalf("historical hold lost: %+v %v", retained, err)
					}
					if !tc.owned {
						if retained != initialRequest {
							t.Fatalf("excluded history changed on observation: before=%+v after=%+v", initialRequest, retained)
						}
						if events := TrailEventsSince(t, mark, "ad.spawn.displayed_state_transition", "pre"); len(events) != 0 {
							t.Fatalf("ambiguous history emitted display records: %v", events)
						}
					} else if !retained.FallbackAt.Equal(start) || retained.FallbackBasis != basis {
						t.Fatalf("provable historical fallback clock drifted: %+v", retained)
					}
				})
			}
		}
	}
}

func TestV14MigrationNewAdmissionsQualifyBesideAmbiguousHistory(t *testing.T) {
	for _, agent := range []string{AgentClaude, AgentCodex, AgentCursor} {
		t.Run(agent, func(t *testing.T) {
			dir := t.TempDir()
			path := makeVersionedDB(t, dir, 13)
			seedV13PermissionFixture(t, path)
			withRaw(t, path, func(db *sql.DB) {
				mustExec(t, db, `DELETE FROM permission_requests`)
				mustExec(t, db, `UPDATE spawns SET agent=?,relay_mode=?`,
					agent, map[bool]string{true: "off", false: "on"}[agent == AgentCursor])
				mustExec(t, db, `INSERT INTO permission_requests(instance_id,request_token,tool_name,tool_input,created_at,settled_at,hook_gone_at)
					VALUES('pre',?,'Read','{}','2026-10-19 12:00:00',1792411205000,1792411206000)`, tokenB)
				mustExec(t, db, `UPDATE cursor_runtimes SET bound_launch_token=?,bound_gateway_pid=4242,bound_gateway_start='kept-pane'`, testLaunchToken)
			})
			writeSentinel(t, dir, 13, schemaVersion)
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			old, err := s.GetPermissionRequest("pre", tokenB)
			if err != nil || old.OwnerLaunchToken != "" || old.OwnerPermissionEpoch != -1 {
				t.Fatalf("ambiguous history: %+v %v", old, err)
			}
			row, err := s.GetSpawn("pre")
			if err != nil {
				t.Fatal(err)
			}
			req := relayReq(tokenC)
			if agent == AgentCodex {
				req.ToolUseID = ""
			}
			if agent == AgentCursor {
				b, err := s.ReadCursorRuntime("pre")
				if err != nil {
					t.Fatal(err)
				}
				req.Hook = ProcessIdentity{}
				if err := s.InsertCursorRequest(b, req, 0, req.SettledAt, DefaultLockWait, func(Spawn) error { return nil }); err != nil {
					t.Fatal(err)
				}
			} else {
				gate := HookGate{Event: "PermissionRequest", ParentPID: row.Identity.PanePID, ParentStart: row.Identity.PaneStarttime, SessionID: row.SessionID}
				if _, applied, err := s.InsertRelayRequest("pre", gate, req, 0, DefaultLockWait); err != nil || !applied.Applied {
					t.Fatalf("new relay admission: %+v %v", applied, err)
				}
			}
			current, err := s.GetPermissionRequest("pre", tokenC)
			if err != nil || current.AdmittedAt.IsZero() || current.OwnerLaunchToken != row.Identity.Token || current.OwnerPermissionEpoch != row.PermissionEpoch {
				t.Fatalf("new admission lacks precise current ownership: %+v %v", current, err)
			}
			start, basis := req.SettledAt, FallbackDeadline
			if agent == AgentCursor {
				start, basis = current.AdmittedAt, FallbackAdmission
			}
			row, err = s.GetSpawn("pre")
			if err != nil {
				t.Fatal(err)
			}
			now := start.Add(30 * time.Second)
			samples := []PermissionSample{{Request: old, FallbackAt: old.HookGoneAt, Basis: FallbackProcess}, {Request: current, FallbackAt: start, Basis: basis}}
			display, err := s.ObserveNeedsIntelligence(row, samples, now, 30*time.Second, SettleRead{NowMs: now.UnixMilli()})
			if err != nil || display.State != StateNeedsIntelligence || display.RequestToken != tokenC {
				t.Fatalf("new request did not qualify independently of ambiguous history: %+v %v", display, err)
			}
			if retained, err := s.GetPermissionRequest("pre", tokenB); err != nil || retained != old {
				t.Fatalf("new admission or observation changed excluded history: %+v %v", retained, err)
			}
		})
	}
}
