//go:build linux

package realtmux_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// gatewayHold is a live Cursor launch (b.bqd) whose first turn raised one
// request, req, that the turn's end proved gone (b.7qb); hold makes req hold
// the launch again.
type gatewayHold struct {
	f      *spawnFix
	peer   cursorfix.Runtime
	launch spawned
	relay  string
	req    store.PermissionRow
}

// newGatewayHold launches a relay ("on" or "off") Cursor row whose first turn
// raises one kind request, decided with decision (relay on) or rejected at the
// pane (relay off), with after sent once it is answered; it returns once the
// turn ended and the row is waiting, the peer answering later prompts.
func newGatewayHold(t *testing.T, relay, kind, decision string, after []json.RawMessage) gatewayHold {
	t.Helper()
	const session = "bqd-session"
	script := cursorfix.Scenario{SessionID: session, Requests: []json.RawMessage{relayFrame("bqd-req", "session/request_permission", cursorfix.Permission(session, "bqd-tool", kind))}, AfterAnswers: after}
	f, peer, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{Script: &script, Relay: relay, Timeout: 30})
	h := gatewayHold{f: f, peer: peer, launch: launch, relay: relay}
	if _, e := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "first turn"}); e != nil {
		t.Fatal(e)
	}
	h.req = relayRequests(t, f, launch.ID, 1)[0]
	if relay == "on" {
		if _, e := f.API.Decide(api.DecideParams{InstanceID: launch.ID, RequestToken: h.req.RequestToken, Decision: decision}); e != nil {
			t.Fatal(e)
		}
	} else {
		// A person rejects at the pane: the fixture's third option.
		launch.rt.must(t, "send-keys", "-t", launch.Row.PaneID.(string), "-l", "--", "3")
	}
	transcript := readRow(t, f.DBPath, launch.ID).JSONLPath.(string)
	waitFor(t, "turn ends to waiting", func() bool {
		raw, e := os.ReadFile(transcript)
		return e == nil && strings.Contains(string(raw), `"kind":"turn_outcome"`) && readRow(t, f.DBPath, launch.ID).State == store.StateWaiting
	}, h.observe(t))
	peer.Configure(t, cursorfix.Scenario{SessionID: session})
	return h
}

// hold clears req's turn_end proof in the store, leaving its decision,
// delivery and pane answer, so req is again an unproven request of the
// current launch, as a turn's end left an answered one before b.7qb. Nothing
// proves it gone again while the launch lives, so the gateway refuses every
// later prompt and plain send-keys reports the dialog may be open.
func (h gatewayHold) hold(t *testing.T) {
	t.Helper()
	db, e := sql.Open("sqlite", h.f.DBPath)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	res, e := db.Exec("UPDATE permission_requests SET proven_gone_at=NULL,proven_gone_how=NULL WHERE instance_id=? AND request_token=? AND proven_gone_how='turn_end'", h.launch.ID, h.req.RequestToken)
	if e != nil {
		t.Fatal(e)
	}
	if n, e := res.RowsAffected(); e != nil || n != 1 {
		t.Fatalf("hold: cleared %d turn_end proofs (%v); want req's one; %s", n, e, h.observe(t)())
	}
}

// observe reports the row, its requests and its pane for a failed wait.
func (h gatewayHold) observe(t *testing.T) func() string {
	return func() string {
		p, e := h.f.API.ReadPane(api.ReadPaneParams{InstanceID: h.launch.ID, NLines: 100})
		return fmt.Sprintf("row=%+v requests=%+v pane=%q err=%v", readRow(t, h.f.DBPath, h.launch.ID), relayRequests(t, h.f, h.launch.ID, 1), p.Pane, e)
	}
}

// prompted reports whether a session/prompt carrying text reached the peer.
func (h gatewayHold) prompted(t *testing.T, text string) bool {
	for _, prompt := range panePrompts(t, h.peer) {
		if prompt.Text == text {
			return true
		}
	}
	return false
}

