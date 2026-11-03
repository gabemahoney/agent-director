package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/inputdelivery"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/google/uuid"
)

// deliveryBackend replaces native process/evidence I/O; store and tmux guards stay real.
type deliveryBackend struct {
	mu      sync.Mutex
	locked  bool
	current *api.InputDeliveryAttempt
	history map[string]*api.InputDeliveryAttempt
	ready   func() (bool, error)
	queue   func() (int, error)
	confirm func(*api.InputDeliveryAttempt) (*api.InputDeliveryProof, error)
	save    func(*api.InputDeliveryAttempt) error
}

func cloneAttempt(a *api.InputDeliveryAttempt) *api.InputDeliveryAttempt {
	if a == nil {
		return nil
	}
	b, _ := json.Marshal(a)
	var copy api.InputDeliveryAttempt
	if err := json.Unmarshal(b, &copy); err != nil {
		panic(err)
	}
	return &copy
}
func (b *deliveryBackend) Open(api.InputDeliveryIdentity) (api.InputDeliverySession, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.locked {
		return nil, fmt.Errorf("writer already held")
	}
	b.locked = true
	return &deliverySession{b}, nil
}

type deliverySession struct{ b *deliveryBackend }

func (s *deliverySession) Close() error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	s.b.locked = false
	return nil
}
func (s *deliverySession) Load() (*api.InputDeliveryAttempt, error) {
	return cloneAttempt(s.b.current), nil
}
func (s *deliverySession) LoadRequest(id string) (*api.InputDeliveryAttempt, error) {
	return cloneAttempt(s.b.history[id]), nil
}
func (s *deliverySession) Save(a *api.InputDeliveryAttempt) error {
	if s.b.save != nil {
		if err := s.b.save(a); err != nil {
			return err
		}
	}
	if s.b.history == nil {
		s.b.history = map[string]*api.InputDeliveryAttempt{}
	}
	s.b.current, s.b.history[a.RequestID] = cloneAttempt(a), cloneAttempt(a)
	return nil
}
func (s *deliverySession) Ready(api.InputDeliveryIdentity) (bool, error) {
	if s.b.ready != nil {
		return s.b.ready()
	}
	return true, nil
}
func (s *deliverySession) Queue(api.InputDeliveryIdentity) (int, error) {
	if s.b.queue != nil {
		return s.b.queue()
	}
	return 0, nil
}
func (s *deliverySession) Watermark(api.InputDeliveryIdentity) (map[string]api.InputDeliveryWatermark, error) {
	return map[string]api.InputDeliveryWatermark{"native": {Size: 10, PrefixSHA256: "prior"}}, nil
}
func (s *deliverySession) Confirm(a *api.InputDeliveryAttempt) (*api.InputDeliveryProof, error) {
	if s.b.confirm != nil {
		return s.b.confirm(a)
	}
	return &api.InputDeliveryProof{SessionID: a.NativeSessionID, RecordID: "new-user-record", RawSHA256: "record-hash"}, nil
}

type nativeSubmissionStore struct {
	api.SendKeysStore
	inputdelivery.Persistence
	sent bool
	read func(api.Spawn) (api.Spawn, error)
	get  func(api.Spawn) api.Spawn
	// reread injects a change at pre-input revalidation, leaving post-Enter reads separate.
	reread func(api.Spawn) (api.Spawn, error)
}

func (s *nativeSubmissionStore) GetSpawn(id string) (api.Spawn, error) {
	row, err := s.SendKeysStore.GetSpawn(id)
	if err == nil && s.get != nil {
		row = s.get(row)
	}
	return row, err
}

func (s *nativeSubmissionStore) GetSpawnWithin(id string, _ time.Duration) (api.Spawn, error) {
	// The injected clock owns poll deadlines; SQL reads still fetch fresh state.
	row, err := s.SendKeysStore.GetSpawn(id)
	if err == nil && s.sent && s.read != nil {
		return s.read(row)
	}
	if err == nil && !s.sent && s.reread != nil {
		return s.reread(row)
	}
	return row, err
}

type nativeSubmissionEnv struct {
	*killEnv
	s     *nativeSubmissionStore
	b     *deliveryBackend
	env   api.SendKeysEnv
	slept time.Duration
}

func newNativeSubmissionEnv(t *testing.T) *nativeSubmissionEnv {
	t.Helper()
	e := &nativeSubmissionEnv{killEnv: newKillEnv(t), b: &deliveryBackend{}}
	e.rec.WithVirtualTime(e.clock, tmux.Timeouts{Query: time.Nanosecond, Action: time.Nanosecond})
	e.s = &nativeSubmissionStore{SendKeysStore: e.store, Persistence: e.store}
	// Codex fallback's guarded paste uses the transport Enter call; other input a separate Enter key.
	e.rec.AfterCall(tmuxfix.AnyCall, func(c tmuxfix.SocketCall, err error) {
		if err == nil && (c.Call == tmux.CallSendEnter || c.Call == tmux.CallSendKey && c.Key == "Enter") {
			e.s.sent = true
		}
	})
	e.env = api.SendKeysEnv{Input: e.b, Relay: api.RelayView{Procs: e.pc, Now: e.clock.Now, Window: sendKeysWindow()}, Sleep: func(d time.Duration) { e.slept += d; e.clock.Advance(d) }}
	return e
}

