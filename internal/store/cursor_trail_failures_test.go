package store_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/trail"
)

func TestCursorTrailAdmissionRefusalIsSilent(t *testing.T) {
	for _, refusal := range []string{"collision after state update", "stale binding", "deadline", "invalid request"} {
		t.Run(refusal, func(t *testing.T) {
			e := newCursorTrailEnv(t)
			e.insert(t, "target")
			b, until, req := e.b, e.until, e.requestInput("new")
			switch refusal {
			case "collision after state update":
				req.RequestToken = "target"
			case "stale binding":
				b.Connection += "stale"
			case "deadline":
				until = time.Now().Add(-time.Second)
			case "invalid request":
				req.ToolUseID = ""
			}
			before, err := e.f.s.GetSpawn(b.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			mark := store.TrailMark(t)
			if err := e.f.s.InsertCursorRequest(b, req, 0, until, store.DefaultLockWait, cursorTrailCheck); err == nil {
				t.Fatal("refused admission succeeded")
			}
			after, err := e.f.s.GetSpawn(b.InstanceID)
			rows, rowsErr := e.f.s.PermissionRequestsForSpawn(b.InstanceID)
			if err != nil || rowsErr != nil || len(rows) != 1 || !reflect.DeepEqual(before, after) || len(e.events(t, mark)) != 0 || len(store.TrailEventsSince(t, mark, "ad.spawn.state_transition", b.InstanceID)) != 0 {
				t.Fatalf("refused admission changed state or emitted: %v %v", err, rowsErr)
			}
		})
	}
}

func TestCursorTrailNativeRefusalIsSilent(t *testing.T) {
	for _, refusal := range []string{"mismatch", "intent", "outside", "stale binding", "deadline", "missing", "delivered", "closed", "proven"} {
		t.Run(refusal, func(t *testing.T) {
			e := newCursorTrailEnv(t)
			e.insert(t, "target")
			b, until, token := e.b, e.until, "target"
			switch refusal {
			case "mismatch":
				e.sent(t, token, "deny", true)
			case "intent":
				e.sent(t, token, "allow", false)
			case "outside":
				if ok, err := e.f.s.RecordPaneOutside(b.InstanceID, token, "unknown", store.DefaultLockWait, nil); err != nil || !ok {
					t.Fatalf("outside: %v %v", ok, err)
				}
			case "stale binding":
				b.Connection += "stale"
			case "deadline":
				until = time.Now().Add(-time.Second)
			case "missing":
				token = "missing"
			case "delivered":
				e.exec(t, `UPDATE permission_requests SET delivered_at=1 WHERE request_token='target'`)
			case "closed":
				e.exec(t, `UPDATE permission_requests SET closed_at=1 WHERE request_token='target'`)
			case "proven":
				if err := e.f.s.ProveCursorToolRan(b, token, "tool-target", true, cursorTrailCheck); err != nil {
					t.Fatal(err)
				}
			}
			before, mark := e.read(t, "target"), store.TrailMark(t)
			ok, err := e.f.s.ClaimCursorPaneAnswer(b, token, "allow", until, store.DefaultLockWait, cursorTrailCheck)
			wantErr := refusal == "stale binding" || refusal == "deadline"
			if ok || (err != nil) != wantErr || len(e.events(t, mark)) != 0 || !reflect.DeepEqual(before, e.read(t, "target")) {
				t.Fatalf("refused claim changed row/emitted: %v %v", ok, err)
			}
		})
	}
}

func TestCursorTrailProofRefusalIsSilent(t *testing.T) {
	for _, refusal := range []string{"wrong token", "wrong tool", "independent token tool mismatch", "closed", "stale binding"} {
		t.Run(refusal, func(t *testing.T) {
			e := newCursorTrailEnv(t)
			e.insert(t, "target")
			e.insert(t, "independent")
			b, token, tool := e.b, "target", "tool-target"
			switch refusal {
			case "wrong token":
				token = "missing"
			case "wrong tool":
				tool = "missing"
			case "independent token tool mismatch":
				token = "independent"
			case "closed":
				e.exec(t, `UPDATE permission_requests SET closed_at=1 WHERE request_token='target'`)
			case "stale binding":
				b.Connection += "stale"
			}
			before, other, mark := e.read(t, "target"), e.read(t, "independent"), store.TrailMark(t)
			err := e.f.s.ProveCursorToolRan(b, token, tool, true, cursorTrailCheck)
			if (err != nil) != (refusal == "stale binding") || len(e.events(t, mark)) != 0 || !reflect.DeepEqual(before, e.read(t, "target")) || !reflect.DeepEqual(other, e.read(t, "independent")) {
				t.Fatalf("refused proof changed row/emitted: %v", err)
			}
		})
	}
}

// The update succeeds, but its deferred constraint is checked only at COMMIT.
func (e *cursorTrailEnv) rejectCommitOnUpdate(t *testing.T) {
	t.Helper()
	e.exec(t, `CREATE TABLE trail_parent(id INTEGER PRIMARY KEY)`)
	e.exec(t, `CREATE TABLE trail_commit_failure(id INTEGER REFERENCES trail_parent(id) DEFERRABLE INITIALLY DEFERRED)`)
	e.exec(t, `CREATE TRIGGER trail_deferred_failure AFTER UPDATE ON permission_requests BEGIN INSERT INTO trail_commit_failure VALUES(999); END`)
}

func TestCursorTrailRollbackIsSilent(t *testing.T) {
	for _, operation := range []string{"native", "proof"} {
		for _, failure := range []string{"update", "commit"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				e := newCursorTrailEnv(t)
				e.insert(t, "target")
				if failure == "update" {
					storefix.InjectWriteFailure(t, e.f.path, storefix.WriteFailPermissionDecision, e.b.InstanceID)
				} else {
					e.rejectCommitOnUpdate(t)
				}
				before, mark := e.read(t, "target"), store.TrailMark(t)
				seen := e.beforeCommit(t, mark)
				var err error
				if operation == "native" {
					var ok bool
					ok, err = e.f.s.ClaimCursorPaneAnswer(e.b, "target", "allow", e.until, store.DefaultLockWait, cursorTrailCheck)
					if ok {
						t.Fatal("rollback returned claimed")
					}
				} else {
					err = e.f.s.ProveCursorToolRan(e.b, "target", "tool-target", true, cursorTrailCheck)
				}
				if err == nil || (failure == "commit" && (!*seen || !strings.Contains(err.Error(), "commit"))) || len(e.events(t, mark)) != 0 || !reflect.DeepEqual(before, e.read(t, "target")) {
					t.Fatalf("failed %s changed row/emitted or missed commit failure: %v", failure, err)
				}
			})
		}
	}
}

