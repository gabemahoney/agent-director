//go:build linux

package spawn

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
)

// b.xhh: preflight measures the composed exec entries, including a string's
// NUL, KEY= overhead and synthesized label environment, before trust/insert.
func TestLaunchSizeComposedEntriesPreWrite(t *testing.T) {
	for _, kind := range []string{"argument including NUL", "environment including key equals NUL", "synthesized label environment"} {
		t.Run(kind, func(t *testing.T) {
			e := newLaunchEnv(t)
			e.r.InstanceID = "size-" + uuid.NewString()
			trust := withStubClaudeJSON(t)
			seedFile(t, trust, `{"projects":{}}`)
			switch kind {
			case "argument including NUL":
				e.r.AgentArgs = []string{"--model", strings.Repeat("x", 131072)}
			case "environment including key equals NUL":
				e.r.ExtraEnv = map[string]string{"PAYLOAD": strings.Repeat("x", 131072-len("PAYLOAD")-1)}
			case "synthesized label environment":
				e.r.AgentDirectorLabels = map[string]string{"role": strings.Repeat("x", 131071)}
			}
			id, outcome, err := e.launch()
			if err == nil || id != "" || outcome != "" {
				t.Errorf("over-limit Launch: err=%v id=%q outcome=%q; want refusal without a result", err, id, outcome)
			}
			if _, err := e.s.GetSpawn(e.r.InstanceID); !errors.Is(err, store.ErrSpawnNotFound) {
				t.Errorf("over-limit Launch wrote a row: %v", err)
			}
			if n := len(e.rec.SocketCalls()); n != 0 {
				t.Errorf("over-limit Launch made %d tmux calls", n)
			}
			if got, readErr := os.ReadFile(trust); readErr != nil || string(got) != `{"projects":{}}` {
				t.Error("over-limit Launch changed pre-trust")
			}
		})
	}
}
