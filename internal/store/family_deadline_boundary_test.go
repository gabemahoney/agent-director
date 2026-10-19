package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFamilyRefreshAvoidsCorrelatedAggregateScans_BMPV(t *testing.T) {
	// b.mpv: repeated full aggregate scans exhausted Cursor lifecycle write budgets.
	s, path := newRelayRow(t, "on", StateWaiting)
	b := installDeadlineBoundary(t, s, path)
	var refresh string
	b.before = func(_ context.Context, q string) error {
		if strings.HasPrefix(q, familyAggregateSQL) {
			refresh = q
		}
		return nil
	}
	insertRelay(t, s, relayReq(tokenA))
	b.stopObservingAfterCleanup(t, s)
	if refresh == "" {
		t.Fatal("lifecycle write did not reach family refresh")
	}
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+refresh, s.settleMs, s.settleMs)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type step struct {
		parent int
		detail string
	}
	plan := make(map[int]step)
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan[id] = step{parent, detail}
		t.Logf("%d <- %d: %s", id, parent, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, node := range plan {
		if node.detail != "SCAN aggregate" {
			continue
		}
		for parent, ok := plan[node.parent]; ok; parent, ok = plan[parent.parent] {
			if strings.Contains(parent.detail, "CORRELATED") {
				t.Errorf("full aggregate scan repeated inside %s", parent.detail)
				break
			}
		}
	}
}

func TestLifecycleFamilyRefreshAbsoluteDeadline(t *testing.T) {
	for _, phase := range []string{"family refresh deadline", "family refresh cancellation"} {
		t.Run(phase, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWaiting)
			b := installDeadlineBoundary(t, s, path)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			deadline, _ := ctx.Deadline()
			var reached, commits atomic.Int32
			b.before = func(sqlCtx context.Context, q string) error {
				if got, ok := sqlCtx.Deadline(); !ok || got.After(deadline) {
					t.Errorf("%s received renewed SQL deadline %v, want <= %v", q, got, deadline)
				}
				if q == "COMMIT" {
					commits.Add(1)
				}
				if strings.HasPrefix(q, familyAggregateSQL) {
					reached.Add(1)
					if phase == "family refresh cancellation" {
						cancel()
					}
					<-sqlCtx.Done()
					return sqlCtx.Err()
				}
				return nil
			}
			start := time.Now()
			_, applied, err := s.WithContext(ctx).InsertRelayRequest(relayID, agentGate("PermissionRequest", ""), relayReq(tokenA), 0, 5*time.Second)
			if elapsed := time.Since(start); elapsed > 150*time.Millisecond+time.Second {
				t.Errorf("deadline overrun: %v", elapsed)
			}
			if reached.Load() != 1 {
				t.Fatalf("actual %s boundary reached %d times", phase, reached.Load())
			}
			if applied.Applied || !errors.Is(err, ErrStoreBusy) || !errors.Is(err, ctx.Err()) {
				t.Errorf("family refresh lost precommit cancellation contract: applied=%+v err=%v", applied, err)
			}
			if phase == "family refresh cancellation" && (err == nil || !strings.Contains(err.Error(), "before commit")) {
				t.Errorf("cooperative canceled family refresh lost its error: %v", err)
			}
			if commits.Load() != 0 {
				t.Error("COMMIT attempted after precommit expiry")
			}
			b.stopObservingAfterCleanup(t, s)
			assertDeadlineConnectionReusable(t, s)
			withRaw(t, path, func(db *sql.DB) {
				var state, family string
				var requests int
				if err := db.QueryRow("SELECT state, family_state, (SELECT count(*) FROM permission_requests WHERE instance_id=?) FROM spawns WHERE instance_id=?", relayID, relayID).Scan(&state, &family, &requests); err != nil {
					t.Fatal(err)
				}
				if state != StateWaiting || family != StateWaiting || requests != 0 {
					t.Errorf("canceled lifecycle write persisted state=%s family=%s requests=%d", state, family, requests)
				}
			})
		})
	}
}

