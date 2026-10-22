package api_test

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

type observationReader interface {
	api.StatusStore
	api.GetStore
	api.ListStore
	SpawnStatusRow(string, store.SettleRead) (store.Spawn, error)
	PermissionRequestsForSpawnWithin(string, time.Duration) ([]store.PermissionRow, error)
	ObserveNeedsIntelligence(store.Spawn, []store.PermissionSample, time.Time, time.Duration, store.SettleRead) (store.DisplayDetail, error)
	RecordPermissionFallbacks(time.Time, time.Duration, []store.FallbackObservation) (map[int64]time.Time, error)
}

// Lease only after the mandatory row read (status's row, list's rows) or
// between the two judged reads. reads counts the request reads that returned;
// writes names each display or fallback write called, in order.
type readLeaseStore struct {
	observationReader
	afterRow   func() error
	afterFirst func() error
	reads      int
	writes     []string
}

func (s *readLeaseStore) SpawnStatusRow(id string, settle store.SettleRead) (store.Spawn, error) {
	row, err := s.observationReader.SpawnStatusRow(id, settle)
	if err == nil && s.afterRow != nil {
		err = s.afterRow()
	}
	return row, err
}

func (s *readLeaseStore) ListSpawns(f api.ListFilters) ([]api.Spawn, error) {
	rows, err := s.observationReader.ListSpawns(f)
	if err == nil && s.afterRow != nil {
		err = s.afterRow()
	}
	return rows, err
}

func (s *readLeaseStore) ObserveNeedsIntelligence(row store.Spawn, samples []store.PermissionSample, now time.Time, timeout time.Duration, settle store.SettleRead) (store.DisplayDetail, error) {
	s.writes = append(s.writes, "ObserveNeedsIntelligence")
	return s.observationReader.ObserveNeedsIntelligence(row, samples, now, timeout, settle)
}

func (s *readLeaseStore) RecordPermissionFallbacks(at time.Time, wait time.Duration, observations []store.FallbackObservation) (map[int64]time.Time, error) {
	s.writes = append(s.writes, "RecordPermissionFallbacks")
	return s.observationReader.RecordPermissionFallbacks(at, wait, observations)
}

func (s *readLeaseStore) afterRead() error {
	s.reads++
	if s.reads == 1 && s.afterFirst != nil {
		return s.afterFirst()
	}
	return nil
}

func (s *readLeaseStore) PermissionRequestsForSpawn(id string) ([]store.PermissionRow, error) {
	rows, err := s.observationReader.PermissionRequestsForSpawn(id)
	if err == nil {
		err = s.afterRead()
	}
	return rows, err
}

func (s *readLeaseStore) PermissionRequestsForSpawnWithin(id string, wait time.Duration) ([]store.PermissionRow, error) {
	rows, err := s.observationReader.PermissionRequestsForSpawnWithin(id, wait)
	if err == nil {
		err = s.afterRead()
	}
	return rows, err
}

func (s *readLeaseStore) OpenPermissionRequestsForSpawn(id string) ([]store.PermissionRow, error) {
	rows, err := s.observationReader.OpenPermissionRequestsForSpawn(id)
	if err == nil {
		err = s.afterRead()
	}
	return rows, err
}

// Park an actual same-pool transaction; the sentinel guarantees rollback.
// leased closes once take has returned; a nil error from take means the
// connection is held.
func holdReadConnection(t *testing.T, s *store.Store, id string) (take func() error, leased <-chan struct{}, release func()) {
	t.Helper()
	held := make(chan struct{})
	start, entered, free, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	abort := errors.New("release observation test lease")
	var leaseErr error
	go func() {
		defer close(done)
		select {
		case <-start:
		case <-free:
			return
		}
		_, _, leaseErr = s.RecordPaneIntent(id, storefix.TestRequestTokenA, "allow", store.ProcessIdentity{}, nil, store.DefaultLockWait,
			func(store.Spawn, []store.PermissionRow) error {
				close(entered)
				<-free
				return abort
			})
	}()
	var once sync.Once
	release = func() {
		once.Do(func() {
			close(free)
			<-done
			if leaseErr != nil && !errors.Is(leaseErr, abort) {
				t.Errorf("connection lease did not roll back: %v", leaseErr)
			}
		})
	}
	t.Cleanup(release)
	take = func() error {
		defer close(held)
		close(start)
		select {
		case <-entered:
			return nil
		case <-done:
			return errors.New("connection lease did not reach its transaction")
		case <-time.After(10 * time.Second):
			return errors.New("connection lease did not acquire the store")
		}
	}
	return take, held, release
}

// readUnderLease fails when read is still running 10 s after leased closes
// (it waited for the lease, then released) or never reached the lease. The
// store has one connection, so a waiting read never returns under the lease:
// the bound is generous so suite load cannot fail a read that did not wait.
func readUnderLease(t *testing.T, leased <-chan struct{}, release func(), read func()) {
	t.Helper()
	const within = 10 * time.Second
	done := make(chan struct{})
	go func() {
		defer close(done)
		read()
	}()
	select {
	case <-done:
	case <-leased:
		select {
		case <-done:
		case <-time.After(within):
			t.Errorf("the read waited for the leased connection: still running %v after it was leased", within)
			release()
			<-done
		}
	}
	select {
	case <-leased:
	default:
		t.Fatal("the connection was never leased: the read did not reach the stage under test")
	}
}

