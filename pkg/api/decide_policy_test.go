package api_test

import (
	"context"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// decidePolicyStore controls availability and policy time, not SQL or durability.
// Its snapshots reuse relayEnv seeds; real deadline/cleanup tests retain SQLite.
type decidePolicyStore struct {
	t              *testing.T
	e              *relayEnv
	spawn          api.Spawn
	row            api.PermissionRow
	held           bool
	commits, reads int
	waits, sleeps  []time.Duration
	afterCommit    func()
}

func newDecidePolicyStore(t *testing.T, e *relayEnv) *decidePolicyStore {
	t.Helper()
	sp, err := e.s.GetSpawn(e.id)
	if err != nil {
		t.Fatal(err)
	}
	return &decidePolicyStore{t: t, e: e, spawn: sp, row: e.request(t)}
}

func (s *decidePolicyStore) check(id, token string) {
	s.t.Helper()
	if id != s.e.id || token != s.row.RequestToken {
		s.t.Fatalf("unexpected policy identity %q/%q", id, token)
	}
}

func (s *decidePolicyStore) GetSpawn(id string) (api.Spawn, error) {
	s.t.Fatal("unexpected unbounded spawn read")
	return api.Spawn{}, nil
}

func (s *decidePolicyStore) GetSpawnWithin(id string, wait time.Duration) (api.Spawn, error) {
	s.check(id, s.row.RequestToken)
	s.waits = append(s.waits, wait)
	s.reads++
	return s.spawn, nil
}

func (s *decidePolicyStore) GetPermissionRequest(id, token string) (api.PermissionRow, error) {
	s.check(id, token)
	s.reads++
	return s.row, nil
}

func (s *decidePolicyStore) GetPermissionRequestWithin(id, token string, wait time.Duration) (api.PermissionRow, error) {
	s.waits = append(s.waits, wait)
	if s.held && s.commits > 0 {
		s.check(id, token)
		s.e.now = s.e.now.Add(wait)
		return api.PermissionRow{}, store.ErrStoreBusy
	}
	return s.GetPermissionRequest(id, token)
}

func (s *decidePolicyStore) PermissionRequestsForSpawn(string) ([]api.PermissionRow, error) {
	s.t.Fatal("unexpected policy list read")
	return nil, nil
}

func (s *decidePolicyStore) PermissionRequestsForSpawnWithin(id string, wait time.Duration) ([]api.PermissionRow, error) {
	s.check(id, s.row.RequestToken)
	s.waits = append(s.waits, wait)
	s.reads++
	return []api.PermissionRow{s.row}, nil
}

func (s *decidePolicyStore) RecordRefusedDecision(id, token, decision string, at, gone time.Time, wait time.Duration) error {
	if s.held {
		s.check(id, token)
		s.waits = append(s.waits, wait)
		s.e.now = s.e.now.Add(wait)
		return store.ErrStoreBusy
	}
	s.t.Fatal("unexpected policy refused-decision write")
	return nil
}

func TestDecideHeldLockExactPolicy(t *testing.T) {
	t.Parallel()
	for _, agent := range relayAgents {
		for _, gone := range []bool{false, true} {
			name := "postcommit not_confirmed"
			if gone {
				name = "fallen-back refusal"
			}
			t.Run(agentLabel(agent, name), func(t *testing.T) {
				t.Parallel()
				e := newRelayEnvAgent(t, relayEnvHook, agent)
				if gone {
					e.pc.Set(relayEnvHook.PID, procfix.Gone())
				}
				s := newDecidePolicyStore(t, e)
				s.held = gone
				if !gone {
					s.afterCommit = func() { s.held = true }
				}
				res, _, err := s.decide(ptr(int64(500)))
				if gone {
					assertOneSentinel(t, err, api.ErrRelayFallenBack)
					if s.commits != 0 || s.row.Decision != "" || s.row.AttemptedDecision != "" {
						t.Errorf("refused policy mutated request: %+v commits=%d", s.row, s.commits)
					}
				} else if err != nil || res.Delivery != api.DeliveryNotConfirmed || s.commits != 1 || s.row.Decision != "allow" || s.row.AttemptedDecision != "" {
					t.Errorf("postcommit policy=%+v,%v row=%+v commits=%d", res, err, s.row, s.commits)
				}
			})
		}
	}
}

func (s *decidePolicyStore) DecideRelayRequest(id, token, decision, reason, writer string, cutoff time.Time, wait time.Duration) (bool, error) {
	s.check(id, token)
	s.waits = append(s.waits, wait)
	if decision != "allow" || reason != "" || writer != store.WriterProcessDecide || !cutoff.IsZero() {
		s.t.Fatalf("unexpected policy write %q/%q/%q cutoff=%v", decision, reason, writer, cutoff)
	}
	if s.held {
		s.e.now = s.e.now.Add(wait)
		return false, store.ErrStoreBusy
	}
	if s.row.Decision != "" || s.row.Closed() || s.row.PaneAnswered() {
		return false, nil
	}
	s.row.Decision = decision
	s.commits++
	if s.afterCommit != nil {
		s.afterCommit()
	}
	return true, nil
}

func (s *decidePolicyStore) decide(maxWait *int64) (api.DecideResult, time.Duration, error) {
	s.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	return s.decideContext(ctx, maxWait)
}

func (s *decidePolicyStore) decideContext(ctx context.Context, maxWait *int64) (api.DecideResult, time.Duration, error) {
	s.t.Helper()
	start, calls := s.e.now, len(s.waits)
	var slept time.Duration
	res, err := api.DecideWithinForTest(ctx, s, s.e.view(), func(d time.Duration) {
		s.sleeps = append(s.sleeps, d)
		slept += d
		s.e.now = s.e.now.Add(d)
	}, api.DecideParams{InstanceID: s.e.id, RequestToken: storefix.TestRequestTokenA, Decision: "allow", MaxWaitMs: maxWait})
	budget := time.Duration(*maxWait) * time.Millisecond
	if len(s.waits) == calls || s.waits[calls] != budget {
		s.t.Fatalf("fresh policy call waits=%v; want first read %v", s.waits[calls:], budget)
	}
	for _, wait := range s.waits[calls:] {
		if wait < 0 || wait > budget {
			s.t.Errorf("policy wait=%v outside original %v", wait, budget)
		}
	}
	if elapsed := s.e.now.Sub(start); elapsed > budget || (s.held && elapsed != budget) {
		s.t.Errorf("policy elapsed=%v held=%v budget=%v", elapsed, s.held, budget)
	}
	return res, slept, err
}

func TestDecidePostCommitCancellationKeepsDeliveryWithoutReadOrSleep(t *testing.T) {
	t.Parallel()
	for _, agent := range relayAgents {
		t.Run(agent, func(t *testing.T) {
			t.Parallel()
			e := newRelayEnvAgent(t, relayEnvHook, agent)
			s := newDecidePolicyStore(t, e)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			readsAtCommit := 0
			s.afterCommit = func() { readsAtCommit = s.reads; cancel() }
			res, slept, err := s.decideContext(ctx, ptr(int64(300)))
			if err != nil || res.Delivery != api.DeliveryNotConfirmed || aliveIs(res.HookAlive) != "true" || !res.ConfirmBy.Equal(e.settled) {
				t.Errorf("committed cancellation result=%+v err=%v", res, err)
			}
			if ctx.Err() != context.Canceled || s.commits != 1 || s.row.Decision != "allow" || s.row.AttemptedDecision != "" || slept != 0 || len(s.sleeps) != 0 || s.reads != readsAtCommit {
				t.Errorf("after commit: ctx=%v row=%+v commits=%d slept=%v reads=%d; want canceled, allow, one commit, no sleep/read after %d", ctx.Err(), s.row, s.commits, slept, s.reads, readsAtCommit)
			}
			pr, err := s.GetPermissionRequest(e.id, storefix.TestRequestTokenA)
			if err != nil || pr.Decision != "allow" {
				t.Fatalf("independent policy inspection=%+v err=%v", pr, err)
			}
		})
	}
}
