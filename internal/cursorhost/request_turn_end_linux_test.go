//go:build linux

package cursorhost

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.7qb: a turn's end proves turn_end every request of the turn the gateway
// answered, through the relay or at the pane, and keeps that answer, so the
// next prompt is admitted. One whose tool proof is still deferred is left for
// the Stop flush, and one already proven keeps tool_ran.
func TestCursorTurnEndProvesAnsweredRequests(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	cases := []struct {
		name, relay, decision string
		pane                  bool
		// proof: "" none, "deferred" a valid completion met a held write
		// lock, "ran" a valid completion was stored.
		proof string
	}{
		{"delivered deny", "on", "deny", false, ""},
		{"delivered allow without proof", "on", "allow", false, ""},
		{"pane answer sent", "off", "deny", true, ""},
		{"deferred proof", "on", "allow", false, "deferred"},
		{"already proven", "on", "allow", false, "ran"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRequestEnv(t, tc.relay, "synthetic-session")
			e.startTurn(t)
			c := e.controller(t, &observedWire{}, io.Discard)
			turnOpen := true
			c.turnOpen = func() bool { return turnOpen }
			r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
			if tc.pane {
				displayedKey(t, c, r.token, "reject-once")
				c.choose(r.token, "reject-once")
			} else {
				e.decide(t, r.token, tc.decision)
			}
			if err := continuePermission(c, r); err != nil {
				t.Fatal(err)
			}
			if err := completedWrite(t, c.protocol); err != nil {
				t.Fatal(err)
			}
			switch tc.proof {
			case "deferred":
				release := apitest.HoldWriteLock(t, e.path, time.Minute)
				if err := c.execution(executionUpdate(e.binding.SessionID, "shell", "completed", flushShellOutput)); err != nil {
					t.Fatal(err)
				}
				release()
				if r.proof == nil {
					t.Fatal("proof not deferred while another writer held the lock")
				}
			case "ran":
				if err := c.execution(executionUpdate(e.binding.SessionID, "shell", "completed", flushShellOutput)); err != nil {
					t.Fatal(err)
				}
			}
			before := e.request(t, r.token)
			if before.Decision != tc.decision || before.ProvenGone() != (tc.proof == "ran") || (tc.pane && before.PaneAnswer != store.PaneAnswerSent) || (!tc.pane && before.DeliveredAt.IsZero()) {
				t.Fatalf("answer not recorded before the turn's end: %+v", before)
			}
			turnOpen = false
			ended := c.endTurn()
			if err := ended.write(time.Time{}); err != nil {
				t.Fatal(err)
			}
			ended.settle()
			after := e.request(t, r.token)
			switch tc.proof {
			case "deferred":
				if after != before || r.proof == nil || r.turnEnded {
					t.Fatalf("turn's end took a request whose proof is deferred: before %+v after %+v", before, after)
				}
				flush := c.flushProofs()
				if err := flush.write(time.Time{}); err != nil {
					t.Fatal(err)
				}
				flush.settle()
				if got := e.request(t, r.token); got.ProvenGoneHow != store.ProvenGoneToolRan || r.proof != nil {
					t.Fatalf("Stop flush did not store the deferred proof: %+v", got)
				}
				return
			case "ran":
				if after != before || after.ProvenGoneHow != store.ProvenGoneToolRan {
					t.Fatalf("turn's end changed a proven request: before %+v after %+v", before, after)
				}
				return
			}
			want := before
			want.ProvenGoneAt, want.ProvenGoneHow = after.ProvenGoneAt, store.ProvenGoneTurnEnd
			if after.ProvenGoneAt.IsZero() || after != want {
				t.Errorf("turn's end did not prove the answered request turn_end alone: before %+v after %+v", before, after)
			}
			if ok, err := e.rep.promptEligible(); err != nil || !ok {
				t.Fatalf("next prompt refused after the turn's end: %v %v", ok, err)
			}
		})
	}
}

