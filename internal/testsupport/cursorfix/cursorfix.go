// Package cursorfix provides a synthetic ACP runtime with an observable request trace.
package cursorfix

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

//go:embed runtime.js trace_lifecycle.c
var scripts embed.FS

// TraceLifecycle builds a Linux tracee for real fork/vfork and pthread exec
// schedules that the Go subprocess helper cannot control independently of its runtime.
// Process lifecycle tests must reuse this fixture; callers own termination and pipe cleanup.
func TraceLifecycle(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	source, err := scripts.ReadFile("trace_lifecycle.c")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "trace_lifecycle.c")
	if err = os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(dir, "trace-lifecycle")
	if output, err := exec.Command("cc", "-Wall", "-Wextra", "-Werror", "-pthread", "-o", program, path).CombinedOutput(); err != nil {
		t.Fatalf("build tracee fixture: %v: %s", err, output)
	}
	return program
}

// Runtime records received frames without accessing Cursor or authentication.
type Runtime struct{ Program, Trace, PID, Release, WorkerPID, WorkerRelease, Scenario, Raw, Observer, Sent, Paused, NewRelease, MCPTrace, MCPRelease, MCPControl, Events string }

// Selection reads only designated synthetic selectors, preserving absent versus empty.
func (r Runtime) Selection(t testing.TB) map[string]*string {
	t.Helper()
	raw, err := os.ReadFile(r.PID + ".selection.json")
	if err != nil {
		t.Fatal(err)
	}
	var selected map[string]*string
	if err := json.Unmarshal(raw, &selected); err != nil {
		t.Fatal(err)
	}
	return selected
}

func New(t testing.TB) Runtime { return NewWithWorker(t, false) }

// WorkerOptions varies only the descendant's role and standard descriptors.
// Omitted options retain the independent worker with neither ACP pipe.
type WorkerOptions struct {
	Args  []string
	Stdio []string
}

// NewWithWorker uses a separate terminal session for the synthetic worker.
func NewWithWorker(t testing.TB, worker bool, options ...WorkerOptions) Runtime {
	t.Helper()
	dir := t.TempDir()
	r := Runtime{Program: filepath.Join(dir, "index.js"), Trace: filepath.Join(dir, "trace.jsonl"), PID: filepath.Join(dir, "pid"), Release: filepath.Join(dir, "release"), WorkerPID: filepath.Join(dir, "worker-pid"), WorkerRelease: filepath.Join(dir, "worker-release"), Scenario: filepath.Join(dir, "scenario"), Raw: filepath.Join(dir, "raw"), Observer: filepath.Join(dir, "observer"), Sent: filepath.Join(dir, "sent.jsonl"), Paused: filepath.Join(dir, "paused"), NewRelease: filepath.Join(dir, "new-release")}
	r.MCPTrace, r.MCPRelease = filepath.Join(dir, "mcp.jsonl"), filepath.Join(dir, "mcp-release")
	r.MCPControl = filepath.Join(dir, "mcp-control.json")
	r.Events = filepath.Join(dir, "events.json")
	raw, err := scripts.ReadFile("runtime.js")
	if err != nil {
		t.Fatal(err)
	}
	raw = append([]byte("#!/usr/local/bin/node\nconst SETTINGS = JSON.parse(process.env.AD_CURSOR_FIX_SETTINGS || "+fmt.Sprintf("%q", r.settings(worker, options...))+");\n"), raw...)
	if err = os.WriteFile(r.Program, raw, 0700); err != nil {
		t.Fatal(err)
	}
	return r
}

// Settings selects this runtime's isolated files when sharing a stable executable.
func (r Runtime) Settings() string { return r.settings(false) }

func (r Runtime) settings(worker bool, options ...WorkerOptions) string {
	fields := map[string]any{"trace": r.Trace, "pid": r.PID, "release": r.Release, "worker": worker, "workerPID": r.WorkerPID, "workerRelease": r.WorkerRelease, "scenario": r.Scenario, "raw": r.Raw, "observer": r.Observer, "sent": r.Sent, "paused": r.Paused, "newRelease": r.NewRelease, "mcpTrace": r.MCPTrace, "mcpRelease": r.MCPRelease, "mcpControl": r.MCPControl, "events": r.Events}
	if len(options) > 0 {
		if len(options[0].Args) > 0 {
			fields["workerArgs"] = options[0].Args
		}
		if len(options[0].Stdio) > 0 {
			fields["workerStdio"] = options[0].Stdio
		}
	}
	settings, _ := json.Marshal(fields)
	return string(settings)
}
func (r Runtime) Frames(t testing.TB) []map[string]json.RawMessage {
	return readFrames(t, r.Trace)
}

