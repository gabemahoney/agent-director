package api_test

// decide_delivery_test.go — decide on a request recorded from schema v7 on
// (b.146 decision 1 A, rules 6, 8, 15, 16, decision 9 B): it reports delivery
// after a wait of at most 1 s for the relay hook's ack, refuses a fallen-back
// request at once, keeping the verdict as attempted, and with max_wait_ms
// never waits past its bound: before the verdict commits ErrStoreBusy with
// nothing recorded, after it not_confirmed. Seeds: relay_delivery_test.go's
// relayEnv.

import (
	"errors"
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
	}, api.DecideParams{ClaudeInstanceID: e.id, RequestToken: storefix.TestRequestTokenA, Decision: decision, MaxWaitMs: maxWaitMs})
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
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newRelayEnv(t, relayEnvHook)
			e.ns = tc.ns
			acked := false
			res, waited, err := e.decideWith(t, e.s, "allow", tc.maxWaitMs, func() {
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
			if pr := e.request(t); pr.Decision != "allow" {
				t.Errorf("request decision = %q; want allow recorded", pr.Decision)
			}
		})
	}
}

// TestDecideRefusesFallenBackAtOnce (decision 1 A, rules 5, 8, 15): a request
// whose hook is gone, or cannot be checked past its settle instant, is refused
// with ErrRelayFallenBack at once (no wait, long before its relay window ends),
// nothing is recorded as its decision, and the verdict is kept as
// attempted_decision / attempted_at with hook_gone_at, which a later refused
// decide keeps while replacing the attempt.
func TestDecideRefusesFallenBackAtOnce(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		gone bool // the hook is gone; otherwise it cannot be checked and settled_at has passed
	}{
		{"hook gone", true},
		{"hook cannot be checked, past settled_at", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newRelayEnv(t, relayEnvHook)
			if tc.gone {
				e.pc.Set(relayEnvHook.PID, procfix.Gone())
			} else {
				e.ns, e.now = "", e.settled
			}
			first := e.now

			_, waited, err := e.decideWith(t, e.s, "allow", nil, nil)

			assertOneSentinel(t, err, api.ErrRelayFallenBack)
			adviceAssertPhrase(t, err, "only an answer at the pane can close it")
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
	for _, busyMs := range []int{store.DefaultBusyTimeoutMs, config.MaxStoreBusyTimeoutMs} {
		for _, tc := range cases {
			t.Run(tc.name+"/busy "+time.Duration(busyMs*int(time.Millisecond)).String(), func(t *testing.T) {
				t.Parallel()
				var e *relayEnv
				if tc.legacy {
					s, dbPath := apitest.SeedDecideFixture(t, "on")
					apitest.SeedPermissionRow(t, s, "id-d-1")
					e = &relayEnv{s: s, dbPath: dbPath, id: "id-d-1", pc: procfix.New(), ns: relayNS}
				} else {
					e = newRelayEnv(t, relayEnvHook)
				}
				if tc.gone {
					e.pc.Set(relayEnvHook.PID, procfix.Gone())
				}
				s, err := store.OpenWithBusyTimeout(e.dbPath, busyMs)
				if err != nil {
					t.Fatalf("OpenWithBusyTimeout: %v", err)
				}
				defer s.Close()
				view := e.view()
				view.Now = time.Now
				var release func()
				if !tc.lockLater {
					release = apitest.HoldWriteLock(t, e.dbPath, time.Minute)
				}
				if tc.connHeld {
					release = holdStoreConnection(t, s, release)
				}
				sleep := func(d time.Duration) {
					if release == nil {
						release = apitest.HoldWriteLock(t, e.dbPath, time.Minute)
					}
					time.Sleep(d)
				}

				start := time.Now()
				res, err := api.DecideWithSleep(s, view, sleep, api.DecideParams{ClaudeInstanceID: e.id,
					RequestToken: storefix.TestRequestTokenA, Decision: "allow", MaxWaitMs: ptr(int64(bound))})
				took := time.Since(start)
				if release != nil {
					release()
				}

				if tc.want == nil {
					if err != nil || res.Delivery != api.DeliveryNotConfirmed {
						t.Errorf("decide = %+v, %v; want not_confirmed, no error", res, err)
					}
				} else {
					assertOneSentinel(t, err, tc.want)
				}
				if limit := bound*time.Millisecond + time.Second; took > limit {
					t.Errorf("decide returned after %v; want within %v", took, limit)
				}
				if pr := e.request(t); pr.Decision != tc.decision || pr.AttemptedDecision != "" {
					t.Errorf("request = decision %q, attempted %q; want %q, none", pr.Decision, pr.AttemptedDecision, tc.decision)
				}
			})
		}
	}
	t.Run("a request recorded before v7, its relay hook settling after the bound", func(t *testing.T) {
		t.Parallel()
		s, dbPath := apitest.SeedDecideFixture(t, "on")
		apitest.SeedPermissionRow(t, s, "id-d-1")
		// created_at, in whole seconds, lands 0.5 to 1.5 s before window ago:
		// undeliverable, its hook settling 0.5 to 1.5 s from now.
		storefix.SeedUndeliverablePermissionRequest(t, s, dbPath, "id-d-1", storefix.TestRequestTokenA, time.Hour+500*time.Millisecond)
		var slept time.Duration
		start := time.Now()
		_, err := api.DecideWithSleep(s, api.RelayView{Now: time.Now, Window: time.Hour}, func(d time.Duration) { slept += d },
			api.DecideParams{ClaudeInstanceID: "id-d-1", RequestToken: storefix.TestRequestTokenA, Decision: "allow", MaxWaitMs: ptr(int64(100))})
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
// not_confirmed, "poll get-permission; it ends by confirm_by". Followed
// literally, get-permission polled until confirm_by reads delivered once the
// hook acks, fallen_back once it is gone without one, and never
// not_confirmed at confirm_by.
func TestAdviceFollow_F8_DecideNotConfirmedAskGetPermission(t *testing.T) {
	t.Parallel()
	adviceAssertManifest(t, "decide", "", "returns delivery: delivered, or not_confirmed (poll get-permission; it ends by confirm_by)")
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
// recorded, so retry". Followed literally, the same decide retried once the
// lock is free records the verdict; while it is held the retry is refused
// alike, nothing recorded.
func TestAdviceFollow_F9_DecideStoreBusyRetry(t *testing.T) {
	t.Parallel()
	adviceAssertGoDoc(t, "decide.go", "Decide", "[ErrStoreBusy]: MaxWaitMs was reached before the verdict was recorded; nothing was recorded, so retry.")
	adviceAssertManifest(t, "decide", "max_wait_ms", "decide returns ErrStoreBusy and has recorded nothing, so a retry is safe")
	e := newRelayEnv(t, relayEnvHook)
	decide := func() error {
		_, _, err := e.decideWith(t, e.s, "allow", ptr(int64(100)), nil)
		return err
	}
	release := apitest.HoldWriteLock(t, e.dbPath, time.Minute)
	first := decide()
	assertOneSentinel(t, first, api.ErrStoreBusy)
	if again := decide(); !errors.Is(again, api.ErrStoreBusy) || e.request(t).Decision != "" {
		t.Errorf("retried while the lock is held: %v, decision %q; want ErrStoreBusy again, nothing recorded", again, e.request(t).Decision)
	}
	release()

	// The advice, literally: retry.
	if err := decide(); err != nil {
		t.Fatalf("decide retried: %v; want the verdict recorded", err)
	}
	if pr := e.request(t); pr.Decision != "allow" {
		t.Errorf("request decision = %q; want allow", pr.Decision)
	}
}
