package api_test

// spawn_reuse_restore_test.go covers reuse's restore after a failed launch
// (SR-10.4, SR-8.7, SR-5.8, SR-22.9; AC-REUSE-07, 17, 23, AC-HOOK-02): the
// pre-reuse row restored byte for byte after every non-timeout failure, a
// competing write or a delete standing, hooks on the reset row ignored, a
// failing restore leaving it pending, and one ad.spawn.reuse_restored each.
// A parent deleted meanwhile is the store's (TestReuseRestoreApplied).

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rrHistory reads id's history over every life.
func rrHistory(t *testing.T, e *killEnv, id string) []apitest.HistoryEntry {
	t.Helper()
	h, err := apitest.ReadSessionHistoryAllLives(e.dbPath, id)
	if err != nil {
		t.Fatalf("ReadSessionHistoryAllLives(%s): %v", id, err)
	}
	return h
}

// rrAssertRestored fails unless r's row is want, its history before plus the
// reset's archive, no permission request, and one applied reuse_restored
// after reused names launchErr.
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
	rutAssertRestored(t, 0, r.ID, true, launchErr, "")
	rutAssertOrder(t, 0, r.ID, rutReused, rutRestored)
}

// TestSpawnReuseRestoreAfterEachLaunchFailure (AC-REUSE-07, AC-REUSE-23): each non-timeout launch failure,
// on an ended and a missing row, returns its error with the restore's sentence and restores the row byte for
// byte, so get and resume see the pre-reuse life as before; a second reuse launches.
func TestSpawnReuseRestoreAfterEachLaunchFailure(t *testing.T) {
	t.Parallel()
	lookup, create, label, kill := tmux.CallLookup, tmux.CallCreate, tmux.CallSetLabel, tmux.CallKillSession
	createFailed := func(*killEnv, reuseRow) apitest.DescCase {
		return apitest.DescSessionCreateFailed(apitest.SessionCreateFailed{})
	}
	failWith := func(f tmux.Failure) func(*killEnv, reuseRow) {
		return func(e *killEnv, r reuseRow) {
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: f, ExitStatus: 1, Times: 1}, tmux.CallCreate)
		}
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
				t.Parallel()
				e := newKillEnv(t)
				// The old life opted out of pre-trust and the call does not: the restore brings 1 back. A missing
				// row's NULL ended_at is restored as the failure time, read from the Client clock.
				spec := reuseRowSpec{State: prior, Age: time.Hour, NoPreTrust: true}
				if prior == store.StateMissing {
					spec.EndedAt = endedNull
				}
				r := e.seedReusable(t, agentGone, spec)
				tr.arrange(e, r)
				before, hist := e.columns(t, r.ID), rrHistory(t, e, r.ID)

				_, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{}))
				assertLaunchSentinel(t, err, tr.want)
				c := rlOneCreate(t, e, r.Socket, tr.calls...)
				restore := apitest.ResumeRestore{Outcome: apitest.RestoreApplied, PriorState: prior, Launch: apitest.LaunchReuse}
				apitest.AssertDescription(t, err.Error(), tr.desc(e, r).AfterResumeRestore(restore), c.Token, e.storeID, r.Token)
				want := before // every column restored but the launch start, cleared, two versions on (reset, restore)
				want.LaunchStartedAt, want.RowVersion = nil, before.RowVersion.(int64)+2
				if spec.EndedAt == endedNull {
					want.EndedAt = e.clock.Now().UTC().Format(time.DateTime) // the clock when the restore ran
				}
				rrAssertRestored(t, e, r, want, hist, tr.wantName)
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

// rrcRun is one reuse whose create failed: the row, its snapshot taken just
// before the call, the create's new token, the error and the Client's log.
type rrcRun struct {
	r      reuseRow
	before writesSnapshot
	token  string
	err    error
	logs   string
}

