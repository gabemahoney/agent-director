//go:build linux

package cursorhost

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const flushShellOutput = `{"exitCode":0,"stdout":"done","stderr":""}`

// b.o89: a flush waits out a competing writer inside the busy window instead
// of leaving the proofs to later ticks. It stores every deferred proof, a
// successful MCP one among them, after the generation change that /new and
// /exit make first. Only settle clears request state, and a cleared proof is
// not retried once its authority is gone.
func TestCursorProofFlushWaitsOutBriefContention(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	e := newRequestEnv(t, "off", "synthetic-session")
	c := e.controller(t, &observedWire{}, io.Discard)
	shell := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
	mcp := e.admit(t, c, `2`, "session/request_permission", mcpPermission(e.binding.SessionID, "mcp"))

	release := apitest.HoldWriteLock(t, e.path, time.Minute)
	if err := c.execution(executionUpdate(e.binding.SessionID, "shell", "completed", flushShellOutput)); err != nil {
		t.Fatalf("contended shell proof ended the controller: %v", err)
	}
	if err := c.execution(executionUpdate(e.binding.SessionID, "mcp", "completed", `{"success":true}`)); err != nil {
		t.Fatalf("contended MCP proof ended the controller: %v", err)
	}
	if shell.proof == nil || mcp.proof == nil {
		t.Fatal("proofs written while another writer held the lock")
	}
	c.cancel()
	flush := c.flushProofs()
	done := make(chan error, 1)
	go func() { done <- flush.write(time.Time{}) }()
	select {
	case err := <-done:
		t.Fatalf("flush returned %v while the writer held the lock, inside the busy window", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("flush after the writer released: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("flush never finished after the writer released")
	}
	if shell.proof == nil || mcp.proof == nil {
		t.Fatal("write changed request state; only settle may")
	}
	flush.settle()
	if shell.proof != nil || mcp.proof != nil {
		t.Fatal("settle kept a stored proof deferred")
	}
	for _, r := range []*pendingAuthorization{shell, mcp} {
		got := e.request(t, r.token)
		if got.ProvenGoneHow != store.ProvenGoneToolRan || got.PaneAnswer != store.PaneAnswerToolRan || got.Decision != "allow" || got.DecisionReason != store.DecisionReasonToolRan || got.Closed() {
			t.Fatalf("wrong flushed proof for %s: %+v", r.auth.toolID, got)
		}
	}
	e.exec(t, `UPDATE cursor_runtimes SET bound_terminated=1`)
	if err := c.tick(); err != nil {
		t.Fatalf("tick retried a settled proof under retired authority: %v", err)
	}
}

// b.o89: a flush whose bound has already passed returns ErrStoreBusy at once
// and leaves the proof deferred, so a later tick still stores it inside its
// busy window.
func TestCursorProofFlushPastBoundReturnsBusyAndKeepsProof(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	e := newRequestEnv(t, "off", "synthetic-session")
	e.rep.cfg.Store.BusyTimeoutMs = 60000
	c := e.controller(t, &observedWire{}, io.Discard)
	r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))

	release := apitest.HoldWriteLock(t, e.path, time.Minute)
	if err := c.execution(executionUpdate(e.binding.SessionID, "shell", "completed", flushShellOutput)); err != nil {
		t.Fatalf("contended proof ended the controller: %v", err)
	}
	flush := c.flushProofs()
	done := make(chan error, 1)
	go func() { done <- flush.write(time.Now().Add(-time.Second)) }()
	select {
	case err := <-done:
		if !errors.Is(err, store.ErrStoreBusy) {
			t.Fatalf("flush past its bound = %v; want ErrStoreBusy", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("flush past its bound kept retrying inside the 60 s busy window")
	}
	flush.settle()
	if r.proof == nil {
		t.Fatal("settle cleared a proof the flush did not store")
	}
	if got := e.request(t, r.token); got.ProvenGone() {
		t.Fatalf("proof stored while another writer held the lock: %+v", got)
	}
	release()
	if err := c.tick(); err != nil {
		t.Fatalf("tick after the writer released: %v", err)
	}
	if got := e.request(t, r.token); got.ProvenGoneHow != store.ProvenGoneToolRan || r.proof != nil {
		t.Fatalf("deferred proof not stored by the next tick: %+v", got)
	}
}

// b.o89: one proof's non-busy failure does not stop a flush from storing the
// others, and settle keeps only the failed one deferred.
func TestCursorProofFlushAttemptsEveryProof(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	e := newRequestEnv(t, "off", "synthetic-session")
	c := e.controller(t, &observedWire{}, io.Discard)
	stale := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "stale", "execute"))
	current := e.admit(t, c, `2`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "current", "execute"))

	release := apitest.HoldWriteLock(t, e.path, time.Minute)
	for _, tool := range []string{"stale", "current"} {
		if err := c.execution(executionUpdate(e.binding.SessionID, tool, "completed", flushShellOutput)); err != nil {
			t.Fatalf("contended %s proof ended the controller: %v", tool, err)
		}
	}
	release()
	// Only this proof's captured authority no longer matches the runtime row.
	stale.proof.binding.Connection = "replacement"
	flush := c.flushProofs()
	if len(flush.proofs) != 2 {
		t.Fatalf("flush took %d proofs; want 2", len(flush.proofs))
	}
	// The failing proof goes first, so a flush that stops at a failure skips
	// the current one; request map order alone would hide that half the time.
	if flush.proofs[0].r != stale {
		flush.proofs[0], flush.proofs[1] = flush.proofs[1], flush.proofs[0]
	}
	err := flush.write(time.Time{})
	if err == nil || errors.Is(err, store.ErrStoreBusy) {
		t.Fatalf("flush with one stale proof = %v; want its non-busy failure", err)
	}
	flush.settle()
	if got := e.request(t, current.token); got.ProvenGoneHow != store.ProvenGoneToolRan || current.proof != nil {
		t.Fatalf("a failed proof stopped the flush storing another: %+v", got)
	}
	if got := e.request(t, stale.token); got.ProvenGone() || stale.proof == nil {
		t.Fatalf("stale proof stored or cleared: %+v", got)
	}
}

