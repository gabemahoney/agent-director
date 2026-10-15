package api_test

// Pending Codex submission uses raw same-launch turn activity, never pane text.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// submissionStore injects hook/read races only after the keys have been sent.
type submissionStore struct {
	api.SendKeysStore
	sent  bool
	reads int
	read  func(int, api.Spawn) (api.Spawn, error)
	waits []time.Duration
}

func (s *submissionStore) GetSpawnWithin(id string, maxWait time.Duration) (api.Spawn, error) {
	s.waits = append(s.waits, maxWait)
	row, err := s.SendKeysStore.GetSpawnWithin(id, maxWait)
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
	s     *submissionStore
	env   api.SendKeysEnv
	slept time.Duration
	waits int
}

func newSubmissionEnv(t *testing.T) *submissionEnv {
	t.Helper()
	e := &submissionEnv{killEnv: newKillEnv(t)}
	e.s = &submissionStore{SendKeysStore: e.store}
	e.rec.AfterCall(tmux.CallSendEnter, func(tmuxfix.SocketCall, error) { e.s.sent = true })
	e.env = api.SendKeysEnv{
		Relay: api.RelayView{Procs: e.pc, Now: e.clock.Now, Window: sendKeysWindow()},
		Sleep: func(d time.Duration) {
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
	_, err := api.SendKeys(e.s, e.rec, e.env, p)
	return err
}

func assertSubmissionUnconfirmed(t *testing.T, err error, state string) {
	t.Helper()
	assertOneName(t, err, "ErrSubmissionUnconfirmed")
	want := api.SubmissionUnconfirmedDetails{KeysSent: true, SubmissionConfirmed: false, RowState: state}
	if got := api.ErrDetails(err); got != want {
		t.Errorf("details = %+v; want %+v", got, want)
	}
	b, marshalErr := json.Marshal(api.ErrDetails(err))
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"keys_sent": true, "submission_confirmed": false, "row_state": state}; !reflect.DeepEqual(got, want) {
		t.Errorf("wire details = %s; want %+v", b, want)
	}
}

func TestSendKeysCodexSubmissionUnconfirmed(t *testing.T) {
	t.Parallel()
	for _, state := range []string{store.StatePending, store.StateWaiting} {
		for _, text := range []string{skpText, ""} {
			t.Run(state+"/text="+text, func(t *testing.T) {
				t.Parallel()
				e := newSubmissionEnv(t)
				r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
				before := e.columns(t, r.ID)
				e.s.read = func(_ int, row api.Spawn) (api.Spawn, error) { row.State = state; return row, nil }

				err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: text, AllowPending: true})

				assertSubmissionUnconfirmed(t, err, state)
				if e.slept != 2*time.Second {
					t.Errorf("wait = %v; want 2s", e.slept)
				}
				if e.s.reads != 20 {
					t.Errorf("submission reads = %d; want 20 before the deadline", e.s.reads)
				}
				if text == "" {
					e.assertEnterOnly(t, r.Socket, r.Spawn.Identity.PaneID)
				} else {
					e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, text)
				}
				e.assertNoCalls(t, tmux.CallCapture)
				e.assertRowUnchanged(t, r.ID, before)
			})
		}
	}
}

func TestSendKeysCodexSubmissionActivity(t *testing.T) {
	t.Parallel()
	for _, state := range []string{store.StateWorking, store.StateAskUser, store.StateCheckPermission} {
		for _, delay := range []int{0, 2} {
			t.Run(fmt.Sprintf("%s/after %d waits", state, delay), func(t *testing.T) {
				t.Parallel()
				e := newSubmissionEnv(t)
				r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
				e.s.read = func(n int, row api.Spawn) (api.Spawn, error) {
					row.State = store.StateWaiting
					if n > delay {
						row.State = state
					}
					return row, nil
				}
				if err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true}); err != nil {
					t.Fatalf("same-launch activity did not confirm: %v", err)
				}
				if e.waits != delay {
					t.Errorf("waits = %d; want %d", e.waits, delay)
				}
				e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, skpText)
				e.assertNoCalls(t, tmux.CallCapture)
			})
		}
	}
}

