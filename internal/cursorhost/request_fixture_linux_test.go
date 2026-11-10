//go:build linux

package cursorhost

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
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
	"github.com/gabemahoney/agent-director/internal/testsupport/sqliteobserve"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// requestTestClock adds explicit timer dispatch to the shared injected clock.
// Advancing runs due callbacks after releasing its lock; AfterFunc never calls
// a callback inline. Tests consume actual captured wakes from the controller.
type requestTestClock struct {
	*tmuxfix.Clock
	mu        sync.Mutex
	timers    []*requestTestTimer
	scheduled int
}
type requestTestTimer struct {
	clock  *requestTestClock
	at     time.Time
	fn     func()
	active bool
}

func (c *requestTestClock) AfterFunc(d time.Duration, fn func()) requestTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &requestTestTimer{clock: c, at: c.Now().Add(d), fn: fn, active: true}
	c.timers = append(c.timers, timer)
	c.scheduled++
	return timer
}
func (t *requestTestTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	active := t.active
	t.active = false
	return active
}
func (c *requestTestClock) advance(d time.Duration) {
	c.mu.Lock()
	c.Clock.Advance(d)
	var due []func()
	for _, timer := range c.timers {
		if timer.active && !timer.at.After(c.Now()) {
			timer.active = false
			due = append(due, timer.fn)
		}
	}
	c.mu.Unlock()
	for _, fn := range due {
		fn()
	}
}
func (c *requestTestClock) counts() (scheduled, active int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, timer := range c.timers {
		if timer.active {
			active++
		}
	}
	return c.scheduled, active
}

// observeRequestSQLite uses the same real driver wrapper as store's native
// boundary tests. The returned stop waits for native cleanup before unhooking.
func observeRequestSQLite(t *testing.T, s *store.Store, hooks sqliteobserve.Hooks) func() {
	t.Helper()
	stop, err := store.ObserveSQLiteForTest(s, func(d driver.Driver) driver.Driver { return sqliteobserve.Wrap(d, hooks) })
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := stop(ctx); err != nil {
			t.Errorf("stop SQLite observer: %v", err)
		}
	}
	t.Cleanup(cleanup)
	return cleanup
}

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
	process, err := startProcess(runtime.Program, []string{"acp"}, t.TempDir(), os.Environ())
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
	c := newRequestController(e.rep, p, pane)
	t.Cleanup(c.close)
	return c
}

// permissionWakeFor captures one admitted job, just as its timer does. Tests
// dispatch only the named job; this helper never discovers retained requests.
func permissionWakeFor(r *pendingAuthorization) requestWake {
	return requestWake{storeID: r.storeID, token: r.token, binding: r.binding,
		generation: r.generation, kind: permissionWait, sequence: r.wait.sequence,
		deadline: r.wait.deadline}
}

func continuePermission(c *requestController, r *pendingAuthorization) error {
	_, err := c.continueRequest(permissionWakeFor(r))
	return err
}

func proofWakeFor(r *pendingAuthorization) requestWake {
	p := r.proof
	return requestWake{storeID: r.storeID, token: r.token, binding: p.binding,
		generation: p.event.Generation, kind: proofWait, sequence: r.proofWait.sequence,
		deadline: p.event.BusyUntil, proofID: p.event.ID}
}

func continueProof(c *requestController, r *pendingAuthorization) error {
	_, err := c.continueRequest(proofWakeFor(r))
	return err
}

func nextRequestWake(t *testing.T, c *requestController) requestWake {
	t.Helper()
	select {
	case wake := <-c.events:
		return wake
	case <-time.After(time.Second):
		t.Fatal("captured request timer/completion did not deliver its wake")
		return requestWake{}
	}
}

func (e *requestEnv) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", e.path+"?_pragma=busy_timeout(1000)")
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

// startTurn opens a turn with the gateway's UserPromptSubmit and returns its
// gate, so a later Stop can reuse it.
func (e *requestEnv) startTurn(t *testing.T) store.HookGate {
	t.Helper()
	gate := store.HookGate{Event: "UserPromptSubmit", ParentPID: e.binding.GatewayPID, ParentStart: e.binding.GatewayStart, SessionID: e.binding.SessionID, Cursor: &e.binding}
	if _, res, err := e.s.ApplyHookTransitionResult(e.binding.InstanceID, gate, store.StateWorking, false, "UserPromptSubmit", e.tr.path, true); err != nil || !res.Applied {
		t.Fatalf("turn start %+v %v", res, err)
	}
	return gate
}
func (e *requestEnv) state(t *testing.T) string {
	t.Helper()
	row, err := e.s.GetSpawn(e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	return row.State
}
func (e *requestEnv) wantState(t *testing.T, step, want string) {
	t.Helper()
	if got := e.state(t); got != want {
		t.Fatalf("%s: state %s, want %s", step, got, want)
	}
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

// refuseProvenDecision preserves a retired request while exercising the public caller.
func (e *requestEnv) refuseProvenDecision(t *testing.T, token, decision string) {
	t.Helper()
	before := e.request(t, token)
	zero := int64(0)
	_, err := api.Decide(e.s, api.RelayView{}, api.DecideParams{InstanceID: e.binding.InstanceID, RequestToken: token, Decision: decision, MaxWaitMs: &zero})
	if !errors.Is(err, api.ErrNoOpenPermissionRequest) {
		t.Fatalf("late decision: %v", err)
	}
	if got := e.request(t, token); got != before {
		t.Fatalf("refused decision changed proven request: before=%+v after=%+v", before, got)
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

// observeQueuedRequest wraps completion while blockProtocol holds the writer.
// Initial guard refusal calls done without publishing to protocol.writes.
func observeQueuedRequest(t *testing.T, p *protocol, wrapIssue ...func(func() error) func() error) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	select {
	case next := <-p.outgoing:
		if len(wrapIssue) != 0 {
			next.issue = wrapIssue[0](next.issue)
		}
		original := next.done
		next.done = func(err error) {
			if original != nil {
				original(err)
			}
			done <- err
		}
		p.outgoing <- next
	case <-time.After(time.Second):
		t.Fatal("request was not queued behind barrier")
	}
	return done
}

func requestCompletion(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(4 * time.Second):
		t.Fatal("request completion missing")
		return nil
	}
}

// observedWire allows a committed read before the first response byte is accepted.
type observedWire struct {
	mu      sync.Mutex
	raw     []byte
	first   func() error
	observe func([]byte)
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
	if w.observe != nil {
		w.observe(p)
	}
	return len(p), nil
}
func (w *observedWire) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.raw...)
}