// rrcReuse reuses r through the reuse-store seam with its create failing
// (ErrTmuxSessionCreate) and act run once as that create returns, between
// the reset and the restore.
func (e *killEnv) rrcReuse(t *testing.T, r reuseRow, act func()) rrcRun {
	t.Helper()
	e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Times: 1}, tmux.CallCreate)
	rs := &hookedReuseStore{st: e.st}
	var done bool
	e.rec.AfterCall(tmux.CallCreate, func(c tmuxfix.SocketCall, _ error) {
		if !done && c.InstanceID == r.ID {
			done = true
			act()
		}
	})
	p := reuseParams(t, r, reuseRequest{})
	run := rrcRun{r: r, before: e.snapshotReuse(t, r)}
	_, run.logs, run.err = e.reuseWith(t, rs, p)
	if creates := e.rec.SocketCallsOf(tmux.CallCreate); len(creates) == 1 {
		run.token = creates[0].Token
	}
	return run
}

// rrcAssertLaunchError fails unless run's error is the create failure alone,
// ending with restore's sentence and naming no launch token or store id.
func (e *killEnv) rrcAssertLaunchError(t *testing.T, run rrcRun, restore apitest.ResumeRestore) {
	t.Helper()
	assertOneSentinel(t, run.err, tmux.ErrTmuxSessionCreate)
	if run.err == nil {
		t.Fatal("reuse err = nil; want the create failure")
	}
	restore.Launch = apitest.LaunchReuse
	apitest.AssertDescription(t, run.err.Error(), apitest.DescSessionCreateFailed(apitest.SessionCreateFailed{}).
		AfterResumeRestore(restore), run.token, run.r.Token, e.storeID)
}

// rrcAssertOneAttempt fails unless the call made only the lookup and one
// create on the row's socket and wrote one ad.spawn.reuse_restored with
// applied and, when failed, the restore WARN line's text as restore_error.
func (e *killEnv) rrcAssertOneAttempt(t *testing.T, run rrcRun, applied, failed bool) {
	t.Helper()
	var calls []tmux.Call
	for _, c := range e.rec.SocketCalls()[run.before.calls:] {
		if c.Socket != run.r.Socket {
			t.Errorf("tmux call %v on %s; want every call on %s", c.Call, c.Socket, run.r.Socket)
		}
		calls = append(calls, c.Call)
	}
	if want := []tmux.Call{tmux.CallLookup, tmux.CallCreate}; !reflect.DeepEqual(calls, want) {
		t.Errorf("tmux calls = %q; want %q (one create, no retry)", calls, want)
	}
	if failed != (rutRestoreError(run.logs, run.r.ID) != "") {
		t.Errorf("restore WARN line in %q; want one only for a failed restore", run.logs)
	}
	rutAssertRestored(t, run.before.mark, run.r.ID, applied, "ErrTmuxSessionCreate", run.logs)
}

// rrcColumns reads id's row raw; present is false once no row has the id.
func (e *killEnv) rrcColumns(t *testing.T, id string) (cols apitest.SpawnColumns, present bool) {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(e.dbPath, id)
	if errors.Is(err, store.ErrSpawnNotFound) {
		return apitest.SpawnColumns{}, false
	}
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
	}
	return cols, true
}