func TestSendKeysCodexSubmissionEvidenceLost(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, state string
		change      func(*api.Spawn)
		err         error
		failAt      int
	}{
		{name: "ended", state: store.StateEnded},
		{name: "missing", state: store.StateMissing},
		{name: "new life working", state: store.StateWorking, change: func(row *api.Spawn) { row.LifeNumber++ }},
		{name: "new token working", state: store.StateWorking, change: func(row *api.Spawn) { row.Identity.Token = newToken() }},
		{name: "read failed", state: store.StatePending, err: fmt.Errorf("read evidence: %w", api.ErrStoreBusy)},
		{name: "row deleted", state: store.StatePending, err: api.ErrSpawnNotFound},
		{name: "read failed after waiting", state: store.StateWaiting, err: api.ErrStoreBusy, failAt: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newSubmissionEnv(t)
			r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
			e.s.read = func(n int, row api.Spawn) (api.Spawn, error) {
				row.State = tc.state
				if tc.change != nil {
					tc.change(&row)
				}
				if tc.failAt > n {
					return row, nil
				}
				return row, tc.err
			}
			err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
			assertSubmissionUnconfirmed(t, err, tc.state)
			wantWaits := max(0, tc.failAt-1)
			if e.waits != wantWaits {
				t.Errorf("waits = %d; want %d before evidence was lost", e.waits, wantWaits)
			}
			e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, skpText)
		})
	}
}

func TestSendKeysCodexSubmissionBoundedClock(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"fixed clock", "deadline reached during wait", "deadline passed during wait"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			e := newSubmissionEnv(t)
			r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
			sleep := e.env.Sleep
			if mode == "fixed clock" {
				now := e.clock.Now()
				e.env.Relay.Now = func() time.Time { return now }
			} else {
				extra := 3 * time.Second
				if mode == "deadline reached during wait" {
					extra = 2*time.Second - 100*time.Millisecond
				}
				e.env.Sleep = func(d time.Duration) { sleep(d); e.clock.Advance(extra) }
				e.s.read = func(n int, row api.Spawn) (api.Spawn, error) {
					if n > 1 {
						row.State = store.StateWorking
					}
					return row, nil
				}
			}
			err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
			assertSubmissionUnconfirmed(t, err, store.StatePending)
			if mode == "fixed clock" && (e.s.reads > 21 || e.waits > 20) {
				t.Errorf("reads/waits = %d/%d; want bounded by 21/20", e.s.reads, e.waits)
			}
			if mode != "fixed clock" && (e.waits != 1 || e.s.reads != 1) {
				t.Errorf("reads/waits = %d/%d; want 1/1, no read at or after the deadline", e.s.reads, e.waits)
			}
		})
	}
}

func TestSendKeysCodexSubmissionRemainingReadBudget(t *testing.T) {
	t.Parallel()
	e := newSubmissionEnv(t)
	r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
	e.s.read = func(_ int, row api.Spawn) (api.Spawn, error) {
		e.clock.Advance(650 * time.Millisecond)
		return row, nil
	}
	err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
	assertSubmissionUnconfirmed(t, err, store.StatePending)
	want := []time.Duration{2 * time.Second, 1250 * time.Millisecond, 500 * time.Millisecond}
	if !reflect.DeepEqual(e.s.waits, want) {
		t.Errorf("submission read budgets = %v; want %v including time spent reading and sleeping", e.s.waits, want)
	}
	e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, skpText)
}

func TestSendKeysCodexSubmissionActivityReadDeadline(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		readTime time.Duration
		confirm  bool
	}{
		{"just before deadline", 2*time.Second - time.Nanosecond, true},
		{"at deadline", 2 * time.Second, false},
		{"after deadline", 2*time.Second + time.Nanosecond, false},
	}
	for _, state := range []string{store.StateWorking, store.StateAskUser, store.StateCheckPermission} {
		for _, tc := range cases {
			t.Run(state+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				e := newSubmissionEnv(t)
				r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
				e.s.read = func(_ int, row api.Spawn) (api.Spawn, error) {
					e.clock.Advance(tc.readTime)
					row.State = state
					return row, nil
				}

				err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})

				if tc.confirm {
					if err != nil {
						t.Fatalf("activity read before deadline did not confirm: %v", err)
					}
				} else {
					assertSubmissionUnconfirmed(t, err, state)
				}
				if e.s.reads != 1 || e.waits != 0 {
					t.Errorf("submission reads/waits = %d/%d; want 1/0", e.s.reads, e.waits)
				}
				e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, skpText)
			})
		}
	}
}

