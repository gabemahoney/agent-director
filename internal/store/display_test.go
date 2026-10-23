package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.ob8 F1: the observer's new family refresh, and its read of the families
// that refresh changed, are part of the same transaction as its display and
// baseline. A failure after the display UPDATE cannot publish half an exit,
// remove an ancestor's contribution, or emit an event; a committed exit
// returns the member's and ancestor's families as that commit left them.
func TestDisplayFamilyRefreshFailureAtomic(t *testing.T) {
	for _, observer := range []string{"permission", "pane"} {
		for _, failure := range []string{"held lock", "observation update", "family refresh", "family read", "none"} {
			t.Run(observer+"/"+failure, func(t *testing.T) {
				s, path, id, samples, now := permissionObservation(t)
				parent, err := apitest.SeedSpawn(path, "parent", store.StateWaiting, "", "off", "", false)
				if err != nil {
					t.Fatal(err)
				}
				if err := apitest.SeedParentChild(path, parent, id); err != nil {
					t.Fatal(err)
				}
				row, err := s.GetSpawn(id)
				if err != nil {
					t.Fatal(err)
				}
				var shown store.DisplayDetail
				wantDisplay := store.StateNeedsIntelligence
				if observer == "permission" {
					shown = observeUnlessBusy(t, s, row, samples, now, 30*time.Second)
				} else {
					wantDisplay = store.StateIdleScreen
					if err := apitest.SetPermissionFacts(path, samples[0].Request.RequestID, apitest.PermissionProof(now)); err != nil {
						t.Fatal(err)
					}
					if a := storefix.ApplyAgentHook(t, s, id, "UserPromptSubmit", ""); !a.Applied {
						t.Fatalf("working hook=%+v", a)
					}
					for i := 0; i < 2; i++ {
						row, err = s.GetSpawn(id)
						if err != nil {
							t.Fatal(err)
						}
						shown = commitUnlessBusy(t, func() (store.DisplayDetail, error) {
							d, families, err := s.ObserveIdleScreen(row, store.PaneSample{SHA256: "unchanged-pane"}, now, time.Minute, store.SettleRead{})
							if families != nil {
								t.Errorf("raw working sample changed no contribution, yet returned families %v", families)
							}
							return d, err
						})
						if i == 0 {
							now = now.Add(time.Minute)
						}
					}
				}
				if shown.State != wantDisplay {
					t.Fatalf("entry=%+v", shown)
				}
				if err := apitest.SetPermissionFacts(path, samples[0].Request.RequestID, apitest.PermissionProof(now)); err != nil {
					t.Fatal(err)
				}
				if hook := storefix.ApplyAgentHook(t, s, id, "Stop", ""); !hook.Applied {
					t.Fatalf("Stop=%+v", hook)
				}
				row, err = s.GetSpawn(id)
				if err != nil || row.State != store.StateWaiting || row.CommittedDisplay() != shown {
					t.Fatalf("retained=%+v %v", row, err)
				}
				requests, err := s.PermissionRequestsForSpawn(id)
				if err != nil {
					t.Fatal(err)
				}
				samples = nil
				for _, pr := range requests {
					samples = append(samples, store.PermissionSample{Request: pr})
				}
				before := map[string][3]any{}
				for _, member := range []string{parent, id} {
					a, b, stamp, err := apitest.ReadFamilyHistory(path, member)
					if err != nil {
						t.Fatal(err)
					}
					before[member] = [3]any{a, b, stamp}
				}
				blocked := errors.New("fixture: family refresh failed")
				familyAttempted := false
				switch failure {
				case "held lock":
					release := apitest.HoldWriteLock(t, path, time.Minute)
					defer release()
				case "observation update":
					storefix.InjectWriteFailure(t, path, storefix.WriteFailDisplayObservation, id)
				case "family refresh", "family read":
					// The refresh updates histories; the read follows it in the
					// same transaction, so its failure must undo that update too.
					prefix := "WITH RECURSIVE working(id)"
					if failure == "family read" {
						prefix = "WITH RECURSIVE chain(id)"
					}
					store.ObserveDecisionBoundaryForTest(t, s, path, func(_ context.Context, q string) error {
						if strings.HasPrefix(q, prefix) {
							familyAttempted = true
							return blocked
						}
						return nil
					}, nil)
				}
				mark := store.TrailMark(t)
				var got store.DisplayDetail
				var families map[string]string
				// Both store observer entry points may receive a row whose raw
				// state no longer qualifies; each must atomically clear its display.
				repeatBusy := failure == "family refresh" || failure == "family read" || failure == "none"
				_, _, attempted := apitest.RepeatUntilCommitted(func() bool {
					if observer == "permission" {
						got, families, err = s.ObserveNeedsIntelligence(row, samples, now.Add(time.Hour), time.Second, store.SettleRead{})
					} else {
						got, families, err = s.ObserveIdleScreen(row, store.PaneSample{}, now.Add(time.Hour), time.Second, store.SettleRead{})
					}
					return !repeatBusy || !errors.Is(err, store.ErrStoreBusy)
				})
				if failure == "none" {
					wantFamilies := map[string]string{parent: store.StateWaiting, id: store.StateWaiting}
					if !attempted || err != nil || got != (store.DisplayDetail{State: store.StateWaiting}) || !reflect.DeepEqual(families, wantFamilies) {
						t.Fatalf("committed exit=%+v families=%v %v; want waiting with families %v", got, families, err, wantFamilies)
					}
					for member := range before {
						a, b, stamp, err := apitest.ReadFamilyHistory(path, member)
						if err != nil || a != store.StateWaiting || b != store.StateWorking || stamp == 0 || stamp == before[member][2] {
							t.Errorf("committed exit family %s: %s/%s/%d %v; want a new waiting/working change", member, a, b, stamp, err)
						}
					}
					assertDisplayRecords(t, mark, id, displayRecord{wantDisplay, store.StateWaiting, now.Add(time.Hour)})
					return
				}
				if !attempted || err == nil || got != shown || families != nil {
					t.Fatalf("failed exit=%+v families=%v %v; want retained %+v and no families", got, families, err, shown)
				}
				if (failure == "family refresh" || failure == "family read") && (!familyAttempted || !errors.Is(err, blocked)) {
					t.Fatalf("family failure was not reached: attempted=%v err=%v", familyAttempted, err)
				}
				if failure == "held lock" && !errors.Is(err, store.ErrStoreBusy) {
					t.Fatalf("held writer error=%v", err)
				}
				after, readErr := s.GetSpawn(id)
				if readErr != nil || !reflect.DeepEqual(after, row) {
					t.Fatalf("failed exit changed row: before=%+v after=%+v err=%v", row, after, readErr)
				}
				for member, want := range before {
					a, b, stamp, err := apitest.ReadFamilyHistory(path, member)
					if err != nil || [3]any{a, b, stamp} != want {
						t.Errorf("failed exit changed family %s: %s/%s/%d %v; want %v", member, a, b, stamp, err, want)
					}
				}
				assertDisplayRecords(t, mark, id)
			})
		}
	}
}

