package hook_test

// timeout_writes_test.go — the relay hook writes its answer only after its ack
// has committed (b.146 rule 3; it replaces the b.uer DB-before-stdout order):
// at the first byte on stdout the request already reads acked, with the
// verdict printed. The timeout deny is one statement (deny, reason timeout and
// the ack together) and moves the row nowhere: it stays check_permission until
// the agent's next hook.

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
)

// ackWriter records, at the first byte written to it, the request read reads.
type ackWriter struct {
	buf     bytes.Buffer
	read    func() store.PermissionRow
	atFirst *store.PermissionRow
}

func (w *ackWriter) Write(p []byte) (int, error) {
	if w.atFirst == nil {
		r := w.read()
		w.atFirst = &r
	}
	return w.buf.Write(p)
}

// TestRelayAnswerOnlyAfterItsAck: for a verdict decide recorded and for the
// hook's own timeout deny, the request is acked (delivered_at) with the printed
// decision when the first stdout byte is written; the row stays
// check_permission.
func TestRelayAnswerOnlyAfterItsAck(t *testing.T) {
	cases := []struct {
		name     string
		verdict  string // recorded at the poll's first sleep; "" = none, the timeout deny's case
		decision string // the request's
		reason   string
		printed  string // the envelope's message reason
	}{
		{"allow verdict", "allow", "allow", "", ""},
		{"deny verdict", "deny", "deny", store.DecisionReasonOperator, store.DecisionReasonOperator},
		{"timeout deny", "", "deny", store.DecisionReasonTimeout, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "ack-first-" + strings.ReplaceAll(tc.name, " ", "-")
			st, _ := seedAgentRow(t, id, store.StateWorking)
			hc := relayHC(id, agentParent(t, st, id), 10*time.Second)
			if tc.verdict != "" {
				atSleep(t, hc, 1, func() { decideOnly(t, st, id, tc.verdict) })
			}
			w := &ackWriter{read: func() store.PermissionRow { return onlyRequest(t, st, id) }}

			if err := hook.Handle(context.Background(), strings.NewReader(relayPayload), w, st, hc, newSilentLogger()); err != nil {
				t.Fatalf("Handle: %v", err)
			}

			if w.atFirst == nil {
				t.Fatal("the hook printed nothing; want its acked answer")
			}
			if got := *w.atFirst; got.DeliveredAt.IsZero() || got.Decision != tc.decision || got.DecisionReason != tc.reason {
				t.Errorf("request at the first stdout byte = decision %q reason %q delivered_at %v; want %q, %q, acked",
					got.Decision, got.DecisionReason, got.DeliveredAt, tc.decision, tc.reason)
			}
			if want := hook.EncodeDecision(hook.EventNamePermissionRequest, tc.decision, tc.printed) + "\n"; w.buf.String() != want {
				t.Errorf("stdout = %q; want %q", w.buf.String(), want)
			}
			if got := mustGetSpawn(t, st, id).State; got != store.StateCheckPermission {
				t.Errorf("state = %q; want check_permission until the agent's next hook", got)
			}
		})
	}
}