func TestDecisionMetadataOnlyDoesNotRefreshFamilies(t *testing.T) {
	s, path := newRelayRow(t, "on", StateWorking)
	insertRelay(t, s, relayReq(tokenA))
	b := installDeadlineBoundary(t, s, path)
	blocked := errors.New("fixture: family refresh unavailable")
	b.before = func(_ context.Context, q string) error {
		if strings.HasPrefix(q, familyAggregateSQL) {
			return blocked
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	written, err := boundedDecision(s, ctx, 5*time.Second)
	if !written || err != nil {
		t.Errorf("metadata-only decision depended on family refresh: written=%v err=%v", written, err)
	}
	b.stopObservingAfterCleanup(t, s)
	assertDeadlineDatabaseReusable(t, s, path, "allow")
}

func TestCursorFamilyRefreshOnlyForContributionChanges(t *testing.T) {
	for _, tc := range []struct {
		name, state, operation string
		refresh                bool
	}{
		{"relay admission", StateWorking, "relay", false},
		{"cursor admission", StateWorking, "cursor", false},
		{"waiting rotation", StateWaiting, "rotation", false},
		{"waiting terminal", StateWaiting, "terminal", false},
		{"runtime authority", StatePending, "runtime", false},
		{"waiting relay rolls back", StateWaiting, "relay", true},
		{"waiting cursor rolls back", StateWaiting, "cursor", true},
		{"working rotation rolls back", StateWorking, "rotation", true},
		{"working terminal rolls back", StateWorking, "terminal", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StatePending)
			gate := agentGate("PermissionRequest", "")
			var binding CursorRuntime
			if tc.operation != "relay" {
				withRaw(t, path, func(db *sql.DB) {
					mustExec(t, db, `UPDATE spawns SET agent='cursor',jsonl_path='/tmp/family-session.jsonl' WHERE instance_id=?`, relayID)
				})
				row := mustGetSpawn(t, s, relayID)
				binding = CursorRuntime{InstanceID: relayID, Token: row.Identity.Token, Life: row.LifeNumber, GatewayPID: row.Identity.PanePID, GatewayStart: row.Identity.PaneStarttime, RuntimePID: 12345, RuntimeStart: "synthetic", Connection: "synthetic", InputPipe: "pipe:[1]", OutputPipe: "pipe:[2]", InputFD: 11, OutputFD: 12}
				if tc.operation != "runtime" {
					if ok, err := s.RegisterCursorRuntime(row, binding); err != nil || !ok {
						t.Fatalf("register: %v %v", ok, err)
					}
					if ok, err := s.SetCursorRuntimeSession(binding, "family-session"); err != nil || !ok {
						t.Fatalf("bind session: %v %v", ok, err)
					}
					binding.SessionID = "family-session"
					gate.Cursor, gate.SessionID = &binding, binding.SessionID
				}
			}
			if tc.operation != "runtime" {
				if a, err := s.ApplyHookTransition(relayID, gate, tc.state, false, "test_seed", "", false); err != nil || !a.Applied {
					t.Fatalf("seed state: %+v %v", a, err)
				}
			}
			s.SetSettleMs(10000)
			withRaw(t, path, func(db *sql.DB) {
				mustExec(t, db, `UPDATE spawns SET family_settled_state=CASE family_state WHEN 'working' THEN 'waiting' ELSE 'working' END,family_changed_ms=1234 WHERE instance_id=?`, relayID)
			})
			row := mustGetSpawn(t, s, relayID)
			history := func() []map[string]string {
				return readQuotedRows(t, path, "spawns", relayID, "instance_id", []string{"family_state", "family_settled_state", "family_changed_ms"})
			}
			beforeHistory, beforeRequests := history(), requestDump(t, path)
			archive := func() []map[string]string {
				return readQuotedRows(t, path, "session_history", relayID, "history_id", []string{"session_id", "jsonl_path", "life_number"})
			}
			beforeArchive := archive()
			b := installDeadlineBoundary(t, s, path)
			blocked := errors.New("fixture: family refresh unavailable")
			refreshes := 0
			b.before = func(_ context.Context, q string) error {
				if strings.HasPrefix(q, familyAggregateSQL) {
					refreshes++
					return blocked
				}
				return nil
			}
			check := func(Spawn) error { return nil }
			ok := false
			var err error
			switch tc.operation {
			case "relay":
				var applied HookApplied
				_, applied, err = s.InsertRelayRequest(relayID, gate, relayReq(tokenA), 0, DefaultLockWait)
				ok = applied.Applied
			case "cursor":
				req := relayReq(tokenA)
				req.Hook = ProcessIdentity{}
				err = s.InsertCursorRequest(binding, req, 0, time.Now().Add(time.Second), DefaultLockWait, check)
				ok = err == nil
			case "rotation":
				ok, err = s.RotateCursorSession(row, gate, "family-next", row.JSONLPath, time.Now().Add(time.Second), check)
			case "terminal":
				var result CondResult
				_, result, err = s.MarkCursorMissingIfSameLaunch(row, binding)
				ok = result == CondApplied
			case "runtime":
				ok, err = s.RegisterCursorRuntime(row, binding)
				if err == nil && ok {
					ok, err = s.SetCursorRuntimeSession(binding, "family-session")
				}
				if err == nil && ok {
					binding.SessionID = "family-session"
					ok, err = s.MarkCursorRuntimeGone(binding)
				}
			}
			b.stopObservingAfterCleanup(t, s)
			if tc.refresh {
				if !errors.Is(err, blocked) || ok || refreshes != 1 {
					t.Fatalf("contribution change bypassed required refresh: applied=%v refreshes=%d err=%v", ok, refreshes, err)
				}
				if !reflect.DeepEqual(row, mustGetSpawn(t, s, relayID)) || !reflect.DeepEqual(beforeRequests, requestDump(t, path)) {
					t.Fatal("failed refresh committed lifecycle/request changes")
				}
			} else {
				if err != nil || !ok || refreshes != 0 {
					t.Fatalf("contribution-preserving write depended on family refresh: applied=%v refreshes=%d err=%v", ok, refreshes, err)
				}
				after := mustGetSpawn(t, s, relayID)
				switch tc.operation {
				case "relay", "cursor":
					if after.State != StateCheckPermission || mustRequest(t, s, tokenA).RequestToken != tokenA {
						t.Fatal("admission did not persist state and request")
					}
				case "terminal":
					if after.State != StateMissing {
						t.Fatalf("terminal state: %s", after.State)
					}
				case "runtime":
					got, e := s.ReadCursorRuntime(relayID)
					if e != nil || !got.Terminated || got.SessionID != "family-session" || !reflect.DeepEqual(row, after) {
						t.Fatalf("authority publication: %+v %v", got, e)
					}
				}
			}
			if !reflect.DeepEqual(beforeHistory, history()) {
				t.Fatal("write changed family settling history")
			}
			if tc.operation == "rotation" {
				got, e := s.ReadCursorRuntime(relayID)
				wantSession := "family-next"
				if tc.refresh {
					wantSession = binding.SessionID
				}
				if e != nil || got.SessionID != wantSession || mustGetSpawn(t, s, relayID).SessionID != wantSession {
					t.Fatalf("rotation session publication: %+v err=%v", got, e)
				}
				if tc.refresh {
					if !reflect.DeepEqual(beforeArchive, archive()) {
						t.Fatal("failed rotation committed session history")
					}
				} else {
					var archived int
					withRaw(t, path, func(db *sql.DB) {
						if e := db.QueryRow(`SELECT count(*) FROM session_history WHERE instance_id=? AND session_id=? AND jsonl_path=? AND life_number=?`, relayID, binding.SessionID, row.JSONLPath, binding.Life).Scan(&archived); e != nil {
							t.Fatal(e)
						}
					})
					if archived != 1 {
						t.Fatalf("outgoing session archive count: %d", archived)
					}
				}
			}
		})
	}
}