// permissionObservation seeds current ownership and samples exact persisted
// records; opts follow the row's defaults.
func permissionObservation(t *testing.T, opts ...apitest.SpawnOption) (*store.Store, string, string, []store.PermissionSample, time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	now := time.Date(2026, 10, 19, 23, 59, 59, 123000000, time.UTC)
	id, err := apitest.SeedSpawn(path, "permission", store.StateCheckPermission, "", "on", "", true, append([]apitest.SpawnOption{apitest.WithPermissionEpoch(7)}, opts...)...)
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

// observeUnlessBusy is ObserveNeedsIntelligence for a call that must commit,
// through commitUnlessBusy.
func observeUnlessBusy(t *testing.T, s *store.Store, row store.Spawn, samples []store.PermissionSample, now time.Time, timeout time.Duration) store.DisplayDetail {
	t.Helper()
	return commitUnlessBusy(t, func() (store.DisplayDetail, error) {
		d, _, err := s.ObserveNeedsIntelligence(row, samples, now, timeout, store.SettleRead{})
		return d, err
	})
}

// commitUnlessBusy runs observe, an observation that must commit. The write
// never waits for the write lock, so a busy store is the one failure that is
// repeated, at the same time and without sleeping, under
// apitest.RepeatUntilCommitted's bound; any other error fails the test at once.
func commitUnlessBusy(t *testing.T, observe func() (store.DisplayDetail, error)) store.DisplayDetail {
	t.Helper()
	var display store.DisplayDetail
	var err error
	attempts, elapsed, committed := apitest.RepeatUntilCommitted(func() bool {
		if display, err = observe(); err != nil && !errors.Is(err, store.ErrStoreBusy) {
			t.Fatalf("observation did not commit: %+v %v", display, err)
		}
		return err == nil
	})
	if !committed {
		t.Fatalf("observation did not commit in %d attempts over %v: %+v %v", attempts, elapsed, display, err)
	}
	if attempts > 1 {
		t.Logf("observation committed on attempt %d after %v", attempts, elapsed)
	}
	return display
}

// displayRecord is one ad.spawn.displayed_state_transition record a test expects.
type displayRecord struct {
	prior, next string
	at          time.Time
}

// assertDisplayRecords fails unless id's display records since mark are exactly want, field set
// included, with no ad.spawn.state_transition naming a Displayed State and no old-event record.
func assertDisplayRecords(t *testing.T, mark int, id string, want ...displayRecord) {
	t.Helper()
	var wantRows []map[string]any
	for _, w := range want {
		wantRows = append(wantRows, map[string]any{"event": "ad.spawn.displayed_state_transition", "instance_id": id,
			"prior_state": w.prior, "new_state": w.next, "ts": w.at.UTC().Format(lifecycleTS)})
	}
	if got := store.TrailEventsSince(t, mark, "ad.spawn.displayed_state_transition", id); !reflect.DeepEqual(got, wantRows) {
		t.Errorf("display records=%v; want %v", got, wantRows)
	}
	for _, row := range store.TrailEventsSince(t, mark, "ad.spawn.state_transition", id) {
		for _, state := range []any{row["prior_state"], row["new_state"]} {
			if state == store.StateNeedsIntelligence || state == store.StateIdleScreen {
				t.Errorf("ad.spawn.state_transition names a Displayed State: %v", row)
			}
		}
	}
	if old := store.TrailEventsSince(t, mark, "ad.spawn.stuck_transition", id); len(old) != 0 {
		t.Errorf("records under the old event name: %v", old)
	}
}

func TestNeedsIntelligenceStaleSampleCannotCommit(t *testing.T) {
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
			got, _, err := s.ObserveNeedsIntelligence(row, samples, now, 30*time.Second, store.SettleRead{})
			if err == nil || got != row.CommittedDisplay() {
				t.Fatalf("stale observation=%+v %v; want the previous %+v", got, err, row.CommittedDisplay())
			}
			after, err := s.GetSpawn(id)
			if err != nil {
				t.Fatal(err)
			}
			if after.DisplayObservation != "" {
				t.Errorf("stale observation wrote detail: %s", after.DisplayObservation)
			}
			assertDisplayRecords(t, mark, id)
		})
	}
}

