package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const lifecycleTS = "2006-01-02T15:04:05.000Z"

// storedStuckExit is id's pending stuck_exit as the database holds it.
func storedStuckExit(t *testing.T, path, id string) sql.NullString {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw sql.NullString
	if err := db.QueryRow(`SELECT stuck_exit FROM spawns WHERE instance_id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

// execRaw changes a column the store never writes the way a test needs.
func execRaw(t *testing.T, path, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

// displayedStuck commits the entry on permissionObservation's row and returns
// the committed row.
func displayedStuck(t *testing.T) (*store.Store, string, string, []store.PermissionSample, time.Time, store.Spawn) {
	t.Helper()
	s, path, id, samples, now := permissionObservation(t)
	row, err := s.GetSpawnSettled(id, store.SettleRead{})
	if err != nil {
		t.Fatal(err)
	}
	if display := observeUnlessBusy(t, s, row, samples, now, 30*time.Second); display.State != store.StateStuck {
		t.Fatalf("setup entry=%+v", display)
	}
	committed, err := s.GetSpawn(id)
	if err != nil {
		t.Fatal(err)
	}
	if committed.CommittedStuckDisplay().State != store.StateStuck {
		t.Fatalf("setup row is not displayed stuck: %+v", committed)
	}
	return s, path, id, samples, now, committed
}

// Find-missing's repair of a check_permission row whose dialog is gone ends a
// displayed episode in the same transaction: the exit is recorded with the
// repair's own epoch and version change, and is emitted once, at the repair's
// commit time, after the commit.
func TestRepairCheckPermissionClearsDisplayedStuckImmediately(t *testing.T) {
	s, path, id, samples, now, committed := displayedStuck(t)
	if err := apitest.SetPermissionFacts(path, samples[0].Request.RequestID, apitest.PermissionProof(now)); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetSpawnSettled(id, store.SettleRead{})
	if err != nil {
		t.Fatal(err)
	}
	mark := store.TrailMark(t)
	next, res, err := s.RepairCheckPermissionIfSameLife(id, before.Snapshot)
	if err != nil || res != store.CondApplied || next != store.StateWorking {
		t.Fatalf("repair=%q %v %v; want working, applied", next, res, err)
	}
	after, err := s.GetSpawn(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != store.StateWorking || after.StuckObservation != "" || after.CommittedStuckDisplay().State != store.StateWorking {
		t.Errorf("repaired row=%+v; want working with no observation", after)
	}
	if after.StuckVersion != committed.StuckVersion+1 || after.PermissionEpoch != committed.PermissionEpoch+1 {
		t.Errorf("version %d epoch %d; want %d and %d", after.StuckVersion, after.PermissionEpoch, committed.StuckVersion+1, committed.PermissionEpoch+1)
	}
	if raw := storedStuckExit(t, path, id); raw.Valid {
		t.Errorf("exit still pending after its emission: %q", raw.String)
	}
	_, stamp, err := apitest.ReadSettle(path, id)
	if err != nil {
		t.Fatal(err)
	}
	events := store.TrailEventsSince(t, mark, "ad.spawn.stuck_transition", id)
	if len(events) != 1 {
		t.Fatalf("events=%v; want the exit once", events)
	}
	ev := events[0]
	if ev["prior_state"] != store.StateStuck || ev["new_state"] != store.StateWorking || ev["stuck_reason"] != store.StuckPermissionUnanswered ||
		ev["ts"] != time.UnixMilli(stamp).UTC().Format(lifecycleTS) {
		t.Errorf("event=%v; want stuck -> working at the repair's commit time %s", ev, time.UnixMilli(stamp).UTC().Format(lifecycleTS))
	}
}

// With no displayed episode a repair leaves no exit and emits nothing.
func TestRepairCheckPermissionOfOrdinaryRowEmitsNothing(t *testing.T) {
	s, path, id, samples, now := permissionObservation(t)
	if err := apitest.SetPermissionFacts(path, samples[0].Request.RequestID, apitest.PermissionProof(now)); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetSpawnSettled(id, store.SettleRead{})
	if err != nil {
		t.Fatal(err)
	}
	mark := store.TrailMark(t)
	if next, res, err := s.RepairCheckPermissionIfSameLife(id, before.Snapshot); err != nil || res != store.CondApplied || next != store.StateWorking {
		t.Fatalf("repair=%q %v %v; want working, applied", next, res, err)
	}
	if events := store.TrailEventsSince(t, mark, "ad.spawn.stuck_transition", id); len(events) != 0 {
		t.Errorf("events=%v; want none", events)
	}
	if raw := storedStuckExit(t, path, id); raw.Valid {
		t.Errorf("exit recorded for an ordinary row: %q", raw.String)
	}
}

// Metadata the store cannot read is never a reason to refuse a lifecycle
// write: a hook, an end or a missing mark applies, leaves no observation and
// emits nothing for what it could not read.
func TestLifecycleWritesIgnoreUnreadableStuckMetadata(t *testing.T) {
	validExit := `{"new_state":"working","reason":"permission_unanswered","at_ms":1792411205000}`
	writes := []struct {
		name  string
		state string
		write func(t *testing.T, s *store.Store, path, id string)
	}{
		{"hook", store.StateWorking, func(t *testing.T, _ *store.Store, path, id string) {
			if got := apitest.ApplyAgentHook(t, path, id, "PreToolUse", ""); !got.Applied {
				t.Fatalf("hook not applied: %+v", got)
			}
		}},
		{"ended", store.StateEnded, func(t *testing.T, _ *store.Store, path, id string) {
			if got := apitest.ApplyAgentHook(t, path, id, "SessionEnd", ""); !got.Applied {
				t.Fatalf("end not applied: %+v", got)
			}
		}},
		{"missing", store.StateMissing, func(t *testing.T, s *store.Store, _, id string) {
			row, err := s.GetSpawn(id)
			if err != nil {
				t.Fatal(err)
			}
			if _, res, err := s.MarkMissingIfSameLife(id, row.Snapshot); err != nil || res != store.CondApplied {
				t.Fatalf("mark missing=%v %v", res, err)
			}
		}},
	}
	for _, w := range writes {
		for _, tc := range []struct {
			name string
			opts []apitest.SpawnOption
			// partial marks a valid document of no valid shape, which only a
			// write that records an exit for it can meet.
			partial bool
		}{
			{"malformed observation", []apitest.SpawnOption{apitest.WithStuckObservation("{not json")}, false},
			{"shapeless stuck observation", []apitest.SpawnOption{apitest.WithStuckObservation(`{"state":"stuck"}`)}, true},
			{"malformed own exit", []apitest.SpawnOption{apitest.WithStuckExit(`{"new_state":`)}, false},
			{"own exit into stuck", []apitest.SpawnOption{apitest.WithStuckExit(`{"new_state":"stuck","reason":"permission_unanswered","at_ms":1792411205000}`)}, false},
			{"own exit with another reason", []apitest.SpawnOption{apitest.WithStuckExit(`{"new_state":"working","reason":"other","at_ms":1792411205000}`)}, false},
			{"own exit without a time", []apitest.SpawnOption{apitest.WithStuckExit(`{"new_state":"working","reason":"permission_unanswered","at_ms":0}`)}, false},
		} {
			if tc.partial && w.name == "hook" {
				continue
			}
			t.Run(w.name+"/"+tc.name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "state.db")
				id, err := apitest.SeedSpawn(path, "lifecycle", store.StateCheckPermission, "", "on", "", true, tc.opts...)
				if err != nil {
					t.Fatal(err)
				}
				s, err := store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = s.Close() })
				mark := store.TrailMark(t)
				w.write(t, s, path, id)
				after, err := s.GetSpawn(id)
				if err != nil {
					t.Fatal(err)
				}
				if after.State != w.state {
					t.Errorf("state=%q; want %q", after.State, w.state)
				}
				if after.StuckObservation != "" {
					t.Errorf("observation kept: %s", after.StuckObservation)
				}
				if raw := storedStuckExit(t, path, id); raw.Valid {
					t.Errorf("unreadable exit kept: %q", raw.String)
				}
				if events := store.TrailEventsSince(t, mark, "ad.spawn.stuck_transition", id); len(events) != 0 {
					t.Errorf("emitted for metadata it could not read: %v", events)
				}
			})
		}
	}
	// Positive control: a readable pending exit of the row's own is emitted by
	// the same write, so the cases above are silent because of what they hold.
	t.Run("hook/readable own exit is emitted", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.db")
		id, err := apitest.SeedSpawn(path, "lifecycle", store.StateCheckPermission, "", "on", "", true, apitest.WithStuckExit(validExit))
		if err != nil {
			t.Fatal(err)
		}
		mark := store.TrailMark(t)
		if got := apitest.ApplyAgentHook(t, path, id, "PreToolUse", ""); !got.Applied {
			t.Fatalf("hook not applied: %+v", got)
		}
		events := store.TrailEventsSince(t, mark, "ad.spawn.stuck_transition", id)
		if len(events) != 1 || events[0]["new_state"] != store.StateWorking || events[0]["ts"] != time.UnixMilli(1792411205000).UTC().Format(lifecycleTS) {
			t.Fatalf("events=%v; want the pending exit once", events)
		}
		if raw := storedStuckExit(t, path, id); raw.Valid {
			t.Errorf("emitted exit still pending: %q", raw.String)
		}
	})
}

// A read that has nothing to change writes nothing even while an episode is
// displayed: the version, the observation and the requests stay as committed,
// and the answer needs no write lock.
func TestPermissionStuckDisplayedEpisodeReadWritesNothing(t *testing.T) {
	s, path, id, samples, now, committed := displayedStuck(t)
	pr, err := s.GetPermissionRequest(id, samples[0].Request.RequestToken)
	if err != nil {
		t.Fatal(err)
	}
	samples[0].Request = pr
	row, err := s.GetSpawnSettled(id, store.SettleRead{})
	if err != nil {
		t.Fatal(err)
	}
	mark := store.TrailMark(t)
	release := apitest.HoldWriteLock(t, path, time.Minute)
	defer release()
	for _, at := range []time.Time{now, now.Add(time.Millisecond), now.Add(time.Hour)} {
		got, err := s.ObservePermissionStuck(row, samples, at, 30*time.Second, store.SettleRead{})
		if err != nil || got != committed.CommittedStuckDisplay() {
			t.Fatalf("read at %s=%+v %v; want the committed display without a write", at, got, err)
		}
	}
	release()
	after, err := s.GetSpawn(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.StuckVersion != committed.StuckVersion || after.StuckObservation != committed.StuckObservation {
		t.Errorf("read wrote: before=%+v after=%+v", committed, after)
	}
	if stored, err := s.GetPermissionRequest(id, pr.RequestToken); err != nil || stored != pr {
		t.Errorf("read wrote request: before=%+v after=%+v %v", pr, stored, err)
	}
	if events := store.TrailEventsSince(t, mark, "ad.spawn.stuck_transition", id); len(events) != 0 {
		t.Errorf("events=%v; want none", events)
	}
}

// An unconfirmed COMMIT (errCommitUnconfirmed) is settled by one nonwaiting
// re-read at either observer: when the write landed the caller gets the
// committed display and the transition is emitted once; otherwise it gets the
// previous view with the error, and nothing is stored or emitted.
func TestStuckObservationUnconfirmedCommit(t *testing.T) {
	type observation func(store.Spawn) (store.StuckDisplay, error)
	for _, obs := range []struct {
		name, prior, reason string
		setup               func(*testing.T) (*store.Store, string, string, observation) // a row the observation moves to stuck
	}{
		{"permission", store.StateCheckPermission, store.StuckPermissionUnanswered, func(t *testing.T) (*store.Store, string, string, observation) {
			s, path, id, samples, now := permissionObservation(t)
			return s, path, id, func(row store.Spawn) (store.StuckDisplay, error) {
				return s.ObservePermissionStuck(row, samples, now, 30*time.Second, store.SettleRead{})
			}
		}},
		{"pane", store.StateWorking, store.StuckNoScreenChange, func(t *testing.T) (*store.Store, string, string, observation) {
			path := filepath.Join(t.TempDir(), "state.db")
			now := time.Date(2026, 10, 19, 23, 59, 59, 123000000, time.UTC)
			// The committed baseline has been unchanged for an hour.
			id, err := apitest.SeedSpawn(path, "", store.StateWorking, "", "off", "", true, apitest.WithPermissionEpoch(7),
				apitest.WithPaneBaseline("abc", now.Add(-time.Hour).UnixMilli(), int64(7)))
			if err != nil {
				t.Fatal(err)
			}
			s, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s, path, id, func(row store.Spawn) (store.StuckDisplay, error) {
				return s.ObservePaneStuck(row, store.PaneSample{SHA256: "abc"}, now, time.Minute, store.SettleRead{})
			}
		}},
	} {
		for _, tc := range []struct {
			name   string
			landed bool
		}{{"landed", true}, {"not landed", false}} {
			t.Run(obs.name+", "+tc.name, func(t *testing.T) {
				s, path, id, observe := obs.setup(t)
				row, err := s.GetSpawnSettled(id, store.SettleRead{})
				if err != nil {
					t.Fatal(err)
				}
				requests, err := s.PermissionRequestsForSpawn(id)
				if err != nil {
					t.Fatal(err)
				}
				// Only the observation's own write transaction is disturbed: it
				// begins with BEGIN IMMEDIATE, and its COMMIT is the one affected.
				var writing atomic.Bool
				lost := errors.New("injected: COMMIT response lost")
				before := func(_ context.Context, q string) error {
					if q == "BEGIN IMMEDIATE" {
						writing.Store(true)
					}
					// The COMMIT never reaches SQLite, so the transaction rolls back.
					if !tc.landed && q == "COMMIT" && writing.CompareAndSwap(true, false) {
						return lost
					}
					return nil
				}
				afterCommit := func(context.Context) error {
					// SQLite has committed; only its answer is uncertain.
					if tc.landed && writing.CompareAndSwap(true, false) {
						return lost
					}
					return nil
				}
				store.ObserveDecisionBoundaryForTest(t, s, path, before, afterCommit)
				mark := store.TrailMark(t)
				got, err := observe(row)
				reader, rerr := store.Open(path)
				if rerr != nil {
					t.Fatal(rerr)
				}
				defer reader.Close()
				after, rerr := reader.GetSpawn(id)
				if rerr != nil {
					t.Fatal(rerr)
				}
				events := store.TrailEventsSince(t, mark, "ad.spawn.stuck_transition", id)
				if tc.landed {
					if err != nil {
						t.Fatalf("landed observation=%+v %v; want the committed display and no error", got, err)
					}
					if want := after.CommittedStuckDisplay(); got.State != store.StateStuck || got.Reason != obs.reason || got != want {
						t.Errorf("display=%+v; want the committed %s entry %+v", got, obs.reason, want)
					}
					if after.StuckVersion != row.StuckVersion+1 {
						t.Errorf("version=%d; want %d", after.StuckVersion, row.StuckVersion+1)
					}
					if len(events) != 1 || events[0]["prior_state"] != obs.prior || events[0]["new_state"] != store.StateStuck || events[0]["stuck_reason"] != obs.reason {
						t.Errorf("events=%v; want the %s entry from %s once", events, obs.reason, obs.prior)
					}
					return
				}
				if !errors.Is(err, store.ErrCommitUnconfirmedForTest) {
					t.Fatalf("observation=%+v %v; want the unconfirmed-commit error", got, err)
				}
				if got != row.CommittedStuckDisplay() {
					t.Errorf("display=%+v; want the previous %+v", got, row.CommittedStuckDisplay())
				}
				if after.StuckVersion != row.StuckVersion || after.StuckObservation != row.StuckObservation {
					t.Errorf("row changed though nothing landed: %+v", after)
				}
				if stored, rerr := reader.PermissionRequestsForSpawn(id); rerr != nil || !reflect.DeepEqual(stored, requests) {
					t.Errorf("requests=%+v %v; want them unchanged %+v", stored, rerr, requests)
				}
				if len(events) != 0 {
					t.Errorf("events=%v; want none", events)
				}
			})
		}
	}
}

// FallbackClock is the one rule for a fallen-back request's clock, and
// RecordPermissionFallbacks persists exactly what it answers.
func TestFallbackClockIsWhatRecordPermissionFallbacksPersists(t *testing.T) {
	at := time.Date(2026, 10, 19, 23, 59, 59, 123000000, time.UTC)
	created := time.Date(2026, 10, 19, 22, 59, 59, 0, time.UTC) // stored with second precision
	admitted := at.Add(-50 * time.Minute)
	settled := at.Add(-5 * time.Minute)
	confirmBy := at.Add(-4 * time.Minute)
	gone := at.Add(-2 * time.Minute)
	stored := at.Add(-20 * time.Minute)
	v7 := apitest.PermissionTimes(created, admitted, settled, time.Time{}, time.Time{}, "")
	for _, tc := range []struct {
		name        string
		agent       string
		relay       string
		facts       []apitest.PermissionOption
		obs         store.FallbackObservation
		wantStart   time.Time
		wantBasis   string
		wantGoneAt  time.Time
		persistsNil bool // no clock is stored at all
	}{
		{name: "live hook", agent: store.AgentClaude, relay: "on", facts: []apitest.PermissionOption{v7},
			obs: store.FallbackObservation{ConfirmBy: confirmBy}, wantGoneAt: at, persistsNil: true},
		{name: "process gone, time not recorded", agent: store.AgentClaude, relay: "on", facts: []apitest.PermissionOption{v7},
			obs: store.FallbackObservation{ConfirmBy: confirmBy, ProcessGone: true}, wantStart: at, wantBasis: store.FallbackProcess, wantGoneAt: at},
		{name: "process gone, time recorded", agent: store.AgentClaude, relay: "on",
			facts: []apitest.PermissionOption{apitest.PermissionTimes(created, admitted, settled, gone, time.Time{}, "")},
			obs:   store.FallbackObservation{ConfirmBy: confirmBy, ProcessGone: true}, wantStart: gone, wantBasis: store.FallbackProcess, wantGoneAt: gone},
		{name: "hook could not be checked", agent: store.AgentClaude, relay: "on", facts: []apitest.PermissionOption{v7},
			obs: store.FallbackObservation{ConfirmBy: confirmBy, CantTell: true}, wantStart: confirmBy, wantBasis: store.FallbackDeadline, wantGoneAt: at},
		{name: "cursor with relay", agent: store.AgentCursor, relay: "on", facts: []apitest.PermissionOption{v7},
			obs: store.FallbackObservation{ConfirmBy: confirmBy}, wantStart: confirmBy, wantBasis: store.FallbackDeadline, wantGoneAt: at},
		{name: "cursor without relay, admitted", agent: store.AgentCursor, relay: "off", facts: []apitest.PermissionOption{v7},
			obs: store.FallbackObservation{ConfirmBy: confirmBy}, wantStart: admitted, wantBasis: store.FallbackAdmission, wantGoneAt: at},
		{name: "cursor without relay, no admission", agent: store.AgentCursor, relay: "off",
			facts: []apitest.PermissionOption{apitest.PermissionTimes(created, time.Time{}, settled, time.Time{}, time.Time{}, "")},
			obs:   store.FallbackObservation{ConfirmBy: confirmBy}, wantStart: created, wantBasis: store.FallbackAdmission, wantGoneAt: at},
		{name: "recorded before schema v7", agent: store.AgentClaude, relay: "on",
			facts: []apitest.PermissionOption{apitest.PermissionTimes(created, admitted, time.Time{}, time.Time{}, time.Time{}, "")},
			obs:   store.FallbackObservation{ConfirmBy: confirmBy}, wantStart: confirmBy, wantBasis: store.FallbackDeadline, wantGoneAt: at},
		{name: "stored clock kept", agent: store.AgentClaude, relay: "on",
			facts: []apitest.PermissionOption{apitest.PermissionTimes(created, admitted, settled, time.Time{}, stored, store.FallbackDeadline)},
			obs:   store.FallbackObservation{ConfirmBy: confirmBy, ProcessGone: true}, wantStart: stored, wantBasis: store.FallbackDeadline, wantGoneAt: at},
		{name: "time not positive", agent: store.AgentClaude, relay: "on", facts: []apitest.PermissionOption{v7},
			obs: store.FallbackObservation{ConfirmBy: time.UnixMilli(0), CantTell: true}, wantGoneAt: at, persistsNil: true},
		{name: "time beyond year 9999", agent: store.AgentClaude, relay: "on", facts: []apitest.PermissionOption{v7},
			obs: store.FallbackObservation{ConfirmBy: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), CantTell: true}, wantGoneAt: at, persistsNil: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			id, err := apitest.SeedSpawn(path, "fallback", store.StateCheckPermission, "", "on", "", true)
			if err != nil {
				t.Fatal(err)
			}
			seed, err := apitest.SeedPermissionRequest(path, id, "Bash", append([]apitest.PermissionOption{apitest.PermissionToken(storefix.TestRequestTokenA)}, tc.facts...)...)
			if err != nil {
				t.Fatal(err)
			}
			if tc.agent != store.AgentClaude || tc.relay != "on" {
				execRaw(t, path, `UPDATE spawns SET agent=?, relay_mode=? WHERE instance_id=?`, tc.agent, tc.relay, id)
			}
			s, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			pr, err := s.GetPermissionRequest(id, seed.RequestToken)
			if err != nil {
				t.Fatal(err)
			}
			tc.obs.RequestID = pr.RequestID
			start, basis := store.FallbackClock(tc.agent, tc.relay, pr, tc.obs, at)
			if !start.Equal(tc.wantStart) || basis != tc.wantBasis {
				t.Fatalf("FallbackClock=%s %q; want %s %q", start, basis, tc.wantStart, tc.wantBasis)
			}
			goneAt, err := s.RecordPermissionFallbacks(at, store.DefaultLockWait, []store.FallbackObservation{tc.obs})
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.GetPermissionRequest(id, seed.RequestToken)
			if err != nil {
				t.Fatal(err)
			}
			if !got.FallbackAt.Equal(start) || got.FallbackBasis != basis {
				t.Errorf("persisted clock=%s %q; FallbackClock said %s %q", got.FallbackAt, got.FallbackBasis, start, basis)
			}
			if tc.persistsNil && (!got.FallbackAt.IsZero() || got.FallbackBasis != "") {
				t.Errorf("a clock was stored for a request that has none: %s %q", got.FallbackAt, got.FallbackBasis)
			}
			if !got.HookGoneAt.Equal(tc.wantGoneAt) || !goneAt[pr.RequestID].Equal(tc.wantGoneAt) {
				t.Errorf("hook_gone_at=%s (returned %s); want %s", got.HookGoneAt, goneAt[pr.RequestID], tc.wantGoneAt)
			}
		})
	}
}

// A request recorded before schema v7 whose creation time is unknown has no
// clock whatever its basis: there is nothing to start it from.
func TestFallbackClockOfPreV7RequestWithoutCreationTime(t *testing.T) {
	at := time.Date(2026, 10, 19, 23, 59, 59, 123000000, time.UTC)
	for _, o := range []store.FallbackObservation{
		{ConfirmBy: at.Add(-time.Minute), CantTell: true},
		{ConfirmBy: at.Add(-time.Minute), ProcessGone: true},
	} {
		if start, basis := store.FallbackClock(store.AgentClaude, "on", store.PermissionRow{}, o, at); !start.IsZero() || basis != "" {
			t.Errorf("%+v: clock=%s %q; want none", o, start, basis)
		}
	}
}
