package api_test

// advice_follow_codex_relay_test.go (E4-T4, E14): ErrDialogMaybeOpen's Codex
// wording of what proves a request gone, followed literally. Fixture:
// pane_answer_fixture_test.go.

import (
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// advCodexProofs is ErrDialogMaybeOpen's Codex wording of what proves the
// request gone (b.146 step 2c; a Codex request has no tool_use_id).
const advCodexProofs = "no Stop or Interrupt of the main agent after it for a request with no agent_id, and no end of its agent is recorded"

// TestAdviceFollow_E14_CodexDialogMaybeOpenNamesItsProofs: E14 ErrDialogMaybeOpen on a Codex row, "(no Stop or
// Interrupt of the main agent after it for a request with no agent_id, and no end of its agent is recorded)" (b.146
// step 2c). It names no PostToolUse or tool_use_id: a Codex request has none. Followed literally: a Stop or an
// Interrupt of the main agent after a request with no agent_id releases it; neither releases a request that has an
// agent_id (a subagent's), which the end of its agent does (the row ended, then resumed).
func TestAdviceFollow_E14_CodexDialogMaybeOpenNamesItsProofs(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	assertWording := func(t *testing.T, p *paEnv, state string) {
		t.Helper()
		err := p.sendKeys(p.plain("hi"))
		assertDialogMaybeOpen(t, err, tok, state)
		adviceAssertPhrase(t, err, "("+advCodexProofs+")")
		for _, claude := range []string{"PostToolUse", "tool_use_id"} {
			if strings.Contains(errText(err), claude) {
				t.Errorf("description %q names %q; a Codex request has none", errText(err), claude)
			}
		}
	}
	for _, event := range []string{"Interrupt", "Stop"} {
		t.Run("a request with no agent_id, released by the main agent's "+event, func(t *testing.T) {
			t.Parallel()
			p := newCodexPAEnv(t)
			r7Acked(t, p)
			assertWording(t, p, store.StateCheckPermission)

			p.prove(t, turnEnd())
			if a := apitest.ApplyAgentHook(t, p.dbPath, p.r.ID, event, "", apitest.HookAgent("codex")); !a.Applied {
				t.Fatalf("the main agent's %s not applied: %s", event, a.Reason)
			}

			p.assertTyped(t)
		})
	}
	t.Run("a request with an agent_id, released only by the end of its agent", func(t *testing.T) {
		t.Parallel()
		p := newCodexSubagentPAEnv(t)
		r7Acked(t, p)
		assertWording(t, p, store.StateCheckPermission)

		p.mainAgentStop(t)
		assertWording(t, p, store.StateWaiting)

		r7End(t, p)
		r7Resume(t, p)

		// The resumed launch is a new tmux session the fixture's does not match, so read the guard (it makes no tmux
		// call) rather than type.
		row, err := p.st.GetSpawn(p.r.ID)
		if err != nil {
			t.Fatalf("GetSpawn: %v", err)
		}
		if eval, refusal, err := api.EvaluateRelayGuardForTest(p.store, p.view(), 0, row, p.plain("hi")); err != nil || eval != "released" || refusal != nil {
			t.Errorf("guard = %q, %v, %v; want released", eval, refusal, err)
		}
		if pr := p.request(t, tok); pr.ProvenGoneHow != store.ProvenGoneAgentGone {
			t.Errorf("request A proven_gone_how = %q; want %q", pr.ProvenGoneHow, store.ProvenGoneAgentGone)
		}
	})
	t.Run("the Claude wording is unchanged", func(t *testing.T) {
		t.Parallel()
		p := newPAEnv(t)
		r7Acked(t, p)
		err := p.sendKeys(p.plain("hi"))
		assertDialogMaybeOpen(t, err, tok, store.StateCheckPermission)
		adviceAssertPhrase(t, err, "(no PostToolUse with its tool_use_id, no end of the main agent's turn after it for a request with no agent_id, and no end of its agent is recorded)")
		if strings.Contains(errText(err), "Interrupt") {
			t.Errorf("description %q names an Interrupt; Claude has none", errText(err))
		}
	})
}
