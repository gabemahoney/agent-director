package store_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// permissionObservation seeds current ownership and samples exact persisted records.
func permissionObservation(t *testing.T) (*store.Store, string, string, []store.PermissionSample, time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	now := time.Date(2026, 10, 19, 23, 59, 59, 123000000, time.UTC)
	id, err := apitest.SeedSpawn(path, "permission", store.StateCheckPermission, "", "on", "", true, apitest.WithPermissionEpoch(7))
	if err != nil {
		t.Fatal(err)
	}
	_, err = apitest.SeedPermissionRequest(path, id, "Bash", apitest.PermissionToken(storefix.TestRequestTokenA), apitest.PermissionTimes(now.Add(-time.Hour), time.Time{}, now.Add(-time.Hour), time.Time{}, time.Time{}, ""))
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	pr, err := s.GetPermissionRequest(id, storefix.TestRequestTokenA)
	if err != nil {
		t.Fatal(err)
	}
	return s, path, id, []store.PermissionSample{{Request: pr, FallbackAt: now.Add(-time.Minute), Basis: store.FallbackDeadline}}, now
}

// observeUnlessBusy is ObservePermissionStuck for a call that must commit,
// through commitUnlessBusy.
func observeUnlessBusy(t *testing.T, s *store.Store, row store.Spawn, samples []store.PermissionSample, now time.Time, timeout time.Duration) store.StuckDisplay {
	t.Helper()
	return commitUnlessBusy(t, func() (store.StuckDisplay, error) {
		return s.ObservePermissionStuck(row, samples, now, timeout, store.SettleRead{})
	})
}

// commitUnlessBusy runs observe, an observation that must commit. The write
// never waits for the write lock, so a busy store is the one failure that is
// repeated, at the same time and without sleeping; any other error, and a
// third busy answer, fail the test.
func commitUnlessBusy(t *testing.T, observe func() (store.StuckDisplay, error)) store.StuckDisplay {
	t.Helper()
	for attempt := 1; ; attempt++ {
		display, err := observe()
		if err == nil {
			return display
		}
		if !errors.Is(err, store.ErrStoreBusy) || attempt == 3 {
			t.Fatalf("observation did not commit: %+v %v", display, err)
		}
		t.Logf("attempt %d: store busy: %v", attempt, err)
	}
}

