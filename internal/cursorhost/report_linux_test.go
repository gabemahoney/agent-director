//go:build linux

package cursorhost

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
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
		Event string `json:"hook_event_name"`
		Path  string `json:"transcript_path"`
	}
	if err = json.NewDecoder(os.Stdin).Decode(&payload); err != nil {
		return 2
	}
	mode := os.Getenv("AD_TEST_CURSOR_REPORT_MODE")
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
	if _, a, e := s.ApplyHookTransitionResult(b.InstanceID, g, target, false, payload.Event, payload.Path, true); e != nil || !a.Applied {
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
