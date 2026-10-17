//go:build linux

package cursorhost

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
)

func TestCursorOwnedRuntimeSelection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		selected map[string]string
	}{{"absent", nil}, {"both directories", map[string]string{"CURSOR_CONFIG_DIR": "/selected/config with spaces", "CURSOR_DATA_DIR": "/selected/data"}}, {"config only", map[string]string{"CURSOR_CONFIG_DIR": "/selected/config"}}} {
		t.Run(tc.name, func(t *testing.T) {
			selected := tc.selected
			for _, key := range []string{"CURSOR_CONFIG_DIR", "CURSOR_DATA_DIR", "CURSOR_API_KEY", "AGENT_CLI_CREDENTIAL_STORE"} {
				t.Setenv(key, "synthetic-ambient-"+key)
			}
			c := beginCursorLoad(t, cursorfix.Scenario{LoadSupport: true}, "", terminalLoop, func(e *requestEnv) {
				raw, _ := json.Marshal(selected)
				e.exec(t, `UPDATE spawns SET extra_env=? WHERE instance_id=?`, string(raw), e.binding.InstanceID)
			})
			c.wait(t, "protected runtime loaded", func() bool { return strings.Contains(string(c.pane.bytes()), "conversation loaded") })
			got := c.e.runtime.Selection(t)
			for _, key := range []string{"CURSOR_CONFIG_DIR", "CURSOR_DATA_DIR", "CURSOR_API_KEY", "AGENT_CLI_CREDENTIAL_STORE"} {
				want, present := selected[key]
				if present {
					if got[key] == nil || *got[key] != want {
						t.Fatalf("%s: received %v want selected %q", key, got[key], want)
					}
				} else if got[key] != nil {
					t.Fatalf("ambient %s inherited", key)
				}
			}
		})
	}
}

func TestCursorSavedProfileNeverStartsRuntime(t *testing.T) {
	for _, selected := range []map[string]string{{"CURSOR_API_KEY": "synthetic-private-secret"}, {"AGENT_CLI_CREDENTIAL_STORE": ""}, {"CURSOR_CONFIG_DIR": "relative"}, {"E6_INVALID_OWNER": "synthetic"}} {
		t.Run("saved refusal", func(t *testing.T) {
			c := beginCursorLoad(t, cursorfix.Scenario{LoadSupport: true}, "must never dispatch", terminalLoop, func(e *requestEnv) {
				raw, _ := json.Marshal(selected)
				e.exec(t, `UPDATE spawns SET extra_env=? WHERE instance_id=?`, string(raw), e.binding.InstanceID)
				if selected["E6_INVALID_OWNER"] != "" {
					e.exec(t, `UPDATE spawns SET pane_pid=pane_pid+1 WHERE instance_id=?`, e.binding.InstanceID)
				}
				for _, path := range []string{e.runtime.PID, e.runtime.PID + ".selection.json", e.runtime.Trace} {
					if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
				}
			})
			select {
			case err := <-c.done:
				c.ended = true
				if err == nil || strings.Contains(err.Error(), "synthetic-private-secret") {
					t.Fatalf("refusal=%v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("saved profile refusal unbounded")
			}
			if _, err := os.Stat(c.e.runtime.PID); !os.IsNotExist(err) {
				t.Fatalf("refused runtime started: %v", err)
			}
			if len(c.e.runtime.Frames(t)) != 0 {
				t.Fatal("refused profile exchanged ACP")
			}
		})
	}
}
