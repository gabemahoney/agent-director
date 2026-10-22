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

// storedDisplayExit is id's pending display_exit as the database holds it.
func storedDisplayExit(t *testing.T, path, id string) sql.NullString {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw sql.NullString
	if err := db.QueryRow(`SELECT display_exit FROM spawns WHERE instance_id=?`, id).Scan(&raw); err != nil {
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

// displayedRow returns permissionObservation's row displaying state as
// committed: the needs_intelligence entry its observation commits, or the
// idle_screen display a hook into check_permission left standing.
func displayedRow(t *testing.T, state string) (*store.Store, string, string, []store.PermissionSample, time.Time, store.Spawn) {
	t.Helper()
	var opts []apitest.SpawnOption
	if state == store.StateIdleScreen {
		opts = append(opts, apitest.WithDisplayObservation(`{"state":"idle_screen","since_ms":1792411205000,"unchanged_minutes":20,`+
			`"candidate":"","candidate_since_ms":0,"observed_at_ms":1792411205000,"permission_epoch":6}`))
	}
	s, path, id, samples, now := permissionObservation(t, opts...)
	if state == store.StateNeedsIntelligence {
		row, err := s.GetSpawnSettled(id, store.SettleRead{})
		if err != nil {
			t.Fatal(err)
		}
		if display := observeUnlessBusy(t, s, row, samples, now, 30*time.Second); display.State != state {
			t.Fatalf("setup entry=%+v", display)
		}
	}
	committed, err := s.GetSpawn(id)
	if err != nil {
		t.Fatal(err)
	}
	if committed.CommittedDisplay().State != state {
		t.Fatalf("setup row is not displayed %s: %+v", state, committed)
	}
	return s, path, id, samples, now, committed
}

// Find-missing's repair of a check_permission row whose dialog is gone ends a
// display of either state in the same transaction: the exit is recorded with
// the repair's own epoch and version change, and is emitted once, at the
// repair's commit time, after the commit.
func TestRepairCheckPermissionEndsDisplayImmediately(t *testing.T) {
	for _, state := range []string{store.StateNeedsIntelligence, store.StateIdleScreen} {
		t.Run(state, func(t *testing.T) {
			s, path, id, samples, now, committed := displayedRow(t, state)
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
			if after.State != store.StateWorking || after.DisplayObservation != "" || after.CommittedDisplay().State != store.StateWorking {
				t.Errorf("repaired row=%+v; want working with no observation", after)
			}
			if after.ObservationVersion != committed.ObservationVersion+1 || after.PermissionEpoch != committed.PermissionEpoch+1 {
				t.Errorf("version %d epoch %d; want %d and %d", after.ObservationVersion, after.PermissionEpoch, committed.ObservationVersion+1, committed.PermissionEpoch+1)
			}
			if raw := storedDisplayExit(t, path, id); raw.Valid {
				t.Errorf("exit still pending after its emission: %q", raw.String)
			}
			_, stamp, err := apitest.ReadSettle(path, id)
			if err != nil {
				t.Fatal(err)
			}
			assertDisplayRecords(t, mark, id, displayRecord{state, store.StateWorking, time.UnixMilli(stamp)})
		})
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
	assertDisplayRecords(t, mark, id)
	if raw := storedDisplayExit(t, path, id); raw.Valid {
		t.Errorf("exit recorded for an ordinary row: %q", raw.String)
	}
}

// Metadata the store cannot read, an earlier encoding's included, reads as no
// display and is never a reason to refuse a lifecycle write: a hook, an end or
// a missing mark applies, leaves no observation and emits nothing for what it
// could not read. Writes judge a stored value exactly as reads do: a NUL makes
// an observation unreadable, and a BLOB holding a valid one shows its display,
// which a hook keeps and an end or a missing mark exits.
func TestLifecycleWritesIgnoreUnreadableDisplayMetadata(t *testing.T) {
	validExit := `{"prior_state":"needs_intelligence","new_state":"working","at_ms":1792411205000}`
	validObservation := `{"state":"needs_intelligence","since_ms":1792411205000,"request_token":"req-a","tool_name":"Bash",` +
		`"candidate":"","candidate_since_ms":0,"observed_at_ms":1792411205000,"permission_epoch":0}`
	shown := store.DisplayDetail{State: store.StateNeedsIntelligence, SinceMillis: 1792411205000, RequestToken: "req-a", ToolName: "Bash"}
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
			name  string
			opts  []apitest.SpawnOption
			blob  bool // the observation is stored as a BLOB
			shown bool // reads show the observation's display (shown)
		}{
			{"malformed observation", []apitest.SpawnOption{apitest.WithDisplayObservation("{not json")}, false, false},
			{"shapeless displayed observation", []apitest.SpawnOption{apitest.WithDisplayObservation(`{"state":"needs_intelligence"}`)}, false, false},
			{"earlier encoding's observation", []apitest.SpawnOption{apitest.WithDisplayObservation(`{"state":"stuck","reason":"permission_unanswered","since_ms":1792411205000,` +
				`"request_token":"req-old","tool_name":"Bash","candidate":"stuck","candidate_reason":"no_screen_change","candidate_since_ms":1792411205000,` +
				`"observed_at_ms":1792411205000,"permission_epoch":0}`)}, false, false},
			{"observation with a NUL", []apitest.SpawnOption{apitest.WithDisplayObservation(validObservation + "\x00x")}, false, false},
			{"BLOB observation with a NUL", []apitest.SpawnOption{apitest.WithDisplayObservation(validObservation + "\x00x")}, true, false},
			{"BLOB observation", []apitest.SpawnOption{apitest.WithDisplayObservation(validObservation)}, true, true},
			{"malformed own exit", []apitest.SpawnOption{apitest.WithDisplayExit(`{"prior_state":`)}, false, false},
			{"own exit into a Displayed State", []apitest.SpawnOption{apitest.WithDisplayExit(`{"prior_state":"needs_intelligence","new_state":"idle_screen","at_ms":1792411205000}`)}, false, false},
			{"own exit from an ordinary state", []apitest.SpawnOption{apitest.WithDisplayExit(`{"prior_state":"working","new_state":"waiting","at_ms":1792411205000}`)}, false, false},
			{"earlier encoding's own exit", []apitest.SpawnOption{apitest.WithDisplayExit(`{"new_state":"working","reason":"permission_unanswered","at_ms":1792411205000}`)}, false, false},
			{"own exit without a time", []apitest.SpawnOption{apitest.WithDisplayExit(`{"prior_state":"idle_screen","new_state":"working","at_ms":0}`)}, false, false},
		} {
			t.Run(w.name+"/"+tc.name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "state.db")
				id, err := apitest.SeedSpawn(path, "lifecycle", store.StateCheckPermission, "", "on", "", true, tc.opts...)
				if err != nil {
					t.Fatal(err)
				}
				if tc.blob {
					execRaw(t, path, `UPDATE spawns SET display_observation = CAST(display_observation AS BLOB) WHERE instance_id=?`, id)
				}
				s, err := store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = s.Close() })
				want := store.DisplayDetail{State: store.StateCheckPermission}
				if tc.shown {
					want = shown
				}
				if seeded, err := s.GetSpawn(id); err != nil || seeded.CommittedDisplay() != want {
					t.Fatalf("seeded row displays %+v %v; want %+v", seeded.CommittedDisplay(), err, want)
				}
				mark := store.TrailMark(t)
				w.write(t, s, path, id)
				after, err := s.GetSpawn(id)
				if err != nil {
					t.Fatal(err)
				}
				if after.State != w.state {
					t.Errorf("state=%q; want %q", after.State, w.state)
				}
				if tc.shown && w.name == "hook" {
					if after.CommittedDisplay() != shown {
						t.Errorf("display=%+v; want %+v kept", after.CommittedDisplay(), shown)
					}
				} else if after.DisplayObservation != "" {
					t.Errorf("observation kept: %s", after.DisplayObservation)
				}
				if raw := storedDisplayExit(t, path, id); raw.Valid {
					t.Errorf("exit kept: %q", raw.String)
				}
				var records []displayRecord
				if tc.shown && w.name != "hook" {
					_, stamp, err := apitest.ReadSettle(path, id)
					if err != nil {
						t.Fatal(err)
					}
					records = append(records, displayRecord{store.StateNeedsIntelligence, w.state, time.UnixMilli(stamp)})
				}
				assertDisplayRecords(t, mark, id, records...)
			})
		}
	}
	// Positive control: a readable pending exit of the row's own is emitted by
	// the same write, so the cases above are silent because of what they hold.
	t.Run("hook/readable own exit is emitted", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.db")
		id, err := apitest.SeedSpawn(path, "lifecycle", store.StateCheckPermission, "", "on", "", true, apitest.WithDisplayExit(validExit))
		if err != nil {
			t.Fatal(err)
		}
		mark := store.TrailMark(t)
		if got := apitest.ApplyAgentHook(t, path, id, "PreToolUse", ""); !got.Applied {
			t.Fatalf("hook not applied: %+v", got)
		}
		assertDisplayRecords(t, mark, id, displayRecord{store.StateNeedsIntelligence, store.StateWorking, time.UnixMilli(1792411205000)})
		if raw := storedDisplayExit(t, path, id); raw.Valid {
			t.Errorf("emitted exit still pending: %q", raw.String)
		}
	})
}

