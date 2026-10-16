package api_test

// decide_delivery_test.go — decide on a request recorded from schema v7 on
// (b.146 decision 1 A, rules 6, 8, 15, 16, decision 9 B): it reports delivery
// after a wait of at most 1 s for the relay hook's ack, refuses a fallen-back
// request at once, keeping the verdict as attempted, and with max_wait_ms
// never waits past its bound: before the verdict commits ErrStoreBusy with
// nothing recorded, after it not_confirmed. Seeds: relay_delivery_test.go's
// relayEnv.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gabemahoney/agent-director/internal/trail"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// decideWith runs decide on e's request A with decision and maxWaitMs (nil:
// no bound) on e's clock, its waits advancing that clock and, at each, running
// atSleep; it returns the result, the total wait and the error.
func (e *relayEnv) decideWith(t *testing.T, s api.DecideStore, decision string, maxWaitMs *int64, atSleep func()) (api.DecideResult, time.Duration, error) {
	t.Helper()
	var slept time.Duration
	res, err := api.DecideWithSleep(s, e.view(), func(d time.Duration) {
		if atSleep != nil {
			atSleep()
		}
		slept += d
		e.now = e.now.Add(d)
	}, api.DecideParams{InstanceID: e.id, RequestToken: storefix.TestRequestTokenA, Decision: decision, MaxWaitMs: maxWaitMs})
	return res, slept, err
}

// TestDecideReportsDelivery (decision 1 A, rule 16): decide records the
// verdict on a request whose hook is not fallen back, then waits at most 1 s
// (or to its max_wait_ms bound) for the hook's ack: delivered when the hook
// acks within it, else not_confirmed with confirm_by, the request's settle
// instant, a hook that exits during the wait without an ack included (never
// fallen_back once the verdict is recorded).
func TestDecideReportsDelivery(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		ns        string // the reader's pid namespace
		ack       bool   // the hook acks at decide's first wait
		gone      bool   // the hook exits without an ack at decide's first wait
		maxWaitMs *int64
		want      string
		alive     string
		waited    time.Duration // decide's whole wait
	}{
		{"the hook acks within the wait", relayNS, true, false, nil, api.DeliveryDelivered, "true", 50 * time.Millisecond},
		{"no ack within 1 s", relayNS, false, false, nil, api.DeliveryNotConfirmed, "true", time.Second},
		{"the hook exits during the wait without an ack", relayNS, false, true, nil, api.DeliveryNotConfirmed, "false", time.Second},
		{"the hook cannot be checked, before settled_at", "pid:[4026532000]", false, false, nil, api.DeliveryNotConfirmed, "null", time.Second},
		{"max_wait_ms reached during the wait", relayNS, false, false, ptr(int64(300)), api.DeliveryNotConfirmed, "true", 300 * time.Millisecond},
	}
	for _, agent := range relayAgents {
		for _, tc := range cases {
			t.Run(agentLabel(agent, tc.name), func(t *testing.T) {
				t.Parallel()
				e := newRelayEnvAgent(t, relayEnvHook, agent)
				e.ns = tc.ns
				acked := false
				var policy *decidePolicyStore
				call := func(atSleep func()) (api.DecideResult, time.Duration, error) {
					if tc.maxWaitMs != nil {
						policy = newDecidePolicyStore(t, e)
						return policy.decide(tc.maxWaitMs)
					}
					return e.decideWith(t, e.s, "allow", tc.maxWaitMs, atSleep)
				}
				res, waited, err := call(func() {
					if tc.gone {
						e.pc.Set(relayEnvHook.PID, procfix.Gone())
					}
					if tc.ack && !acked {
						acked = true
						if _, _, ok, err := e.s.AckRelayDecision(e.id, storefix.TestRequestTokenA, e.now, store.DefaultLockWait, nil); err != nil || !ok {
							t.Errorf("the hook's ack = %v, %v", ok, err)
						}
					}
				})

				if err != nil {
					t.Fatalf("decide: %v; want the verdict recorded", err)
				}
				if res.Delivery != tc.want || aliveIs(res.HookAlive) != tc.alive || !res.ConfirmBy.Equal(e.settled) || waited != tc.waited {
					t.Errorf("decide = delivery %q, hook_alive %s, confirm_by %v after waiting %v; want %q, %s, %v after %v",
						res.Delivery, aliveIs(res.HookAlive), res.ConfirmBy, waited, tc.want, tc.alive, e.settled, tc.waited)
				}
				var pr api.PermissionRow
				if policy != nil {
					pr, err = policy.GetPermissionRequest(e.id, storefix.TestRequestTokenA)
					if err != nil || policy.commits != 1 || len(policy.sleeps) != 6 {
						t.Fatalf("policy commits=%d sleeps=%v read error=%v", policy.commits, policy.sleeps, err)
					}
					for _, d := range policy.sleeps {
						if d != 50*time.Millisecond {
							t.Errorf("policy poll=%v; want 50ms", d)
						}
					}
				} else {
					pr = e.request(t)
				}
				if pr.Decision != "allow" {
					t.Errorf("request decision = %q; want allow recorded", pr.Decision)
				}
			})
		}
	}
}

