//go:build linux

package realtmux_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime/pprof"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
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
	for _, surface := range []string{"Go", "Go release wrapper", "Go release wrapper fresh", "CLI", "MCP", "Bun installed"} {
		t.Run(surface, func(t *testing.T) {
			f, runtime, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{StandaloneGo: surface == "Go release wrapper fresh"})
			diagnostics := cursorFailureDiagnostics{t: t, f: f, peer: runtime, id: launch.ID, home: os.Getenv("HOME")}
			defer diagnostics.capture()
			id := launch.ID
			before := readRow(t, f.DBPath, id)
			assertResumeGatewayProcess(t, f, id, filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "bin", "agent-director"), "")
			native := cursorfix.NewNativeHistory(t, f.CWD, before.SessionID.(string), "older-retained")
			db, openErr := sql.Open("sqlite", f.DBPath)
			if openErr != nil {
				t.Fatal(openErr)
			}
			selection, _ := json.Marshal(map[string]string{"CURSOR_CONFIG_DIR": native.ConfigRoot})
			_, updateErr := db.Exec("UPDATE spawns SET extra_env=? WHERE instance_id=?", string(selection), id)
			db.Close()
			if updateErr != nil {
				t.Fatal(updateErr)
			}
			if _, err := f.API.Pause(context.Background(), api.PauseParams{InstanceID: id}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "original gateway exited", func() bool { return pidGone(int(before.PanePID.(int64))) }, func() string { return fmt.Sprint(readRow(t, f.DBPath, id)) })
			runtime.Configure(t, cursorfix.Scenario{LoadSupport: true, WaitForLoad: true, NativeConfigRoot: native.ConfigRoot})
			t.Setenv("CURSOR_CONFIG_DIR", "/changed-caller/config")
			t.Setenv("CURSOR_API_KEY", "synthetic-changed-caller-key")
			callerBin := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "bin", "agent-director")
			if surface == "CLI" {
				callerBin = buildHookCLI(t, t.TempDir())
			}
			adapter := surface
			if surface == "Go release wrapper fresh" {
				adapter = "Go release wrapper"
			}
			caller := newPaneCaller(t, f, adapter, callerBin)
			call := caller.call
			caller.call = func(verb string, params map[string]any) ([]byte, error) {
				started := time.Now()
				raw, err := call(verb, params)
				diagnostics.record(verb, started, raw, err)
				return raw, err
			}
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
			received := runtime.Selection(t)
			if received["CURSOR_CONFIG_DIR"] == nil || *received["CURSOR_CONFIG_DIR"] != native.ConfigRoot || received["CURSOR_API_KEY"] != nil || received["AGENT_CLI_CREDENTIAL_STORE"] != nil {
				t.Fatal("resume substituted changed caller selection")
			}
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

