package hook_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Regression b.kii / t2.xis.w3.ok, capture 5 (SR-2.5/O4): a sibling Stop
// continuation cannot let a later steer pass a completed, unsighted wake.
func TestClaudeSteerWaitsForBusyWakeSightingAfterStop(t *testing.T) {
	e := newWakeEnv(t, store.StateWaiting)
	e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": "existing parent turn"}))
	turn := mustGetSpawn(t, e.st, e.id)
	if turn.State != store.StateWorking || turn.TurnStarts != 1 {
		t.Fatalf("before A: state %s, Turn Starts %d; want working, 1", turn.State, turn.TurnStarts)
	}
	e.admit(t, "A", "QUEUED-WAKE")
	c := e.force(t)
	const wake = "[agent-director: message 1 of 1]\nQUEUED-WAKE"
	if got := e.wake(t); got != wake {
		t.Fatalf("busy forced wake wrote %q; want A alone %q", got, wake)
	}
	e.assertSent(t, store.BusySendReasonHandedOverNotProven, "A")
	finalA := e.result(t, "A")

	checkA := func(phase string, ordered bool) {
		t.Helper()
		wantPlace, wantOrder := "issued", store.BusySendOrderKind("")
		if ordered {
			wantPlace, wantOrder = "closed", store.BusySendOrderOrderedAcceptance
		}
		h, err := e.st.ReadBusySendHandover(context.Background(), c.Handover.HandoverID)
		if err != nil || len(h.Prompts) != 1 {
			t.Fatalf("%s: A hand-over %+v, %v; want one prompt", phase, h, err)
		}
		p := h.Prompts[0]
		if h.Boundary != store.BusySendBoundaryForce || h.Open == ordered ||
			p.Emission != store.BusySendEmissionIssued || p.Transport != store.BusySendTransportWritten || p.OrderKind != wantOrder {
			t.Errorf("%s: A open=%t, boundary=%s, emission=%s, transport=%s, order=%q; want open=%t, force, issued, written, order=%q",
				phase, h.Open, h.Boundary, p.Emission, p.Transport, p.OrderKind, !ordered, wantOrder)
		}
		facts, err := apitest.ReadBusySendFacts(e.db, e.id)
		if err != nil || len(facts.Messages) == 0 || facts.Messages[0].RequestID.String != "A" {
			t.Fatalf("%s: Message facts %+v, %v; want A first", phase, facts.Messages, err)
		}
		if a := facts.Messages[0]; a.Place != wantPlace || a.Reserved {
			t.Errorf("%s: A custody=%s, reserved=%t; want %s with its reservation freed", phase, a.Place, a.Reserved, wantPlace)
		}
		if got := e.result(t, "A"); !reflect.DeepEqual(got, finalA) {
			t.Errorf("%s: A Final Result changed: %+v; want %+v", phase, got, finalA)
		}
	}
	checkA("complete output without sighting", false)
	// The finite call returns pending before B arrives; its end and deadline
	// cannot release the issued wake's ownership.
	if ended := e.endCall(t, c); ended.Outcome != store.BusySendForcePending {
		t.Fatalf("force call ended %+v; want pending without A's sighting", ended)
	}
	e.inCall(c, 20*time.Second)
	e.admitAs(t, "B", "LATER-STEER", store.BusySendSteerTurn, true)
	facts, err := apitest.ReadBusySendFacts(e.db, e.id)
	if err != nil || len(facts.Messages) != 2 || facts.Messages[0].RequestID.String != "A" || facts.Messages[0].Seq != 1 ||
		facts.Messages[1].RequestID.String != "B" || facts.Messages[1].Seq != 2 {
		t.Fatalf("Messages %+v, %v; want A at sequence 1 before B at sequence 2", facts.Messages, err)
	}
	if o := e.observe(t); o.AwaitingTurnStart != 1 || o.HeldCount != 1 {
		t.Fatalf("before Stop: awaiting %d, Held %d; want A awaiting, B Held", o.AwaitingTurnStart, o.HeldCount)
	}
	checkA("force call ended pending", false)

	// Each Stop is strictly after A's completion. The sibling continuation
	// reaches PostToolBatch directly: no UserPromptSubmit and no new Turn Start.
	for range 2 {
		e.tick(time.Second)
		e.fire(t, e.payload(t, "stop.json", nil))
		checkA("parent Stop without sighting", false)
		e.tick(time.Second)
		got := e.steer(t)
		if row := mustGetSpawn(t, e.st, e.id); row.TurnStarts != turn.TurnStarts {
			t.Fatalf("continuation changed Turn Starts to %d; want %d without a new user turn", row.TurnStarts, turn.TurnStarts)
		}
		e.assertHeld(t, "B", store.BusySendWaitHandoverInProgress)
		checkA("continuation tool boundary without sighting", false)
		if got != "" {
			t.Fatalf("PostToolBatch after parent Stop issued early steer %q while wake A remains unsighted; want no output", got)
		}
		if o := e.observe(t); o.AwaitingTurnStart != 1 || o.HeldCount != 1 {
			t.Fatalf("after continuation: awaiting %d, Held %d; want A awaiting, B Held", o.AwaitingTurnStart, o.HeldCount)
		}
	}

	// A's exact wrapped sighting supplies the missing fact, but schedules no
	// ordinary output: B still needs its own later eligible tool boundary.
	e.tick(time.Second)
	e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": claudeWake(wake)}))
	checkA("exact sighting after complete output", true)
	if r := e.result(t, "B"); r.Class != store.BusySendClassHeld || r.Final {
		t.Fatalf("sighting alone released B: %+v; want non-Final Held until its tool boundary", r)
	}
	e.tick(time.Second)
	const steer = "[agent-director: message 1 of 1]\nLATER-STEER"
	if got := e.steer(t); got != steer {
		t.Fatalf("PostToolBatch after A's sighting wrote %q; want B alone %q", got, steer)
	}
	e.assertSent(t, store.BusySendReasonHandedOverNotProven, "B")
	finalB := e.result(t, "B")
	if got := e.steer(t); got != "" {
		t.Errorf("repeated PostToolBatch wrote %q; want B issued once", got)
	}
	e.tick(time.Second)
	e.fire(t, e.payload(t, "stop.json", nil))
	if got := e.steer(t); got != "" {
		t.Errorf("PostToolBatch after later Stop wrote %q; want no replay", got)
	}
	if got := e.wake(t); got != "" {
		t.Errorf("later wake wrote %q; want no replay", got)
	}
	checkA("subsequent callbacks", true)
	if got := e.result(t, "B"); !reflect.DeepEqual(got, finalB) {
		t.Errorf("subsequent callbacks changed B Final Result: %+v; want %+v", got, finalB)
	}
}
