package store

// b.w21: a decided pre-v7 request with an unfinished pane intent also closes
// when its asking launch ends, without completing or replacing its verdict.

import (
	"testing"
	"time"
)

func TestLegacyDecidedIntentClosesWithSpawn(t *testing.T) {
	for _, c := range closers {
		t.Run(c.name, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			if err := agentPermissionRequest(s, relayID, tokenA, "Bash", `{}`, 0, ""); err != nil {
				t.Fatal(err)
			}
			writeIntent(t, s, "allow", time.Now())
			if ok, err := s.DecidePermissionRequest(relayID, tokenA, "deny", DecisionReasonOperator, WriterProcessDecide); err != nil || !ok {
				t.Fatalf("legacy decision = %v, %v", ok, err)
			}
			before := mustRequest(t, s, tokenA)
			if c.unclosed {
				endRelayRowUnclosed(t, path)
			}

			c.close(t, s)

			after := mustRequest(t, s, tokenA)
			if !after.Closed() || after.ProvenGoneHow != ProvenGoneAgentGone {
				t.Errorf("b.w21 decided legacy intent remains unclosed: %+v", after)
			}
			if after.Decision != before.Decision || after.DecisionReason != before.DecisionReason || !after.DecidedAt.Equal(before.DecidedAt) ||
				!after.DeliveredAt.Equal(before.DeliveredAt) || after.PaneAnswer != PaneAnswerIntent || after.PaneAs != before.PaneAs ||
				after.PaneSender != before.PaneSender || !after.PaneIntentAt.Equal(before.PaneIntentAt) {
				t.Errorf("legacy close changed verdict or completed its intent: before %+v, after %+v", before, after)
			}
		})
	}
}