// TestDecideRefusesFallenBackAtOnce (decision 1 A, rules 5, 8, 15): a request
// whose hook is gone, or cannot be checked past its settle instant, is refused
// with ErrRelayFallenBack at once (no wait, long before its relay window ends),
// nothing is recorded as its decision, and the verdict is kept as
// attempted_decision / attempted_at with hook_gone_at, which a later refused
// decide keeps while replacing the attempt. The refusal advises a pane answer
// or record-pane-answer and carries the request's facts as err_details, read
// after that write (step 2b).
func TestDecideRefusesFallenBackAtOnce(t *testing.T) {
	t.Parallel()
	for _, agent := range relayAgents {
		for _, tc := range []struct {
			name string
			gone bool // the hook is gone; otherwise it cannot be checked and settled_at has passed
		}{
			{"hook gone", true},
			{"hook cannot be checked, past settled_at", false},
		} {
			t.Run(agentLabel(agent, tc.name), func(t *testing.T) {
				t.Parallel()
				e := newRelayEnvAgent(t, relayEnvHook, agent)
				var wantToolUseID any = "toolu_01" // a Codex request has no tool_use_id
				if agent == store.AgentCodex {
					wantToolUseID = nil
				}
				if tc.gone {
					e.pc.Set(relayEnvHook.PID, procfix.Gone())
				} else {
					e.ns, e.now = "", e.settled
				}
				first := e.now

				_, waited, err := e.decideWith(t, e.s, "allow", nil, nil)

				assertOneSentinel(t, err, api.ErrRelayFallenBack)
				adviceAssertPhrase(t, err, advRelayFallenBack)
				d := assertFallenBackDetails(t, err, storefix.TestRequestTokenA, store.StateCheckPermission)
				if d["attempted_decision"] != "allow" || d["hook_gone_at"] == nil || d["tool_use_id"] != wantToolUseID || len(d["open_requests"].([]any)) != 0 {
					t.Errorf("err_details = %v; want the attempt (allow), hook_gone_at and tool_use_id recorded, no other open request", d)
				}
				if waited != 0 {
					t.Errorf("decide waited %v; want the refusal at once", waited)
				}
				pr := e.request(t)
				if pr.Decision != "" || pr.AttemptedDecision != "allow" || !pr.AttemptedAt.Equal(first) || !pr.HookGoneAt.Equal(first) {
					t.Errorf("request = decision %q, attempted %q at %v, hook_gone_at %v; want none, allow at %v, %v",
						pr.Decision, pr.AttemptedDecision, pr.AttemptedAt, pr.HookGoneAt, first, first)
				}

				e.now = e.now.Add(time.Minute)
				_, _, err = e.decideWith(t, e.s, "deny", nil, nil)
				assertOneSentinel(t, err, api.ErrRelayFallenBack)
				if pr := e.request(t); pr.AttemptedDecision != "deny" || !pr.AttemptedAt.Equal(e.now) || !pr.HookGoneAt.Equal(first) {
					t.Errorf("after a second refusal: attempted %q at %v, hook_gone_at %v; want deny at %v, %v kept",
						pr.AttemptedDecision, pr.AttemptedAt, pr.HookGoneAt, e.now, first)
				}
				if res := e.getPermission(t, e.s); res.AttemptedDecision == nil || *res.AttemptedDecision != "deny" || res.Decision != nil {
					t.Errorf("get-permission = attempted %v, decision %v; want deny shown, no decision", res.AttemptedDecision, res.Decision)
				}
			})
		}
	}
}

