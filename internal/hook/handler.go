package hook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// HookStore is the narrow store surface the handler needs. Production
// callers pass *store.Store; tests can pass a stub to drive failure
// branches (DB-unreachable, etc.) without scripting SQLite errors.
//
// Every write takes the hook's store.HookGate and reports store.HookApplied
// (SR-22.9): the gate is a condition of each write's own statement. GetSpawn
// gives SessionStart the snapshot it examines (SR-5.3) and ad.hook.ignored
// its row fields (SR-14). A relayed PermissionRequest also needs the relay
// writes (RelayStore), which *store.Store has.
type HookStore interface {
	GetSpawn(instanceID string) (store.Spawn, error)
	ApplyHookTransition(instanceID string, gate store.HookGate, newState string, softRefresh bool, triggeringEventName, jsonlPath string, jsonlPresent bool) (store.HookApplied, error)
	RecordSessionStartIdentity(instanceID string, gate store.HookGate, jsonlPath string, jsonlPresent bool) (applied store.HookApplied, snapshotChanged bool, err error)
	GetPermissionRequest(instanceID, requestToken string) (store.PermissionRow, error)
}

// outcomeTransitioner is an optional extension of HookStore. *store.Store
// satisfies it; test doubles that don't implement it receive
// store.UpsertNoChange as a conservative fallback for the trail field.
type outcomeTransitioner interface {
	ApplyHookTransitionResult(instanceID string, gate store.HookGate, newState string, softRefresh bool, triggeringEventName, jsonlPath string, jsonlPresent bool) (store.UpsertOutcome, store.HookApplied, error)
}

// waitingIfWorkingTransitioner is an optional extension of HookStore for the
// main agent's idle-prompt Notification (ClassifyResult.WaitingIfWorking,
// b.svb). *store.Store satisfies it; a test double that does not gets the
// soft refresh the classification also carries.
type waitingIfWorkingTransitioner interface {
	ApplyHookWaitingIfWorking(instanceID string, gate store.HookGate, triggeringEventName, jsonlPath string, jsonlPresent bool) (store.UpsertOutcome, store.HookApplied, error)
}

// HandleConfig bundles the inputs Handle takes beyond the store and
// stdin reader. Bundling them keeps the cmd/-side wiring tidy and
// lets future relay knobs land here without churning the signature.
type HandleConfig struct {
	Env   func(string) string
	Cfg   config.Relay
	Clock PollClock
	// ParentPID returns the hook's parent pid; with exec-form hooks it is
	// the agent process (SR-22.9). cmd/agent-director wires os.Getppid; tests
	// inject a fixed pid. A nil ParentPID reads as pid 0, which matches no
	// row.
	ParentPID func() int
	// ParentProc reads the parent's start time (the gate), command name
	// (ad.hook.ignored and ad.hook.pane_is_grandparent only) and parent pid
	// (ad.hook.pane_is_grandparent's check only, b.9n6, b.zde).
	// cmd/agent-director wires the per-OS probe readers; tests inject a
	// double. A nil ParentProc leaves the start time unreadable, so the hook
	// matches no row.
	ParentProc ParentProc
	// Now reads the injected clock that bounds SessionStart's wait for its
	// launch's identity write (SR-22.9, SR-20.2): the grace bound is compared
	// with it on the wall clock, and the wait's elapsed time, which
	// sessionStartWaitCap bounds, is the difference between two of its
	// readings. cmd/agent-director wires time.Now, whose readings carry Go's
	// monotonic clock reading, so that difference is monotonic and no
	// wall-clock step moves it; production wiring must keep that reading and
	// never pass it through .UTC, .Local, .Round(0), .Truncate or .In, which
	// strip it.
	// Tests inject a virtual clock that Clock.Sleep advances; the difference
	// between two of its readings is the virtual elapsed time. A nil Now means
	// SessionStart never waits.
	//
	// The relay hook's timing (b.146 rule 4) reads it too: its kill instant
	// (Start plus RelayTimeout), its first write's and its ack's cut, and its
	// poll's deadline. A nil Now gives the relay hook time.Now.
	Now func() time.Time
	// Start is the instant the hook process started, on Now's clock:
	// cmd/agent-director reads time.Now first thing in the hook verb. The
	// relay hook's kill instant is Start plus RelayTimeout (b.146 rule 4).
	// The zero time reads Now at Handle's entry.
	Start time.Time
	// RelayTimeout is the per-hook timeout spawn wrote for the relay hook in
	// the agent's settings, its --timeout argument (b.146 rule 4): Claude Code
	// kills the hook at Start plus it. Zero or negative (a hook entry spawned
	// before this argument existed, or an unparseable one) reads Cfg's
	// effective relay window, the value spawn writes there.
	RelayTimeout time.Duration
	// Self reads the hook process's own identity at its start, which the
	// relay hook records with its request (b.146 rules 2 and 14): its pid,
	// its start time (/proc/self/stat field 22) and its pid namespace (the
	// target of /proc/self/ns/pid). It returns the zero identity when it
	// cannot read the start time or the namespace. cmd/agent-director wires
	// the per-OS readers; tests inject a double. A nil Self records no
	// identity: readers then cannot tell whether the hook is gone and fall
	// back to its settle instant.
	Self func() store.ProcessIdentity
	// PendingGrace is the effective pending grace period
	// (config.Tmux.EffectivePendingGrace, SR-13.4): SessionStart waits for its
	// launch's identity write until the row's launch start plus PendingGrace,
	// or until sessionStartWaitCap (540 s) after it began waiting, whichever
	// comes first (WD 2026-09-30c). cmd/agent-director wires the loaded
	// config's value, which has no maximum. Zero or negative means
	// SessionStart never waits.
	PendingGrace time.Duration
}