func TestSendKeysCodexSubmissionConnectionContention(t *testing.T) {
	t.Parallel()
	e := newSubmissionEnv(t)
	r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
	before := e.columns(t, r.ID)
	free, held, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	holderStarted := false
	release := func() { once.Do(func() { close(free) }) }
	watchdog := time.AfterFunc(5*time.Second, release)
	t.Cleanup(func() { watchdog.Stop(); release() })
	e.s.read = func(_ int, row api.Spawn) (api.Spawn, error) {
		e.clock.Advance(1850 * time.Millisecond)
		return row, nil
	}
	sleep := e.env.Sleep
	e.env.Sleep = func(d time.Duration) {
		sleep(d)
		if holderStarted {
			return
		}
		holderStarted = true
		// The transaction's precondition holds the real store connection without writing.
		go func() {
			_, _, _, err := e.st.AckRelayDecision(r.ID, "absent-request", e.clock.Now(), store.DefaultLockWait, func() error {
				close(held)
				<-free
				return nil
			})
			done <- err
		}()
		select {
		case <-held:
		case err := <-done:
			t.Fatalf("hold store connection: %v", err)
		}
	}

	start := time.Now()
	err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
	elapsed := time.Since(start)
	release()
	if holderStarted {
		if holdErr := <-done; holdErr != nil {
			t.Fatalf("release store connection: %v", holdErr)
		}
	}
	assertSubmissionUnconfirmed(t, err, store.StatePending)
	if !strings.Contains(err.Error(), "ErrStoreBusy") {
		t.Errorf("err = %v; want the bounded connection wait to explain the unconfirmed submission", err)
	}
	if elapsed > time.Second {
		t.Errorf("submission check took %v; want the remaining 50ms wait with scheduling allowance", elapsed)
	}
	if want := []time.Duration{2 * time.Second, 50 * time.Millisecond}; !reflect.DeepEqual(e.s.waits, want) {
		t.Errorf("submission read budgets = %v; want %v", e.s.waits, want)
	}
	e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, skpText)
	e.assertNoCalls(t, tmux.CallCapture)
	e.assertRowUnchanged(t, r.ID, before)
}

func TestSendKeysSubmissionApplicability(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		spec   killRowSpec
		params api.SendKeysParams
	}{
		{"pending Claude", killRowSpec{State: store.StatePending}, api.SendKeysParams{Text: skpText}},
		{"waiting Codex", codexSpec(store.StateWaiting, agentAlive, false), api.SendKeysParams{Text: skpText}},
		{"working Codex", codexSpec(store.StateWorking, agentAlive, false), api.SendKeysParams{Text: skpText}},
		{"pending Codex no_enter", codexSpec(store.StatePending, agentAlive, false), api.SendKeysParams{Text: skpText, NoEnter: true}},
		{"pending Codex named key", codexSpec(store.StatePending, agentAlive, false), api.SendKeysParams{Key: "Enter"}},
		{"pending Codex character key", codexSpec(store.StatePending, agentAlive, false), api.SendKeysParams{Key: "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newSubmissionEnv(t)
			r := e.seedRow(t, tc.spec)
			p := tc.params
			p.InstanceID, p.AllowPending = r.ID, true
			if err := e.send(p); err != nil {
				t.Fatal(err)
			}
			if e.s.reads != 0 || e.waits != 0 {
				t.Errorf("submission reads/waits = %d/%d; want none", e.s.reads, e.waits)
			}
		})
	}
}

func TestSendKeysCodexSubmissionTmuxFailurePrecedence(t *testing.T) {
	t.Parallel()
	for _, call := range []tmux.Call{tmux.CallSendText, tmux.CallSendEnter} {
		t.Run(string(call), func(t *testing.T) {
			t.Parallel()
			e := newSubmissionEnv(t)
			r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout}, call)
			err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
			assertOneName(t, err, "ErrTmuxUnresponsive")
			if e.s.reads != 0 || e.waits != 0 || api.ErrDetails(err) != nil {
				t.Errorf("submission reads/waits/details = %d/%d/%+v; want none after tmux failure", e.s.reads, e.waits, api.ErrDetails(err))
			}
		})
	}
}

// E10: "check hook-derived get or status" after keys were accepted.
func TestAdviceFollow_E10_SubmissionUnconfirmedInspectGet(t *testing.T) {
	t.Parallel()
	adviceAssertManifest(t, "send-keys", "", "Do not repeat text or Enter automatically after that error: check hook-derived get or status; if submission remains unknown, escalate to the caller or a human.")
	e := newSubmissionEnv(t)
	r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
	err := e.send(api.SendKeysParams{InstanceID: r.ID, Text: skpText, AllowPending: true})
	assertSubmissionUnconfirmed(t, err, store.StatePending)
	adviceAssertAdvice(t, err, api.ErrSubmissionUnconfirmed, "check hook-derived get or status; if submission remains unknown, escalate to the caller or a human")
	row, err := api.Get(e.st, e.env.Relay, r.ID)
	if err != nil || row.State != store.StatePending {
		t.Fatalf("Get after unconfirmed = %+v, %v; want pending, no error", row, err)
	}
	e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, skpText)
}