// followUp sends text as a caller past the relay guard's hold does: with an
// inspected pane's hash when relay is on, again while the pane changed in
// between.
func (h gatewayHold) followUp(t *testing.T, text string) error {
	params := api.SendKeysParams{InstanceID: h.launch.ID, Text: text}
	var e error
	waitFor(t, "send-keys on an unchanged pane", func() bool {
		if h.relay == "on" {
			// Having read the pane, the caller's hash passes the relay guard's hold.
			p, err := h.f.API.ReadPane(api.ReadPaneParams{InstanceID: h.launch.ID, NLines: 100})
			if err != nil {
				t.Fatal(err)
			}
			params.ExpectPaneSHA256, params.NLines = p.PaneSHA256, 100
		}
		_, e = h.f.API.SendKeys(params)
		return !errors.Is(e, api.ErrPaneChanged)
	}, h.observe(t))
	return e
}

// assertRefused fails unless send-keys of text returned e, gateway_refused,
// and text never reached the peer.
func (h gatewayHold) assertRefused(t *testing.T, e error, text string) {
	t.Helper()
	d, ok := api.ErrDetails(e).(api.SubmissionUnconfirmedDetails)
	if !errors.Is(e, api.ErrSubmissionUnconfirmed) || !ok || d.Outcome != "gateway_refused" || !d.KeysSent || d.SubmissionConfirmed {
		t.Fatalf("refused follow-up: want ErrSubmissionUnconfirmed gateway_refused, got err=%v details=%+v; %s", e, api.ErrDetails(e), h.observe(t)())
	}
	if h.prompted(t, text) {
		t.Fatal("refused follow-up reached ACP")
	}
}

// b.bqd: a live Cursor launch with a current-launch request never proven gone
// refuses typed input at its gateway; send-keys must report that refusal
// (ErrSubmissionUnconfirmed, outcome gateway_refused), never success. A turn's
// end proves its requests gone (b.7qb), so each case's hold is its answered
// request with that proof cleared (gatewayHold.hold).
func TestCursorSendKeysReportsGatewayRefusal(t *testing.T) {
	// Serial: the shared real-pane fixture changes HOME, PATH and tmux environment.
	completed := func(output string) []json.RawMessage {
		return []json.RawMessage{json.RawMessage(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"bqd-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"bqd-tool","status":"completed",` + output + `}}}`)}
	}
	for _, tc := range []struct {
		name, relay, kind, decision string
		after                       []json.RawMessage
		control                     bool
	}{
		{name: "deny-execute", relay: "on", kind: "execute", decision: "deny"},
		{name: "allow-edit-diff-only", relay: "on", kind: "edit", decision: "allow", after: completed(`"content":[{"type":"diff","path":"/tmp/bqd.txt","oldText":"a","newText":"b"}]`)},
		// Relay off runs no relay guard, so a plain send reaches the gateway.
		{name: "relay-off-pane-deny-execute", relay: "off", kind: "execute"},
		{name: "control-allow-execute-proof", relay: "on", kind: "execute", decision: "allow", control: true, after: completed(`"rawOutput":{"exitCode":0,"stdout":"ok","stderr":""}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newGatewayHold(t, tc.relay, tc.kind, tc.decision, tc.after)
			f, launch := h.f, h.launch
			const text = "follow-up"
			if tc.control {
				if _, e := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: text}); e != nil {
					t.Fatalf("control follow-up: %v", e)
				}
				waitFor(t, "control follow-up reaches ACP and completes", func() bool {
					return h.prompted(t, text) && readRow(t, f.DBPath, launch.ID).State == store.StateWaiting
				}, h.observe(t))
				return
			}
			h.hold(t)
			h.assertRefused(t, h.followUp(t, text), text)
			if tc.relay == "on" {
				// The API sees the unproven request too and refuses before the gateway.
				_, e := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: text})
				if dd, ok := api.ErrDetails(e).(api.DialogMaybeOpenDetails); !errors.Is(e, api.ErrDialogMaybeOpen) || !ok || dd.RequestToken != h.req.RequestToken {
					t.Fatalf("plain send: want ErrDialogMaybeOpen naming %s, got %v", h.req.RequestToken, e)
				}
			}
		})
	}
}