// clock is hc.Now, or time.Now when it is nil (the relay hook's clock).
func (hc HandleConfig) clock() func() time.Time {
	if hc.Now == nil {
		return time.Now
	}
	return hc.Now
}

// self is hc.Self's identity, or the zero identity when it is nil.
func (hc HandleConfig) self() store.ProcessIdentity {
	if hc.Self == nil {
		return store.ProcessIdentity{}
	}
	return hc.Self()
}

// Handle is the entry point cmd/ dispatches into. It captures the hook's
// parent process, reads the payload from stdin, classifies the event and
// applies the gated row write. When the event is PermissionRequest AND
// AGENT_DIRECTOR_RELAY_MODE=on it runs the relay flow instead (runRelay, SRD
// §6.2/§6.3; b.146 step 2): one gated transaction records the request and the
// move to check_permission together, then the polling loop, then the acked
// answer on stdout.
//
// The gate (SR-22.9): the hook's parent pid (HandleConfig.ParentPID) and that
// pid's start time (HandleConfig.ParentProc) are captured once, at entry,
// before any store call, and every store write for this hook carries them as
// its store.HookGate. A hook applies only when they are the row's recorded
// pane process. One that does not apply changes nothing: no state, no
// last_seen_at, no liveness-note clear, no permission request, no relay and
// no decision on stdout; it exits 0 and writes exactly one ad.hook.ignored
// (SR-14; emitIgnored) with the store's reason. A hook for an id with no row
// is today's silent no-op, with no ad.hook.ignored (decision A2). The hook
// path makes no tmux call and walks no process ancestry: past the parent it
// reads only, for a SessionStart refused with pid_mismatch while the row is
// still pending, the parent's own parent pid, once, after the gate has
// decided. When that is the row's pane process (a `claude` launcher that does
// not exec, or a hook run through a shell instead of in exec form; the two
// look alike), an ad.hook.pane_is_grandparent says so, with that process's
// command name (b.9n6, b.zde); the decision, the row and the exit stay those
// of any ignored hook.
//
// Subagents and in-process teammates (SR-22.9; WD 2026-09-30b) run inside the
// agent's process, so their hooks pass the gate; the payload marks them with a
// non-empty agent_id. Their SessionStart and SessionEnd are decided from the
// payload before the gate and any write: nothing is written, stdout stays
// empty, and one ad.hook.ignored with reason subagent_event is written (none
// when no row has the id), with ad.hook.fired's upsert_outcome no_change.
// Their other hooks apply as any hook does but record no session id and no
// transcript path. agent_type alone does not mark a subagent.
//
// SessionStart (SR-22.9, SR-5.3): the row is read for the snapshot the write
// is conditioned on, and one gated RecordSessionStartIdentity records the
// payload's session id and transcript path and the parent as pid and
// proc_starttime, and sets waiting. When only the snapshot changed, the row is
// re-read and the write retried once; a second change logs one line and
// writes no ad.hook.ignored (decision A3). A SessionStart that arrives before
// its launch's identity write (the write does not apply because the row
// records no pane) logs nothing yet: while the row is pending with no pane
// and inside its pending grace period (HandleConfig.PendingGrace, measured
// from the launch start), for at most sessionStartWaitCap (540 s) after it
// began waiting, it re-reads the row every 250 ms on the injected clock, then
// makes the ordinary gated write with the new snapshot; only the result of
// that write is logged, so no_pane_recorded is written for SessionStart only
// after its bounded wait (recordSessionStart). Only
// SessionStart waits, a subagent's never does, and the wait makes no tmux
// call. Every other event but a relayed PermissionRequest (whose write is
// runRelay's) is one gated ApplyHookTransitionResult with the payload's
// session id, transcript path and its presence on disk; the session id is
// recorded, never a gate. The main agent's idle-prompt Notification is
// ApplyHookWaitingIfWorking instead, with the same gate and records: a row
// working, or relayed and in check_permission with no permission request that
// still awaits an answer, returns to waiting, any other row is soft
// refreshed, and
// either way the row records idle_since (applyOrdinaryHook; b.svb, b.146
// problem 3).
//
// State-tracking is fail-open per SRD §3.2: any internal failure logs
// and returns nil. The relay flow is fail-closed per SRD §6.4 and b.146
// rule 3: a failure before its request is recorded (its first write
// included) emits a deny envelope, so no dialog appears; once the request is
// recorded, the hook writes only an answer it has acked, and every failure
// ends with an empty stdout, so Claude Code's own dialog appears and the
// request falls back. A relayed PermissionRequest that the gate did not apply
// returns no decision (empty stdout), so the process's own Claude Code asks
// as it would with no relay.
//
// Exactly one ad.hook.fired trail event is emitted per invocation
// regardless of exit path (SR-A-2.1); an ignored hook's upsert_outcome is
// no_change (decision A11). Fields are populated incrementally as the
// function progresses; fields not reached before an early exit are
// emitted as null.
//
// Stdout is reserved for the decision envelope; state-tracking events
// (everything except an on-relay PermissionRequest) leave it empty.
func Handle(ctx context.Context, stdin io.Reader, stdout io.Writer, st HookStore, hc HandleConfig, logger *log.Logger) error {
	// The hook's start, from which the relay hook's kill instant is counted
	// (b.146 rule 4).
	start := hc.Start
	if start.IsZero() {
		start = hc.clock()()
	}
	// The gate's identity, captured before any store call (SR-22.9).
	parent := captureParent(hc)

	env := hc.Env
	if env == nil {
		env = func(string) string { return "" }
	}

	relayActive := env(EnvRelayMode) == RelayModeOn

	// Trail fields for ad.hook.fired — populated incrementally as the
	// function progresses. The defer fires exactly once at function exit
	// regardless of exit path (SR-A-2.1). Fields not reached before an
	// early exit are emitted as their zero value (nil → JSON null).
	fields := map[string]any{
		"source":             "ad_hook",
		"relay_mode":         env(EnvRelayMode),
		"matcher":            []string{"*"},
		"claude_instance_id": nil,
		"event_name":         nil,
		"tool_name":          nil,
		"session_id":         "",
		"upsert_outcome":     nil,
		"request_token":      nil,
	}
	defer func() {
		_ = trail.Emit(ctx, "ad.hook.fired", fields)
	}()

	// Read the payload BEFORE resolving instance id so we can peek the
	// event name from the raw bytes (b.45p). A payload-read failure is
	// the one pre-classify error that genuinely cannot know the event
	// type, so it falls back to silent exit (fail-open) — Claude Code
	// routes hook stdout by file descriptor back to the in-flight tool,
	// so a permission-shaped deny envelope emitted from a PreToolUse
	// process would race and beat the legitimate PermissionRequest
	// process's allow.
	raw, err := ReadPayload(stdin)
	if err != nil {
		logf(logger, "hook: read payload: %v", err)
		return nil
	}

	// Peek the event name from the raw payload so failClosed can gate
	// its envelope write on event type from the first post-read failure
	// point. An empty event name (parse failure or missing field) is
	// treated as non-PermissionRequest, which silences the envelope.
	eventName := PeekEventName(raw)

	// failClosed emits a deny envelope ONLY when this invocation is a
	// PermissionRequest AND relay mode is on (SRD §6.4). Non-permission
	// events (state-tracking) stay fail-open per SRD §3.2: log and
	// exit 0 with empty stdout. This gate is the b.45p fix — Claude
	// Code routes hook output by fd, not by envelope contents, so an
	// envelope from a PreToolUse process would be applied to the
	// in-flight tool regardless of its hookEventName field.
	failClosed := func(why string) {
		logf(logger, "hook: %s", why)
		if relayActive && eventName == EventNamePermissionRequest {
			_, _ = fmt.Fprintln(stdout, EncodeDecision(eventName, "deny", ""))
		}
	}

	instanceID, err := ResolveInstanceID(env)
	if err != nil {
		failClosed(fmt.Sprintf("resolve instance id: %v", err))
		return nil
	}
	fields["claude_instance_id"] = instanceID

	res, err := ClassifyEvent(raw)
	if err != nil {
		failClosed(fmt.Sprintf("classify (instance=%s): %v", instanceID, err))
		return nil
	}
	if res.EventName != "" {
		fields["event_name"] = res.EventName
	}
	if res.ToolName != "" {
		fields["tool_name"] = res.ToolName
	}
	if res.SessionID != "" {
		fields["session_id"] = res.SessionID
	}

	if res.UnknownEvent {
		logf(logger, "hook: unknown event %q (instance=%s) — treating as soft refresh", res.EventName, instanceID)
	}

	if res.SubagentLifecycle() {
		// A subagent's or in-process teammate's SessionStart or SessionEnd
		// (SR-22.9): decided from the payload, before the gate and any write.
		fields["upsert_outcome"] = string(store.UpsertNoChange)
		emitIgnored(ctx, st, hc.ParentProc, ignoredHook{
			instanceID:       instanceID,
			event:            res.EventName,
			sessionID:        res.SessionID,
			parent:           parent,
			reason:           store.HookReasonSubagentEvent,
			silentWithoutRow: true,
		})
		return nil
	}

	// An ordinary hook carrying agent_id applies its transition but records
	// no session id and no transcript path (SR-22.9).
	sessionID, transcriptPath := res.SessionID, res.TranscriptPath
	if res.AgentID != "" {
		sessionID, transcriptPath = "", ""
	}
	gate := store.HookGate{
		Event:       res.EventName,
		ParentPID:   parent.pid,
		ParentStart: parent.start,
		SessionID:   sessionID,
	}
	jsonlPresent := transcriptPresent(transcriptPath)
	onIgnored := func(reason string) {
		emitIgnored(ctx, st, hc.ParentProc, ignoredHook{
			instanceID: instanceID,
			event:      res.EventName,
			sessionID:  res.SessionID,
			parent:     parent,
			reason:     reason,
		})
	}

	var applied store.HookApplied
	switch {
	case res.EventName == "SessionStart":
		var outcome store.UpsertOutcome
		applied, outcome, err = recordSessionStart(ctx, st, hc, instanceID, gate, transcriptPath, jsonlPresent, logger)
		fields["upsert_outcome"] = string(outcome)
		if err != nil {
			failClosed(fmt.Sprintf("record session start identity (instance=%s): %v", instanceID, err))
			return nil
		}
	case relayActive && res.EventName == EventNamePermissionRequest:
		// The relay branch (b.146 rule 1): its first write records the
		// request and the move to check_permission in one gated
		// transaction, so no separate state write comes first. It writes
		// the hook's one ad.hook.ignored itself when the gate does not hold.
		runRelay(ctx, stdout, st, hc, newRelayClock(hc, start), logger, relayInput{
			instanceID:        instanceID,
			gate:              gate,
			parent:            parent,
			agentID:           res.AgentID,
			transcriptPath:    transcriptPath,
			transcriptPresent: jsonlPresent,
			raw:               raw,
		}, fields, onIgnored)
		return nil
	default:
		var upsertOutcome store.UpsertOutcome
		upsertOutcome, applied, err = applyOrdinaryHook(st, instanceID, gate, res, transcriptPath, jsonlPresent)
		fields["upsert_outcome"] = string(upsertOutcome)
		if err != nil {
			failClosed(fmt.Sprintf("apply transition (instance=%s, event=%s): %v", instanceID, res.EventName, err))
			return nil
		}
	}

	if !applied.Applied && applied.Reason != "" {
		// Not this row's agent: nothing written, no decision on stdout, exit
		// 0 (SR-22.9). No row: the same, with no ad.hook.ignored.
		onIgnored(applied.Reason)
	}
	return nil
}

