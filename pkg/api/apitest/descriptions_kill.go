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
// was done", and kill's own texts: its manifest description
// (DescKillManifest, whose tmux error classes come from tmuxErrorClasses, the
// definition the pane verbs' DescPaneManifest shares), and the ErrInternal
// trigger (DescKillInternalTrigger, over descriptions_unusable.go's
// DescUnusableNameTrigger) and repeated-kill limitation
// (DescKillRepeatedAfterLastSession) it shares with Client.Kill's Go doc
// prose. Kill reuses DescConflictingLabels (with NothingWasDone),
// DescDifferentServer, DescCallTimeout (lookup, pane listing, pane kill,
// session kill), DescUnrecognisedReply, DescSocketPermission,
// DescTmuxNotRun and the unusable-name cases (descriptions_unusable.go) as
// they are.

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
	return leftoverCase("ErrTmuxSessionConflict, kill of a Leftover", sessions,
		[]string{"no kill was sent", "a human's decision"}, []string{"a kill was sent"})
}

// leftoverCase is the Leftover refusal's part that kill's (DescKillLeftover)
// and the pane verbs' (DescPaneLeftover) share (SR-1.4, SR-3.4): each
// leftover session's quoted name and tmux id as namedSessions gives them
// (the rest's quoted names must not appear), "this row's own id", "not this
// launch's session", "list --tmux-session-name" and the "Operator actions"
// pointer, plus the verb's own req and mustNot phrases.
func leftoverCase(name string, sessions []DescSession, req, mustNot []string) DescCase {
	named, unnamed := namedSessions(sessions)
	return DescCase{
		Name:    name,
		Require: append(append([]string{thisRowsOwnID, notThisLaunch, listSessionName}, req...), named...),
		MustNot: append(append([]string(nil), mustNot...), unnamed...),
	}.PointsToOperatorActions()
}

// The phrases every Leftover and pane-not-found refusal shares (SR-1.4).
const (
	thisRowsOwnID   = "this row's own id"
	notThisLaunch   = "not this launch's session"
	listSessionName = "list --tmux-session-name"
)

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

// swallowedStatements are statements that a tmux failure of kill is
// swallowed or only logged, which kill's texts never make (SR-18.9).
var swallowedStatements = []string{"swallow", "swallowed", "swallows", "logged", "WARN"}

// DescKillInternalTrigger is DescUnusableNameTrigger as kill's manifest
// description and Client.Kill's Go doc prose state it (SR-1.7, SR-3.2), also
// never saying that tmux failures are swallowed or logged (SR-18.9).
func DescKillInternalTrigger() DescCase {
	c := DescUnusableNameTrigger()
	c.Name = "kill, " + c.Name
	c.MustNot = append(append([]string(nil), c.MustNot...), swallowedStatements...)
	return c
}

// DescKillRepeatedAfterLastSession is the limitation kill's manifest
// description and Client.Kill's Go doc prose state (decision-0930b Q5): a
// repeated kill right after the last session on its tmux server ends can get
// ErrTmuxUnresponsive or ErrTmuxNotAvailable while the server exits, and the
// caller waits and checks again. verb is how the text names the verb: "kill"
// in the manifest description, "Kill" in the Go doc.
func DescKillRepeatedAfterLastSession(verb string) DescCase {
	return DescCase{
		Name: "kill, repeated kill after the last session ends",
		Require: []string{
			"A repeated " + verb + " right after the last session on its tmux server ends",
			"can get ErrTmuxUnresponsive or ErrTmuxNotAvailable while the server exits",
			"the caller waits and checks again",
		},
	}
}

