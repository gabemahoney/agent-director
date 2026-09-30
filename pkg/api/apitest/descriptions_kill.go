package apitest

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// descriptions_kill.go holds the shared description helper's SR-1.4 cases
// that `kill` introduces (Epic 10): the three ErrTmuxKillFailed variants (the
// only cases that allow "retry kill later"), the Leftover refusal, the
// follow-up lookup that could not answer after a kill was sent
// (DescCase.AfterKillSent), the socket-directory refusal that ends "nothing
// was done", and the unusable recorded name's three ErrInternal cases. Kill
// reuses DescConflictingLabels (with NothingWasDone), DescDifferentServer,
// DescCallTimeout (lookup, pane listing, pane kill, session kill),
// DescUnrecognisedReply, DescSocketPermission and DescTmuxNotRun as they are.

// killFailedMustNot is what no kill refusal may say (SR-1.4).
var killFailedMustNot = []string{"dead", "gone"}

// killSentConsequence is the consequence sentence of a refusal after a kill
// was sent whose follow-up lookup could not answer (SR-1.4).
const killSentConsequence = "the kill was sent and may or may not have taken effect"

// KillSent says which kills an ErrTmuxKillFailed description states were
// sent: to the agent's pane, to its labelled session, or both. Neither set
// requires only that a kill was sent.
type KillSent struct {
	Pane    bool
	Session bool
}

// require returns the phrase stating s, and a phrase it must not contain.
func (s KillSent) require() (req, mustNot string) {
	switch {
	case s.Pane && s.Session:
		return "a kill was sent to the agent's pane and to its labelled session", ""
	case s.Pane:
		return "a kill was sent to the agent's pane", "and to its labelled session"
	case s.Session:
		return "a kill was sent to its labelled session", "a kill was sent to the agent's pane"
	}
	return "a kill was sent to", ""
}

// killFailed is an ErrTmuxKillFailed case: the instance id, the quoted name,
// req, "retry kill later" (allowed here only) and "never delete this row";
// never "dead" or "gone" (SR-1.4).
func killFailed(variant, instanceID, name string, req, mustNot []string) DescCase {
	return DescCase{
		Name:           "ErrTmuxKillFailed, " + variant,
		Require:        append([]string{instanceID, strconv.Quote(name), retryKillLater, "never delete this row"}, req...),
		MustNot:        append(append([]string(nil), killFailedMustNot...), mustNot...),
		allowRetryKill: true,
	}
}

// KillWaitExpired parameterises DescKillWaitExpired: the row's instance id
// and recorded name; which kills were sent; the kill exit wait's effective
// value; AgentPID, the agent process's pid when it still ran at the end (0:
// it did not); and SurvivorPIDs, the other pane processes of the labelled
// session still running, in the order the call listed them. At least one of
// AgentPID and SurvivorPIDs is set.
type KillWaitExpired struct {
	InstanceID   string
	Name         string
	Sent         KillSent
	ExitWait     time.Duration
	AgentPID     int
	SurvivorPIDs []int
}

// DescKillWaitExpired is ErrTmuxKillFailed variant (a) (SR-1.4, SR-6.1;
// AC-KILL-19): a kill was sent and, after the kill exit wait (its effective
// value in seconds, "5 s"), the agent process or another process of a pane of
// the labelled session was still running, each pid named.
func DescKillWaitExpired(p KillWaitExpired) DescCase {
	if p.AgentPID == 0 && len(p.SurvivorPIDs) == 0 {
		panic("apitest: KillWaitExpired with nothing still running")
	}
	sent, sentNot := p.Sent.require()
	req := []string{sent, "still running after the kill exit wait of " + strconv.FormatFloat(p.ExitWait.Seconds(), 'f', -1, 64) + " s"}
	mustNot := []string{sentNot}
	if p.AgentPID != 0 {
		req = append(req, fmt.Sprintf("the agent process (pid %d)", p.AgentPID))
	} else {
		mustNot = append(mustNot, "the agent process (pid")
	}
	switch n := len(p.SurvivorPIDs); {
	case n == 0:
		mustNot = append(mustNot, "of a pane of the labelled session", "of panes of the labelled session")
	case n == 1:
		req = append(req, fmt.Sprintf("another process of a pane of the labelled session (pid %d)", p.SurvivorPIDs[0]))
	default:
		pids := make([]string, n)
		for i, pid := range p.SurvivorPIDs {
			pids[i] = strconv.Itoa(pid)
		}
		req = append(req, "other processes of panes of the labelled session (pids "+strings.Join(pids, ", ")+")")
	}
	return killFailed("still running after the kill exit wait", p.InstanceID, p.Name, req, mustNot)
}

// DescKillUncheckable is ErrTmuxKillFailed variant (b) (SR-1.4, SR-6.1): a
// kill was sent (sent says which), the agent process cannot be checked and
// its labelled session is still there.
func DescKillUncheckable(instanceID, name string, sent KillSent) DescCase {
	req, mustNot := sent.require()
	return killFailed("agent process cannot be checked", instanceID, name,
		[]string{req, "the agent process cannot be checked and its labelled session is still there"}, []string{mustNot})
}

// DescKillNoPane is ErrTmuxKillFailed variant (c) (SR-1.4, SR-6.1): no
// session or pane of this launch was found while the agent process (agentPID)
// still runs, no kill was sent, and a human can find and look at the process,
// with the "Operator actions" pointer.
func DescKillNoPane(instanceID, name string, agentPID int) DescCase {
	return killFailed("no session or pane found", instanceID, name, []string{
		"no session or pane of this launch was found while its agent process still runs",
		fmt.Sprintf("(pid %d)", agentPID), "no kill was sent", "a human can find and look at the process",
	}, []string{"a kill was sent"}).PointsToOperatorActions()
}

