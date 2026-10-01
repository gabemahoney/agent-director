package api_test

// spawn_reuse_restore_test.go covers reuse's restore after a failed launch at
// the verb (SR-10.4, SR-8.7; AC-REUSE-07): every non-timeout launch failure,
// on an ended and a missing row, restores the pre-reuse row byte for byte; a
// NULL ended_at becomes the failure time and a parent deleted meanwhile a NULL
// parent_id; the archive and the deleted permission requests stay; and
// resume, get, expire and a second reuse behave afterwards as before. The
// restore not applied is spawn_reuse_restore_cond_test.go's; the fixture is
// spawn_reuse_fixture_test.go.

import (
	"errors"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rrRestored is before after an applied restore: every column as it was but
// the launch start, cleared, and row_version, two past (reset, restore).
func rrRestored(before apitest.SpawnColumns) apitest.SpawnColumns {
	w := before
	w.LaunchStartedAt = nil
	w.RowVersion = before.RowVersion.(int64) + 2
	return w
}

// rrFailCreate makes r's next create fail with f, creating nothing.
func rrFailCreate(e *killEnv, r reuseRow, f tmux.Failure) {
	e.rec.Script(r.Socket, tmuxfix.Script{Failure: f, ExitStatus: 1, Times: 1}, tmux.CallCreate)
}

// rrHistory reads id's history over every life.
func rrHistory(t *testing.T, e *killEnv, id string) []apitest.HistoryEntry {
	t.Helper()
	h, err := apitest.ReadSessionHistoryAllLives(e.dbPath, id)
	if err != nil {
		t.Fatalf("ReadSessionHistoryAllLives(%s): %v", id, err)
	}
	return h
}

// rrAssertRestored fails unless r's row is want, its history is before plus
// the reset's archive of its session (life reuseLife, want's transcript
// path), it has no permission request, and one applied
// ad.spawn.reuse_restored names launchErr.
func rrAssertRestored(t *testing.T, e *killEnv, r reuseRow, want apitest.SpawnColumns, before []apitest.HistoryEntry, launchErr string) {
	t.Helper()
	if got := e.columns(t, r.ID); !reflect.DeepEqual(got, want) {
		t.Errorf("row %s =\n  %+v\nwant the pre-reuse row restored\n  %+v", r.ID, got, want)
	}
	path, _ := want.JSONLPath.(string)
	var archived []apitest.HistoryEntry
	rest := slices.DeleteFunc(rrHistory(t, e, r.ID), func(h apitest.HistoryEntry) bool {
		if h.ClaudeSessionID == r.Spawn.ClaudeSessionID {
			archived = append(archived, h)
			return true
		}
		return false
	})
	if len(archived) != 1 || archived[0].LifeNumber != reuseLife || archived[0].JSONLPath.String != path ||
		!reflect.DeepEqual(rest, before) {
		t.Errorf("history: archived %+v, others %+v; want one entry of %s at life %d (%q), others unchanged %+v",
			archived, rest, r.Spawn.ClaudeSessionID, reuseLife, path, before)
	}
	if perms, err := e.st.PermissionRequestsForSpawn(r.ID); err != nil || len(perms) != 0 {
		t.Errorf("permission requests = %+v (%v); want the reset's deletion kept", perms, err)
	}
	l := pendTrail(t, "ad.spawn.reuse_restored", r.ID)
	if len(l) != 1 || l[0]["applied"] != true || l[0]["launch_error"] != launchErr || l[0]["restore_error"] != nil ||
		l[0]["source"] != "ad_spawn" {
		t.Errorf("ad.spawn.reuse_restored = %v; want one applied, launch_error %s, no restore_error, source ad_spawn", l, launchErr)
	}
}

// TestSpawnReuseRestoreAfterEachLaunchFailure: each non-timeout launch failure, on an ended and a missing
// row, returns its error with the restore's sentence and restores the row byte for byte; a second reuse launches.
func TestSpawnReuseRestoreAfterEachLaunchFailure(t *testing.T) {
	lookup, create, label, kill := tmux.CallLookup, tmux.CallCreate, tmux.CallSetLabel, tmux.CallKillSession
	createFailed := func(*killEnv, reuseRow) apitest.DescCase {
		return apitest.DescSessionCreateFailed(apitest.SessionCreateFailed{})
	}
	failWith := func(f tmux.Failure) func(*killEnv, reuseRow) {
		return func(e *killEnv, r reuseRow) { rrFailCreate(e, r, f) }
	}
	triggers := []struct {
		name     string
		arrange  func(e *killEnv, r reuseRow)
		want     error
		wantName string
		calls    []tmux.Call
		desc     func(e *killEnv, r reuseRow) apitest.DescCase
	}{
		{"recognised create failure", failWith(tmux.FailNoServer), tmux.ErrTmuxSessionCreate, "ErrTmuxSessionCreate",
			[]tmux.Call{lookup, create}, createFailed},
		// SR-10.4: a bad cwd or a missing shell is an unrecognised, non-duplicate reply with no output.
		{"bad cwd or missing shell", failWith(tmux.FailUnrecognized), tmux.ErrTmuxSessionCreate, "ErrTmuxSessionCreate",
			[]tmux.Call{lookup, create}, createFailed},
		{"tmux binary missing", failWith(tmux.FailUnavailable), tmux.ErrTmuxNotAvailable, "ErrTmuxNotAvailable",
			[]tmux.Call{lookup, create}, func(*killEnv, reuseRow) apitest.DescCase { return apitest.DescTmuxNotRun() }},
		{"socket permission denied", failWith(tmux.FailSocketDenied), tmux.ErrTmuxNotAvailable, "ErrTmuxNotAvailable",
			[]tmux.Call{lookup, create}, func(_ *killEnv, r reuseRow) apitest.DescCase { return apitest.DescSocketPermission(r.Socket) }},
		{"unlabelled session killed after both label attempts", func(e *killEnv, r reuseRow) {
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailLabel, Times: 1}, create).
				Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Times: 1}, label)
		}, tmux.ErrTmuxSessionCreate, "ErrTmuxSessionCreate", []tmux.Call{lookup, create, label, kill},
			func(e *killEnv, r reuseRow) apitest.DescCase {
				return apitest.DescUnlabelledSession(apitest.UnlabelledSession{Name: r.Name,
					SessionID: e.rec.SocketCallsOf(label)[0].Target, Ended: true})
			}},
	}
	for _, tr := range triggers {
		for _, prior := range []string{store.StateEnded, store.StateMissing} {
			t.Run(tr.name+"/"+prior, func(t *testing.T) {
				e := newKillEnv(t)
				r := e.seedReusable(t, agentGone, reuseRowSpec{State: prior, Age: time.Hour})
				tr.arrange(e, r)
				before, hist := e.columns(t, r.ID), rrHistory(t, e, r.ID)

				_, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{}))
				assertLaunchSentinel(t, err, tr.want)
				c := rlOneCreate(t, e, r.Socket, tr.calls...)
				restore := apitest.ResumeRestore{Outcome: apitest.RestoreApplied, PriorState: prior, Launch: apitest.LaunchReuse}
				apitest.AssertDescription(t, err.Error(), tr.desc(e, r).AfterResumeRestore(restore), c.Token, e.storeID, r.Token)
				rrAssertRestored(t, e, r, rrRestored(before), hist, tr.wantName)
				for _, s := range e.rec.Sessions(r.Socket) {
					if s.Name == storedFormOf(c.Target) {
						t.Errorf("session %+v left under the requested name; want none", s)
					}
				}

				// AC-REUSE-07: with the cause gone, an immediate second reuse launches.
				if _, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{})); err != nil {
					t.Fatalf("second reuse: %v", err)
				}
				if cols := e.columns(t, r.ID); cols.State != store.StatePending || cols.LifeNumber != reuseLife+1 {
					t.Errorf("after the second reuse: state %v, life %v; want pending, %d", cols.State, cols.LifeNumber, reuseLife+1)
				}
			})
		}
	}
}

