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
		{"live row", store.StateWaiting, `{"instance_id":"` + killMCPID + `","text":""}`},
		{"pending row with allow_pending", store.StatePending, `{"instance_id":"` + killMCPID + `","text":"","allow_pending":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t, tc.state, seedOurs)

			toolResult(t, callTool(t, e.d, "send_keys", tc.args))

			if texts := e.rec.SocketCallsOf(tmux.CallSendText); len(texts) != 0 {
				t.Errorf("text calls = %+v; want none, including no empty-text transport", texts)
			}
			if keys := e.rec.SocketCallsOf(tmux.CallSendKey); len(keys) != 1 || keys[0].Key != "Enter" || keys[0].Target != apitest.TestPaneID || keys[0].Socket != apitest.TestSocket {
				t.Errorf("key calls = %+v; want one Enter to the agent's pane %s on %s", keys, apitest.TestPaneID, apitest.TestSocket)
			}
		})
	}
}
