package hook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/busysend"
	"github.com/gabemahoney/agent-director/internal/store"
)

// Claude's busy-send route A (SR-3.4, SR-3.5): a covered Claude Launch's Held
// Messages reach the agent only as the output of its owned FileChanged hook
// with asyncRewake, which Claude Code turns into a queued wake prompt. That
// prompt is the hook's text inside Claude Code's measured wrapping, recorded
// once in the capability table (claudeWaitTurn's Wrapping): a fixed prefix
// naming the hook, the text with two closing tags escaped, and a fixed
// suffix. AD's own UserPromptSubmit hook sees the wrapped prompt; undoing the
// wrapping exactly gives the take evidence SR-3.3 needs
// (recordClaudeSighting). No marker, nonce or id is ever added to the text.

// claudeWaitTurn is Claude's Wait Turn row of the capability table, whose
// limits and wrapping this route reads and keeps none of its own.
func claudeWaitTurn() store.BusySendModeLimits {
	limits, _ := store.BusySendCapabilitiesFor(store.AgentClaude).Mode(store.BusySendWaitTurn)
	return limits
}

// unwrapClaudeWake undoes the table's wrapping of a UserPromptSubmit prompt
// exactly, returning the body AD's hook wrote, or false when the prompt is not
// a wake in that wrapping or its escaping cannot be undone exactly: a body
// holding an escaped form (literal or escaped, the two are
// indistinguishable) or a raw escaped text (which the escaping never leaves)
// gives nothing. Nothing is trimmed or normalized. A changed wrapping matches
// nothing, so it gives no evidence (SR-3.5).
func unwrapClaudeWake(prompt string) ([]byte, bool) {
	w := claudeWaitTurn().Wrapping
	if w.Prefix == "" || len(prompt) <= len(w.Prefix)+len(w.Suffix) ||
		!strings.HasPrefix(prompt, w.Prefix) || !strings.HasSuffix(prompt, w.Suffix) {
		return nil, false
	}
	body := prompt[len(w.Prefix) : len(prompt)-len(w.Suffix)]
	for _, e := range w.Escapes {
		if strings.Contains(body, e.Escaped) || strings.Contains(body, e.Raw) {
			return nil, false
		}
	}
	return []byte(body), true
}

// EventNameFileChanged is Claude Code's FileChanged hook, busy send's owned
// wake (SR-3.4): registered with asyncRewake for a covered Claude Launch only.
const EventNameFileChanged = "FileChanged"

// ErrRewake is Handle's result when an owned FileChanged hook wrote one whole
// wake prompt to its error stream: the verb exits RewakeExitCode, which is
// what makes Claude Code take that stream as the agent's next input. Any other
// FileChanged result exits 0, so a partial or failed write is never taken.
var ErrRewake = errors.New("hook: wake prompt written")

// RewakeExitCode is the exit status of a hook that wrote a wake (ErrRewake).
const RewakeExitCode = 2

// claudeWakeBudgets are one wake's finite phases (SR-3.2): the issue phase
// bounds eligibility, the claim and the issue commit; the job bounds the
// stderr write; cleanup records what it observed. Each is far inside Claude
// Code's command-hook timeout.
var claudeWakeBudgets = busysend.Budgets{Issue: 30 * time.Second, Job: time.Minute, Cleanup: 30 * time.Second}

// claudeRouteStore is the store surface the Claude route drives: the
// coordinator's guarded steps and the route's own facts. *store.Store has it;
// a store without it delivers nothing.
type claudeRouteStore interface {
	busysend.Store
	ReadBusySendRouteFacts(ctx context.Context, instanceID string, gate store.HookGate) (store.BusySendRouteFacts, error)
}

// routeFactsReader is the route facts read alone, for the Stop and idle
// re-signal and SessionStart's watch path.
type routeFactsReader interface {
	ReadBusySendRouteFacts(ctx context.Context, instanceID string, gate store.HookGate) (store.BusySendRouteFacts, error)
}