// applyOrdinaryHook is Handle's gated write of every event but SessionStart
// and a relayed PermissionRequest, with ad.hook.fired's upsert_outcome. The
// main agent's idle-prompt Notification (res.WaitingIfWorking) is
// ApplyHookWaitingIfWorking: a row working, or relayed and in
// check_permission with no permission request that still awaits an answer,
// returns to waiting, any other row is soft refreshed, and the row records
// idle_since (b.svb, b.146
// problem 3). Every other event is one ApplyHookTransitionResult. A
// store without those (a test double) gets ApplyHookTransition, which for the
// idle-prompt Notification is the soft refresh res also carries, and the
// outcome is store.UpsertNoChange as a conservative sentinel (store.UpsertError
// on an error).
func applyOrdinaryHook(st HookStore, instanceID string, gate store.HookGate, res ClassifyResult, jsonlPath string, jsonlPresent bool) (store.UpsertOutcome, store.HookApplied, error) {
	if res.WaitingIfWorking {
		if wt, ok := st.(waitingIfWorkingTransitioner); ok {
			return wt.ApplyHookWaitingIfWorking(instanceID, gate, res.EventName, jsonlPath, jsonlPresent)
		}
	}
	if ot, ok := st.(outcomeTransitioner); ok {
		return ot.ApplyHookTransitionResult(instanceID, gate, res.NewState, res.SoftRefresh, res.EventName, jsonlPath, jsonlPresent)
	}
	applied, err := st.ApplyHookTransition(instanceID, gate, res.NewState, res.SoftRefresh, res.EventName, jsonlPath, jsonlPresent)
	if err != nil {
		return store.UpsertError, applied, err
	}
	return store.UpsertNoChange, applied, nil
}

