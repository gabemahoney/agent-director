package qualification_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// Execute the real typed producer to its two long-tool inputs. Product command
// execution is intercepted; real SQLite observations and recorder facts supply
// the already covered timing gate. This is a scheduling/text unit, no intake.
func TestQualificationTypedWorkloadInputs(t *testing.T) {
	u := newWorkloadUnit(t, "typed-controls")
	stop := errors.New("unit stop before between-tools without another input")
	u.r.Sleep = func(context.Context, time.Duration) error { return stop }
	u.spawn = func() {
		u.hook("PreToolUse", runner.Row{"tool_name": "Bash", "tool_use_id": "unit-initial-long", "is_background": false, "tool_input": runner.Row{"command": strings.Split(q.LongToolForTest, "`")[1], "run_in_background": false}})
	}
	err := q.TypedControls(context.Background(), u.e)
	if !errors.Is(err, stop) {
		t.Fatalf("typed producer did not reach its intended boundary: %v", err)
	}
	texts := map[string]string{}
	for _, c := range u.r.Manifest.Cases {
		if strings.HasPrefix(c.ID, "typed-long-tool-") {
			texts[c.ID] = c.Text
		}
	}
	ad, control := texts["typed-long-tool-ad-input"], texts["typed-long-tool-typed-input"]
	if len(ad) != 8000 || ad != control || !strings.Contains(ad, "TYPED-long-tool:") {
		t.Fatalf("actual matched inputs must carry the same long-tool case at the cap: bytes=%d/%d equal=%t", len(ad), len(control), ad == control)
	}
}
