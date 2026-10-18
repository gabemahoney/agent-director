//go:build linux

package realtmux_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/cursorhost"
	"github.com/gabemahoney/agent-director/internal/cursormcp"
	"github.com/gabemahoney/agent-director/internal/helptext"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestCursorGuidanceTwoStoresTwoSocketsAndOwnedMCPRetirement(t *testing.T) {
	if testing.Short() {
		t.Skip("real tmux composition")
	}
	plain, err := helptext.Plain()
	if err != nil {
		t.Fatal(err)
	}
	scenario := cursorfix.Scenario{SessionID: "guidance-owned", MCPExpectedInstructions: plain, MCPCalls: []map[string]any{{"name": "list", "arguments": map[string]any{}}}}
	first, firstRuntime, firstLaunch := newCursorPaneScenario(t, false, "", cursorPaneOptions{InjectHelp: true, Script: &scenario})
	firstHome := os.Getenv("HOME")
	second, secondRuntime, secondLaunch := newCursorPaneScenario(t, false, "", cursorPaneOptions{InjectHelp: true, Script: &scenario})
	secondHome := os.Getenv("HOME")
	captureRoot := filepath.Join("..", "..", "tickets", "handoff", "b.ftd", "Plans", "b.jf1", "e7-task2", "child-capture")
	parentDiagnostics := cursorFailureDiagnostics{t: t, f: first, peer: firstRuntime, id: firstLaunch.ID, home: firstHome, root: captureRoot}
	peerDiagnostics := cursorFailureDiagnostics{t: t, f: second, peer: secondRuntime, id: secondLaunch.ID, home: secondHome, root: captureRoot}
	childDiagnostics := map[string]*cursorFailureDiagnostics{}
	captureChildren := func() {
		for _, d := range childDiagnostics {
			d.captureNow()
		}
		parentDiagnostics.captureNow()
		peerDiagnostics.captureNow()
	}
	captureFailure := func() {
		if t.Failed() {
			captureChildren()
		}
	}
	type process struct {
		pid   int
		start string
	}
	owned := func(r cursorfix.Runtime, own, foreign string) process {
		var child process
		waitFor(t, "scoped MCP list reaches only intended store", func() bool {
			raw, err := os.ReadFile(r.MCPTrace)
			if err != nil {
				return false
			}
			seen := false
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				var event struct {
					Event    string          `json:"event"`
					PID      int             `json:"pid"`
					Response json.RawMessage `json:"response"`
				}
				if json.Unmarshal([]byte(line), &event) != nil {
					return false
				}
				if event.Event == "started" {
					child.pid = event.PID
				}
				if event.Event == "tool_response" {
					var reply struct {
						Result struct {
							IsError bool `json:"isError"`
							Content []struct {
								Text string `json:"text"`
							} `json:"content"`
						} `json:"result"`
					}
					if json.Unmarshal(event.Response, &reply) != nil || reply.Result.IsError || len(reply.Result.Content) != 1 {
						t.Fatalf("MCP list response: %s", event.Response)
					}
					text := reply.Result.Content[0].Text
					seen = strings.Contains(text, own) && !strings.Contains(text, foreign)
				}
			}
			return child.pid > 0 && seen
		}, func() string { raw, _ := os.ReadFile(r.MCPTrace); return string(raw) })
		var alive, known bool
		child.start, alive, known = probe.NewProcChecker().StartTime(child.pid)
		if !alive || !known {
			t.Fatal("MCP subprocess missing before retirement")
		}
		return child
	}
	firstMCP := owned(firstRuntime, firstLaunch.ID, secondLaunch.ID)
	secondMCP := owned(secondRuntime, secondLaunch.ID, firstLaunch.ID)
	// These are actual configured descendants. Public adapter recreation remains
	// a separate operation below and never stands in for configured restart.
	t.Setenv("HOME", firstHome)
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", firstLaunch.ID)
	t.Setenv("AGENT_DIRECTOR_CURSOR_STORE_PATH", second.DBPath)
	t.Setenv("TMUX", fmt.Sprintf("%s,%v,0", firstLaunch.Row.TmuxSocket, firstLaunch.Row.TmuxServerPID))
	if _, err := apitest.SeedSpawn(second.DBPath, firstLaunch.ID, "ended", "/foreign", "off", "foreign-session", true); err != nil {
		t.Fatal(err)
	}
	foreignBefore := readRow(t, second.DBPath, firstLaunch.ID)
	st, err := store.Open(first.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	binding, err := st.ReadCursorRuntime(firstLaunch.ID)
	if err != nil {
		t.Fatal(err)
	}
	parent := readRow(t, first.DBPath, firstLaunch.ID)
	history, err := apitest.ReadSessionHistoryAllLives(first.DBPath, firstLaunch.ID)
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := os.ReadFile(parent.JSONLPath.(string))
	if err != nil {
		t.Fatal(err)
	}
	configured := func(control, operation string, calls ...map[string]any) {
		firstRuntime.MCPCommand(t, control, operation, calls...)
		waitFor(t, "configured "+operation, func() bool {
			for _, event := range firstRuntime.MCPEvents(t) {
				if string(event["event"]) == `"control_result"` && string(event["control"]) == fmt.Sprintf("%q", control) {
					if len(event["error"]) != 0 {
						t.Fatalf("configured control: %s", event["error"])
					}
					return true
				}
			}
			return false
		}, func() string { raw, _ := os.ReadFile(firstRuntime.MCPTrace); return string(raw) })
		for _, event := range firstRuntime.MCPEvents(t) {
			if string(event["control"]) != fmt.Sprintf("%q", control) || string(event["event"]) != `"tool_response"` {
				continue
			}
			var reply struct {
				Result struct {
					IsError bool `json:"isError"`
				} `json:"result"`
			}
			if err := json.Unmarshal(event["response"], &reply); err != nil || reply.Result.IsError {
				t.Fatalf("configured useful call failed: %s", event["response"])
			}
		}
	}
	bin := buildHookCLI(t, t.TempDir())
	public := newPaneCaller(t, first, "MCP", bin)
	public.must("get", map[string]any{"instance_id": firstLaunch.ID})
	public.close()
	public = newPaneCaller(t, first, "MCP", bin)
	defer public.close()
	public.must("get", map[string]any{"instance_id": firstLaunch.ID})
	cli := newPaneCaller(t, first, "CLI", bin)
	childRuntimes := map[string]cursorfix.Runtime{}
	readyChild := func(id, resumeSession string) {
		var lastPane api.ReadPaneResult
		var lastError error
		var loadedBinding store.CursorRuntime
		promptSent := false
		const resumedPrompt = "useful child after retained resume"
		started := time.Now()
		waitFor(t, "child ready", func() bool {
			lastPane, lastError = first.API.ReadPane(api.ReadPaneParams{InstanceID: id, NLines: 100})
			if resumeSession == "" {
				return lastError == nil && strings.Contains(lastPane.Pane, "Cursor ready")
			}
			row := readRow(t, first.DBPath, id)
			current, err := st.ReadCursorRuntime(id)
			if lastError != nil || err != nil || row.SessionID != resumeSession || current.SessionID != resumeSession || current.Terminated {
				return false
			}
			// Loaded sessions need not publish SessionStart's generic hook PID.
			// The current launch and protected ACP binding own their identities.
			pid, pidError := os.ReadFile(childRuntimes[id].PID)
			if row.LaunchToken != current.Token || row.LifeNumber != current.Life || row.PanePID != int64(current.GatewayPID) || row.PaneStarttime != current.GatewayStart || pidError != nil || string(pid) != fmt.Sprint(current.RuntimePID) || cursorhost.VerifyRuntime(current, false) != nil {
				return false
			}
			if !promptSent {
				if row.State != store.StatePending || !strings.Contains(lastPane.Pane, "conversation loaded; awaiting a prompt") {
					return false
				}
				loaded := false
				for _, record := range paneRecords(t, row.JSONLPath.(string)) {
					if record.Kind == "session_loaded" && record.Instance == id && record.Session == resumeSession && record.Life == current.Life && record.Launch == current.Token && record.Connection == current.Connection {
						loaded = true
					}
				}
				if !loaded {
					return false
				}
				loadedBinding = current
				// Load preserves pending state until the first verified prompt.
				// Send once, within this wait's original budget.
				promptSent = true
				cli.must("send-keys", map[string]any{"instance_id": id, "text": resumedPrompt, "allow_pending": true})
				return false
			}
			if current != loadedBinding || row.State != store.StateWaiting || !strings.Contains(lastPane.Pane, "Synthetic reply: "+resumedPrompt) || strings.Count(lastPane.Pane, "Cursor turn complete") != 1 {
				return false
			}
			prompts := panePrompts(t, childRuntimes[id])
			if len(prompts) != 1 || prompts[0].Text != resumedPrompt || prompts[0].Session != resumeSession {
				return false
			}
			var request any
			if json.Unmarshal([]byte(prompts[0].Request), &request) != nil {
				return false
			}
			completed := false
			for _, record := range paneRecords(t, row.JSONLPath.(string)) {
				if record.Kind == "turn_outcome" && record.Data.Request == fmt.Sprint(request) && record.Instance == id && record.Session == resumeSession && record.Life == current.Life && record.Launch == current.Token && record.Connection == current.Connection {
					completed = true
				}
			}
			for _, frame := range childRuntimes[id].SentFrames(t) {
				if completed && string(frame["id"]) == prompts[0].Request && string(frame["result"]) == `{"stopReason":"end_turn"}` {
					return true
				}
			}
			return false
		}, func() string {
			d := childDiagnostics[id]
			d.record("last child readiness ReadPane", started, lastPane, lastError)
			captureChildren()
			row, rowError := apitest.ReadSpawnColumns(first.DBPath, id)
			paneSummary := lastPane.Pane
			if len(paneSummary) > 4096 {
				paneSummary = paneSummary[:4096] + " [truncated; complete result in operations.txt]"
			}
			summary := fmt.Sprintf("last ReadPane error=%v pane_sha256=%s pane=%q\nrow state=%v session=%v pid=%v start=%v launch_started_at=%v row_error=%v", lastError, lastPane.PaneSHA256, paneSummary, row.State, row.SessionID, row.PID, row.ProcStarttime, row.LaunchStartedAt, rowError)
			if len(summary) > 8192 {
				summary = summary[:8192] + " [summary truncated]"
			}
			return summary + "\ncomplete pre-cleanup child/parent/peer evidence under " + captureRoot
		})
	}
	pauseChild := func(id string) {
		b, err := st.ReadCursorRuntime(id)
		if err != nil {
			t.Fatal(err)
		}
		cli.must("pause", map[string]any{"instance_id": id})
		for _, identity := range []struct {
			pid   int
			start string
		}{{b.GatewayPID, b.GatewayStart}, {b.RuntimePID, b.RuntimeStart}} {
			waitFor(t, "child exact process dead", func() bool {
				start, alive, known := probe.NewProcChecker().StartTime(identity.pid)
				return known && (!alive || start != identity.start)
			}, func() string { return procState(identity.pid) })
		}
	}
	child := func(surface, control string) spawned {
		r := cursorfix.New(t)
		session := newInstanceID("child-session")
		r.Configure(t, cursorfix.Scenario{SessionID: session, LoadSupport: true, MCPExpectedInstructions: plain})
		id := newInstanceID("configured-child")
		childRuntimes[id] = r
		childDiagnostics[id] = &cursorFailureDiagnostics{t: t, f: first, peer: r, id: id, home: firstHome, root: captureRoot}
		// Also retain spawn/route failures before a launch object exists.
		t.Cleanup(captureFailure)
		params := map[string]any{"instance_id": id, "agent": "cursor", "cwd": first.CWD, "no_pre_trust": true, "tmux_session_name": id, "extra_env": map[string]string{"AD_CURSOR_FIX_SETTINGS": r.Settings()}}
		if surface == "configured" {
			configured(control, "calls", map[string]any{"name": "spawn", "arguments": params})
		} else {
			// CLI adapter intentionally lacks session-name; explicit CLI invocation
			// below uses the public home/store flags with the selected server.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, bin, "spawn", "--home", firstHome, "--store-path", first.DBPath, "--agent", "cursor", "--instance-id", id, "--tmux-session-name", id, "--cwd", first.CWD, "--no-pre-trust", "--extra-env", "AD_CURSOR_FIX_SETTINGS="+r.Settings()).CombinedOutput()
			if err != nil {
				t.Fatalf("explicit child CLI: %v %s", err, out)
			}
		}
		launch := first.launched(t, id)
		t.Cleanup(func() { stopCursorPaneGateways(t, first, launch) })
		// LIFO cleanup captures before this child's shared-server teardown and TempDir removal.
		t.Cleanup(captureFailure)
		if launch.Row.ParentID != firstLaunch.ID || launch.Row.TmuxSocket != firstLaunch.Row.TmuxSocket || launch.Row.TmuxServerPID != firstLaunch.Row.TmuxServerPID || launch.rt.formatInt(t, launch.Row.PaneID.(string), "#{pid}") != int(firstLaunch.Row.TmuxServerPID.(int64)) {
			t.Fatalf("actual child route: %+v", launch.Row)
		}
		if _, err := apitest.ReadSpawnColumns(second.DBPath, id); err == nil {
			t.Fatal("child reached foreign store")
		}
		if _, err := apitest.ReadSpawnColumns(filepath.Join(firstHome, ".config", "agent-director", "state.db"), id); err == nil {
			t.Fatal("child reached default store")
		}
		cli.must("get", map[string]any{"instance_id": id})
		listed := cli.must("list", map[string]any{"parent": firstLaunch.ID})
		if !strings.Contains(string(listed), id) {
			t.Fatalf("public parent list missing child: %s", listed)
		}
		readyChild(id, "")
		row := readRow(t, first.DBPath, id)
		childBinding, err := st.ReadCursorRuntime(id)
		if err != nil {
			t.Fatal(err)
		}
		pid, err := os.ReadFile(r.PID)
		if err != nil || string(pid) != fmt.Sprint(childBinding.RuntimePID) || row.SessionID != session || childBinding.SessionID != session || childBinding.RuntimePID == binding.RuntimePID || childBinding.GatewayPID == binding.GatewayPID || row.JSONLPath == parent.JSONLPath {
			t.Fatalf("child session/runtime isolation: row=%+v binding=%+v fixturePID=%s error=%v", row, childBinding, pid, err)
		}
		for otherID := range childRuntimes {
			if otherID == id {
				continue
			}
			other, err := st.ReadCursorRuntime(otherID)
			if err != nil || other.RuntimePID == childBinding.RuntimePID || other.GatewayPID == childBinding.GatewayPID || other.SessionID == childBinding.SessionID {
				t.Fatalf("children share runtime/session identity: %+v %+v %v", other, childBinding, err)
			}
		}
		return launch
	}
	beforeChild := child("configured", "child-before")
	cliChild := child("CLI", "cli-child")
	// Fresh calls selected the parent's server; later resume/reuse must retain it
	// even with the caller now pointing at the independent foreign server.
	t.Setenv("TMUX", fmt.Sprintf("%s,%v,0", secondLaunch.Row.TmuxSocket, secondLaunch.Row.TmuxServerPID))
	for _, verb := range []string{"resume", "spawn"} {
		previous := readRow(t, first.DBPath, cliChild.ID)
		pauseChild(cliChild.ID)
		params := map[string]any{"instance_id": cliChild.ID}
		if verb == "spawn" {
			childRuntimes[cliChild.ID].Configure(t, cursorfix.Scenario{SessionID: newInstanceID("reused-session"), LoadSupport: true, MCPExpectedInstructions: plain})
			params["agent"] = "cursor"
			params["cwd"] = first.CWD
			params["no_pre_trust"] = true
			params["reuse_finished"] = true
			params["extra_env"] = map[string]string{"AD_CURSOR_FIX_SETTINGS": childRuntimes[cliChild.ID].Settings()}
		}
		cli.must(verb, params)
		if verb == "resume" {
			readyChild(cliChild.ID, previous.SessionID.(string))
		} else {
			readyChild(cliChild.ID, "")
		}
		retained := first.launched(t, cliChild.ID)
		if retained.Row.TmuxSocket != firstLaunch.Row.TmuxSocket || retained.Row.TmuxServerPID != firstLaunch.Row.TmuxServerPID {
			t.Fatalf("%s followed ambient socket: %+v", verb, retained.Row)
		}
		if verb == "resume" && retained.Row.SessionID != previous.SessionID {
			t.Fatal("latest resume lost recorded conversation")
		}
		if verb == "spawn" && retained.Row.SessionID == previous.SessionID {
			t.Fatal("finished-id reuse did not create fresh conversation")
		}
	}
	var until time.Time
	for _, event := range firstRuntime.MCPEvents(t) {
		if string(event["event"]) != `"registration"` {
			continue
		}
		var servers []cursormcp.Stdio
		if err := json.Unmarshal(event["servers"], &servers); err != nil {
			t.Fatal(err)
		}
		for _, server := range servers {
			for _, env := range server.Env {
				if env.Name == cursormcp.ScopeEnv {
					var scope cursormcp.Scope
					if err := json.Unmarshal([]byte(env.Value), &scope); err != nil {
						t.Fatal(err)
					}
					until = scope.Until
				}
			}
		}
	}
	if until.IsZero() {
		t.Fatal("missing original observer deadline")
	}
	if remaining := time.Until(until); remaining > 0 {
		time.Sleep(remaining)
	}
	public.send(firstLaunch.ID, "HOLD")
	waitFor(t, "active parent before server stop", func() bool { return len(panePrompts(t, firstRuntime)) == 1 }, func() string { return fmt.Sprint(panePrompts(t, firstRuntime)) })
	configured("stop", "stop")
	waitFor(t, "configured exact process reaped", func() bool { _, err := os.Stat(fmt.Sprintf("/proc/%d", firstMCP.pid)); return os.IsNotExist(err) }, func() string { return procState(firstMCP.pid) })
	configured("restart", "restart", map[string]any{"name": "get", "arguments": map[string]any{"instance_id": firstLaunch.ID}})
	afterChild := child("configured", "child-after")
	if err := os.WriteFile(firstRuntime.Release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "held parent completes", func() bool {
		pane, err := first.API.ReadPane(api.ReadPaneParams{InstanceID: firstLaunch.ID, NLines: 200})
		return err == nil && paneRecordCount(t, parent.JSONLPath.(string), "turn_outcome") == 1 && readRow(t, first.DBPath, firstLaunch.ID).State == store.StateWaiting && strings.Count(pane.Pane, "Cursor turn complete") == 1
	}, func() string { return fmt.Sprint(panePrompts(t, firstRuntime)) })
	public.send(firstLaunch.ID, "meaningful parent after restart")
	waitFor(t, "continued parent result", func() bool {
		pane, err := first.API.ReadPane(api.ReadPaneParams{InstanceID: firstLaunch.ID, NLines: 100})
		return err == nil && strings.Contains(pane.Pane, "Synthetic reply: meaningful parent after restart") && strings.Count(pane.Pane, "Cursor turn complete") == 2 && readRow(t, first.DBPath, firstLaunch.ID).State == store.StateWaiting
	}, func() string { return fmt.Sprint(panePrompts(t, firstRuntime)) })
	afterBinding, err := st.ReadCursorRuntime(firstLaunch.ID)
	if err != nil || binding != afterBinding {
		t.Fatalf("parent binding changed: %+v %v", afterBinding, err)
	}
	for _, identity := range []struct {
		pid   int
		start string
	}{{binding.GatewayPID, binding.GatewayStart}, {binding.RuntimePID, binding.RuntimeStart}, {secondMCP.pid, secondMCP.start}} {
		start, alive, known := probe.NewProcChecker().StartTime(identity.pid)
		if !known || !alive || start != identity.start {
			t.Fatal("independent process identity changed")
		}
	}
	afterParent := readRow(t, first.DBPath, firstLaunch.ID)
	for _, pair := range [][2]any{{parent.LaunchToken, afterParent.LaunchToken}, {parent.LifeNumber, afterParent.LifeNumber}, {parent.SessionID, afterParent.SessionID}, {parent.JSONLPath, afterParent.JSONLPath}, {parent.PanePID, afterParent.PanePID}, {parent.PaneStarttime, afterParent.PaneStarttime}} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Fatalf("parent identity changed: %v", pair)
		}
	}
	afterHistory, err := apitest.ReadSessionHistoryAllLives(first.DBPath, firstLaunch.ID)
	if err != nil || !reflect.DeepEqual(history, afterHistory) {
		t.Fatalf("parent history changed: %+v %v", afterHistory, err)
	}
	afterTranscript, err := os.ReadFile(parent.JSONLPath.(string))
	if err != nil || !strings.HasPrefix(string(afterTranscript), string(transcript)) {
		t.Fatal("parent history rewritten")
	}
	if !reflect.DeepEqual(foreignBefore, readRow(t, second.DBPath, firstLaunch.ID)) {
		t.Fatal("foreign same-id row changed")
	}
	// Parent retirement must leave independently AD-launched children useful.
	public.must("pause", map[string]any{"instance_id": firstLaunch.ID})
	for _, launch := range []spawned{beforeChild, afterChild} {
		public.send(launch.ID, "independent child after parent retirement")
		waitFor(t, "child independent continuation", func() bool {
			pane, err := first.API.ReadPane(api.ReadPaneParams{InstanceID: launch.ID, NLines: 100})
			return err == nil && strings.Contains(pane.Pane, "Synthetic reply: independent child after parent retirement") && strings.Contains(pane.Pane, "Cursor turn complete") && readRow(t, first.DBPath, launch.ID).State == store.StateWaiting
		}, func() string { return fmt.Sprint(readRow(t, first.DBPath, launch.ID)) })
		pauseChild(launch.ID)
	}
	if _, err := second.API.Pause(context.Background(), api.PauseParams{InstanceID: secondLaunch.ID}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "owned MCP actually reaped", func() bool { _, err := os.Stat(fmt.Sprintf("/proc/%d", secondMCP.pid)); return os.IsNotExist(err) }, func() string { return procState(secondMCP.pid) })
	if _, err := first.API.SendKeys(api.SendKeysParams{InstanceID: cliChild.ID, Text: "ordinary peer continuation"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "foreign conversation remains useful", func() bool {
		pane, err := first.API.ReadPane(api.ReadPaneParams{InstanceID: cliChild.ID, NLines: 100})
		return err == nil && strings.Contains(pane.Pane, "Synthetic reply: ordinary peer continuation") && strings.Contains(pane.Pane, "Cursor turn complete") && readRow(t, first.DBPath, cliChild.ID).State == store.StateWaiting
	}, func() string { return fmt.Sprint(readRow(t, first.DBPath, firstLaunch.ID)) })
	pauseChild(cliChild.ID)
}
