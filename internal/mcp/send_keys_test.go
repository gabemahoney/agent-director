package mcp_test

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestSendKeysMCPEmptyText (b.9o4): the send_keys tool takes text "" (the
// Enter-only send) on a live row and, with allow_pending, a pending row: one
// Enter to the agent's pane, nothing typed.
func TestSendKeysMCPEmptyText(t *testing.T) {
	for _, tc := range []struct{ name, state, args string }{
		{"live row", store.StateWaiting, `{"claude_instance_id":"` + killMCPID + `","text":""}`},
		{"pending row with allow_pending", store.StatePending, `{"claude_instance_id":"` + killMCPID + `","text":"","allow_pending":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t, tc.state, seedOurs)

			toolResult(t, callTool(t, e.d, "send_keys", tc.args))

			for _, c := range e.rec.SocketCallsOf(tmux.CallSendText) {
				if c.Text != "" {
					t.Errorf("text call typed %q; want nothing typed", c.Text)
				}
			}
			if enters := e.rec.SocketCallsOf(tmux.CallSendEnter); len(enters) != 1 || enters[0].Target != apitest.TestPaneID {
				t.Errorf("Enter calls = %+v; want one to the agent's pane %s", enters, apitest.TestPaneID)
			}
		})
	}
}
