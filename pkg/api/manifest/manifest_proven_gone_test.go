package manifest_test

import (
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestProvenGoneHowTextNamesTheCodexProofs (b.ssz Epic 4, SR-8.2): every verb
// whose result carries a permission request's delivery facts describes
// proven_gone_how with Claude's tool_ran proof (never on a Codex row), the
// turn_end proofs of both agents (the Stop or idle-prompt Notification, a
// Codex Interrupt) and agent_gone; the committed surface.json mirror test
// then carries the same text to help, MCP and the docs.
func TestProvenGoneHowTextNamesTheCodexProofs(t *testing.T) {
	for _, verb := range []string{"get-permission", "decide"} {
		t.Run(verb, func(t *testing.T) {
			apitest.AssertAgentTextCase(t, verb+" result field proven_gone_how", siteText(t, verb, "", "proven_gone_how"), apitest.DescProvenGoneHow())
		})
	}
}
