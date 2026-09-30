package hook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"

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
// its row fields (SR-14).
type HookStore interface {
	GetSpawn(instanceID string) (store.Spawn, error)
	ApplyHookTransition(instanceID string, gate store.HookGate, newState string, softRefresh bool, triggeringEventName, jsonlPath string, jsonlPresent bool) (store.HookApplied, error)
	RecordSessionStartIdentity(instanceID string, gate store.HookGate, jsonlPath string, jsonlPresent bool) (applied store.HookApplied, snapshotChanged bool, err error)
	UpsertOpenPermissionRequest(instanceID string, gate store.HookGate, requestToken, toolName, toolInputJSON string, cap int, writerProcess string) (store.HookApplied, error)
	GetPermissionRequest(instanceID, requestToken string) (store.PermissionRow, error)
	DecidePermissionRequest(instanceID, requestToken, decision, reason string, writerProcess string) (bool, error)
}

// outcomeTransitioner is an optional extension of HookStore. *store.Store
// satisfies it; test doubles that don't implement it receive
// store.UpsertNoChange as a conservative fallback for the trail field.
type outcomeTransitioner interface {
	ApplyHookTransitionResult(instanceID string, gate store.HookGate, newState string, softRefresh bool, triggeringEventName, jsonlPath string, jsonlPresent bool) (store.UpsertOutcome, store.HookApplied, error)
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
	// ParentProc reads the parent's start time (the gate) and command name
	// (ad.hook.ignored only). cmd/agent-director wires the per-OS probe
	// readers; tests inject a double. A nil ParentProc leaves the start time
	// unreadable, so the hook matches no row.
	ParentProc ParentProc
}

// Handle is the entry point cmd/ dispatches into. It captures the hook's
// parent process, reads the payload from stdin, classifies the event, applies
// the gated row write, and — when the event is PermissionRequest AND
// AGENT_DIRECTOR_RELAY_MODE=on and the write applied — runs the relay flow
// (INSERT per-request-token + polling loop + envelope on stdout per SRD
// §6.2/§6.3).
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
// path makes no tmux call and walks no process ancestry.
//
// SessionStart (SR-22.9, SR-5.3): the row is read for the snapshot the write
// is conditioned on, and one gated RecordSessionStartIdentity records the
// payload's session id and transcript path and the parent as pid and
// proc_starttime, and sets waiting. When only the snapshot changed, the row is
// re-read and the write retried once; a second change logs one line and
// writes no ad.hook.ignored (decision A3). Every other event is one gated
// ApplyHookTransitionResult with the payload's session id, transcript path and
// its presence on disk; the session id is recorded, never a gate.
//
// State-tracking is fail-open per SRD §3.2: any internal failure logs
// and returns nil. The relay flow has stronger fail-closed semantics
// per SRD §6.4 — every failure path emits a deny envelope before
// returning, so Claude Code never hangs. A relayed PermissionRequest that
// the gate did not apply returns no decision (empty stdout), so the
// process's own Claude Code asks as it would with no relay.
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

	gate := store.HookGate{
		Event:       res.EventName,
		ParentPID:   parent.pid,
		ParentStart: parent.start,
		SessionID:   res.SessionID,
	}
	jsonlPresent := transcriptPresent(res.TranscriptPath)

	var applied store.HookApplied
	if res.EventName == "SessionStart" {
		var outcome store.UpsertOutcome
		applied, outcome, err = recordSessionStart(st, instanceID, gate, res.TranscriptPath, jsonlPresent, logger)
		fields["upsert_outcome"] = string(outcome)
		if err != nil {
			failClosed(fmt.Sprintf("record session start identity (instance=%s): %v", instanceID, err))
			return nil
		}
	} else {
		// Use the outcome-aware variant when available so the trail
		// captures the exact result. Test doubles that don't implement
		// outcomeTransitioner fall back to store.UpsertNoChange as a
		// conservative sentinel.
		var upsertOutcome store.UpsertOutcome
		if ot, ok := st.(outcomeTransitioner); ok {
			upsertOutcome, applied, err = ot.ApplyHookTransitionResult(instanceID, gate, res.NewState, res.SoftRefresh, res.EventName, res.TranscriptPath, jsonlPresent)
		} else {
			applied, err = st.ApplyHookTransition(instanceID, gate, res.NewState, res.SoftRefresh, res.EventName, res.TranscriptPath, jsonlPresent)
			if err != nil {
				upsertOutcome = store.UpsertError
			} else {
				upsertOutcome = store.UpsertNoChange
			}
		}
		fields["upsert_outcome"] = string(upsertOutcome)
		if err != nil {
			failClosed(fmt.Sprintf("apply transition (instance=%s, event=%s): %v", instanceID, res.EventName, err))
			return nil
		}
	}

	if !applied.Applied {
		// Not this row's agent (or no row): nothing written, no relay, no
		// decision on stdout, exit 0 (SR-22.9).
		if applied.Reason != "" {
			emitIgnored(ctx, st, hc.ParentProc, ignoredHook{
				instanceID: instanceID,
				event:      res.EventName,
				sessionID:  res.SessionID,
				parent:     parent,
				reason:     applied.Reason,
			})
		}
		return nil
	}

	// Relay branch. Only PermissionRequest events with explicit
	// relay-on env take the polling path; everything else falls
	// through to the standard state-tracking exit (no stdout).
	if relayActive && res.EventName == EventNamePermissionRequest {
		clock := hc.Clock
		if clock == nil {
			clock = DefaultPollClock()
		}
		onIgnored := func(reason string) {
			emitIgnored(ctx, st, hc.ParentProc, ignoredHook{
				instanceID: instanceID,
				event:      res.EventName,
				sessionID:  res.SessionID,
				parent:     parent,
				reason:     reason,
			})
		}
		runRelay(ctx, stdout, st, hc.Cfg, clock, logger, instanceID, gate, raw, fields, onIgnored)
	}

	return nil
}

// recordSessionStart is Handle's SessionStart write (SR-22.9, SR-5.3): read
// the row for the snapshot the write is conditioned on, then one gated
// RecordSessionStartIdentity. When only the snapshot changed (the gate held),
// it re-reads and retries once; a second change logs one line and returns not
// applied with no reason, so no ad.hook.ignored is written (decision A3). No
// row is a silent no-op (decision A2). The outcome is ad.hook.fired's
// upsert_outcome: updated when applied, no_change when not, error on a store
// error (returned).
func recordSessionStart(st HookStore, instanceID string, gate store.HookGate, jsonlPath string, jsonlPresent bool, logger *log.Logger) (store.HookApplied, store.UpsertOutcome, error) {
	gate.SessionStart = true
	for attempt := 0; attempt < 2; attempt++ {
		sp, err := st.GetSpawn(instanceID)
		if errors.Is(err, store.ErrSpawnNotFound) {
			return store.HookApplied{}, store.UpsertNoChange, nil
		}
		if err != nil {
			return store.HookApplied{}, store.UpsertError, err
		}
		gate.Examined = sp.Snapshot
		applied, changed, err := st.RecordSessionStartIdentity(instanceID, gate, jsonlPath, jsonlPresent)
		if err != nil {
			return store.HookApplied{}, store.UpsertError, err
		}
		if applied.Applied {
			return applied, store.UpsertUpdated, nil
		}
		if !changed {
			return applied, store.UpsertNoChange, nil
		}
	}
	logf(logger, "hook: SessionStart not recorded (instance=%s): the row changed twice while it was written", instanceID)
	return store.HookApplied{}, store.UpsertNoChange, nil
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