// TestSpawnReuseRestoreNotAppliedAfterAnotherWrite (SR-10.4, SR-5.8): a
// parent-id write or a delete between the reset and the restore stands and
// the restore writes nothing; a failing restore leaves the reset row pending
// with one WARN line; each returns the launch error (not ErrInternal) after
// one create, with the restore's sentence.
func TestSpawnReuseRestoreNotAppliedAfterAnotherWrite(t *testing.T) {
	t.Parallel()
	writes := []struct {
		name    string
		write   func(e *killEnv, id, other string) error // nil: the restore write fails instead
		outcome apitest.RestoreOutcome
	}{
		{"parent id written", func(e *killEnv, id, other string) error { return e.st.SetParentID(id, other) },
			apitest.RestoreRowChanged},
		{"row deleted", func(e *killEnv, id, _ string) error { return e.st.DeleteSpawn(id) }, apitest.RestoreRowRemoved},
		{"restore store error", nil, apitest.RestoreStoreError},
	}
	for _, wr := range writes {
		t.Run(wr.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Age: rlkSettled(e)})
			other := e.seedRow(t, killRowSpec{State: store.StateEnded, Agent: agentGone, NoSession: true}).ID
			if wr.write == nil {
				storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, r.ID)
			}
			var written apitest.SpawnColumns
			var present bool
			run := e.rrcReuse(t, r, func() {
				if wr.write != nil {
					if err := wr.write(e, r.ID, other); err != nil {
						t.Errorf("write: %v", err)
					}
				}
				written, present = e.rrcColumns(t, r.ID)
			})

			e.rrcAssertLaunchError(t, run, apitest.ResumeRestore{Outcome: wr.outcome})
			final, still := e.rrcColumns(t, r.ID)
			if still != present || !reflect.DeepEqual(final, written) {
				t.Errorf("row after the reuse (present %v) =\n  %+v\nwant as left before the restore (present %v)\n  %+v",
					still, final, present, written)
			}
			switch wr.outcome {
			case apitest.RestoreRowRemoved:
				if still {
					t.Error("row present after the reuse; want it to stay deleted")
				}
			case apitest.RestoreRowChanged:
				if final.State != store.StatePending || final.ParentID != other || final.LifeNumber != reuseLife+1 {
					t.Errorf("row {state %v, parent %v, life %v}; want pending, %s, %d (the reset's life)",
						final.State, final.ParentID, final.LifeNumber, other, reuseLife+1)
				}
			case apitest.RestoreStoreError:
				if written.State != store.StatePending {
					t.Errorf("row = %v; want the reset's, pending", written.State)
				}
			}
			lines := strings.Split(strings.TrimSpace(run.logs), "\n")
			switch {
			case wr.write != nil && run.logs != "":
				t.Errorf("client log = %q; want nothing", run.logs)
			case wr.write == nil && (len(lines) != 1 || !strings.HasPrefix(lines[0], "WARN: spawn: ") ||
				!strings.Contains(lines[0], r.ID) || strings.Contains(lines[0], run.token) || strings.Contains(lines[0], r.Token)):
				t.Errorf("client log lines = %q; want one spawn WARN line naming %s and no token", lines, r.ID)
			}
			e.rrcAssertOneAttempt(t, run, false, wr.write == nil)
		})
	}
}

// TestSpawnReuseHooksBeforeRestoreIgnored (SR-22.9): the reset row records no
// pane, so hooks naming the archived or another session id are ignored and the restore applies.
func TestSpawnReuseHooksBeforeRestoreIgnored(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.seedReusable(t, agentGone, reuseRowSpec{Age: rlkSettled(e)})
	ignored := store.HookApplied{Reason: store.HookReasonNoPaneRecorded}
	run := e.rrcReuse(t, r, func() {
		reset := e.columns(t, r.ID)
		for _, sid := range []string{r.Spawn.ClaudeSessionID, "sess-other-" + uuid.NewString()[:8]} {
			for _, ev := range []string{"Stop", "SessionEnd", "PermissionRequest", "SessionStart"} {
				if got := apitest.ApplyAgentHook(t, e.dbPath, r.ID, ev, sid,
					apitest.HookTranscript(r.JSONLPath, true)); got != ignored {
					t.Errorf("%s with session %s = %+v; want %+v", ev, sid, got, ignored)
				}
			}
		}
		if got := e.columns(t, r.ID); !reflect.DeepEqual(got, reset) {
			t.Errorf("row after the hooks =\n  %+v\nwant as the reset left it\n  %+v", got, reset)
		}
	})

	e.rrcAssertLaunchError(t, run, apitest.ResumeRestore{Outcome: apitest.RestoreApplied, PriorState: store.StateEnded})
	e.assertRowUnchanged(t, r.ID, rstRestored(resumableRow{Before: run.before.cols}, run.before.cols.ParentID))
	e.rrcAssertOneAttempt(t, run, true, false)
}
