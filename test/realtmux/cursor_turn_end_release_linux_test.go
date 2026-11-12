//go:build linux

package realtmux_test

import (
	"encoding/json"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// b.7qb: a Cursor turn's end proves every request answered in it gone
// (turn_end), keeping its decision, delivery and pane answer, so a plain
// send-keys reaches ACP without a kill.
func TestCursorTurnEndReleasesAnsweredRequests(t *testing.T) {
	// Serial: the shared real-pane fixture changes HOME, PATH and tmux environment.
	completed := func(output string) []json.RawMessage {
		return []json.RawMessage{json.RawMessage(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"bqd-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"bqd-tool","status":"completed",` + output + `}}}`)}
	}
	for _, tc := range []struct {
		name, relay, kind, decision string
		after                       []json.RawMessage
		how, paneAnswer             string
		delivered                   bool
		wantDecision                string
	}{
		{name: "deny-execute", relay: "on", kind: "execute", decision: "deny", how: "turn_end", paneAnswer: "none", delivered: true, wantDecision: "deny"},
		{name: "allow-edit-diff-only", relay: "on", kind: "edit", decision: "allow", after: completed(`"content":[{"type":"diff","path":"/tmp/bqd.txt","oldText":"a","newText":"b"}]`), how: "turn_end", paneAnswer: "none", delivered: true, wantDecision: "allow"},
		{name: "relay-off-pane-deny-execute", relay: "off", kind: "execute", how: "turn_end", paneAnswer: "sent", wantDecision: "deny"},
		{name: "control-allow-execute-proof", relay: "on", kind: "execute", decision: "allow", after: completed(`"rawOutput":{"exitCode":0,"stdout":"ok","stderr":""}`), how: "tool_ran", paneAnswer: "none", delivered: true, wantDecision: "allow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newGatewayHold(t, tc.relay, tc.kind, tc.decision, tc.after)
			f, launch := h.f, h.launch
			req := relayRequests(t, f, launch.ID, 1)[0]
			if !req.ProvenGone() || req.ProvenGoneHow != tc.how || req.Decision != tc.wantDecision || req.DeliveredAt.IsZero() == tc.delivered || req.PaneAnswer != tc.paneAnswer {
				t.Errorf("request after turn end: want proven_gone_how %q, decision %q, delivered %v, pane_answer %q; got proven_gone_at=%v how=%q decision=%q delivered_at=%v pane_answer=%q",
					tc.how, tc.wantDecision, tc.delivered, tc.paneAnswer, req.ProvenGoneAt, req.ProvenGoneHow, req.Decision, req.DeliveredAt, req.PaneAnswer)
			}
			const text = "follow-up"
			if _, e := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: text}); e != nil {
				t.Fatalf("plain follow-up after turn end: %v; %s", e, h.observe(t)())
			}
			waitFor(t, "follow-up reaches ACP and completes", func() bool {
				return h.prompted(t, text) && readRow(t, f.DBPath, launch.ID).State == store.StateWaiting
			}, h.observe(t))
		})
	}
}
