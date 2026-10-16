//go:build linux

package realtmux_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.jf1 E4: public callers return launch truth while actual synthetic ACP load is withheld.
func TestCursorResumePublicLaunchPendingParity(t *testing.T) {
	if testing.Short() {
		t.Skip("real tmux synthetic ACP public resume")
	}
	for _, surface := range []string{"Go", "Go release wrapper", "Go release wrapper fresh", "CLI", "MCP"} {
		t.Run(surface, func(t *testing.T) {
			f, runtime, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{StandaloneGo: surface == "Go release wrapper fresh"})
			id := launch.ID
			before := readRow(t, f.DBPath, id)
			assertResumeGatewayProcess(t, f, id, filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "bin", "agent-director"), "")
			native := cursorfix.NewNativeHistory(t, f.CWD, before.SessionID.(string), "older-retained")
			if _, err := f.API.Pause(context.Background(), api.PauseParams{InstanceID: id}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "original gateway exited", func() bool { return pidGone(int(before.PanePID.(int64))) }, func() string { return fmt.Sprint(readRow(t, f.DBPath, id)) })
			runtime.Configure(t, cursorfix.Scenario{LoadSupport: true, WaitForLoad: true, NativeConfigRoot: native.ConfigRoot})
			callerBin := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "bin", "agent-director")
			if surface == "CLI" {
				callerBin = buildHookCLI(t, t.TempDir())
			}
			adapter := surface
			if surface == "Go release wrapper fresh" {
				adapter = "Go release wrapper"
			}
			caller := newPaneCaller(t, f, adapter, callerBin)
			started := time.Now()
			raw := caller.must("resume", map[string]any{"instance_id": id})
			if time.Since(started) >= 30*time.Second {
				t.Fatal("public resume whole call exceeded default client bound")
			}
			var result api.ResumeResult
			if err := json.Unmarshal(raw, &result); err != nil || result.InstanceID != id || result.PreTrust != "skipped" {
				t.Fatalf("resume launch result: %s %v", raw, err)
			}
			waitFor(t, "latest load received", func() bool {
				for _, frame := range runtime.Frames(t) {
					if string(frame["method"]) == `"session/load"` {
						return true
					}
				}
				return false
			}, func() string { return fmt.Sprint(runtime.Frames(t)) })
			assertResumeGatewayProcess(t, f, id, callerBin, before.SessionID.(string))
			var pending map[string]any
			if err := json.Unmarshal(caller.must("get", map[string]any{"instance_id": id}), &pending); err != nil || pending["state"] != "pending" || pending["session_id"] != before.SessionID {
				t.Fatalf("load manufactured readiness: %v %v", pending, err)
			}
			var subagents api.ListSubagentsResult
			if err := json.Unmarshal(caller.must("list-subagents", map[string]any{"instance_id": id}), &subagents); err != nil || subagents.Tracked || subagents.Subagents == nil || len(subagents.Subagents) != 0 {
				t.Fatalf("unsupported subagents claimed: %+v %v", subagents, err)
			}
			bad, err := caller.call("resume", map[string]any{"instance_id": "unknown-" + id})
			if !errors.Is(err, api.ErrSpawnNotFound) && !strings.Contains(string(bad)+fmt.Sprint(err), "ErrSpawnNotFound") {
				t.Fatalf("unknown id parity: %s %v", bad, err)
			}
			if err := os.WriteFile(runtime.NewRelease, nil, 0600); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "loaded pending", func() bool {
				pane, e := f.API.ReadPane(api.ReadPaneParams{InstanceID: id, NLines: 100})
				return e == nil && strings.Contains(pane.Pane, "conversation loaded")
			}, func() string { return fmt.Sprint(readRow(t, f.DBPath, id)) })
			caller.must("send-keys", map[string]any{"instance_id": id, "text": "real resumed prompt", "allow_pending": true})
			waitFor(t, "real prompt completes", func() bool { return readRow(t, f.DBPath, id).State == store.StateWaiting }, func() string { return fmt.Sprint(readRow(t, f.DBPath, id)) })
			if _, err := f.API.Pause(context.Background(), api.PauseParams{InstanceID: id}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// E4 AC5/6: actual gateways reuse fresh; the final switch starts from a labelled synthetic Claude row.
func TestCursorReusePublicFreshConversationParity(t *testing.T) {
	if testing.Short() {
		t.Skip("real tmux synthetic ACP public reuse")
	}
	for _, surface := range []string{"Go", "CLI", "MCP", "Bun"} {
		t.Run(surface, func(t *testing.T) {
			f, runtime, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{})
			initial := readRow(t, f.DBPath, launch.ID)
			if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "/new"}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "reported rotation before reuse", func() bool {
				row := readRow(t, f.DBPath, launch.ID)
				return row.State == store.StateWaiting && row.SessionID != initial.SessionID
			}, func() string { return fmt.Sprint(readRow(t, f.DBPath, launch.ID)) })
			child, err := apitest.SeedSpawn(f.DBPath, "", store.StateEnded, f.CWD, "off", "independent-child", false)
			if err != nil {
				t.Fatal(err)
			}
			if err := apitest.SeedParentChild(f.DBPath, launch.ID, child); err != nil {
				t.Fatal(err)
			}
			childBefore := readRow(t, f.DBPath, child)
			retained := []string{initial.SessionID.(string)}
			for reuse := 0; reuse < 3; reuse++ {
				before := readRow(t, f.DBPath, launch.ID)
				decoyPath := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "state.db")
				if reuse == 0 {
					if _, err := apitest.SeedSpawn(decoyPath, launch.ID, store.StateWaiting, f.CWD, "off", "unrelated-session", false, apitest.WithAgent(store.AgentCursor)); err != nil {
						t.Fatal(err)
					}
				}
				decoy := readRow(t, decoyPath, launch.ID)
				if _, err := f.API.Pause(context.Background(), api.PauseParams{InstanceID: launch.ID}); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "old gateway exited", func() bool { return pidGone(int(before.PanePID.(int64))) }, func() string { return fmt.Sprint(readRow(t, f.DBPath, launch.ID)) })
				runtime.Configure(t, cursorfix.Scenario{SessionID: before.SessionID.(string) + "-reused"})
				params := map[string]any{"instance_id": launch.ID, "agent": "cursor", "cwd": f.CWD, "reuse_finished": true, "no_pre_trust": true}
				wantLife := before.LifeNumber.(int64)
				var drops []string
				var failMarker string
				if reuse == 2 {
					// Synthetic finished-agent switch eligibility only: no Claude execution, resume, or native artifact claim.
					db, err := sql.Open("sqlite", f.DBPath)
					if err != nil {
						t.Fatal(err)
					}
					_, err = db.Exec("UPDATE spawns SET agent='claude',subagent_tracking=1,row_version=row_version+1 WHERE instance_id=? AND state='ended'", launch.ID)
					closeErr := db.Close()
					if err != nil || closeErr != nil {
						t.Fatalf("synthetic finished-agent seed: %v %v", err, closeErr)
					}
					before = readRow(t, f.DBPath, launch.ID)
					if before.Agent != "claude" {
						t.Fatal("synthetic finished-agent seed did not apply")
					}
					wantLife++
					retained = nil
					drops = []string{"--resume", "old-selector", "--continue"}
					params["agent_args"] = drops
					realTmux, err := exec.LookPath("tmux")
					if err != nil {
						t.Fatal(err)
					}
					dir := t.TempDir()
					failMarker = filepath.Join(dir, "fail-create")
					script := fmt.Sprintf("#!/bin/sh\nif [ -f %q ]; then for value in \"$@\"; do if [ \"$value\" = new-session ]; then exit 1; fi; done; fi\nexec %q \"$@\"\n", failMarker, realTmux)
					if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0700); err != nil {
						t.Fatal(err)
					}
					t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				}
				caller := newPaneCaller(t, f, surface, filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "bin", "agent-director"))
				if reuse == 2 {
					if err := os.WriteFile(failMarker, nil, 0600); err != nil {
						t.Fatal(err)
					}
					raw, err := caller.call("spawn", params)
					message := string(raw) + fmt.Sprint(err)
					if !errors.Is(err, api.ErrTmuxSessionCreate) && !strings.Contains(message, "ErrTmuxSessionCreate") {
						t.Fatalf("public determinate error: %s", message)
					}
					if !strings.Contains(message, "the row was restored to its prior state, ended") {
						t.Fatalf("public restore advice: %s", message)
					}
					want := before
					want.LaunchStartedAt = nil
					want.RowVersion = before.RowVersion.(int64) + 2
					if got := readRow(t, f.DBPath, launch.ID); !reflect.DeepEqual(got, want) {
						t.Fatalf("public full restore: got=%+v want=%+v", got, want)
					}
					if err := os.Remove(failMarker); err != nil {
						t.Fatal(err)
					}
				}
				var result api.SpawnResult
				raw := caller.must("spawn", params)
				if err := json.Unmarshal(raw, &result); err != nil || result.InstanceID != launch.ID || result.PreTrust != "skipped" || !slices.Equal(result.DroppedArgs, drops) {
					t.Fatalf("public reuse: %s %v", raw, err)
				}
				waitFor(t, "fresh conversation", func() bool {
					row := readRow(t, f.DBPath, launch.ID)
					return row.State == store.StateWaiting && row.SessionID != nil && row.SessionID != before.SessionID
				}, func() string {
					pane, paneErr := f.API.ReadPane(api.ReadPaneParams{InstanceID: launch.ID, NLines: 100})
					return fmt.Sprintf("row=%+v pane=%+v pane_error=%v frames=%+v", readRow(t, f.DBPath, launch.ID), pane, paneErr, runtime.Frames(t))
				})
				assertResumeGatewayProcess(t, f, launch.ID, filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "bin", "agent-director"), "")
				after := readRow(t, f.DBPath, launch.ID)
				if after.LifeNumber != wantLife || after.LaunchToken == before.LaunchToken {
					t.Fatalf("reuse life/token: before=%+v after=%+v", before, after)
				}
				for _, frame := range runtime.Frames(t) {
					if string(frame["method"]) == `"session/load"` {
						t.Fatal("fresh reuse loaded old conversation")
					}
				}
				var subs api.ListSubagentsResult
				if err := json.Unmarshal(caller.must("list-subagents", map[string]any{"instance_id": launch.ID}), &subs); err != nil || subs.Tracked || subs.Subagents == nil || len(subs.Subagents) != 0 {
					t.Fatalf("unsupported subagents: %+v %v", subs, err)
				}
				var view api.SpawnRow
				if err := json.Unmarshal(caller.must("get", map[string]any{"instance_id": launch.ID}), &view); err != nil {
					t.Fatal(err)
				}
				if reuse != 2 {
					retained = append(retained, before.SessionID.(string))
				}
				if len(view.PriorSessions) != len(retained) {
					t.Fatalf("same-life history: %+v want %v", view.PriorSessions, retained)
				}
				for _, session := range retained {
					found := false
					for _, h := range view.PriorSessions {
						if h.SessionID == view.SessionID {
							t.Fatal("current session appears in prior history")
						}
						if h.SessionID == session {
							found = true
						}
					}
					if !found {
						t.Fatalf("same-life history lost %s: %+v", session, view)
					}
				}
				var children api.ListResult
				if err := json.Unmarshal(caller.must("list", map[string]any{"parent": launch.ID}), &children); err != nil || len(children.Spawns) != 1 || children.Spawns[0].InstanceID != child {
					t.Fatalf("parent listing: %+v %v", children, err)
				}
				if !reflect.DeepEqual(childBefore, readRow(t, f.DBPath, child)) {
					t.Fatal("parent reuse mutated independent child")
				}
				if _, err := f.API.Pause(context.Background(), api.PauseParams{InstanceID: launch.ID}); err != nil {
					t.Fatal(err)
				}
				if got := readRow(t, decoyPath, launch.ID); !reflect.DeepEqual(got, decoy) {
					t.Fatal("public reuse changed same-ID default-store decoy")
				}
			}
		})
	}
}

func assertResumeGatewayProcess(t *testing.T, f *spawnFix, id, bin, session string) {
	t.Helper()
	row := readRow(t, f.DBPath, id)
	pid := int(row.PanePID.(int64))
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatal(err)
	}
	if exe != want {
		t.Fatalf("gateway executable %q want actual CLI %q", exe, want)
	}
	argv, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argv), "\x00cursor-host\x00") || (session != "" && !strings.Contains(string(argv), "\x00--load-session\x00"+session+"\x00")) || (session == "" && strings.Contains(string(argv), "\x00--load-session\x00")) {
		t.Fatalf("gateway argv %q", argv)
	}
	if path, ok := envValue(procEnviron(t, pid), "AGENT_DIRECTOR_CURSOR_STORE_PATH"); !ok || path != f.DBPath {
		t.Fatalf("gateway store=%q want actual override %q", path, f.DBPath)
	}
}
