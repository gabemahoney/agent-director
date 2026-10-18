//go:build linux

package cursorhost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/cursormcp"
	"github.com/gabemahoney/agent-director/internal/helptext"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/clibuild"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

var guidanceBuild struct {
	sync.Once
	path string
	err  error
}

func TestCursorGuidanceActualScopedToolsIgnoreAmbientStoreAndSocket(t *testing.T) {
	// Serial: protected runtime and fake tmux fixtures own HOME/PATH.
	e := newRequestEnv(t, "off", "synthetic-session-1")
	enableGuidance(t, e)
	configPath := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "config.toml")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf("[defaults]\ninject_help_hook = true\n[launch]\ncursor_path = '%s'\n", e.runtime.Program)), 0600); err != nil {
		t.Fatal(err)
	}
	row, err := e.s.GetSpawn(e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "state.db")
	if _, err := apitest.SeedSpawn(decoy, row.InstanceID, "ended", "/decoy", "off", "decoy-session", true); err != nil {
		t.Fatal(err)
	}
	faketmuxfix.Tables{}.Update(t, row.Identity.Socket, func(table *faketmuxfix.Table) { table.Sessions[0].Panes[0].Capture = "intended socket pane" })
	executable := filepath.Join(t.TempDir(), "CLI ; $(literal)")
	if err := os.Symlink(guidanceCLI(t), executable); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "home ; $(literal)")
	if err := os.Symlink(os.Getenv("HOME"), home); err != nil {
		t.Fatal(err)
	}
	observation, registration, err := cursormcp.New(executable, home, e.s, row, e.binding, time.Now().Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer observation.Close()
	scenario := guidanceScenario(t)
	scenario.MCPAmbientEnv = map[string]string{"TMUX": "/decoy/socket,123,0", "TMUX_PANE": "%999", "AGENT_DIRECTOR_INSTANCE_ID": "decoy-parent", "AGENT_DIRECTOR_CURSOR_STORE_PATH": decoy}
	scenario.MCPCalls = []map[string]any{
		{"name": "get", "arguments": map[string]any{"instance_id": row.InstanceID}},
		{"name": "read-pane", "arguments": map[string]any{"instance_id": row.InstanceID}},
		{"name": "spawn", "arguments": map[string]any{"instance_id": "scoped-child", "agent": "cursor", "cwd": row.CWD, "no_pre_trust": true}},
	}
	e.runtime.Configure(t, scenario)
	rpc := newProtocol(e.process.input, e.process.output)
	defer rpc.close()
	if _, err := rpc.request("session/new", map[string]any{"cwd": row.CWD, "mcpServers": []cursormcp.Stdio{registration}}); err != nil {
		t.Fatal(err)
	}
	f := nextFrame(t, rpc)
	if f.err != nil {
		t.Fatal(f.err)
	}
	if err := observation.Wait(); err != nil {
		t.Fatal(err)
	}
	c := &cursorSession{e: e}
	until := time.Now().Add(3 * time.Second)
	var results []string
	for time.Now().Before(until) {
		results = nil
		for _, event := range mcpEvents(t, c) {
			if string(event["event"]) != `"tool_response"` {
				continue
			}
			var response struct {
				Result struct {
					IsError bool `json:"isError"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
			}
			if err := json.Unmarshal(event["response"], &response); err != nil {
				t.Fatal(err)
			}
			if response.Result.IsError || len(response.Result.Content) != 1 {
				t.Fatalf("tool failed: %s", event["response"])
			}
			results = append(results, response.Result.Content[0].Text)
		}
		if len(results) == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(results) != 3 || !bytes.Contains([]byte(results[0]), []byte(row.CWD)) || !bytes.Contains([]byte(results[1]), []byte("intended socket pane")) || bytes.Contains([]byte(results[0]), []byte("decoy-session")) {
		t.Fatalf("actual dispatcher routes=%q", results)
	}
	retained, err := apitest.ReadSpawnColumns(decoy, row.InstanceID)
	if err != nil || retained.SessionID != "decoy-session" {
		t.Fatalf("decoy changed: %+v %v", retained, err)
	}
	child, err := e.s.GetSpawn("scoped-child")
	if err != nil || child.ParentID != row.InstanceID || child.Identity.Socket != row.Identity.Socket {
		t.Fatalf("actual dispatcher parent=%+v %v", child, err)
	}
	if _, err := apitest.ReadSpawnColumns(decoy, "scoped-child"); err == nil {
		t.Fatal("scoped child reached ambient store")
	}
	// Follow the public two-command example literally, substituting only its store path.
	const example = `agent-director --home "$HOME" --store-path /srv/ad/state.db spawn --cwd "$PWD" --agent cursor
agent-director --home "$HOME" --store-path /srv/ad/state.db list --parent "$AGENT_DIRECTOR_INSTANCE_ID"`
	usage, err := os.ReadFile(filepath.Join("..", "..", "docs", "usage.md"))
	if err != nil || !strings.Contains(string(usage), "```sh\n"+example+"\n```") {
		t.Fatalf("public CLI example changed; recheck its literal follow: %v", err)
	}
	cliDir := t.TempDir()
	if err := os.Symlink(executable, filepath.Join(cliDir, "agent-director")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-e", "-c", strings.ReplaceAll(example, "/srv/ad/state.db", "'"+strings.ReplaceAll(e.s.Path(), "'", "'\"'\"'")+"'"))
	cmd.Dir = row.CWD
	cmd.Env = append(os.Environ(), "PATH="+cliDir+":"+os.Getenv("PATH"), "HOME="+home, "PWD="+row.CWD, "AGENT_DIRECTOR_INSTANCE_ID="+row.InstanceID, "AGENT_DIRECTOR_CURSOR_STORE_PATH="+decoy, "TMUX="+fmt.Sprintf("%s,%d,0", row.Identity.Socket, row.Identity.ServerPID), "TMUX_TMPDIR="+t.TempDir())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("literal CLI override route: %v stdout=%s stderr=%s", err, &stdout, &stderr)
	}
	decoder := json.NewDecoder(&stdout)
	var spawned struct {
		InstanceID string `json:"instance_id"`
	}
	if err := decoder.Decode(&spawned); err != nil || spawned.InstanceID == "" {
		t.Fatalf("literal spawn response: %+v %v", spawned, err)
	}
	var listed struct {
		Spawns []struct {
			InstanceID string `json:"instance_id"`
			ParentID   string `json:"parent_id"`
		} `json:"spawns"`
	}
	if err := decoder.Decode(&listed); err != nil {
		t.Fatalf("literal list response: %v", err)
	}
	found := false
	for _, child := range listed.Spawns {
		if child.ParentID != row.InstanceID {
			t.Fatalf("list --parent returned unrelated row: %+v", child)
		}
		found = found || child.InstanceID == spawned.InstanceID
	}
	if !found {
		t.Fatalf("literal list did not find spawned child %q: %+v", spawned.InstanceID, listed)
	}
	cliID := spawned.InstanceID
	cliChild, err := e.s.GetSpawn(cliID)
	if err != nil || cliChild.ParentID != row.InstanceID || cliChild.Identity.Socket != row.Identity.Socket || cliChild.CWD != row.CWD || cliChild.Agent != store.AgentCursor {
		t.Fatalf("CLI persisted child route: %+v %v", cliChild, err)
	}
	if !strings.Contains(string(usage), "with `get --instance-id <child-id>` and find associated children with\n`list --parent <parent-id>`.") {
		t.Fatal("public child inspection instructions changed; recheck their literal follow")
	}
	cmd = exec.Command(executable, "--home", home, "--store-path", e.s.Path(), "get", "--instance-id", cliID)
	cmd.Env = append(os.Environ(), "AGENT_DIRECTOR_CURSOR_STORE_PATH="+decoy)
	output, err := cmd.Output()
	var inspected struct {
		InstanceID string `json:"instance_id"`
		ParentID   string `json:"parent_id"`
		Socket     string `json:"tmux_socket"`
	}
	if err != nil {
		t.Fatalf("literal get child: %v", err)
	}
	if err := json.Unmarshal(output, &inspected); err != nil || inspected.InstanceID != cliID || inspected.ParentID != row.InstanceID || inspected.Socket != row.Identity.Socket {
		t.Fatalf("literal get child route: %+v %v", inspected, err)
	}
	if _, err := apitest.ReadSpawnColumns(decoy, cliID); err == nil {
		t.Fatal("explicit CLI child reached decoy private store")
	}
	retained, err = apitest.ReadSpawnColumns(decoy, row.InstanceID)
	if err != nil || retained.SessionID != "decoy-session" {
		t.Fatalf("CLI changed default-store decoy: %+v %v", retained, err)
	}
}

func guidanceCLI(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("AD_TEST_GUIDANCE_CLI"); path != "" {
		return path
	}
	guidanceBuild.Do(func() {
		var dir string
		dir, guidanceBuild.err = os.MkdirTemp("", "ad-guidance-cli-")
		if guidanceBuild.err == nil {
			cursorTestCleanup = func() { os.RemoveAll(dir) }
			guidanceBuild.path = filepath.Join(dir, "agent-director")
			guidanceBuild.err = clibuild.Build(guidanceBuild.path)
		}
	})
	if guidanceBuild.err != nil {
		t.Fatal(guidanceBuild.err)
	}
	return guidanceBuild.path
}

func TestCursorGuidanceConfiguredRestartAfterObserverDeadline(t *testing.T) {
	// Serial: Run owns HOME, signal handlers and the protected runtime fixture.
	if testing.Short() {
		t.Skip("actual configured process restart")
	}
	c := beginConfiguredCursorSession(t, Options{mcpExecutable: guidanceCLI(t)}, guidanceScenario(t), openTranscript, terminalLoop, func(e *requestEnv) {
		enableGuidance(t, e)
		t.Setenv("AD_CURSOR_FIX_SETTINGS", e.runtime.Settings())
	})
	c.wait(t, "published ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
	id := c.e.binding.InstanceID
	before, err := c.e.s.ReadCursorRuntime(id)
	if err != nil {
		t.Fatal(err)
	}
	row, err := c.e.s.GetSpawn(id)
	if err != nil {
		t.Fatal(err)
	}
	history, err := c.e.s.ListSessionHistory(id, row.LifeNumber)
	if err != nil {
		t.Fatal(err)
	}
	command := func(id, operation string, calls ...map[string]any) map[string]json.RawMessage {
		c.e.runtime.MCPCommand(t, id, operation, calls...)
		var result map[string]json.RawMessage
		c.wait(t, operation, func() bool {
			for _, event := range mcpEvents(t, c) {
				if string(event["event"]) == `"control_result"` && string(event["control"]) == fmt.Sprintf("%q", id) {
					result = event
					if len(event["error"]) != 0 {
						t.Fatalf("configured control failed: %s", event["error"])
					}
					return true
				}
			}
			return false
		})
		return result
	}
	get := map[string]any{"name": "get", "arguments": map[string]any{"instance_id": id}}
	command("before", "calls", get)
	var until time.Time
	for _, event := range mcpEvents(t, c) {
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
	if until.IsZero() || time.Until(until) > protocolWait {
		t.Fatalf("missing original observer bound: %v", until)
	}
	if remaining := time.Until(until); remaining > 0 {
		time.Sleep(remaining)
	}
	stopped := command("stop", "stop")
	var oldPID int
	json.Unmarshal(stopped["pid"], &oldPID)
	c.wait(t, "configured child independently gone", func() bool { _, err := os.Stat(fmt.Sprintf("/proc/%d", oldPID)); return os.IsNotExist(err) })
	replacement := command("replacement", "restart", get)
	if bytes.Equal(stopped["pid"], replacement["pid"]) {
		t.Fatal("configured process was not replaced")
	}
	if !guidanceEvent(t, c, "response", 2) || !guidanceEvent(t, c, "notification", 2) {
		t.Fatal("replacement missing standard initialization")
	}
	c.keys(t, "useful parent after configured restart\r")
	c.wait(t, "parent result after restart", func() bool {
		return bytes.Contains(c.pane.bytes(), []byte("Synthetic reply: useful parent after configured restart"))
	})
	c.wait(t, "parent outcome", func() bool { return rotationRecordCount(c, t, "turn_outcome") == 1 })
	after, err := c.e.s.ReadCursorRuntime(id)
	if err != nil || before != after {
		t.Fatalf("runtime binding changed: before=%+v after=%+v err=%v", before, after, err)
	}
	afterRow, err := c.e.s.GetSpawn(id)
	if err != nil || row.Identity != afterRow.Identity || row.LifeNumber != afterRow.LifeNumber || row.SessionID != afterRow.SessionID || row.JSONLPath != afterRow.JSONLPath {
		t.Fatalf("parent identity changed: %+v %v", afterRow, err)
	}
	afterHistory, err := c.e.s.ListSessionHistory(id, row.LifeNumber)
	if err != nil || !reflect.DeepEqual(history, afterHistory) {
		t.Fatalf("history changed: %+v %v", afterHistory, err)
	}
	for _, identity := range []struct {
		pid   int
		start string
	}{{before.GatewayPID, before.GatewayStart}, {before.RuntimePID, before.RuntimeStart}} {
		start, alive, known := probe.NewProcChecker().StartTime(identity.pid)
		if !known || !alive || start != identity.start {
			t.Fatal("parent process identity changed")
		}
	}
	for _, event := range mcpEvents(t, c) {
		if string(event["event"]) == `"tool_response"` {
			var response struct {
				Result struct {
					IsError bool `json:"isError"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
			}
			if err := json.Unmarshal(event["response"], &response); err != nil || response.Result.IsError || len(response.Result.Content) != 1 || !bytes.Contains([]byte(response.Result.Content[0].Text), []byte(id)) {
				t.Fatalf("useful configured get failed: %s", event["response"])
			}
		}
	}
	if rotationRecordCount(c, t, "guidance_initialized") != 1 {
		t.Fatal("restart retroactively certified a setup attempt")
	}
	c.stop(t)
}
func enableGuidance(t *testing.T, e *requestEnv) {
	t.Helper()
	path := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[defaults]\ninject_help_hook = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			raw, _ := os.ReadFile(e.runtime.MCPTrace)
			t.Logf("actual MCP trace: %s", raw)
		}
	})
}
func guidanceScenario(t *testing.T) cursorfix.Scenario {
	t.Helper()
	plain, err := helptext.Plain()
	if err != nil {
		t.Fatal(err)
	}
	return cursorfix.Scenario{SessionID: "synthetic-session-1", LoadSupport: true, MCPExpectedInstructions: plain}
}
func mcpEvents(t *testing.T, c *cursorSession) []map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(c.e.runtime.MCPTrace)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	end := bytes.LastIndexByte(raw, '\n')
	if end < 0 {
		return nil
	}
	var out []map[string]json.RawMessage
	for _, line := range bytes.Split(raw[:end], []byte("\n")) {
		var f map[string]json.RawMessage
		if err := json.Unmarshal(line, &f); err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
	return out
}
func guidanceEvent(t *testing.T, c *cursorSession, event string, attempt int) bool {
	for _, f := range mcpEvents(t, c) {
		var got string
		var n int
		json.Unmarshal(f["event"], &got)
		json.Unmarshal(f["attempt"], &n)
		if got == event && n == attempt {
			return true
		}
	}
	return false
}
func TestCursorGuidanceFreshAndLatestLoad(t *testing.T) {
	// Serial: Run owns process environment and signal handlers.
	for _, load := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "latest load"}[load], func(t *testing.T) {
			scenario := guidanceScenario(t)
			scenario.MCPWaitAttempt = 1
			input := &completeInput{ready: make(chan struct{})}
			options := Options{InitialPrompt: "HOLD", mcpExecutable: guidanceCLI(t)}
			var c *cursorSession
			if load {
				c = beginCursorLoadWithOptions(t, scenario, options, input.start, nil, func(e *requestEnv) { enableGuidance(t, e) })
			} else {
				c = beginConfiguredCursorSession(t, options, scenario, openTranscript, input.start, func(e *requestEnv) { enableGuidance(t, e) })
			}
			<-input.ready
			c.wait(t, "actual MCP response before notification", func() bool { return guidanceEvent(t, c, "response", 1) })
			if input.admit() || len(rotationPrompts(c, t)) != 0 {
				t.Fatal("MCP response alone admitted work")
			}
			b, err := c.e.s.ReadCursorRuntime(c.e.binding.InstanceID)
			if err != nil || b.SessionID != "" {
				t.Fatalf("before initialization published runtime=%+v %v", b, err)
			}
			rotationRelease(t, c.e.runtime.MCPRelease)
			c.wait(t, "initial prompt after client initialization", func() bool { return len(rotationPrompts(c, t)) == 1 })
			c.wait(t, "input admission after initialization", input.admit)
			input.emit(terminalInput{text: "第二\t🦋\nline", accepted: true})
			c.wait(t, "queued acceptance", func() bool { return rotationRecordCount(c, t, "submission_accepted") == 2 })
			rotationRelease(t, c.e.runtime.Release)
			c.wait(t, "FIFO completion", func() bool { return rotationRecordCount(c, t, "turn_outcome") == 2 })
			if got := rotationPrompts(c, t); !reflect.DeepEqual(got, []rotationPrompt{{"synthetic-session-1", "HOLD"}, {"synthetic-session-1", "第二\t🦋\nline"}}) {
				t.Fatalf("exact once FIFO=%+v", got)
			}
			c.stop(t)
			configured, initialized, published, dispatched := uint64(0), uint64(0), uint64(0), uint64(0)
			for _, r := range c.records(t) {
				switch r.Kind {
				case "guidance_configured":
					configured = r.Sequence
				case "guidance_initialized":
					initialized = r.Sequence
				case "session_start", "session_loaded":
					published = r.Sequence
				case "submission_started":
					if dispatched == 0 {
						dispatched = r.Sequence
					}
				}
			}
			if configured == 0 || !(configured < initialized && initialized < published && published < dispatched) {
				t.Fatalf("guidance/publication/admission order=%d,%d,%d,%d", configured, initialized, published, dispatched)
			}
			method := `"session/new"`
			if load {
				method = `"session/load"`
			}
			setups := 0
			for _, f := range c.e.runtime.Frames(t) {
				if string(f["method"]) == method {
					setups++
				}
			}
			if setups != 1 {
				t.Fatalf("session setups=%d", setups)
			}
		})
	}
}