// sqliteSubmission uses the production Native session and SQLite, replacing only
// the terminal ioctl queue probe. Logs, proof files, guards and commits stay real.
// SQLite-backed native tests must reuse it and sqliteDeliveryBackend's fault seams.
func sqliteSubmission(t *testing.T, relay ...bool) (*nativeSubmissionEnv, killRow, inputdelivery.Launch) {
	t.Helper()
	e := newNativeSubmissionEnv(t)
	e.clock.Advance(time.Now().Sub(e.clock.Now()))
	r := e.seedRow(t, killRowSpec{State: store.StatePending, SessionID: uuid.NewString(), CWD: t.TempDir(), RelayOn: len(relay) > 0 && relay[0]})
	return e, r, e.enableSQLiteSubmission(t, r, r.Spawn.SessionID)
}

// enableSQLiteSubmission must be reused when a native case needs an existing
// row (including an initial report-in). It retains sqliteSubmission's capability.
func (e *nativeSubmissionEnv) enableSQLiteSubmission(t *testing.T, r killRow, sessionID string) inputdelivery.Launch {
	t.Helper()
	id := sqliteInputIdentity(r.Spawn, e.storeID)
	id.PaneID, id.PID, id.Starttime = "", 0, ""
	dir := t.TempDir()
	l := inputdelivery.Launch{Identity: id, Revision: 1, EvidenceDir: dir, Capability: inputdelivery.Capability{
		Agent: id.Agent, Version: "2.1.280", SessionID: sessionID, CWD: id.CWD, Socket: id.Socket,
		Token: id.Token, InstanceID: id.InstanceID, Transcript: filepath.Join(dir, "native.jsonl"), Created: time.Now().UTC(), Safe: true,
	}}
	if err := e.st.CreateInputLaunch(context.Background(), l); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "claude-debug.log"), []byte("Terminal capabilities:"), 0600); err != nil {
		t.Fatal(err)
	}
	e.env.Input = &sqliteDeliveryBackend{e: e, ctx: context.Background()}
	return l
}

func sqliteInputIdentity(row api.Spawn, storeID string) inputdelivery.Identity {
	i := row.Identity
	return inputdelivery.Identity{StoreID: storeID, Token: i.Token, InstanceID: row.InstanceID, Socket: i.Socket,
		PaneID: i.PaneID, PID: i.PanePID, Starttime: i.PaneStarttime, Agent: row.Agent, CWD: row.CWD, LifeNumber: row.LifeNumber}
}

func sqliteGuard(row api.Spawn, storeID string) inputdelivery.Guard {
	return inputdelivery.Guard{Identity: sqliteInputIdentity(row, storeID), RowVersion: row.RowVersion, PermissionEpoch: row.PermissionEpoch, SessionID: row.SessionID, State: row.State}
}

// sqliteDeliveryBackend must be reused for SQLite-backed native tests; it keeps
// real admission and persistence while exposing optional I/O and fault seams.
type sqliteDeliveryBackend struct {
	e           *nativeSubmissionEnv
	ctx         context.Context
	afterSave   func(*api.InputDeliveryAttempt) error
	afterClose  func() error
	limit       func(time.Time) time.Time
	beforeCheck func()
}

func (b *sqliteDeliveryBackend) Open(id api.InputDeliveryIdentity) (api.InputDeliverySession, error) {
	row, err := b.e.st.GetSpawn(id.InstanceID)
	if err != nil {
		return nil, err
	}
	g := inputdelivery.Guard{Identity: id, RowVersion: row.RowVersion, PermissionEpoch: row.PermissionEpoch, SessionID: row.SessionID, State: row.State}
	s, err := (inputdelivery.Native{Store: b.e.store, Context: b.ctx, Guard: g}).Open(id)
	if err != nil {
		return nil, err
	}
	return &sqliteDeliverySession{InputDeliverySession: s, afterSave: b.afterSave, afterClose: b.afterClose, limit: b.limit, beforeCheck: b.beforeCheck}, nil
}

type sqliteDeliverySession struct {
	api.InputDeliverySession
	afterSave   func(*api.InputDeliveryAttempt) error
	afterClose  func() error
	limit       func(time.Time) time.Time
	beforeCheck func()
}

