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

// TestResumeRestoreKeepsSubagentTracking: the move to pending marks the row's
// new launch as tracking subagent events (1), and a failed launch's restore
// puts back what the row had, so a row from before the upgrade (0) is not
// turned into a tracked one and a tracked row (1) stays tracked.
func TestResumeRestoreKeepsSubagentTracking(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts []apitest.SpawnOption
		want int64
	}{
		{"pre-upgrade row restores 0", []apitest.SpawnOption{apitest.WithNoSubagentTracking()}, 0},
		{"tracked row restores 1", nil, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newResumeEnv(t)
			r := e.seedResumable(t, store.StateEnded, tc.opts...)
			if got := r.Before.SubagentTracking; got != tc.want {
				t.Fatalf("seeded subagent_tracking = %#v; want %d", got, tc.want)
			}
			e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, tmux.CallCreate)
			var moved any
			e.store.afterMove(func() { moved = e.columns(t, r.ID).SubagentTracking })

			_, err := e.resume(r.ID)

			assertLaunchSentinel(t, err, tmux.ErrTmuxSessionCreate)
			if moved != int64(1) {
				t.Errorf("subagent_tracking after the move = %#v; want 1", moved)
			}
			if got := e.columns(t, r.ID).SubagentTracking; got != tc.want {
				t.Errorf("subagent_tracking after the restore = %#v; want %d", got, tc.want)
			}
			rstAssertRow(t, e, r.ID, rstRestored(r, nil))
			rstAssertRestoredTrail(t, r.ID, true, "ErrTmuxSessionCreate", nil)
		})
	}
}