// TestDecideMaxWaitUnderHeldLock (decision 9 B; the b.146 property): with
// another process holding the write lock past the call and the store's busy
// timeout at either end of its range, decide with max_wait_ms N returns within
// N ms plus 1 s: ErrStoreBusy with nothing recorded when the bound is reached
// before the verdict commits (a request from v7 on, one from before v7, and
// the store's one connection held by another call of this process waiting
// for that lock), its refusal unchanged for a fallen-back request (the attempt
// not stored), and not_confirmed, never ErrStoreBusy, when the lock is taken
// after the commit. A request from before v7 whose relay hook settles after
// the bound is ErrStoreBusy at once, with no wait. Without max_wait_ms a lock
// held past the store's busy timeout is an unnamed store error, never
// ErrStoreBusy. A negative max_wait_ms is ErrInvalidFlags.
func TestDecideMaxWaitUnderHeldLock(t *testing.T) {
	t.Parallel()
	const bound = 500
	cases := []struct {
		name      string
		legacy    bool // the request was recorded before v7
		gone      bool // its hook is gone
		lockLater bool // the lock is taken at decide's first wait, after the commit
		connHeld  bool // another call of this process holds the store's connection, waiting for the lock
		want      error
		decision  string // recorded
	}{
		{name: "verdict write", want: api.ErrStoreBusy},
		{name: "verdict write, a request recorded before v7", legacy: true, want: api.ErrStoreBusy},
		{name: "the store's connection held by another call", connHeld: true, want: api.ErrStoreBusy},
		{name: "fallen-back refusal", gone: true, want: api.ErrRelayFallenBack},
		{name: "lock taken after the commit", lockLater: true, decision: "allow"},
	}
	for _, agent := range relayAgents {
		for _, busyMs := range []int{store.DefaultBusyTimeoutMs, config.MaxStoreBusyTimeoutMs} {
			for _, tc := range cases {
				t.Run(agentLabel(agent, tc.name+"/busy "+time.Duration(busyMs*int(time.Millisecond)).String()), func(t *testing.T) {
					t.Parallel()
					var e *relayEnv
					if tc.legacy {
						s, dbPath := apitest.SeedDecideFixtureAgent(t, "on", agent)
						apitest.SeedPermissionRow(t, s, "id-d-1")
						e = &relayEnv{s: s, dbPath: dbPath, id: "id-d-1", pc: procfix.New(), ns: relayNS}
					} else {
						e = newRelayEnvAgent(t, relayEnvHook, agent)
					}
					if tc.gone {
						e.pc.Set(relayEnvHook.PID, procfix.Gone())
					}
					s, err := store.OpenWithBusyTimeout(e.dbPath, busyMs)
					if err != nil {
						t.Fatalf("OpenWithBusyTimeout: %v", err)
					}
					defer s.Close()
					var commitStarted, commitConfirmed atomic.Bool
					postcommitHolder := make(chan func(), 1)
					view := e.view()
					view.Now = time.Now
					var release func()
					if !tc.lockLater {
						release = apitest.HoldWriteLock(t, e.dbPath, time.Minute)
					}
					if tc.connHeld {
						release = holdStoreConnection(t, s, release)
					}
					phases := make([]decidePhase, 0, 32)
					observed := &deadlineStore{DecideStore: s, phases: &phases, originalDeadline: new(time.Time)}
					if tc.lockLater {
						observed.afterVerdict = func(written bool, err error) {
							if written || err != nil && strings.Contains(err.Error(), "commit outcome unconfirmed") {
								commitStarted.Store(true)
							}
							if written {
								commitConfirmed.Store(true)
								postcommitHolder <- apitest.HoldWriteLock(t, e.dbPath, time.Minute)
							}
						}
					}
					var realSleep time.Duration
					sleep := func(d time.Duration) {
						entered := time.Now()
						if release == nil && !tc.lockLater {
							release = apitest.HoldWriteLock(t, e.dbPath, time.Minute)
						}
						ready := time.Now()
						phases = append(phases, decidePhase{name: "sleep callback enter to holder ready", start: entered, end: ready})
						sleepStart := time.Now()
						time.Sleep(d)
						sleepEnd := time.Now()
						realSleep += sleepEnd.Sub(sleepStart)
						phases = append(phases, decidePhase{name: "real sleep", start: sleepStart, end: sleepEnd, wait: d})
					}

					start := time.Now()
					res, err := api.DecideWithSleep(observed, view, sleep, api.DecideParams{InstanceID: e.id,
						RequestToken: storefix.TestRequestTokenA, Decision: "allow", MaxWaitMs: ptr(int64(bound))})
					end := time.Now()
					took := end.Sub(start)
					defer func() {
						if t.Failed() {
							t.Logf("decide phases: call start=%s end=%s elapsed=%v total real sleep=%v", start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano), took, realSleep)
							for _, phase := range phases {
								t.Logf("decide phase %s: start=+%v end=+%v elapsed=%v requested wait=%v", phase.name, phase.start.Sub(start), phase.end.Sub(start), phase.end.Sub(phase.start), phase.wait)
							}
						}
					}()
					if release != nil {
						release()
					}
					// Join the operation's safe lease before durable inspection of an
					// unconfirmed response; never replay it to obtain a desired phase.
					verifyCtx, stopVerify := context.WithTimeout(context.Background(), 3*time.Second)
					_, verifyErr := s.WithContext(verifyCtx).GetSpawnWithin(e.id, store.DefaultLockWait)
					stopVerify()
					if verifyErr != nil {
						t.Fatalf("safe lease inspection: %v", verifyErr)
					}
					if tc.lockLater && commitConfirmed.Load() {
						select {
						case free := <-postcommitHolder:
							free()
							t.Log("actual COMMIT and postcommit holder-ready observed")
						case <-time.After(time.Second):
							t.Fatal("actual COMMIT lacked holder-ready event")
						}
					}

					wantDecision := tc.decision
					if tc.lockLater && !commitStarted.Load() {
						assertOneSentinel(t, err, api.ErrStoreBusy)
						wantDecision = ""
						t.Log("physical guard expired before COMMIT; exact postcommit policy is covered separately")
					} else if tc.lockLater && err != nil && strings.Contains(err.Error(), "commit outcome unconfirmed") {
						if errors.Is(err, api.ErrStoreBusy) {
							t.Error("in-flight COMMIT misclassified as Busy")
						}
						wantDecision = e.request(t).Decision
						if wantDecision != "" && wantDecision != "allow" {
							t.Errorf("uncertain COMMIT invalid durable decision %q", wantDecision)
						}
					} else if tc.gone && errors.Is(err, api.ErrStoreBusy) {
						assertOneSentinel(t, err, api.ErrStoreBusy)
						t.Log("physical guard expired during authoritative preclassification reads")
					} else if tc.want == nil {
						if err != nil || res.Delivery != api.DeliveryNotConfirmed {
							t.Errorf("decide = %+v, %v; want not_confirmed, no error", res, err)
						}
					} else {
						assertOneSentinel(t, err, tc.want)
					}
					if limit := bound*time.Millisecond + time.Second; took > limit {
						t.Errorf("decide returned after %v; want within %v", took, limit)
					}
					for _, phase := range phases {
						if phase.name != "sleep callback enter to holder ready" && phase.name != "real sleep" && (phase.deadline.IsZero() || !phase.deadline.Equal(*observed.originalDeadline)) {
							t.Errorf("phase %s lacks the original SQL deadline: %v", phase.name, phase.deadline)
						}
					}
					if pr := e.request(t); pr.Decision != wantDecision || pr.AttemptedDecision != "" {
						t.Errorf("request = decision %q, attempted %q; want %q, none", pr.Decision, pr.AttemptedDecision, wantDecision)
					}
				})
			}
		}
	}
	t.Run("a request recorded before v7, its relay hook settling after the bound", func(t *testing.T) {
		t.Parallel()
		s, dbPath := apitest.SeedDecideFixture(t, "on")
		apitest.SeedPermissionRow(t, s, "id-d-1")
		storefix.SeedUndeliverablePermissionRequest(t, s, dbPath, "id-d-1", storefix.TestRequestTokenA, time.Hour+500*time.Millisecond)
		// Use persisted created_at: undeliverable, exactly 1 s before its hook
		// settles, regardless of scheduling delays between seeding and decide.
		now := rowA(t, s).CreatedAt.Add(time.Hour + time.Second)
		var slept time.Duration
		start := time.Now()
		_, err := api.DecideWithSleep(s, api.RelayView{Now: func() time.Time { return now }, Window: time.Hour}, func(d time.Duration) { slept += d },
			api.DecideParams{InstanceID: "id-d-1", RequestToken: storefix.TestRequestTokenA, Decision: "allow", MaxWaitMs: ptr(int64(100))})
		took := time.Since(start)

		assertOneSentinel(t, err, api.ErrStoreBusy)
		if slept != 0 || took > 100*time.Millisecond+time.Second {
			t.Errorf("decide slept %v and returned after %v; want no sleep, within the 100 ms bound", slept, took)
		}
		if pr := rowA(t, s); pr.Decision != "" || pr.AttemptedDecision != "" {
			t.Errorf("request = decision %q, attempted %q; want nothing recorded", pr.Decision, pr.AttemptedDecision)
		}
	})
	t.Run("no max_wait_ms, the lock held past the store's busy timeout", func(t *testing.T) {
		t.Parallel()
		e := newRelayEnv(t, relayEnvHook)
		s, err := store.OpenWithBusyTimeout(e.dbPath, 100)
		if err != nil {
			t.Fatalf("OpenWithBusyTimeout: %v", err)
		}
		defer s.Close()
		release := apitest.HoldWriteLock(t, e.dbPath, time.Minute)
		_, _, err = e.decideWith(t, s, "allow", nil, nil)
		release()

		if err == nil || errors.Is(err, api.ErrStoreBusy) {
			t.Errorf("decide = %v; want an unnamed store error, not ErrStoreBusy (no bound was given)", err)
		}
		if pr := e.request(t); pr.Decision != "" || pr.AttemptedDecision != "" {
			t.Errorf("request = decision %q, attempted %q; want nothing recorded", pr.Decision, pr.AttemptedDecision)
		}
	})
	t.Run("negative max_wait_ms", func(t *testing.T) {
		t.Parallel()
		e := newRelayEnv(t, relayEnvHook)
		_, _, err := e.decideWith(t, e.s, "allow", ptr(int64(-1)), nil)
		assertOneSentinel(t, err, api.ErrInvalidFlags)
		if pr := e.request(t); pr.Decision != "" {
			t.Errorf("request decision = %q; want none recorded", pr.Decision)
		}
	})
}

