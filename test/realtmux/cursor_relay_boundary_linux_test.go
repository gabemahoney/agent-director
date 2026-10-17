//go:build linux

package realtmux_test

import (
	"bytes"
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

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
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
	approval := row.SettledAt.Add(-4 * time.Second)
	setupCtx, stopSetup := context.WithDeadline(context.Background(), approval)
	defer stopSetup()
	if _, e = conn.ExecContext(setupCtx, "PRAGMA busy_timeout = 0"); e != nil {
		t.Fatal(e)
	}
	for {
		_, e = conn.ExecContext(setupCtx, "BEGIN IMMEDIATE")
		if e == nil {
			break
		}
		if !strings.Contains(e.Error(), "SQLITE_BUSY") {
			t.Fatalf("holder setup before immutable %v: %v", approval, e)
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-timer.C:
		case <-setupCtx.Done():
			timer.Stop()
			t.Fatalf("holder not acquired before immutable approval %v: %v", approval, setupCtx.Err())
		}
	}
	holderReady := time.Now()
	if !holderReady.Before(approval) {
		t.Fatalf("holderReady=%v not before original approval=%v", holderReady, approval)
	}
	t.Logf("holderReady=%v originalApproval=%v", holderReady, approval)
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
	diagnostics := cursorFailureDiagnostics{t: t, f: f, peer: r, id: launch.ID, home: os.Getenv("HOME")}
	defer diagnostics.capture()
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
			started := time.Now()
			result, decideErr := f.API.Decide(api.DecideParams{InstanceID: launch.ID, RequestToken: row.RequestToken, Decision: decision, MaxWaitMs: &bound})
			e = decideErr
			diagnostics.record("Decide "+decision, started, result, e)
			if e != nil && !errors.Is(e, api.ErrStoreBusy) {
				name, description := errnames.Classify(e)
				if name != "ErrInternal" || !strings.HasPrefix(description, "store: commit outcome unconfirmed; read the request before retrying (") {
					t.Fatal(e)
				}
				// An uncertain commit is never replayed or a delivery receipt.
				started = time.Now()
				verdict, readErr := f.API.GetPermission(api.GetPermissionParams{RequestToken: row.RequestToken})
				diagnostics.record("GetPermission after uncertain "+decision, started, verdict, readErr)
				t.Logf("uncertain decision: %v; public readback: %+v; error: %v", e, verdict, readErr)
				reasonMatches := verdict.DecisionReason == nil
				if decision == "deny" {
					reasonMatches = verdict.DecisionReason != nil && *verdict.DecisionReason == "operator"
				}
				if readErr != nil || verdict.RequestToken != row.RequestToken || verdict.RequestID != row.RequestID || verdict.ToolInput != row.ToolInput || verdict.Decision == nil || *verdict.Decision != decision || !reasonMatches || verdict.DecidedAt == nil || verdict.DecidedAt.IsZero() {
					t.Fatalf("uncertain decision has no exact public recorded verdict: original=%v readback=%+v read_error=%v", e, verdict, readErr)
				}
				started = time.Now()
				owner, ownerErr := f.API.Get(launch.ID)
				diagnostics.record("Get owner after uncertain "+decision, started, owner, ownerErr)
				owned := false
				for _, request := range owner.UnprovenRequests {
					if request.RequestToken == verdict.RequestToken && request.RequestID == verdict.RequestID {
						owned = true
					}
				}
				if ownerErr != nil || owner.InstanceID != launch.ID || !owned {
					t.Fatalf("uncertain decision public ownership mismatch: original=%v owner=%+v read_error=%v", e, owner, ownerErr)
				}
				return true
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

// A held authorization must not replace the finite cancellation timer on release.
func TestCursorRelayHeldCancellationDeadline(t *testing.T) {
	// Serial: the shared real-pane fixture changes HOME, PATH and tmux environment.
	for _, mode := range []string{"interrupt", "policy", "successful release"} {
		t.Run(mode, func(t *testing.T) {
			script := relayCapturedScenario(t)
			script.WaitForRelease = true
			if mode == "policy" {
				script.Requests = append(script.Requests, relayFrame("unsupported", "cursor/authorize", json.RawMessage(`{}`)))
			}
			f, r, launch := newCursorPaneScenario(t, false, "independent-worker", cursorPaneOptions{Script: &script, Relay: "on", Timeout: 30})
			id := launch.ID
			st, err := store.Open(f.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			binding, err := st.ReadCursorRuntime(id)
			if err != nil {
				t.Fatal(err)
			}
			worker := 0
			waitFor(t, "independent worker sentinel", func() bool {
				raw, err := os.ReadFile(r.WorkerPID)
				if err == nil {
					fmt.Sscan(string(raw), &worker)
				}
				return worker > 0
			}, nil)
			before := readRow(t, f.DBPath, id)
			controlBoundary := time.Now()
			if _, err = f.API.SendKeys(api.SendKeysParams{InstanceID: id, Text: "held cancellation"}); err != nil {
				t.Fatal(err)
			}
			rows := relayRequests(t, f, id, 2)
			var permission, plan store.PermissionRow
			for _, row := range rows {
				var params struct {
					Tool string `json:"toolCallId"`
				}
				if err = json.Unmarshal([]byte(row.ToolInput), &params); err != nil {
					t.Fatal(err)
				}
				if params.Tool != "" {
					plan = row
				} else {
					permission = row
				}
			}
			if permission.RequestToken == "" || plan.RequestToken == "" {
				t.Fatalf("missing independent plan/permission: %+v", rows)
			}
			if mode != "policy" {
				if _, err = f.API.Decide(api.DecideParams{InstanceID: id, RequestToken: plan.RequestToken, Decision: "deny"}); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "only plan rejection received", func() bool { return len(relayReplies(t, r)) == 1 }, nil)
				if got := relayReplies(t, r)[0]; string(got["id"]) != `"plan"` || !strings.Contains(string(got["result"]), `"outcome":"rejected"`) {
					t.Fatalf("wrong independent plan answer: %v", got)
				}
			}
			completed := false
			assertPreserved := func() {
				t.Helper()
				row := readRow(t, f.DBPath, id)
				pc := probe.NewProcChecker()
				gatewayStart, gatewayAlive, gatewayKnown := pc.StartTime(binding.GatewayPID)
				runtimeStart, runtimeAlive, runtimeKnown := pc.StartTime(binding.RuntimePID)
				provenGone := (gatewayKnown && (!gatewayAlive || gatewayStart != binding.GatewayStart)) || (runtimeKnown && (!runtimeAlive || runtimeStart != binding.RuntimeStart))
				missing := row.State == store.StateMissing && provenGone
				if row.SessionID != before.SessionID || row.LaunchToken != before.LaunchToken || row.LifeNumber != before.LifeNumber || row.PanePID != before.PanePID || row.PaneStarttime != before.PaneStarttime || (!completed && row.State != store.StateCheckPermission && !missing) {
					t.Fatalf("conversation/hold changed without exact death: row=%+v gateway known/alive/start=%v/%v/%q runtime=%v/%v/%q", row, gatewayKnown, gatewayAlive, gatewayStart, runtimeKnown, runtimeAlive, runtimeStart)
				}
				if pidGone(worker) {
					t.Fatal("cancellation killed independent worker sentinel")
				}
				if mode != "successful release" {
					held := []store.PermissionRow{permission}
					if mode == "policy" {
						held = append(held, plan)
					}
					for _, request := range held {
						got, err := st.GetPermissionRequest(id, request.RequestToken)
						if err != nil {
							t.Fatal(err)
						}
						if got.Closed() && provenGone {
							// The atomic terminal close may commit between these raw reads.
							missing = readRow(t, f.DBPath, id).State == store.StateMissing
						}
						if missing {
							if got.Decision != "deny" || !got.ProvenGoneAt.IsZero() || got.ProvenGoneHow != "" || !got.Closed() || !got.DeliveredAt.IsZero() || got.PaneAnswered() {
								t.Fatalf("missing did not atomically close held request: row=%+v request=%+v", row, got)
							}
						} else if got.Decision != "" || !got.DeliveredAt.IsZero() || got.ProvenGone() || got.Closed() || got.PaneAnswered() {
							t.Fatalf("cancellation released independent request without exact death: row=%+v request=%+v gateway=%v/%v/%q runtime=%v/%v/%q", row, got, gatewayKnown, gatewayAlive, gatewayStart, runtimeKnown, runtimeAlive, runtimeStart)
						}
					}
				}
			}
			assertPreserved()
			started := time.Now()
			if mode == "successful release" {
				if _, err = f.API.Decide(api.DecideParams{InstanceID: id, RequestToken: permission.RequestToken, Decision: "allow"}); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "both independent answers received", func() bool { return len(relayReplies(t, r)) == 2 }, nil)
				// Generation remains live beyond cancellation's 3s bound, then completes normally.
				waitForObserved(t, 5*time.Second, "ordinary generation continues after successful hold release", func() bool {
					assertPreserved()
					if pidGone(binding.GatewayPID) || pidGone(binding.RuntimePID) {
						t.Fatal("ordinary hold release armed a cancellation deadline")
					}
					return time.Since(started) >= 3200*time.Millisecond
				}, nil)
				if err = os.WriteFile(r.Release, nil, 0600); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "successful release completes the same conversation", func() bool {
					raw, err := os.ReadFile(before.JSONLPath.(string))
					return err == nil && strings.Contains(string(raw), `"kind":"turn_outcome"`)
				}, nil)
				completed = true
				assertPreserved()
			} else {
				lastInterrupt := time.Time{}
				if mode == "interrupt" {
					controlBoundary = time.Now()
				}
				waitForObserved(t, 5*time.Second, "original cancellation deadline retires gateway and runtime", func() bool {
					pc := probe.NewProcChecker()
					if tmux.JudgeProcess(pc, tmux.ProcIdentity{PID: binding.GatewayPID, Starttime: binding.GatewayStart}) == tmux.ProcGone && tmux.JudgeProcess(pc, tmux.ProcIdentity{PID: binding.RuntimePID, Starttime: binding.RuntimeStart}) == tmux.ProcGone {
						return true
					}
					assertPreserved()
					if mode == "interrupt" && time.Since(lastInterrupt) >= 150*time.Millisecond {
						launch.rt.run(t, "send-keys", "-t", launch.Row.PaneID.(string), "C-c")
						lastInterrupt = time.Now()
					}
					return false
				}, func() string {
					return fmt.Sprintf("gateway=%s runtime=%s", procState(binding.GatewayPID), procState(binding.RuntimePID))
				})
				retiredAt := time.Now()
				assertPreserved()
				raw, readErr := os.ReadFile(before.JSONLPath.(string))
				if readErr != nil {
					t.Fatal(readErr)
				}
				for _, line := range bytes.Split(raw, []byte{'\n'}) {
					var record struct {
						Kind string `json:"kind"`
						Data struct {
							ConsumedAt time.Time `json:"consumed_at"`
						} `json:"data"`
					}
					if json.Unmarshal(line, &record) == nil && record.Kind == "input_control" && !record.Data.ConsumedAt.IsZero() {
						controlBoundary = record.Data.ConsumedAt
						break
					}
				}
				if elapsed := retiredAt.Sub(controlBoundary); elapsed < 3*time.Second {
					t.Fatalf("cancellation expired before the original 3s deadline: %s", elapsed)
				}
				failures := hookTrailRecords(t, os.Getenv("HOME"), "ad.cursor.failed", id)
				if len(failures) != 1 || failures[0]["instance_id"] != id || failures[0]["source"] != "ad_cursor_host" {
					t.Fatalf("expected one attributed cancellation failure: %v", failures)
				}
				if reason, ok := failures[0]["reason"].(string); !ok || strings.TrimSpace(reason) == "" {
					t.Fatalf("cancellation failure lacks diagnostic reason: %v", failures)
				}
			}
			cancels, permissionReplies, prompts, replies := 0, 0, 0, 0
			for _, frame := range r.Frames(t) {
				switch string(frame["method"]) {
				case `"session/cancel"`:
					cancels++
					if !strings.Contains(string(frame["params"]), script.SessionID) {
						t.Fatal("cancellation changed conversation")
					}
				case `"session/prompt"`:
					prompts++
				case "":
					replies++
					if mode == "policy" && (string(frame["id"]) != `"unsupported"` || len(frame["error"]) == 0) {
						t.Fatalf("policy cancellation answered an admitted request: %v", frame)
					}
					if string(frame["id"]) == `"permission"` {
						permissionReplies++
					}
				}
			}
			wantCancels, wantPermissionReplies, wantReplies := 1, 0, 1
			if mode == "successful release" {
				wantCancels, wantPermissionReplies, wantReplies = 0, 1, 2
			}
			if cancels != wantCancels || permissionReplies != wantPermissionReplies || replies != wantReplies || prompts != 1 || pidGone(worker) {
				t.Fatalf("cancel=%d permission replies=%d total replies=%d prompts=%d sentinel gone=%v", cancels, permissionReplies, replies, prompts, pidGone(worker))
			}
			t.Logf("mode=%s original bound=5s elapsed=%s cancels=%d permission replies=%d sentinel preserved", mode, time.Since(started), cancels, permissionReplies)
		})
	}
}