// sessionStartWaitInterval is how often SessionStart re-reads its row while it
// waits for its launch's identity write (SR-22.9).
const sessionStartWaitInterval = 250 * time.Millisecond

// sessionStartWaitCap is the longest SessionStart waits for its launch's
// identity write, measured from when it began waiting on HandleConfig.Now
// (monotonic in production), whatever the pending grace period and whatever a
// launch start in the future or a clock step does (SR-22.9, SR-13.4; WD
// 2026-09-30c). It stays below the "timeout" that internal/spawn's
// synthesised settings state on the SessionStart agent-director hook entry
// (sessionStartHookTimeoutSeconds in internal/spawn, 600 s, Claude Code's
// default): cap < timeout. Nothing checks that across the two packages; each
// value is pinned in its own package's tests.
//
// The cap bounds the wait only, not the hook, so it does not guarantee that
// the hook ends itself before Claude Code kills it. Claude Code's timeout
// runs from the hook's start, not from the wait's, and the 60 s between the
// cap and the timeout is all the time left for the hook's store writes
// outside the wait. On a contended store each RecordSessionStartIdentity write
// can wait up to [store] busy_timeout_ms for the write lock, and
// recordSessionStart makes up to four: up to two in the gated write before
// the wait and up to two in the one after it (writeSessionStart's retry on a
// snapshot change). At the default (10 s) that is at most 40 s; from about
// 15 s (4 x 15 s = 60 s) the writes can outlast the headroom, and from 30 s
// the two after the wait alone can. busy_timeout_ms is accepted up to
// math.MaxInt32 ms and is not capped against this headroom (b.c7f, b.146), so
// a long store wait can still get SessionStart killed before it writes its
// result or its no_pane_recorded, with no trail record; so can any other
// death of the hook (OOM, a crash, an overloaded host).
//
// Such a death leaves the row pending although its agent runs. find-missing
// reports it (b.kdf): once the row is past its pending grace period and no
// longer held by a live launch owner, a find-missing run that finds its pane
// process alive writes liveness note unreported and leaves the row pending,
// within pending_grace_seconds plus one find-missing period, provided
// find-missing is scheduled; it marks the row missing when the agent is
// gone. unreported means the agent is alive but no hook has reported since
// its launch: it may sit at a Claude Code startup screen or idle at its
// prompt, and only something that reads the pane can tell which. The caller
// looks (read-pane) and only a caller that looked types (send-keys
// --allow-pending). The agent's next applied hook clears the note: a
// SessionStart that writes later still records the session identity and
// moves the row to waiting, and a UserPromptSubmit moves it to working.
const sessionStartWaitCap = 540 * time.Second