func TestCursorTrailFailureDoesNotUndoCommit(t *testing.T) {
	if os.Getenv("AD_CURSOR_TRAIL_FAILURE_CHILD") == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCursorTrailFailureDoesNotUndoCommit$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), "AD_CURSOR_TRAIL_FAILURE_CHILD=1")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: TestCursorTrailFailureDoesNotUndoCommit") {
			t.Fatalf("child: %v\n%s", err, out)
		}
		return
	}
	// Use the established isolated subprocess and real unwritable-directory fault.
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "agent-director"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	if err := trail.Emit(context.Background(), "ad.test.cursor_trail_probe", nil); err == nil {
		t.Fatal("trail fault did not take effect")
	}
	e := newCursorTrailEnv(t)
	e.insert(t, "native")
	e.insert(t, "proof")
	if ok, err := e.f.s.ClaimCursorPaneAnswer(e.b, "native", "deny", e.until, store.DefaultLockWait, cursorTrailCheck); err != nil || !ok {
		t.Fatalf("trail failure failed native write: %v %v", ok, err)
	}
	if r := e.read(t, "native"); r.Decision != "deny" || r.DecisionReason != "pane" || r.PaneAnswer != "sent" {
		t.Fatalf("native commit lost: %+v", r)
	}
	if err := e.f.s.ProveCursorToolRan(e.b, "proof", "tool-proof", true, cursorTrailCheck); err != nil {
		t.Fatal(err)
	}
	if r := e.read(t, "proof"); !r.ProvenGone() || r.Decision != "allow" || r.DecisionReason != "tool_ran" {
		t.Fatalf("proof commit lost: %+v", r)
	}
	if err := trail.Emit(context.Background(), "ad.test.cursor_trail_probe", nil); err == nil {
		t.Fatal("trail fault stopped applying")
	}
}
