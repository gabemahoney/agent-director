package realtmux_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The pane verbs (read-pane, send-keys, pause) through the production pkg/api
// client on real tmux: the socket-permission and $ / \ name cases (SRD
// SR-20.7, SR-1.8, SR-3.2, SR-7.2; PRD AC-LKP-09, AC-CLS-02 pane-verb halves),
// and the pane-verb call and observation helpers pane_verbs_targets_test.go
// reuses. Panes are observed by pane id through the raw runner (idtWorld).
// A later verb that acts on the agent's pane must extend these helpers
// (paneVerbs, paneVerb, paneGoneName, waitPaneShows), not copy them.

// paneVerbs are the three pane verbs, in the order the tables run them.
var paneVerbs = []apitest.PaneVerb{apitest.PaneReadPane, apitest.PaneSendKeys, apitest.PanePause}

// paneCall is one pane-verb call's outcome: read-pane's pane text and the error.
type paneCall struct {
	Pane string
	Err  error
}

// paneVerb opens the production client (open) and calls verb on instanceID:
// read-pane; send-keys of text with allow_pending (a live row ignores it); or
// pause, typing /exit, with a cancelled context so its wait ends at once:
// that wait's context.Canceled means /exit and Enter went through, and is
// returned as success.
func (f *killFix) paneVerb(t testing.TB, verb apitest.PaneVerb, instanceID, text string) paneCall {
	t.Helper()
	c := f.open(t, 0, "")
	defer c.Close() //nolint:errcheck
	switch verb {
	case apitest.PaneReadPane:
		res, err := c.ReadPane(api.ReadPaneParams{ClaudeInstanceID: instanceID})
		return paneCall{Pane: res.Pane, Err: err}
	case apitest.PaneSendKeys:
		_, err := c.SendKeys(api.SendKeysParams{ClaudeInstanceID: instanceID, Text: text, AllowPending: true})
		return paneCall{Err: err}
	case apitest.PanePause:
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := c.Pause(ctx, api.PauseParams{ClaudeInstanceID: instanceID})
		if errors.Is(err, context.Canceled) {
			err = nil
		}
		return paneCall{Err: err}
	}
	t.Fatalf("unknown pane verb %q", verb)
	return paneCall{}
}

// paneGoneName is verb's gone error (SR-7.2): ErrTmuxCaptureFailed for
// read-pane, ErrTmuxSendKeys for send-keys and pause.
func paneGoneName(verb apitest.PaneVerb) string {
	if verb == apitest.PaneReadPane {
		return "ErrTmuxCaptureFailed"
	}
	return "ErrTmuxSendKeys"
}

// paneMarker returns a fresh text to type into a pane.
func paneMarker(t testing.TB) string { return "pv-" + newToken(t)[:8] }

// waitPaneShows waits until the raw capture of paneID shows text.
func waitPaneShows(t testing.TB, rt *realTmux, paneID, text string) {
	t.Helper()
	var last string
	waitFor(t, fmt.Sprintf("pane %s shows %q", paneID, text), func() bool {
		last = rt.capture(t, paneID)
		return strings.Contains(last, text)
	}, func() string { return fmt.Sprintf("capture %q", redact(last)) })
}

// TestPaneVerbSocketDeniedIsTmuxNotAvailable: with a waiting row's socket at
// mode 000, each pane verb is ErrTmuxNotAvailable naming the socket; nothing changes.
func TestPaneVerbSocketDeniedIsTmuxNotAvailable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: root's access ignores the socket's mode 000, so tmux would still connect")
	}
	for _, verb := range paneVerbs {
		t.Run(string(verb), func(t *testing.T) {
			f := newKillFix(t)
			r := f.liveRow(t, killRowSpec{})
			world := idtWorld(t, f.realTmux)
			chmodSocket(t, f.Socket, 0o000)

			call := f.paneVerb(t, verb, r.InstanceID, paneMarker(t))
			assertVerbError(t, string(verb), call.Err, "ErrTmuxNotAvailable", apitest.DescSocketPermission(f.Socket), r.Token, f.StoreID)
			chmodSocket(t, f.Socket, 0o600)
			f.assertSession(t, r.Reply.SessionID, true)
			assertProcs(t, false, r.Reply.PanePID)
			idtSame(t, "the refused "+string(verb), world, idtWorld(t, f.realTmux), idtPaneIDs(world)...)
			f.assertRowUnchanged(t, r.InstanceID, r.Before)
		})
	}
}

// TestPaneVerbDollarAndBackslashNames: read-pane reads and send-keys types
// into each $ or \ name's own pane only; the session whose id a$1 spells is untouched.
func TestPaneVerbDollarAndBackslashNames(t *testing.T) {
	for _, n := range dollarBackslashNames(t) {
		t.Run(n.Raw, func(t *testing.T) {
			f := newKillFix(t)
			r, others := f.namedRow(t, n)
			seen := idtSendAndSee(t, f.realTmux, f.Client, r.Reply.PaneID)

			read := f.paneVerb(t, apitest.PaneReadPane, r.InstanceID, "")
			if read.Err != nil || !strings.Contains(read.Pane, seen) {
				t.Errorf("read-pane = %q, %s; want the row's pane showing %q", redact(read.Pane), describe(read.Err), seen)
			}
			marker := paneMarker(t)
			if err := f.paneVerb(t, apitest.PaneSendKeys, r.InstanceID, marker).Err; err != nil {
				t.Fatalf("send-keys: %s", describe(err))
			}
			waitPaneShows(t, f.realTmux, r.Reply.PaneID, marker)
			idtSame(t, "read-pane and send-keys", others, idtWorld(t, f.realTmux), idtPaneIDs(others)...)
			f.assertRowUnchanged(t, r.InstanceID, r.Before)
		})
	}
}