// recordSessionStart is Handle's SessionStart write (SR-22.9, SR-5.3): the
// gated write (writeSessionStart), and, when that did not apply only because
// the row records no pane yet, the bounded wait for the launch's identity
// write (waitForLaunchIdentity) followed by the ordinary gated write again.
//
// The wait runs only when the write reported HookReasonNoPaneRecorded and the
// row it examined is pending, records no pane_pid and is inside its pending
// grace period (store.InsidePendingGrace with hc.PendingGrace and hc.Now: the
// launch start is set and the clock reads before launch start plus the
// grace). A row with no launch start, a row past the grace, a zero grace or a
// nil hc.Now never waits. The wait ends at the earlier of two bounds: the
// grace bound (launch start plus the grace) and sessionStartWaitCap after it
// began. After the wait, the ordinary gated write runs with the snapshot it
// reads then (with its one retry on a snapshot change), and its result is the
// hook's: applied when the pane recorded meanwhile is the hook's parent;
// pid_mismatch for a leftover or stray that waited; no_pane_recorded when the
// row still records no pane at either bound (this last gated write, rather
// than a verdict of the wait, gives the reason, so an identity written
// between the last read and the bound still applies); no reason when the row
// is gone. Handle writes the one ad.hook.ignored from that result, so
// SessionStart's no_pane_recorded is written only after its bounded wait.
//
// The outcome is ad.hook.fired's upsert_outcome: updated when applied,
// no_change when not, error on a store error (returned).
func recordSessionStart(ctx context.Context, st HookStore, hc HandleConfig, instanceID string, gate store.HookGate, jsonlPath string, jsonlPresent bool, logger *log.Logger) (store.HookApplied, store.UpsertOutcome, error) {
	gate.SessionStart = true
	applied, outcome, examined, err := writeSessionStart(st, instanceID, gate, jsonlPath, jsonlPresent, logger)
	if err != nil || applied.Applied || applied.Reason != store.HookReasonNoPaneRecorded {
		return applied, outcome, err
	}
	if !waitForLaunchIdentity(ctx, st, hc, instanceID, examined, logger) {
		return applied, outcome, nil
	}
	applied, outcome, _, err = writeSessionStart(st, instanceID, gate, jsonlPath, jsonlPresent, logger)
	return applied, outcome, err
}

