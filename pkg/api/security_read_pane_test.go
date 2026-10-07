package api_test

// security_read_pane_test.go is read-pane's part of SR-15's per-verb table
// (security_test.go; Epic 11): read-pane meets the planted SECRET=xyz
// sessions on its Gone (a planted session or another store's session holding
// the row's name or id), lone-Leftover, conflicting-labels and Ours paths. It
// writes no trail event (SR-7.5), captures only the agent's own pane or the
// lone leftover's, by pane id, and nothing it returns or logs carries xyz, a
// launch token, the other row's id or another store's id.

import (
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// securityReadPaneCall gives every pane on the target's socket its own text,
// the planted sessions' carrying SECRET=xyz, then runs read-pane on the subject.
func securityReadPaneCall(_ *testing.T, c *api.Client, s *securityScene) (any, error) {
	s.e.setPaneTexts(s.target.Socket)
	for _, sess := range s.e.rec.Sessions(s.target.Socket) {
		if sess.ID != s.noIDSess && sess.ID != s.otherSess {
			continue
		}
		for _, p := range sess.Panes {
			s.e.rec.SetCapture(s.target.Socket, p.ID, "SECRET="+securitySecret+"\n"+paneText(s.target.Socket, p.ID))
		}
	}
	return c.ReadPane(api.ReadPaneParams{ClaudeInstanceID: s.subject})
}

// rpSecReads checks that the call changed no tmux state and captured only the
// pane pane(s) names, returning that pane's own text (pane nil: no capture).
func rpSecReads(pane func(s *securityScene) string) func(*testing.T, *securityScene, any) {
	return func(t *testing.T, s *securityScene, res any) {
		t.Helper()
		s.e.assertNoCalls(t, paneWriteCalls...)
		if pane == nil {
			s.e.assertNoCalls(t, tmux.CallCapture)
			return
		}
		want := pane(s)
		s.e.assertCaptured(t, want, api.DefaultReadPaneLines, false)
		if got, text := res.(api.ReadPaneResult).Pane, paneText(s.target.Socket, want); got != text {
			t.Errorf("pane = %q; want %s's text %q", got, want, text)
		}
	}
}

// rpSecGone is read-pane's gone error for the target: nothing read, its name quoted.
func rpSecGone(s *securityScene) apitest.DescCase {
	return apitest.DescPaneGone(apitest.PaneGone{Verb: apitest.PaneReadPane, InstanceID: s.target.ID, Name: s.target.Name})
}

// rpSecOtherStore returns an arrange that creates, on the target's socket,
// another store's session named name(s) labelled for the target's id with
// token(s), carrying SECRET=xyz (WD 2026-09-29 STORE).
func rpSecOtherStore(name, token func(s *securityScene) string) func(*testing.T, *securityScene) {
	return func(t *testing.T, s *securityScene) {
		id := securityCreate(t, s.e, s.target.Socket, name(s), token(s), s.target.ID, apitest.OtherStoreID(s.e.storeID))
		s.extraSess = tmuxfix.SeedSession{ID: id, Name: name(s)}
	}
}

// secDuplicateLabel is the pane verbs' and kill's conflicting-labels arrange: a
// second session carrying the target's current label (s.extraSess).
func secDuplicateLabel(t *testing.T, s *securityScene) {
	s.extraSess = s.e.seedOther(t, s.target.Socket,
		tmuxfix.SeedSession{Name: "dup-" + uuid.NewString()[:8], Label: s.target.current()})
}

// secConflictingLabels is secDuplicateLabel's refusal: both sessions named, nothing done.
func secConflictingLabels(s *securityScene) apitest.DescCase {
	return apitest.DescConflictingLabels(apitest.ConflictingLabels{InstanceID: s.target.ID, NothingWasDone: true,
		Sessions: []apitest.DescSession{{Name: s.target.Session.Name, ID: s.target.Session.ID},
			{Name: s.extraSess.Name, ID: s.extraSess.ID}}})
}

// securityReadPaneCases meet the planted sessions on read-pane's Gone (a
// holder of each kind), lone-Leftover, conflicting-labels and Ours paths (SR-7.2).
var securityReadPaneCases = []securityCase{
	{
		name:    "gone, name held by the no-id session",
		target:  killRowSpec{NoSession: true},
		holder:  securityHolderNoID,
		wantErr: api.ErrTmuxCaptureFailed,
		desc:    rpSecGone,
		check:   rpSecReads(nil),
	},
	{
		name:    "gone, name held by the other row's session",
		target:  killRowSpec{NoSession: true},
		holder:  securityHolderOther,
		wantErr: api.ErrTmuxCaptureFailed,
		desc:    rpSecGone,
		check:   rpSecReads(nil),
	},
	{
		name:   "gone, another store's session with this launch's name, token and id",
		target: killRowSpec{NoSession: true},
		arrange: rpSecOtherStore(func(s *securityScene) string { return s.target.Name },
			func(s *securityScene) string { return s.target.Token }),
		wantErr: api.ErrTmuxCaptureFailed,
		desc:    rpSecGone,
		check:   rpSecReads(nil),
	},
	{
		name:   "gone, another store's session naming the row's id with another launch's token",
		target: killRowSpec{NoSession: true},
		arrange: rpSecOtherStore(func(*securityScene) string { return "store-" + uuid.NewString()[:8] },
			func(*securityScene) string { return tmuxfix.OtherToken }),
		wantErr: api.ErrTmuxCaptureFailed,
		desc:    rpSecGone,
		check:   rpSecReads(nil),
	},
	{
		name:   "lone leftover, its pane is read",
		target: killRowSpec{NoSession: true},
		arrange: func(t *testing.T, s *securityScene) {
			s.extraSess = s.e.seedLeftover(t, s.target, tmuxfix.OtherToken)
		},
		check: rpSecReads(func(s *securityScene) string { return s.extraSess.Panes[0].ID }),
	},
	{
		name:    "conflicting labels",
		arrange: secDuplicateLabel,
		wantErr: api.ErrTmuxSessionConflict,
		desc:    secConflictingLabels,
		check:   rpSecReads(nil),
	},
	{
		name:  "ours, the agent's pane is read",
		check: rpSecReads(func(s *securityScene) string { return s.target.Spawn.Identity.PaneID }),
	},
}