type decidePhase struct {
	name       string
	start, end time.Time
	wait       time.Duration
	deadline   time.Time
}

// deadlineStore observes the bounded calls used by the deadline table, delegating
// every operation to its real store. Unbounded methods are forwarded by embedding.
type deadlineStore struct {
	api.DecideStore
	phases           *[]decidePhase
	ctx              context.Context
	originalDeadline *time.Time
	afterVerdict     func(bool, error)
}

func (s *deadlineStore) WithDecideContext(ctx context.Context) api.DecideStore {
	scoped := *s
	scoped.ctx = ctx
	*s.originalDeadline, _ = ctx.Deadline()
	if real, ok := s.DecideStore.(*store.Store); ok {
		scoped.DecideStore = real.WithContext(ctx)
	} else if contextual, ok := s.DecideStore.(interface {
		WithDecideContext(context.Context) api.DecideStore
	}); ok {
		scoped.DecideStore = contextual.WithDecideContext(ctx)
	} else {
		panic("deadline observer requires a context-aware delegate")
	}
	return &scoped
}

func (s *deadlineStore) observe(name string, wait time.Duration) func() {
	start := time.Now()
	return func() {
		end := time.Now()
		deadline, _ := s.ctx.Deadline()
		*s.phases = append(*s.phases, decidePhase{name: name, start: start, end: end, wait: wait, deadline: deadline})
	}
}