// writeSessionStart is one gated SessionStart write (SR-22.9, SR-5.3): read
// the row for the snapshot the write is conditioned on, then one gated
// RecordSessionStartIdentity. When only the snapshot changed (the gate held),
// it re-reads and retries once; a second change logs one line and returns not
// applied with no reason, so no ad.hook.ignored is written (decision A3). No
// row is a silent no-op (decision A2). It also returns the row it last
// examined (zero when none), which decides whether SessionStart waits.
func writeSessionStart(st HookStore, instanceID string, gate store.HookGate, jsonlPath string, jsonlPresent bool, logger *log.Logger) (store.HookApplied, store.UpsertOutcome, store.Spawn, error) {
	for attempt := 0; attempt < 2; attempt++ {
		sp, err := st.GetSpawn(instanceID)
		if errors.Is(err, store.ErrSpawnNotFound) {
			return store.HookApplied{}, store.UpsertNoChange, store.Spawn{}, nil
		}
		if err != nil {
			return store.HookApplied{}, store.UpsertError, store.Spawn{}, err
		}
		gate.Examined = sp.Snapshot
		applied, changed, err := st.RecordSessionStartIdentity(instanceID, gate, jsonlPath, jsonlPresent)
		if err != nil {
			return store.HookApplied{}, store.UpsertError, sp, err
		}
		if applied.Applied {
			return applied, store.UpsertUpdated, sp, nil
		}
		if !changed {
			return applied, store.UpsertNoChange, sp, nil
		}
	}
	logf(logger, "hook: SessionStart not recorded (instance=%s): the row changed twice while it was written", instanceID)
	return store.HookApplied{}, store.UpsertNoChange, store.Spawn{}, nil
}

// waitingForIdentity reports whether sp is a row a SessionStart waits on
// (SR-22.9): pending, no pane_pid recorded, and inside its pending grace
// period at now (store.InsidePendingGrace: a launch start is set and now is
// before launch start plus grace).
func waitingForIdentity(sp store.Spawn, grace time.Duration, now time.Time) bool {
	return sp.Identity.PanePID == 0 && store.InsidePendingGrace(sp.State, sp.LaunchStartedAtMillis, grace, now)
}

