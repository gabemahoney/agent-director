package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

func rotationStore(t *testing.T) (*cursorTrailEnv, store.Spawn, store.HookGate) {
	t.Helper()
	e := newCursorTrailEnv(t)
	e.exec(t, `UPDATE spawns SET jsonl_path='/protected/launch.jsonl',tmux_server_pid=123,tmux_server_started=456,tmux_server_starttime='synthetic-server' WHERE instance_id=?`, e.b.InstanceID)
	row, err := e.f.s.GetSpawn(e.b.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	return e, row, store.HookGate{Event: "SessionStart", ParentPID: e.b.GatewayPID, ParentStart: e.b.GatewayStart, SessionID: e.b.SessionID, Cursor: &e.b}
}
func rotationVisibility(t *testing.T, e *cursorTrailEnv, want string, archives int) {
	t.Helper()
	db, err := sql.Open("sqlite", e.f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var row, runtime, path string
	var count, life int
	err = db.QueryRow(`SELECT s.session_id,r.bound_session_id,s.jsonl_path,s.life_number,(SELECT COUNT(*) FROM session_history h WHERE h.instance_id=s.instance_id AND h.life_number=s.life_number) FROM spawns s JOIN cursor_runtimes r ON r.bound_instance_id=s.instance_id WHERE s.instance_id=?`, e.b.InstanceID).Scan(&row, &runtime, &path, &life, &count)
	if err != nil || row != want || runtime != want || path != "/protected/launch.jsonl" || int64(life) != e.b.Life || count != archives {
		t.Fatalf("atomic independent visibility row=%q runtime=%q path=%q life=%d archives=%d err=%v", row, runtime, path, life, count, err)
	}
}
func TestCursorRotationAtomicPublicationAndSuccessiveHistory(t *testing.T) {
	e, row, gate := rotationStore(t)
	observer, err := store.Open(e.f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	for i, next := range []string{"fresh-one", "fresh-two"} {
		prior := e.b.SessionID
		ctxBefore, cancelBefore := context.WithTimeout(context.Background(), time.Second)
		committed, unchanged, reconcileErr := observer.ReconcileCursorRotation(ctxBefore, e.b, row, next, row.JSONLPath)
		cancelBefore()
		if reconcileErr != nil || committed || !unchanged {
			t.Fatalf("outgoing snapshot not proven unchanged: committed=%v unchanged=%v err=%v", committed, unchanged, reconcileErr)
		}
		if observed, err := observer.ObserveCursorRotation(context.Background(), e.b, row, next, row.JSONLPath); err != nil || observed {
			t.Fatalf("unchanged snapshot claimed a fresh commit: %v %v", observed, err)
		}
		sawCommit := false
		store.BeforeCursorTrailExecForTest(t, e.f.s, e.f.path, func(query string) {
			if query == "COMMIT" {
				sawCommit = true
				rotationVisibility(t, e, prior, i)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if ok, err := observer.ObserveCursorRotation(ctx, e.b, row, next, row.JSONLPath); err != nil || ok {
					t.Fatalf("uncommitted publication observed: %v %v", ok, err)
				}
			}
		})
		ok, err := e.f.s.RotateCursorSession(row, gate, next, row.JSONLPath, time.Now().Add(time.Second), func(got store.Spawn) error { rotationVisibility(t, e, prior, i); return nil })
		if err != nil || !ok || !sawCommit {
			t.Fatalf("rotation %v %v commit=%v", ok, err, sawCommit)
		}
		rotationVisibility(t, e, next, i+1)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		observed, observeErr := observer.ObserveCursorRotation(ctx, e.b, row, next, row.JSONLPath)
		cancel()
		if observeErr != nil || !observed {
			t.Fatalf("committed atomic snapshot unavailable: %v %v", observed, observeErr)
		}
		committed, unchanged, reconcileErr = observer.ReconcileCursorRotation(context.Background(), e.b, row, next, row.JSONLPath)
		if reconcileErr != nil || !committed || unchanged {
			t.Fatalf("fresh publication not exclusively committed: committed=%v unchanged=%v err=%v", committed, unchanged, reconcileErr)
		}
		oldGate := gate
		before := hgColumns(t, e.f, row.InstanceID)
		history := hgHistory(t, e.f, row.InstanceID)
		if ok, err := e.f.s.RotateCursorSession(row, oldGate, next, row.JSONLPath, time.Now().Add(time.Second), cursorTrailCheck); err == nil || ok {
			t.Fatalf("duplicate publication accepted %v %v", ok, err)
		}
		if !reflect.DeepEqual(before, hgColumns(t, e.f, row.InstanceID)) || !reflect.DeepEqual(history, hgHistory(t, e.f, row.InstanceID)) {
			t.Fatal("duplicate rotation changed row/history")
		}
		e.b.SessionID = next
		gate.SessionID = next
		row, err = e.f.s.GetSpawn(row.InstanceID)
		if err != nil {
			t.Fatal(err)
		}
	}
	history := hgHistory(t, e.f, row.InstanceID)
	for _, h := range history {
		if h.JSONLPath.String != "/protected/launch.jsonl" || h.LifeNumber != e.b.Life {
			t.Fatalf("lost outgoing reference/life: %+v", h)
		}
	}
	// Reusing any prior identity is refused, not merely reusing the immediate predecessor.
	before := hgColumns(t, e.f, row.InstanceID)
	if ok, err := e.f.s.RotateCursorSession(row, gate, "trail-session", row.JSONLPath, time.Now().Add(time.Second), cursorTrailCheck); err == nil || ok {
		t.Fatalf("historical identity reused %v %v", ok, err)
	}
	if !reflect.DeepEqual(before, hgColumns(t, e.f, row.InstanceID)) {
		t.Fatal("reused identity mutated row")
	}
}

func TestCursorRotationRequiredArchiveAndLaterWritesRollbackTogether(t *testing.T) {
	for _, failure := range []string{"archive", "runtime update", "commit"} {
		t.Run(failure, func(t *testing.T) {
			e, row, gate := rotationStore(t)
			switch failure {
			case "archive":
				storefix.InjectWriteFailure(t, e.f.path, storefix.WriteFailReuseArchive, row.InstanceID)
			case "runtime update":
				e.exec(t, `CREATE TRIGGER fail_rotation_runtime BEFORE UPDATE OF bound_session_id ON cursor_runtimes BEGIN SELECT RAISE(ABORT,'synthetic runtime write failure'); END`)
			case "commit":
				e.exec(t, `CREATE TABLE rotation_commit_parent(id INTEGER PRIMARY KEY)`)
				e.exec(t, `CREATE TABLE rotation_commit_child(id INTEGER REFERENCES rotation_commit_parent(id) DEFERRABLE INITIALLY DEFERRED)`)
				e.exec(t, `CREATE TRIGGER fail_rotation_commit AFTER UPDATE OF bound_session_id ON cursor_runtimes BEGIN INSERT INTO rotation_commit_child VALUES(1); END`)
			}
			before := hgColumns(t, e.f, row.InstanceID)
			b := e.b
			history := hgHistory(t, e.f, row.InstanceID)
			ok, err := e.f.s.RotateCursorSession(row, gate, "fresh-session", row.JSONLPath, time.Now().Add(time.Second), cursorTrailCheck)
			if err == nil || ok {
				t.Fatalf("%s falsely succeeded %v %v", failure, ok, err)
			}
			t.Logf("causal %s refusal: %v", failure, err)
			got, e2 := e.f.s.ReadCursorRuntime(row.InstanceID)
			if e2 != nil || got != b || !reflect.DeepEqual(before, hgColumns(t, e.f, row.InstanceID)) || !reflect.DeepEqual(history, hgHistory(t, e.f, row.InstanceID)) {
				t.Fatalf("failed rotation left partial publication: binding=%+v %v", got, e2)
			}
			rotationVisibility(t, e, b.SessionID, 0)
		})
	}
}

func TestCursorRotationRejectsChangedExaminedAuthority(t *testing.T) {
	cases := []struct{ name, query string }{
		{"launch", `UPDATE spawns SET launch_token='2222222222222222'`},
		{"life", `UPDATE spawns SET life_number=life_number+1`},
		{"pane", `UPDATE spawns SET pane_pid=pane_pid+1`},
		{"pane start", `UPDATE spawns SET pane_starttime='replacement'`},
		{"server", `UPDATE spawns SET tmux_server_pid=COALESCE(tmux_server_pid,0)+1`},
		{"socket", `UPDATE spawns SET tmux_socket='/other/socket'`},
		{"snapshot", `UPDATE spawns SET row_version=row_version+1`},
		{"snapshot process", `UPDATE spawns SET pid=COALESCE(pid,0)+1`},
		{"transcript", `UPDATE spawns SET jsonl_path='/protected/replacement.jsonl'`},
		{"old session", `UPDATE spawns SET session_id='replacement'`},
		{"retired", `UPDATE cursor_runtimes SET bound_terminated=1`},
		{"runtime pid", `UPDATE cursor_runtimes SET bound_runtime_pid=bound_runtime_pid+1`},
		{"runtime start", `UPDATE cursor_runtimes SET bound_runtime_start='replacement'`},
		{"connection", `UPDATE cursor_runtimes SET bound_connection_id='replacement'`},
		{"input pipe", `UPDATE cursor_runtimes SET bound_input_pipe='replacement'`},
		{"output pipe", `UPDATE cursor_runtimes SET bound_output_pipe='replacement'`},
		{"input descriptor", `UPDATE cursor_runtimes SET bound_input_fd=bound_input_fd+1`},
		{"output descriptor", `UPDATE cursor_runtimes SET bound_output_fd=bound_output_fd+1`},
		{"deletion", `DELETE FROM spawns`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, row, g := rotationStore(t)
			e.exec(t, tc.query)
			committed, unchanged, reconcileErr := e.f.s.ReconcileCursorRotation(context.Background(), e.b, row, "fresh-session", row.JSONLPath)
			if reconcileErr != nil || committed || unchanged {
				t.Fatalf("changed outgoing evidence authorized reconciliation: committed=%v unchanged=%v err=%v", committed, unchanged, reconcileErr)
			}
			var replacement any
			if tc.name != "deletion" {
				replacement = hgColumns(t, e.f, row.InstanceID)
			}
			// Preserve the replacement exactly; no stale publisher may acquire it.
			db, err := sql.Open("sqlite", e.f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var version int
			err = db.QueryRow(`SELECT COUNT(*) FROM session_history`).Scan(&version)
			if err != nil {
				t.Fatal(err)
			}
			ok, err := e.f.s.RotateCursorSession(row, g, "fresh-session", row.JSONLPath, time.Now().Add(time.Second), cursorTrailCheck)
			if err == nil || ok {
				t.Fatalf("changed %s accepted %v %v", tc.name, ok, err)
			}
			var after int
			if err := db.QueryRow(`SELECT COUNT(*) FROM session_history`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if after != version {
				t.Fatal("stale publisher fabricated archive")
			}
			if tc.name != "deletion" {
				got, _ := e.f.s.GetSpawn(row.InstanceID)
				if !reflect.DeepEqual(replacement, hgColumns(t, e.f, row.InstanceID)) || got.SessionID == "fresh-session" {
					t.Fatal("stale publisher changed replacement session")
				}
			}
		})
	}
}
func TestCursorRotationIndependentEvidenceAndGateAreMandatory(t *testing.T) {
	for _, tc := range []string{"nil verifier", "kernel lost", "wrong parent", "parent reused", "expired after evidence", "wrong path", "malformed identity"} {
		t.Run(tc, func(t *testing.T) {
			e, row, g := rotationStore(t)
			check := store.CursorCheck(cursorTrailCheck)
			until := time.Now().Add(time.Second)
			path, next := row.JSONLPath, "fresh-session"
			switch tc {
			case "nil verifier":
				check = nil
			case "kernel lost":
				check = func(store.Spawn) error { return errors.New("kernel no longer owns process/pipes") }
			case "wrong parent":
				g.ParentPID++
			case "parent reused":
				g.ParentStart += "other"
			case "expired after evidence":
				until = time.Now().Add(20 * time.Millisecond)
				check = func(store.Spawn) error { time.Sleep(time.Until(until) + time.Millisecond); return nil }
			case "wrong path":
				path += "replacement"
			case "malformed identity":
				next = " spaced "
			}
			ok, err := e.f.s.RotateCursorSession(row, g, next, path, until, check)
			if err == nil || ok {
				t.Fatalf("%s accepted %v %v", tc, ok, err)
			}
			// Expiry can return while the sole lease is still being discarded.
			// Finish that lifecycle before the independent zero-wait SQL snapshot.
			if _, err := e.f.s.GetSpawnWithin(row.InstanceID, time.Second); err != nil {
				t.Fatalf("post-refusal cleanup/read: %v", err)
			}
			rotationVisibility(t, e, e.b.SessionID, 0)
		})
	}
}
func TestCursorRotationPreservesIndependentUnknownPermissionAndPlanHolds(t *testing.T) {
	e, row, g := rotationStore(t)
	for i, tool := range []string{"Unknown", "Plan"} {
		token := fmt.Sprintf("aaaaaaaa-aaaa-4aaa-aaaa-%012d", i)
		r := e.requestInput(token)
		r.ToolName = tool
		if err := e.f.s.InsertCursorRequest(e.b, r, 0, e.until, store.DefaultLockWait, cursorTrailCheck); err != nil {
			t.Fatal(err)
		}
	}
	row, err := e.f.s.GetSpawn(row.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := e.f.s.RotateCursorSession(row, g, "fresh-session", row.JSONLPath, time.Now().Add(time.Second), cursorTrailCheck); err != nil || !ok {
		t.Fatalf("rotation %v %v", ok, err)
	}
	e.b.SessionID = "fresh-session"
	after, _ := e.f.s.GetSpawn(row.InstanceID)
	if after.State != store.StateCheckPermission {
		t.Fatalf("rotation erased holds: %+v", after)
	}
	requests, err := e.f.s.PermissionRequestsForSpawn(row.InstanceID)
	if err != nil || len(requests) != 2 {
		t.Fatalf("independent requests %v %v", requests, err)
	}
	for _, r := range requests {
		if r.ProvenGone() || r.MainTurnEndEligible || r.AgentID != "" {
			t.Fatalf("rotation invented proof: %+v", r)
		}
	}
	if ok, err := e.f.s.CheckCursorPrompt(e.b, time.Now().Add(time.Second), cursorTrailCheck); err != nil || ok {
		t.Fatalf("held fresh session eligible %v %v", ok, err)
	}
}

func TestCursorRotationReconciliationRejectsInconsistentOutgoingHistory(t *testing.T) {
	e, row, _ := rotationStore(t)
	e.exec(t, `INSERT INTO session_history(instance_id,session_id,life_number,jsonl_path) SELECT instance_id,session_id,life_number,jsonl_path FROM spawns`)
	committed, unchanged, err := e.f.s.ReconcileCursorRotation(context.Background(), e.b, row, "fresh-session", row.JSONLPath)
	if err != nil || committed || unchanged {
		t.Fatalf("outgoing identity with contradictory archive accepted: committed=%v unchanged=%v err=%v", committed, unchanged, err)
	}
}

func TestCursorRotationObservationRejectsIncompleteOrReplacedPublication(t *testing.T) {
	for _, tc := range []struct{ name, query string }{
		{"archive missing", `DELETE FROM session_history`},
		{"archive wrong life", `UPDATE session_history SET life_number=life_number+1`},
		{"archive wrong path", `UPDATE session_history SET jsonl_path='/replacement'`},
		{"runtime replaced", `UPDATE cursor_runtimes SET bound_connection_id='replacement'`},
		{"launch replaced", `UPDATE spawns SET launch_token='2222222222222222'`},
		{"pane replaced", `UPDATE spawns SET pane_starttime='replacement'`},
		{"row wrong path", `UPDATE spawns SET jsonl_path='/replacement'`},
		{"context cancelled", ""},
		{"context expired", ""},
		{"store closed", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, row, gate := rotationStore(t)
			if ok, err := e.f.s.RotateCursorSession(row, gate, "observed-fresh", row.JSONLPath, time.Now().Add(time.Second), cursorTrailCheck); err != nil || !ok {
				t.Fatalf("commit: %v %v", ok, err)
			}
			ctxBefore, cancelBefore := context.WithTimeout(context.Background(), time.Second)
			observed, observeErr := e.f.s.ObserveCursorRotation(ctxBefore, e.b, row, "observed-fresh", row.JSONLPath)
			cancelBefore()
			if observeErr != nil || !observed {
				t.Fatalf("baseline publication not observable: %v %v", observed, observeErr)
			}
			if tc.query != "" {
				e.exec(t, tc.query)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if tc.name == "context cancelled" {
				cancel()
			}
			if tc.name == "context expired" {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer cancel()
			}
			if tc.name == "store closed" {
				if err := e.f.s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			committed, unchanged, err := e.f.s.ReconcileCursorRotation(ctx, e.b, row, "observed-fresh", row.JSONLPath)
			failedRead := tc.name == "context cancelled" || tc.name == "context expired" || tc.name == "store closed"
			if committed || unchanged || failedRead && err == nil {
				t.Fatalf("incomplete/replaced/failed snapshot accepted: committed=%v unchanged=%v err=%v", committed, unchanged, err)
			}
			if !failedRead && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCursorRotationObservationConnectionWaitUsesCallerDeadline(t *testing.T) {
	e, row, gate := rotationStore(t)
	checked := false
	store.BeforeCursorTrailExecForTest(t, e.f.s, e.f.path, func(query string) {
		if query != "COMMIT" {
			return
		}
		// The writer owns the Store's sole connection. Observation must time out,
		// rather than wait for a connection beyond its original caller budget.
		checked = true
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		start := time.Now()
		committed, unchanged, err := e.f.s.ReconcileCursorRotation(ctx, e.b, row, "observed-fresh", row.JSONLPath)
		if committed || unchanged || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
			t.Fatalf("reconciliation acquisition ignored context: committed=%v unchanged=%v err=%v elapsed=%s", committed, unchanged, err, time.Since(start))
		}
	})
	if ok, err := e.f.s.RotateCursorSession(row, gate, "observed-fresh", row.JSONLPath, time.Now().Add(time.Second), cursorTrailCheck); err != nil || !ok || !checked {
		t.Fatalf("rotation/check: %v %v %v", ok, err, checked)
	}
}