// claudeRouteFacts is the Claude route's delivery authority (SR-3.4): it reads
// the route facts of a main-agent Claude hook and reports whether, for the
// hook's own gate and session, its Launch is a covered Claude Launch with
// route A and a wake endpoint. A callback from another, a missing or a
// changed session has none (claudeCallbackSession), whatever lifecycle
// tracking records from it. Such ordinary refusals are silent; a facts read
// that fails for another reason than a missing row is logged to the hook's
// diagnostic sink, and also authorizes nothing.
func claudeRouteFacts(ctx context.Context, st HookStore, instanceID string, gate store.HookGate, res ClassifyResult, logger *log.Logger) (store.BusySendRouteFacts, bool) {
	r, ok := st.(routeFactsReader)
	if !ok || res.Agent != store.AgentClaude || res.AgentID != "" {
		return store.BusySendRouteFacts{}, false
	}
	f, err := r.ReadBusySendRouteFacts(ctx, instanceID, gate)
	if err != nil && !errors.Is(err, store.ErrSpawnNotFound) {
		logf(logger, "hook: busy-send route facts (instance=%s, event=%s): %v", instanceID, res.EventName, err)
	}
	if err != nil || !f.GateHolds || !f.Covered || f.Ended || f.WakePath == "" ||
		f.Destination.RouteKind != store.BusySendRouteClaudeHooks || !claudeCallbackSession(res, f.Destination.SessionID) {
		return store.BusySendRouteFacts{}, false
	}
	return f, true
}

// claudeCallbackSession reports whether a Claude callback is from session, the
// current Launch's recorded session: the session its transcript path names
// (the one lifecycle tracking records) is nonempty and session, and a
// session_id the payload also carries is the same.
func claudeCallbackSession(res ClassifyResult, session string) bool {
	return res.SessionID != "" && res.SessionID == session && (res.PayloadSessionID == "" || res.PayloadSessionID == res.SessionID)
}

// writeClaudeWatchPath is a covered Claude Launch's SessionStart output
// (SR-3.4): it creates the Launch's recorded wake endpoint when missing
// (prepare, HandleConfig.PrepareBusySendWake, in the Launch's private launch
// directory) and returns it in watchPaths, so Claude Code runs the owned
// FileChanged hook when it changes. Any other SessionStart, a nil prepare, or
// an endpoint that cannot be created where the Launch's record names it,
// writes nothing. A send's touch before the watch is registered is recovered
// by one finite detached startup touch after this hook exits (decision 19).
// With a startup prompt its first Stop still supplies the touch instead. It
// carries no permission decision.
func writeClaudeWatchPath(ctx context.Context, stdout io.Writer, st HookStore, hc HandleConfig,
	instanceID string, gate store.HookGate, res ClassifyResult, logger *log.Logger) {
	f, ok := claudeRouteFacts(ctx, st, instanceID, gate, res, logger)
	if !ok {
		return
	}
	id, ok := st.(interface{ StoreID() string })
	if !ok || hc.PrepareBusySendWake == nil {
		return
	}
	path, err := hc.PrepareBusySendWake(id.StoreID(), f.Destination.LaunchToken)
	if err != nil || path != f.WakePath {
		logf(logger, "hook: busy-send wake endpoint (instance=%s): %v", instanceID, err)
		return
	}
	out, err := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "SessionStart", "watchPaths": []string{f.WakePath}}})
	if err == nil {
		_, err = fmt.Fprintln(stdout, string(out))
	}
	if err != nil {
		logf(logger, "hook: busy-send watch path (instance=%s): %v", instanceID, err)
		return
	}
	if hc.ScheduleClaudeStartupWake == nil || hc.ClaudeStartupPrompt == nil ||
		(f.Kind != store.BusySendLaunchResume && f.Kind != store.BusySendLaunchMigrated && hc.ClaudeStartupPrompt(f.AgentArgs)) {
		return
	}
	job := ClaudeStartupWake{InstanceID: instanceID, LaunchToken: f.Destination.LaunchToken, SessionID: res.SessionID,
		ParentPID: gate.ParentPID, ParentStart: gate.ParentStart, Deadline: hc.clock()().Add(claudeStartupWakeBudget)}
	if err := hc.ScheduleClaudeStartupWake(job); err != nil {
		logf(logger, "hook: schedule busy-send startup wake (instance=%s): %v", instanceID, err)
	}
}

