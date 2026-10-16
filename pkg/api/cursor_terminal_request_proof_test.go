package api_test

import (
	"os/exec"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.ftd: a terminal row is not per-request evidence of hook death or Cursor receipt.
func TestCursorTerminalReadsPreserveRequestEvidence(t *testing.T) {
	// Serial: deadline-sensitive public reconciliation is isolated from the parallel fixture wave.
	for _, verb := range []string{"Get", "Status", "List"} {
		t.Run(verb, func(t *testing.T) {
			e, c, b, token := cursorReadLaunch(t)
			hook := exec.Command("sleep", "30")
			if err := hook.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { hook.Process.Kill(); hook.Wait() }()
			checker := probe.NewProcChecker()
			hookStart, alive, known := checker.StartTime(hook.Process.Pid)
			if !known || !alive || hookStart == "" {
				t.Fatal("independent hook not alive before reconciliation")
			}
			e.pc.Set(b.GatewayPID, procfix.Gone())
			e.pc.Set(b.RuntimePID, procfix.Gone())
			tokens := map[string]string{"undecided": token}
			for _, kind := range []string{"decided", "delivered", "verified"} {
				request, err := apitest.SeedPermissionRequest(e.dbPath, b.InstanceID, "Bash")
				if err != nil {
					t.Fatal(err)
				}
				tokens[kind] = request.RequestToken
			}
			at := time.Now().Add(-time.Minute).UnixMilli()
			for kind, tok := range tokens {
				cursorReadSQL(t, e.dbPath, `UPDATE permission_requests SET settled_at=?,main_turn_end_eligible=0,
				 hook_pid=?,hook_starttime=?,hook_pidns='independent-hook',attempted_decision='deny',attempted_at=?,
				 pane_sender_pid=?,pane_sender_starttime=?,pane_sender_pidns='independent-sender',pane_intent_at=?
				 WHERE instance_id=? AND request_token=?`, at+3600000, hook.Process.Pid, hookStart, at, hook.Process.Pid, hookStart, at, b.InstanceID, tok)
				if kind != "undecided" {
					cursorReadSQL(t, e.dbPath, `UPDATE permission_requests SET decision='allow',decision_reason='operator',decided_at='2026-10-15 00:00:00' WHERE instance_id=? AND request_token=?`, b.InstanceID, tok)
				}
				if kind == "delivered" {
					cursorReadSQL(t, e.dbPath, `UPDATE permission_requests SET delivered_at=? WHERE instance_id=? AND request_token=?`, at, b.InstanceID, tok)
				}
				if kind == "verified" {
					cursorReadSQL(t, e.dbPath, `UPDATE permission_requests SET proven_gone_at=?,proven_gone_how='tool_ran',pane_answer='sent',pane_as='allow' WHERE instance_id=? AND request_token=?`, at, b.InstanceID, tok)
				}
			}
			before := e.columns(t, b.InstanceID)
			requests := map[string]store.PermissionRow{}
			for kind, tok := range tokens {
				pr, err := e.st.GetPermissionRequest(b.InstanceID, tok)
				if err != nil {
					t.Fatal(err)
				}
				requests[kind] = pr
			}
			for i := 0; i < 2; i++ {
				state, err := cursorStartupRead(c, verb, b.InstanceID)
				if err != nil || state != store.StateMissing {
					t.Fatalf("%s after exact gateway death = %s/%v; want missing", verb, state, err)
				}
			}
			after := e.columns(t, b.InstanceID)
			if after.State != store.StateMissing || after.RowVersion != before.RowVersion.(int64)+1 {
				t.Errorf("missing close not atomic/idempotent: before=%+v after=%+v", before, after)
			}
			for kind, was := range requests {
				got, err := e.st.GetPermissionRequest(b.InstanceID, was.RequestToken)
				if err != nil {
					t.Fatal(err)
				}
				if got.ProvenGoneHow != was.ProvenGoneHow || !got.ProvenGoneAt.Equal(was.ProvenGoneAt) {
					t.Errorf("%s: gateway/runtime death invented request proof: before=%s/%v after=%s/%v", kind, was.ProvenGoneHow, was.ProvenGoneAt, got.ProvenGoneHow, got.ProvenGoneAt)
				}
				want := was
				if was.AwaitsAnswer() {
					if !got.Closed() {
						t.Errorf("%s request not atomically closed", kind)
					}
					want.ClosedAt = got.ClosedAt
					if was.Decision == "" {
						want.Decision, want.DecisionReason, want.DecidedAt = "deny", store.DecisionReasonFindMissing, got.DecidedAt
						if got.DecidedAt.IsZero() {
							t.Error("orphan deny lacks timestamp")
						}
					}
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s request evidence changed beyond close/deny:\n before %+v\n after %+v\n want %+v", kind, was, got, want)
				}
			}
			if start, alive, known := checker.StartTime(hook.Process.Pid); !known || !alive || start != hookStart {
				t.Error("independent request hook was killed by row reconciliation")
			}
		})
	}
}
