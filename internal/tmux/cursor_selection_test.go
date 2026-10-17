package tmux_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

func TestCursorCreateSelectionTransport(t *testing.T) {
	keys := []string{"CURSOR_CONFIG_DIR", "CURSOR_DATA_DIR", "CURSOR_API_KEY", "AGENT_CLI_CREDENTIAL_STORE"}
	for _, key := range keys {
		t.Setenv(key, "synthetic-ambient-"+key)
	}
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "parent")
	t.Setenv("E6_UNRELATED_SENTINEL", "preserved")
	for _, agent := range []string{"cursor", "claude", "codex"} {
		for _, selected := range []map[string]string{nil, {"CURSOR_CONFIG_DIR": "/explicit/config with space;", "CURSOR_DATA_DIR": "/explicit/data"}} {
			t.Run(agent, func(t *testing.T) {
				env := map[string]string{"AGENT_DIRECTOR_AGENT": agent}
				for key, value := range selected {
					env[key] = value
				}
				var got tmux.Invocation
				client := tmux.NewWithRunner("", testTimeouts, func(inv tmux.Invocation) tmux.RunResult {
					got = inv
					return exitZero(tmuxfix.CreateReplyLine(tmuxfix.RecordedCreate))
				})
				if _, err := client.NewSession(testSocket, "selected", "/tmp", env, []string{"true"}, execToken, "child", tmuxfix.StoreID); err != nil {
					t.Fatal(err)
				}
				if !slices.Contains(got.Env, "E6_UNRELATED_SENTINEL=preserved") {
					t.Fatal("unrelated client env lost")
				}
				for _, key := range keys {
					inherited := slices.Contains(got.Env, key+"=synthetic-ambient-"+key)
					if inherited != (agent != "cursor") {
						t.Fatalf("%s ambient filter: inherited=%v agent=%s", key, inherited, agent)
					}
					value, present := selected[key]
					want := key
					if present {
						want += "=" + value
						if strings.HasSuffix(want, ";") {
							want = strings.TrimSuffix(want, ";") + `\;`
						}
					}
					found := false
					for i, arg := range got.Args {
						if i > 0 && got.Args[i-1] == "-e" && arg == want {
							found = true
						}
					}
					if found != (present || agent == "cursor") {
						t.Fatalf("%s per-session selector found=%v selected=%v agent=%s argv=%q", key, found, present, agent, got.Args)
					}
				}
			})
		}
	}
}
