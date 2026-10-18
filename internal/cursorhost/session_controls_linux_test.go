//go:build linux

package cursorhost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func TestCursorFIFOExactTextAndLifecycle(t *testing.T) {
	// Serial: verified runtime fixture sets HOME/PATH and Run owns signal handlers.
	c := startCursorSession(t, "HOLD")
	for _, frame := range c.e.runtime.Frames(t) {
		if string(frame["method"]) == `"session/new"` {
			var params struct {
				MCP []any `json:"mcpServers"`
			}
			if err := json.Unmarshal(frame["params"], &params); err != nil || params.MCP == nil || len(params.MCP) != 0 {
				t.Fatalf("help-off registration=%s %v", frame["params"], err)
			}
		}
	}
	if rotationRecordCount(c, t, "guidance_configured") != 0 || rotationRecordCount(c, t, "guidance_initialized") != 0 {
		t.Fatal("help-off session recorded guidance")
	}
	c.wait(t, "initial receipt", func() bool { return len(c.prompts(t)) == 1 })
	texts := []string{"HOLD", "第一\t🦋\nsecond café", "paste\tone\nline two", "final 🐝"}
	c.keys(t, texts[1]+"\r\x1b[200~paste\tone\rline two\x1b[201~\r"+texts[3]+"\r")
	c.wait(t, "three queued acceptances", func() bool {
		n := 0
		for _, r := range c.records(t) {
			if r.Kind == "submission_accepted" {
				n++
			}
		}
		return n == 4
	})
	if got := c.prompts(t); !reflect.DeepEqual(got, texts[:1]) {
		t.Fatalf("overlap before initial outcome: %q", got)
	}
	var admitted []submissionRecord
	for _, r := range c.records(t) {
		if r.Kind == "submission_accepted" {
			admitted = append(admitted, r)
		}
	}
	defer func() {
		if t.Failed() {
			t.Logf("FIFO accepted before release=%+v records=%+v peer=%+v", admitted, c.records(t), c.e.runtime.Frames(t))
		}
	}()
	if err := os.WriteFile(c.e.runtime.Release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, accepted := range admitted {
		c.wait(t, "FIFO outcome for "+accepted.Data.ID+" text="+fmt.Sprintf("%q", accepted.Data.Text), func() bool {
			request := ""
			for _, r := range c.records(t) {
				if r.Data.ID != accepted.Data.ID {
					continue
				}
				if r.Kind == "submission_started" {
					request = r.Data.Request
				}
				if r.Kind == "turn_outcome" && request != "" && r.Data.Request == request {
					return true
				}
			}
			return false
		})
	}
	c.stop(t)
	if got := c.prompts(t); !reflect.DeepEqual(got, texts) {
		t.Fatalf("FIFO exact text: %q want %q", got, texts)
	}
	accepted := map[string]string{}
	started := map[string]string{}
	outcomes := map[string]bool{}
	var order []string
	row, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range c.records(t) {
		if r.Sequence != uint64(i+1) {
			t.Fatalf("sequence %d at %d", r.Sequence, i)
		}
		if strings.HasPrefix(r.Kind, "submission_") || r.Kind == "turn_outcome" || r.Kind == "update" {
			if r.Session != "synthetic-session-1" || r.Connection == "" || r.Instance != row.InstanceID || r.Token != row.Identity.Token || r.Life != row.LifeNumber {
				t.Fatalf("lost accepted binding: %+v", r)
			}
		}
		switch r.Kind {
		case "submission_accepted":
			if _, ok := accepted[r.Data.ID]; ok {
				t.Fatal("duplicate acceptance")
			}
			accepted[r.Data.ID] = r.Data.Text
			order = append(order, r.Data.Text)
		case "submission_started":
			if _, ok := accepted[r.Data.ID]; !ok || r.Data.Request == "" || started[r.Data.ID] != "" {
				t.Fatalf("uncorrelated start %+v", r)
			}
			started[r.Data.ID] = r.Data.Request
		case "update", "turn_outcome":
			if started[r.Data.ID] != r.Data.Request || r.Data.Request == "" {
				t.Fatalf("uncorrelated output %+v", r)
			}
			if r.Kind == "turn_outcome" {
				if outcomes[r.Data.ID] {
					t.Fatalf("duplicate outcome %+v", r)
				}
				outcomes[r.Data.ID] = true
			}
		}
	}
	if !reflect.DeepEqual(order, texts) || len(started) != 4 || len(outcomes) != 4 {
		t.Fatalf("incomplete lifecycle order=%q starts=%v outcomes=%v", order, started, outcomes)
	}
}

func TestCursorControlsRetireQueuedSubmissions(t *testing.T) {
	// Serial: verified runtime fixture sets HOME/PATH and Run owns signal handlers.
	for _, control := range []string{"interrupt", "exit"} {
		t.Run(control, func(t *testing.T) {
			c := startCursorSession(t, "HOLD")
			c.wait(t, "initial receipt", func() bool { return len(c.prompts(t)) == 1 })
			c.keys(t, "one\rtwo\rthree\r")
			c.wait(t, "queued acceptance", func() bool {
				n := 0
				for _, r := range c.records(t) {
					if r.Kind == "submission_accepted" {
						n++
					}
				}
				return n == 4
			})
			if control == "exit" {
				c.stop(t)
			} else {
				c.keys(t, "\x03")
				c.wait(t, "all queued cancellations", func() bool {
					n := 0
					for _, r := range c.records(t) {
						if r.Kind == "submission_cancelled" {
							n++
						}
					}
					return n == 3
				})
			}
			accepted := map[string]string{}
			cancelled := map[string]int{}
			for _, r := range c.records(t) {
				if r.Kind == "submission_accepted" {
					accepted[r.Data.ID] = r.Data.Text
				}
				if r.Kind == "submission_cancelled" {
					if r.Data.Started {
						t.Fatal("queued cancellation fabricated execution")
					}
					cancelled[r.Data.ID]++
				}
			}
			for id, text := range accepted {
				if text == "one" || text == "two" || text == "three" {
					if cancelled[id] != 1 {
						t.Fatalf("submission %s cancellation count %d", id, cancelled[id])
					}
					c.wait(t, "visible cancellation for "+id, func() bool {
						return bytes.Contains(c.pane.bytes(), []byte(fmt.Sprintf("submission %s cancelled before dispatch", id)))
					})
				}
			}
			want := []string{"HOLD"}
			if control == "interrupt" {
				if err := os.WriteFile(c.e.runtime.Release, nil, 0600); err != nil {
					t.Fatal(err)
				}
				c.wait(t, "active completes after cancellation", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor turn complete")) })
				c.keys(t, "later explicit\r")
				c.wait(t, "explicit later receipt", func() bool { return len(c.prompts(t)) == 2 })
				c.stop(t)
				want = append(want, "later explicit")
			}
			if got := c.prompts(t); !reflect.DeepEqual(got, want) {
				t.Fatalf("cancelled queue replayed: %q", got)
			}
		})
	}
}

func TestCursorControlBypassesSaturatedInputAndPendingReport(t *testing.T) {
	// Serial: report subprocess barriers and Run use process-wide environment.
	c := startCursorSession(t, "")
	dir := t.TempDir()
	t.Setenv("AD_TEST_CURSOR_BARRIER_EVENT", "UserPromptSubmit")
	t.Setenv("AD_TEST_CURSOR_BARRIER_PHASE", "before")
	t.Setenv("AD_TEST_CURSOR_BARRIER_DIR", dir)
	t.Cleanup(func() { os.WriteFile(filepath.Join(dir, "release"), nil, 0600) })
	c.keys(t, "before-wire\r")
	c.wait(t, "pending actual submission report", func() bool { _, err := os.Stat(filepath.Join(dir, "entered")); return err == nil })
	var keys strings.Builder
	for i := 0; i < 32; i++ {
		fmt.Fprintf(&keys, "queued-%02d\r", i)
	}
	keys.WriteString("~")
	c.keys(t, keys.String())
	c.wait(t, "saturated ordinary input consumed", func() bool { return bytes.Contains(c.pane.bytes(), []byte("\r\x1b[2K> ~")) })
	c.keys(t, "\x03^")
	c.wait(t, "control consumed beyond full mailbox", func() bool { return bytes.Contains(c.pane.bytes(), []byte("\r\x1b[2K> ^")) })
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	c.wait(t, "capacity refusal and all cancellations", func() bool {
		n := 0
		refused := false
		for _, r := range c.records(t) {
			if r.Kind == "submission_cancelled" {
				n++
			}
			if r.Kind == "submission_refused" {
				refused = true
			}
		}
		return n == 32 && refused && bytes.Contains(c.pane.bytes(), []byte("submit a new prompt"))
	})
	raw, err := os.ReadFile(c.e.runtime.Raw)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("before-wire")) || bytes.Contains(raw, []byte("queued-")) {
		t.Fatalf("retired prompt bytes reached peer: %s", raw)
	}
	if len(c.prompts(t)) != 0 {
		t.Fatal("control allowed an unwritten prompt")
	}
	c.keys(t, "\x15later-explicit\r")
	c.wait(t, "later explicit turn outcome", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Synthetic reply: later-explicit")) })
	c.stop(t)
	if got := c.prompts(t); !reflect.DeepEqual(got, []string{"later-explicit"}) {
		t.Fatalf("retired work replayed: %q", got)
	}
}

func TestCursorQueuedPromptWaitsForEveryDurableHold(t *testing.T) {
	// Serial: verified runtime and reporter set HOME/PATH and signal handlers.
	c := startCursorSession(t, "HOLD")
	c.wait(t, "active receipt", func() bool { return len(c.prompts(t)) == 1 })
	c.keys(t, "queued-after-holds\r")
	c.wait(t, "queued acceptance", func() bool {
		n := 0
		for _, r := range c.records(t) {
			if r.Kind == "submission_accepted" {
				n++
			}
		}
		return n == 2
	})
	b, err := c.e.s.ReadCursorRuntime(c.e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	gate := store.HookGate{Event: "PermissionRequest", ParentPID: b.GatewayPID, ParentStart: b.GatewayStart, SessionID: b.SessionID, Cursor: &b}
	for i, tool := range []string{"Bash", "Plan"} {
		token := fmt.Sprintf("aaaaaaaa-aaaa-4aaa-aaaa-%012d", i)
		if _, a, err := c.e.s.InsertRelayRequest(b.InstanceID, gate, store.RelayRequest{RequestToken: token, ToolName: tool, ToolInput: "{}", SettledAt: time.Now().Add(time.Minute)}, 0, store.DefaultLockWait); err != nil || !a.Applied {
			t.Fatalf("hold: %+v %v", a, err)
		}
	}
	if err := os.WriteFile(c.e.runtime.Release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	c.wait(t, "normal outcome", func() bool {
		for _, r := range c.records(t) {
			if r.Kind == "turn_outcome" {
				return true
			}
		}
		return false
	})
	// These are independent durable holds with no in-memory request entries.
	// A normal outcome and closing an unknown-origin request prove neither gone.
	c.wait(t, "held Stop report verified", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor turn complete")) })
	c.e.exec(t, `UPDATE permission_requests SET closed_at=? WHERE tool_name='Bash'`, time.Now().UnixMilli())
	time.Sleep(120 * time.Millisecond)
	if got := c.prompts(t); len(got) != 1 {
		t.Fatalf("completion/closed request released another hold: %q", got)
	}
	c.e.exec(t, `UPDATE permission_requests SET proven_gone_at=? WHERE tool_name='Bash'`, time.Now().UnixMilli())
	time.Sleep(120 * time.Millisecond)
	if got := c.prompts(t); len(got) != 1 {
		t.Fatalf("one proof released independent hold: %q", got)
	}
	c.e.exec(t, `UPDATE permission_requests SET proven_gone_at=? WHERE tool_name='Plan'`, time.Now().UnixMilli())
	c.wait(t, "all independent holds released", func() bool { return len(c.prompts(t)) == 2 })
	c.stop(t)
	if got := c.prompts(t); !reflect.DeepEqual(got, []string{"HOLD", "queued-after-holds"}) {
		t.Fatalf("held queue %q", got)
	}
}

func TestCursorQueuedPromptNeverMigratesAfterAuthorityLoss(t *testing.T) {
	// Serial: verified runtime fixture sets HOME/PATH and signal handlers.
	for _, loss := range []string{"launch", "conversation", "connection", "runtime crash"} {
		t.Run(loss, func(t *testing.T) {
			c := startCursorSession(t, "HOLD")
			c.wait(t, "active receipt", func() bool { return len(c.prompts(t)) == 1 })
			c.keys(t, "must-never-migrate\r")
			c.wait(t, "accepted queue", func() bool {
				for _, r := range c.records(t) {
					if r.Kind == "submission_accepted" && r.Data.Text == "must-never-migrate" {
						return true
					}
				}
				return false
			})
			switch loss {
			case "launch":
				c.e.exec(t, `UPDATE spawns SET launch_token='2222222222222222'`)
			case "conversation":
				c.e.exec(t, `UPDATE spawns SET session_id='replacement'`)
			case "connection":
				c.e.exec(t, `UPDATE cursor_runtimes SET bound_connection_id='replacement'`)
			case "runtime crash":
				var pid int
				raw, err := os.ReadFile(c.e.runtime.PID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := fmt.Sscan(string(raw), &pid); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(c.e.runtime.Release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-c.done:
				c.ended = true
				if err == nil {
					t.Fatal("lost authority reported success")
				}
				t.Logf("honest failure: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("authority loss did not stop session")
			}
			raw, err := os.ReadFile(c.e.runtime.Raw)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte("must-never-migrate")) {
				t.Fatalf("accepted work migrated: %s", raw)
			}
			cancelled := 0
			for _, r := range c.records(t) {
				if r.Kind == "submission_cancelled" {
					cancelled++
				}
				if r.Kind == "session_end" {
					t.Fatal("authority loss fabricated intentional ending")
				}
			}
			if cancelled != 1 {
				t.Fatalf("queued disposition count %d", cancelled)
			}
		})
	}
}

func TestCursorProtocolQueuedUnwrittenPromptRetiresBeforeBytes(t *testing.T) {
	wire := &observedWire{}
	p := newProtocol(wire, strings.NewReader(""))
	defer p.close()
	unblock := blockProtocol(t, p)
	var retired atomic.Bool
	done := make(chan error, 1)
	if _, err := p.requestGuarded("session/prompt", map[string]string{"text": "must-never-reach-wire"}, func() error {
		if retired.Load() {
			return errPromptRetired
		}
		return nil
	}, func(err error) { done <- err }); err != nil {
		t.Fatal(err)
	}
	retired.Store(true)
	if err := p.notify("session/cancel", map[string]string{"sessionId": "synthetic"}); err != nil {
		t.Fatal(err)
	}
	unblock()
	select {
	case err := <-done:
		if err != errPromptRetired {
			t.Fatalf("prewrite retirement became uncertain delivery: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued prompt not retired")
	}
	if err := completedWrite(t, p); err != nil {
		t.Fatal(err)
	}
	raw := wire.bytes()
	if bytes.Contains(raw, []byte("session/prompt")) || bytes.Contains(raw, []byte("must-never-reach-wire")) || !bytes.Contains(raw, []byte("session/cancel")) {
		t.Fatalf("raw protocol bytes: %s", raw)
	}
	if bytes.Count(raw, []byte{'\n'}) != 1 {
		t.Fatalf("unexpected frames: %s", raw)
	}
}

func TestCursorPartialPromptWriteIsUncertainAndNeverReplayed(t *testing.T) {
	wire := &observedWire{partial: true}
	p := newProtocol(wire, strings.NewReader(""))
	defer p.close()
	if _, err := p.request("session/prompt", map[string]string{"text": "uncertain"}); err != nil {
		t.Fatal(err)
	}
	if err := completedWrite(t, p); err == nil || !strings.Contains(err.Error(), "uncertain") {
		t.Fatalf("partial write not uncertain: %v", err)
	}
	before := wire.bytes()
	if len(before) != 1 {
		t.Fatalf("partial writer evidence %q", before)
	}
	if err := p.notify("session/prompt", map[string]string{"text": "uncertain"}); err == nil {
		t.Fatal("failed transport accepted replay")
	}
	if !bytes.Equal(before, wire.bytes()) {
		t.Fatal("uncertain frame replayed")
	}
}

func TestCursorPromptRetiredAtFinalWriteGuardRemainsKnownUnwritten(t *testing.T) {
	wire := &observedWire{}
	p := newProtocol(wire, strings.NewReader(""))
	defer p.close()
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	var retired atomic.Bool
	done := make(chan error, 1)
	guard := func() error {
		if calls.Add(1) == 2 {
			close(entered)
			<-release
		}
		if retired.Load() {
			return errPromptRetired
		}
		return nil
	}
	if _, err := p.requestGuarded("session/prompt", map[string]string{"text": "known-unwritten"}, guard, func(err error) { done <- err }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("final write guard not reached")
	}
	retired.Store(true)
	close(release)
	select {
	case err := <-done:
		t.Logf("final guard retired before Write: calls=%d raw_peer_bytes=%q", calls.Load(), wire.bytes())
		if raw := wire.bytes(); len(raw) != 0 {
			t.Fatalf("retired prompt emitted raw bytes: %s", raw)
		}
		if err != errPromptRetired {
			t.Errorf("zero-byte prewrite retirement must remain known unwritten, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("final guard did not retire prompt")
	}
	if err := p.notify("session/cancel", map[string]string{"sessionId": "synthetic"}); err != nil {
		t.Errorf("known unwritten retirement poisoned transport: %v", err)
	}
	if err := completedWrite(t, p); err != nil {
		t.Fatalf("healthy cancel transport: %v", err)
	}
	if raw := wire.bytes(); !bytes.Contains(raw, []byte("session/cancel")) || bytes.Contains(raw, []byte("known-unwritten")) {
		t.Fatalf("post-retirement raw bytes: %s", raw)
	}
}

func TestCursorBlockedPaneRenderingCannotDelayCancellation(t *testing.T) {
	// Serial: verified runtime and reporter use process-wide environment.
	c := startCursorSession(t, "HOLD")
	c.wait(t, "active receipt", func() bool { return len(c.prompts(t)) == 1 })
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	c.pane.mu.Lock()
	c.pane.observe = func(raw []byte) {
		if bytes.Contains(raw, []byte("blocked-pane")) {
			once.Do(func() { close(entered); <-release })
		}
	}
	c.pane.mu.Unlock()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	c.keys(t, "blocked-pane")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("pane boundary did not block")
	}
	// More than the finite rendering buffer's entries; input remains consumable.
	c.keys(t, strings.Repeat("x", 100)+"\rqueued-after-render-pressure\r\x03")
	until := time.Now().Add(time.Second)
	for {
		cancelled := 0
		for _, r := range c.records(t) {
			if r.Kind == "submission_cancelled" {
				cancelled++
			}
		}
		if cancelled == 2 {
			break
		}
		if time.Now().After(until) {
			close(release)
			t.Fatal("blocked pane delayed control past queued work")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	c.keys(t, "^")
	c.wait(t, "visible rendering loss advice", func() bool {
		return bytes.Contains(c.pane.bytes(), []byte("Cursor pane output omitted under pressure; inspect the protected transcript"))
	})
	view, err := api.Get(c.e.s, api.RelayView{}, c.e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(view.JSONLPath)
	if err != nil {
		t.Fatal(err)
	}
	accepted, cancelled := map[string]bool{}, map[string]int{}
	lines := bytes.Split(raw, []byte("\n"))
	for n, line := range lines[:len(lines)-1] {
		var r submissionRecord
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatal(err)
		}
		if r.Sequence != uint64(n+1) {
			t.Fatal("protected inspection found a missing record")
		}
		if r.Kind == "submission_accepted" && r.Data.Text != "HOLD" {
			accepted[r.Data.ID] = true
		}
		if r.Kind == "submission_cancelled" && !r.Data.Started {
			cancelled[r.Data.ID]++
		}
		if accepted[r.Data.ID] && (r.Kind == "submission_started" || r.Kind == "turn_outcome") {
			t.Fatal("omitted pane output fabricated queued execution")
		}
	}
	if len(accepted) != 2 || len(cancelled) != 2 {
		t.Fatalf("protected inspection lost affected outcomes: accepted=%v cancelled=%v", accepted, cancelled)
	}
	for id := range accepted {
		if cancelled[id] != 1 {
			t.Fatalf("protected inspection lacks cancellation for %s", id)
		}
	}
	if got := c.prompts(t); !reflect.DeepEqual(got, []string{"HOLD"}) {
		t.Fatalf("pane pressure dispatched cancelled queue: %q", got)
	}
	c.stop(t)
}