func TestPermissionStuckStaleSampleCannotCommit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, *store.Store, string, string, store.PermissionRow, time.Time)
	}{
		{"answer", func(t *testing.T, s *store.Store, path, id string, pr store.PermissionRow, now time.Time) {
			if err := apitest.SetPermissionFacts(path, pr.RequestID, apitest.PermissionAnswer(store.PaneAnswerOutside)); err != nil {
				t.Fatal(err)
			}
		}},
		{"acknowledgement", func(t *testing.T, s *store.Store, path, id string, pr store.PermissionRow, now time.Time) {
			if err := apitest.SetPermissionFacts(path, pr.RequestID, apitest.PermissionDelivered(now)); err != nil {
				t.Fatal(err)
			}
		}},
		{"proof", func(t *testing.T, s *store.Store, path, id string, pr store.PermissionRow, now time.Time) {
			if err := apitest.SetPermissionFacts(path, pr.RequestID, apitest.PermissionProof(now)); err != nil {
				t.Fatal(err)
			}
		}},
		{"ownership", func(t *testing.T, s *store.Store, path, id string, pr store.PermissionRow, now time.Time) {
			if err := apitest.SetPermissionFacts(path, pr.RequestID, apitest.PermissionOwner("0000000000000000", 8)); err != nil {
				t.Fatal(err)
			}
		}},
		{"independent admission", func(t *testing.T, s *store.Store, path, id string, pr store.PermissionRow, now time.Time) {
			if _, err := apitest.SeedPermissionRequest(path, id, "Read"); err != nil {
				t.Fatal(err)
			}
		}},
		{"new launch", func(t *testing.T, s *store.Store, path, id string, pr store.PermissionRow, now time.Time) {
			if got := apitest.ApplyAgentHook(t, path, id, "SessionEnd", ""); !got.Applied {
				t.Fatal(got)
			}
			ended, err := s.GetSpawn(id)
			if err != nil {
				t.Fatal(err)
			}
			if res, _, err := s.MoveToPending(id, ended.Snapshot, now.UnixMilli(), "2222222222222222", apitest.TestSocket, "", store.LaunchOwner{}); err != nil || res != store.CondApplied {
				t.Fatalf("new launch=%v %v", res, err)
			}
		}},
		// A real turn: the row is in check_permission again, but through
		// waiting, so it is a new permission interval (the epoch advanced).
		{"turn", func(t *testing.T, s *store.Store, path, id string, pr store.PermissionRow, now time.Time) {
			for _, state := range []string{store.StateWaiting, store.StateCheckPermission} {
				if err := storefix.WithSeedPane(path, id, func(gate store.HookGate) error {
					return storefix.SeedAgentWrites(s, gate, id, "", state)
				}); err != nil {
					t.Fatal(err)
				}
			}
			if after, err := s.GetSpawn(id); err != nil || after.State != store.StateCheckPermission || after.PermissionEpoch == pr.OwnerPermissionEpoch {
				t.Fatalf("turn did not start a new interval: %+v %v", after, err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, path, id, samples, now := permissionObservation(t)
			row, err := s.GetSpawnSettled(id, store.SettleRead{})
			if err != nil {
				t.Fatal(err)
			}
			mark := store.TrailMark(t)
			tc.change(t, s, path, id, samples[0].Request, now)
			got, err := s.ObservePermissionStuck(row, samples, now, 30*time.Second, store.SettleRead{})
			if err == nil || got.State == "stuck" {
				t.Fatalf("stale observation=%+v %v", got, err)
			}
			after, err := s.GetSpawn(id)
			if err != nil {
				t.Fatal(err)
			}
			if after.StuckObservation != "" {
				t.Errorf("stale observation wrote detail: %s", after.StuckObservation)
			}
			if events := store.TrailEventsSince(t, mark, "ad.spawn.stuck_transition", id); len(events) != 0 {
				t.Errorf("stale transition events: %v", events)
			}
		})
	}
}

func TestPermissionStuckRollbackAndNonwaitingLock(t *testing.T) {
	for _, mode := range []string{"locked", "failed final write"} {
		t.Run(mode, func(t *testing.T) {
			s, path, id, samples, now := permissionObservation(t)
			row, err := s.GetSpawnSettled(id, store.SettleRead{})
			if err != nil {
				t.Fatal(err)
			}
			mark := store.TrailMark(t)
			if mode == "locked" {
				release := apitest.HoldWriteLock(t, path, time.Minute)
				defer release()
			} else {
				storefix.InjectWriteFailure(t, path, storefix.WriteFailStuckObservation, id)
			}
			start := time.Now()
			got, err := s.ObservePermissionStuck(row, samples, now, 30*time.Second, store.SettleRead{})
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Errorf("nonwaiting write took %s", elapsed)
			}
			if err == nil || got != row.CommittedStuckDisplay() {
				t.Fatalf("failed observation=%+v %v; want %+v", got, err, row.CommittedStuckDisplay())
			}
			pr, err := s.GetPermissionRequest(id, samples[0].Request.RequestToken)
			if err != nil {
				t.Fatal(err)
			}
			if pr != samples[0].Request {
				t.Errorf("request clock leaked through rollback: before=%+v after=%+v", samples[0].Request, pr)
			}
			after, err := s.GetSpawn(id)
			if err != nil {
				t.Fatal(err)
			}
			if after.StuckObservation != row.StuckObservation || after.StuckVersion != row.StuckVersion {
				t.Errorf("report persisted after failed write: %+v", after)
			}
			if events := store.TrailEventsSince(t, mark, "ad.spawn.stuck_transition", id); len(events) != 0 {
				t.Errorf("uncommitted events: %v", events)
			}
		})
	}
}