func (s *deadlineStore) GetSpawnWithin(id string, wait time.Duration) (api.Spawn, error) {
	defer s.observe("GetSpawnWithin", wait)()
	return s.DecideStore.GetSpawnWithin(id, wait)
}

func (s *deadlineStore) GetPermissionRequestWithin(id, token string, wait time.Duration) (api.PermissionRow, error) {
	defer s.observe("GetPermissionRequestWithin", wait)()
	return s.DecideStore.GetPermissionRequestWithin(id, token, wait)
}

func (s *deadlineStore) PermissionRequestsForSpawnWithin(id string, wait time.Duration) ([]api.PermissionRow, error) {
	defer s.observe("PermissionRequestsForSpawnWithin", wait)()
	return s.DecideStore.PermissionRequestsForSpawnWithin(id, wait)
}

func (s *deadlineStore) DecideRelayRequest(id, token, decision, reason, writer string, cutoff time.Time, wait time.Duration) (bool, error) {
	defer s.observe("DecideRelayRequest (including verdict write return)", wait)()
	written, err := s.DecideStore.DecideRelayRequest(id, token, decision, reason, writer, cutoff, wait)
	if s.afterVerdict != nil {
		s.afterVerdict(written, err)
	}
	return written, err
}

func (s *deadlineStore) RecordRefusedDecision(id, token, decision string, at, gone time.Time, wait time.Duration) error {
	defer s.observe("RecordRefusedDecision", wait)()
	return s.DecideStore.RecordRefusedDecision(id, token, decision, at, gone, wait)
}

