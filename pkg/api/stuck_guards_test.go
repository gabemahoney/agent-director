package api_test

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// getStuck is api.Get judged against the committed row of p.
func (p *paEnv) getStuck(t *testing.T, want string, commit apitest.CommitCheck) {
	t.Helper()
	observeStuck(t, p.st, p.r.ID, want, commit, func() (string, api.StuckInfo, error) {
		row, err := api.Get(p.st, p.view(), p.r.ID)
		return row.State, row.StuckInfo, err
	})
}

// enterStuck reads once, committing the request's fallback clock, then again
// at the timeout, committing the entry.
func (p *paEnv) enterStuck(t *testing.T) {
	t.Helper()
	p.getStuck(t, store.StateCheckPermission, func(store.Spawn, store.Spawn) bool {
		return !p.request(t, storefix.TestRequestTokenA).FallbackAt.IsZero()
	})
	p.now = p.now.Add(30 * time.Second)
	p.getStuck(t, "stuck", apitest.VersionAdvanced)
}

func TestPermissionStuckPreservesLiveCommandGuards(t *testing.T) {
	t.Parallel()
	for _, agent := range paAgents {
		t.Run(agent.name, func(t *testing.T) {
			p := agent.env(t)
			p.enterStuck(t)
			assertOneSentinel(t, p.sendKeys(p.plain("1")), api.ErrRelayFallenBack)
			zero := int64(0)
			_, err := api.Decide(p.st, p.view(), api.DecideParams{InstanceID: p.r.ID, RequestToken: storefix.TestRequestTokenA, Decision: "allow", MaxWaitMs: &zero})
			assertOneSentinel(t, err, api.ErrRelayFallenBack)
			result := p.pauseRun(t, false)
			assertOneSentinel(t, result.err, api.ErrSpawnNotPausable)
			p.assertNoTmuxCall(t)
			if pr := p.request(t, storefix.TestRequestTokenA); pr.Decision != "" || pr.ProvenGone() || pr.PaneAnswered() {
				t.Fatalf("report granted authority: %+v", pr)
			}
		})
	}
}

func TestPermissionStuckPaneAnswerClearsReportButKeepsDialogHold(t *testing.T) {
	t.Parallel()
	for _, agent := range paAgents {
		t.Run(agent.name, func(t *testing.T) {
			p := agent.env(t)
			p.enterStuck(t)
			if _, err := p.recordPaneAnswer(p.outside(storefix.TestRequestTokenA, "unknown")); err != nil {
				t.Fatal(err)
			}
			row, err := api.Get(p.st, p.view(), p.r.ID)
			if err != nil || row.State != store.StateCheckPermission || row.StuckInfo != (api.StuckInfo{}) || len(row.UnprovenRequests) != 1 || len(row.PermissionRequests) != 0 {
				t.Fatalf("pane answer=%+v %v", row, err)
			}
			assertDialogMaybeOpen(t, p.sendKeys(p.plain("next")), storefix.TestRequestTokenA, store.StateCheckPermission)
		})
	}
}

func TestPermissionStuckCodexTurnEndPreservesIndependentChildHold(t *testing.T) {
	t.Parallel()
	for _, event := range []string{"Stop", "Interrupt"} {
		t.Run(event, func(t *testing.T) {
			p := newCodexPAEnv(t)
			storefix.SeedRelayRequest(t, p.st, p.r.ID, store.RelayRequest{RequestToken: storefix.TestRequestTokenB, ToolName: "Bash", ToolInput: "{}", AgentID: paSubagentID, MainTurnEndEligible: ptr(false), SettledAt: p.now.Add(time.Hour)})
			p.enterStuck(t)
			// Codex PostToolUse does not supply proof or close a null-tool-use request.
			p.prove(t, toolRan(paToolUseID))
			if pr := p.request(t, storefix.TestRequestTokenA); pr.ProvenGone() || pr.ToolUseID != "" {
				t.Fatalf("Codex invented proof/tool id: %+v", pr)
			}
			p.prove(t, turnEnd())
			if applied := apitest.ApplyAgentHook(t, p.dbPath, p.r.ID, event, "", apitest.HookAgent(store.AgentCodex)); !applied.Applied {
				t.Fatal(applied)
			}
			row, err := api.Get(p.st, p.view(), p.r.ID)
			if err != nil || row.State != store.StateWaiting || row.StuckInfo != (api.StuckInfo{}) {
				t.Fatalf("turn end=%+v %v", row, err)
			}
			child := p.request(t, storefix.TestRequestTokenB)
			if child.ProvenGone() || child.Closed() || !child.AwaitsAnswer() {
				t.Fatalf("turn end removed child hold: %+v", child)
			}
		})
	}
}
