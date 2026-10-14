package hook

import (
	"encoding/json"
	"fmt"
	"github.com/gabemahoney/agent-director/internal/store"
	"testing"
)

func TestCursorLifecycleAttribution(t *testing.T) {
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd", "PermissionRequest", "Notification"} {
		for _, origin := range []string{"main", "unknown", "child"} {
			for _, child := range []bool{false, true} {
				name := fmt.Sprintf("%s/%s/child=%v", event, origin, child)
				t.Run(name, func(t *testing.T) {
					p := map[string]any{"hook_event_name": event, "origin": origin, "reason": "prompt_input_exit", "session_id": "synthetic-session", "transcript_path": "/protected/synthetic.jsonl"}
					if child {
						p["agent_id"] = "nested-agent"
					}
					raw, _ := json.Marshal(p)
					got, err := ClassifyEvent(store.AgentCursor, raw)
					if err != nil {
						t.Fatal(err)
					}
					if got.SubagentCapture || got.HealsTranscript || got.NewState == store.StateAskUser || got.NewState == store.StateCheckPermission {
						t.Fatalf("Cursor acquired unsupported flow: %+v", got)
					}
					if origin != "main" || child {
						if got.NewState != "" || got.SessionID != "" || got.TranscriptPath != "" {
							t.Fatalf("unknown/child supplied main proof %+v", got)
						}
						return
					}
					want := map[string]string{"SessionStart": store.StateWaiting, "UserPromptSubmit": store.StateWorking, "Stop": store.StateWaiting, "SessionEnd": store.StateEnded}[event]
					if got.NewState != want {
						t.Fatalf("state %q; want %q", got.NewState, want)
					}
				})
			}
		}
	}
}
