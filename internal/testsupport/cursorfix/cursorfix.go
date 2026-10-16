// Package cursorfix provides a synthetic ACP runtime with an observable request trace.
package cursorfix

import (
	"embed"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

//go:embed runtime.js
var scripts embed.FS

// Runtime records received frames without accessing Cursor or authentication.
type Runtime struct{ Program, Trace, PID, Release, WorkerPID, WorkerRelease, Scenario, Raw, Observer, Sent, Paused, NewRelease string }

func New(t testing.TB) Runtime { return NewWithWorker(t, false) }

// NewWithWorker uses a separate terminal session for the independent worker with neither ACP pipe.
func NewWithWorker(t testing.TB, worker bool) Runtime {
	t.Helper()
	dir := t.TempDir()
	r := Runtime{Program: filepath.Join(dir, "index.js"), Trace: filepath.Join(dir, "trace.jsonl"), PID: filepath.Join(dir, "pid"), Release: filepath.Join(dir, "release"), WorkerPID: filepath.Join(dir, "worker-pid"), WorkerRelease: filepath.Join(dir, "worker-release"), Scenario: filepath.Join(dir, "scenario"), Raw: filepath.Join(dir, "raw"), Observer: filepath.Join(dir, "observer"), Sent: filepath.Join(dir, "sent.jsonl"), Paused: filepath.Join(dir, "paused"), NewRelease: filepath.Join(dir, "new-release")}
	raw, err := scripts.ReadFile("runtime.js")
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]any{"trace": r.Trace, "pid": r.PID, "release": r.Release, "worker": worker, "workerPID": r.WorkerPID, "workerRelease": r.WorkerRelease, "scenario": r.Scenario, "raw": r.Raw, "observer": r.Observer, "sent": r.Sent, "paused": r.Paused, "newRelease": r.NewRelease})
	raw = append([]byte("#!/usr/local/bin/node\nconst SETTINGS = "+string(settings)+";\n"), raw...)
	if err = os.WriteFile(r.Program, raw, 0700); err != nil {
		t.Fatal(err)
	}
	return r
}
func (r Runtime) Frames(t testing.TB) []map[string]json.RawMessage {
	return readFrames(t, r.Trace)
}

// SentFrames records scripted peer output; admission separately proves what the gateway received.
func (r Runtime) SentFrames(t testing.TB) []map[string]json.RawMessage {
	return readFrames(t, r.Sent)
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
	BeforeRequests        []json.RawMessage `json:"beforeRequests,omitempty"`
	NewReplies            []json.RawMessage `json:"newReplies,omitempty"`
	WaitForNew            bool              `json:"waitForNew,omitempty"`
	AfterNew              []json.RawMessage `json:"afterNew,omitempty"`
	SessionID             string            `json:"sessionId"`
	Requests              []json.RawMessage `json:"requests"`
	PauseAfterRequests    bool              `json:"pauseAfterRequests,omitempty"`
	AfterAnswers          []json.RawMessage `json:"afterAnswers,omitempty"`
	WaitForRelease        bool              `json:"waitForRelease,omitempty"`
	CompleteBeforeAnswers bool              `json:"completeBeforeAnswers,omitempty"`
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
