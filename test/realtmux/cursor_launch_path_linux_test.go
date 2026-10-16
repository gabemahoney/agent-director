//go:build linux

package realtmux_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Synthetic ACP peers prove actual executable selection through public callers and real tmux.
func TestCursorCurrentRuntimePublicPaths(t *testing.T) {
	if testing.Short() {
		t.Skip("real tmux current runtime paths")
	}
	for _, surface := range []string{"Go", "CLI", "MCP", "Bun"} {
		for _, verb := range []string{"spawn", "reuse", "resume"} {
			t.Run(surface+"/"+verb, func(t *testing.T) {
				f, first, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{ProgramName: `initial "agent" --flag`})
				before := readRow(t, f.DBPath, launch.ID)
				assertCursorRuntimeProgram(t, first, first.Program)
				if _, err := f.API.Pause(context.Background(), api.PauseParams{InstanceID: launch.ID}); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "old gateway gone", func() bool { return pidGone(int(before.PanePID.(int64))) }, func() string { return fmt.Sprint(readRow(t, f.DBPath, launch.ID)) })
				second := cursorfix.New(t)
				selected := filepath.Join(filepath.Dir(second.Program), `current "agent" --flag`)
				if err := os.Rename(second.Program, selected); err != nil {
					t.Fatal(err)
				}
				second.Program = selected
				second.Configure(t, cursorfix.Scenario{SessionID: "new-current-path", LoadSupport: true})
				configPath := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "config.toml")
				// Literal TOML keeps double quotes as whole path content without escapes.
				configText := "[launch]\ncursor_path = '" + selected + "'\n"
				if surface == "CLI" || surface == "Bun" {
					tools := t.TempDir()
					pathAgent := filepath.Join(tools, "agent")
					if err := os.Symlink(selected, pathAgent); err != nil {
						t.Fatal(err)
					}
					t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
					selected = pathAgent
					configText = "[launch]\ncursor_path = ''\n"
				}
				if err := os.WriteFile(configPath, []byte(configText), 0600); err != nil {
					t.Fatal(err)
				}
				caller := newPaneCaller(t, f, surface, filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "bin", "agent-director"))
				params := map[string]any{"instance_id": launch.ID, "agent": "cursor", "cwd": before.CWD.(string), "no_pre_trust": true}
				call := "spawn"
				if verb == "resume" {
					call = "resume"
					params = map[string]any{"instance_id": launch.ID}
				} else if verb == "reuse" {
					params["reuse_finished"] = true
				} else {
					params["instance_id"] = newInstanceID("current-path")
				}
				caller.must(call, params)
				id := params["instance_id"].(string)
				waitFor(t, "selected runtime protocol effect", func() bool { return len(second.Frames(t)) >= 2 }, func() string { return fmt.Sprint(second.Frames(t)) })
				assertCursorRuntimeProgram(t, second, selected)
				frames := second.Frames(t)
				method := "session/new"
				if verb == "resume" {
					method = "session/load"
				}
				found := false
				for _, frame := range frames {
					if string(frame["method"]) == fmt.Sprintf("%q", method) {
						found = true
						var p struct {
							CWD       string `json:"cwd"`
							SessionID string `json:"sessionId"`
						}
						if err := json.Unmarshal(frame["params"], &p); err != nil {
							t.Fatal(err)
						}
						if p.CWD != before.CWD || (verb == "resume" && p.SessionID != before.SessionID) {
							t.Fatalf("runtime parameters: %+v; saved=%+v", p, before)
						}
					}
				}
				if !found {
					t.Fatalf("selected runtime never received %s: %+v", method, frames)
				}
				ready := "Cursor ready"
				if verb == "resume" {
					ready = "conversation loaded"
				}
				waitFor(t, "selected gateway input ready", func() bool {
					pane, err := f.API.ReadPane(api.ReadPaneParams{InstanceID: id, NLines: 100})
					return err == nil && strings.Contains(pane.Pane, ready)
				}, func() string { return fmt.Sprint(readRow(t, f.DBPath, id)) })
				caller.must("send-keys", map[string]any{"instance_id": id, "text": "selected runtime effect", "allow_pending": verb == "resume"})
				waitFor(t, "selected runtime completed prompt", func() bool { return readRow(t, f.DBPath, id).State == store.StateWaiting }, func() string { return fmt.Sprint(second.Frames(t)) })
				if verb == "resume" {
					after := readRow(t, f.DBPath, id)
					if after.CWD != before.CWD || after.AgentArgs != before.AgentArgs || after.ExtraEnv != before.ExtraEnv || after.SessionID != before.SessionID || after.LifeNumber != before.LifeNumber {
						t.Fatalf("saved values changed: before=%+v after=%+v", before, after)
					}
				}
				caller.must("pause", map[string]any{"instance_id": id})
				pid, err := strconv.Atoi(readFileTrim(t, second.PID))
				if err != nil {
					t.Fatal(err)
				}
				waitFor(t, "selected runtime gone", func() bool { return pidGone(pid) }, func() string { return procState(pid) })
			})
		}
	}
}