// resignalClaudeWake is the main agent's turn end for busy send (decision 10):
// after a Stop's, or the idle-prompt Notification's, own write applied, a
// covered Claude Launch with Messages Held and no earlier issued input still
// awaiting evidence it was taken gets its wake endpoint touched. It claims
// nothing and emits nothing: the owned FileChanged hook decides again from
// SQLite. A lost touch leaves the Messages Held for the next eligible event.
func resignalClaudeWake(ctx context.Context, st HookStore, instanceID string, gate store.HookGate, res ClassifyResult, logger *log.Logger) {
	if res.EventName != EventNameStop && !res.WaitingIfWorking {
		return
	}
	f, ok := claudeRouteFacts(ctx, st, instanceID, gate, res, logger)
	if !ok || f.Held == 0 || f.Awaiting > 0 {
		return
	}
	store.TouchBusySendWake(f.WakePath)
}

// handleClaudeWake is the owned FileChanged hook (SR-3.4; decision 10): a
// change of the Launch's own wake endpoint, from the agent process the gate
// names, makes one finite coordinator invocation at the turn-end boundary
// through claudeRoute, which re-reads SQLite and emits one whole prompt on
// stderr only when eligible Held work and the agent's state authorize it. It
// reports whether that prompt was written whole (the hook then exits 2). A
// change of any other path, a subagent's callback, a stale gate or an
// uncovered Launch delivers nothing, and the hook writes no state: FileChanged
// is not a state report.
func handleClaudeWake(ctx context.Context, stderr io.Writer, st HookStore, hc HandleConfig, instanceID string, gate store.HookGate, res ClassifyResult, logger *log.Logger) bool {
	rs, ok := st.(claudeRouteStore)
	if !ok || stderr == nil || res.FilePath == "" {
		return false
	}
	f, ok := claudeRouteFacts(ctx, st, instanceID, gate, res, logger)
	if !ok || filepath.Clean(res.FilePath) != f.WakePath {
		return false
	}
	route := &claudeRoute{st: rs, instanceID: instanceID, launchID: f.Destination.LaunchID, session: res.SessionID, gate: gate,
		limits: claudeWaitTurn(), startupPrompt: hc.ClaudeStartupPrompt, out: stderr, logger: logger}
	if self := hc.self(); self.PID > 0 && self.Starttime != "" {
		route.issuer = &store.BusySendIssuer{Role: store.BusySendProcessHelper, PID: self.PID, StartTime: self.Starttime,
			PIDNamespace: self.PIDNamespace}
	}
	c := busysend.Coordinator{Store: rs, Route: route, Budgets: claudeWakeBudgets, Now: hc.clock()}
	out, err := c.Deliver(ctx, busysend.Event{InstanceID: instanceID, Boundary: store.BusySendBoundaryTurnEnd})
	if err != nil {
		logf(logger, "hook: busy-send wake (instance=%s): %v", instanceID, err)
	}
	for _, d := range out.Issued {
		if d.Output == store.BusySendOutputComplete {
			return true
		}
	}
	return false
}

// claudeRoute is Claude's route A on the provider-neutral coordinator
// (busysend.Route): an owned FileChanged hook's one invocation, issuing at
// most one prompt (More false) on its own error stream.
type claudeRoute struct {
	st         claudeRouteStore
	instanceID string
	// launchID and session are the Launch and provider session the callback
	// was authorized for (claudeRouteFacts): the destination it may claim and
	// issue for, never a later one.
	launchID, session string
	gate              store.HookGate
	// limits are Claude's Wait Turn row of the capability table.
	limits store.BusySendModeLimits
	// startupPrompt is HandleConfig.ClaudeStartupPrompt: whether agent
	// arguments carry a startup prompt, which goes first in the Launch's
	// order, asked only on a fresh or reused Launch's first turn; nil issues
	// nothing on any Launch.
	startupPrompt func(args []string) bool
	// issuer is this hook process's own identity, nil when unreadable.
	issuer *store.BusySendIssuer
	out    io.Writer
	logger *log.Logger
}

// facts re-reads the route facts; a failed read is logged, and the caller
// keeps custody with readiness_unknown.
func (r *claudeRoute) facts(ctx context.Context) (store.BusySendRouteFacts, bool) {
	f, err := r.st.ReadBusySendRouteFacts(ctx, r.instanceID, r.gate)
	if err != nil {
		logf(r.logger, "hook: busy-send route facts (instance=%s): %v", r.instanceID, err)
		return f, false
	}
	return f, true
}

// destination is f's destination as this callback may use it: in the
// callback's own session, so the claim and issue guards refuse it when the
// current session is another.
func (r *claudeRoute) destination(f store.BusySendRouteFacts) store.BusySendDestination {
	d := f.Destination
	d.SessionID = r.session
	return d
}

