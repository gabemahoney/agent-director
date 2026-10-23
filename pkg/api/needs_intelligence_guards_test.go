package api_test

import (
	"errors"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// getDisplay is api.Get judged against the committed row of p.
func (p *paEnv) getDisplay(t *testing.T, want string, commit apitest.CommitCheck) {
	t.Helper()
	observeDisplay(t, p.st, p.r.ID, want, commit, func() (string, api.StateDetail, error) {
		row, err := api.Get(p.st, p.view(), p.r.ID)
		return row.State, row.StateDetail, err
	})
}

// enterNeedsIntelligence reads once, committing the request's fallback clock,
// then again at the timeout, committing the entry.
func (p *paEnv) enterNeedsIntelligence(t *testing.T) {
	t.Helper()
	p.getDisplay(t, store.StateCheckPermission, func(store.Spawn, store.Spawn) bool {
		return !p.request(t, storefix.TestRequestTokenA).FallbackAt.IsZero()
	})
	p.now = p.now.Add(30 * time.Second)
	p.getDisplay(t, api.StateNeedsIntelligence, apitest.VersionAdvanced)
}

func TestNeedsIntelligencePreservesLiveCommandGuards(t *testing.T) {
	t.Parallel()
	for _, agent := range paAgents {
		t.Run(agent.name, func(t *testing.T) {
			p := agent.env(t)
			p.enterNeedsIntelligence(t)
			assertOneSentinel(t, p.sendKeys(p.plain("1")), api.ErrRelayFallenBack)
			// Under load max_wait_ms 0 may answer its documented ErrStoreBusy, having
			// recorded nothing; repeat it then, for at most 10 s (t2.sp6.rk.1k).
			zero := int64(0)
			var err error
			for start := time.Now(); ; {
				_, err = api.Decide(p.st, p.view(), api.DecideParams{InstanceID: p.r.ID, RequestToken: storefix.TestRequestTokenA, Decision: "allow", MaxWaitMs: &zero})
				if !errors.Is(err, api.ErrStoreBusy) || time.Since(start) >= 10*time.Second {
					break
				}
			}
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

func TestNeedsIntelligencePaneAnswerClearsReportButKeepsDialogHold(t *testing.T) {
	t.Parallel()
	for _, agent := range paAgents {
		t.Run(agent.name, func(t *testing.T) {
			p := agent.env(t)
			p.enterNeedsIntelligence(t)
			if _, err := p.recordPaneAnswer(p.outside(storefix.TestRequestTokenA, "unknown")); err != nil {
				t.Fatal(err)
			}
			row, err := api.Get(p.st, p.view(), p.r.ID)
			if err != nil || row.State != store.StateCheckPermission || row.StateDetail != (api.StateDetail{}) || len(row.UnprovenRequests) != 1 || len(row.PermissionRequests) != 0 {
				t.Fatalf("pane answer=%+v %v", row, err)
			}
			assertDialogMaybeOpen(t, p.sendKeys(p.plain("next")), storefix.TestRequestTokenA, store.StateCheckPermission)
		})
	}
}

func TestNeedsIntelligenceCodexTurnEndPreservesIndependentChildHold(t *testing.T) {
	t.Parallel()
	for _, event := range []string{"Stop", "Interrupt"} {
		t.Run(event, func(t *testing.T) {
			p := newCodexPAEnv(t)
			storefix.SeedRelayRequest(t, p.st, p.r.ID, store.RelayRequest{RequestToken: storefix.TestRequestTokenB, ToolName: "Bash", ToolInput: "{}", AgentID: paSubagentID, MainTurnEndEligible: ptr(false), SettledAt: p.now.Add(time.Hour)})
			p.enterNeedsIntelligence(t)
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
			if err != nil || row.State != store.StateWaiting || row.StateDetail != (api.StateDetail{}) {
				t.Fatalf("turn end=%+v %v", row, err)
			}
			child := p.request(t, storefix.TestRequestTokenB)
			if child.ProvenGone() || child.Closed() || !child.AwaitsAnswer() {
				t.Fatalf("turn end removed child hold: %+v", child)
			}
		})
	}
}
