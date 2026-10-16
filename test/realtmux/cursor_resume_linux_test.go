//go:build linux

package realtmux_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
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