// Eligibility reads the route's own boundary (SR-3.3): only the main agent
// at its prompt, with no earlier issued input lacking take evidence, may be
// woken; Claude cannot exclude a person's typing or a sibling Stop hook's
// continuation, so every attempt is race-exposed (SR-3.3a). A failed read of
// the facts claims nothing and waits readiness_unknown.
func (r *claudeRoute) Eligibility(ctx context.Context, _ busysend.Event) (busysend.Eligibility, error) {
	if r.startupPrompt == nil {
		logf(r.logger, "hook: busy-send wake (instance=%s): no startup seam (HandleConfig.ClaudeStartupPrompt); nothing issued", r.instanceID)
	}
	f, ok := r.facts(ctx)
	if !ok {
		return busysend.Eligibility{Wait: store.BusySendWaitReadinessUnknown,
			Destination: store.BusySendDestination{LaunchID: r.launchID}}, nil
	}
	return busysend.Eligibility{Wait: r.wait(f), Destination: r.destination(f), RaceExposed: true}, nil
}

// wait is the cause that forbids a wake now, "" for none.
func (r *claudeRoute) wait(f store.BusySendRouteFacts) store.BusySendWaitReason {
	firstTurn := f.TurnStarts == 0 && f.Kind != store.BusySendLaunchResume && f.Kind != store.BusySendLaunchMigrated
	switch {
	case !f.GateHolds || !f.Covered || f.Destination.RouteKind != store.BusySendRouteClaudeHooks:
		return store.BusySendWaitRouteUnavailable
	case f.Ended || f.State == store.StateEnded || f.State == store.StateMissing:
		return store.BusySendWaitLaunchEnded
	case f.State == store.StatePending:
		return store.BusySendWaitAgentStarting
	case r.startupPrompt == nil:
		// With no startup seam the route delivers nothing at all, whatever
		// the Launch's kind or Turn Start counter.
		return store.BusySendWaitReadinessUnknown
	case firstTurn && r.startupPrompt(f.AgentArgs):
		// The startup prompt goes first in the Launch's order; its own
		// UserPromptSubmit is the only evidence it was taken.
		return store.BusySendWaitAgentStarting
	case f.PermissionHold || f.State == store.StateCheckPermission || f.State == store.StateAskUser:
		return store.BusySendWaitDialog
	case f.State == store.StateWorking:
		return store.BusySendWaitTurnRunning
	case f.Awaiting > 0 || f.State != store.StateWaiting:
		return store.BusySendWaitReadinessUnknown
	}
	return ""
}

// Measure is the complete prompt as the agent receives it: the capability
// table's exact measure of Claude's wrapping and escaping (decision 12).
func (r *claudeRoute) Measure(body []byte) int64 { return r.limits.Measure(body) }

// Authorize looks once more just before the issue commit, which records this
// hook as the Launch's helper in the same transaction: a hook whose own
// identity cannot be read issues nothing, because nothing could later prove
// it gone (M1 G4); neither does one whose output stream cannot take a write
// deadline, because its write could outlive the Job (SR-3.2), nor one whose
// facts cannot be read or whose destination, its session included, is no
// longer the current one. Each keeps custody with readiness_unknown.
func (r *claudeRoute) Authorize(ctx context.Context, p busysend.Prepared) (busysend.Authority, error) {
	if !r.boundedOutput() {
		logf(r.logger, "hook: busy-send wake (instance=%s): the output stream takes no write deadline; nothing issued", r.instanceID)
		return busysend.Authority{Wait: store.BusySendWaitReadinessUnknown}, nil
	}
	f, ok := r.facts(ctx)
	switch {
	case !ok || r.issuer == nil || f.Destination != p.Destination || p.Destination.SessionID != r.session:
		return busysend.Authority{Wait: store.BusySendWaitReadinessUnknown}, nil
	}
	if wait := r.wait(f); wait != "" {
		return busysend.Authority{Wait: wait}, nil
	}
	return busysend.Authority{Issuer: r.issuer}, nil
}