// Close preserves production cleanup, then optionally loses its acknowledgement.
func (s *sqliteDeliverySession) Close() error {
	if err := s.InputDeliverySession.Close(); err != nil {
		return err
	}
	if s.afterClose != nil {
		return s.afterClose()
	}
	return nil
}

func (s *sqliteDeliverySession) Queue(api.InputDeliveryIdentity) (int, error) { return 0, nil }
func (s *sqliteDeliverySession) Check() error {
	if s.beforeCheck != nil {
		s.beforeCheck()
	}
	return s.InputDeliverySession.(interface{ Check() error }).Check()
}

// sqliteAdmissionWaitContext observes the actual held operation lane: Done is
// first read to select the context-aware path, then to wait for the occupied slot.
// It preserves the caller's deadline and cancellation channel without sleeps.
type sqliteAdmissionWaitContext struct {
	context.Context
	armed  atomic.Bool
	looks  atomic.Int32
	onWait func()
}

func (c *sqliteAdmissionWaitContext) Done() <-chan struct{} {
	if c.armed.Load() && c.looks.Add(1) == 2 {
		c.onWait()
	}
	return c.Context.Done()
}
func (s *sqliteDeliverySession) LimitTo(t time.Time) {
	if s.limit != nil {
		t = s.limit(t)
	}
	s.InputDeliverySession.(interface{ LimitTo(time.Time) }).LimitTo(t)
}
func (s *sqliteDeliverySession) BindIdentity(id api.InputDeliveryIdentity, adopted bool) error {
	return s.InputDeliverySession.(interface {
		BindIdentity(api.InputDeliveryIdentity, bool) error
	}).BindIdentity(id, adopted)
}
func (s *sqliteDeliverySession) Admit(g inputdelivery.Guard, target api.InputDeliveryIdentity) error {
	return s.InputDeliverySession.(interface {
		Admit(inputdelivery.Guard, api.InputDeliveryIdentity) error
	}).Admit(g, target)
}
func (s *sqliteDeliverySession) Manual() { s.InputDeliverySession.(interface{ Manual() }).Manual() }
func (s *sqliteDeliverySession) Supports(id api.InputDeliveryIdentity) bool {
	return s.InputDeliverySession.(interface {
		Supports(api.InputDeliveryIdentity) bool
	}).Supports(id)
}
func (s *sqliteDeliverySession) Save(a *api.InputDeliveryAttempt) error {
	if err := s.InputDeliverySession.Save(a); err != nil {
		return err
	}
	if s.afterSave != nil {
		return s.afterSave(a)
	}
	return nil
}

func sqliteAttempt(t *testing.T, e *nativeSubmissionEnv, r killRow) (*inputdelivery.Launch, *api.InputDeliveryAttempt) {
	t.Helper()
	l, err := e.st.ReadInputLaunch(context.Background(), r.Token)
	if err != nil || l == nil {
		t.Fatalf("launch=%+v %v", l, err)
	}
	a, err := e.st.ReadInputAttempt(context.Background(), *l, l.CurrentRequestID)
	if err != nil {
		t.Fatal(err)
	}
	return l, a
}

// sqliteProofRecord retains noncanonical JSON whitespace and literal HTML bytes.
func sqliteProofRecord(t *testing.T, l inputdelivery.Launch, payload string) []byte {
	t.Helper()
	text, _ := json.Marshal(payload)
	return []byte(fmt.Sprintf(" \t{ \"type\":\"user\", \"uuid\":\"cde93519-6972-4b1b-a650-830a0042e876\", \"sessionId\":%q, \"cwd\":%q, \"version\":\"2.1.280\", \"message\":{\"role\":\"user\",\"content\":%s} } \t", l.Capability.SessionID, l.Identity.CWD, text))
}
func (e *nativeSubmissionEnv) send(p api.SendKeysParams) (api.SendKeysResult, error) {
	return api.SendKeys(e.s, e.rec, e.env, p)
}

func assertDeliveryError(t *testing.T, err error, outcome string, bytes int, enter bool) api.SubmissionUnconfirmedDetails {
	t.Helper()
	assertOneName(t, err, "ErrSubmissionUnconfirmed")
	d, ok := api.ErrDetails(err).(api.SubmissionUnconfirmedDetails)
	if !ok || d.Outcome != outcome || d.BytesAttempted != bytes || d.EnterMayHaveBeenSent != enter || d.SubmissionConfirmed {
		t.Fatalf("delivery error details=%+v, err=%v; want outcome=%s bytes=%d enter=%v", api.ErrDetails(err), err, outcome, bytes, enter)
	}
	return d
}
func assertOrdinaryWrites(t *testing.T, e *killEnv, r killRow, text string) {
	t.Helper()
	for _, c := range e.rec.SocketCallsOf(tmux.CallSendText) {
		if c.Paste || len(c.Text) > 512 {
			t.Fatalf("unsafe native chunk: %+v", c)
		}
	}
	assertInputWrites(t, e, r, text)
}