// b.eoe: a flush writes its proofs with the turnOpen read when it was taken,
// not when write runs, so a proof flushed with no turn open leaves its row in
// check_permission even if a turn opens before the write.
func TestCursorProofFlushCarriesTurnOpenFromWhenTaken(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	cases := []struct {
		name      string
		takenOpen bool
		want      string
	}{
		{"turn closed when taken, reopened before write", false, store.StateCheckPermission},
		{"turn open when taken, closed before write", true, store.StateWorking},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRequestEnv(t, "on", "synthetic-session")
			e.startTurn(t)
			c := e.controller(t, &observedWire{}, io.Discard)
			turnOpen := tc.takenOpen
			c.turnOpen = func() bool { return turnOpen }
			r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
			e.decide(t, r.token, "allow")
			if err := c.tick(); err != nil {
				t.Fatal(err)
			}
			if err := completedWrite(t, c.protocol); err != nil {
				t.Fatal(err)
			}

			release := apitest.HoldWriteLock(t, e.path, time.Minute)
			if err := c.execution(executionUpdate(e.binding.SessionID, "shell", "completed", flushShellOutput)); err != nil {
				t.Fatalf("contended proof ended the controller: %v", err)
			}
			release()
			if r.proof == nil {
				t.Fatal("proof written while another writer held the lock; the flush has nothing to carry")
			}
			e.wantState(t, "before flush", store.StateCheckPermission)
			flush := c.flushProofs()
			turnOpen = !tc.takenOpen
			if err := flush.write(time.Time{}); err != nil {
				t.Fatalf("flush: %v", err)
			}
			flush.settle()
			if got := e.request(t, r.token); got.ProvenGoneHow != store.ProvenGoneToolRan || r.proof != nil {
				t.Fatalf("flushed proof: %+v", got)
			}
			e.wantState(t, "after flush", tc.want)
		})
	}
}
