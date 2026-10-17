//go:build linux

package cursorhost

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
)

type submissionRecord struct {
	Sequence   uint64 `json:"sequence"`
	Kind       string `json:"kind"`
	Session    string `json:"session_id"`
	Connection string `json:"connection_id"`
	Instance   string `json:"instance_id"`
	Token      string `json:"launch_token"`
	Life       int64  `json:"life_number"`
	Data       struct {
		ID         string    `json:"submission_id"`
		Request    string    `json:"request_id"`
		Text       string    `json:"text"`
		Started    bool      `json:"started"`
		Reason     string    `json:"reason"`
		ConsumedAt time.Time `json:"consumed_at"`
		Control    string    `json:"control"`
		InputID    string    `json:"input_id"`
		InputEpoch *uint64   `json:"input_epoch"`
		Count      uint64    `json:"count"`
	} `json:"data"`
}

// Retired-response records preserve numeric JSON-RPC IDs while submission
// lifecycle records use their printable correlation string.
func (r *submissionRecord) UnmarshalJSON(raw []byte) error {
	type plain submissionRecord
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(envelope["data"], &data); err != nil {
		return err
	}
	if id := bytes.TrimSpace(data["request_id"]); len(id) > 0 && id[0] != '"' && string(id) != "null" {
		data["request_id"], _ = json.Marshal(string(id))
		envelope["data"], _ = json.Marshal(data)
		raw, _ = json.Marshal(envelope)
	}
	return json.Unmarshal(raw, (*plain)(r))
}

type cursorSession struct {
	e                      *requestEnv
	master                 *os.File
	pane                   *observedWire
	done                   chan error
	ended                  bool
	endErr                 error
	waitFailureDiagnostics bool
	diagnosticBarrier      *recordBarrier
	diagnosticMu           sync.Mutex
	diagnosticCaptured     bool
	controllerReturnMu     sync.Mutex
	controllerReturned     bool
	controllerErr          error
}

// observeControllerReturn retains nil as an observed result without blocking cleanup.
func (c *cursorSession) observeControllerReturn(err error) {
	c.controllerReturnMu.Lock()
	c.controllerReturned, c.controllerErr = true, err
	c.controllerReturnMu.Unlock()
}

func (c *cursorSession) controllerResult() (bool, error) {
	c.controllerReturnMu.Lock()
	defer c.controllerReturnMu.Unlock()
	return c.controllerReturned, c.controllerErr
}

func (c *cursorSession) captureFailure(t *testing.T, label string) {
	t.Helper()
	c.diagnosticMu.Lock()
	if c.diagnosticCaptured {
		c.diagnosticMu.Unlock()
		return
	}
	c.diagnosticCaptured = true
	c.diagnosticMu.Unlock()
	seen, err := c.controllerResult()
	t.Logf("rotation failure label=%q before-cleanup observed=%v original error=%v; final Run ended=%v error=%v", label, seen, err, c.ended, c.endErr)
	rotationFailureDiagnostics(t, c, c.diagnosticBarrier, []string{"spawns", "cursor_runtimes", "permission_requests"})
}

// completeInput feeds completed text through Run's real admission and mailbox.
type completeInput struct {
	admit func() bool
	emit  func(terminalInput)
	ready chan struct{}
}

func (i *completeInput) start(in *os.File, out io.Writer, emit func(terminalInput), admit func() bool, choices ...func(rune) (string, string, bool)) func() {
	i.admit, i.emit = admit, emit
	close(i.ready)
	return terminalLoop(in, out, emit, admit, choices...)
}

func (i *completeInput) submit(t *testing.T, text string) {
	t.Helper()
	if !i.admit() {
		t.Fatal("completed input lacked consumption-time authority")
	}
	i.emit(terminalInput{text: text, accepted: true})
}