// SR-18.7's two consequences of running kill or find-missing as the wrong user
// or against the wrong tmux server, as the one sentence DescKillManifest and
// DescFindMissingManifest both require: "kill's success on a finished row is
// not verification that the agent exited" (finishedRowNotVerification); and
// "on the wrong tmux server, a row wrongly marked missing, kill's no-op
// success and a reuse together start a second agent for the same id"
// (wrongServerSecondAgent).
const (
	notVerification            = "not verification"
	finishedRowNotVerification = "kill's success on a finished row is " + notVerification + " that the agent exited"
	wrongServerSecondAgent     = "on the wrong tmux server, a row wrongly marked missing, " +
		"kill's no-op success and a reuse together start a second agent for the same id"
)

// The tmux error classes of SR-1.1 that verb manifest Descriptions state
// (SR-18.1's last paragraph).
const (
	classGone        = "GONE"
	classUnavailable = "UNAVAILABLE"
	classConflict    = "CONFLICT"
	classEnvironment = "ENVIRONMENT"
)

// tmuxErrorClasses is the one tmux error name to class definition that the
// manifest Description cases share (DescKillManifest, DescPaneManifest;
// SR-1.1). The pane verbs' gone names are GONE; kill has none (for kill,
// GONE is success).
var tmuxErrorClasses = map[string]string{
	"ErrTmuxCaptureFailed":   classGone,
	"ErrTmuxSendKeys":        classGone,
	"ErrTmuxKillFailed":      classUnavailable,
	"ErrTmuxUnresponsive":    classUnavailable,
	"ErrTmuxSessionConflict": classConflict,
	"ErrTmuxNotAvailable":    classEnvironment,
}

// classStated returns the phrase stating tmux error name's class in a
// manifest Description, "ErrX (CLASS", left open so that a note may follow
// the class before the ")". It panics on a name tmuxErrorClasses lacks.
func classStated(name string) string {
	class, ok := tmuxErrorClasses[name]
	if !ok {
		panic("apitest: no SR-1.1 class defined for tmux error " + strconv.Quote(name))
	}
	return name + " (" + class
}

// classStatedAlone is classStated closed, "ErrX (CLASS)": the class with no
// note after it.
func classStatedAlone(name string) string {
	return classStated(name) + ")"
}

// DescKillManifest is kill's manifest description (SR-6.1, SR-1.7, SR-18.1,
// SR-18.7, SR-18.9; decision-0930b Q4 and Q5): success only once the agent
// process is gone, else ErrTmuxKillFailed; kill_sent; a finished row's no-op
// success is not verification; the row's state is not changed; the per-call
// contract; each error's class (tmuxErrorClasses), GONE being success;
// DescKillRepeatedAfterLastSession("kill"); never delete; the same user and
// tmux environment and their two consequences; and DescKillInternalTrigger.
// Check it with AssertAgentTextCase.
func DescKillManifest() DescCase {
	trigger := DescKillInternalTrigger()
	require := []string{
		"succeeds only once the agent process is gone; otherwise it returns ErrTmuxKillFailed",
		"kill_sent says whether a kill was sent",
		"On a finished row (ended or missing) kill is a no-op success with kill_sent false and no tmux call",
		"that is " + notVerification + " that the agent exited",
		"kill never changes the row's state",
		"Success is judged per call", "later calls do not track it",
		"a retried kill checks only the agent process",
		"A retry's success means only that the agent is gone",
		"(for kill, " + classGone + " is success)",
		classStatedAlone("ErrTmuxKillFailed"), classStatedAlone("ErrTmuxUnresponsive"),
		classStated("ErrTmuxSessionConflict"), classStatedAlone("ErrTmuxNotAvailable"),
	}
	require = append(require, DescKillRepeatedAfterLastSession("kill").Require...)
	require = append(require,
		"None of these errors means that the agent is dead",
		"kill must run as the same user and in the same tmux environment as the agents",
		finishedRowNotVerification, wrongServerSecondAgent,
	)
	return DescCase{
		Name:    "kill manifest description",
		Require: append(require, trigger.Require...),
		MustNot: append([]string{"Terminate the Spawn's tmux session", "never delete"}, trigger.MustNot...),
	}
}
