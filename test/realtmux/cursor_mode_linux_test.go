//go:build linux

package realtmux_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// Actual tmux/public clients and synthetic ACP; no native Cursor mode claim.
func TestCursorModePublicSavedReplayAndDeliberateRepair(t *testing.T) {
	// Serial: the existing real-pane fixture owns HOME/PATH and tmux environment.
	for _, surface := range []string{"Go", "Bun"} {
		t.Run(surface, func(t *testing.T) {
			initial := "--resume\n多行 🦋"
			modes := json.RawMessage(`{"currentModeId":"agent","availableModes":[{"id":"agent","name":"Agent"},{"id":"plan","name":"Plan"}]}`)
			f, r, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{AgentArgs: []string{"--plan", "--", initial}, Script: &cursorfix.Scenario{Modes: modes}})
			caller := newPaneCaller(t, f, surface, filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "bin", "agent-director"))
			selected := map[string]string{"CURSOR_CONFIG_DIR": filepath.Join(t.TempDir(), "repair config"), "CURSOR_DATA_DIR": filepath.Join(t.TempDir(), "repair data")}
			preserveSelection := false
			// Follow the final product commands verbatim. The function only routes
			// the CLI to this fixture's private store; cwd/PWD is the project dir.
			docSpawn := func(doc, command string) api.SpawnResult {
				t.Helper()
				text, err := os.ReadFile(filepath.Join("../..", doc))
				if err != nil || !strings.Contains(string(text), command) {
					t.Fatalf("literal command absent from %s: %v", doc, err)
				}
				script := `agent-director() { local verb="$1"; shift; "$E5_POSTDOC_CLI" "$verb" --store-path "$E5_POSTDOC_STORE" "$@"; }
` + command
				if preserveSelection {
					script += ` --extra-env "CURSOR_CONFIG_DIR=$E6_RECOVERY_CONFIG" --extra-env "CURSOR_DATA_DIR=$E6_RECOVERY_DATA"`
				}
				cmd := exec.Command("bash", "-c", script)
				cmd.Dir = f.CWD
				cmd.Env = append(os.Environ(), "E5_POSTDOC_CLI="+filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "bin", "agent-director"), "E5_POSTDOC_STORE="+f.DBPath)
				cmd.Env = append(cmd.Env, "E6_RECOVERY_CONFIG="+selected["CURSOR_CONFIG_DIR"], "E6_RECOVERY_DATA="+selected["CURSOR_DATA_DIR"])
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				out, err := cmd.Output()
				var result api.SpawnResult
				if err != nil || json.Unmarshal(out, &result) != nil || result.InstanceID == "" {
					t.Fatalf("literal %s: %v stdout=%s stderr=%s", command, err, out, stderr.String())
				}
				return result
			}
			waitComplete := func(id string) {
				waitFor(t, "current turn completion", func() bool {
					pane, e := f.API.ReadPane(api.ReadPaneParams{InstanceID: id, NLines: 100})
					return e == nil && strings.Contains(pane.Pane, "Cursor turn complete")
				}, func() string { return fmt.Sprint(readRow(t, f.DBPath, id)) })
			}
			pause := func(id string) {
				before := readRow(t, f.DBPath, id)
				if _, err := f.API.Pause(context.Background(), api.PauseParams{InstanceID: id}); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "owned gateway stopped", func() bool { return pidGone(int(before.PanePID.(int64))) }, func() string { return fmt.Sprint(readRow(t, f.DBPath, id)) })
			}
			waitComplete(launch.ID)
			saved := readRow(t, f.DBPath, launch.ID)
			pause(launch.ID)
			r.Configure(t, cursorfix.Scenario{LoadSupport: true, Modes: modes})
			caller.must("resume", map[string]any{"instance_id": launch.ID})
			waitComplete(launch.ID)
			pause(launch.ID)
			var methods, prompts []string
			for _, frame := range r.Frames(t) {
				if len(frame["method"]) > 0 {
					methods = append(methods, string(frame["method"]))
				}
				if string(frame["method"]) == `"session/prompt"` {
					var p struct {
						Prompt []struct {
							Text string `json:"text"`
						} `json:"prompt"`
					}
					if err := json.Unmarshal(frame["params"], &p); err != nil || len(p.Prompt) != 1 {
						t.Fatalf("prompt frame: %s %v", frame["params"], err)
					}
					prompts = append(prompts, p.Prompt[0].Text)
				}
			}
			if !reflect.DeepEqual(prompts, []string{initial, initial}) || strings.Join(methods, ",") != `"initialize","session/new","session/set_mode","session/prompt","initialize","session/load","session/set_mode","session/prompt"` {
				t.Fatalf("public mode/replay order=%v prompts=%q", methods, prompts)
			}
			after := readRow(t, f.DBPath, launch.ID)
			if after.AgentArgs != saved.AgentArgs || after.SessionID != saved.SessionID {
				t.Fatalf("saved configuration changed: before=%+v after=%+v", saved, after)
			}
			r.Configure(t, cursorfix.Scenario{})
			literal := docSpawn("docs/usage.md", `agent-director spawn --cwd "$PWD" --agent cursor -- -- "--explain this project's entry points"`)
			waitComplete(literal.InstanceID)
			pause(literal.InstanceID)
			var literalPrompts []string
			for _, prompt := range panePrompts(t, r) {
				literalPrompts = append(literalPrompts, prompt.Text)
			}
			if !reflect.DeepEqual(literalPrompts, []string{initial, initial, "--explain this project's entry points"}) {
				t.Fatalf("literal protected prompt must reach ACP exactly once: %q", literalPrompts)
			}
			// A late refusal is asynchronous launch failure. The caller explicitly
			// starts a new id without mode after observing process-backed missing.
			for _, failure := range []string{"missing state", "RPC rejection"} {
				modeArgs := []string{"--plan"}
				scenario := cursorfix.Scenario{}
				want := "mode state unavailable; remove --plan, or --mode and its value, then retry"
				if failure == "RPC rejection" {
					modeArgs = []string{"--mode", "plan"}
					scenario.Modes = modes
					scenario.ModeReply = json.RawMessage(`{"error":{"code":-32601,"message":"unsupported"}}`)
					want = "requested mode configuration failed; remove --plan, or --mode and its value, then retry"
				}
				r.Configure(t, scenario)
				failedID := newInstanceID("mode-failed")
				caller.must("spawn", map[string]any{"instance_id": failedID, "agent": "cursor", "cwd": f.CWD, "no_pre_trust": true, "agent_args": modeArgs, "extra_env": selected})
				waitFor(t, "late failed launch reconciled", func() bool { row, e := f.API.Get(failedID); return e == nil && row.State == store.StateMissing }, func() string { return fmt.Sprint(readRow(t, f.DBPath, failedID)) })
				trail, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "ad-trail.jsonl"))
				if err != nil || !strings.Contains(string(trail), want) {
					t.Fatalf("late repair instruction absent: %v %s", err, trail)
				}
				failed := readRow(t, f.DBPath, failedID)
				pane, paneErr := f.API.ReadPane(api.ReadPaneParams{InstanceID: failedID, NLines: 100})
				t.Logf("failed pane inspection: pane=%q error=%v", pane.Pane, paneErr)
				failures := hookTrailRecords(t, os.Getenv("HOME"), "ad.cursor.failed", failedID)
				if len(failures) != 1 || !strings.Contains(fmt.Sprint(failures[0]["reason"]), want) {
					t.Fatalf("inspect exact failed event: %v", failures)
				}
				if failed.SessionID != nil {
					t.Fatalf("late refusal published session: %+v", failed)
				}
				r.Configure(t, cursorfix.Scenario{})
				beforeRepair := len(r.Frames(t))
				preserveSelection = true
				repairedID := docSpawn("docs/caller-contract.md", `agent-director spawn --agent cursor --cwd "$PWD"`).InstanceID
				if repairedID == failedID {
					t.Fatal("fresh procedure reused failed id")
				}
				waitFor(t, "fresh default-mode ready", func() bool { row, err := f.API.Get(repairedID); return err == nil && row.State == store.StateWaiting }, nil)
				for key, want := range selected {
					got := r.Selection(t)[key]
					if got == nil || *got != want {
						t.Fatalf("mode remedy dropped %s: %v want %q", key, got, want)
					}
				}
				pause(repairedID)
				for _, frame := range r.Frames(t)[beforeRepair:] {
					if string(frame["method"]) == `"session/set_mode"` || string(frame["method"]) == `"session/prompt"` {
						t.Fatalf("removing entire mode group left a mode or accidental prompt: %s", frame["method"])
					}
				}
				if row := readRow(t, f.DBPath, failedID); !reflect.DeepEqual(row, failed) {
					t.Fatalf("deliberate repair rewrote failed launch: %+v", row)
				}
			}
		})
	}
}