// holdStoreConnection makes another call of this process hold s's one
// connection: a write, with s's busy timeout, waiting for the write lock that
// releaseLock frees. It returns once a bounded read of s finds the connection
// held, with a release that frees the lock and waits for that call to end.
func holdStoreConnection(t *testing.T, s *store.Store, releaseLock func()) (release func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.RecordRefusedDecision("no-such-row", "no-such-token", "allow", time.Now(), time.Time{}, store.DefaultLockWait)
	}()
	release = func() {
		releaseLock()
		<-done
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if _, err := s.GetSpawnWithin("no-such-row", 5*time.Millisecond); errors.Is(err, store.ErrStoreBusy) {
			return release
		}
	}
	release()
	t.Fatal("the other call did not take the store's connection within 5s")
	return nil
}

// TestAdviceFollow_F8_DecideNotConfirmedAskGetPermission: F8 decide's
// not_confirmed, "poll get-permission; ends by confirm_by". Followed
// literally, get-permission polled until confirm_by reads delivered once the
// hook acks, fallen_back once it is gone without one, and never
// not_confirmed at confirm_by.
func TestAdviceFollow_F8_DecideNotConfirmedAskGetPermission(t *testing.T) {
	t.Parallel()
	adviceAssertManifest(t, "decide", "", "delivery delivered/not_confirmed (poll get-permission; ends by confirm_by)")
	for _, tc := range []struct {
		name  string
		after func(e *relayEnv) // what the hook does after decide returned
		want  string
	}{
		{"the hook acks late", func(e *relayEnv) {
			_, _, _, _ = e.s.AckRelayDecision(e.id, storefix.TestRequestTokenA, e.now, store.DefaultLockWait, nil)
		}, api.DeliveryDelivered},
		{"the hook dies without an ack", func(e *relayEnv) { e.pc.Set(relayEnvHook.PID, procfix.Gone()) }, api.DeliveryFallenBack},
		{"the hook cannot be checked and never acks", func(e *relayEnv) { e.ns = "" }, api.DeliveryFallenBack},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newRelayEnv(t, relayEnvHook)
			res, _, err := e.decideWith(t, e.s, "allow", nil, nil)
			if err != nil || res.Delivery != api.DeliveryNotConfirmed {
				t.Fatalf("decide = %+v, %v; want not_confirmed", res, err)
			}
			confirmBy := res.ConfirmBy

			// The advice, literally: poll get-permission, at the latest at confirm_by.
			tc.after(e)
			got := e.getPermission(t, e.s).Delivery
			for got == api.DeliveryNotConfirmed && e.now.Before(confirmBy) {
				e.now = minTime(e.now.Add(10*time.Minute), confirmBy)
				got = e.getPermission(t, e.s).Delivery
			}
			if got != tc.want {
				t.Errorf("get-permission by confirm_by = %q; want %q", got, tc.want)
			}
		})
	}
}