// A read with nothing to commit answers from its snapshot without taking the
// write lock; only a new clock or a timed-out request needs the transaction.
func TestPermissionStuckReadWithoutChangeTakesNoWriteLock(t *testing.T) {
	for _, tc := range []struct {
		name   string
		writes bool
		facts  func(now time.Time) []apitest.PermissionOption
		start  time.Duration // sample fallback, before now
	}{
		{"persisted clock under timeout", false, func(now time.Time) []apitest.PermissionOption {
			return []apitest.PermissionOption{apitest.PermissionTimes(now.Add(-time.Hour), time.Time{}, now.Add(-10*time.Second), time.Time{}, now.Add(-10*time.Second), store.FallbackDeadline)}
		}, 10 * time.Second},
		{"answered", false, func(time.Time) []apitest.PermissionOption {
			return []apitest.PermissionOption{apitest.PermissionAnswer(store.PaneAnswerOutside)}
		}, time.Minute},
		{"unowned history", false, func(time.Time) []apitest.PermissionOption {
			return []apitest.PermissionOption{apitest.PermissionOwner("", 0)}
		}, time.Minute},
		{"new clock under timeout", true, func(time.Time) []apitest.PermissionOption { return nil }, 10 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, path, id, samples, now := permissionObservation(t)
			if facts := tc.facts(now); facts != nil {
				if err := apitest.SetPermissionFacts(path, samples[0].Request.RequestID, facts...); err != nil {
					t.Fatal(err)
				}
			}
			pr, err := s.GetPermissionRequest(id, samples[0].Request.RequestToken)
			if err != nil {
				t.Fatal(err)
			}
			samples[0].Request, samples[0].FallbackAt = pr, now.Add(-tc.start)
			row, err := s.GetSpawnSettled(id, store.SettleRead{})
			if err != nil {
				t.Fatal(err)
			}
			release := apitest.HoldWriteLock(t, path, time.Minute)
			got, err := s.ObservePermissionStuck(row, samples, now, 30*time.Second, store.SettleRead{})
			if got != row.CommittedStuckDisplay() || (err != nil) != tc.writes {
				t.Fatalf("observation=%+v err=%v; want %+v, needs write lock %v", got, err, row.CommittedStuckDisplay(), tc.writes)
			}
			release()
			after, err := s.GetSpawn(id)
			if err != nil {
				t.Fatal(err)
			}
			if after.StuckObservation != row.StuckObservation || after.StuckVersion != row.StuckVersion {
				t.Errorf("read wrote report: before=%+v after=%+v", row, after)
			}
			if stored, err := s.GetPermissionRequest(id, pr.RequestToken); err != nil || stored != pr {
				t.Errorf("read wrote request: before=%+v after=%+v %v", pr, stored, err)
			}
		})
	}
}

func TestPermissionStuckConcurrentSnapshotAndOlderClock(t *testing.T) {
	s, _, id, samples, now := permissionObservation(t)
	row, err := s.GetSpawnSettled(id, store.SettleRead{})
	if err != nil {
		t.Fatal(err)
	}
	mark := store.TrailMark(t)
	first := observeUnlessBusy(t, s, row, samples, now, 30*time.Second)
	if first.State != "stuck" {
		t.Fatalf("entry=%+v", first)
	}
	for _, at := range []time.Time{now, now.Add(time.Hour)} {
		if _, err := s.ObservePermissionStuck(row, samples, at, 30*time.Second, store.SettleRead{}); err == nil {
			t.Error("concurrent old version overwrote committed entry")
		}
	}
	fresh, err := s.GetSpawnSettled(id, store.SettleRead{})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := s.GetPermissionRequest(id, samples[0].Request.RequestToken)
	if err != nil {
		t.Fatal(err)
	}
	samples[0].Request = pr
	if got, err := s.ObservePermissionStuck(fresh, samples, now.Add(-time.Millisecond), 30*time.Second, store.SettleRead{}); err == nil || !reflect.DeepEqual(got, first) {
		t.Errorf("older sample=%+v %v", got, err)
	}
	if events := store.TrailEventsSince(t, mark, "ad.spawn.stuck_transition", id); len(events) != 1 {
		t.Errorf("entry attempts=%v", events)
	}
}

