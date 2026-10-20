package api_test

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

type stuckObservationReader interface {
	api.StatusStore
	SpawnStatusRow(string, store.SettleRead) (store.Spawn, error)
	PermissionRequestsForSpawn(string) ([]store.PermissionRow, error)
	PermissionRequestsForSpawnWithin(string, time.Duration) ([]store.PermissionRow, error)
	ObservePermissionStuck(store.Spawn, []store.PermissionSample, time.Time, time.Duration, store.SettleRead) (store.StuckDisplay, error)
}

// Lease only after the mandatory row read or between the two judged reads.
type stuckReadLeaseStore struct {
	stuckObservationReader
	afterRow   func() error
	afterFirst func() error
	reads      int
}

func (s *stuckReadLeaseStore) SpawnStatusRow(id string, settle store.SettleRead) (store.Spawn, error) {
	row, err := s.stuckObservationReader.SpawnStatusRow(id, settle)
	if err == nil && s.afterRow != nil {
		err = s.afterRow()
	}
	return row, err
}

func (s *stuckReadLeaseStore) afterRead() error {
	s.reads++
	if s.reads == 1 && s.afterFirst != nil {
		return s.afterFirst()
	}
	return nil
}

func (s *stuckReadLeaseStore) PermissionRequestsForSpawn(id string) ([]store.PermissionRow, error) {
	rows, err := s.stuckObservationReader.PermissionRequestsForSpawn(id)
	if err == nil {
		err = s.afterRead()
	}
	return rows, err
}

func (s *stuckReadLeaseStore) PermissionRequestsForSpawnWithin(id string, wait time.Duration) ([]store.PermissionRow, error) {
	rows, err := s.stuckObservationReader.PermissionRequestsForSpawnWithin(id, wait)
	if err == nil {
		err = s.afterRead()
	}
	return rows, err
}

// Park an actual same-pool transaction; the sentinel guarantees rollback.
func holdStuckReadConnection(t *testing.T, s *store.Store, id string) (take func() error, release func()) {
	t.Helper()
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
		close(start)
		select {
		case <-entered:
			return nil
		case <-done:
			return errors.New("connection lease did not reach its transaction")
		case <-time.After(time.Second):
			return errors.New("connection lease did not acquire the store")
		}
	}
	return take, release
}

func TestPermissionStuckSamplingSkipsLeasedConnection(t *testing.T) {
	// Serial: the relay fixture reuses row ids in the shared trail.
	for _, backend := range []string{"store", "cursor missing view"} {
		for _, stage := range []string{"after row", "between permission reads"} {
			for _, transition := range []string{"entry", "clear"} {
				t.Run(backend+"/"+stage+"/"+transition, func(t *testing.T) {
					e := newRelayEnv(t, store.ProcessIdentity{})
					var reader stuckObservationReader = e.s
					if backend == "cursor missing view" {
						reader = e.s.WithCursorMissingReads(nil)
					}
					want, err := api.Status(reader, e.id)
					if err != nil || want.State != store.StateCheckPermission || want.StuckInfo != (api.StuckInfo{}) {
						t.Fatalf("ordinary setup: %+v %v", want, err)
					}
					stuckFacts(t, e, apitest.PermissionTimes(e.now.Add(-time.Hour), time.Time{}, e.now.Add(-time.Minute), time.Time{}, time.Time{}, ""))
					if transition == "clear" {
						// Exercise the unwrapped production backend before introducing contention.
						observeStuck(t, e.s, e.id, store.StateStuck, apitest.VersionAdvanced, func() (string, api.StuckInfo, error) {
							want, err = api.Status(reader, e.id)
							return want.State, want.StuckInfo, err
						})
						if want.StuckReason != api.StuckPermissionUnanswered || want.Since == "" || want.RequestToken != storefix.TestRequestTokenA || want.ToolName != "Bash" {
							t.Fatalf("committed setup: %+v", want)
						}
						stuckFacts(t, e, apitest.PermissionAnswer(store.PaneAnswerOutside))
					}
					before, err := e.s.GetSpawn(e.id)
					if err != nil {
						t.Fatal(err)
					}
					request := e.request(t)
					mark := trailMark(t)
					lease, release := holdStuckReadConnection(t, e.s, e.id)
					// leased closes once the connection is really held: the one
					// second the read may take is counted from then.
					leased, leaseOnce := make(chan struct{}), sync.Once{}
					takeLease := func() error {
						err := lease()
						leaseOnce.Do(func() { close(leased) })
						return err
					}
					s := &stuckReadLeaseStore{stuckObservationReader: reader}
					if stage == "after row" {
						s.afterRow = takeLease
					} else {
						s.afterFirst = takeLease
					}
					type result struct {
						row api.StatusResult
						err error
					}
					done := make(chan result, 1)
					go func() {
						row, err := api.Status(s, e.id)
						done <- result{row, err}
					}()
					var got result
					select {
					case got = <-done:
					case <-leased:
						select {
						case got = <-done:
						case <-time.After(time.Second):
							t.Error("optional permission sampling waited for the leased connection")
							release()
							got = <-done
						}
					}
					select {
					case <-leased:
					default:
						t.Fatal("the connection was never leased: the read did not reach the stage under test")
					}
					if got.err != nil || !reflect.DeepEqual(got.row, want) {
						t.Errorf("skipped observation: %+v %v; want coherent committed %+v", got.row, got.err, want)
					}
					// The response must precede releasing the actual connection.
					release()
					after, err := e.s.GetSpawn(e.id)
					if err != nil || after.StuckObservation != before.StuckObservation || after.StuckVersion != before.StuckVersion || after.RowVersion != before.RowVersion || e.request(t) != request {
						t.Errorf("skipped observation changed persisted facts: before=%+v after=%+v err=%v", before, after, err)
					}
					for _, ev := range readAPITrailLines(t)[mark:] {
						if ev["instance_id"] == e.id && ev["event"] == "ad.spawn.stuck_transition" {
							t.Errorf("skipped observation emitted %v", ev)
						}
					}
				})
			}
		}
	}
}
