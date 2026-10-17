package store_test

import (
	"database/sql"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.ftd: all Cursor callers of the shared terminal close retain unproven relay acknowledgements.
func TestCursorTerminalClosePreservesRequestProof(t *testing.T) {
	for _, path := range []string{"cursor missing", "fatal unretired", "fatal retired", "find missing", "SessionEnd", "resume backstop"} {
		t.Run(path, func(t *testing.T) {
			f, row, b := cursorRowBinding(t)
			request, err := apitest.SeedPermissionRequest(f.path, row.InstanceID, "Bash")
			if err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec(`UPDATE permission_requests SET decision='allow',decision_reason='operator',decided_at='2026-10-15 00:00:00',delivered_at=?,settled_at=? WHERE instance_id=? AND request_token=?`, time.Now().Add(-time.Minute).UnixMilli(), time.Now().Add(time.Hour).UnixMilli(), row.InstanceID, request.RequestToken); err != nil {
				t.Fatal(err)
			}
			if path == "resume backstop" {
				if _, err = db.Exec(`UPDATE spawns SET state='ended',ended_at=CURRENT_TIMESTAMP WHERE instance_id=?`, row.InstanceID); err != nil {
					t.Fatal(err)
				}
			}
			row, err = f.s.GetSpawn(row.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			before, err := f.s.GetPermissionRequest(row.InstanceID, request.RequestToken)
			if err != nil {
				t.Fatal(err)
			}
			wantState := store.StateMissing
			switch path {
			case "fatal unretired", "fatal retired":
				if path == "fatal retired" {
					if ok, err := f.s.MarkCursorRuntimeGone(b); err != nil || !ok {
						t.Fatalf("retire %v %v", ok, err)
					}
				}
				if _, result, err := f.s.MarkFailedCursorRuntimeMissing(row, b); err != nil || result != store.CondApplied {
					t.Fatalf("fatal missing: %v %v", result, err)
				}
			case "cursor missing":
				if _, result, err := f.s.MarkCursorMissingIfSameLaunch(row, b); err != nil || result != store.CondApplied {
					t.Fatalf("cursor missing: %v %v", result, err)
				}
			case "find missing":
				if _, result, err := f.s.MarkMissingIfSameLife(row.InstanceID, row.Snapshot); err != nil || result != store.CondApplied {
					t.Fatalf("find missing: %v %v", result, err)
				}
			case "SessionEnd":
				if ok, err := f.s.SetCursorRuntimeSession(b, "terminal-proof-session"); err != nil || !ok {
					t.Fatalf("session bind: %v %v", ok, err)
				}
				b.SessionID = "terminal-proof-session"
				if ok, err := f.s.MarkCursorRuntimeGone(b); err != nil || !ok {
					t.Fatalf("runtime gone: %v %v", ok, err)
				}
				b.Terminated = true
				gate := store.HookGate{Event: "SessionEnd", ParentPID: b.GatewayPID, ParentStart: b.GatewayStart, SessionID: b.SessionID, Cursor: &b, Examined: row.Snapshot}
				if _, a, err := f.s.ApplyHookTransitionResult(row.InstanceID, gate, store.StateEnded, false, "SessionEnd", "", false); err != nil || !a.Applied {
					t.Fatalf("SessionEnd: %+v %v", a, err)
				}
				wantState = store.StateEnded
			case "resume backstop":
				if result, _, err := f.s.MoveToPending(row.InstanceID, row.Snapshot, time.Now().UnixMilli(), "resumed-token", "/tmp/proof-sock", "", store.LaunchOwner{}); err != nil || result != store.CondApplied {
					t.Fatalf("resume: %v %v", result, err)
				}
				wantState = store.StatePending
			}
			afterRow, err := f.s.GetSpawn(row.InstanceID)
			if err != nil || afterRow.State != wantState || afterRow.RowVersion != row.RowVersion+1 {
				t.Fatalf("terminal row: %+v %v; want %s/version+1", afterRow, err, wantState)
			}
			after, err := f.s.GetPermissionRequest(row.InstanceID, request.RequestToken)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Errorf("%s invented proof or rewrote acknowledged request:\n before %+v\n after %+v", path, before, after)
			}
		})
	}
}
