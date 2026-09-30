package realtmux_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// kill through the production pkg/api client on real tmux (SRD SR-6.1,
// SR-3.6, SR-3.7, SR-20.7; PRD AC-KILL-01, AC-KILL-03, AC-KILL-17, AC-KILL-18,
// AC-KILL-19, AC-LKP-21). Teammates are split panes only.

// TestKillSharedPaneEndsAgent: with the agent's window shared by a grouped
// session or a linked window, or a teammate pane beside it, kill succeeds only once every pane process is gone.
func TestKillSharedPaneEndsAgent(t *testing.T) {
	cases := []struct {
		name string
		// share adds what shares the agent's session or window and returns
		// the check made after the kill.
		share func(t *testing.T, r killRow) func(t *testing.T)
	}{
		{"grouped session", func(t *testing.T, r killRow) func(t *testing.T) {
			win := r.RT.format(t, r.Reply.PaneID, "#{window_id}")
			viewer := strings.TrimSpace(r.RT.must(t, "new-session", "-d", "-t", r.Reply.SessionID, "-s", uniqueName(), "-P", "-F", "#{session_id}"))
			if !slices.Contains(r.RT.windowsOf(t, viewer), win) {
				t.Fatalf("grouped session %s does not show window %s", viewer, win)
			}
			return func(t *testing.T) {
				if slices.Contains(r.RT.windowsOf(t, viewer), win) {
					t.Errorf("grouped session %s still shows the agent's window %s", viewer, win)
				}
			}
		}},
		{"linked window", func(t *testing.T, r killRow) func(t *testing.T) {
			win := r.RT.format(t, r.Reply.PaneID, "#{window_id}")
			other := r.RT.startSession(t, "")
			own := r.RT.windowsOf(t, other.ID)
			r.RT.must(t, "link-window", "-d", "-s", win, "-t", other.ID+":")
			if !slices.Contains(r.RT.windowsOf(t, other.ID), win) {
				t.Fatalf("session %s does not show the linked window %s", other.ID, win)
			}
			return func(t *testing.T) {
				if got := r.RT.windowsOf(t, other.ID); !slices.Equal(got, own) {
					t.Errorf("session %s shows windows %v, want only its own %v", other.ID, got, own)
				}
				assertProcs(t, false, other.PanePID)
			}
		}},
		{"teammate pane", func(t *testing.T, r killRow) func(t *testing.T) {
			_, pid := idtSplit(t, r.RT, r.Reply.SessionID, stubCommand())
			return func(t *testing.T) { assertProcs(t, true, pid) }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newKillFix(t)
			r := f.liveRow(t, killRowSpec{})
			check := tc.share(t, r)

			k := f.kill(t, r.InstanceID, 0)
			k.assertSuccess(t, true)
			assertProcs(t, true, r.Reply.PanePID)
			r.RT.assertSession(t, r.Reply.SessionID, false)
			check(t)
			f.assertRowUnchanged(t, r.InstanceID, r.Before)
		})
	}
}

// TestKillSurvivorIsKillFailed: a pane process that ignores SIGHUP, the
// agent's or a teammate's, gives ErrTmuxKillFailed naming its pid once the exit wait passes.
func TestKillSurvivorIsKillFailed(t *testing.T) {
	const wait = 300 * time.Millisecond
	cases := []struct {
		name     string
		agentCmd []string // nil: stubCommand, which exits on SIGHUP
		teammate bool     // a split pane running hupSurvivor
	}{
		{name: "agent survives SIGHUP", agentCmd: hupSurvivor()},
		{name: "teammate survives SIGHUP", teammate: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newKillFix(t)
			r := f.liveRow(t, killRowSpec{Command: tc.agentCmd})
			want := apitest.KillWaitExpired{InstanceID: r.InstanceID, Name: r.Name,
				Sent: apitest.KillSent{Pane: true, Session: true}, ExitWait: wait}
			survivor := r.Reply.PanePID
			if tc.teammate {
				_, survivor = idtSplit(t, r.RT, r.Reply.SessionID, hupSurvivor())
				endAtCleanup(t, survivor)
				want.SurvivorPIDs = []int{survivor}
			} else {
				want.AgentPID = survivor
			}

			k := f.kill(t, r.InstanceID, wait)
			k.assertRefused(t, "ErrTmuxKillFailed", apitest.DescKillWaitExpired(want), r.Token, f.StoreID)
			if k.Took < wait {
				t.Errorf("kill returned after %s, before the kill exit wait of %s", k.Took, wait)
			}
			assertProcs(t, false, survivor)
			if tc.teammate {
				assertProcs(t, true, r.Reply.PanePID)
			}
			f.assertRowUnchanged(t, r.InstanceID, r.Before)
		})
	}
}

// TestKillPendingRefusesOldLabel: a pending row beside a hand-made session
// under its name labelled for an earlier launch of its id is refused; the session runs on.
func TestKillPendingRefusesOldLabel(t *testing.T) {
	f := newKillFix(t)
	id, name, token := newInstanceID("agent"), uniqueName(), newToken(t)
	s := f.startSessionWithID(t, name, id)
	f.must(t, "set-option", "-t", s.ID, "@ad_owner", tmuxfix.LabelValue(newToken(t), s.ID, id, f.StoreID))
	world := idtWorld(t, f.realTmux)
	before := f.seedRow(t, id, name, "pending", store.LaunchIdentity{Token: token, Socket: f.Socket})

	k := f.kill(t, id, 0)
	k.assertRefused(t, "ErrTmuxSessionConflict", apitest.DescKillLeftover([]apitest.DescSession{{Name: name, ID: s.ID}}), token, f.StoreID)
	idtSame(t, "the refused kill", world, idtWorld(t, f.realTmux), idtPaneIDs(world)...)
	assertProcs(t, false, s.PanePID)
	f.assertRowUnchanged(t, id, before)
}

// TestKillBaseIndexOneAdoptsLostReply: with base-index and pane-base-index 1,
// kill of a pending row with no recorded pane adopts it by @ad_pane and ends it.
func TestKillBaseIndexOneAdoptsLostReply(t *testing.T) {
	f := newKillFix(t)
	f.startSession(t, "") // starts the server the options are set on
	f.must(t, "set-option", "-g", "base-index", "1")
	f.must(t, "set-option", "-gw", "pane-base-index", "1")
	r := f.liveRow(t, killRowSpec{State: "pending", NoPane: true})
	if w, i := f.formatInt(t, r.Reply.PaneID, "#{window_index}"), f.formatInt(t, r.Reply.PaneID, "#{pane_index}"); w != 1 || i != 1 {
		t.Fatalf("agent pane %s is window %d pane %d, want 1.1", r.Reply.PaneID, w, i)
	}
	if r.Before.PaneID != nil {
		t.Fatalf("row records pane %v, want none (a lost reply)", r.Before.PaneID)
	}

	k := f.kill(t, r.InstanceID, 0)
	k.assertSuccess(t, true)
	assertProcs(t, true, r.Reply.PanePID)
	f.assertSession(t, r.Reply.SessionID, false)
	row := readRow(t, f.DBPath, r.InstanceID)
	got := fmt.Sprint(row.State, " ", row.PaneID, " ", row.PanePID, " ", row.PaneStarttime)
	if want := fmt.Sprint("pending ", r.Reply.PaneID, " ", r.Reply.PanePID, " ", r.PaneStart); got != want {
		t.Errorf("row state and pane after kill = %s, want %s (pane adopted, still pending)", got, want)
	}
}
