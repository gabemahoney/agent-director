package api_test

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
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
	sent bool
	read func(api.Spawn) (api.Spawn, error)
	get  func(api.Spawn) api.Spawn
}

func (s *nativeSubmissionStore) GetSpawn(id string) (api.Spawn, error) {
	row, err := s.SendKeysStore.GetSpawn(id)
	if err == nil && s.get != nil {
		row = s.get(row)
	}
	return row, err
}

func (s *nativeSubmissionStore) GetSpawnWithin(id string, wait time.Duration) (api.Spawn, error) {
	row, err := s.SendKeysStore.GetSpawnWithin(id, wait)
	if err == nil && s.sent && s.read != nil {
		return s.read(row)
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
	e.s = &nativeSubmissionStore{SendKeysStore: e.store}
	e.rec.AfterCall(tmux.CallSendKey, func(c tmuxfix.SocketCall, _ error) {
		if c.Key == "Enter" {
			e.s.sent = true
		}
	})
	e.env = api.SendKeysEnv{Input: e.b, Relay: api.RelayView{Procs: e.pc, Now: e.clock.Now, Window: sendKeysWindow()}, Sleep: func(d time.Duration) { e.slept += d; e.clock.Advance(d) }}
	return e
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
	var joined string
	for _, c := range e.rec.SocketCallsOf(tmux.CallSendText) {
		if c.Target != r.Spawn.Identity.PaneID || c.Socket != r.Socket || c.PressEnter || len(c.Text) > 512 {
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