// A read that has nothing to change writes nothing even while an episode is
// displayed: the version, the observation and the requests stay as committed,
// and the answer needs no write lock.
func TestNeedsIntelligenceDisplayedEpisodeReadWritesNothing(t *testing.T) {
	s, path, id, samples, now, committed := displayedRow(t, store.StateNeedsIntelligence)
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
		got, err := s.ObserveNeedsIntelligence(row, samples, at, 30*time.Second, store.SettleRead{})
		if err != nil || got != committed.CommittedDisplay() {
			t.Fatalf("read at %s=%+v %v; want the committed display without a write", at, got, err)
		}
	}
	release()
	after, err := s.GetSpawn(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.ObservationVersion != committed.ObservationVersion || after.DisplayObservation != committed.DisplayObservation {
		t.Errorf("read wrote: before=%+v after=%+v", committed, after)
	}
	if stored, err := s.GetPermissionRequest(id, pr.RequestToken); err != nil || stored != pr {
		t.Errorf("read wrote request: before=%+v after=%+v %v", pr, stored, err)
	}
	assertDisplayRecords(t, mark, id)
}

// An unconfirmed COMMIT (errCommitUnconfirmed) is settled by one nonwaiting
// re-read at either observer: when the write landed the caller gets the
// committed display and the transition is emitted once; otherwise it gets the
// previous view with the error, and nothing is stored or emitted.
func TestDisplayObservationUnconfirmedCommit(t *testing.T) {
	type observation func(store.Spawn) (store.DisplayDetail, error)
	for _, obs := range []struct {
		name, prior, display string
		setup                func(*testing.T) (*store.Store, string, string, time.Time, observation) // a row the observation moves to display
	}{
		{"permission", store.StateCheckPermission, store.StateNeedsIntelligence, func(t *testing.T) (*store.Store, string, string, time.Time, observation) {
			s, path, id, samples, now := permissionObservation(t)
			return s, path, id, now, func(row store.Spawn) (store.DisplayDetail, error) {
				return s.ObserveNeedsIntelligence(row, samples, now, 30*time.Second, store.SettleRead{})
			}
		}},
		{"pane", store.StateWorking, store.StateIdleScreen, func(t *testing.T) (*store.Store, string, string, time.Time, observation) {
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
			return s, path, id, now, func(row store.Spawn) (store.DisplayDetail, error) {
				return s.ObserveIdleScreen(row, store.PaneSample{SHA256: "abc"}, now, time.Minute, store.SettleRead{})
			}
		}},
	} {
		for _, tc := range []struct {
			name   string
			landed bool
		}{{"landed", true}, {"not landed", false}} {
			t.Run(obs.name+", "+tc.name, func(t *testing.T) {
				s, path, id, now, observe := obs.setup(t)
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
				if tc.landed {
					if err != nil {
						t.Fatalf("landed observation=%+v %v; want the committed display and no error", got, err)
					}
					if want := after.CommittedDisplay(); got.State != obs.display || got != want {
						t.Errorf("display=%+v; want the committed %s entry %+v", got, obs.display, want)
					}
					if after.ObservationVersion != row.ObservationVersion+1 {
						t.Errorf("version=%d; want %d", after.ObservationVersion, row.ObservationVersion+1)
					}
					assertDisplayRecords(t, mark, id, displayRecord{obs.prior, obs.display, now})
					return
				}
				if !errors.Is(err, store.ErrCommitUnconfirmedForTest) {
					t.Fatalf("observation=%+v %v; want the unconfirmed-commit error", got, err)
				}
				if got != row.CommittedDisplay() {
					t.Errorf("display=%+v; want the previous %+v", got, row.CommittedDisplay())
				}
				if after.ObservationVersion != row.ObservationVersion || after.DisplayObservation != row.DisplayObservation {
					t.Errorf("row changed though nothing landed: %+v", after)
				}
				if stored, rerr := reader.PermissionRequestsForSpawn(id); rerr != nil || !reflect.DeepEqual(stored, requests) {
					t.Errorf("requests=%+v %v; want them unchanged %+v", stored, rerr, requests)
				}
				assertDisplayRecords(t, mark, id)
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