// b.m8h: once a relayed launch is verified gone, its delivered request no longer
// holds the confirmed replacement's prompt, and keeps its proof as recorded.
func TestCursorResumeAfterOldLaunchRequestAcceptsPrompt(t *testing.T) {
	if testing.Short() {
		t.Skip("real tmux synthetic ACP resume after old-launch request")
	}
	const session = "resume-hold-session"
	scenario := cursorfix.Scenario{SessionID: session, Requests: []json.RawMessage{relayFrame("old-request", "session/request_permission", cursorfix.Permission(session, "old-tool", "execute"))}}
	f, peer, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{Script: &scenario, Relay: "on", Timeout: 30})
	if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "old launch work"}); err != nil {
		t.Fatal(err)
	}
	request := relayRequests(t, f, launch.ID, 1)[0]
	if _, err := f.API.Decide(api.DecideParams{InstanceID: launch.ID, RequestToken: request.RequestToken, Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "old turn finishes", func() bool { return readRow(t, f.DBPath, launch.ID).State == store.StateWaiting }, nil)
	s, err := store.Open(f.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old, err := s.ReadCursorRuntime(launch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.API.Kill(api.KillParams{InstanceID: launch.ID}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "old gateway and runtime gone", func() bool { return pidGone(old.GatewayPID) && pidGone(old.RuntimePID) }, nil)
	if _, err := f.API.FindMissing(context.Background()); err != nil {
		t.Fatal(err)
	}
	if row, err := f.API.Get(launch.ID); err != nil || row.State != store.StateMissing {
		t.Fatalf("not missing: %+v %v", row, err)
	}
	before, err := s.GetPermissionRequest(launch.ID, request.RequestToken)
	if err != nil || before.DeliveredAt.IsZero() || before.ProvenGone() {
		t.Fatalf("old request before resume: %+v %v", before, err)
	}
	if n := len(relayReplies(t, peer)); n != 1 {
		t.Fatalf("replies before resume = %d; want the one allow", n)
	}
	peer.Configure(t, cursorfix.Scenario{LoadSupport: true})
	if _, err := f.API.Resume(api.ResumeParams{InstanceID: launch.ID}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "load confirmed in pane", func() bool {
		pane, err := f.API.ReadPane(api.ReadPaneParams{InstanceID: launch.ID, NLines: 100})
		return err == nil && strings.Contains(pane.Pane, "Cursor conversation loaded; awaiting a prompt")
	}, nil)
	if current, err := s.ReadCursorRuntime(launch.ID); err != nil || current.GatewayPID == old.GatewayPID || current.SessionID != old.SessionID {
		t.Fatalf("resume binding: %+v %v", current, err)
	}
	if row, err := f.API.Get(launch.ID); err != nil || row.State != store.StatePending || len(row.UnprovenRequests) != 0 {
		t.Fatalf("resumed get lists the old launch's request: %+v %v", row, err)
	}
	const text = "resumed work after old launch"
	prompted := func() bool {
		for _, frame := range peer.Frames(t) {
			if string(frame["method"]) == `"session/prompt"` && strings.Contains(string(frame["params"]), text) {
				return true
			}
		}
		return false
	}
	if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: text, AllowPending: true}); err != nil {
		t.Fatalf("resumed prompt refused: %v", err)
	}
	waitFor(t, "resumed prompt reaches ACP and completes", func() bool {
		return prompted() && readRow(t, f.DBPath, launch.ID).State == store.StateWaiting
	}, func() string {
		pane, err := f.API.ReadPane(api.ReadPaneParams{InstanceID: launch.ID, NLines: 100})
		return fmt.Sprintf("row=%+v pane=%q err=%v", readRow(t, f.DBPath, launch.ID), pane.Pane, err)
	})
	if n := len(relayReplies(t, peer)); n != 1 {
		t.Fatalf("replies after the resumed prompt = %d; resume replayed an old approval", n)
	}
	after, err := s.GetPermissionRequest(launch.ID, request.RequestToken)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("old request changed: before=%+v after=%+v %v", before, after, err)
	}
	if _, err := f.API.Pause(context.Background(), api.PauseParams{InstanceID: launch.ID}); err != nil {
		t.Fatal(err)
	}
}

