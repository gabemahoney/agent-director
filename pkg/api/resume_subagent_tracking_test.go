package api_test

// resume_subagent_tracking_test.go covers resume's handling of a row's
// subagent_tracking flag across its move to pending and a failed launch's
// restore (resumePriorOf carries the flag into ResumePrior). On the shared
// fixture in resume_fixture_test.go against a real store.

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// resumeTrackingCases are the rows a resume's tracking flag is read on: the
// row's agent and the flag it holds, and the flag the move gives its new
// launch, which follows the agent (claude 1, codex 0) whatever the row held.
var resumeTrackingCases = []struct {
	name  string
	agent string
	opts  []apitest.SpawnOption
	held  int64 // the seeded flag
	moved int64 // the flag of the launch the move begins
}{
	{"claude, pre-upgrade row", "claude", []apitest.SpawnOption{apitest.WithNoSubagentTracking()}, 0, 1},
	{"claude, tracked row", "claude", nil, 1, 1},
	{"codex, a row that held 0", "codex", codexRowOpts(), 0, 0},
	{"codex, a row that held 1", "codex", []apitest.SpawnOption{apitest.WithAgent("codex")}, 1, 0},
}

// TestResumeRestoreKeepsSubagentTracking: the move to pending marks the row's
// new launch by its agent (a claude launch tracks subagent events, 1; a codex
// launch registers none, 0), and a failed launch's restore puts back what the
// row had, so a row from before the upgrade (0) is not turned into a tracked
// one, a tracked row (1) stays tracked, and a codex row's earlier flag comes
// back whatever it was.
func TestResumeRestoreKeepsSubagentTracking(t *testing.T) {
	t.Parallel()
	for _, tc := range resumeTrackingCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newResumeEnv(t)
			r := e.seedResumable(t, store.StateEnded, append([]apitest.SpawnOption{apitest.WithAgent(tc.agent)}, tc.opts...)...)
			if got := r.Before.SubagentTracking; got != tc.held {
				t.Fatalf("seeded subagent_tracking = %#v; want %d", got, tc.held)
			}
			e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, tmux.CallCreate)
			var moved any
			e.store.afterMove(func() { moved = e.columns(t, r.ID).SubagentTracking })

			_, err := e.resume(r.ID)

			assertLaunchSentinel(t, err, tmux.ErrTmuxSessionCreate)
			if moved != tc.moved {
				t.Errorf("subagent_tracking after the move = %#v; want %d", moved, tc.moved)
			}
			if got := e.columns(t, r.ID).SubagentTracking; got != tc.held {
				t.Errorf("subagent_tracking after the restore = %#v; want %d", got, tc.held)
			}
			rstAssertRow(t, e, r.ID, rstRestored(r, nil))
			rstAssertRestoredTrail(t, r.ID, true, "ErrTmuxSessionCreate", nil)
		})
	}
}

// TestResumeSetsSubagentTrackingByAgent: a resume that launches leaves the row
// tracked (1) for a claude agent and untracked (0) for a codex one, over the
// flag it held, and list-subagents then reads the row as it is.
func TestResumeSetsSubagentTrackingByAgent(t *testing.T) {
	t.Parallel()
	for _, tc := range resumeTrackingCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newResumeEnv(t)
			r := e.seedResumable(t, store.StateEnded, append([]apitest.SpawnOption{apitest.WithAgent(tc.agent)}, tc.opts...)...)

			if _, err := e.resume(r.ID); err != nil {
				t.Fatalf("resume: %v", err)
			}

			cols := e.columns(t, r.ID)
			if cols.State != store.StatePending || cols.SubagentTracking != tc.moved {
				t.Errorf("row after the resume: state %v, subagent_tracking %#v; want pending, %d", cols.State, cols.SubagentTracking, tc.moved)
			}
			got, err := e.c.ListSubagents(r.ID)
			if err != nil || got.Tracked != (tc.moved == 1) || len(got.Subagents) != 0 {
				t.Errorf("ListSubagents = %+v, %v; want tracked %v and no subagents", got, err, tc.moved == 1)
			}
		})
	}
}