// Issue writes p's whole body to the hook's error stream, the one
// irreversible call, under the coordinator's Job deadline as the stream's
// write deadline (writeBounded), so the write ends by then and no writer
// outlives it: complete only when every byte was written (Claude takes it on
// the hook's exit 2, in completion order: emission_complete at the
// handed_over_not_proven boundary, SR-2.5, SR-3.5); some bytes is partial and
// none uncertain, both unproven and never retried. More is false: one wake
// carries one prompt.
func (r *claudeRoute) Issue(ctx context.Context, p busysend.Prepared) busysend.Disposition {
	n, err := writeBounded(ctx, r.out, p.Body)
	switch {
	case err == nil && n == len(p.Body):
		return busysend.Disposition{Output: store.BusySendOutputComplete, Order: store.BusySendOrderEmissionComplete,
			UnprovenBoundary: true}
	case n > 0:
		return busysend.Disposition{Output: store.BusySendOutputPartial}
	}
	if err != nil {
		logf(r.logger, "hook: busy-send wake output (instance=%s): %v", r.instanceID, err)
	}
	return busysend.Disposition{Output: store.BusySendOutputUncertain}
}

// deadlineWriter is a stream whose writes a deadline can cut short, such as an
// *os.File on a descriptor the runtime polls (cmd/agent-director's hook
// stderr for this event).
type deadlineWriter interface {
	io.Writer
	SetWriteDeadline(time.Time) error
}

// boundedOutput reports whether the route's output stream takes a write
// deadline, checked before any issue commit so a stream that cannot is never
// written to.
func (r *claudeRoute) boundedOutput() bool {
	dw, ok := r.out.(deadlineWriter)
	return ok && dw.SetWriteDeadline(time.Time{}) == nil
}

// writeBounded writes b to w with ctx's deadline as w's write deadline
// (SR-3.2), in the calling goroutine: a write that runs out returns with the
// bytes it wrote, and none is left running. A stream that takes no deadline,
// or a ctx with none, is not written to at all.
func writeBounded(ctx context.Context, w io.Writer, b []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	deadline, bounded := ctx.Deadline()
	dw, ok := w.(deadlineWriter)
	if !ok || !bounded {
		return 0, errors.New("the output stream or the Job phase has no deadline")
	}
	if err := dw.SetWriteDeadline(deadline); err != nil {
		return 0, err
	}
	return dw.Write(b)
}

// busySendSighter is the store's take-evidence write (store.Store
// RecordBusySendSighting). A store without it records no sighting.
type busySendSighter interface {
	RecordBusySendSighting(context.Context, store.BusySendSighting) (store.BusySendSightingResult, error)
}

// claudeTurnGate marks an owned main-agent Claude UserPromptSubmit (no
// agent_id, with the session id of its transcript path) as a Turn Start, which
// its gated write counts (store.HookGate.ClaudeTurnStart; b.7si t1.7si.nc
// SR-1.4). A subagent's, a Codex or Cursor hook, or one with no session id is
// not one.
func claudeTurnGate(gate *store.HookGate, res ClassifyResult) {
	gate.ClaudeTurnStart = res.Agent == store.AgentClaude && res.EventName == "UserPromptSubmit" && res.AgentID == "" &&
		res.SessionID != "" && !res.UnknownEvent
}

// recordClaudeSighting is an owned main-agent Claude UserPromptSubmit's take
// evidence (SR-3.3, SR-3.5), after its own gated write applied (a held write
// counts): a prompt in the measured wake wrapping, undone exactly, is offered
// to the store, which binds it, with the hook's gate, Launch, session and
// connection generation, to the one awaiting prompt with that body or to none.
// A callback whose payload session disagrees with its transcript's offers
// nothing (claudeCallbackSession; the store compares the session with the
// current one). It changes no hook outcome: it is fail-open, logs a failure
// and writes nothing to stdout.
func recordClaudeSighting(ctx context.Context, st HookStore, instanceID string, gate store.HookGate, res ClassifyResult, logger *log.Logger) {
	if !gate.ClaudeTurnStart || !claudeCallbackSession(res, res.SessionID) {
		return
	}
	body, ok := unwrapClaudeWake(res.Prompt)
	if !ok {
		return
	}
	s, ok := st.(busySendSighter)
	if !ok {
		return
	}
	if _, err := s.RecordBusySendSighting(ctx, store.BusySendSighting{InstanceID: instanceID, Gate: gate,
		SessionID: res.SessionID, Body: body}); err != nil {
		logf(logger, "hook: busy-send sighting (instance=%s): %v", instanceID, err)
	}
}