// Synthetic rejection composes actual saved selection with public asynchronous lifecycle.
func TestCursorRejectedLaunchSelectionAndSavedLifecycle(t *testing.T) {
	// Serial: real-pane fixtures own HOME/PATH and the private tmux environment.
	for _, operation := range []string{"resume", "reuse"} {
		t.Run(operation, func(t *testing.T) {
			saved := map[string]string{"CURSOR_CONFIG_DIR": filepath.Join(t.TempDir(), "saved config"), "CURSOR_DATA_DIR": filepath.Join(t.TempDir(), "saved data")}
			f, peer, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{ExtraEnv: saved})
			initial := readRow(t, f.DBPath, launch.ID)
			if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "/new"}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "latest rotation published", func() bool {
				row := readRow(t, f.DBPath, launch.ID)
				return row.State == store.StateWaiting && row.SessionID != nil && row.SessionID != initial.SessionID
			}, nil)
			before := readRow(t, f.DBPath, launch.ID)
			if _, err := f.API.Pause(context.Background(), api.PauseParams{InstanceID: launch.ID}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "old gateway gone", func() bool { return pidGone(int(before.PanePID.(int64))) }, nil)
			prefix, err := os.ReadFile(before.JSONLPath.(string))
			if err != nil {
				t.Fatal(err)
			}
			// Rotations share the protected journal. Seed a separate retained
			// older artifact through the existing history factory for exact checks.
			artifacts := cursorfix.NewGatewayHistory(t, launch.ID, before.LifeNumber.(int64), before.SessionID.(string), initial.SessionID.(string))
			db, err := sql.Open("sqlite", f.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec("UPDATE session_history SET jsonl_path=? WHERE instance_id=? AND session_id=?", artifacts.Older, launch.ID, initial.SessionID)
			closeErr := db.Close()
			if err != nil || closeErr != nil {
				t.Fatalf("older artifact setup: %v %v", err, closeErr)
			}
			history, err := apitest.ReadSessionHistoryAllLives(f.DBPath, launch.ID)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("CURSOR_CONFIG_DIR", "/different/caller/config")
			t.Setenv("CURSOR_DATA_DIR", "/different/caller/data")
			rejection := json.RawMessage(`{"error":{"code":-32000,"message":"SYNTHETIC-PRIVATE"}}`)
			scenario := cursorfix.Scenario{LoadSupport: true, LoadReply: rejection, WaitForLoad: true}
			selected := saved
			params := map[string]any{"instance_id": launch.ID}
			verb := "resume"
			if operation == "reuse" {
				selected = map[string]string{"CURSOR_CONFIG_DIR": filepath.Join(t.TempDir(), "new config"), "CURSOR_DATA_DIR": filepath.Join(t.TempDir(), "new data")}
				scenario = cursorfix.Scenario{FirstNewReply: rejection}
				verb = "spawn"
				params = map[string]any{"instance_id": launch.ID, "agent": "cursor", "cwd": f.CWD, "no_pre_trust": true, "reuse_finished": true, "extra_env": selected}
			}
			peer.Configure(t, scenario)
			mark := len(peer.Frames(t))
			caller := newPaneCaller(t, f, "CLI", filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "bin", "agent-director"))
			var result struct {
				InstanceID string `json:"instance_id"`
			}
			if err := json.Unmarshal(caller.must(verb, params), &result); err != nil || result.InstanceID != launch.ID {
				t.Fatalf("public launch result: %+v %v", result, err)
			}
			if operation == "resume" {
				waitFor(t, "latest load received", func() bool {
					for _, frame := range peer.Frames(t)[mark:] {
						if string(frame["method"]) == `"session/load"` {
							return true
						}
					}
					return false
				}, nil)
				row, err := f.API.Get(launch.ID)
				if err != nil || row.State != store.StatePending || row.SessionID != before.SessionID {
					t.Fatalf("load manufactured readiness: %+v %v", row, err)
				}
				if err := os.WriteFile(peer.NewRelease, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			waitFor(t, "asynchronous failure reconciled", func() bool { row, err := f.API.Get(launch.ID); return err == nil && row.State == store.StateMissing }, nil)
			for key, want := range selected {
				got := peer.Selection(t)[key]
				if got == nil || *got != want {
					t.Fatalf("%s received=%v want=%q", key, got, want)
				}
			}
			var methods []string
			for _, frame := range peer.Frames(t)[mark:] {
				if len(frame["method"]) > 0 {
					methods = append(methods, string(frame["method"]))
				}
				if string(frame["method"]) == `"session/load"` {
					var p struct {
						SessionID string `json:"sessionId"`
					}
					if json.Unmarshal(frame["params"], &p) != nil || p.SessionID != before.SessionID {
						t.Fatalf("not latest: %s", frame["params"])
					}
				}
			}
			wantMethod := `"session/load"`
			if operation == "reuse" {
				wantMethod = `"session/new"`
			}
			if strings.Join(methods, ",") != `"initialize",`+wantMethod {
				t.Fatalf("dependent work/fallback: %v", methods)
			}
			after := readRow(t, f.DBPath, launch.ID)
			if after.LifeNumber != before.LifeNumber {
				t.Fatal("failed launch changed life")
			}
			if operation == "resume" && (after.SessionID != before.SessionID || after.JSONLPath != before.JSONLPath || after.ExtraEnv != before.ExtraEnv) {
				t.Fatalf("failed resume changed saved identity: %+v", after)
			}
			if operation == "reuse" && (after.SessionID != nil || after.ExtraEnv == before.ExtraEnv) {
				t.Fatalf("accepted asynchronous reuse restored old selection: %+v", after)
			}
			afterHistory, err := apitest.ReadSessionHistoryAllLives(f.DBPath, launch.ID)
			if operation == "reuse" {
				var retained []apitest.HistoryEntry
				archived := 0
				for _, entry := range afterHistory {
					if entry.SessionID == before.SessionID {
						archived++
						if !entry.JSONLPath.Valid || entry.JSONLPath.String != before.JSONLPath || entry.LifeNumber != before.LifeNumber {
							t.Fatalf("wrong accepted-reuse archive: %+v", entry)
						}
					} else {
						retained = append(retained, entry)
					}
				}
				if archived != 1 {
					t.Fatalf("accepted reuse archived latest %d times", archived)
				}
				afterHistory = retained
				var persisted map[string]string
				if json.Unmarshal([]byte(after.ExtraEnv.(string)), &persisted) != nil || !reflect.DeepEqual(persisted, selected) {
					t.Fatalf("async reuse restored/dropped selection: %v", after.ExtraEnv)
				}
			}
			if err != nil || !reflect.DeepEqual(history, afterHistory) {
				t.Fatalf("failed launch changed older history: %v", err)
			}
			for path, want := range map[string][]byte{before.JSONLPath.(string): prefix, artifacts.Older: artifacts.OlderBytes} {
				got, err := os.ReadFile(path)
				unchanged := bytes.HasPrefix(got, want)
				if path == artifacts.Older {
					unchanged = bytes.Equal(got, want)
				}
				if err != nil || !unchanged {
					t.Fatalf("artifact rewritten: %s %v", path, err)
				}
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

// cursorFailureDiagnostics retains only synthetic fixture evidence before cleanup.
type cursorFailureDiagnostics struct {
	t          *testing.T
	f          *spawnFix
	peer       cursorfix.Runtime
	id, home   string
	operations []string
	root       string
	captured   bool
}

func (d *cursorFailureDiagnostics) record(operation string, started time.Time, result any, err error) {
	if raw, ok := result.([]byte); ok {
		result = string(raw)
	}
	d.operations = append(d.operations, fmt.Sprintf("start=%s end=%s elapsed=%s operation=%s result=%#v error=%v error_detail=%#v", started.Format(time.RFC3339Nano), time.Now().Format(time.RFC3339Nano), time.Since(started), operation, result, err, err))
}

func (d *cursorFailureDiagnostics) capture() {
	if !d.t.Failed() {
		return
	}
	d.captureNow()
}

func (d *cursorFailureDiagnostics) captureNow() {
	if d.captured {
		return
	}
	d.captured = true
	// This repository-mounted ignored directory survives sandbox and TempDir cleanup.
	root := filepath.Join("..", "..", "tmpbin")
	if d.root != "" {
		root = d.root
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		d.t.Logf("FAILURE CAPTURE ERROR creating artifact root: %v", err)
		return
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		d.t.Logf("FAILURE CAPTURE ERROR unsafe artifact root %s: %v", root, err)
		return
	}
	if err := os.Chmod(root, 0700); err != nil {
		d.t.Logf("FAILURE CAPTURE ERROR protecting artifact root: %v", err)
		return
	}
	dir, err := os.MkdirTemp(root, "realtmux-failure-")
	if err != nil {
		d.t.Logf("FAILURE CAPTURE ERROR creating artifact directory: %v", err)
		return
	}
	if err := os.Chmod(dir, 0700); err != nil {
		d.t.Logf("FAILURE CAPTURE ERROR protecting artifact directory: %v", err)
		return
	}
	d.t.Logf("failure evidence before cleanup: %s", dir)
	var captureErrors []string
	note := func(operation string, err error) {
		if err != nil {
			captureErrors = append(captureErrors, fmt.Sprintf("%s: %v", operation, err))
			d.t.Logf("FAILURE CAPTURE ERROR %s: %v", operation, err)
		}
	}
	write := func(name string, raw []byte) { note("write "+name, os.WriteFile(filepath.Join(dir, name), raw, 0600)) }
	write("operations.txt", []byte(fmt.Sprintf("test=%s instance=%s capture=%s\n%s\n", d.t.Name(), d.id, time.Now().Format(time.RFC3339Nano), strings.Join(d.operations, "\n"))))
	read := func(name, source string) []byte {
		// Explicit files only; never traverse HOME, native history or private config trees.
		if slices.Contains(strings.Split(filepath.Clean(source), string(os.PathSeparator)), ".claude") {
			note("read "+name, fmt.Errorf("excluded path component"))
			return nil
		}
		info, err := os.Lstat(source)
		if err != nil {
			note("read "+name, err)
			return nil
		}
		if !info.Mode().IsRegular() {
			note("read "+name, fmt.Errorf("not a regular owned fixture file: %s", source))
			return nil
		}
		raw, err := os.ReadFile(source)
		note("read "+name, err)
		if err == nil {
			write(name, raw)
			return raw
		}
		return nil
	}
	pane, paneErr := d.f.API.ReadPane(api.ReadPaneParams{InstanceID: d.id, NLines: 1000})
	write("pane.txt", []byte(fmt.Sprintf("pane=%+v\nread_error=%v\n", pane, paneErr)))
	note("read pane", paneErr)
	for _, file := range []struct{ name, source string }{
		{"peer-received.jsonl", d.peer.Trace}, {"peer-sent.jsonl", d.peer.Sent},
		{"peer-raw", d.peer.Raw}, {"peer-observer", d.peer.Observer},
		{"peer-scenario", d.peer.Scenario}, {"peer-mcp-control.json", d.peer.MCPControl},
		{"trail.jsonl", filepath.Join(d.home, ".config", "agent-director", "ad-trail.jsonl")},
		{"hook-errors.log", filepath.Join(d.home, ".config", "agent-director", "errors.log")},
	} {
		read(file.name, file.source)
	}
	peerPID := read("peer-pid", d.peer.PID)
	peerMCPTrace := read("peer-mcp.jsonl", d.peer.MCPTrace)
	// Only identities in this synthetic fixture's own files or rows are probed.
	pids := map[int]bool{}
	addPID := func(value any) {
		pid := 0
		if _, err := fmt.Sscan(fmt.Sprint(value), &pid); err == nil && pid > 0 {
			pids[pid] = true
		}
	}
	addPID(string(peerPID))
	for _, line := range bytes.Split(peerMCPTrace, []byte("\n")) {
		var event struct {
			PID int `json:"pid"`
		}
		if json.Unmarshal(line, &event) == nil {
			addPID(event.PID)
		}
	}
	var stacks bytes.Buffer
	note("goroutine stacks", pprof.Lookup("goroutine").WriteTo(&stacks, 2))
	write("goroutines.txt", stacks.Bytes())
	db, err := sql.Open("sqlite", d.f.DBPath)
	note("open synthetic SQL", err)
	if err == nil {
		// Failure-only bounded raw reads, without reconciliation or public read projection.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		for _, table := range []string{"spawns", "cursor_runtimes", "permission_requests", "session_history"} {
			key := "instance_id"
			if table == "cursor_runtimes" {
				key = "bound_instance_id"
			}
			rows, err := db.QueryContext(ctx, "SELECT * FROM "+table+" WHERE "+key+" = ?", d.id)
			note("query "+table, err)
			if err != nil {
				continue
			}
			cols, err := rows.Columns()
			note("columns "+table, err)
			var records []map[string]any
			if err == nil {
				for rows.Next() {
					values, targets := make([]any, len(cols)), make([]any, len(cols))
					for i := range values {
						targets[i] = &values[i]
					}
					if err := rows.Scan(targets...); err != nil {
						note("scan "+table, err)
						break
					}
					record := map[string]any{}
					for i, key := range cols {
						record[key] = values[i]
					}
					records = append(records, record)
					for _, key := range []string{"pid", "pane_pid", "tmux_server_pid", "bound_gateway_pid", "bound_runtime_pid"} {
						addPID(record[key])
					}
				}
			}
			note("iterate "+table, rows.Err())
			note("close rows "+table, rows.Close())
			raw, err := json.MarshalIndent(records, "", "  ")
			note("encode "+table, err)
			if err == nil {
				write("sql-"+table+".json", raw)
				d.t.Logf("failure snapshot instance=%s table=%s rows=%d artifact=%s", d.id, table, len(records), filepath.Join(dir, "sql-"+table+".json"))
			}
			if table == "spawns" {
				for _, row := range records {
					if row["instance_id"] == d.id {
						if path, ok := row["jsonl_path"].(string); ok && path != "" {
							transcriptRoot := filepath.Join(d.home, ".config", "agent-director", "cursor-transcripts")
							relative, err := filepath.Rel(transcriptRoot, path)
							if err != nil {
								note("transcript path", err)
							} else if relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
								note("transcript path", fmt.Errorf("outside synthetic HOME: %s", path))
							} else {
								read("transcript.jsonl", path)
							}
						} else {
							note("transcript path", fmt.Errorf("absent for %s", d.id))
						}
					}
				}
			}
		}
		note("close synthetic SQL", db.Close())
	}
	var identities []string
	for pid := range pids {
		start, alive, known := probe.NewProcChecker().StartTime(pid)
		identities = append(identities, fmt.Sprintf("pid=%d observed_start=%s alive=%t known=%t state=%s", pid, start, alive, known, procState(pid)))
	}
	write("process-identities.txt", []byte(strings.Join(identities, "\n")+"\n"))
	write("capture-errors.txt", []byte(strings.Join(captureErrors, "\n")+"\n"))
}
