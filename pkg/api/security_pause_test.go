package api_test

// security_pause_test.go is pause's part of SR-15's per-verb table
// (security_test.go; Epic 11): pause on a waiting row meets the planted
// SECRET=xyz sessions on its Gone (a planted session or another store's
// session holding the row's name), Leftover, conflicting-labels and Ours
// paths (a lost create reply's adoption included). It writes no call event,
// only ad.provenance.disagree when a reason arises (verb pause, source
// ad_send_keys), and types /exit only into the agent's own pane, by pane id.

import (
	"context"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// securityPauseCall is pause's call on the subject.
func securityPauseCall(_ *testing.T, c *api.Client, s *securityScene) (any, error) {
	return c.Pause(context.Background(), api.PauseParams{ClaudeInstanceID: s.subject})
}

// psSecEnds ends the target's row as its own agent after the first Enter, so
// the Ours rows' wait returns at once.
func psSecEnds(t *testing.T, s *securityScene) { s.e.endAfterEnter(t, s.target) }

// psSecEndEvents are the records that SessionEnd writes for the row during
// the call (its state transition), not pause's.
var psSecEndEvents = []string{"ad.spawn.state_transition"}

// psSecChecks checks pause's sends and its disagree records: /exit and Enter
// delivered to the agent's pane (the recorded one, else the adopted first
// pane) or, when not delivered, nothing sent and no planted pane targeted
// (skSecSends); every disagree record is verb pause, source ad_send_keys,
// with action and, when reason is set, one record with that reason.
func psSecChecks(delivered bool, action, reason string) func(*testing.T, *securityScene, any) {
	return func(t *testing.T, s *securityScene, res any) {
		t.Helper()
		if delivered {
			pane := s.target.Spawn.Identity.PaneID
			if pane == "" {
				pane = s.target.Session.Panes[0].ID
			}
			s.e.assertExitDelivered(t, s.target.Socket, pane)
		} else {
			skSecSends(false)(t, s, res)
		}
		recs := pauseDisagrees(t, s.subject)
		reasons := 0
		for _, r := range recs {
			if r["source"] != "ad_send_keys" || r["action"] != action {
				t.Errorf("pause disagree source/action = %v/%v; want ad_send_keys/%s", r["source"], r["action"], action)
			}
			if r["reason"] == reason {
				reasons++
			}
		}
		if reason != "" && reasons != 1 {
			t.Errorf("pause disagree records with reason %s = %d of %v; want 1", reason, reasons, recs)
		}
	}
}

// psSecGone is pause's gone error for the target: nothing sent, its name quoted.
func psSecGone(s *securityScene) apitest.DescCase {
	return apitest.DescPaneGone(apitest.PaneGone{Verb: apitest.PanePause, InstanceID: s.target.ID, Name: s.target.Name})
}

// securityPauseCases meet the planted sessions on pause's Gone, Leftover,
// conflicting-labels and Ours paths (SR-7.2, SR-3.6) on a waiting row.
var securityPauseCases = []securityCase{
	{
		name:    "gone, name held by the no-id session",
		target:  killRowSpec{NoSession: true},
		holder:  securityHolderNoID,
		wantErr: api.ErrTmuxSendKeys,
		desc:    psSecGone,
		check:   psSecChecks(false, "nothing_sent", ""),
	},
	{
		name:    "gone, name held by the other row's session",
		target:  killRowSpec{NoSession: true},
		holder:  securityHolderOther,
		wantErr: api.ErrTmuxSendKeys,
		desc:    psSecGone,
		check:   psSecChecks(false, "nothing_sent", ""),
	},
	{
		name:   "gone, another store's session with this launch's name, token and id",
		target: killRowSpec{NoSession: true},
		arrange: rpSecOtherStore(func(s *securityScene) string { return s.target.Name },
			func(s *securityScene) string { return s.target.Token }),
		wantErr: api.ErrTmuxSendKeys,
		desc:    psSecGone,
		check:   psSecChecks(false, "nothing_sent", ""),
	},
	{
		name:   "leftover",
		target: killRowSpec{NoSession: true},
		arrange: func(t *testing.T, s *securityScene) {
			s.e.seedSession(t, &s.target, tmuxfix.WithRowSessionLabel(s.target.old(), true))
		},
		wantErr: api.ErrTmuxSessionConflict,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescPaneLeftover(apitest.PaneLeftover{Verb: apitest.PanePause, InstanceID: s.target.ID,
				Sessions: []apitest.DescSession{{Name: s.target.Session.Name, ID: s.target.Session.ID}}})
		},
		check: psSecChecks(false, "nothing_sent", ""),
	},
	{
		name:     "conflicting labels",
		arrange:  secDuplicateLabel,
		wantErr:  api.ErrTmuxSessionConflict,
		desc:     secConflictingLabels,
		disagree: true,
		check:    psSecChecks(false, "nothing_sent", tmux.ReasonDuplicateLabel),
	},
	{
		name:    "ours, /exit typed into the agent's pane",
		arrange: psSecEnds,
		others:  psSecEndEvents,
		check:   psSecChecks(true, "keys_sent", ""),
	},
	{
		name:     "ours after a lost create reply, the token pane adopted and /exit typed into it",
		target:   killRowSpec{NoServerIdentity: true, NoPane: true},
		arrange:  psSecEnds,
		others:   psSecEndEvents,
		disagree: true,
		check:    psSecChecks(true, "keys_sent", tmux.ReasonAdopted),
	},
}
