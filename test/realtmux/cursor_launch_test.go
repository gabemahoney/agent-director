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

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func newCursorPane(t *testing.T, disableQuestions bool) (*spawnFix, cursorfix.Runtime, spawned) {
	t.Helper()
	return newCursorPaneScenario(t, disableQuestions, "")
}
func newCursorPaneScenario(t *testing.T, disableQuestions bool, scenario string) (*spawnFix, cursorfix.Runtime, spawned) {
	t.Helper()
	f := newSpawnFix(t)
	r := cursorfix.New(t)
	if scenario != "" {
		if err := os.WriteFile(r.Scenario, []byte(scenario), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bin := buildHookCLI(t, t.TempDir())
	configPath := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "config.toml")
	cfg, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg = append(cfg, []byte("\n[defaults]\ndisable_ask_user = "+fmt.Sprint(disableQuestions)+"\n")...)
	cfg = append(cfg, []byte(fmt.Sprintf("\n[launch]\ncursor_path = %q\n", r.Program))...)
	if err = os.WriteFile(configPath, cfg, 0600); err != nil {
		t.Fatal(err)
	}

	// A caller store override must reach the gateway and its direct hook children.
	f.DBPath = filepath.Join(t.TempDir(), "override-state.db")
	client, err := api.New(api.Options{StorePath: f.DBPath, ConfigPath: configPath, CreateIfMissing: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	f.API = client
	id := newInstanceID("cursor")
	args := []string{"spawn", "--store-path", f.DBPath, "--agent", "cursor", "--instance-id", id, "--cwd", f.CWD, "--no-pre-trust"}
	if scenario == "blocked-input" {
		args = append(args, "--", strings.Repeat("\x01", 12*1024))
	}
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("production CLI spawn: %v %s", err, out)
	}
	var spawnResult api.SpawnResult
	if err := json.Unmarshal(out, &spawnResult); err != nil || spawnResult.InstanceID != id {
		t.Fatalf("CLI Cursor result %s %v", out, err)
	}
	launch := f.launched(t, id)
	return f, r, launch
}

// Synthetic runtime through the production CLI, row, gateway, and actual tmux pane.
func TestCursorFreshRealPanePromptsRefusalsAndPause(t *testing.T) {
	if testing.Short() {
		t.Skip("real pane scripted ACP smoke")
	}
	f, r, launch := newCursorPane(t, false)
	id := launch.ID
	var err error
	state := func(want string) {
		waitFor(t, "Cursor state "+want, func() bool { row := readRow(t, f.DBPath, id); return row.State == want }, func() string { return fmt.Sprintf("%+v", readRow(t, f.DBPath, id)) })
	}
	paneContains := func(want string) {
		waitFor(t, "pane shows "+want, func() bool {
			res, e := f.API.ReadPane(api.ReadPaneParams{InstanceID: id, NLines: 100})
			return e == nil && strings.Contains(res.Pane, want)
		}, func() string {
			res, e := f.API.ReadPane(api.ReadPaneParams{InstanceID: id, NLines: 100})
			return fmt.Sprintf("%+v %v", res, e)
		})
	}
	send := func(text string) {
		if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: id, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	prompts := func() []string {
		var texts []string
		for _, frame := range r.Frames(t) {
			if string(frame["method"]) != `"session/prompt"` {
				continue
			}
			var p struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			if err := json.Unmarshal(frame["params"], &p); err != nil {
				t.Fatal(err)
			}
			for _, part := range p.Prompt {
				texts = append(texts, part.Text)
			}
		}
		return texts
	}

	state(store.StateWaiting)
	// A controller claiming the exact current session/connection still lacks immediate-parent authority.
	s, err := store.Open(f.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := s.ReadCursorRuntime(id)
	s.Close()
	if err != nil {
		t.Fatal(err)
	}
	before := readRow(t, f.DBPath, id)
	claim, _ := json.Marshal(map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": binding.SessionID, "cursor_connection": binding.Connection, "origin": "main", "transcript_path": before.JSONLPath})
	hook := exec.Command(buildHookCLI(t, t.TempDir()), "hook", "--agent", "cursor")
	hook.Env = append(os.Environ(), "AGENT_DIRECTOR_INSTANCE_ID="+id, "AGENT_DIRECTOR_AGENT=cursor", "AGENT_DIRECTOR_CURSOR_STORE_PATH="+f.DBPath)
	hook.Stdin = strings.NewReader(string(claim))
	if out, err := hook.CombinedOutput(); err != nil {
		t.Fatalf("controller hook %v %s", err, out)
	}
	if after := readRow(t, f.DBPath, id); !reflect.DeepEqual(after, before) {
		t.Fatal("controller hook mutated current Cursor row")
	}
	paneContains("Cursor ready")
	if got := prompts(); len(got) != 0 {
		t.Fatalf("startup submitted prompt: %q", got)
	}
	expected := []string{"第一行\t🦋\nsecond\tline café", "別の\t🐝\nsecond distinct\trésumé"}
	for _, text := range expected {
		send(text)
		waitFor(t, "turn completion", func() bool {
			return len(prompts()) > 0 && prompts()[len(prompts())-1] == text && readRow(t, f.DBPath, id).State == store.StateWaiting
		}, func() string { return fmt.Sprintf("prompts %q row %+v", prompts(), readRow(t, f.DBPath, id)) })
		paneContains("Synthetic reply:")
	}
	if got := prompts(); !reflect.DeepEqual(got, expected) {
		t.Fatalf("distinct multiline prompts %q; want %q", got, expected)
	}
	for _, text := range []string{"/clear", "/new"} {
		send(text)
		paneContains("Unsupported Cursor conversation control; nothing sent")
	}
	send("HOLD")
	state(store.StateWorking)
	send("must refuse busy")
	paneContains("Cursor input refused while starting or busy; nothing sent")
	if err = os.WriteFile(r.Release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	state(store.StateWaiting)
	want := append(expected, "HOLD")
	if got := prompts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("refusal submitted prompt: %q", got)
	}
	rawTranscript, err := os.ReadFile(readRow(t, f.DBPath, id).JSONLPath.(string))
	if err != nil {
		t.Fatal(err)
	}
	var recorded []string
	for _, line := range strings.Split(strings.TrimSpace(string(rawTranscript)), "\n") {
		var record struct {
			Kind string `json:"kind"`
			Data struct {
				Text string `json:"text"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record.Kind == "user_input" {
			recorded = append(recorded, record.Data.Text)
		}
	}
	if !reflect.DeepEqual(recorded, want) {
		t.Fatalf("literal transcript inputs %q; want %q", recorded, want)
	}
	for _, frame := range r.Frames(t) {
		method := string(frame["method"])
		if method == `"session/load"` || method == `"session/clear"` {
			t.Fatalf("unsupported conversation rotation sent %s", method)
		}
	}
	// Pause performs the actual C-u /exit Enter sequence, clearing a stale editable draft.
	launch.rt.must(t, "send-keys", "-t", launch.Row.PaneID.(string), "-l", "--", "draft must never submit")
	runtimePID := readFileTrim(t, r.PID)
	res, err := f.API.Pause(context.Background(), api.PauseParams{InstanceID: id})
	if err != nil {
		t.Fatalf("pause %+v %v", res, err)
	}
	state(store.StateEnded)
	if got := prompts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("/exit or cleared draft became prompt: %q", got)
	}
	var pid int
	if _, err = fmt.Sscan(runtimePID, &pid); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "accepting runtime gone after pause", func() bool { return pidGone(pid) }, func() string { return procState(pid) })
}

func TestCursorQuestionsDeniedWithEitherSetting(t *testing.T) {
	if testing.Short() {
		t.Skip("real pane question denial")
	}
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprint(disabled), func(t *testing.T) {
			f, r, launch := newCursorPane(t, disabled)
			id := launch.ID
			waitFor(t, "fresh waiting", func() bool { return readRow(t, f.DBPath, id).State == store.StateWaiting }, func() string { return fmt.Sprint(readRow(t, f.DBPath, id)) })
			if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: id, Text: "ASK"}); err != nil {
				t.Fatal(err)
			}
			responses := func() map[string]map[string]json.RawMessage {
				found := map[string]map[string]json.RawMessage{}
				for _, frame := range r.Frames(t) {
					if len(frame["method"]) == 0 && len(frame["id"]) != 0 {
						var key string
						if json.Unmarshal(frame["id"], &key) == nil {
							found[key] = frame
						}
					}
				}
				return found
			}
			waitFor(t, "all authorization denied", func() bool { return len(responses()) == 4 }, func() string { return fmt.Sprint(responses()) })
			got := responses()
			for _, key := range []string{"question-direct", "question-fallback"} {
				if !strings.Contains(string(got[key]["result"]), `"outcome":"cancelled"`) {
					t.Fatalf("question authorized %s", got[key]["result"])
				}
			}
			if len(got["unknown-auth"]["error"]) == 0 || !strings.Contains(string(got["plan"]["result"]), `"outcome":"rejected"`) {
				t.Fatalf("authorization response %v", got)
			}
			for _, frame := range r.Frames(t) {
				if string(frame["method"]) == `"initialize"` && strings.Contains(string(frame["params"]), "askQuestion") {
					t.Fatal("client advertised question capability")
				}
			}
			s, err := store.Open(f.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			reqs, err := s.OpenPermissionRequestsForSpawn(id)
			if err != nil || len(reqs) != 0 {
				t.Fatalf("question reached answerable permission flow: %v %v", reqs, err)
			}
		})
	}
}
func TestCursorUnexpectedRuntimeLossFollowsMissing(t *testing.T) {
	if testing.Short() {
		t.Skip("real pane unexpected runtime loss")
	}
	f, r, launch := newCursorPane(t, false)
	id := launch.ID
	waitFor(t, "fresh waiting", func() bool { return readRow(t, f.DBPath, id).State == store.StateWaiting }, func() string { return fmt.Sprint(readRow(t, f.DBPath, id)) })
	if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: id, Text: "DIE"}); err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Sscan(readFileTrim(t, r.PID), &pid); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "runtime gone unexpectedly", func() bool { return pidGone(pid) }, func() string { return procState(pid) })
	gatewayPID := int(launch.Row.PanePID.(int64))
	waitFor(t, "gateway exited", func() bool { return pidGone(gatewayPID) }, func() string { return procState(gatewayPID) })
	if _, err := f.API.FindMissing(context.Background()); err != nil {
		t.Fatal(err)
	}
	if row := readRow(t, f.DBPath, id); row.State != store.StateMissing {
		t.Fatalf("unexpected runtime loss falsely ended: %+v", row)
	}
}

func TestCursorBaselinePauseNeverSubmitsModelPrompt(t *testing.T) {
	if testing.Short() {
		t.Skip("real pane baseline exit")
	}
	f, r, launch := newCursorPane(t, false)
	id := launch.ID
	waitFor(t, "baseline startup waiting", func() bool { return readRow(t, f.DBPath, id).State == store.StateWaiting }, func() string { return fmt.Sprint(readRow(t, f.DBPath, id)) })
	if _, err := f.API.Pause(context.Background(), api.PauseParams{InstanceID: id}); err != nil {
		t.Fatal(err)
	}
	if row := readRow(t, f.DBPath, id); row.State != store.StateEnded {
		t.Fatalf("baseline stop did not confirm end %+v", row)
	}
	for _, frame := range r.Frames(t) {
		if string(frame["method"]) == `"session/prompt"` {
			t.Fatalf("baseline /exit became model prompt %s", frame["params"])
		}
	}
}

func TestCursorRepeatedCancellationDoesNotExtendDeadline(t *testing.T) {
	for _, mode := range []string{"interrupt", "denial"} {
		t.Run(mode, func(t *testing.T) {
			f, r, launch := newCursorPane(t, false)
			id := launch.ID
			waitFor(t, "fresh waiting", func() bool { return readRow(t, f.DBPath, id).State == store.StateWaiting }, func() string { return fmt.Sprint(readRow(t, f.DBPath, id)) })
			text := "HOLD"
			if mode == "denial" {
				text = "DENY_FOREVER"
			}
			if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: id, Text: text}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "working", func() bool { return readRow(t, f.DBPath, id).State == store.StateWorking }, func() string { return fmt.Sprint(readRow(t, f.DBPath, id)) })
			gatewayPID := int(launch.Row.PanePID.(int64))
			runtimePID := 0
			if _, err := fmt.Sscan(readFileTrim(t, r.PID), &runtimePID); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			for !pidGone(gatewayPID) && time.Since(started) < 5*time.Second {
				if mode == "interrupt" {
					launch.rt.run(t, "send-keys", "-t", launch.Row.PaneID.(string), "C-c")
				}
				time.Sleep(150 * time.Millisecond)
			}
			if !pidGone(gatewayPID) {
				t.Fatal("repeated cancellation extended the operation deadline")
			}
			waitFor(t, "runtime gone after bounded cancellation", func() bool { return pidGone(runtimePID) }, func() string { return procState(runtimePID) })
			cancels := 0
			for _, frame := range r.Frames(t) {
				if string(frame["method"]) == `"session/cancel"` {
					cancels++
				}
			}
			failures := hookTrailRecords(t, os.Getenv("HOME"), "ad.cursor.failed", id)
			if len(failures) != 1 || !strings.Contains(fmt.Sprint(failures[0]["reason"]), "operation timed out") {
				t.Fatalf("cancellation did not retain bounded deadline: %v", failures)
			}
			if cancels != 1 {
				t.Fatalf("cancellation replayed: %d", cancels)
			}
		})
	}
}
