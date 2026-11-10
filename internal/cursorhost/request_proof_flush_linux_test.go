//go:build linux

package cursorhost

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/sqliteobserve"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const flushShellOutput = `{"exitCode":0,"stdout":"done","stderr":""}`

// b.o89: a flush waits out a competing writer inside the busy window instead
// of leaving the proofs to later proof wakes. It stores every deferred proof, a
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
	lateShellWake, lateMCPWake := proofWakeFor(shell), proofWakeFor(mcp)
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
	for _, wake := range []requestWake{lateShellWake, lateMCPWake} {
		if progressed, err := c.continueRequest(wake); err != nil || progressed {
			t.Fatalf("retired proof wake progressed=%v error=%v", progressed, err)
		}
	}
}

// b.o89: a flush whose bound has already passed returns ErrStoreBusy at once
// and leaves the proof deferred, so a later flush attempt still stores it inside its
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
	if err := flush.write(time.Time{}); err != nil {
		t.Fatalf("same flush after the writer released: %v", err)
	}
	flush.settle()
	if got := e.request(t, r.token); got.ProvenGoneHow != store.ProvenGoneToolRan || r.proof != nil {
		t.Fatalf("deferred proof not stored by the same admitted flush: %+v", got)
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
	flush := c.flushProofs()
	if len(flush.proofs) != 2 {
		t.Fatalf("flush took %d proofs; want 2", len(flush.proofs))
	}
	// The failing proof goes first, so a flush that stops at a failure skips
	// the current one; request map order alone would hide that half the time.
	if flush.proofs[0].r != stale {
		flush.proofs[0], flush.proofs[1] = flush.proofs[1], flush.proofs[0]
	}
	flush.proofs[0].proof.binding.Connection = "replacement"
	flush.proofs[0].proof.event.Owner.Runtime = flush.proofs[0].proof.binding
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
			if err := continuePermission(c, r); err != nil {
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

func TestCursorRepeatedFlushEventsOwnTheirExactCompletion(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	for _, event := range []struct {
		name    string
		kind    proofFlushKind
		control bool
	}{
		{"waiting", proofWaiting, false}, {"rotation", proofRotation, true},
		{"exit", proofExit, true}, {"fatal", proofFatal, true}, {"runtime-gone", proofRuntimeGone, false},
	} {
		t.Run(event.name, func(t *testing.T) {
			e := newRequestEnv(t, "off", "synthetic-session")
			wire := &observedWire{}
			c := e.controller(t, wire, io.Discard)
			r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
			var previous *proofFlush
			var ids []string
			for n := uint64(1); n <= 2; n++ {
				release := apitest.HoldWriteLock(t, e.path, time.Minute)
				raw := append(bytes.Repeat([]byte(" "), int(n)), executionUpdate(e.binding.SessionID, "shell", "completed", flushShellOutput)...)
				if err := c.execution(raw); err != nil {
					t.Fatal(err)
				}
				if r.proof == nil {
					t.Fatal("busy proof not retained")
				}
				ids = append(ids, r.proof.event.ID)
				deadline := r.proof.event.BusyUntil
				if event.control {
					c.cancel()
				}
				old := c.flushProofs(proofFlushEvent{kind: event.kind, epoch: n})
				current := c.flushProofs(proofFlushEvent{kind: event.kind, epoch: n})
				if old.sequence == current.sequence || current.event.epoch != n || !current.proofs[0].proof.event.BusyUntil.Equal(deadline) {
					t.Fatal("new event reused ownership or renewed proof budget")
				}
				if previous != nil {
					previous.settle()
					if r.proof == nil || r.proof.event.ID != ids[len(ids)-1] {
						t.Fatal("old event disposed newer proof")
					}
				}
				release()
				if err := old.write(time.Time{}); err != nil {
					t.Fatal(err)
				}
				old.settle()
				if r.proof == nil {
					t.Fatal("old worker disposed the newer flush")
				}
				if err := current.write(time.Time{}); err != nil {
					t.Fatal(err)
				}
				current.settle()
				if r.proof != nil {
					t.Fatal("current completion did not settle its proof")
				}
				got, err := e.s.ReadCursorProof(e.binding.Connection, ids[len(ids)-1])
				if err != nil || !bytes.Equal(got.Raw, raw) || !got.BusyUntil.Equal(deadline) {
					t.Fatalf("snapshot changed: %+v %v", got, err)
				}
				before := e.request(t, r.token)
				if err := current.write(time.Time{}); err != nil {
					t.Fatal(err)
				}
				current.settle()
				if e.request(t, r.token) != before || len(wire.bytes()) != 0 {
					t.Fatal("duplicate completion changed evidence or emitted response")
				}
				previous = current
			}
			if ids[0] == ids[1] {
				t.Fatal("new event reused proof ID")
			}
		})
	}
}

func TestCursorAbandonedFlushCannotStartSQLOrRecoverWaiting(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	e := newRequestEnv(t, "off", "synthetic-session")
	c := e.controller(t, &observedWire{}, io.Discard)
	r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
	release := apitest.HoldWriteLock(t, e.path, time.Minute)
	if err := c.execution(executionUpdate(e.binding.SessionID, "shell", "completed", flushShellOutput)); err != nil {
		t.Fatal(err)
	}
	id := r.proof.event.ID
	old := c.flushProofs(proofFlushEvent{kind: proofWaiting, epoch: 1})
	old.abandon()
	release()
	waiting := false
	old.registerWaiting(func(func() bool) { waiting = true }, func() bool { return true })
	if err := old.write(time.Time{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("abandoned write: %v", err)
	}
	old.settle()
	if waiting || r.proof == nil || e.request(t, r.token).ProvenGone() {
		t.Fatal("late grant started SQL or rearmed waiting")
	}
	// A newly admitted lifecycle event can commit the same captured evidence.
	current := c.flushProofs(proofFlushEvent{kind: proofExit, epoch: 2})
	if err := current.write(time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("expired event lost its immediate attempt: %v", err)
	}
	current.settle()
	if got, err := e.s.ReadCursorProof(e.binding.Connection, id); err != nil || got.ID != id || r.proof != nil {
		t.Fatalf("new event did not progress: %+v %v", got, err)
	}
	old.settle()
	if waiting {
		t.Fatal("retired completion enabled input")
	}
}

func TestCursorLateCommitCannotDisposeNewerFlush(t *testing.T) {
	for _, replaceGeneration := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-generation", true: "new-generation"}[replaceGeneration], func(t *testing.T) {
			e := newRequestEnv(t, "off", "synthetic-session")
			wire := &observedWire{}
			c := e.controller(t, wire, io.Discard)
			first := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "first", "execute"))
			second := e.admit(t, c, `2`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "second", "execute"))
			releaseWriter := apitest.HoldWriteLock(t, e.path, time.Minute)
			rawA := executionUpdate(e.binding.SessionID, "first", "completed", flushShellOutput)
			if err := c.execution(rawA); err != nil {
				t.Fatal(err)
			}
			if first.proof == nil {
				t.Fatal("proof A was not captured before SQL issue")
			}
			proofA := first.proof.event
			old := c.flushProofs(proofFlushEvent{kind: proofWaiting, epoch: 1})
			releaseWriter()
			entered, release := make(chan struct{}), make(chan struct{})
			var blockOnce, releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			stop := observeRequestSQLite(t, e.s, sqliteobserve.Hooks{AfterCommit: func(context.Context) error {
				blockOnce.Do(func() { close(entered); <-release })
				return nil
			}})
			done := make(chan error, 1)
			go func() { done <- old.write(time.Time{}) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("actual proof COMMIT did not reach its result barrier")
			}
			old.abandon()
			var result error
			select {
			case result = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("abandoned observer waited for native COMMIT response")
			}
			if result == nil || !strings.Contains(result.Error(), "unconfirmed") || old.proofs[0].stored {
				t.Fatalf("abandoned return confirmed late COMMIT: %v %+v", result, old.proofs[0])
			}
			frozenResult := result.Error()
			if replaceGeneration {
				c.cancel()
			}
			rawB := executionUpdate(e.binding.SessionID, "second", "completed", `{"exitCode":7,"stdout":"new proof","stderr":""}`)
			if err := c.execution(rawB); err != nil {
				t.Fatal(err)
			}
			if second.proof == nil {
				t.Fatal("explicit new proof did not remain deferred behind old native work")
			}
			proofB := second.proof.event
			if proofA.ID == proofB.ID || (proofA.Generation != proofB.Generation) != replaceGeneration {
				t.Fatal("new captured proof lost distinct identity/generation")
			}
			current := c.flushProofs(proofFlushEvent{kind: proofExit, epoch: 2})
			reader, err := store.Open(e.path)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			assertProof := func(want store.CursorProof) {
				t.Helper()
				got, err := reader.ReadCursorProof(e.binding.Connection, want.ID)
				gotJSON, _ := json.Marshal(got)
				wantJSON, _ := json.Marshal(want)
				if err != nil || !bytes.Equal(gotJSON, wantJSON) || !bytes.Equal(got.Raw, want.Raw) {
					t.Fatalf("independent proof changed: got=%s want=%s error=%v", gotJSON, wantJSON, err)
				}
			}
			assertProof(proofA)
			if _, err := reader.ReadCursorProof(e.binding.Connection, proofB.ID); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("new proof inherited old COMMIT: %v", err)
			}
			a, err := reader.GetPermissionRequest(e.binding.InstanceID, first.token)
			if err != nil || a.ProvenGoneHow != store.ProvenGoneToolRan || a.DecisionReason != store.DecisionReasonToolRan || a.ResponseState != "" {
				t.Fatalf("A atomic request proof: %+v %v", a, err)
			}
			b, err := reader.GetPermissionRequest(e.binding.InstanceID, second.token)
			if err != nil || b.ProvenGone() || b.Decision != "" || b.ResponseState != "" {
				t.Fatalf("old COMMIT mutated B: %+v %v", b, err)
			}
			waiting := false
			old.registerWaiting(func(func() bool) { waiting = true }, func() bool { return true })
			old.settle()
			if waiting || first.proof == nil || second.proof == nil || first.proof.flush != current.sequence || second.proof.flush != current.sequence || len(wire.bytes()) != 0 {
				t.Fatal("abandoned COMMIT disposed new work or enabled input/output")
			}
			unblock()
			stop() // Native result and connection cleanup finish before any assertion below.
			old.settle()
			if result.Error() != frozenResult || old.proofs[0].stored || second.proof == nil || second.proof.event.ID != proofB.ID || first.proof == nil || waiting || len(wire.bytes()) != 0 {
				t.Fatal("late native completion changed frozen return or newer obligations")
			}
			assertProof(proofA)
			if err := current.write(time.Time{}); err != nil {
				t.Fatal(err)
			}
			current.settle()
			assertProof(proofA)
			assertProof(proofB)
			if first.proof != nil || second.proof != nil || len(wire.bytes()) != 0 {
				t.Fatal("new admitted event failed to settle exactly its proofs without response")
			}
			old.settle()
			if waiting || result.Error() != frozenResult || len(wire.bytes()) != 0 {
				t.Fatal("retired completion regained publication authority")
			}
		})
	}
}
