package api_test

// codex_relay_test.go — the permission relay on a Codex row (E4-T4): what
// differs from Claude's in the send-keys hold, the pane answers, the repair
// and the Esc flow. A Codex request has no tool_use_id (so no PostToolUse
// proves it gone) and the main agent's Interrupt, Codex's end of a cancelled
// turn, is its turn_end proof; the row's Interrupt moves it to waiting. The
// cases both agents share are the agent dimension of the existing relay,
// hold, pane-answer, pause and repair tests (paAgents). Fixture:
// pane_answer_fixture_test.go.

import (
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// codexState reads p's row state.
func (p *paEnv) codexState(t *testing.T) string {
	t.Helper()
	sp, err := p.st.GetSpawn(p.r.ID)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	return sp.State
}

// assertTyped fails unless a plain send-keys on p now types.
func (p *paEnv) assertTyped(t *testing.T) {
	t.Helper()
	if run := p.sendKeysRun(p.plain("hi")); run.err != nil || !slices.Equal(paKinds(run.calls), paneSendCalls) {
		t.Fatalf("plain send-keys = %v, calls %v; want it typed", run.err, paKinds(run.calls))
	}
}

// TestCodexRequestRecordsNoToolUseID: the request a Codex relay hook records
// carries Codex's tool_name and tool_input as sent, no tool_use_id, and, for
// a subagent's, its agent_id; its row is a Codex row (the fixture's premise).
func TestCodexRequestRecordsNoToolUseID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		env     func(*testing.T) *paEnv
		agentID string
	}{{"main agent", newCodexPAEnv, ""}, {"subagent", newCodexSubagentPAEnv, paSubagentID}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := tc.env(t)
			sp, err := p.st.GetSpawn(p.r.ID)
			if err != nil || sp.Agent != store.AgentCodex {
				t.Fatalf("row = %q, %v; want a Codex row", sp.Agent, err)
			}
			pr := p.request(t, storefix.TestRequestTokenA)
			if pr.ToolUseID != "" || pr.AgentID != tc.agentID || pr.ToolName != "Bash" ||
				pr.ToolInput != `{"command":"ls","description":"List the files"}` {
				t.Errorf("request A = %+v; want Bash with Codex's input, no tool_use_id, agent_id %q", pr, tc.agentID)
			}
		})
	}
}

// TestCodexInterruptProvesTurnEndAndMovesRowToWaiting: request A of a Codex
// row, acked by its relay hook, is not proven gone (plain send-keys is held);
// the main agent's Interrupt proves it turn_end and moves the row to waiting,
// and the same send-keys then types.
func TestCodexInterruptProvesTurnEndAndMovesRowToWaiting(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	p := newCodexPAEnv(t)
	r7Acked(t, p)
	assertDialogMaybeOpen(t, p.sendKeys(p.plain("hi")), tok, store.StateCheckPermission)

	p.mainAgentStop(t)

	if got := p.codexState(t); got != store.StateWaiting {
		t.Errorf("row after the Interrupt = %q; want %q", got, store.StateWaiting)
	}
	if pr := p.request(t, tok); pr.ProvenGoneHow != store.ProvenGoneTurnEnd || !pr.ProvenGone() {
		t.Errorf("request A = %+v; want proven gone by turn_end", pr)
	}
	p.assertTyped(t)
}