// TestSpawnReuseRestoreEndedAtAndParent: a NULL ended_at is restored as the failure time in the store's
// layout; a parent deleted after the reset is restored as a NULL parent_id; the restore applies in both.
func TestSpawnReuseRestoreEndedAtAndParent(t *testing.T) {
	cases := []struct {
		name         string
		endedAt      reuseEndedAt
		deleteParent bool
	}{
		{"NULL ended_at becomes the failure time", endedNull, false},
		{"parent deleted meanwhile", endedAged, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Age: time.Hour, EndedAt: tc.endedAt})
			rs := &hookedReuseStore{st: e.st}
			if tc.deleteParent {
				rs.afterReset(func() {
					if err := e.st.DeleteSpawn(r.ParentID); err != nil {
						t.Errorf("DeleteSpawn(%s): %v", r.ParentID, err)
					}
				})
			}
			rrFailCreate(e, r, tmux.FailUnavailable)
			before, hist := e.columns(t, r.ID), rrHistory(t, e, r.ID)

			_, _, err := e.reuseWith(t, rs, reuseParams(t, r, reuseRequest{}))
			assertLaunchSentinel(t, err, tmux.ErrTmuxNotAvailable)
			want := rrRestored(before)
			if tc.endedAt == endedNull {
				if before.EndedAt != nil {
					t.Fatalf("seeded ended_at = %v; want NULL", before.EndedAt)
				}
				want.EndedAt = e.clock.Now().UTC().Format(time.DateTime) // read when the restore ran
			}
			if tc.deleteParent {
				want.ParentID = nil
				if _, err := apitest.ReadSpawnColumns(e.dbPath, r.ParentID); !errors.Is(err, store.ErrSpawnNotFound) {
					t.Errorf("parent %s: %v; want deleted", r.ParentID, err)
				}
			}
			rrAssertRestored(t, e, r, want, hist, "ErrTmuxNotAvailable")
		})
	}
}

