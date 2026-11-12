//go:build linux

package realtmux_test

// advice_follow_cursor_gateway_linux_test.go (b.bqd E18): send-keys' advice
// after a Cursor gateway refused the call's text, followed literally against
// the real gateway (cursor_gateway_refusal_linux_test.go's gatewayHold).

import (
	"context"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// TestAdviceFollow_E18_GatewayRefusedKillResume: E18 "read-pane and get, then
// answer or clear what holds the input before sending again, or kill and
// resume the row" after gateway_refused. The hold is the decided request with
// its turn_end proof cleared (gatewayHold.hold), since a turn's end proves it
// gone (b.7qb). The pane shows the refusal and get the decided request holding
// the input, which nothing answers or clears, so sending again is refused
// alike; after kill (find-missing until the row is ended or missing, as
// docs/caller-contract.md says) and resume, the next send reaches Cursor.
func TestAdviceFollow_E18_GatewayRefusedKillResume(t *testing.T) {
	// Serial: the shared real-pane fixture changes HOME, PATH and tmux environment.
	h := newGatewayHold(t, "on", "execute", "deny", nil)
	h.hold(t)
	c, id := h.f.API, h.launch.ID
	const text = "after the refusal"
	e := h.followUp(t, text)
	h.assertRefused(t, e, text)
	if advice := "read-pane and get, then answer or clear what holds the input before sending again, or kill and resume the row"; !strings.Contains(e.Error(), advice) {
		t.Errorf("description %q\nwant it to carry %q", e.Error(), advice)
	}

	waitFor(t, "gateway refusal visible in pane", func() bool {
		pane, err := c.ReadPane(api.ReadPaneParams{InstanceID: id, NLines: 100})
		// The 80-column pane wraps the refusal line.
		return err == nil && strings.Contains(strings.ReplaceAll(pane.Pane, "\n", ""), "Cursor input refused while starting or busy (starting, held, stale or cancellation); nothing sent")
	}, h.observe(t))
	row, err := c.Get(id)
	if err != nil || len(row.UnprovenRequests) != 1 || row.UnprovenRequests[0].RequestToken != h.req.RequestToken {
		t.Fatalf("get = %+v, %v; want the decided request %s holding the input", row.UnprovenRequests, err, h.req.RequestToken)
	}
	h.assertRefused(t, h.followUp(t, text), text)

	if _, err := c.Kill(api.KillParams{InstanceID: id}); err != nil {
		t.Fatalf("kill: %v", err)
	}
	waitFor(t, "row ended or missing after find-missing", func() bool {
		if _, err := c.FindMissing(context.Background()); err != nil {
			t.Fatalf("find-missing: %v", err)
		}
		row, err := c.Get(id)
		return err == nil && (row.State == store.StateEnded || row.State == store.StateMissing)
	}, h.observe(t))
	h.peer.Configure(t, cursorfix.Scenario{LoadSupport: true})
	if _, err := c.Resume(api.ResumeParams{InstanceID: id}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	waitFor(t, "load confirmed in pane", func() bool {
		pane, err := c.ReadPane(api.ReadPaneParams{InstanceID: id, NLines: 100})
		return err == nil && strings.Contains(pane.Pane, "Cursor conversation loaded; awaiting a prompt")
	}, h.observe(t))
	// A loaded row stays pending until its first prompt, which allow_pending permits.
	if _, err := c.SendKeys(api.SendKeysParams{InstanceID: id, Text: text, AllowPending: true}); err != nil {
		t.Fatalf("send-keys after kill and resume: %v; want success", err)
	}
	waitFor(t, "send after resume reaches Cursor and completes", func() bool {
		return h.prompted(t, text) && readRow(t, h.f.DBPath, id).State == store.StateWaiting
	}, h.observe(t))
}
