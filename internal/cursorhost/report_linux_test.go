//go:build linux

package cursorhost

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
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
	g := store.HookGate{Event: payload.Event, ParentPID: b.GatewayPID, ParentStart: b.GatewayStart, SessionID: b.SessionID, Cursor: &b}
	if _, a, e := s.ApplyHookTransitionResult(b.InstanceID, g, target, false, payload.Event, payload.Path, true); e != nil || !a.Applied {
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
				home := t.TempDir()
				t.Setenv("HOME", home)
				t.Setenv("PATH", faketmuxfix.Dir(t)+":"+os.Getenv("PATH"))
				runtime := cursorfix.New(t)
				process, err := startProcess(runtime.Program, []string{"acp"}, t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				defer process.terminate()
				defer process.input.Close()
				defer process.output.Close()
				start, alive, known := probe.NewProcChecker().StartTime(os.Getpid())
				if !known || !alive {
					t.Fatal("gateway kernel identity unavailable")
				}
				in, err := pipeAt(process.pid, 0)
				if err != nil {
					t.Fatal(err)
				}
				out, err := pipeAt(process.pid, 1)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "state.db")
				s, err := store.OpenOrInit(path)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				token := "1111111111111111"
				id := "report-row"
				socket := filepath.Join(t.TempDir(), "socket")
				if err = s.InsertPending(store.Spawn{InstanceID: id, Agent: store.AgentCursor, CWD: home, TmuxSessionName: "report-session", RelayMode: "on", Identity: store.LaunchIdentity{Token: token, Socket: socket}}); err != nil {
					t.Fatal(err)
				}
				row, err := s.GetSpawn(id)
				if err != nil {
					t.Fatal(err)
				}
				li := store.LaunchIdentity{Token: token, Socket: socket, PaneID: "%0", PanePID: os.Getpid(), PaneStarttime: start, ServerPID: os.Getpid(), ServerStart: time.Now().Unix(), ServerStarttime: start}
				if a, err := s.RecordLaunchIdentity(id, row.RowVersion, token, li); err != nil || a != store.CondApplied {
					t.Fatalf("pane %v %v", a, err)
				}
				row, err = s.GetSpawn(id)
				if err != nil {
					t.Fatal(err)
				}
				b := store.CursorRuntime{InstanceID: id, Token: token, Life: row.LifeNumber, GatewayPID: os.Getpid(), GatewayStart: start, RuntimePID: process.pid, RuntimeStart: process.start, Connection: "synthetic-report", InputPipe: in, OutputPipe: out, InputFD: int(process.input.Fd()), OutputFD: int(process.output.Fd())}
				if ok, err := s.RegisterCursorRuntime(row, b); err != nil || !ok {
					t.Fatalf("runtime %v %v", ok, err)
				}
				if ok, err := s.SetCursorRuntimeSession(b, "report-session-id"); err != nil || !ok {
					t.Fatalf("session %v %v", ok, err)
				}
				b.SessionID = "report-session-id"
				tr, err := openTranscript(home, b)
				if err != nil {
					t.Fatal(err)
				}
				defer tr.file.Close()

				initial := store.HookGate{Event: "SessionStart", ParentPID: b.GatewayPID, ParentStart: start, SessionID: b.SessionID, Cursor: &b}
				if a, err := s.ApplyHookTransition(id, initial, store.StateWaiting, false, "SessionStart", tr.path, true); err != nil || !a.Applied {
					t.Fatalf("initial session %+v %v", a, err)
				}
				if mode != "no hold" {
					g := store.HookGate{Event: "PermissionRequest", ParentPID: b.GatewayPID, ParentStart: start, SessionID: b.SessionID, Cursor: &b}
					if _, a, err := s.InsertRelayRequest(id, g, store.RelayRequest{RequestToken: "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", ToolName: "Bash", ToolInput: "{}", SettledAt: time.Now().Add(time.Hour)}, 0, store.DefaultLockWait); err != nil || !a.Applied {
						t.Fatalf("seed request %+v %v", a, err)
					}
				}
				faketmuxfix.Tables{}.Write(t, socket, faketmuxfix.Table{Server: &faketmuxfix.Server{PID: os.Getpid(), Start: li.ServerStart}, Sessions: []faketmuxfix.Session{{ID: "$0", Name: row.TmuxSessionName, Created: li.ServerStart, Label: fmt.Sprintf("ad1 %s $0 %s %s", token, id, s.StoreID()), Panes: []faketmuxfix.Pane{{ID: "%0", PID: os.Getpid(), AdPane: token + " %0"}}}}})
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