// assertInputWrites checks exact bytes/target/one Enter without depending on
// the number of authority reads or whether tmux spells Enter as a key call.
func assertInputWrites(t *testing.T, e *killEnv, r killRow, text string) {
	t.Helper()
	var joined string
	for _, c := range e.rec.SocketCallsOf(tmux.CallSendText) {
		if c.Target != r.Spawn.Identity.PaneID || c.Socket != r.Socket || c.PressEnter {
			t.Fatalf("unsafe text write %+v", c)
		}
		joined += c.Text
	}
	if joined != text {
		t.Fatalf("text writes=%q, want %q", joined, text)
	}
	enters := ordinaryEnters(e)
	if len(enters) != 1 || enters[0].Socket != r.Socket || enters[0].Target != r.Spawn.Identity.PaneID {
		t.Fatalf("Enter calls=%+v; want one to the owned pane", enters)
	}
	e.assertNoCalls(t, tmux.CallCapture)
}

func ordinaryEnters(e *killEnv) []tmuxfix.SocketCall {
	var result []tmuxfix.SocketCall
	for _, c := range e.rec.SocketCalls() {
		if c.Call == tmux.CallSendEnter || c.Call == tmux.CallSendKey && c.Key == "Enter" {
			result = append(result, c)
		}
	}
	return result
}

// submissionStore injects hook/read races only after the keys have been sent.
// Its Persistence keeps send-keys' real SQLite input journal and admission;
// preparation records the read budgets before input, waits those after it.
type submissionStore struct {
	api.SendKeysStore
	inputdelivery.Persistence
	sent         bool
	reads        int
	read         func(int, api.Spawn) (api.Spawn, error)
	waits        []time.Duration
	preparation  []time.Duration
	boundedReads bool
}

func (s *submissionStore) GetSpawnWithin(id string, maxWait time.Duration) (api.Spawn, error) {
	if s.sent {
		s.waits = append(s.waits, maxWait)
	} else {
		s.preparation = append(s.preparation, maxWait)
	}
	// Virtual poll budgets must not become real SQL deadlines in semantic tests.
	read := s.SendKeysStore.GetSpawn
	if s.boundedReads {
		read = func(id string) (api.Spawn, error) { return s.SendKeysStore.GetSpawnWithin(id, maxWait) }
	}
	row, err := read(id)
	if err != nil || !s.sent {
		return row, err
	}
	s.reads++
	if s.read != nil {
		return s.read(s.reads, row)
	}
	return row, nil
}

// submissionEnv extends the shared real-store fixture with an advancing poll clock.
type submissionEnv struct {
	*killEnv
	t     *testing.T
	s     *submissionStore
	env   api.SendKeysEnv
	slept time.Duration
	waits int
}

func newSubmissionEnv(t *testing.T) *submissionEnv {
	t.Helper()
	e := &submissionEnv{killEnv: newKillEnv(t), t: t}
	e.s = &submissionStore{SendKeysStore: e.store, Persistence: e.st}
	// Text marks the send; Enter-only input has no preceding text event.
	e.rec.AfterCall(tmuxfix.AnyCall, func(c tmuxfix.SocketCall, _ error) {
		if c.Call == tmux.CallSendText || c.Call == tmux.CallSendEnter || c.Call == tmux.CallSendKey && c.Key == "Enter" {
			e.s.sent = true
		}
	})
	e.env = api.SendKeysEnv{
		Relay: api.RelayView{Procs: e.pc, Now: e.clock.Now, Window: sendKeysWindow()},
		Sleep: func(d time.Duration) {
			// b.qcf: readiness has its own 12s budget; these counters cover only post-Enter observation.
			if !e.s.sent {
				e.clock.Advance(d)
				return
			}
			e.waits++
			e.slept += d
			if d <= 0 || d > 100*time.Millisecond || e.waits > 20 {
				t.Fatalf("submission wait %d = %v; want at most twenty positive waits <= 100ms", e.waits, d)
			}
			e.clock.Advance(d)
		},
	}
	return e
}

func (e *submissionEnv) send(p api.SendKeysParams) error {
	// b.qcf: hook-observation tests first let pending Claude reach its composer.
	row, err := e.st.GetSpawn(p.InstanceID)
	if err != nil {
		e.t.Fatal(err)
	}
	if row.Agent == store.AgentClaude && row.State == store.StatePending && p.Text != "" && !p.NoEnter && p.Key == "" && p.ExpectPaneSHA256 == "" {
		e.sessionStartsWhenPolled(e.t, killRow{ID: row.InstanceID, Socket: row.Identity.Socket}, nil)
	}
	_, err = api.SendKeys(e.s, e.rec, e.env, p)
	return err
}