// TestSpawnReuseFailedThenResumeAsBefore (AC-REUSE-07): after a failed reuse of a never-messaged row,
// resume refuses with the same ErrJsonlNeverWritten and get shows never_written, the archive kept.
func TestSpawnReuseFailedThenResumeAsBefore(t *testing.T) {
	e := newKillEnv(t)
	r := e.seedReusable(t, agentGone, reuseRowSpec{Age: time.Hour, Bare: true,
		Opts: []apitest.SpawnOption{apitest.WithJsonlPath("")}})
	if err := os.Remove(r.JSONLPath); err != nil { // never messaged: no transcript at the fallback path either
		t.Fatalf("remove the seeded transcript: %v", err)
	}
	c, _ := e.client(t)
	observe := func(when string) (api.SpawnRow, error) {
		_, rerr := e.resume(r.ID)
		if !errors.Is(rerr, api.ErrJsonlNeverWritten) || errors.Is(rerr, api.ErrJsonlMissing) {
			t.Fatalf("resume %s the reuse = %v; want ErrJsonlNeverWritten only", when, rerr)
		}
		got, gerr := c.Get(r.ID)
		if gerr != nil || got.TranscriptStatus != "never_written" {
			t.Fatalf("get %s the reuse = %+v, %v; want transcript_status never_written", when, got, gerr)
		}
		return got, rerr
	}
	getBefore, errBefore := observe("before")
	before, hist := e.columns(t, r.ID), rrHistory(t, e, r.ID)
	rrFailCreate(e, r, tmux.FailUnavailable)
	_, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{}))
	assertLaunchSentinel(t, err, tmux.ErrTmuxNotAvailable)

	getAfter, errAfter := observe("after")
	if errAfter.Error() != errBefore.Error() {
		t.Errorf("resume after the failed reuse = %q; want as before, %q", errAfter, errBefore)
	}
	if !reflect.DeepEqual(getAfter.PriorSessions, getBefore.PriorSessions) {
		t.Errorf("get prior_sessions = %+v; want as before, %+v", getAfter.PriorSessions, getBefore.PriorSessions)
	}
	rrAssertRestored(t, e, r, rrRestored(before), hist, "ErrTmuxNotAvailable")
}

// TestSpawnReuseFailedThenExpired (AC-REUSE-07): a row restored by a failed reuse keeps its
// ended_at, so expire removes it once that is past the retention window.
func TestSpawnReuseFailedThenExpired(t *testing.T) {
	e := newKillEnv(t)
	retention := time.Duration(config.Default().Defaults.ExpireRetentionDays) * 24 * time.Hour
	r := e.seedReusable(t, agentGone, reuseRowSpec{Age: retention + time.Hour})
	rrFailCreate(e, r, tmux.FailUnavailable)
	_, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{}))
	assertLaunchSentinel(t, err, tmux.ErrTmuxNotAvailable)
	if cols := e.columns(t, r.ID); cols.State != store.StateEnded {
		t.Fatalf("state after the failed reuse = %v; want ended (restored)", cols.State)
	}

	res, _, err := e.expire(nil)
	if err != nil || !slices.Contains(res.IDs, r.ID) {
		t.Fatalf("expire = %+v, %v; want %s deleted", res, err, r.ID)
	}
	if _, err := apitest.ReadSpawnColumns(e.dbPath, r.ID); !errors.Is(err, store.ErrSpawnNotFound) {
		t.Errorf("row %s after expire: %v; want removed", r.ID, err)
	}
}
