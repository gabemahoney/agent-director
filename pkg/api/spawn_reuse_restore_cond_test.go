package api_test

// spawn_reuse_restore_cond_test.go covers reuse's restore after a failed
// launch when something else reaches the row first, or the restore write
// fails (SR-10.4, SR-5.8, SR-22.9; AC-REUSE-17, AC-HOOK-02): a delete or a
// versioned write stands and the restore writes nothing; hooks on the reset
// row are ignored, so the restore applies; a failing restore leaves the reset
// row pending. Fixture: spawn_reuse_fixture_test.go.

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// rrcWindow is where a test acts between reuse's reset and its restore.
type rrcWindow int

const (
	rrcAfterReset rrcWindow = iota // the reuse store's afterReset hook, before the create
	rrcOnCreate                    // the failing create's after-call hook
)

// rrcWindows is every window.
var rrcWindows = []rrcWindow{rrcAfterReset, rrcOnCreate}

// String names w for a subtest.
func (w rrcWindow) String() string {
	if w == rrcAfterReset {
		return "after the reset"
	}
	return "on the failing create"
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
// (ErrTmuxSessionCreate) and act run once in window w.
func (e *killEnv) rrcReuse(t *testing.T, r reuseRow, w rrcWindow, act func()) rrcRun {
	t.Helper()
	e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Times: 1}, tmux.CallCreate)
	rs := &hookedReuseStore{st: e.st}
	if act != nil && w == rrcAfterReset {
		rs.afterReset(act)
	}
	if act != nil && w == rrcOnCreate {
		var done bool
		e.rec.AfterCall(tmux.CallCreate, func(c tmuxfix.SocketCall, _ error) {
			if !done && c.InstanceID == r.ID {
				done = true
				act()
			}
		})
	}
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
// applied and a null or non-null restore_error (failed).
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
	recs := run.before.since(t, "ad.spawn.reuse_restored")
	if len(recs) != 1 {
		t.Fatalf("ad.spawn.reuse_restored records = %v; want exactly one", recs)
	}
	rec := recs[0]
	if rec["applied"] != applied || rec["launch_error"] != "ErrTmuxSessionCreate" || rec["source"] != "ad_spawn" {
		t.Errorf("ad.spawn.reuse_restored = %v; want applied %v, launch_error ErrTmuxSessionCreate, source ad_spawn", rec, applied)
	}
	if msg, ok := rec["restore_error"].(string); failed != (ok && msg != "") || (!failed && rec["restore_error"] != nil) {
		t.Errorf("restore_error = %#v; want non-null %v", rec["restore_error"], failed)
	}
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

// TestSpawnReuseRestoreNotAppliedAfterAnotherWrite: a parent-id write or a
// delete between the reset and the restore stands; the restore writes nothing.
func TestSpawnReuseRestoreNotAppliedAfterAnotherWrite(t *testing.T) {
	writes := []struct {
		name    string
		write   func(e *killEnv, id, other string) error
		outcome apitest.RestoreOutcome
	}{
		{"parent id written", func(e *killEnv, id, other string) error { return e.st.SetParentID(id, other) },
			apitest.RestoreRowChanged},
		{"row deleted", func(e *killEnv, id, _ string) error { return e.st.DeleteSpawn(id) }, apitest.RestoreRowRemoved},
	}
	for _, wr := range writes {
		for _, w := range rrcWindows {
			t.Run(wr.name+"/"+w.String(), func(t *testing.T) {
				e := newKillEnv(t)
				r := e.seedReusable(t, agentGone, reuseRowSpec{Age: rlkSettled(e)})
				other := e.seedRow(t, killRowSpec{State: store.StateEnded, Agent: agentGone, NoSession: true}).ID
				var written apitest.SpawnColumns
				var present bool
				run := e.rrcReuse(t, r, w, func() {
					if err := wr.write(e, r.ID, other); err != nil {
						t.Errorf("write: %v", err)
					}
					written, present = e.rrcColumns(t, r.ID)
				})

				e.rrcAssertLaunchError(t, run, apitest.ResumeRestore{Outcome: wr.outcome})
				final, still := e.rrcColumns(t, r.ID)
				if still != present || !reflect.DeepEqual(final, written) {
					t.Errorf("row after the reuse (present %v) =\n  %+v\nwant as the write left it (present %v)\n  %+v",
						still, final, present, written)
				}
				if wr.outcome == apitest.RestoreRowRemoved && still {
					t.Error("row present after the reuse; want it to stay deleted")
				}
				if wr.outcome == apitest.RestoreRowChanged && (final.State != store.StatePending || final.ParentID != other ||
					final.LifeNumber != reuseLife+1) {
					t.Errorf("row {state %v, parent %v, life %v}; want pending, %s, %d (the reset's life)",
						final.State, final.ParentID, final.LifeNumber, other, reuseLife+1)
				}
				e.rrcAssertOneAttempt(t, run, false, false)
				if strings.Contains(run.logs, "WARN") {
					t.Errorf("client log = %q; want no WARN line", run.logs)
				}
			})
		}
	}
}

// TestSpawnReuseHooksBeforeRestoreIgnored (SR-22.9): the reset row records no
// pane, so hooks naming the archived or another session id are ignored and the restore applies.
func TestSpawnReuseHooksBeforeRestoreIgnored(t *testing.T) {
	for _, w := range rrcWindows {
		t.Run(w.String(), func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Age: rlkSettled(e)})
			ignored := store.HookApplied{Reason: store.HookReasonNoPaneRecorded}
			run := e.rrcReuse(t, r, w, func() {
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
		})
	}
}

// TestSpawnReuseRestoreStoreErrorLeavesReset (SR-5.8): a failing restore
// leaves the reset row pending, logs one WARN line and still returns the launch error.
func TestSpawnReuseRestoreStoreErrorLeavesReset(t *testing.T) {
	e := newKillEnv(t)
	r := e.seedReusable(t, agentGone, reuseRowSpec{Age: rlkSettled(e)})
	storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, r.ID)
	var reset apitest.SpawnColumns
	run := e.rrcReuse(t, r, rrcOnCreate, func() { reset = e.columns(t, r.ID) })

	e.rrcAssertLaunchError(t, run, apitest.ResumeRestore{Outcome: apitest.RestoreStoreError})
	if name, _ := errnames.Classify(run.err); name != "ErrTmuxSessionCreate" {
		t.Errorf("classified as %s; want ErrTmuxSessionCreate (not ErrInternal)", name)
	}
	if reset.State != store.StatePending {
		t.Fatalf("row at the create = %v; want pending", reset.State)
	}
	e.assertRowUnchanged(t, r.ID, reset)
	lines := strings.Split(strings.TrimSpace(run.logs), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "WARN: spawn: ") || !strings.Contains(lines[0], r.ID) ||
		strings.Contains(lines[0], run.token) || strings.Contains(lines[0], r.Token) {
		t.Errorf("client log lines = %q; want one spawn WARN line naming %s and no token", lines, r.ID)
	}
	e.rrcAssertOneAttempt(t, run, false, true)
}
