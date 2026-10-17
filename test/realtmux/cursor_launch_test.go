package realtmux_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/cursorhost"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func newCursorPane(t *testing.T, disableQuestions bool) (*spawnFix, cursorfix.Runtime, spawned) {
	t.Helper()
	return newCursorPaneScenario(t, disableQuestions, "")
}

type cursorPaneOptions struct {
	Script       *cursorfix.Scenario
	Relay        string
	Timeout      int
	StandaloneGo bool
	ProgramName  string
	UnsetPath    bool
	AgentArgs    []string
	ExtraEnv     map[string]string
	Unconfigured bool
	BeforeLaunch func(*spawnFix)
}

func newCursorPaneScenario(t *testing.T, disableQuestions bool, scenario string, options ...cursorPaneOptions) (*spawnFix, cursorfix.Runtime, spawned) {
	t.Helper()
	f := newSpawnFix(t)
	r := cursorfix.NewWithWorker(t, scenario == "independent-worker")
	if scenario != "" {
		if err := os.WriteFile(r.Scenario, []byte(scenario), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var opts cursorPaneOptions
	if len(options) > 0 {
		opts = options[0]
	}
	selection := opts.ExtraEnv
	if selection == nil && !opts.Unconfigured {
		selection = map[string]string{"CURSOR_CONFIG_DIR": filepath.Join(t.TempDir(), "cursor-config"), "CURSOR_DATA_DIR": filepath.Join(t.TempDir(), "cursor-data")}
	}
	if opts.BeforeLaunch != nil {
		opts.BeforeLaunch(f)
	}
	if opts.ProgramName != "" {
		whole := filepath.Join(filepath.Dir(r.Program), opts.ProgramName)
		if err := os.Rename(r.Program, whole); err != nil {
			t.Fatal(err)
		}
		r.Program = whole
	}
	if opts.UnsetPath {
		tools := t.TempDir()
		if err := os.Symlink(r.Program, filepath.Join(tools, "agent")); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if opts.Script != nil {
		r.Configure(t, *opts.Script)
	}
	bin := buildHookCLI(t, t.TempDir())
	installed := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "bin", "agent-director")
	if err := os.MkdirAll(filepath.Dir(installed), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bin, installed); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "config.toml")
	cfg, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg = append(cfg, []byte("\n[defaults]\ndisable_ask_user = "+fmt.Sprint(disableQuestions)+"\n")...)
	if opts.Timeout > 0 {
		cfg = append(cfg, []byte(fmt.Sprintf("\n[relay]\ntimeout_seconds = %d\n", opts.Timeout))...)
	}
	if !opts.UnsetPath {
		cfg = append(cfg, []byte(fmt.Sprintf("\n[launch]\ncursor_path = '%s'\n", r.Program))...)
	}
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
	for key, value := range selection {
		args = append(args, "--extra-env", key+"="+value)
	}
	if opts.Relay != "" {
		args = append(args, "--relay-mode", opts.Relay)
	}
	if len(opts.AgentArgs) > 0 {
		args = append(args, "--")
		args = append(args, opts.AgentArgs...)
	}
	if scenario == "blocked-input" {
		args = append(args, "--", strings.Repeat("\x01", 12*1024))
	}
	cmd := exec.Command(bin, args...)
	if scenario == "ts-default-hold" {
		clientSource := installedCursorClient(t)
		js := fmt.Sprintf(`import {Client} from %q;
const options = {storePath: %q, home: %q, _cliPath: %q};
const c = await Client.create(options);
const started = performance.now();
const result = await c.spawn({agent:"cursor", instance_id:%q, cwd:%q, no_pre_trust:true, extra_env:%s, agent_args:["HOLD"]});
const elapsed = performance.now()-started;
if (!(elapsed < 30000)) throw new Error("default launch timeout exceeded");
await c.close();
const recreated = await Client.create(options);
await recreated.get({instance_id:result.instance_id});
await recreated.close();
console.log(JSON.stringify({...result,fixture_whole_call_ms:elapsed}));`, clientSource, f.DBPath, os.Getenv("HOME"), bin, id, f.CWD, func() []byte { raw, _ := json.Marshal(selection); return raw }())
		path := filepath.Join(t.TempDir(), "caller.ts")
		if e := os.WriteFile(path, []byte(js), 0600); e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		cmd = exec.CommandContext(ctx, "bun", path)
	}
	var out []byte
	if opts.StandaloneGo {
		caller := newPaneCaller(t, f, "Go release wrapper", bin)
		out, err = caller.call("spawn", map[string]any{"agent": "cursor", "instance_id": id, "cwd": f.CWD, "no_pre_trust": true, "extra_env": selection})
	} else {
		out, err = cmd.CombinedOutput()
	}
	if err != nil {
		t.Fatalf("production CLI spawn: %v %s", err, out)
	}
	if scenario == "ts-default-hold" {
		t.Logf("installed default spawn receipt=%s", out)
	}
	var spawnResult api.SpawnResult
	if err := json.Unmarshal(out, &spawnResult); err != nil || spawnResult.InstanceID != id {
		t.Fatalf("CLI Cursor result %s %v", out, err)
	}
	launch := f.launched(t, id)
	if scenario != "blocked-input" {
		// SessionStart publishes waiting before the gateway enables prompt input.
		waitFor(t, "gateway ready for pane input", func() bool {
			pane, err := f.API.ReadPane(api.ReadPaneParams{InstanceID: id, NLines: 100})
			return err == nil && strings.Contains(pane.Pane, "Cursor ready")
		}, func() string { return fmt.Sprint(readRow(t, f.DBPath, id)) })
	}
	// Wait for the gateway's final writes as well as the peer before removing files.
	t.Cleanup(func() {
		stopCursorPaneGateways(t, f, launch)
		for _, path := range []string{r.PID, r.WorkerPID} {
			raw, e := os.ReadFile(path)
			if os.IsNotExist(e) {
				continue
			}
			if e != nil {
				t.Error(e)
				continue
			}
			pid := 0
			if _, e = fmt.Sscan(string(raw), &pid); e != nil {
				t.Error(e)
				continue
			}
			if path == r.WorkerPID {
				if e = os.WriteFile(r.WorkerRelease, nil, 0600); e != nil {
					t.Error(e)
				}
			}
			waitFor(t, "fixture process gone before temporary files", func() bool { return pidGone(pid) }, func() string { return procState(pid) })
		}
	})
	return f, r, launch
}

// Snapshot the private server before stopping it: a resumed gateway can still
// be pending, with no current identity published in the row/runtime binding.
func stopCursorPaneGateways(t testing.TB, f *spawnFix, launch spawned) {
	t.Helper()
	gateways := []tmux.ProcIdentity{{PID: int(launch.Row.PanePID.(int64)), Starttime: launch.Row.PaneStarttime.(string)}}
	server := tmux.ProcIdentity{PID: int(launch.Row.TmuxServerPID.(int64)), Starttime: launch.Row.TmuxServerStarttime.(string)}
	serverAbsent := func(result rawResult) bool {
		_, err := os.Stat(launch.rt.Socket)
		noServer := result.Stderr == "no server running on "+launch.rt.Socket+"\n"
		missingSocket := os.IsNotExist(err) && result.Stderr == "error connecting to "+launch.rt.Socket+" (No such file or directory)\n"
		return result.Exit == 1 && result.Stdout == "" && (noServer || missingSocket) && tmux.JudgeProcess(f.PC, server) == tmux.ProcGone
	}
	panes := launch.rt.run(t, "list-panes", "-a", "-F", "#{pane_pid}")
	if panes.Exit != 0 {
		if !serverAbsent(panes) {
			t.Errorf("snapshot fixture panes: %+v", panes)
		} else {
			// A dead server does not prove that a replacement gateway exited.
			row, err := apitest.ReadSpawnColumns(f.DBPath, launch.ID)
			if err != nil {
				t.Errorf("read current fixture gateway after server exit: %v", err)
			} else if pid, ok := row.PanePID.(int64); ok && pid > 0 {
				if start, ok := row.PaneStarttime.(string); ok && start != "" {
					gateways = append(gateways, tmux.ProcIdentity{PID: int(pid), Starttime: start})
				} else {
					t.Errorf("unknown current fixture gateway start time: %+v", row)
				}
			} else {
				t.Errorf("unpublished current fixture gateway identity after server exit: %+v", row)
			}
		}
	} else {
		for _, raw := range strings.Fields(panes.Stdout) {
			pid, err := strconv.Atoi(raw)
			if err != nil || pid <= 0 {
				t.Errorf("invalid fixture pane PID %q: %v", raw, err)
				continue
			}
			start, alive, known := f.PC.StartTime(pid)
			if !known {
				t.Errorf("unknown fixture pane identity: %s", procState(pid))
			}
			if alive || !known {
				gateways = append(gateways, tmux.ProcIdentity{PID: pid, Starttime: start})
			}
		}
	}
	result := launch.rt.run(t, "kill-server")
	lostReply := result.Exit == 1 && result.Stdout == "" && result.Stderr == "server exited unexpectedly\n"
	if result.Exit != 0 {
		t.Logf("stop fixture server raw result: %+v", result)
		if !serverAbsent(result) && !lostReply {
			t.Errorf("stop fixture server: %+v", result)
		}
	}
	// All gateway identities share the existing single wait budget.
	waitFor(t, "fixture gateways gone before temporary files", func() bool {
		if tmux.JudgeProcess(f.PC, server) != tmux.ProcGone {
			return false
		}
		for _, gateway := range gateways {
			if tmux.JudgeProcess(f.PC, gateway) != tmux.ProcGone {
				return false
			}
		}
		return true
	}, func() string {
		states := []string{fmt.Sprintf("server=%+v judge=%v %s; kill=%+v", server, tmux.JudgeProcess(f.PC, server), procState(server.PID), result)}
		for _, gateway := range gateways {
			states = append(states, fmt.Sprintf("%+v judge=%v %s", gateway, tmux.JudgeProcess(f.PC, gateway), procState(gateway.PID)))
		}
		return strings.Join(states, "; ")
	})
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
	for index, text := range []string{"/clear", "/new"} {
		outgoing := readRow(t, f.DBPath, id)
		previous := outgoing.SessionID
		send(text)
		waitFor(t, "real hook published fresh conversation", func() bool {
			return readRow(t, f.DBPath, id).SessionID != previous && readRow(t, f.DBPath, id).State == store.StateWaiting
		}, func() string { return fmt.Sprint(readRow(t, f.DBPath, id)) })
		paneContains("Cursor fresh conversation published")
		// Independent reads verify the actual built hook's three-write transaction.
		current := readRow(t, f.DBPath, id)
		actual, err := store.Open(f.DBPath)
		if err != nil {
			t.Fatal(err)
		}
		bound, bindErr := actual.ReadCursorRuntime(id)
		history, historyErr := actual.ListSessionHistory(id, bound.Life)
		actual.Close()
		found := false
		for _, entry := range history {
			found = found || (entry.SessionID == previous && entry.JSONLPath == outgoing.JSONLPath)
		}
		if bindErr != nil || historyErr != nil || bound.SessionID != current.SessionID || len(history) != index+1 || !found || current.LifeNumber != before.LifeNumber || current.JSONLPath != before.JSONLPath {
			t.Fatalf("actual hook lost atomic identity/history: row=%+v binding=%+v history=%+v errors=%v/%v", current, bound, history, bindErr, historyErr)
		}
	}
	send("HOLD")
	state(store.StateWorking)
	send("queued follow-up")
	waitFor(t, "follow-up accepted while HOLD runs", func() bool {
		raw, err := os.ReadFile(readRow(t, f.DBPath, id).JSONLPath.(string))
		return err == nil && strings.Contains(string(raw), `"text":"queued follow-up"`)
	}, func() string { return fmt.Sprint(prompts()) })
	if err = os.WriteFile(r.Release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	want := append(expected, "HOLD", "queued follow-up")
	waitFor(t, "queued follow-up completes", func() bool {
		return len(prompts()) == len(want) && readRow(t, f.DBPath, id).State == store.StateWaiting
	}, func() string { return fmt.Sprint(prompts()) })
	if got := prompts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("queued FIFO prompts: %q", got)
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
			// Keep this question-policy check independent of unsupported extension requests.
			r.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", Requests: []json.RawMessage{
				json.RawMessage(`{"jsonrpc":"2.0","id":"question-direct","method":"cursor/ask_question","params":{"questions":[{"text":"one"},{"text":"two"}]}}`),
				json.RawMessage(`{"jsonrpc":"2.0","id":"question-fallback","method":"session/request_permission","params":{"toolCall":{"title":"AskUserQuestion"},"options":[{"optionId":"yes","kind":"allow_once"}]}}`),
			}})
			if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: id, Text: "question policy"}); err != nil {
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
			waitFor(t, "both questions denied", func() bool { return len(responses()) == 2 }, func() string { return fmt.Sprint(responses()) })
			got := responses()
			for _, key := range []string{"question-direct", "question-fallback"} {
				if !strings.Contains(string(got[key]["result"]), `"outcome":"cancelled"`) {
					t.Fatalf("question authorized %s", got[key]["result"])
				}
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
			waitFor(t, "actual prompt receipt before cancellation", func() bool {
				for _, frame := range r.Frames(t) {
					if string(frame["method"]) == `"session/prompt"` {
						return true
					}
				}
				return false
			}, func() string { return fmt.Sprint(r.Frames(t)) })
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
			reason := "operation timed out"
			if mode == "denial" {
				reason = "authorization after cancellation; no response sent"
			}
			if len(failures) != 1 || !strings.Contains(fmt.Sprint(failures[0]["reason"]), reason) {
				t.Fatalf("cancellation did not retain bounded deadline: %v", failures)
			}
			if cancels != 1 {
				t.Fatalf("cancellation replayed: %d", cancels)
			}
		})
	}
}

