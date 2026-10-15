//go:build linux

package cursorhost

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

type pausePTY struct {
	api.PauseTmux
	c            *cursorSession
	t            *testing.T
	release      string
	exitConsumed <-chan struct{}
}

func (p pausePTY) SendKeyPane(socket, pane, key string) error {
	if key != "C-u" {
		p.t.Fatalf("unexpected pause key %s", key)
	}
	_, err := p.c.master.Write([]byte("\x15"))
	return err
}

func (p pausePTY) SendKeyPaneRepeat(socket, pane, key string, count int) error {
	if key != "C-u" || count < 1 {
		p.t.Fatalf("unexpected pause clear %s x%d", key, count)
	}
	_, err := p.c.master.Write(bytes.Repeat([]byte("\x15"), count))
	return err
}

func (p pausePTY) SendKeysPane(socket, pane, text string, enter bool) error {
	if text != "/exit" || !enter {
		p.t.Fatalf("pause sent %q enter=%v", text, enter)
	}
	p.c.keys(p.t, text+"\r")
	p.c.wait(p.t, "pause exit consumed before dispatch resumes", func() bool {
		select {
		case <-p.exitConsumed:
			return true
		default:
			return false
		}
	})
	return os.WriteFile(p.release, nil, 0600)
}

func TestCursorEligiblePublicPauseCancelsAcceptedQueue(t *testing.T) {
	// Serial: real Run/report subprocess with isolated verified pane and PTY.
	exitConsumed := make(chan struct{})
	var exitOnce sync.Once
	starter := func(in *os.File, out io.Writer, emit func(terminalInput), admit func() bool, choices ...func(rune) (string, string, bool)) func() {
		return terminalLoop(in, out, func(input terminalInput) {
			emit(input)
			if input.text == "/exit" {
				exitOnce.Do(func() { close(exitConsumed) })
			}
		}, admit, choices...)
	}
	c := beginCursorSessionWithInput(t, "HOLD", openTranscript, starter)
	c.wait(t, "confirmed ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
	c.wait(t, "active receipt", func() bool { return len(c.prompts(t)) == 1 })
	c.keys(t, "pause-queued-one\rpause-queued-two\r")
	c.wait(t, "accepted queue", func() bool {
		n := 0
		for _, r := range c.records(t) {
			if r.Kind == "submission_accepted" {
				n++
			}
		}
		return n == 3
	})
	before, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("AD_TEST_CURSOR_BARRIER_EVENT", "Stop")
	t.Setenv("AD_TEST_CURSOR_BARRIER_PHASE", "after")
	t.Setenv("AD_TEST_CURSOR_BARRIER_DIR", dir)
	defer os.WriteFile(filepath.Join(dir, "release"), nil, 0600)
	if err := os.WriteFile(c.e.runtime.Release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	c.wait(t, "real committed waiting before queued dispatch", func() bool { _, err := os.Stat(filepath.Join(dir, "entered")); return err == nil })
	row, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != store.StateWaiting {
		t.Fatalf("pause must be legitimately eligible: %s", row.State)
	}
	tc := tmux.New("", tmux.Timeouts{Query: time.Second, Action: time.Second, Create: time.Second, WaitDelay: time.Second})
	send := pausePTY{PauseTmux: tc, c: c, t: t, release: filepath.Join(dir, "release"), exitConsumed: exitConsumed}
	_, err = api.Pause(context.Background(), c.e.s, send, api.PauseEnv{Relay: api.RelayView{Procs: probe.NewProcChecker()}}, 5, api.PauseParams{InstanceID: row.InstanceID})
	if err != nil {
		t.Fatalf("eligible public pause: %v", err)
	}
	select {
	case err := <-c.done:
		c.ended = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("pause success without gateway completion")
	}
	after, err := c.e.s.GetSpawn(row.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != store.StateEnded || after.SessionID != before.SessionID || after.LifeNumber != before.LifeNumber {
		t.Fatalf("pause identity/end: %+v", after)
	}
	raw, err := os.ReadFile(c.e.runtime.Raw)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("pause-queued-")) || bytes.Contains(raw, []byte(`"text":"/exit"`)) {
		t.Fatalf("paused queue reached ACP: %s", raw)
	}
	accepted, cancelled := map[string]bool{}, map[string]int{}
	for _, r := range c.records(t) {
		if r.Kind == "submission_accepted" && r.Data.Text != "HOLD" {
			accepted[r.Data.ID] = true
		}
		if r.Kind == "submission_cancelled" {
			if r.Data.Started {
				t.Fatal("pause fabricated queue execution")
			}
			cancelled[r.Data.ID]++
		}
	}
	if len(accepted) != 2 {
		t.Fatalf("queue identities: %v", accepted)
	}
	for id := range accepted {
		if cancelled[id] != 1 || !bytes.Contains(c.pane.bytes(), []byte("submission "+id+" cancelled before dispatch")) {
			t.Fatalf("pause missing visible protected cancellation %s: %v", id, cancelled)
		}
	}
}

func TestCursorInterruptAndRefusedRotationRetainUnknownOriginHold(t *testing.T) {
	// Serial: real Run owns environment and signal handlers.
	c := startCursorSession(t, "HOLD")
	c.wait(t, "active receipt", func() bool { return len(c.prompts(t)) == 1 })
	c.keys(t, "cancel-before-held-dispatch\r")
	c.wait(t, "queued acceptance", func() bool {
		for _, r := range c.records(t) {
			if r.Kind == "submission_accepted" && r.Data.Text == "cancel-before-held-dispatch" {
				return true
			}
		}
		return false
	})
	b, err := c.e.s.ReadCursorRuntime(c.e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	token := "aaaaaaaa-aaaa-4aaa-aaaa-000000000099"
	gate := store.HookGate{Event: "PermissionRequest", ParentPID: b.GatewayPID, ParentStart: b.GatewayStart, SessionID: b.SessionID, Cursor: &b}
	if _, a, err := c.e.s.InsertRelayRequest(b.InstanceID, gate, store.RelayRequest{RequestToken: token, ToolName: "Unknown", ToolInput: "{}", SettledAt: time.Now().Add(time.Minute)}, 0, store.DefaultLockWait); err != nil || !a.Applied {
		t.Fatalf("durable unknown hold: %+v %v", a, err)
	}
	c.keys(t, "\x03")
	if err := os.WriteFile(c.e.runtime.Release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	c.wait(t, "control disposed queue and recovered", func() bool { return bytes.Contains(c.pane.bytes(), []byte("submit a new prompt to continue")) })
	c.keys(t, "/clear\r/new\rmust-stay-refused\r^")
	c.wait(t, "refused input consumed", func() bool {
		n := 0
		for _, r := range c.records(t) {
			if r.Kind == "submission_refused" {
				n++
			}
		}
		return n == 3 && bytes.Contains(c.pane.bytes(), []byte("\r\x1b[2K> ^"))
	})
	request, err := c.e.s.GetPermissionRequest(b.InstanceID, token)
	if err != nil {
		t.Fatal(err)
	}
	if request.ProvenGone() || request.MainTurnEndEligible || request.AgentID != "" {
		t.Fatalf("interruption/unsupported rotation fabricated unknown proof: %+v", request)
	}
	row, err := c.e.s.GetSpawn(b.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if row.SessionID != b.SessionID || row.LifeNumber != b.Life {
		t.Fatal("refused rotation changed conversation/life")
	}
	if got := c.prompts(t); len(got) != 1 || got[0] != "HOLD" {
		t.Fatalf("hold released dispatch: %q", got)
	}
	cancelled := 0
	for _, r := range c.records(t) {
		if r.Kind == "submission_cancelled" {
			cancelled++
		}
	}
	if cancelled != 1 {
		t.Fatalf("queued cancellation count %d", cancelled)
	}
	c.stop(t)
}