func assertCursorRuntimeProgram(t *testing.T, runtime cursorfix.Runtime, program string) {
	t.Helper()
	pid, err := strconv.Atoi(readFileTrim(t, runtime.PID))
	if err != nil {
		t.Fatal(err)
	}
	argv, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || !strings.Contains(string(argv), "\x00"+program+"\x00") {
		t.Fatalf("actual runtime argv %q, %v; want whole %q", argv, err, program)
	}
}

func TestCursorUnsetRuntimeUsesPath(t *testing.T) {
	if testing.Short() {
		t.Skip("real tmux PATH runtime")
	}
	f, runtime, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{UnsetPath: true})
	if len(runtime.Frames(t)) < 2 {
		t.Fatal("PATH agent had no protocol effect")
	}
	if _, err := f.API.Pause(context.Background(), api.PauseParams{InstanceID: launch.ID}); err != nil {
		t.Fatal(err)
	}
}

func TestCursorBadRuntimeDoesNotFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("real tmux missing runtime")
	}
	for _, kind := range []string{"missing", "nonexecutable", "directory"} {
		for _, verb := range []string{"spawn", "reuse", "resume"} {
			t.Run(kind+"/"+verb, func(t *testing.T) {
				f, runtime, launch := newCursorPane(t, false)
				before := readRow(t, f.DBPath, launch.ID)
				if _, err := f.API.Pause(context.Background(), api.PauseParams{InstanceID: launch.ID}); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "original gateway gone", func() bool { return pidGone(int(before.PanePID.(int64))) }, func() string { return fmt.Sprint(readRow(t, f.DBPath, launch.ID)) })
				tools := t.TempDir()
				if err := os.Symlink(runtime.Program, filepath.Join(tools, "agent")); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
				frames := runtime.Frames(t)
				bad := filepath.Join(t.TempDir(), "bad-runtime")
				if kind == "nonexecutable" {
					if err := os.WriteFile(bad, []byte("fixture"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "directory" {
					if err := os.Mkdir(bad, 0700); err != nil {
						t.Fatal(err)
					}
				}
				apitest.WriteKeysConfig(t, filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "config.toml"), apitest.ConfigKeys{CursorPath: bad})
				caller := newPaneCaller(t, f, "CLI", filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "bin", "agent-director"))
				params := map[string]any{"instance_id": launch.ID, "agent": "cursor", "cwd": f.CWD, "no_pre_trust": true}
				call := "spawn"
				if verb == "resume" {
					call = "resume"
					params = map[string]any{"instance_id": launch.ID}
				} else if verb == "reuse" {
					params["reuse_finished"] = true
				} else {
					params["instance_id"] = newInstanceID("bad-path")
				}
				caller.must(call, params) // Launch succeeds; runtime startup is asynchronous.
				id := params["instance_id"].(string)
				waitFor(t, "failed runtime is process-backed missing", func() bool { row, err := f.API.Get(id); return err == nil && row.State == store.StateMissing }, func() string { return fmt.Sprint(readRow(t, f.DBPath, id)) })
				if !reflect.DeepEqual(frames, runtime.Frames(t)) {
					t.Fatal("bad configured runtime fell back to PATH agent")
				}
				if failures := hookTrailRecords(t, os.Getenv("HOME"), "ad.cursor.failed", id); len(failures) != 1 {
					t.Fatalf("missing asynchronous failure: %+v", failures)
				}
			})
		}
	}
}