func TestCursorGuidanceRotationWaitsForCurrentAttempt(t *testing.T) {
	// Serial: verified pane, process environment and Run signals.
	for _, alias := range []string{"/clear", "/new"} {
		t.Run(alias, func(t *testing.T) {
			scenario := guidanceScenario(t)
			scenario.MCPWaitAttempt = 2
			c := beginConfiguredCursorSession(t, Options{mcpExecutable: guidanceCLI(t)}, scenario, openTranscript, terminalLoop, func(e *requestEnv) { enableGuidance(t, e) })
			c.wait(t, "initial ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
			c.keys(t, alias+"\r")
			c.wait(t, "new attempt response", func() bool { return guidanceEvent(t, c, "response", 2) })
			row, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
			if err != nil || row.SessionID != "synthetic-session-1" {
				t.Fatalf("candidate response published %+v %v", row, err)
			}
			if rotationRecordCount(c, t, "guidance_initialized") != 1 {
				t.Fatal("old acknowledgement satisfied new attempt")
			}
			c.keys(t, "held new text\r")
			rotationNoBytes(t, c, "held new text")
			rotationRelease(t, c.e.runtime.MCPRelease)
			c.wait(t, "new session initialized", func() bool { return rotationRecordCount(c, t, "guidance_initialized") == 2 })
			c.wait(t, "held prompt completed", func() bool { return rotationRecordCount(c, t, "turn_outcome") == 1 })
			if got := rotationPrompts(c, t); !reflect.DeepEqual(got, []rotationPrompt{{"synthetic-session-2", "held new text"}}) {
				t.Fatalf("rotation FIFO=%+v", got)
			}
			c.stop(t)
		})
	}
}

func TestCursorGuidanceRotationMissingNotificationKeepsFirstBound(t *testing.T) {
	// Serial: verified pane, environment and Run signals.
	scenario := guidanceScenario(t)
	c := beginConfiguredCursorSession(t, Options{mcpExecutable: guidanceCLI(t)}, scenario, openTranscript, terminalLoop, func(e *requestEnv) { enableGuidance(t, e) })
	c.wait(t, "ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
	scenario.MCPOmitNotification = true
	c.e.runtime.Configure(t, scenario)
	started := time.Now()
	c.keys(t, "/new\r")
	c.wait(t, "unacknowledged response", func() bool { return guidanceEvent(t, c, "response", 2) })
	c.keys(t, "never dispatch\r/new\r")
	c.awaitFailure(t, time.Until(started.Add(cancelWait+time.Second)))
	if time.Since(started) < cancelWait-time.Second {
		t.Fatal("missing notification failed before original bound")
	}
	if len(rotationPrompts(c, t)) != 0 || rotationRecordCount(c, t, "guidance_initialized") != 1 {
		t.Fatal("uninitialized replacement admitted work")
	}
}

func TestCursorGuidanceProtectedRecordFailurePreventsPublication(t *testing.T) {
	// Serial: fixture owns HOME/PATH and Run signal handlers.
	scenario := guidanceScenario(t)
	opener := func(home string, b store.CursorRuntime) (*transcript, error) {
		tr, err := openTranscript(home, b)
		if err == nil {
			tr.file = &failGuidanceRecord{transcriptFile: tr.file}
		}
		return tr, err
	}
	c := beginConfiguredCursorSession(t, Options{InitialPrompt: "never dispatch", mcpExecutable: guidanceCLI(t)}, scenario, opener, terminalLoop, func(e *requestEnv) { enableGuidance(t, e) })
	c.awaitFailure(t, 3*time.Second)
	if len(rotationPrompts(c, t)) != 0 {
		t.Fatal("failed guidance record admitted prompt")
	}
}

func TestCursorGuidanceWaitingRetainsControlAndRuntimeSupervision(t *testing.T) {
	// Serial: real Run owns process environment and signal handlers.
	for _, control := range []string{"exit", "interrupt", "runtime death"} {
		t.Run(control, func(t *testing.T) {
			scenario := guidanceScenario(t)
			scenario.MCPWaitAttempt = 1
			input := &completeInput{ready: make(chan struct{})}
			c := beginConfiguredCursorSession(t, Options{InitialPrompt: "never dispatch", mcpExecutable: guidanceCLI(t)}, scenario, openTranscript, input.start, func(e *requestEnv) { enableGuidance(t, e) })
			<-input.ready
			c.wait(t, "actual response before interrupted acknowledgement", func() bool { return guidanceEvent(t, c, "response", 1) })
			started := time.Now()
			if control == "runtime death" {
				raw, err := os.ReadFile(c.e.runtime.PID)
				if err != nil {
					t.Fatal(err)
				}
				var pid int
				if _, err := fmt.Sscan(string(raw), &pid); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
			} else if control == "exit" {
				input.emit(terminalInput{text: "/exit"})
			} else {
				input.emit(terminalInput{interrupt: true})
			}
			select {
			case err := <-c.done:
				c.ended = true
				c.endErr = err
			case <-time.After(time.Until(started.Add(cancelWait + time.Second))):
				t.Fatal("guidance wait concealed control/runtime loss")
			}
			if len(rotationPrompts(c, t)) != 0 || rotationRecordCount(c, t, "guidance_initialized") != 0 {
				t.Fatal("interrupted guidance admitted work")
			}
			b, err := c.e.s.ReadCursorRuntime(c.e.binding.InstanceID)
			if err != nil || b.SessionID != "" {
				t.Fatalf("unqualified startup published: %+v %v", b, err)
			}
		})
	}
}

func TestCursorGuidanceFreshMissingNotificationExpiresOriginalProtocolBound(t *testing.T) {
	// Serial: real Run owns process environment and signal handlers.
	if testing.Short() {
		t.Skip("retains actual 45-second startup bound")
	}
	scenario := guidanceScenario(t)
	scenario.MCPOmitNotification = true
	executable := guidanceCLI(t)
	started := time.Now()
	c := beginConfiguredCursorSession(t, Options{InitialPrompt: "never dispatch", mcpExecutable: executable}, scenario, openTranscript, terminalLoop, func(e *requestEnv) { enableGuidance(t, e) })
	c.wait(t, "actual unacknowledged fresh initialize", func() bool { return guidanceEvent(t, c, "response", 1) })
	c.awaitFailure(t, time.Until(started.Add(protocolWait+time.Second)))
	if time.Since(started) < protocolWait-time.Second {
		t.Fatal("fresh initialization abandoned original bound early")
	}
	if len(rotationPrompts(c, t)) != 0 || rotationRecordCount(c, t, "guidance_initialized") != 0 {
		t.Fatal("unacknowledged startup admitted work")
	}
}

type failGuidanceRecord struct{ transcriptFile }

func (f *failGuidanceRecord) Write(raw []byte) (int, error) {
	if bytes.Contains(raw, []byte(`"kind":"guidance_initialized"`)) {
		return 0, os.ErrPermission
	}
	return f.transcriptFile.Write(raw)
}

func TestCursorGuidanceBootstrapUsesProtectedLiteralRoutes(t *testing.T) {
	// Serial: request fixture owns HOME/PATH and protected runtime identities.
	e := newRequestEnv(t, "off", "synthetic-session-1")
	row, err := e.s.GetSpawn(e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX", "/ambient/decoy,123,0")
	t.Setenv("TMUX_PANE", "%999")
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "ambient-parent")
	home := filepath.Join(t.TempDir(), "home ; $(literal)")
	exe := filepath.Join(t.TempDir(), "CLI ; $(literal)")
	observation, registration, err := cursormcp.New(exe, home, e.s, row, e.binding, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer observation.Close()
	if registration.Command != exe || !reflect.DeepEqual(registration.Args, []string{"--home", home, "--store-path", e.path, "--create-if-missing=false", "serve", "--stdio"}) {
		t.Fatalf("literal registration=%+v", registration)
	}
	env := map[string]string{}
	for _, entry := range registration.Env {
		env[entry.Name] = entry.Value
	}
	if env["TMUX"] != row.Identity.Socket+","+fmt.Sprint(row.Identity.ServerPID)+",0" || env["TMUX_PANE"] != row.Identity.PaneID || env["AGENT_DIRECTOR_INSTANCE_ID"] != row.InstanceID || env["AGENT_DIRECTOR_AGENT"] != "cursor" || env["TMUX_TMPDIR"] != "" {
		t.Fatalf("protected route=%v", env)
	}
	scope, err := cursormcp.Parse(env[cursormcp.ScopeEnv])
	if err != nil {
		t.Fatal(err)
	}
	if scope.StorePath != e.s.Path() || scope.StoreID != e.s.StoreID() || scope.Home != home {
		t.Fatalf("scope route=%+v", scope)
	}
	for _, tc := range []struct {
		name, life string
		valid      bool
		want       int64
	}{
		{"first life", "0", true, 0},
		{"later life", "7", true, 7},
		{"missing", "", false, 0},
		{"null", "null", false, 0},
		{"negative", "-1", false, 0},
		{"fraction", "0.5", false, 0},
		{"string", `"0"`, false, 0},
		{"overflow", "9223372036854775808", false, 0},
		{"malformed", "{", false, 0},
	} {
		t.Run("parse/"+tc.name, func(t *testing.T) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(env[cursormcp.ScopeEnv]), &fields); err != nil {
				t.Fatal(err)
			}
			delete(fields, "Life")
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if tc.life != "" {
				raw = append(append(raw[:len(raw)-1], []byte(`,"Life":`+tc.life)...), '}')
			}
			got, err := cursormcp.Parse(string(raw))
			if (err == nil) != tc.valid || tc.valid && got.Life != tc.want {
				t.Fatalf("Parse life=%s got=%d err=%v", tc.life, got.Life, err)
			}
		})
	}
	if _, _, err = scope.Binding(e.s); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"store", "parent", "connection", "token", "life", "runtime", "gateway", "socket"} {
		t.Run(change, func(t *testing.T) {
			stale := scope
			switch change {
			case "store":
				stale.StoreID = "decoy"
			case "parent":
				stale.Parent = "decoy"
			case "connection":
				stale.Connection = "retired"
			case "token":
				stale.Token = "retired"
			case "life":
				stale.Life++
			case "runtime":
				stale.RuntimeStart = "retired"
			case "gateway":
				stale.GatewayStart = "retired"
			case "socket":
				stale.Socket = "/decoy/socket"
			}
			if _, _, err := stale.Binding(e.s); err == nil {
				t.Fatal("stale scope accepted")
			}
		})
	}
}
