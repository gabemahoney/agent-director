package api_test

// spawn_reuse_pretrust_test.go: a reuse pre-trusts by its own call's choice
// and records it for the new life; later resumes follow it, a failed reuse's
// restore brings the earlier choice back, a refusal before the reset writes
// no entry and a failed write still launches (SR-10.3, SR-22.6; AC-REUSE-27).
// Trust files are pretrust_fixture_test.go's; life steps the rlf helpers.

import (
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// rtrChoice is the old life's pre-trust opt-out and the reuse call's.
type rtrChoice struct {
	name                  string
	rowOptOut, callOptOut bool
}

// rtrChoices: X (opted out) reused without the opt-out; Y (allowed) reused with it.
var rtrChoices = []rtrChoice{
	{"X opted out, reused allowing", true, false},
	{"Y allowed, reused opting out", false, true},
}

// rtrOutcome is pre_trust for an opt-out (skipped, nothing written) or not
// (ok, the entry written), and the no_pre_trust column recording it.
func rtrOutcome(optOut bool) (preTrust string, trusted bool, column int64) {
	if optOut {
		return "skipped", false, 1
	}
	return "ok", true, 0
}

// rtrResume resumes id and fails unless it launches, reports want and leaves
// c trusting cwd as trusted says.
func rtrResume(t *testing.T, e *killEnv, id, cwd string, c trustConfig, want string, trusted bool) {
	t.Helper()
	res, err := e.resume(id)
	if err != nil {
		t.Fatalf("Resume(%s): %v", id, err)
	}
	if res.PreTrust != want {
		t.Errorf("resume pre_trust = %q; want %q", res.PreTrust, want)
	}
	c.check(t, cwd, trusted, "after the resume")
}

// TestSpawnReuseFollowsOwnPreTrustChoice: the reuse pre-trusts by its own
// call's opt-out, not the old life's, records it, and a later resume follows it.
func TestSpawnReuseFollowsOwnPreTrustChoice(t *testing.T) {
	for _, tc := range rtrChoices {
		t.Run(tc.name, func(t *testing.T) {
			e := newRlfEnv(t)
			r0 := e.seedReusable(t, agentGone, reuseRowSpec{NoPreTrust: tc.rowOptOut})
			want, trusted, column := rtrOutcome(tc.callOptOut)

			r, res := rlfReuse(t, e, r0, reuseRequest{NoPreTrust: tc.callOptOut})
			if res.PreTrust != want {
				t.Errorf("reuse pre_trust = %q; want %q", res.PreTrust, want)
			}
			checkPreTrustJSON(t, res, want)
			r.Trust.check(t, r.CWD, trusted, "after the reuse")
			if got := e.columns(t, r.ID).NoPreTrust; got != column {
				t.Errorf("no_pre_trust after the reuse = %#v; want %d", got, column)
			}

			rlfReportIn(t, e, r.ID, rlfNewSession(), true)
			rlfEndLife(t, e, r.ID)
			r.Trust.reset(t)
			rtrResume(t, e, r.ID, r.CWD, r.Trust, want, trusted)
			if got := e.columns(t, r.ID).NoPreTrust; got != column {
				t.Errorf("no_pre_trust after the resume = %#v; want %d", got, column)
			}
		})
	}
}

// TestSpawnReuseFailedRestoresPreTrustChoice: a reuse whose create fails and
// whose restore applies leaves the old life's choice recorded, and a following
// resume follows it.
func TestSpawnReuseFailedRestoresPreTrustChoice(t *testing.T) {
	for _, tc := range rtrChoices {
		t.Run(tc.name, func(t *testing.T) {
			e := newRlfEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{NoPreTrust: tc.rowOptOut})
			want, trusted, column := rtrOutcome(tc.rowOptOut)
			e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Times: 1}, tmux.CallCreate)

			_, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{NoPreTrust: tc.callOptOut}))
			assertLaunchSentinel(t, err, tmux.ErrTmuxSessionCreate)
			if got := e.columns(t, r.ID).NoPreTrust; got != column {
				t.Errorf("no_pre_trust after the restore = %#v; want the old life's %d", got, column)
			}

			r.Trust.reset(t) // the failed reuse's own entry, if any
			rtrResume(t, e, r.ID, r.CWD, r.Trust, want, trusted)
		})
	}
}

// TestSpawnReusePreTrustAfterNamePreCheck: a reuse refused at the old-row
// lookup or the new-name pre-check writes no trust entry (nor anything else).
func TestSpawnReusePreTrustAfterNamePreCheck(t *testing.T) {
	cases := []struct {
		name   string
		holder holderKind
		rename bool // the holder holds a requested new name, not the recorded one
	}{
		{"leftover of an earlier life", holderOld, false},
		{"requested name held by another row's session", holderForeign, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{})
			q, held := reuseRequest{}, r.killRow
			if tc.rename {
				q.Name = "requested-" + uuid.NewString()[:8]
				held = held.withName(q.Name)
			}
			e.seedHolder(t, held, tc.holder)
			before := e.snapshotReuse(t, r)

			_, _, err := e.reuse(t, reuseParams(t, r, q))
			assertLaunchSentinel(t, err, api.ErrTmuxSessionConflict)
			e.assertWroteNothing(t, before)
		})
	}
}

// TestSpawnReusePreTrustWriteFailureStillLaunches: a trust file that cannot
// be written reports failed and the reuse still launches.
func TestSpawnReusePreTrustWriteFailureStillLaunches(t *testing.T) {
	for _, tc := range []struct {
		name string
		file trustFile
	}{{".claude.json missing", trustMissing}, {".claude.json unwritable", trustUnwritable}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRlfEnv(t)
			c := seedTrustConfig(t, t.TempDir(), tc.file)
			r0 := e.seedReusable(t, agentGone, reuseRowSpec{})

			r, res := rlfReuse(t, e, r0, reuseRequest{Env: c.extraEnv()})
			if res.PreTrust != "failed" {
				t.Errorf("reuse pre_trust = %q; want failed", res.PreTrust)
			}
			c.check(t, r.CWD, false, "after the reuse")
			if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 1 {
				t.Errorf("creates = %d; want 1", n)
			}
		})
	}
}