// minTime returns the earlier of a and b.
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// TestAdviceFollow_F9_DecideStoreBusyRetry: F9 ErrStoreBusy "nothing was
// recorded, so retry". Controlled policy availability preserves two refusals
// then a fresh-budget success; real SQLite locking has its deadline owner above.
func TestAdviceFollow_F9_DecideStoreBusyRetry(t *testing.T) {
	t.Parallel()
	adviceAssertGoDoc(t, "decide.go", "Decide", "[ErrStoreBusy]: MaxWaitMs prevents completing a required wait before recording the verdict; nothing was recorded, so retry.")
	adviceAssertManifest(t, "decide", "max_wait_ms", "if they cannot finish in time, ErrStoreBusy records nothing and a retry is safe")
	e := newRelayEnv(t, relayEnvHook)
	policy := newDecidePolicyStore(t, e)
	policy.held = true
	decide := func() error {
		_, _, err := policy.decide(ptr(int64(100)))
		return err
	}
	first := decide()
	assertOneSentinel(t, first, api.ErrStoreBusy)
	if pr, _ := policy.GetPermissionRequest(e.id, storefix.TestRequestTokenA); pr.Decision != "" || pr.AttemptedDecision != "" || policy.commits != 0 {
		t.Fatalf("first held call mutated policy request: %+v commits=%d", pr, policy.commits)
	}
	if again := decide(); !errors.Is(again, api.ErrStoreBusy) || policy.row.Decision != "" || policy.row.AttemptedDecision != "" || policy.commits != 0 {
		t.Errorf("retried while held: %v, request %+v commits=%d; want ErrStoreBusy again, nothing recorded", again, policy.row, policy.commits)
	}
	policy.held = false

	// The advice, literally: retry.
	if err := decide(); err != nil {
		t.Fatalf("decide retried: %v; want the verdict recorded", err)
	}
	if pr, _ := policy.GetPermissionRequest(e.id, storefix.TestRequestTokenA); pr.Decision != "allow" || policy.commits != 1 {
		t.Errorf("request decision = %q; want allow", pr.Decision)
	}
	if err := decide(); !errors.Is(err, api.ErrAlreadyDecided) || policy.commits != 1 {
		t.Errorf("duplicate call=%v commits=%d; want first verdict preserved", err, policy.commits)
	}
}

