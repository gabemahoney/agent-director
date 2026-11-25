package hook_test

// b.3mx: the early peek must share each reporting agent's decode policy,
// including where a failed decode suppresses a relay deny envelope.

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
)

func TestPeekEventNameForAgentNonClassifierTypes(t *testing.T) {
	for _, agent := range []string{"", "claude", "codex", "cursor", "invalid"} {
		for _, key := range hook.ClaudeLenientPayloadFieldsForTest {
			t.Run(agent+"/"+key, func(t *testing.T) {
				raw := payloadWith(t, "permission-request.json", map[string]any{key: "compact"})
				if got := hook.PeekEventNameForAgent(agent, raw); got != "PermissionRequest" {
					t.Errorf("string control: peek = %q; want PermissionRequest", got)
				}
				for _, value := range nonStringPayloadValues {
					t.Run(value.name, func(t *testing.T) {
						raw := payloadWith(t, "permission-request.json", map[string]any{key: value.value})
						want := ""
						if agent == "" || agent == "claude" {
							want = "PermissionRequest"
						}
						if got := hook.PeekEventNameForAgent(agent, raw); got != want {
							t.Errorf("peek = %q; want %q", got, want)
						}
					})
				}
			})
		}
	}
}

func TestRelayNumericSessionIDUsesAgentPeekPolicy(t *testing.T) {
	for _, agent := range []string{"", "claude", "codex", "cursor"} {
		t.Run(agent, func(t *testing.T) {
			const id = "relay-payload-type"
			rowAgent := agent
			if rowAgent == "" {
				rowAgent = "claude"
			}
			st, _ := cxRelayRow(t, id, rowAgent, store.StateWorking)
			before := mustGetSpawn(t, st, id)
			hc := relayHC(id, cxRelayParent(t, st, id, rowAgent), 10*time.Second)
			hc.Agent = agent
			claude := rowAgent == "claude"
			if claude {
				atSleep(t, hc, 1, func() {
					if got := mustGetSpawn(t, st, id).State; got != store.StateCheckPermission {
						t.Errorf("state while polling = %q; want check_permission", got)
					}
					decideOnly(t, st, id, "allow")
				})
			}
			raw := payloadWith(t, "permission-request.json", map[string]any{"session_id": 12345, "origin": "main"})
			var out, logs bytes.Buffer
			if err := hook.Handle(context.Background(), bytes.NewReader(raw), &out, st, hc, log.New(&logs, "", 0)); err != nil {
				t.Fatalf("Handle = %v; want fail-open nil", err)
			}
			if claude {
				want := hook.EncodeDecision(hook.EventNamePermissionRequest, "allow", "") + "\n"
				if out.String() != want {
					t.Errorf("stdout = %q; want %q", out.String(), want)
				}
				if strings.Contains(logs.String(), "hook: classify") {
					t.Errorf("Claude classification failed: %s", logs.String())
				}
				if req := onlyRequest(t, st, id); req.Decision != "allow" || req.DeliveredAt.IsZero() {
					t.Errorf("request = decision %q, delivered %v; want allow and acknowledged", req.Decision, req.DeliveredAt)
				}
				return
			}
			if out.Len() != 0 {
				t.Errorf("strict decode stdout = %q; want empty (no deny envelope)", out.String())
			}
			wantLog := "hook: classify (instance=" + id + "): json: cannot unmarshal number into Go struct field payload.session_id of type string\n"
			if logs.String() != wantLog {
				t.Errorf("log = %q; want %q", logs.String(), wantLog)
			}
			assertNoRequests(t, st, id)
			assertRowUnchanged(t, st, id, before)
		})
	}
}
