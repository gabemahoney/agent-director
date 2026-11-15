package api

import (
	"context"
	"fmt"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// The shared exit-evidence judgement (SR-6.2, SR-6.3; M1's Exit under G4;
// sf-answer-2): resume, reuse, covered administrator deletion, kill's and
// keyless pause's verified exits, and E6's pause-claim closure all decide
// old-issuer exclusion here, from identity-checked process facts and captured
// route facts only. It needs no tmux, so an unusable recorded session name
// does not stop it. The store re-reads the scope under its write lock before
// it applies the evidence (store.BusySendExitEvidence).

// The causes an exit judgement gives when exclusion is not established. They
// are the machine-readable cause of the refusing verb's error.
const (
	// exitCauseProcessRunning: an owned process (the agent or an issuer) still
	// runs with its recorded identity.
	exitCauseProcessRunning = "owned_process_running"
	// exitCauseProcessUnverifiable: an owned process cannot be judged (its
	// start time cannot be read, it records no start time and its pid is in
	// use, its pid namespace is another or unknown), or the current Launch
	// records no agent identity at all.
	exitCauseProcessUnverifiable = "owned_process_unverifiable"
	// exitCauseDestinationUnfenced: a hand-over of the Launch is still open on
	// a route whose captured destination this version cannot fence by its
	// processes' exit.
	exitCauseDestinationUnfenced = "destination_unfenced"
	// exitCauseLaunchChanged: the published current Launch is no longer the
	// one the caller acted on (busySendExitForLaunch); nothing was judged.
	exitCauseLaunchChanged = "launch_changed"
)

// busySendExitRefusal is why an exit judgement did not establish exclusion:
// its cause, and the process concerned (zero for a route cause).
type busySendExitRefusal struct {
	cause string
	role  store.BusySendProcessRole
	pid   int
}

// describe is the refusal's English part, naming the process by role and pid.
func (r *busySendExitRefusal) describe() string {
	switch r.cause {
	case exitCauseProcessRunning:
		return fmt.Sprintf("an owned %s process (pid %d) of the agent's Launch still runs", r.role, r.pid)
	case exitCauseDestinationUnfenced:
		return "a hand-over of the agent's Launch is open on a route whose destination cannot be fenced"
	case exitCauseLaunchChanged:
		return "the agent's Launch was replaced after it was acted on"
	case exitCauseProcessUnverifiable:
		if r.pid == 0 {
			return "the agent's Launch records no agent process identity, so its exit cannot be checked"
		}
	}
	return fmt.Sprintf("an owned %s process (pid %d) of the agent's Launch cannot be checked", r.role, r.pid)
}

// clears is the condition that clears the refusal, for the advice "retry
// <verb> <clears>" (Main Agent decision 16). The refusing row is usually
// finished, where kill is a no-op and sweep ends no process, so the advice
// names the condition rather than those verbs.
func (r *busySendExitRefusal) clears() string {
	switch r.cause {
	case exitCauseProcessRunning:
		if r.role != store.BusySendProcessAgent {
			return "once that process has exited (an agent-director hook or delivery job ends on its own within its job budget)"
		}
		return "once that process has exited"
	case exitCauseDestinationUnfenced:
		return "once that hand-over has closed"
	case exitCauseProcessUnverifiable:
		if r.pid == 0 {
			return "once its agent's exit can be checked"
		}
	}
	return "once that process can be checked gone"
}

// busySendExitNamespace is the reader of this process's own pid namespace the
// judgement compares a recorded issuer's namespace with (probe's contract).
var busySendExitNamespace = probe.SelfPIDNamespace

// judgeBusySendExit judges scope (store.ReadBusySendExitScope) with the
// start-time reader pc: every owned process of every Launch must be gone by
// its identity (tmux.JudgeProcess: gone, or its pid alive with another start
// time; a recorded pid namespace must be this process's own, known one), the
// current Launch must record an agent identity (see below), and every open
// hand-over must be on Claude's hook route, whose captured destination is
// fenced once the Launch's processes are gone: its prompts are written only
// by the Launch's own hook processes to its own agent, and every job step
// compares the captured Launch, session and connection generation. Anything
// else is a refusal; unknown is never gone. It writes nothing.
//
// A current Launch with no recorded agent identity (its creation failed or it
// never reported in) counts as exited only when it never persisted intent
// (Issued false) and owns no recorded process, agent or helper (Main Agent
// decision 15, sf-answer-3): no AD-owned process of it could emit or take AD
// input, since every issue registers its helper in the transaction that
// persists its intent. The store revalidates the process set and Issued under
// the lifecycle write's lock, so a helper, an identity or an intent recorded
// after the read voids the evidence. Row state, Launch End and ended/missing
// are never evidence here, and any recorded process whose identity cannot be
// checked still refuses. Decision 15 never closes a Launch its caller did not
// act on: a caller that acted on one Launch (kill's or keyless pause's
// verified exit) judges only while that Launch is still the published current
// one (busySendExitForLaunch), so a Launch that became current later, with no
// identity yet, is never judged exited on its behalf.
func judgeBusySendExit(pc ProcChecker, scope store.BusySendExitScope) (store.BusySendExitEvidence, *busySendExitRefusal) {
	ns, nsKnown := busySendExitNamespace()
	for _, l := range scope.Launches {
		for _, route := range l.Routes {
			if route != store.BusySendRouteClaudeHooks {
				return store.BusySendExitEvidence{}, &busySendExitRefusal{cause: exitCauseDestinationUnfenced}
			}
		}
		agent := false
		for _, p := range l.Processes {
			agent = agent || p.Role == store.BusySendProcessAgent
			if p.PIDNamespace != "" && (!nsKnown || p.PIDNamespace != ns) {
				return store.BusySendExitEvidence{}, &busySendExitRefusal{cause: exitCauseProcessUnverifiable, role: p.Role, pid: p.PID}
			}
			switch tmux.JudgeProcess(pc, tmux.ProcIdentity{PID: p.PID, Starttime: p.StartTime}) {
			case tmux.ProcGone:
			case tmux.ProcAlive:
				return store.BusySendExitEvidence{}, &busySendExitRefusal{cause: exitCauseProcessRunning, role: p.Role, pid: p.PID}
			default:
				return store.BusySendExitEvidence{}, &busySendExitRefusal{cause: exitCauseProcessUnverifiable, role: p.Role, pid: p.PID}
			}
		}
		if l.Current && !agent && (l.Issued || len(l.Processes) > 0) {
			return store.BusySendExitEvidence{}, &busySendExitRefusal{cause: exitCauseProcessUnverifiable}
		}
	}
	return store.BusySendExitEvidence{Scope: scope}, nil
}

// busySendExitReader is the store surface a lifecycle verb reads exit scope
// through. *store.Store satisfies it; a narrow test store that lacks it gives
// no evidence, which a covered conversation's write then refuses.
type busySendExitReader interface {
	ReadBusySendExitScope(instanceID string) (store.BusySendExitScope, error)
}

// busySendExitFor reads and judges instanceID's exit scope through s, for a
// lifecycle write guarded on the row it examined (resume, reuse, delete),
// whose snapshot guard and the store's scope revalidation bind the evidence
// to the Launches judged. An uncovered published conversation needs no
// evidence (nil, nil, nil). A store that cannot read the scope gives none. A
// covered one gives its evidence, or the refusal.
func busySendExitFor(s any, pc ProcChecker, instanceID string) (*store.BusySendExitEvidence, *busySendExitRefusal, error) {
	reader, ok := s.(busySendExitReader)
	if !ok {
		return nil, nil, nil
	}
	scope, err := reader.ReadBusySendExitScope(instanceID)
	if err != nil {
		return nil, nil, err
	}
	return judgeCoveredExit(pc, scope)
}

// busySendLaunchExitReader reads an exit scope bound to the Launch its caller
// acted on, within the caller's context and wait
// (store.ReadBusySendExitScopeOfWithin). *store.Store satisfies it.
type busySendLaunchExitReader interface {
	ReadBusySendExitScopeOfWithin(ctx context.Context, instanceID, token string, maxWait time.Duration) (store.BusySendExitScope, bool, error)
}

// busySendExitForLaunch is busySendExitFor for a caller that acted on the
// Launch whose launch token is token (kill's and keyless pause's verified
// exit, which record nothing on a Launch they did not act on): when that
// Launch is no longer the published current Launch, it refuses
// launch_changed and judges nothing, whatever the coverage. Otherwise it
// judges as busySendExitFor; the store's revalidation of the same Launches
// at the write keeps the evidence bound to that Launch. A store that cannot
// read a bound scope gives none. The read runs within ctx and maxWait.
func busySendExitForLaunch(ctx context.Context, s any, pc ProcChecker, instanceID, token string, maxWait time.Duration) (*store.BusySendExitEvidence, *busySendExitRefusal, error) {
	reader, ok := s.(busySendLaunchExitReader)
	if !ok {
		return nil, nil, nil
	}
	scope, current, err := reader.ReadBusySendExitScopeOfWithin(ctx, instanceID, token, maxWait)
	if err != nil {
		return nil, nil, err
	}
	if !current {
		return nil, &busySendExitRefusal{cause: exitCauseLaunchChanged}, nil
	}
	return judgeCoveredExit(pc, scope)
}

// judgeCoveredExit is judgeBusySendExit for a covered scope; an uncovered one
// needs no evidence (nil, nil, nil).
func judgeCoveredExit(pc ProcChecker, scope store.BusySendExitScope) (*store.BusySendExitEvidence, *busySendExitRefusal, error) {
	if scope.Coverage != store.BusySendCovered {
		return nil, nil, nil
	}
	ev, refusal := judgeBusySendExit(pc, scope)
	if refusal != nil {
		return nil, refusal, nil
	}
	return &ev, nil, nil
}