// SentFrames records scripted peer output; admission separately proves what the gateway received.
func (r Runtime) SentFrames(t testing.TB) []map[string]json.RawMessage {
	return readFrames(t, r.Sent)
}

// Emit sends an explicit synthetic provider event batch through the real ACP
// pipe. Callers wait for its observation before replacing the command file.
// raw retains whitespace/escaping and also permits malformed/partial IPC tests.
func (r Runtime) Emit(t testing.TB, id, raw string) {
	t.Helper()
	command, err := json.Marshal(map[string]string{"id": id, "raw": raw})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.Events+".tmp", command, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(r.Events+".tmp", r.Events); err != nil {
		t.Fatal(err)
	}
}

// MCPCommand controls the retained configured process independently of ACP input.
// Callers wait for the matching control_result before issuing the next command.
func (r Runtime) MCPCommand(t testing.TB, id, operation string, calls ...map[string]any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"id": id, "operation": operation, "calls": calls})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.MCPControl+".tmp", raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(r.MCPControl+".tmp", r.MCPControl); err != nil {
		t.Fatal(err)
	}
}

// MCPEvents returns complete trace records from the actual configured process.
func (r Runtime) MCPEvents(t testing.TB) []map[string]json.RawMessage {
	return readFrames(t, r.MCPTrace)
}

func readFrames(t testing.TB, path string) []map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var frames []map[string]json.RawMessage
	for _, line := range splitLines(raw) {
		var f map[string]json.RawMessage
		if err := json.Unmarshal(line, &f); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, f)
	}
	return frames
}
func splitLines(raw []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range raw {
		if b == '\n' {
			if i > start {
				lines = append(lines, raw[start:i])
			}
			start = i + 1
		}
	}
	return lines
}

// Scenario scripts synthetic peer traffic; captured envelopes retain separate provenance.
type Scenario struct {
	MCPWaitAttempt             int               `json:"mcpWaitAttempt,omitempty"`
	MCPOmitNotification        bool              `json:"mcpOmitNotification,omitempty"`
	MCPExpectedInstructions    string            `json:"mcpExpectedInstructions,omitempty"`
	MCPCalls                   []map[string]any  `json:"mcpCalls,omitempty"`
	MCPAmbientEnv              map[string]string `json:"mcpAmbientEnv,omitempty"`
	RecordArtifacts            bool              `json:"recordArtifacts,omitempty"`
	InitializeReply            json.RawMessage   `json:"initializeReply,omitempty"`
	FirstNewReply              json.RawMessage   `json:"firstNewReply,omitempty"`
	PromptReply                json.RawMessage   `json:"promptReply,omitempty"`
	Modes                      json.RawMessage   `json:"modes,omitempty"`
	ModeReply                  json.RawMessage   `json:"modeReply,omitempty"`
	BeforeModeReply            []json.RawMessage `json:"beforeModeReply,omitempty"`
	AfterModeReply             []json.RawMessage `json:"afterModeReply,omitempty"`
	WaitForMode                bool              `json:"waitForMode,omitempty"`
	NativeConfigRoot           string            `json:"nativeConfigRoot,omitempty"`
	LoadSupport                bool              `json:"loadSupport,omitempty"`
	BeforeLoadReply            []json.RawMessage `json:"beforeLoadReply,omitempty"`
	LoadReply                  json.RawMessage   `json:"loadReply,omitempty"`
	WaitForLoad                bool              `json:"waitForLoad,omitempty"`
	AfterLoadReply             []json.RawMessage `json:"afterLoadReply,omitempty"`
	BeforeRequests             []json.RawMessage `json:"beforeRequests,omitempty"`
	PromptHook                 map[string]any    `json:"promptHook,omitempty"`
	NewReplies                 []json.RawMessage `json:"newReplies,omitempty"`
	WaitForNew                 bool              `json:"waitForNew,omitempty"`
	AfterNew                   []json.RawMessage `json:"afterNew,omitempty"`
	SessionID                  string            `json:"sessionId"`
	Requests                   []json.RawMessage `json:"requests"`
	PauseAfterRequests         bool              `json:"pauseAfterRequests,omitempty"`
	AfterAnswers               []json.RawMessage `json:"afterAnswers,omitempty"`
	WaitForRelease             bool              `json:"waitForRelease,omitempty"`
	WaitForAfterAnswersRelease bool              `json:"waitForAfterAnswersRelease,omitempty"`
	CompleteBeforeAnswers      bool              `json:"completeBeforeAnswers,omitempty"`
}

func (r Runtime) Configure(t testing.TB, scenario Scenario) {
	t.Helper()
	raw, err := json.Marshal(scenario)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(r.Scenario, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