func TestNeedsIntelligenceSamplingSkipsLeasedConnection(t *testing.T) {
	// Serial: the relay fixture reuses row ids in the shared trail.
	for _, backend := range []string{"store", "cursor missing view"} {
		for _, stage := range []string{"after row", "between permission reads"} {
			for _, transition := range []string{"entry", "clear"} {
				t.Run(backend+"/"+stage+"/"+transition, func(t *testing.T) {
					e := newRelayEnv(t, store.ProcessIdentity{})
					var reader observationReader = e.s
					if backend == "cursor missing view" {
						reader = e.s.WithCursorMissingReads(nil)
					}
					view := api.RelayView{Window: config.DefaultRelayTimeoutSeconds * time.Second}
					want, err := api.Status(reader, view, e.id)
					if err != nil || want.State != store.StateCheckPermission || want.StateDetail != (api.StateDetail{}) {
						t.Fatalf("ordinary setup: %+v %v", want, err)
					}
					requestFacts(t, e, apitest.PermissionTimes(e.now.Add(-time.Hour), time.Time{}, e.now.Add(-time.Minute), time.Time{}, time.Time{}, ""))
					if transition == "clear" {
						// Exercise the unwrapped production backend before introducing contention.
						observeDisplay(t, e.s, e.id, api.StateNeedsIntelligence, apitest.VersionAdvanced, func() (string, api.StateDetail, error) {
							want, err = api.Status(reader, view, e.id)
							return want.State, want.StateDetail, err
						})
						if want.Since == "" || want.RequestToken != storefix.TestRequestTokenA || want.ToolName != "Bash" {
							t.Fatalf("committed setup: %+v", want)
						}
						requestFacts(t, e, apitest.PermissionAnswer(store.PaneAnswerOutside))
					}
					before, err := e.s.GetSpawn(e.id)
					if err != nil {
						t.Fatal(err)
					}
					request := e.request(t)
					mark := trailMark(t)
					take, leased, release := holdReadConnection(t, e.s, e.id)
					s := &readLeaseStore{observationReader: reader}
					if stage == "after row" {
						s.afterRow = take
					} else {
						s.afterFirst = take
					}
					var got api.StatusResult
					readUnderLease(t, leased, release, func() { got, err = api.Status(s, view, e.id) })
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Errorf("skipped observation: %+v %v; want coherent committed %+v", got, err, want)
					}
					// The response must precede releasing the actual connection.
					release()
					after, err := e.s.GetSpawn(e.id)
					if err != nil || after.DisplayObservation != before.DisplayObservation || after.ObservationVersion != before.ObservationVersion || after.RowVersion != before.RowVersion || e.request(t) != request {
						t.Errorf("skipped observation changed persisted facts: before=%+v after=%+v err=%v", before, after, err)
					}
					if events := displayEvents(t, mark, e.id); len(events) != 0 {
						t.Errorf("skipped observation emitted %v", events)
					}
				})
			}
		}
	}
}

// t2.sp6.rk.1k review b5a9c71c: list --state needs_intelligence never waits
// for a dropped row's requests.
func TestListStateFilterSkipsLeasedConnection(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, relayEnvHook)
	e.pc.Set(relayEnvHook.PID, procfix.Gone())
	take, leased, release := holdReadConnection(t, e.s, e.id)
	s := &readLeaseStore{observationReader: e.s, afterRow: take}
	var res api.ListResult
	var err error
	readUnderLease(t, leased, release, func() {
		res, err = api.List(s, e.view(), api.ListParams{State: []string{api.StateNeedsIntelligence}})
	})
	if err != nil || len(res.Spawns) != 0 {
		t.Errorf("list --state needs_intelligence = %+v, %v; want no rows", res.Spawns, err)
	}
}

// t2.sp6.rk.1k read cost: under a held write lock, get and list make one judged
// read and one write attempt; after it is busy they attempt no other write. A
// get whose hook is alive writes no hook_gone_at and gives its read to the
// observation.
func TestHeldWriteLockReadsWriteOnce(t *testing.T) {
	t.Parallel()
	get := func(e *relayEnv, s *readLeaseStore) error {
		_, err := api.Get(s, e.view(), e.id)
		return err
	}
	for _, tc := range []struct {
		verb   string
		gone   bool
		read   func(*relayEnv, *readLeaseStore) error
		writes []string
	}{
		{"get", true, get, []string{"RecordPermissionFallbacks"}},
		{"get, hook alive", false, get, []string{"ObserveNeedsIntelligence"}},
		{"list", true, func(e *relayEnv, s *readLeaseStore) error {
			_, err := api.List(s, e.view(), api.ListParams{})
			return err
		}, []string{"ObserveNeedsIntelligence"}},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			t.Parallel()
			e := newRelayEnv(t, relayEnvHook)
			if tc.gone {
				e.pc.Set(relayEnvHook.PID, procfix.Gone())
			}
			apitest.HoldWriteLock(t, e.dbPath, time.Minute)
			s := &readLeaseStore{observationReader: e.s}
			if err := tc.read(e, s); err != nil || s.reads != 2 || !reflect.DeepEqual(s.writes, tc.writes) {
				t.Errorf("%s: error %v, %d request reads, writes %v; want none, 2 (one judged read), %v", tc.verb, err, s.reads, s.writes, tc.writes)
			}
		})
	}
}