// Synthetic ACP: actual Bun default deadline, then fresh CLI/MCP callers on the same live conversation.
func TestCursorDefaultTypeScriptLaunchAndCallerRecreation(t *testing.T) {
	if testing.Short() {
		t.Skip("real pane default TypeScript timing")
	}
	f, r, launch := newCursorPaneScenario(t, false, "ts-default-hold")
	waitFor(t, "unfinished initial generation", func() bool { return readRow(t, f.DBPath, launch.ID).State == store.StateWorking }, func() string { return fmt.Sprint(readRow(t, f.DBPath, launch.ID)) })
	before := readRow(t, f.DBPath, launch.ID)
	decoyPath := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "state.db")
	if _, err := apitest.SeedSpawn(decoyPath, launch.ID, store.StateWaiting, f.CWD, "off", "unrelated-session", false, apitest.WithAgent(store.AgentCursor)); err != nil {
		t.Fatal(err)
	}
	decoy := readRow(t, decoyPath, launch.ID)
	runtimePID := readFileTrim(t, r.PID)
	bin := buildHookCLI(t, t.TempDir())
	for i := 0; i < 2; i++ {
		m := startStdioMCP(t, bin, []string{"serve", "--stdio", "--store-path", f.DBPath}, os.Environ(), f.CWD)
		m.request("initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "cursor-regression", "version": "1"}})
		text := m.callTool("get", map[string]any{"instance_id": launch.ID})
		if !strings.Contains(text, launch.ID) {
			t.Fatalf("recreated MCP get: %s", text)
		}
		m.stop()
	}
	after := readRow(t, f.DBPath, launch.ID)
	if before.SessionID != after.SessionID || before.PanePID != after.PanePID || before.PaneStarttime != after.PaneStarttime || before.LaunchToken != after.LaunchToken || runtimePID != readFileTrim(t, r.PID) {
		t.Fatal("caller recreation replaced conversation identity")
	}
	if after.State != store.StateWorking {
		t.Fatal("generation finished before release")
	}
	if err := os.WriteFile(r.Release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "released generation", func() bool { return readRow(t, f.DBPath, launch.ID).State == store.StateWaiting }, func() string { return fmt.Sprint(readRow(t, f.DBPath, launch.ID)) })
	if _, err := f.API.Pause(context.Background(), api.PauseParams{InstanceID: launch.ID}); err != nil {
		t.Fatal(err)
	}
	if got := readRow(t, decoyPath, launch.ID); !reflect.DeepEqual(got, decoy) {
		t.Fatal("override gateway mutated unrelated same-id default-store row")
	}
}