func TestNeedsIntelligenceRollbackAndNonwaitingLock(t *testing.T) {
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
				storefix.InjectWriteFailure(t, path, storefix.WriteFailDisplayObservation, id)
			}
			// A write that waited would wait out the store's busy timeout (the
			// lock is held for a minute), so half of it separates the two
			// under suite load.
			limit := store.DefaultBusyTimeoutMs * time.Millisecond / 2
			start := time.Now()
			got, _, err := s.ObserveNeedsIntelligence(row, samples, now, 30*time.Second, store.SettleRead{})
			if elapsed := time.Since(start); elapsed >= limit {
				t.Errorf("nonwaiting write took %s; a write that waits for the lock takes the %d ms busy timeout", elapsed, store.DefaultBusyTimeoutMs)
			}
			if err == nil || got != row.CommittedDisplay() {
				t.Fatalf("failed observation=%+v %v; want %+v", got, err, row.CommittedDisplay())
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
			if after.DisplayObservation != row.DisplayObservation || after.ObservationVersion != row.ObservationVersion {
				t.Errorf("report persisted after failed write: %+v", after)
			}
			assertDisplayRecords(t, mark, id)
		})
	}
}

// A read with nothing to commit answers from its snapshot without taking the
// write lock; only a new clock or a timed-out request needs the transaction.
func TestNeedsIntelligenceReadWithoutChangeTakesNoWriteLock(t *testing.T) {
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
			got, _, err := s.ObserveNeedsIntelligence(row, samples, now, 30*time.Second, store.SettleRead{})
			if got != row.CommittedDisplay() || (err != nil) != tc.writes {
				t.Fatalf("observation=%+v err=%v; want %+v, needs write lock %v", got, err, row.CommittedDisplay(), tc.writes)
			}
			release()
			after, err := s.GetSpawn(id)
			if err != nil {
				t.Fatal(err)
			}
			if after.DisplayObservation != row.DisplayObservation || after.ObservationVersion != row.ObservationVersion {
				t.Errorf("read wrote report: before=%+v after=%+v", row, after)
			}
			if stored, err := s.GetPermissionRequest(id, pr.RequestToken); err != nil || stored != pr {
				t.Errorf("read wrote request: before=%+v after=%+v %v", pr, stored, err)
			}
		})
	}
}

