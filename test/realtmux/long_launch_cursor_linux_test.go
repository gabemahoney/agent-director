//go:build linux

package realtmux_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// b.xhh: the synthetic ACP runtime observes the startup prompt, or a saved
// large ordinary env value on reuse/resume; the receipt alone cannot prove delivery.
func TestLongLaunchCursorProcessDelivery(t *testing.T) {
	for _, verb := range []string{"spawn", "reuse", "resume"} {
		t.Run(verb, func(t *testing.T) {
			payload := strings.Repeat("x", 30000)
			opts := cursorPaneOptions{Script: &cursorfix.Scenario{LoadSupport: true}}
			if verb == "spawn" {
				opts.AgentArgs = []string{payload}
			}
			f, peer, launch := newCursorPaneScenario(t, false, "", opts)
			if verb == "spawn" {
				waitFor(t, "exact long startup prompt reaches ACP runtime", func() bool {
					for _, prompt := range panePrompts(t, peer) {
						if prompt.Text == payload {
							return true
						}
					}
					return false
				}, func() string { return "runtime did not receive the exact 30000-byte prompt" })
				return
			}
			oldPID, err := strconv.Atoi(readFileTrim(t, peer.PID))
			if err != nil {
				t.Fatal(err)
			}
			before := readRow(t, f.DBPath, launch.ID)
			if _, err := f.API.Pause(context.Background(), api.PauseParams{InstanceID: launch.ID}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "original gateway gone", func() bool { return pidGone(int(before.PanePID.(int64))) }, nil)
			// b.ckn / SR-6.1: the last session's gateway can exit before its server.
			waitPidGone(t, int(before.TmuxServerPID.(int64)))
			// Extend the existing runtime's recorder for this one ordinary key.
			// Its JavaScript process writes the value itself, after exec, without
			// depending on a provider accepting any configuration selector.
			record := peer.PID + ".long-value"
			raw, err := os.ReadFile(peer.Program)
			if err != nil {
				t.Fatal(err)
			}
			instrumented := strings.Replace(string(raw), "\n", "\nrequire('fs').writeFileSync("+strconv.Quote(record)+", process.env.LONG_LAUNCH_VALUE || '');\n", 1)
			if err := os.WriteFile(peer.Program, []byte(instrumented), 0700); err != nil {
				t.Fatal(err)
			}
			extra := map[string]string{"LONG_LAUNCH_VALUE": payload}
			if verb == "reuse" {
				f.mustLaunchWith(t, api.SpawnParams{InstanceID: launch.ID, Agent: "cursor", ReuseFinished: true, ExtraEnv: extra})
			} else {
				encoded, err := json.Marshal(extra)
				if err != nil {
					t.Fatal(err)
				}
				db, err := sql.Open("sqlite", f.DBPath)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec("UPDATE spawns SET extra_env=? WHERE instance_id=?", string(encoded), launch.ID)
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.API.Resume(api.ResumeParams{InstanceID: launch.ID}); err != nil {
					t.Fatalf("long Cursor resume must succeed: %s", describe(err))
				}
			}
			waitFor(t, "replacement runtime records exact env value", func() bool {
				got, err := os.ReadFile(record)
				return err == nil && string(got) == payload
			}, func() string {
				return fmt.Sprintf("replacement runtime did not record exact 30000-byte value for %s", verb)
			})
			// b.ckn: the env recorder runs before PID publication. Cleanup must
			// wait for the replacement runtime, even when the old PID file remains.
			waitFor(t, "live replacement runtime publishes its PID", func() bool {
				raw, err := os.ReadFile(peer.PID)
				if err != nil {
					return false
				}
				pid, err := strconv.Atoi(string(raw))
				return err == nil && pid > 0 && pid != oldPID && !pidGone(pid)
			}, func() string {
				raw, err := os.ReadFile(peer.PID)
				return fmt.Sprintf("old PID %d; replacement PID file %q: %v", oldPID, raw, err)
			})
		})
	}
}
