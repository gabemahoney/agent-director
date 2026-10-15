//go:build linux

package realtmux_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"golang.org/x/sys/unix"
)

func TestCursorRelayLockCannotExtendApprovalDeadline(t *testing.T) {
	// Serial: the shared real-pane fixture changes HOME, PATH and tmux environment.
	script := cursorfix.Scenario{SessionID: "lock-session", Requests: []json.RawMessage{relayFrame("locked", "session/request_permission", cursorfix.Permission("lock-session", "locked-tool", "execute"))}}
	f, r, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{Script: &script, Relay: "on", Timeout: 2})
	if _, e := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "delayed ack"}); e != nil {
		t.Fatal(e)
	}
	row := relayRequests(t, f, launch.ID, 1)[0]
	db, e := sql.Open("sqlite", f.DBPath)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if _, e = db.Exec(`CREATE TRIGGER reject_ack BEFORE UPDATE OF delivered_at ON permission_requests WHEN NEW.delivered_at IS NOT NULL BEGIN SELECT RAISE(ABORT,'held ack'); END`); e != nil {
		t.Fatal(e)
	}
	zero := int64(100)
	if _, e = f.API.Decide(api.DecideParams{InstanceID: launch.ID, RequestToken: row.RequestToken, Decision: "allow", MaxWaitMs: &zero}); e != nil {
		t.Fatal(e)
	}
	conn, e := db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	if _, e = conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); e != nil {
		t.Fatal(e)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	if _, e = conn.ExecContext(context.Background(), "DROP TRIGGER reject_ack"); e != nil {
		t.Fatal(e)
	}
	before := string(rawInput(t, r))
	// The lock is known held across this immutable, persisted approval deadline.
	timer := time.NewTimer(time.Until(row.SettledAt.Add(-4 * time.Second).Add(100 * time.Millisecond)))
	defer timer.Stop()
	<-timer.C
	if _, e = conn.ExecContext(context.Background(), "COMMIT"); e != nil {
		t.Fatal(e)
	}
	waitFor(t, "late allow falls back", func() bool {
		v, e := f.API.GetPermission(api.GetPermissionParams{RequestToken: row.RequestToken})
		return e == nil && v.Delivery == "fallen_back"
	}, func() string { return fmt.Sprint(relayReplies(t, r)) })
	got := relayRequests(t, f, launch.ID, 1)[0]
	if !got.DeliveredAt.IsZero() || got.Decision != "allow" || !got.SettledAt.Equal(row.SettledAt) || string(rawInput(t, r)) != before {
		t.Fatalf("late locked approval emitted/rearmed: %+v", got)
	}
}

// A full real pipe blocks the production writer; both accepted verdicts retire without bytes.
func TestCursorRelayQueuedVerdictsNeverReplay(t *testing.T) {
	// Serial: the shared real-pane fixture changes HOME, PATH and tmux environment.
	const session = "queued-session"
	script := cursorfix.Scenario{SessionID: session, PauseAfterRequests: true}
	for _, decision := range []string{"allow", "deny"} {
		script.Requests = append(script.Requests, relayFrame("queued-"+decision, "session/request_permission", cursorfix.Permission(session, "queue-tool-"+decision, "execute")))
	}
	f, r, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{Script: &script, Relay: "on", Timeout: 30})
	st, e := store.Open(f.DBPath)
	if e != nil {
		t.Fatal(e)
	}
	binding, e := st.ReadCursorRuntime(launch.ID)
	st.Close()
	if e != nil {
		t.Fatal(e)
	}
	pipePath := fmt.Sprintf("/proc/%d/fd/0", binding.RuntimePID)
	pipe, e := os.OpenFile(pipePath, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer pipe.Close()
	if _, e = unix.FcntlInt(pipe.Fd(), unix.F_SETPIPE_SZ, 4096); e != nil {
		t.Fatal(e)
	}
	if _, e = f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "queue verdicts"}); e != nil {
		t.Fatal(e)
	}
	rows := relayRequests(t, f, launch.ID, 2)
	waitFor(t, "peer stopped draining before pressure injection", func() bool { _, e := os.Stat(r.Paused); return e == nil }, nil)
	// Fixture bytes fill the paused peer's kernel pipe, and are never parsed as ACP.
	// They are a backpressure control, not claimed gateway output or real traffic.
	padding := strings.Repeat("x", 4096)
	writer, e := os.OpenFile(pipePath, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if e != nil {
		t.Fatal(e)
	}
	n, e := writer.Write([]byte(padding))
	writer.Close()
	if e != nil || n != len(padding) {
		t.Fatalf("pressure control: %d %v", n, e)
	}
	bound := int64(100)
	for _, row := range rows {
		decision := strings.TrimPrefix(row.ToolUseID, "queue-tool-")
		waitFor(t, "public decision under contention", func() bool {
			_, e = f.API.Decide(api.DecideParams{InstanceID: launch.ID, RequestToken: row.RequestToken, Decision: decision, MaxWaitMs: &bound})
			if e != nil && !errors.Is(e, api.ErrStoreBusy) {
				t.Fatal(e)
			}
			return e == nil
		}, func() string { return fmt.Sprint(e) })
	}
	waitFor(t, "both verdicts acknowledged with pipe still full", func() bool {
		for _, row := range rows {
			v, e := f.API.GetPermission(api.GetPermissionParams{RequestToken: row.RequestToken})
			if e != nil || v.Delivery != "delivered" {
				return false
			}
		}
		return true
	}, nil)
	waitFor(t, "gateway retires blocked transport", func() bool { return pidGone(binding.GatewayPID) }, func() string { return procState(binding.GatewayPID) })
	waitFor(t, "accepting runtime gone", func() bool { return pidGone(binding.RuntimePID) }, func() string { return procState(binding.RuntimePID) })
	raw, e := io.ReadAll(pipe)
	if e != nil {
		t.Fatal(e)
	}
	if string(raw) != padding {
		t.Fatalf("retired allow/deny added raw bytes beyond the exact pressure control: got %d, control %d", len(raw), len(padding))
	}
	if len(relayReplies(t, r)) != 0 {
		t.Fatal("paused peer parsed a response")
	}
	for _, row := range rows {
		if _, e = f.API.Decide(api.DecideParams{InstanceID: launch.ID, RequestToken: row.RequestToken, Decision: "allow", MaxWaitMs: &bound}); e == nil {
			t.Fatal("retired request accepted replay")
		}
	}
}