// TestCodexSubagentRequestHeldAfterMainInterrupt: a Codex subagent's request
// (agent_id recorded) is no main-agent request: the main agent's Interrupt
// proves nothing of it, so plain send-keys stays ErrDialogMaybeOpen, naming it
// with a null tool_use_id, after one Interrupt and after another.
func TestCodexSubagentRequestHeldAfterMainInterrupt(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	p := newCodexSubagentPAEnv(t)
	r7Acked(t, p)

	for i := 0; i < 2; i++ {
		p.mainAgentStop(t)

		held := p.sendKeysRun(p.plain("hi"))
		d := assertDialogMaybeOpen(t, held.err, tok, store.StateWaiting)
		if d.ToolUseID != nil || d.ToolName != "Bash" || len(held.calls) != 0 {
			t.Errorf("err_details tool_use_id = %v, tool_name = %q, calls %v; want null, Bash, nothing sent", d.ToolUseID, d.ToolName, paKinds(held.calls))
		}
		if pr := p.request(t, tok); pr.ProvenGone() || pr.AgentID != paSubagentID {
			t.Fatalf("request A = %+v after Interrupt %d; want the subagent's, not proven gone", pr, i+1)
		}
	}
}

// TestCodexEscapePaneAnswerOnFallenBackRequest: with request A fallen back, a
// pane answer of Escape (deny) is accepted: it types the one key and records
// pane_answer sent. It is no proof: plain send-keys is held until the Interrupt
// Codex sends for the cancelled turn proves A turn_end and moves the row to
// waiting, after which plain send-keys types.
func TestCodexEscapePaneAnswerOnFallenBackRequest(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	p := newCodexPAEnv(t)

	run := p.sendKeysRun(p.answer(tok, "deny", "Escape"))

	if run.err != nil {
		t.Fatalf("Escape pane answer: %v", run.err)
	}
	assertOneKey(t, run.calls, p.r.Spawn.Identity.PaneID, "Escape", tmux.CallLookup, tmux.CallListPanes, tmux.CallCapture)
	if pr := p.request(t, tok); pr.PaneAnswer != store.PaneAnswerSent || pr.Decision != "deny" || pr.ProvenGone() {
		t.Fatalf("request A = %+v; want pane_answer sent, deny, not proven gone", pr)
	}
	assertDialogMaybeOpen(t, p.sendKeys(p.plain("hi")), tok, store.StateCheckPermission)

	p.mainAgentStop(t)

	if got := p.codexState(t); got != store.StateWaiting {
		t.Errorf("row after the Interrupt = %q; want %q", got, store.StateWaiting)
	}
	p.assertTyped(t)
}

// TestCodexFallenBackAllowClosesOnlyViaRecordPaneAnswer: a fallen-back Codex
// request that the person allowed at the pane stays open in agent-director's
// records through decide (refused, ErrRelayFallenBack, the attempt kept), plain
// send-keys, a PostToolUse (which proves nothing, the request has no
// tool_use_id) and the main agent's Interrupt (a proof, not a close); only
// record-pane-answer closes it.
func TestCodexFallenBackAllowClosesOnlyViaRecordPaneAnswer(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	p := newCodexPAEnv(t)

	_, err := api.Decide(p.st, p.view(), api.DecideParams{InstanceID: p.r.ID, RequestToken: tok, Decision: "allow"})
	assertOneSentinel(t, err, api.ErrRelayFallenBack)
	assertOneSentinel(t, p.sendKeys(p.plain("hi")), api.ErrRelayFallenBack)
	if n := p.prove(t, toolRan(paToolUseID)); n != 0 {
		t.Fatalf("a PostToolUse proved %d; want none on a Codex row", n)
	}
	p.mainAgentStop(t)
	pr := p.request(t, tok)
	if !pr.AwaitsAnswer() || pr.PaneAnswer != store.PaneAnswerNone || pr.AttemptedDecision != "allow" || pr.Decision != "" {
		t.Fatalf("request A = %+v; want open, no pane answer, the allow only attempted", pr)
	}
	assertOneSentinel(t, p.sendKeys(p.plain("hi")), api.ErrRelayFallenBack)

	p.now = p.now.Add(time.Minute) // past the 2 s the first refusal's hook_gone_at needs
	if _, err := p.recordPaneAnswer(p.outside(tok, "allow")); err != nil {
		t.Fatalf("record-pane-answer: %v", err)
	}

	pr = p.request(t, tok)
	if pr.AwaitsAnswer() || pr.PaneAnswer != store.PaneAnswerOutside || pr.PaneAs != "allow" {
		t.Errorf("request A = %+v; want closed, pane_answer outside, pane_as allow", pr)
	}
	p.assertTyped(t) // proven turn_end by the Interrupt, now closed too
}

