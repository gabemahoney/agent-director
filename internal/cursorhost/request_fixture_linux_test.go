//go:build linux

package cursorhost

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

type requestEnv struct {
	runtime cursorfix.Runtime
	process *ownedProcess
	s       *store.Store
	path    string
	binding store.CursorRuntime
	tr      *transcript
	rep     *reporter
}

// newRequestEnv shares the reporter's verified kernel, pipe and pane fixture.
func newRequestEnv(t *testing.T, relay, session string) *requestEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", faketmuxfix.Dir(t)+":"+os.Getenv("PATH"))
	runtime := cursorfix.New(t)
	process, err := startProcess(runtime.Program, []string{"acp"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { process.terminate() })
	t.Cleanup(func() { process.input.Close() })
	t.Cleanup(func() { process.output.Close() })
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
	t.Cleanup(func() { s.Close() })
	token := "1111111111111111"
	id := "report-row"
	socket := filepath.Join(t.TempDir(), "socket")
	if err = s.InsertPending(store.Spawn{InstanceID: id, Agent: store.AgentCursor, CWD: home, TmuxSessionName: "report-session", RelayMode: relay, Identity: store.LaunchIdentity{Token: token, Socket: socket}}); err != nil {
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
	if ok, err := s.SetCursorRuntimeSession(b, session); err != nil || !ok {
		t.Fatalf("session %v %v", ok, err)
	}
	b.SessionID = session
	tr, err := openTranscript(home, b)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.file.Close() })

	initial := store.HookGate{Event: "SessionStart", ParentPID: b.GatewayPID, ParentStart: start, SessionID: b.SessionID, Cursor: &b}
	if a, err := s.ApplyHookTransition(id, initial, store.StateWaiting, false, "SessionStart", tr.path, true); err != nil || !a.Applied {
		t.Fatalf("initial session %+v %v", a, err)
	}
	faketmuxfix.Tables{}.Write(t, socket, faketmuxfix.Table{Server: &faketmuxfix.Server{PID: os.Getpid(), Start: li.ServerStart}, Sessions: []faketmuxfix.Session{{ID: "$0", Name: row.TmuxSessionName, Created: li.ServerStart, Label: fmt.Sprintf("ad1 %s $0 %s %s", token, id, s.StoreID()), Panes: []faketmuxfix.Pane{{ID: "%0", PID: os.Getpid(), AdPane: token + " %0"}}}}})
	return &requestEnv{runtime: runtime, process: process, s: s, path: path, binding: b, tr: tr, rep: &reporter{store: s, cfg: config.Default(), binding: b, path: tr.path}}
}
func (e *requestEnv) controller(t *testing.T, wire io.Writer, pane io.Writer) *requestController {
	t.Helper()
	p := newProtocol(wire, strings.NewReader(""))
	t.Cleanup(p.close)
	return newRequestController(e.rep, p, pane)
}

func (e *requestEnv) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", e.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}
func (e *requestEnv) request(t *testing.T, token string) store.PermissionRow {
	t.Helper()
	row, err := e.s.GetPermissionRequest(e.binding.InstanceID, token)
	if err != nil {
		t.Fatal(err)
	}
	return row
}
func (e *requestEnv) decide(t *testing.T, token, decision string) {
	t.Helper()
	zero := int64(0)
	result, err := api.Decide(e.s, api.RelayView{}, api.DecideParams{InstanceID: e.binding.InstanceID, RequestToken: token, Decision: decision, MaxWaitMs: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if result.Delivery != api.DeliveryNotConfirmed || result.HookAlive != nil {
		t.Fatalf("API decision invented delivery evidence: %+v", result)
	}
}

func (e *requestEnv) admit(t *testing.T, c *requestController, id, method string, params []byte) *pendingAuthorization {
	t.Helper()
	before := len(c.requests)
	if err := c.admit(frame{ID: []byte(id), Method: method, Params: params}, e.binding.SessionID); err != nil {
		t.Fatal(err)
	}
	if len(c.requests) != before+1 {
		t.Fatal("request not independently admitted")
	}
	for _, r := range c.requests {
		if string(r.auth.frame.ID) == id {
			return r
		}
	}
	t.Fatal("lost request")
	return nil
}

// observedWire allows a committed read before the first response byte is accepted.
type observedWire struct {
	mu      sync.Mutex
	raw     []byte
	first   func() error
	err     error
	partial bool
}

func (w *observedWire) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.raw) == 0 && w.first != nil {
		w.err = w.first()
		if w.err != nil {
			return 0, w.err
		}
	}
	if w.partial {
		w.raw = append(w.raw, p[:1]...)
		return 1, io.ErrUnexpectedEOF
	}
	w.raw = append(w.raw, p...)
	return len(p), nil
}
func (w *observedWire) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.raw...)
}