// DescKillLeftover is ErrTmuxSessionConflict for kill of a live row whose
// lookup is Leftover (SR-1.4, SR-6.1): each leftover session's quoted name
// and tmux id, in the order the description names them (lowest $N first; up
// to three, then their count); "this row's own id"; "not this launch's
// session"; that no kill was sent; that ending the session is a human's
// decision, with the "Operator actions" pointer; "list --tmux-session-name".
// Pass any other row's id as forbid.
func DescKillLeftover(sessions []DescSession) DescCase {
	named, unnamed := namedSessions(sessions)
	req := append([]string{
		"this row's own id", "not this launch's session", "no kill was sent",
		"a human's decision", "list --tmux-session-name",
	}, named...)
	return DescCase{
		Name:    "ErrTmuxSessionConflict, kill of a Leftover",
		Require: req,
		MustNot: append([]string{"a kill was sent"}, unnamed...),
	}.PointsToOperatorActions()
}

// AfterKillSent returns c as a refusal given after a kill was sent, whose
// follow-up lookup could not answer (SR-1.4): that the kill was sent and may
// or may not have taken effect, in place of "nothing was done", which it
// must not say; never "dead" or "gone". Use it on DescDifferentServer,
// DescConflictingLabels, DescSocketPermission or DescTmuxNotRun;
// DescKillFollowUpUnresponsive applies it to the unreadable follow-up.
func (c DescCase) AfterKillSent() DescCase {
	c.Name += ", after a sent kill"
	c.Require = append(slices.DeleteFunc(append([]string(nil), c.Require...), func(p string) bool { return p == nothingWasDone }),
		killSentConsequence)
	c.MustNot = append(append(append([]string(nil), c.MustNot...), nothingWasDone), killFailedMustNot...)
	return c
}

// KillFollowUp parameterises DescKillFollowUpUnresponsive: the recorded
// name; the follow-up lookup's effective timeout, or Unrecognised with the
// reply's first line (FirstLine, as for DescUnrecognisedReply).
type KillFollowUp struct {
	Name         string
	Timeout      time.Duration
	Unrecognised bool
	FirstLine    string
}

// DescKillFollowUpUnresponsive is ErrTmuxUnresponsive for kill's follow-up
// lookup that could not answer after a kill was sent (SR-1.4): the lookup's
// timeout or unrecognised reply, the quoted name, that the kill was sent and
// may or may not have taken effect, and "retry later".
func DescKillFollowUpUnresponsive(p KillFollowUp) DescCase {
	c := DescCallTimeout(tmux.CallLookup, p.Timeout)
	if p.Unrecognised {
		c = DescUnrecognisedReply(tmux.CallLookup, p.FirstLine)
		c.Require = append(c.Require, "retry later")
	}
	c = c.AfterKillSent()
	c.Require = append(c.Require, strconv.Quote(p.Name))
	return c
}

// DescSocketDirNothingDone is ErrTmuxNotAvailable for an unusable socket
// directory of a single-row verb (kill of a row that records no socket;
// SR-1.4, SR-3.3): as DescSocketDir, but "nothing was done" in place of
// "nothing was launched", which it must not say.
func DescSocketDirNothingDone(socket, dir, reason string) DescCase {
	return DescCase{
		Name:    "ErrTmuxNotAvailable, unusable socket directory, nothing was done",
		Require: []string{socket, dir, reason, nothingWasDone},
		MustNot: []string{"nothing was launched"},
	}
}

// unusableName is an unusable recorded name's ErrInternal case (SR-1.4,
// SR-3.2): req, then that the name cannot be used, so removing the row is a
// human's decision, the "Operator actions" pointer, and that no tmux call
// was made.
func unusableName(kind string, req, mustNot []string) DescCase {
	return DescCase{
		Name: "ErrInternal, recorded name " + kind,
		Require: append(req,
			"the name cannot be used, so removing the row is a human's decision", "no tmux call was made"),
		MustNot: mustNot,
	}.PointsToOperatorActions()
}

// DescUnusableNameEmpty is ErrInternal for a live row whose recorded tmux
// session name is empty.
func DescUnusableNameEmpty() DescCase {
	return unusableName("empty", []string{"the recorded tmux session name is empty"}, nil)
}

// DescUnusableNameControlChar is ErrInternal for a recorded tmux session name
// with a control character, quoted (Go quoted-string form).
func DescUnusableNameControlChar(name string) DescCase {
	return unusableName("with a control character",
		[]string{strconv.Quote(name), "the recorded tmux session name contains a control character"}, nil)
}

// RewrittenChars says which characters tmux stores differently a recorded
// name holds: '.', ':', bytes that are not valid UTF-8.
type RewrittenChars struct {
	Dot         bool
	Colon       bool
	InvalidUTF8 bool
}

// DescUnusableNameRewritten is ErrInternal for a recorded tmux session name
// holding a character tmux stores differently: the quoted name and which
// (each of which's set; each unset one must not be named).
func DescUnusableNameRewritten(name string, which RewrittenChars) DescCase {
	req := []string{strconv.Quote(name), "the recorded tmux session name contains a character tmux stores differently"}
	var mustNot []string
	for _, ch := range []struct {
		set    bool
		phrase string
	}{{which.Dot, "'.'"}, {which.Colon, "':'"}, {which.InvalidUTF8, "bytes that are not valid UTF-8"}} {
		if ch.set {
			req = append(req, ch.phrase)
		} else {
			mustNot = append(mustNot, ch.phrase)
		}
	}
	return unusableName("tmux rewrites", req, mustNot)
}