// Store-backed synthetic Cursor cases do not claim ACP admission or receipt.
func TestDecideKeepsDurableOrigin(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{store.AgentClaude, store.AgentCodex, store.AgentCursor} {
		t.Run(agent, func(t *testing.T) {
			t.Parallel()
			no := false
			now := time.Now()
			s, path := apitest.SeedDecideFixtureAgent(t, "on", agent, store.RelayRequest{RequestToken: storefix.TestRequestTokenA, ToolName: "Bash", ToolInput: "{}", MainTurnEndEligible: &no, SettledAt: now.Add(time.Hour)})
			// t2.jf1.r8.a8: real trail mutex fault, after original seed and before original Decide.
			gate := &trailEncodingGate{entered: make(chan struct{}), release: make(chan struct{})}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(gate.release) }) }
			holderDone := make(chan error, 1)
			go func() {
				holderDone <- trail.Emit(context.Background(), "ad.test.original_slow_trail_holder", map[string]any{"gate": gate})
			}()
			select {
			case <-gate.entered:
			case <-time.After(5 * time.Second):
				release()
				t.Fatal("fault holder did not acquire real trail mutex")
			}
			faultStarted := time.Now()
			pastBudget := time.NewTimer(1100 * time.Millisecond)
			defer pastBudget.Stop()
			// Release after the original Decide returns AND the original one-second budget has passed.
			defer func() {
				<-pastBudget.C
				t.Logf("FAULT release after Decide return: held=%s pending=%+v", time.Since(faultStarted), trail.Default().Status())
				release()
				select {
				case holderErr := <-holderDone:
					if holderErr != nil {
						t.Errorf("fault holder Emit: %v", holderErr)
					}
				case <-time.After(5 * time.Second):
					t.Error("fault holder did not finish after release")
				}
				drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer drainCancel()
				if drainErr := trail.Default().Close(drainCtx); drainErr != nil {
					t.Errorf("fault queue drain: %v", drainErr)
				}
				t.Logf("FAULT drained: %+v", trail.Default().Status())
			}()
			// Cleanup safety only: never used to make the original assertion pass.
			watchdog := time.AfterFunc(8*time.Second, release)
			defer watchdog.Stop()
			zero := int64(0)
			res, err := api.DecideWithSleep(s, api.RelayView{Now: func() time.Time { return now }}, func(time.Duration) { t.Fatal("zero wait slept") }, api.DecideParams{InstanceID: "id-d-1", RequestToken: storefix.TestRequestTokenA, Decision: "deny", MaxWaitMs: &zero})
			errorText := "<nil>"
			if err != nil {
				errorText = err.Error()
			}
			t.Logf("FAULT actual elapsed=%s error=%q historical_exact_match=%t delivery=%q result=%+v pending=%+v", time.Since(faultStarted), errorText, errorText == "store: commit outcome unconfirmed; read the request before retrying: context deadline exceeded", res.Delivery, res, trail.Default().Status())
			if err != nil || res.Delivery != api.DeliveryNotConfirmed {
				t.Fatalf("decide %+v %v", res, err)
			}
			reopened, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			request := rowA(t, reopened)
			t.Logf("FAULT independent reopen: decision=%q main_turn_end_eligible=%t agent_id=%q proven_gone=%t row=%+v", request.Decision, request.MainTurnEndEligible, request.AgentID, request.ProvenGone(), request)
			if request.MainTurnEndEligible || request.AgentID != "" || request.Decision != "deny" || request.ProvenGone() {
				t.Fatalf("decision upgraded origin/proof %+v", request)
			}
		})
	}
}