// b.7qb round 1: an answer still queued when its turn ends can pass the write
// loop's first guard before the turn's end proves its request turn_end. The
// store then refuses the final guard or issue, and that refusal is known
// unwritten: nothing is written, the transport stays up and the pane says the
// turn had ended. An answer already issued is still written. The turn's end
// settles while the writer is paused just after its commit, as on the session loop.
func TestCursorTurnEndRefusesQueuedAnswerWithoutFailingTransport(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	cases := []struct {
		name, relay string
		pane        bool
		// at: where the turn's end commits. "final guard" is after the write
		// loop's first guard and before its final one, "issue" after the
		// final guard and before issue, and "after issue" once the answer
		// is issued.
		at string
	}{
		{"relay answer before the final guard", "on", false, "final guard"},
		{"relay answer before issue", "on", false, "issue"},
		{"pane answer before the final guard", "off", true, "final guard"},
		{"pane answer before issue", "off", true, "issue"},
		{"relay answer after issue", "on", false, "after issue"},
		{"pane answer after issue", "off", true, "after issue"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRequestEnv(t, tc.relay, "synthetic-session")
			e.startTurn(t)
			wire := &observedWire{}
			var pane bytes.Buffer
			c := e.controller(t, wire, &pane)
			turnOpen := true
			c.turnOpen = func() bool { return turnOpen }
			r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
			release := blockProtocol(t, c.protocol)
			if tc.pane {
				displayedKey(t, c, r.token, "reject-once")
				c.choose(r.token, "reject-once")
			} else {
				e.decide(t, r.token, "allow")
			}
			if err := continuePermission(c, r); err != nil {
				t.Fatal(err)
			}
			if !r.queued || r.sent.Load() {
				t.Fatal("answer not queued behind the writer")
			}
			turnOpen = false
			ended := c.endTurn()
			if !slices.Contains(ended.tokens, r.token) {
				t.Fatalf("turn's end did not list the queued answer's request: %v", ended.tokens)
			}
			proved, guards := false, 0
			var proveErr error
			paused, resume := make(chan struct{}), make(chan struct{})
			var resumeOnce sync.Once
			resumeWriter := func() { resumeOnce.Do(func() { close(resume) }) }
			t.Cleanup(resumeWriter)
			prove := func() { proved, proveErr = true, ended.write(time.Time{}); close(paused); <-resume }
			var done <-chan error
			switch tc.at {
			case "final guard":
				select {
				case next := <-c.protocol.outgoing:
					guard := next.guard
					next.guard = func() error {
						if guards++; guards == 2 {
							prove()
						}
						return guard()
					}
					c.protocol.outgoing <- next
				case <-time.After(time.Second):
					t.Fatal("answer was not queued behind barrier")
				}
				done = observeQueuedRequest(t, c.protocol)
			case "issue":
				done = observeQueuedRequest(t, c.protocol, func(issue func() error) func() error {
					return func() error { prove(); return issue() }
				})
			case "after issue":
				done = observeQueuedRequest(t, c.protocol, func(issue func() error) func() error {
					return func() error { err := issue(); prove(); return err }
				})
			}
			release()
			select {
			case <-paused:
			case <-time.After(4 * time.Second):
				t.Fatalf("turn's end not committed at %s", tc.at)
			}
			if !proved || proveErr != nil || (tc.at == "final guard" && guards != 2) {
				t.Fatalf("turn's end not committed at %s: proved=%v guards=%d %v", tc.at, proved, guards, proveErr)
			}
			ended.settle()
			if !r.turnEnded {
				t.Fatal("turn's end did not settle the queued answer's request")
			}
			resumeWriter()
			err := requestCompletion(t, done)
			for {
				wake := nextRequestWake(t, c)
				if wake.kind != responseCompleted || wake.token != r.token {
					continue
				}
				if _, err := c.continueRequest(wake); err != nil {
					t.Fatalf("answer's completion ended the controller: %v", err)
				}
				break
			}
			got := e.request(t, r.token)
			if got.ProvenGoneHow != store.ProvenGoneTurnEnd {
				t.Fatalf("turn's end did not prove the request: %+v", got)
			}
			claimed, issued := store.ResponseRelayClaimed, store.ResponseRelayIssued
			if tc.pane {
				claimed, issued = store.ResponseNativeClaimed, store.ResponseNativeIssued
			}
			if tc.at == "after issue" {
				if err != nil || !r.sent.Load() || got.ResponseState != issued || !bytes.Contains(wire.bytes(), []byte(`"result"`)) {
					t.Fatalf("issued answer not written: err=%v state=%s wire=%s", err, got.ResponseState, wire.bytes())
				}
				if err := completedWrite(t, c.protocol); err != nil {
					t.Fatal(err)
				}
			} else {
				if !errors.Is(err, errAnswerTurnEnded) {
					t.Fatalf("answer refused at %s completed with %v, want errAnswerTurnEnded", tc.at, err)
				}
				if len(wire.bytes()) != 0 || r.sent.Load() || got.ResponseState != claimed {
					t.Fatalf("refused answer was written or issued: state=%s wire=%s", got.ResponseState, wire.bytes())
				}
				if !strings.Contains(pane.String(), "Cursor turn ended before its answer was written; nothing sent") || strings.Contains(pane.String(), "delivery is unconfirmed") {
					t.Fatalf("pane misreports the refused answer: %q", pane.String())
				}
			}
			if c.protocol.writeFailed.Load() {
				t.Fatal("turn's end failed the ACP transport")
			}
			select {
			case err := <-c.protocol.writes:
				t.Fatalf("refused answer published a write result: %v", err)
			case <-c.protocol.closed:
				t.Fatal("ACP transport closed")
			default:
			}
			if err := c.protocol.send(map[string]any{"jsonrpc": "2.0", "method": "later"}); err != nil {
				t.Fatalf("later frame refused: %v", err)
			}
			if err := completedWrite(t, c.protocol); err != nil || !bytes.Contains(wire.bytes(), []byte(`"later"`)) {
				t.Fatalf("later frame not written: %v %s", err, wire.bytes())
			}
		})
	}
}