func startCursorSession(t *testing.T, initial string) *cursorSession {
	t.Helper()
	c := beginCursorSession(t, initial, openTranscript)
	c.wait(t, "confirmed ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
	return c
}

func beginCursorSession(t *testing.T, initial string, opener func(string, store.CursorRuntime) (*transcript, error)) *cursorSession {
	t.Helper()
	return beginCursorSessionWithInput(t, initial, opener, terminalLoop)
}

func beginCursorSessionWithInput(t *testing.T, initial string, opener func(string, store.CursorRuntime) (*transcript, error), starter inputStarter, afterRecoveryReport ...func()) *cursorSession {
	return beginCursorSessionWithOptions(t, Options{InitialPrompt: initial}, cursorfix.Scenario{SessionID: "synthetic-session-1", Requests: []json.RawMessage{}}, opener, starter, afterRecoveryReport...)
}

func beginCursorSessionWithOptions(t *testing.T, options Options, scenario cursorfix.Scenario, opener func(string, store.CursorRuntime) (*transcript, error), starter inputStarter, afterRecoveryReport ...func()) *cursorSession {
	t.Helper()
	e := newRequestEnv(t, "off", "synthetic-session-1")
	e.runtime.Configure(t, scenario)
	master, slave := e.prepareRunPTY(t)
	c := &cursorSession{e: e, master: master, pane: &observedWire{}, done: make(chan error, 1)}
	options.Runtime = e.runtime.Program
	observe := options.beforeControllerCleanup
	options.beforeControllerCleanup = func(err error) {
		c.observeControllerReturn(err)
		if observe != nil {
			observe(err)
		}
	}
	go func() {
		c.done <- runWithInput(options, slave, c.pane, opener, starter, afterRecoveryReport...)
	}()
	t.Cleanup(func() {
		if !c.ended {
			master.Write([]byte("\x15/exit\r"))
			select {
			case <-c.done:
			case <-time.After(5 * time.Second):
				slave.Close()
				t.Error("session cleanup timed out")
			}
		}
	})
	return c
}

func (c *cursorSession) wait(t *testing.T, label string, predicate func() bool) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for !predicate() {
		select {
		case err := <-c.done:
			c.ended = true
			c.endErr = err
			if c.waitFailureDiagnostics {
				t.Logf("rotation wait failure label=%q done reason=%v", label, err)
				c.captureFailure(t, label)
			}
			t.Fatalf("%s: session ended %v; pane=%s", label, err, c.pane.bytes())
		default:
		}
		if time.Now().After(until) {
			if label == "held FIFO completion" || c.waitFailureDiagnostics {
				t.Logf("rotation wait failure label=%q deadline expired; session ended=%v reason=%v", label, c.ended, c.endErr)
				c.captureFailure(t, label)
			}
			pane := c.pane.bytes()
			if len(pane) > 2000 {
				pane = pane[len(pane)-2000:]
			}
			t.Fatalf("%s: frame count=%d pane tail=%s", label, len(c.e.runtime.Frames(t)), pane)
		}
		time.Sleep(time.Millisecond)
	}
}

func (c *cursorSession) keys(t *testing.T, keys string) {
	t.Helper()
	if _, err := c.master.Write([]byte(keys)); err != nil {
		t.Fatal(err)
	}
}

func (c *cursorSession) records(t *testing.T) []submissionRecord {
	t.Helper()
	row, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	path := row.JSONLPath
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var records []submissionRecord
	lines := bytes.Split(raw, []byte("\n"))
	for _, line := range lines[:len(lines)-1] {
		var r submissionRecord
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
	}
	return records
}

func (c *cursorSession) prompts(t *testing.T) []string {
	t.Helper()
	var texts []string
	for _, f := range c.e.runtime.Frames(t) {
		if string(f["method"]) != `"session/prompt"` {
			continue
		}
		var p struct {
			Session string `json:"sessionId"`
			Prompt  []struct {
				Text string `json:"text"`
			} `json:"prompt"`
		}
		if err := json.Unmarshal(f["params"], &p); err != nil {
			t.Fatal(err)
		}
		if p.Session != "synthetic-session-1" || len(p.Prompt) != 1 {
			t.Fatalf("wrong prompt binding: %s", f["params"])
		}
		texts = append(texts, p.Prompt[0].Text)
	}
	return texts
}

func (c *cursorSession) stop(t *testing.T) {
	t.Helper()
	c.keys(t, "\x15/exit\r")
	select {
	case err := <-c.done:
		c.ended = true
		c.endErr = err
		if err != nil {
			t.Fatalf("exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exit timed out")
	}
}

// recordBarrier retains real protected file I/O and gates one record's Sync.
type recordBarrier struct {
	transcriptFile
	kind    string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	current string
	match   func([]byte) bool
	matched bool
}

func (f *recordBarrier) Write(raw []byte) (int, error) {
	var record struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return 0, err
	}
	f.current = record.Kind
	f.matched = f.match == nil || f.match(raw)
	return f.transcriptFile.Write(raw)
}

func (f *recordBarrier) Sync() error {
	if f.current == f.kind && f.matched {
		f.once.Do(func() { close(f.entered); <-f.release })
	}
	return f.transcriptFile.Sync()
}

func newRecordBarrier(t *testing.T, kind string) (*recordBarrier, func(string, store.CursorRuntime) (*transcript, error)) {
	t.Helper()
	return newMatchingRecordBarrier(t, kind, nil)
}

func newMatchingRecordBarrier(t *testing.T, kind string, match func([]byte) bool) (*recordBarrier, func(string, store.CursorRuntime) (*transcript, error)) {
	t.Helper()
	f := &recordBarrier{kind: kind, match: match, entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-f.release:
		default:
			close(f.release)
		}
	})
	return f, func(home string, b store.CursorRuntime) (*transcript, error) {
		tr, err := openTranscript(home, b)
		if err == nil {
			f.transcriptFile = tr.file
			tr.file = f
		}
		return tr, err
	}
}

func (f *recordBarrier) unblock() {
	select {
	case <-f.release:
	default:
		close(f.release)
	}
}

func (c *cursorSession) awaitFailure(t *testing.T, limit time.Duration) error {
	t.Helper()
	select {
	case err := <-c.done:
		c.ended = true
		if err == nil {
			t.Fatal("failed operation reported success")
		}
		return err
	case <-time.After(limit):
		t.Fatal("gateway exceeded original finite bound")
		return nil
	}
}
