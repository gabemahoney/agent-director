//go:build linux

package cursorhost

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
)

func postBootstrapCursorSession(t *testing.T, initial string) *cursorSession {
	t.Helper()
	c := postBootstrapCursorSessionWithInput(t, initial, openTranscript, terminalLoop)
	c.wait(t, "confirmed ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
	return c
}

// Rotation semantics start after real bootstrap; original caller assertions keep their bounds.
func postBootstrapCursorSessionWithInput(t *testing.T, initial string, opener func(string, store.CursorRuntime) (*transcript, error), starter inputStarter) *cursorSession {
	t.Helper()
	e := newRequestEnv(t, "off", "synthetic-session-1")
	e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", Requests: []json.RawMessage{}})
	master, slave := e.prepareRunPTY(t)
	row, err := e.s.GetSpawn(e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	marker := []byte("Cursor ready")
	var tail []byte
	seen := false
	c := &cursorSession{e: e, master: master, pane: &observedWire{}, done: make(chan error, 1)}
	c.pane.observe = func(raw []byte) {
		if seen {
			return
		}
		tail = append(tail, raw...)
		if bytes.Contains(tail, marker) {
			seen = true
			close(ready)
		} else if len(tail) >= len(marker) {
			tail = tail[len(tail)-len(marker)+1:]
		}
	}
	var pending submissionRecord
	var latch *recordBarrier
	var accepted <-chan struct{}
	if initial != "" {
		latch, _ = newMatchingRecordBarrier(t, "submission_accepted", func(raw []byte) bool {
			var r submissionRecord
			if json.Unmarshal(raw, &r) != nil || r.Kind != "submission_accepted" || r.Data.ID != "initial" || r.Data.Text != initial {
				return false
			}
			pending = r
			return true
		})
		accepted = latch.entered
		defer latch.unblock()
	}
	type openedTranscript struct {
		binding store.CursorRuntime
		path    string
	}
	opened := make(chan openedTranscript, 1)
	open := func(home string, b store.CursorRuntime) (*transcript, error) {
		tr, err := opener(home, b)
		if err == nil {
			if latch != nil {
				latch.transcriptFile, tr.file = tr.file, latch
			}
			opened <- openedTranscript{b, tr.path}
		}
		return tr, err
	}
	t.Cleanup(func() {
		if latch != nil {
			latch.unblock()
		}
		if !c.ended {
			master.Write([]byte("\x15/exit\r"))
			select {
			case c.endErr = <-c.done:
				c.ended = true
			case <-time.After(5 * time.Second):
				slave.Close()
				t.Error("session cleanup timed out")
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), protocolWait)
	defer cancel()
	go func() {
		c.done <- runWithInput(Options{Runtime: e.runtime.Program, InitialPrompt: initial}, slave, c.pane, open, starter)
	}()
	fail := func(phase string, err error) {
		t.Helper()
		t.Errorf("post-bootstrap setup %s: %v", phase, err)
		rotationFailureDiagnostics(t, c, latch, []string{"spawns", "cursor_runtimes", "permission_requests"})
		t.FailNow()
	}
	for ready != nil || accepted != nil {
		select {
		case <-ready:
			ready = nil
		case <-accepted:
			accepted = nil
		case c.endErr = <-c.done:
			c.ended = true
			fail("ready/initial acceptance entry: Run ended", c.endErr)
		case <-ctx.Done():
			fail(fmt.Sprintf("ready/initial acceptance entry (ready=%v initial=%v)", ready == nil, initial == "" || accepted == nil), ctx.Err())
		}
	}
	var tr openedTranscript
	select {
	case tr = <-opened:
	case c.endErr = <-c.done:
		c.ended = true
		fail("transcript identity: Run ended", c.endErr)
	case <-ctx.Done():
		fail("transcript identity", ctx.Err())
	}
	tr.binding.SessionID = "synthetic-session-1"
	if latch != nil && (pending.Instance != tr.binding.InstanceID || pending.Token != tr.binding.Token || pending.Life != tr.binding.Life || pending.Connection != tr.binding.Connection || pending.Session != tr.binding.SessionID || pending.Data.ID != "initial" || pending.Data.Text != initial) {
		fail("saved initial identity at acceptance entry", fmt.Errorf("unexpected protected record: %+v", pending))
	}
	verified := make(chan error, 1)
	go func() { verified <- verifyBootstrapSnapshot(ctx, e.path, row, tr.binding, tr.path, initial) }()
	select {
	case err := <-verified:
		if err != nil {
			fail("waiting binding/session_start snapshot", err)
		}
	case c.endErr = <-c.done:
		c.ended = true
		fail("snapshot: Run ended", c.endErr)
	case <-ctx.Done():
		fail("waiting binding/session_start snapshot", ctx.Err())
	}
	// Do not return success if Run or the aggregate budget expired with the snapshot.
	select {
	case c.endErr = <-c.done:
		c.ended = true
		fail("verified snapshot: Run ended", c.endErr)
	case <-ctx.Done():
		fail("verified snapshot", ctx.Err())
	default:
	}
	return c
}

func verifyBootstrapSnapshot(ctx context.Context, dbPath string, before store.Spawn, expected store.CursorRuntime, path, initial string) error {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(0)")
	if err != nil {
		return err
	}
	defer db.Close()
	var row store.Spawn
	var b store.CursorRuntime
	b.InstanceID = before.InstanceID
	err = db.QueryRowContext(ctx, `SELECT s.state,s.session_id,s.jsonl_path,s.launch_token,s.life_number,s.pane_pid,s.pane_starttime,s.tmux_socket,s.pane_id,s.tmux_server_pid,s.tmux_server_started,s.tmux_server_starttime,
 r.bound_launch_token,r.bound_life_number,r.bound_gateway_pid,r.bound_gateway_start,r.bound_runtime_pid,r.bound_runtime_start,r.bound_connection_id,r.bound_input_pipe,r.bound_output_pipe,r.bound_input_fd,r.bound_output_fd,r.bound_session_id,r.bound_terminated
 FROM spawns s JOIN cursor_runtimes r ON r.bound_instance_id=s.instance_id WHERE s.instance_id=? AND s.agent='cursor'`, before.InstanceID).Scan(
		&row.State, &row.SessionID, &row.JSONLPath, &row.Identity.Token, &row.LifeNumber, &row.Identity.PanePID, &row.Identity.PaneStarttime, &row.Identity.Socket, &row.Identity.PaneID, &row.Identity.ServerPID, &row.Identity.ServerStart, &row.Identity.ServerStarttime,
		&b.Token, &b.Life, &b.GatewayPID, &b.GatewayStart, &b.RuntimePID, &b.RuntimeStart, &b.Connection, &b.InputPipe, &b.OutputPipe, &b.InputFD, &b.OutputFD, &b.SessionID, &b.Terminated)
	if err != nil {
		return err
	}
	if row.State != store.StateWaiting || row.SessionID != expected.SessionID || row.JSONLPath != path || row.LifeNumber != before.LifeNumber || row.Identity != before.Identity || b != expected || row.Identity.Token != b.Token || row.Identity.PanePID != b.GatewayPID || row.Identity.PaneStarttime != b.GatewayStart {
		return fmt.Errorf("bootstrap waiting identity changed: row=%+v runtime=%+v expected=%+v", row, b, expected)
	}
	if err := VerifyRuntime(b, false); err != nil {
		return err
	}
	f, err := openExistingTranscriptFlags(path, os.O_RDONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 256<<10 {
		return fmt.Errorf("unexpected bootstrap transcript size: %d", info.Size())
	}
	raw := make([]byte, info.Size())
	if _, err := f.ReadAt(raw, 0); err != nil {
		return err
	}
	starts, initials := 0, 0
	lines := bytes.Split(raw, []byte("\n"))
	if len(lines[len(lines)-1]) != 0 {
		return fmt.Errorf("incomplete bootstrap record")
	}
	for _, line := range lines[:len(lines)-1] {
		var r submissionRecord
		if err := json.Unmarshal(line, &r); err != nil {
			return err
		}
		if r.Kind != "session_start" && r.Kind != "submission_accepted" {
			continue
		}
		if r.Instance != b.InstanceID || r.Token != b.Token || r.Life != b.Life || r.Connection != b.Connection || r.Session != b.SessionID {
			return fmt.Errorf("bootstrap record binding changed: %+v", r)
		}
		if r.Kind == "session_start" {
			starts++
		} else {
			if initial == "" || r.Data.ID != "initial" || r.Data.Text != initial {
				return fmt.Errorf("unexpected bootstrap acceptance: %+v", r)
			}
			initials++
		}
	}
	wantInitials := 0
	if initial != "" {
		wantInitials = 1
	}
	if starts != 1 || initials != wantInitials {
		return fmt.Errorf("bootstrap record counts: session_start=%d initial acceptance entries=%d", starts, initials)
	}
	return ctx.Err()
}
