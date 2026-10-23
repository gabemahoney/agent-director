//go:build linux

package cursorhost

import (
	"io"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.o89: a competing writer released inside the store's busy timeout must not
// end the controller, or lose the validated proof, of an allowed and
// delivered shell request; the next tick stores it.
func TestCursorExecutionProofSurvivesBriefWriterContention(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	e := newRequestEnv(t, "on", "synthetic-session")
	c := e.controller(t, &observedWire{}, io.Discard)
	r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
	e.decide(t, r.token, "allow")
	if err := c.tick(); err != nil {
		t.Fatal(err)
	}
	if err := completedWrite(t, c.protocol); err != nil {
		t.Fatal(err)
	}
	if e.request(t, r.token).DeliveredAt.IsZero() {
		t.Fatal("allow was not delivered before the tool completed")
	}

	release := apitest.HoldWriteLock(t, e.path, time.Minute)
	if err := c.execution(executionUpdate(e.binding.SessionID, "shell", "completed", `{"exitCode":0,"stdout":"done","stderr":""}`)); err != nil {
		t.Fatalf("writer contention inside the busy timeout ended the controller: %v", err)
	}
	if got := e.request(t, r.token); got.ProvenGone() {
		t.Fatalf("proof stored while another writer held the lock: %+v", got)
	}
	release()
	if err := c.tick(); err != nil {
		t.Fatalf("tick after the competing writer released: %v", err)
	}
	got := e.request(t, r.token)
	if got.ProvenGoneHow != store.ProvenGoneToolRan || got.Decision != "allow" || got.DecisionReason != "tool_ran" || got.PaneAnswer != store.PaneAnswerNone || got.DeliveredAt.IsZero() || got.Closed() {
		t.Fatalf("wrong proof after contention: %+v", got)
	}
}