// waitForLaunchIdentity is SessionStart's bounded wait for its launch's
// identity write (SR-22.9; SR-13.4): it reports false, having waited not at
// all, unless examined is a row a SessionStart waits on (waitingForIdentity)
// with hc.Now wired and hc.PendingGrace positive. Otherwise it sleeps on
// hc.Clock (the production sleeper when nil) and re-reads the row through
// GetSpawn every sessionStartWaitInterval, a read only that never blocks the
// identity write, and reports true once the wait has ended: when a pane is
// recorded, the row leaves pending or changes version, the row is gone, the
// row is no longer inside its grace period (the grace bound: launch start
// plus hc.PendingGrace on hc.Now), sessionStartWaitCap has passed since the
// wait began (the cap: elapsed is hc.Now at each turn minus hc.Now at the
// start, monotonic in production, virtual in tests), ctx is cancelled, or a
// read fails (fail-open: logged, and the caller's gated write decides). The
// grace bound and the cap form one budget, min(time left to the grace bound
// at the start, sessionStartWaitCap), and each sleep is clipped so that none
// runs past either bound. As the guard against a clock that does not advance
// (or steps back during the wait), the number of re-reads is capped at what
// that budget allows: ⌊budget / sessionStartWaitInterval⌋ + 1. It writes
// nothing and makes no tmux call.
func waitForLaunchIdentity(ctx context.Context, st HookStore, hc HandleConfig, instanceID string, examined store.Spawn, logger *log.Logger) bool {
	grace := hc.PendingGrace
	if hc.Now == nil || grace <= 0 {
		return false
	}
	start := hc.Now() // the wait begins: the cap's elapsed time is measured from here
	if !waitingForIdentity(examined, grace, start) {
		return false
	}
	clock := hc.Clock
	if clock == nil {
		clock = DefaultPollClock()
	}
	bound := time.UnixMilli(examined.LaunchStartedAtMillis).Add(grace) // wall clock
	capEnd := start.Add(sessionStartWaitCap)                           // keeps start's monotonic reading
	budget := min(bound.Sub(start), sessionStartWaitCap)
	maxReads := int(budget/sessionStartWaitInterval) + 1
	for reads := 0; reads < maxReads; reads++ {
		now := hc.Now()
		// Time.Sub saturates, so no clock step can overflow either difference.
		sleep := min(bound.Sub(now), capEnd.Sub(now), sessionStartWaitInterval)
		if sleep <= 0 {
			break
		}
		clock.Sleep(ctx, sleep)
		if ctx.Err() != nil {
			break
		}
		sp, err := st.GetSpawn(instanceID)
		if errors.Is(err, store.ErrSpawnNotFound) {
			break
		}
		if err != nil {
			logf(logger, "hook: SessionStart wait (instance=%s): read row: %v", instanceID, err)
			break
		}
		if sp.State != examined.State || sp.Snapshot.RowVersion != examined.Snapshot.RowVersion ||
			!waitingForIdentity(sp, grace, hc.Now()) {
			break
		}
	}
	return true
}

// transcriptPresent reports whether the hook-reported transcript path exists
// on disk (b.v2c AC1): a fresh Claude session writes no .jsonl transcript
// until its first user turn, so the path a payload reports may not exist yet.
// The store SETs jsonl_path only when the file is present and NULLs it
// otherwise, so the row never asserts a dead pointer; find-missing heals the
// row once the file appears (AC3). A stat error other than not-exist (e.g. a
// permission wall) is treated as "not present" — the same conservative
// posture the resume fallback takes. An empty path is not present.
func transcriptPresent(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// logf logs to the supplied logger, falling back to a no-op when nil so
// production hook fires with a stripped-down env (no error_log_path) don't
// crash on the logging path.
func logf(logger *log.Logger, format string, args ...any) {
	if logger == nil {
		return
	}
	logger.Printf(format, args...)
}

// Compile-time assertions that *store.Store satisfies HookStore and the
// optional outcomeTransitioner interface. Keeps the production wiring
// honest if the store or interfaces grow.
var _ HookStore = (*store.Store)(nil)
var _ outcomeTransitioner = (*store.Store)(nil)
var _ waitingIfWorkingTransitioner = (*store.Store)(nil)
