package api_test

// security_kill_optin_test.go is kill's part of SR-15's per-verb table
// (security_test.go) with the operator-only finished-row opt-in (SR-6.5;
// Epic 18): an ended target meets the planted SECRET=xyz sessions on the
// finished-row path's Gone (its name held by the no-id session or the other
// row's session), Leftover (and this id's own abandoned launch, still starting
// and ended; b.6sa), conflicting-labels and Ours rows, never reported in and
// reported in. Run by TestSecurityKillOptIn.

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/adminapi"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestSecurityKillOptIn checks SR-15 for kill with the opt-in on a finished
// row: nothing it returns, logs or records carries a forbidden value.
func TestSecurityKillOptIn(t *testing.T) {
	// Serial: its cases scan every record written to the shared trail since their mark for the forbidden
	// values, which only its own cases may write meanwhile; its cases run in parallel.
	runSecurityVerbs(t, securityKillOptInVerbs)
}

// securityKillOptInVerbs is kill with the opt-in through Client.Kill.
var securityKillOptInVerbs = []securityVerb{{
	verb:  "kill with the opt-in",
	event: "ad.kill.called",
	call: func(_ *testing.T, c *api.Client, s *securityScene) (any, error) {
		r, err := adminapi.KillFinished(c, s.subject)
		return api.KillResult{KillSent: r.KillSent}, err
	},
	cases: securityKillOptInCases,
}}

// koSecFields is the opt-in's ad.kill.called fields for lookup outcome lookup
// and kill_sent sent.
func koSecFields(lookup string, sent bool) map[string]any {
	return map[string]any{"include_finished": true, "lookup_outcome": lookup, "kill_sent": sent}
}

// koSecAbandoned seeds a session of an earlier launch of the target, which
// records no session of its own launch (b.6sa), the session's pane process
// its agent; with past, the clock moves past the starting-session bound and
// the agent exits at its pane kill.
func koSecAbandoned(past bool) func(*testing.T, *securityScene) {
	return func(t *testing.T, s *securityScene) {
		s.e.seedSession(t, &s.target, tmuxfix.WithRowSessionLabel(s.target.old(), true))
		if past {
			koPastBoth(s.e)
			s.e.setAfterCall(tmux.CallKillPane, procfix.Gone(), s.target.AgentPID)
		}
	}
}

// koSecNoPane is the Gone refusal with the target's agent running and no pane of it found.
func koSecNoPane(s *securityScene) apitest.DescCase {
	return apitest.DescKillNoPane(s.target.ID, s.target.Name, s.target.AgentPID)
}

// securityKillOptInCases meet the planted sessions on the opt-in's
// finished-row table (SR-6.5): Gone, Leftover, conflicting labels, and Ours
// past both, never reported in (session created after ended_at) and reported in.
var securityKillOptInCases = []securityCase{
	{
		name:    "gone, name held by the no-id session",
		target:  koEnded(killRowSpec{NoSession: true}, time.Second),
		holder:  securityHolderNoID,
		wantErr: api.ErrTmuxKillFailed,
		desc:    koSecNoPane,
		fields:  koSecFields("gone", false),
	},
	{
		name:    "gone, name held by the other row's session",
		target:  koEnded(killRowSpec{NoSession: true}, time.Second),
		holder:  securityHolderOther,
		wantErr: api.ErrTmuxKillFailed,
		desc:    koSecNoPane,
		fields:  koSecFields("gone", false),
	},
	{
		name:   "leftover, never reported in",
		target: koEnded(killRowSpec{NoSession: true}, time.Second),
		arrange: func(t *testing.T, s *securityScene) {
			s.e.seedSession(t, &s.target, tmuxfix.WithRowSessionLabel(s.target.old(), true))
		},
		wantErr: api.ErrTmuxSessionConflict,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescKillOptInNeverReportedInLeftover(s.target.ID,
				[]apitest.DescSession{{Name: s.target.Session.Name, ID: s.target.Session.ID}})
		},
		fields: koSecFields("leftover", false),
	},
	{
		name:    "leftover, this id's own abandoned launch, still starting",
		target:  koEnded(killRowSpec{NoSession: true, NoServerIdentity: true, NoPane: true}, time.Second),
		arrange: koSecAbandoned(false),
		wantErr: api.ErrTmuxUnresponsive,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescAbandonedLaunch(apitest.AbandonedLaunch{InstanceID: s.target.ID,
				Sessions: []apitest.DescSession{{Name: s.target.Session.Name, ID: s.target.Session.ID}},
				Bound:    s.e.cfg.EffectiveStartingSession()})
		},
		fields: koSecFields("leftover", false),
	},
	{
		name:    "leftover, this id's own abandoned launch past the bound, ended",
		target:  koEnded(killRowSpec{NoSession: true, NoServerIdentity: true, NoPane: true}, time.Second),
		arrange: koSecAbandoned(true),
		fields:  koSecFields("leftover", true),
		check: func(t *testing.T, _ *securityScene, res any) {
			if !res.(api.KillResult).KillSent {
				t.Errorf("kill_sent = false; want true")
			}
		},
	},
	{
		name:     "conflicting labels",
		target:   koEnded(killRowSpec{}, time.Second),
		arrange:  secDuplicateLabel,
		wantErr:  api.ErrTmuxSessionConflict,
		desc:     secConflictingLabels,
		disagree: true,
		fields:   koSecFields("provenance_conflict", false),
	},
	{
		name:    "ours, never reported in",
		target:  koEnded(killRowSpec{}, -time.Hour),
		arrange: func(_ *testing.T, s *securityScene) { koPastBoth(s.e) },
		wantErr: api.ErrTmuxSessionConflict,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescKillOptInNeverReportedIn(s.target.ID, s.target.Name, s.e.cfg.EffectiveStartingSession())
		},
		fields: koSecFields("ours", false),
	},
	{
		name:   "ours, reported in, success",
		target: koEnded(killRowSpec{}, time.Second),
		arrange: func(_ *testing.T, s *securityScene) {
			koPastBoth(s.e)
			s.e.setAfterCall(tmux.CallKillPane, procfix.Gone(), s.target.AgentPID)
		},
		fields: koSecFields("ours", true),
		check: func(t *testing.T, _ *securityScene, res any) {
			if !res.(api.KillResult).KillSent {
				t.Errorf("kill_sent = false; want true")
			}
		},
	},
}
