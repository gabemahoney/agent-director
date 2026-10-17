//go:build linux

package cursorhost

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
)

func init() { cursorReportHelper = runCursorReportHelper }

// The external hook-command seam applies its store write; reporter still verifies real kernel/pane evidence.
func runCursorReportHelper() int {
	path := os.Getenv("AD_TEST_CURSOR_REPORT_DB")
	s, err := store.Open(path)
	if err != nil {
		return 2
	}
	defer s.Close()
	b, err := s.ReadCursorRuntime("report-row")
	if err != nil {
		return 2
	}
	var payload struct {
		Event      string `json:"hook_event_name"`
		Path       string `json:"transcript_path"`
		Session    string `json:"session_id"`
		Connection string `json:"cursor_connection"`
		Previous   string `json:"cursor_previous_session"`
		Deadline   string `json:"cursor_rotation_deadline"`
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil || json.Unmarshal(raw, &payload) != nil {
		return 2
	}
	if observed := os.Getenv("AD_TEST_CURSOR_REPORT_PAYLOADS"); observed != "" {
		f, e := os.OpenFile(observed, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
		if e != nil {
			return 2
		}
		_, e = f.Write(append(raw, '\n'))
		syncErr := f.Sync()
		closeErr := f.Close()
		if e != nil || syncErr != nil || closeErr != nil {
			return 2
		}
	}
	mode := os.Getenv("AD_TEST_CURSOR_REPORT_MODE")
	if payload.Connection != b.Connection || payload.Session == "" {
		return 2
	}
	if mode == "stale" {
		return 0
	}
	target := store.StateWaiting
	if payload.Event == "UserPromptSubmit" {
		target = store.StateWorking
	}
	if payload.Event == "SessionEnd" {
		target = store.StateEnded
	}
	if !cursorReportBarrier(payload.Event, "before") {
		return 2
	}
	g := store.HookGate{Event: payload.Event, ParentPID: b.GatewayPID, ParentStart: b.GatewayStart, SessionID: b.SessionID, Cursor: &b}
	if payload.Previous != "" {
		until, e := time.Parse(time.RFC3339Nano, payload.Deadline)
		if e != nil || payload.Event != "SessionStart" || payload.Previous != b.SessionID || payload.Session == b.SessionID || !time.Now().Before(until) {
			return 2
		}
		row, e := s.GetSpawn(b.InstanceID)
		if e != nil {
			return 2
		}
		ok, e := s.RotateCursorSession(row, g, payload.Session, payload.Path, until, func(row store.Spawn) error {
			if e := VerifyRuntime(b, false); e != nil {
				return e
			}
			return VerifyPane(s, row, config.Default())
		})
		if e != nil || !ok {
			return 2
		}
		if mode == "rotation-error-after-commit" {
			return 2
		}
	} else if payload.Session != b.SessionID || payload.Deadline != "" {
		return 2
	} else if _, a, e := s.ApplyHookTransitionResult(b.InstanceID, g, target, false, payload.Event, payload.Path, true); e != nil || !a.Applied {
		return 2
	}
	if !cursorReportBarrier(payload.Event, "after") {
		return 2
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return 2
	}
	defer db.Close()
	mutation := map[string]string{
		"no hold":       `UPDATE spawns SET state='check_permission'`,
		"connection":    `UPDATE cursor_runtimes SET bound_connection_id='replacement'`,
		"session":       `UPDATE spawns SET session_id='replacement'`,
		"pane":          `UPDATE spawns SET pane_pid=pane_pid+1`,
		"retired":       `UPDATE cursor_runtimes SET bound_terminated=1`,
		"replacement":   `UPDATE spawns SET launch_token='2222222222222222'`,
		"runtime start": `UPDATE cursor_runtimes SET bound_runtime_start='reused'`,
	}
	if q, ok := mutation[mode]; ok {
		if _, err = db.Exec(q); err != nil {
			return 2
		}
	}
	return 0
}

func TestReporterHeldLifecycleRequiresIndependentEvidence(t *testing.T) {
	// Serial: subprocess controls, HOME and fake tmux PATH use t.Setenv.
	if testing.Short() {
		t.Skip("protected runtime and hook subprocess acceptance")
	}
	for _, event := range []string{"Stop", "Interrupt", "UserPromptSubmit"} {
		for _, mode := range []string{"current", "no hold", "stale", "connection", "session", "pane", "retired", "replacement", "runtime start"} {
			t.Run(event+"/"+mode, func(t *testing.T) {
				e := newRequestEnv(t, "on", "report-session-id")
				s, b, path := e.s, e.binding, e.path
				id, start, tr := b.InstanceID, b.GatewayStart, e.tr

				if mode != "no hold" {
					g := store.HookGate{Event: "PermissionRequest", ParentPID: b.GatewayPID, ParentStart: start, SessionID: b.SessionID, Cursor: &b}
					if _, a, err := s.InsertRelayRequest(id, g, store.RelayRequest{RequestToken: "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", ToolName: "Bash", ToolInput: "{}", SettledAt: time.Now().Add(time.Hour)}, 0, store.DefaultLockWait); err != nil || !a.Applied {
						t.Fatalf("seed request %+v %v", a, err)
					}
				}
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv("AD_TEST_CURSOR_REPORT_HELPER", "1")
				t.Setenv("AD_TEST_CURSOR_REPORT_DB", path)
				t.Setenv("AD_TEST_CURSOR_REPORT_MODE", mode)
				rep := reporter{store: s, cfg: config.Default(), binding: b, exe: exe, path: tr.path}
				err = rep.report(event)
				if (err == nil) != (mode == "current") {
					t.Fatalf("report %v; mode %s", err, mode)
				}
			})
		}
	}
}

func TestReporterRetirementCannotRestartExpiredControlDeadline(t *testing.T) {
	// Serial: real direct-child report helper and isolated runtime set environment.
	for _, delayed := range []bool{false, true} {
		t.Run(map[bool]string{false: "already expired", true: "expires during retirement"}[delayed], func(t *testing.T) {
			e := newRequestEnv(t, "off", "report-session-id")
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			payloads := filepath.Join(t.TempDir(), "reports")
			t.Setenv("AD_TEST_CURSOR_REPORT_HELPER", "1")
			t.Setenv("AD_TEST_CURSOR_REPORT_DB", e.path)
			t.Setenv("AD_TEST_CURSOR_REPORT_PAYLOADS", payloads)
			if err = e.process.terminate(); err != nil {
				t.Fatal(err)
			}
			rep := *e.rep
			rep.exe = exe
			rep.terminationUntil = time.Now().Add(-time.Millisecond)
			if delayed {
				rep.terminationUntil = time.Now().Add(cancelWait)
				rep.afterRuntimeRetired = func() { <-time.After(time.Until(rep.terminationUntil)) }
			}
			err = rep.report("SessionEnd")
			if err == nil || !strings.Contains(err.Error(), "end report deadline passed") {
				t.Fatalf("expired report=%v", err)
			}
			raw, readErr := os.ReadFile(payloads)
			if !os.IsNotExist(readErr) || bytes.Contains(raw, []byte("SessionEnd")) {
				t.Fatalf("expired worker launched helper: %s %v", raw, readErr)
			}
			binding, readErr := e.s.ReadCursorRuntime(e.binding.InstanceID)
			if readErr != nil || !binding.Terminated || rep.binding != e.binding {
				t.Fatalf("retirement proof leaked into controller binding: %+v %v", binding, readErr)
			}
		})
	}
}

// cursorReportBarrier pauses the existing subprocess seam around its actual commit.
func cursorReportBarrier(event, phase string) bool {
	dir := os.Getenv("AD_TEST_CURSOR_BARRIER_DIR")
	if event != os.Getenv("AD_TEST_CURSOR_BARRIER_EVENT") || phase != os.Getenv("AD_TEST_CURSOR_BARRIER_PHASE") {
		if _, err := os.Stat(filepath.Join(dir, "release")); err != nil {
			return true
		}
		dir = os.Getenv("AD_TEST_CURSOR_SECOND_BARRIER_DIR")
		if dir == "" || event != "UserPromptSubmit" || phase != "before" {
			return true
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "entered"), []byte(event+"/"+phase), 0600); err != nil {
		return false
	}
	until := time.Now().Add(4 * time.Second)
	for time.Now().Before(until) {
		if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

func TestReporterObservesCommittedRotationBeforeStalledHookReturns(t *testing.T) {
	// Serial: real direct-child helper, independent SQL and kernel/pane verification.
	for _, stop := range []string{"cancel", "timeout", "runtime death", "cancel before start", "cancel with replaced runtime"} {
		t.Run(stop, func(t *testing.T) {
			e := newRequestEnv(t, "off", "report-session-id")
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("AD_TEST_CURSOR_REPORT_HELPER", "1")
			t.Setenv("AD_TEST_CURSOR_REPORT_DB", e.path)
			dir := rotationBarrier(t, "after")
			until := time.Now().Add(cancelWait)
			ctx, cancel := context.WithDeadline(context.Background(), until)
			defer cancel()
			observed := make(chan store.CursorRuntime, 2)
			reconciled := make(chan struct{}, 1)
			rep := *e.rep
			rep.exe = exe
			rep.rotationContext = ctx
			rep.rotationObserved = func(b store.CursorRuntime) { observed <- b }
			rep.rotationReconciled = func() { reconciled <- struct{}{} }
			done := make(chan error, 1)
			if stop == "cancel before start" {
				cancel()
			}
			go func() { done <- rep.reportIdentity("SessionStart", nil, "report-fresh-session", until) }()
			if stop == "cancel before start" {
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("cancelled unstarted hook falsely succeeded")
					}
				case <-time.After(time.Until(until)):
					t.Fatal("cancelled unstarted hook did not return")
				}
				row, err := e.s.GetSpawn(e.binding.InstanceID)
				if err != nil {
					t.Fatal(err)
				}
				h, err := e.s.ListSessionHistory(e.binding.InstanceID, e.binding.Life)
				if err != nil || row.SessionID != e.binding.SessionID || len(h) != 0 {
					t.Fatalf("unstarted hook changed committed identity: %+v %+v %v", row, h, err)
				}
				if _, err := os.Stat(filepath.Join(dir, "entered")); !os.IsNotExist(err) {
					t.Fatalf("cancel-before-start helper unexpectedly ran: %v", err)
				}
				select {
				case b := <-observed:
					t.Errorf("unstarted hook fabricated committed binding: %+v", b)
				default:
				}
				// Intentional exit needs a bounded final SQL snapshot even when
				// context cancellation prevents exec.Start from creating a child.
				select {
				case <-reconciled:
				default:
					t.Error("cancelled-before-start publication omitted final reconciliation; intentional shutdown cannot finalize the unchanged committed conversation")
				}
				return
			}
			// Commit observation may precede the helper's next instruction (the
			// after barrier marker). Wait for that actual marker independently.
			for {
				if _, err := os.Stat(filepath.Join(dir, "entered")); err == nil {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("hook returned before barrier: %v", err)
				default:
				}
				if !time.Now().Before(until) {
					t.Fatal("after-COMMIT hook barrier not reached")
				}
				time.Sleep(time.Millisecond)
			}
			select {
			case b := <-observed:
				if b.SessionID != "report-fresh-session" || b.Connection != e.binding.Connection {
					t.Fatalf("observation binding: %+v", b)
				}
				if _, err := os.Stat(filepath.Join(dir, "entered")); err != nil {
					t.Fatalf("actual after-commit barrier absent: %v", err)
				}
				row, err := e.s.GetSpawn(b.InstanceID)
				if err != nil {
					t.Fatal(err)
				}
				current, err := e.s.ReadCursorRuntime(b.InstanceID)
				if err != nil {
					t.Fatal(err)
				}
				h, err := e.s.ListSessionHistory(b.InstanceID, b.Life)
				if err != nil || current != b || row.SessionID != b.SessionID || row.JSONLPath != e.tr.path || len(h) != 1 || h[0].SessionID != e.binding.SessionID || h[0].JSONLPath != e.tr.path {
					t.Fatalf("observer fired without committed SQL: %+v %+v %+v %v", row, current, h, err)
				}
				if err := VerifyRuntime(b, false); err != nil {
					t.Fatal(err)
				}
			case err := <-done:
				t.Fatalf("hook completed before commit observation: %v", err)
			case <-time.After(time.Until(until)):
				t.Fatal("committed observation never arrived while hook was blocked")
			}
			select {
			case err := <-done:
				t.Fatalf("unreleased hook returned before requested stop: %v", err)
			default:
			}
			switch stop {
			case "cancel":
				cancel()
			case "cancel with replaced runtime":
				db, err := sql.Open("sqlite", e.path)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if _, err := db.Exec(`UPDATE cursor_runtimes SET bound_connection_id='replacement' WHERE bound_instance_id=?`, e.binding.InstanceID); err != nil {
					t.Fatal(err)
				}
				cancel()
			case "runtime death":
				if err := e.process.terminate(); err != nil {
					t.Fatal(err)
				}
				cancel()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("stalled/killed hook falsely succeeded")
				}
				t.Logf("report stop=%s: %v", stop, err)
			case <-time.After(time.Until(until.Add(time.Second))):
				t.Fatal("report exceeded original deadline")
			}
			if _, err := os.Stat(filepath.Join(dir, "release")); !os.IsNotExist(err) {
				t.Fatalf("helper released before observation/report completion: %v", err)
			}
			if stop == "timeout" || stop == "cancel with replaced runtime" {
				select {
				case <-reconciled:
					t.Error("unknown or expired final snapshot authorized reconciliation")
				default:
				}
			} else {
				select {
				case <-reconciled:
				default:
					t.Error("cancelled hook exit lacked final bounded snapshot")
				}
			}
		})
	}
}