func TestCursorIndependentPaneVerifierRejectsChangedOwnership(t *testing.T) {
	if testing.Short() {
		t.Skip("real pane ownership mutations")
	}
	for _, tc := range []struct{ change, phase string }{{"moved", "fresh"}, {"respawned", "fresh"}, {"conflicting label", "fresh"}, {"server replaced", "fresh"}, {"moved", "reuse"}, {"respawned", "rotation"}} {
		t.Run(tc.change+"/"+tc.phase, func(t *testing.T) {
			change := tc.change
			f, runtime, launch := newCursorPane(t, false)
			original := readRow(t, f.DBPath, launch.ID)
			if tc.phase == "reuse" {
				if _, err := f.API.Pause(context.Background(), api.PauseParams{InstanceID: launch.ID}); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "prior gateway exited", func() bool { return pidGone(int(original.PanePID.(int64))) }, func() string { return fmt.Sprint(readRow(t, f.DBPath, launch.ID)) })
				runtime.Configure(t, cursorfix.Scenario{SessionID: "reused-pane-session"})
				if _, err := f.API.Spawn(api.SpawnParams{InstanceID: launch.ID, Agent: "cursor", CWD: f.CWD, ReuseFinished: true, NoPreTrust: true}); err != nil {
					t.Fatal(err)
				}
			} else if tc.phase == "rotation" {
				if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "/new"}); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "rotation published", func() bool { return readRow(t, f.DBPath, launch.ID).SessionID != original.SessionID }, func() string { return fmt.Sprint(readRow(t, f.DBPath, launch.ID)) })
			}
			waitFor(t, "fresh waiting", func() bool { return readRow(t, f.DBPath, launch.ID).State == store.StateWaiting }, func() string { return fmt.Sprint(readRow(t, f.DBPath, launch.ID)) })
			st, err := store.Open(f.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			row, err := st.GetSpawn(launch.ID)
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.Default()
			if err = cursorhost.VerifyPane(st, row, cfg); err != nil {
				t.Fatalf("original pane: %v", err)
			}
			binding, err := st.ReadCursorRuntime(launch.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "moved":
				other := launch.rt.startSession(t, uniqueName())
				launch.rt.must(t, "join-pane", "-s", row.Identity.PaneID, "-t", other.PaneID)
			case "respawned":
				launch.rt.must(t, "respawn-pane", "-k", "-t", row.Identity.PaneID, "sleep 30")
			case "conflicting label":
				launch.rt.must(t, "set-option", "-p", "-t", row.Identity.PaneID, "@ad_pane", "foreign-token")
			case "server replaced":
				launch.rt.must(t, "kill-server")
				waitFor(t, "old server gone", func() bool { return pidGone(row.Identity.ServerPID) }, func() string { return procState(row.Identity.ServerPID) })
				launch.rt.startSession(t, uniqueName())
			}
			if change == "server replaced" || change == "respawned" {
				pc := probe.NewProcChecker()
				waitFor(t, "old gateway/runtime gone and terminal close committed", func() bool {
					gateway := tmux.JudgeProcess(pc, tmux.ProcIdentity{PID: binding.GatewayPID, Starttime: binding.GatewayStart})
					runtime := tmux.JudgeProcess(pc, tmux.ProcIdentity{PID: binding.RuntimePID, Starttime: binding.RuntimeStart})
					state := readRow(t, f.DBPath, launch.ID).State
					return gateway == tmux.ProcGone && runtime == tmux.ProcGone && (state == store.StateEnded || state == store.StateMissing)
				}, func() string {
					return fmt.Sprintf("gateway=%s runtime=%s row=%+v", procState(binding.GatewayPID), procState(binding.RuntimePID), readRow(t, f.DBPath, launch.ID))
				})
			}
			beforeVerify := readRow(t, f.DBPath, launch.ID)
			if err = cursorhost.VerifyPane(st, row, cfg); err == nil {
				t.Fatal("changed pane accepted by independent verifier")
			}
			if got := readRow(t, f.DBPath, launch.ID); !reflect.DeepEqual(got, beforeVerify) {
				t.Fatalf("verifier mutated raw row: before=%+v after=%+v", beforeVerify, got)
			}
		})
	}
}