// TestCodexEscWhilePolling (F5): the person presses Esc in Codex while request
// A's relay hook polls. The hook, killed with the turn, is gone and A fell
// back; Codex's Interrupt for the main agent then proves A turn_end and moves
// the row to waiting. A fallen back request is no longer closed by that proof:
// plain send-keys stays ErrRelayFallenBack until record-pane-answer, which
// releases it at once (already proven).
func TestCodexEscWhilePolling(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	p := newCodexPAEnv(t)
	p.pc.Set(relayEnvHook.PID, procfix.Alive(relayEnvHook.Starttime))
	assertOneSentinel(t, p.sendKeys(p.plain("hi")), api.ErrSendKeysWhileRelayed)

	p.pc.Set(relayEnvHook.PID, procfix.Gone()) // Esc ends the hook
	p.mainAgentStop(t)                         // Codex's Interrupt, after A was recorded

	if got := p.codexState(t); got != store.StateWaiting {
		t.Fatalf("row after Esc = %q; want %q", got, store.StateWaiting)
	}
	if pr := p.request(t, tok); pr.ProvenGoneHow != store.ProvenGoneTurnEnd || pr.PaneAnswer != store.PaneAnswerNone {
		t.Fatalf("request A = %+v; want proven turn_end, no pane answer", pr)
	}
	assertOneSentinel(t, p.sendKeys(p.plain("hi")), api.ErrRelayFallenBack)

	p.now = p.now.Add(time.Minute) // past the 2 s the first refusal's hook_gone_at needs
	if _, err := p.recordPaneAnswer(p.outside(tok, "deny")); err != nil {
		t.Fatalf("record-pane-answer: %v", err)
	}
	p.assertTyped(t)
}

// TestCodexEscWhilePollingSameSecondResidual (F5, residual): when the Interrupt
// hook starts in the second request A was recorded, the turn_end proof's cut
// (created_at before the hook's start second) leaves A unproven. The row still
// moves to waiting, A stays fallen back, then, once recorded at the pane,
// ErrDialogMaybeOpen (not proven gone) until the next turn end proves it.
func TestCodexEscWhilePollingSameSecondResidual(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	p := newCodexPAEnv(t)
	p.pc.Set(relayEnvHook.PID, procfix.Gone())
	sameSecond := store.RequestProof{How: store.ProvenGoneTurnEnd, RecordedBefore: p.request(t, tok).CreatedAt}

	if n := p.prove(t, sameSecond); n != 0 {
		t.Fatalf("an Interrupt started in A's second proved %d; want none", n)
	}
	if a := apitest.ApplyAgentHook(t, p.dbPath, p.r.ID, "Interrupt", "", apitest.HookAgent("codex")); !a.Applied {
		t.Fatalf("Interrupt not applied: %s", a.Reason)
	}

	if got := p.codexState(t); got != store.StateWaiting {
		t.Fatalf("row after the Interrupt = %q; want %q", got, store.StateWaiting)
	}
	assertOneSentinel(t, p.sendKeys(p.plain("hi")), api.ErrRelayFallenBack)
	p.now = p.now.Add(time.Minute) // past the 2 s the first refusal's hook_gone_at needs
	if _, err := p.recordPaneAnswer(p.outside(tok, "deny")); err != nil {
		t.Fatalf("record-pane-answer: %v", err)
	}
	assertDialogMaybeOpen(t, p.sendKeys(p.plain("hi")), tok, store.StateWaiting)

	p.mainAgentStop(t) // the next turn's end

	p.assertTyped(t)
}
