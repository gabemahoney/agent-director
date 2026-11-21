package store

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// TestMarkKeylessPauseEndedAgentIdentitySelection (b.nqq) keeps the store's
// recorded-field guard equivalent to the shared selector without adding its
// tmux dependency to production or the transitive hook path.
func TestMarkKeylessPauseEndedAgentIdentitySelection(t *testing.T) {
	t.Parallel()
	for _, pid := range []int{-1, 0, 101, 202} {
		for _, start := range []string{"", "10", "20"} {
			for _, panePID := range []int{-1, 0, 101, 202} {
				for _, paneStart := range []string{"", "10", "20"} {
					row := Spawn{PID: pid, ProcStarttime: start, Identity: LaunchIdentity{PanePID: panePID, PaneStarttime: paneStart}}
					want := tmux.SelectAgentProcess(tmux.ProcIdentity{PID: pid, Starttime: start},
						tmux.ProcIdentity{PID: panePID, Starttime: paneStart}).Identity
					if got := keylessPauseAgent(row); got != (ProcessIdentity{PID: want.PID, Starttime: want.Starttime}) {
						t.Errorf("SessionStart=%d/%q pane=%d/%q: got %+v; want %+v", pid, start, panePID, paneStart, got, want)
					}
				}
			}
		}
	}
}