// TestPaneBaselineDecodesTolerantly: a stored baseline that is not a whole
// one of the row's current epoch (an earlier epoch's, or one with no
// fingerprint, a text start or a negative epoch) is none: the read never fails
// on it and the next sample starts a fresh period, however old the start.
func TestPaneBaselineDecodesTolerantly(t *testing.T) {
	now := time.Date(2026, 10, 19, 23, 59, 59, 123000000, time.UTC)
	old := now.Add(-24 * time.Hour).UnixMilli()
	for _, tc := range []struct {
		name              string
		sha, since, epoch any
	}{
		{"earlier epoch", "abc", old, int64(6)},
		{"no fingerprint", nil, old, int64(7)},
		{"blob fingerprint", []byte("abc"), old, int64(7)},
		{"text start", "abc", "yesterday", int64(7)},
		{"negative epoch", "abc", old, int64(-1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			id, err := apitest.SeedSpawn(path, "", store.StateWorking, "", "off", "", true, apitest.WithPermissionEpoch(7),
				apitest.WithPaneBaseline(tc.sha, tc.since, tc.epoch))
			if err != nil {
				t.Fatal(err)
			}
			s, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			row, err := s.SpawnStatusRow(id, store.SettleRead{})
			if err != nil {
				t.Fatalf("read failed on the stored baseline: %v", err)
			}
			got := commitUnlessBusy(t, func() (store.StuckDisplay, error) {
				return s.ObservePaneStuck(row, store.PaneSample{SHA256: "abc"}, now, time.Minute, store.SettleRead{})
			})
			if got.State != store.StateWorking {
				t.Fatalf("observation=%+v; want working", got)
			}
			if after, err := s.GetSpawn(id); err != nil || after.PaneSHA256 != "abc" || after.PaneSinceMillis != now.UnixMilli() || after.PaneEpoch != 7 {
				t.Fatalf("baseline=%+v %v; want a fresh one at %s in epoch 7", after, err, now)
			}
		})
	}
}

// TestRawHookDuringReasonSwitchKeepsDisplay: a raw state change while the
// other reason settles in behind a displayed episode drops that pending reason
// with the candidate, so the observation still decodes: the display stands
// until a read, which ends it with exactly one exit.
func TestRawHookDuringReasonSwitchKeepsDisplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	now := time.Date(2026, 10, 19, 23, 59, 59, 123000000, time.UTC)
	since := now.Add(-time.Hour).UnixMilli()
	switching := fmt.Sprintf(`{"state":"stuck","reason":"permission_unanswered","since_ms":%d,"request_token":%q,"tool_name":"Bash",`+
		`"candidate":"stuck","candidate_reason":"no_screen_change","candidate_since_ms":%d,"observed_at_ms":%d,"permission_epoch":7}`,
		since, storefix.TestRequestTokenA, since, since)
	id, err := apitest.SeedSpawn(path, "", store.StateWorking, "", "off", "", true, apitest.WithPermissionEpoch(7), apitest.WithStuckObservation(switching))
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seeded, err := s.GetSpawn(id)
	if err != nil {
		t.Fatal(err)
	}
	displayed := seeded.CommittedStuckDisplay()
	if displayed.State != store.StateStuck {
		t.Fatalf("setup is not displayed stuck: %+v", seeded)
	}
	if got := apitest.ApplyAgentHook(t, path, id, "Stop", ""); !got.Applied {
		t.Fatal(got)
	}
	row, err := s.GetSpawnSettled(id, store.SettleRead{})
	if err != nil || row.State != store.StateWaiting || row.CommittedStuckDisplay() != displayed {
		t.Fatalf("after the hook: %+v %v; want raw waiting still displaying %+v", row, err, displayed)
	}
	mark := store.TrailMark(t)
	if got := observeUnlessBusy(t, s, row, nil, now, 30*time.Second); got != (store.StuckDisplay{State: store.StateWaiting}) {
		t.Fatalf("read=%+v; want the episode settled out", got)
	}
	settled, err := s.GetSpawnSettled(id, store.SettleRead{})
	if err != nil || settled.StuckObservation != "" {
		t.Fatalf("settled row=%+v %v; want no observation", settled, err)
	}
	if got, err := s.ObservePermissionStuck(settled, nil, now.Add(time.Second), 30*time.Second, store.SettleRead{}); err != nil || got.State != store.StateWaiting {
		t.Fatalf("next read=%+v %v", got, err)
	}
	events := store.TrailEventsSince(t, mark, "ad.spawn.stuck_transition", id)
	if len(events) != 1 || events[0]["prior_state"] != store.StateStuck || events[0]["new_state"] != store.StateWaiting ||
		events[0]["stuck_reason"] != store.StuckPermissionUnanswered || events[0]["ts"] != now.Format(lifecycleTS) {
		t.Fatalf("events=%v; want one exit to waiting at %s", events, now)
	}
}
