package api

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// This file holds kill's own refusal descriptions (SR-1.4): Leftover's
// ErrTmuxSessionConflict and the three ErrTmuxKillFailed variants. Can't tell
// goes through the single-row verbs' shared cantTellError and an unusable
// recorded name through unusableNameError. No description carries a label's
// value, another row's id, a session-environment value, "dead" or "gone", or
// a session-ending command other than ErrTmuxKillFailed's "retry kill later".

// killFailedTail ends every ErrTmuxKillFailed description (SR-1.4).
const killFailedTail = "retry kill later; never delete this row"

// killLeftoversNamed is how many leftover sessions the Leftover refusal names
// before giving the rest as a count (as the plain-spawn scan does, SR-1.4).
const killLeftoversNamed = 3

// leftoverError is kill's refusal of a live row whose lookup is Leftover
// (SR-6.1, SR-1.4; P1): the instance id; "not this launch's session"; each
// leftover session's quoted name and tmux id, lowest $N first, up to
// killLeftoversNamed, then the rest as a count; "this row's own id"; that no
// kill was sent; that ending the session is a human's decision, with the
// pointer to "Operator actions"; and "list --tmux-session-name".
func leftoverError(instanceID string, leftovers []tmux.Session) error {
	sorted := sortedBySessionNumber(leftovers)
	found := namedSessions(sorted, killLeftoversNamed)
	what := "tmux session " + found + " carries the label of an earlier launch with this row's own id"
	if len(sorted) > 1 {
		what = "tmux sessions " + found + " carry labels of earlier launches with this row's own id"
	}
	return fmt.Errorf("%w: instance %s: not this launch's session: %s; no kill was sent; ending such a session is a human's decision, %s; %s",
		tmux.ErrTmuxSessionConflict, instanceID, what, operatorActionsPointer, listSessionNameHint)
}

// noPaneError is ErrTmuxKillFailed variant (c) (SR-1.4, SR-6.1): the lookup
// found no session of this launch (Gone) and no pane of it was found while
// the agent process still runs, so no kill was sent; a human can find and
// look at the process by "Operator actions".
func (k *killRun) noPaneError() error {
	return fmt.Errorf("%w: instance %s: %s: no session or pane of this launch was found while its agent process still runs (pid %d), so no kill was sent; a human can find and look at the process, %s; %s",
		tmux.ErrTmuxKillFailed, k.id, k.context, k.agentPID, operatorActionsPointer, killFailedTail)
}

// uncheckableError is ErrTmuxKillFailed variant (b) (SR-1.4, SR-6.1): a kill
// was sent, the agent process cannot be checked, and the follow-up lookup
// still finds the current label.
func (k *killRun) uncheckableError() error {
	return fmt.Errorf("%w: instance %s: %s: %s, but the agent process cannot be checked and its labelled session is still there; %s",
		tmux.ErrTmuxKillFailed, k.id, k.context, k.sentWhat(), killFailedTail)
}

// waitExpiredError is ErrTmuxKillFailed variant (a) (SR-1.4, SR-6.1;
// AC-KILL-19): a kill was sent and, after the kill exit wait (its effective
// value in seconds), the agent process (agentRuns) or another process of a
// pane of the labelled session (k.survivorPIDs) was still running; each pid
// is named.
func (k *killRun) waitExpiredError(agentRuns bool) error {
	var running []string
	if agentRuns {
		running = append(running, fmt.Sprintf("the agent process (pid %d)", k.agentPID))
	}
	switch n := len(k.survivorPIDs); {
	case n == 1:
		running = append(running, fmt.Sprintf("another process of a pane of the labelled session (pid %d)", k.survivorPIDs[0]))
	case n > 1:
		pids := make([]string, n)
		for i, pid := range k.survivorPIDs {
			pids[i] = strconv.Itoa(pid)
		}
		running = append(running, "other processes of panes of the labelled session (pids "+strings.Join(pids, ", ")+")")
	}
	verb := "was"
	if len(running) > 1 || len(k.survivorPIDs) > 1 {
		verb = "were"
	}
	return fmt.Errorf("%w: instance %s: %s: %s, and %s %s still running after the kill exit wait of %s; %s",
		tmux.ErrTmuxKillFailed, k.id, k.context, k.sentWhat(), strings.Join(running, " and "), verb,
		inSeconds(k.exitWait), killFailedTail)
}

// sentWhat says which kills were sent: the agent's pane, the labelled
// session, or both.
func (k *killRun) sentWhat() string {
	switch {
	case k.paneKilled && k.sessionKilled:
		return "a kill was sent to the agent's pane and to its labelled session"
	case k.paneKilled:
		return "a kill was sent to the agent's pane"
	}
	return "a kill was sent to its labelled session"
}

// inSeconds renders d in seconds as descriptions state an effective value
// ("5 s", "0.3 s").
func inSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + " s"
}
