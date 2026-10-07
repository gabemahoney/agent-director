package api_test

// resume_pending_restore_reuse_test.go: AC-RES-12's reuse half (SR-8.5, SR-10.2, SR-10.4, SR-20.6): after a
// resume whose launch failed and whose restore applied, a reuse with the cause removed launches. Its sibling
// resume_pending_restore_test.go holds the resume half; fixtures: resume_held_fixture_test.go, spawn_reuse_fixture_test.go.

import (
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// TestResumeRestoreReuseAfterFailedResume: after each resume launch failure restored on an ended or missing row,
// with the cause removed a reuse is decided by its lookup (Gone), resets and launches (pending, life + 1); while a
// "duplicate session" holder still holds the name, the reuse is refused and writes nothing.
func TestResumeRestoreReuseAfterFailedResume(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	// createFails makes the resume's create fail with f once; the spent script is the cause removed.
	createFails := func(f tmux.Failure) func(*testing.T, *killEnv, reuseRow) func() {
		return func(_ *testing.T, e *killEnv, r reuseRow) func() {
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: f, ExitStatus: 1, Times: 1}, tmux.CallCreate)
			return nil
		}
	}
	cases := []struct {
		name string
		want error
		// arrange sets up the resume's failure; remove (nil: none needed) takes its cause away.
		arrange func(*testing.T, *killEnv, reuseRow) (remove func())
	}{
		{"launch failure", api.ErrTmuxSessionCreate, createFails(tmux.FailUnrecognized)},
		{"tmux unavailable", api.ErrTmuxNotAvailable, createFails(tmux.FailUnavailable)},
		{"duplicate session with a holder", api.ErrTmuxSessionConflict, func(t *testing.T, e *killEnv, r reuseRow) func() {
			sc := e.arrangeHeld(t, r.resumeRow, heldSpec{Holder: holderNone})
			return func() { e.removeHolders(t, sc) }
		}},
	}
	for _, tc := range cases {
		for _, prior := range []string{store.StateEnded, store.StateMissing} {
			t.Run("Reuse/"+tc.name+"/"+prior, func(t *testing.T) {
				e := newKillEnv(t)
				r := e.seedReusable(t, agentGone, reuseRowSpec{State: prior, Held: true, Age: rlkSettled(e), Bare: true})
				remove := tc.arrange(t, e, r)

				_, err := e.resume(r.ID)

				assertOneSentinel(t, err, tc.want)
				if cols := e.columns(t, r.ID); cols.State != prior || cols.LifeNumber != reuseLife {
					t.Fatalf("after the failed resume: {state %v, life %#v}; want restored to %s, life %d",
						cols.State, cols.LifeNumber, prior, reuseLife)
				}
				if rs := pendTrail(t, "ad.resume.restored", r.ID); len(rs) != 1 || rs[0]["applied"] != true {
					t.Fatalf("ad.resume.restored = %v; want one applied", rs)
				}
				p := reuseParams(t, r, reuseRequest{})
				if remove != nil {
					held := e.snapshotReuse(t, r)
					_, logs, err := e.reuse(t, p)
					assertOneSentinel(t, err, api.ErrTmuxSessionConflict)
					if err == nil {
						t.Fatalf("reuse while the holder holds %q succeeded (log %q)", r.Name, logs)
					}
					// The failed resume already pre-trusted this cwd, so the trust file is no longer as seeded.
					e.assertWroteNothing(t, held, exceptTrust)
					remove()
				}
				before := e.snapshotReuse(t, r)

				if _, logs, err := e.reuse(t, p); err != nil {
					t.Fatalf("reuse with the cause removed: %v (log %q)", err, logs)
				}

				if cols := e.columns(t, r.ID); cols.State != store.StatePending || cols.LifeNumber != reuseLife+1 {
					t.Errorf("after the reuse: {state %v, life %#v}; want pending, life %d", cols.State, cols.LifeNumber, reuseLife+1)
				}
				if got := callKinds(e.rec)[before.calls:]; !slices.Equal(got, []tmux.Call{tmux.CallLookup, tmux.CallCreate}) {
					t.Errorf("reuse tmux calls = %v; want its lookup, then the create", got)
				}
				gone := (tmux.Result{Verdict: tmux.Gone}).Token()
				if rs := before.since(t, "ad.spawn.reused"); len(rs) != 1 || rs[0]["lookup_outcome"] != gone || rs[0]["prior_state"] != prior {
					t.Errorf("ad.spawn.reused = %v; want one with lookup_outcome %s, prior_state %s", rs, gone, prior)
				}
			})
		}
	}
}