func TestNeedsIntelligenceConcurrentSnapshotAndOlderClock(t *testing.T) {
	s, _, id, samples, now := permissionObservation(t)
	row, err := s.GetSpawnSettled(id, store.SettleRead{})
	if err != nil {
		t.Fatal(err)
	}
	mark := store.TrailMark(t)
	first := observeUnlessBusy(t, s, row, samples, now, 30*time.Second)
	if first.State != store.StateNeedsIntelligence {
		t.Fatalf("entry=%+v", first)
	}
	for _, at := range []time.Time{now, now.Add(time.Hour)} {
		if _, _, err := s.ObserveNeedsIntelligence(row, samples, at, 30*time.Second, store.SettleRead{}); err == nil {
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
	if got, _, err := s.ObserveNeedsIntelligence(fresh, samples, now.Add(-time.Millisecond), 30*time.Second, store.SettleRead{}); err == nil || !reflect.DeepEqual(got, first) {
		t.Errorf("older sample=%+v %v", got, err)
	}
	assertDisplayRecords(t, mark, id, displayRecord{store.StateCheckPermission, store.StateNeedsIntelligence, now})
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
			got := commitUnlessBusy(t, func() (store.DisplayDetail, error) {
				d, _, err := s.ObserveIdleScreen(row, store.PaneSample{SHA256: "abc"}, now, time.Minute, store.SettleRead{})
				return d, err
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

// TestRawHookDuringSettlingMoveKeepsDisplay: a raw state change while the
// other Displayed State settles in behind a displayed one clears only the
// candidate: the display and its committed detail stand until a read, which
// ends them with exactly one exit.
func TestRawHookDuringSettlingMoveKeepsDisplay(t *testing.T) {
	now := time.Date(2026, 10, 19, 23, 59, 59, 123000000, time.UTC)
	since := now.Add(-time.Hour).UnixMilli()
	for _, tc := range []struct {
		displayed, raw, detail, candidate string
	}{
		{store.StateNeedsIntelligence, store.StateWorking, fmt.Sprintf(`"request_token":%q,"tool_name":"Bash"`, storefix.TestRequestTokenA), store.StateIdleScreen},
		{store.StateIdleScreen, store.StateCheckPermission, `"unchanged_minutes":20`, store.StateNeedsIntelligence},
	} {
		t.Run(tc.displayed+" to "+tc.candidate, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			moving := fmt.Sprintf(`{"state":%q,"since_ms":%d,%s,"candidate":%q,"candidate_since_ms":%d,"observed_at_ms":%d,"permission_epoch":7}`,
				tc.displayed, since, tc.detail, tc.candidate, since+1000, since+1000)
			id, err := apitest.SeedSpawn(path, "", tc.raw, "", "off", "", true, apitest.WithPermissionEpoch(7), apitest.WithDisplayObservation(moving))
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
			displayed := seeded.CommittedDisplay()
			if displayed.State != tc.displayed {
				t.Fatalf("setup is not displayed %s: %+v", tc.displayed, seeded)
			}
			mark := store.TrailMark(t)
			if got := apitest.ApplyAgentHook(t, path, id, "Stop", ""); !got.Applied {
				t.Fatal(got)
			}
			row, err := s.GetSpawnSettled(id, store.SettleRead{})
			if err != nil || row.State != store.StateWaiting || row.CommittedDisplay() != displayed {
				t.Fatalf("after the hook: %+v %v; want raw waiting still displaying %+v", row, err, displayed)
			}
			var want, kept map[string]any
			if err := json.Unmarshal([]byte(moving), &want); err != nil {
				t.Fatal(err)
			}
			want["candidate"], want["candidate_since_ms"] = "", float64(0)
			if err := json.Unmarshal([]byte(row.DisplayObservation), &kept); err != nil || !reflect.DeepEqual(kept, want) {
				t.Fatalf("observation after the hook=%s %v; want only the candidate cleared: %v", row.DisplayObservation, err, want)
			}
			if got := observeUnlessBusy(t, s, row, nil, now, 30*time.Second); got != (store.DisplayDetail{State: store.StateWaiting}) {
				t.Fatalf("read=%+v; want the display settled out", got)
			}
			settled, err := s.GetSpawnSettled(id, store.SettleRead{})
			if err != nil || settled.DisplayObservation != "" {
				t.Fatalf("settled row=%+v %v; want no observation", settled, err)
			}
			if got, _, err := s.ObserveNeedsIntelligence(settled, nil, now.Add(time.Second), 30*time.Second, store.SettleRead{}); err != nil || got.State != store.StateWaiting {
				t.Fatalf("next read=%+v %v", got, err)
			}
			assertDisplayRecords(t, mark, id, displayRecord{tc.displayed, store.StateWaiting, now})
		})
	}
}