// b.7qb round 1b: c.requests is never pruned, so a turn's end lists only the
// requests no earlier turn's end listed and whose tool proof the gateway has
// not already stored. A turn with no request of its own does no store write
// at its end.
func TestCursorTurnEndListsOnlyItsOwnUnprovenRequests(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	e := newRequestEnv(t, "on", "synthetic-session")
	e.startTurn(t)
	c := e.controller(t, &observedWire{}, io.Discard)
	turnOpen := true
	c.turnOpen = func() bool { return turnOpen }
	endTurn := func(step string, until time.Time) []string {
		t.Helper()
		turnOpen = false
		ended := c.endTurn()
		if err := ended.write(until); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		ended.settle()
		return ended.tokens
	}

	ran := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
	unanswered := e.admit(t, c, `2`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "other", "execute"))
	e.decide(t, ran.token, "allow")
	if err := continuePermission(c, ran); err != nil {
		t.Fatal(err)
	}
	if err := completedWrite(t, c.protocol); err != nil {
		t.Fatal(err)
	}
	if err := c.execution(executionUpdate(e.binding.SessionID, "shell", "completed", flushShellOutput)); err != nil {
		t.Fatal(err)
	}
	proven := e.request(t, ran.token)
	if proven.ProvenGoneHow != store.ProvenGoneToolRan || ran.proof != nil || ran.lastProof == nil {
		t.Fatalf("gateway did not store the tool proof: %+v", proven)
	}
	if tokens := endTurn("first turn's end", time.Time{}); !slices.Equal(tokens, []string{unanswered.token}) {
		t.Fatalf("first turn's end listed %v, want only its unproven request %s", tokens, unanswered.token)
	}

	turnOpen = true
	own := e.admit(t, c, `3`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "third", "execute"))
	if tokens := endTurn("second turn's end", time.Time{}); !slices.Equal(tokens, []string{own.token}) {
		t.Fatalf("second turn's end listed %v, want only its own request %s", tokens, own.token)
	}
	if got := e.request(t, own.token); got.ProvenGoneHow != store.ProvenGoneTurnEnd {
		t.Fatalf("second turn's end did not prove its own request: %+v", got)
	}
	if got := e.request(t, ran.token); got != proven {
		t.Fatalf("a later turn's end changed a request proven tool_ran: before %+v after %+v", proven, got)
	}

	// Another writer holds the lock, so any store write would fail.
	turnOpen = true
	release := apitest.HoldWriteLock(t, e.path, time.Minute)
	defer release()
	if tokens := endTurn("third turn's end", time.Now().Add(200*time.Millisecond)); len(tokens) != 0 {
		t.Fatalf("turn with no request of its own listed %v", tokens)
	}
}
