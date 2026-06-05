package hook

import (
	"context"
	"fmt"
	"io"
	"log"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// HookStore is the narrow store surface the handler needs. Production
// callers pass *store.Store; tests can pass a stub to drive failure
// branches (DB-unreachable, etc.) without scripting SQLite errors.
type HookStore interface {
	ApplyHookTransition(instanceID, newState string, softRefresh bool) error
	SetSessionID(instanceID, sessionID string) error
	UpsertOpenPermissionRequest(instanceID, requestToken, toolName, toolInputJSON string, cap int, writerProcess string) error
	GetPermissionRequest(instanceID, requestToken string) (store.PermissionRow, error)
	DecidePermissionRequest(instanceID, requestToken, decision, reason string, writerProcess string) (bool, error)
}

// outcomeTransitioner is an optional extension of HookStore. *store.Store
// satisfies it; test doubles that don't implement it receive
// store.UpsertNoChange as a conservative fallback for the trail field.
type outcomeTransitioner interface {
	ApplyHookTransitionResult(instanceID, newState string, softRefresh bool) (store.UpsertOutcome, error)
}

// HandleConfig bundles the inputs Handle takes beyond the store and
// stdin reader. Bundling them keeps the cmd/-side wiring tidy and
// lets future relay knobs land here without churning the signature.
type HandleConfig struct {
	Env   func(string) string
	Cfg   config.Relay
	Clock PollClock
}

// Handle is the entry point cmd/ dispatches into. It reads the payload
// from stdin, classifies the event, applies the row UPSERT, and — when
// the event is PermissionRequest AND AGENT_DIRECTOR_RELAY_MODE=on —
// runs the relay flow (INSERT per-request-token + polling loop + envelope
// on stdout per SRD §6.2/§6.3).
//
// State-tracking is fail-open per SRD §3.2: any internal failure logs
// and returns nil. The relay flow has stronger fail-closed semantics
// per SRD §6.4 — every failure path emits a deny envelope before
// returning, so Claude Code never hangs.
//
// Exactly one ad.hook.fired trail event is emitted per invocation
// regardless of exit path (SR-A-2.1). Fields are populated incrementally
// as the function progresses; fields not reached before an early exit are
// emitted as null.
//
// Stdout is reserved for the decision envelope; state-tracking events
// (everything except an on-relay PermissionRequest) leave it empty.
func Handle(ctx context.Context, stdin io.Reader, stdout io.Writer, st HookStore, hc HandleConfig, logger *log.Logger) error {
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

	// Apply the hook transition. Use the outcome-aware variant when
	// available so the trail captures the exact result. Test doubles
	// that don't implement outcomeTransitioner fall back to
	// store.UpsertNoChange as a conservative sentinel.
	var upsertOutcome store.UpsertOutcome
	if ot, ok := st.(outcomeTransitioner); ok {
		upsertOutcome, err = ot.ApplyHookTransitionResult(instanceID, res.NewState, res.SoftRefresh)
	} else {
		err = st.ApplyHookTransition(instanceID, res.NewState, res.SoftRefresh)
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

	if res.SessionID != "" {
		if err := st.SetSessionID(instanceID, res.SessionID); err != nil {
			failClosed(fmt.Sprintf("set session id (instance=%s): %v", instanceID, err))
			return nil
		}
	}

	// Relay branch. Only PermissionRequest events with explicit
	// relay-on env take the polling path; everything else falls
	// through to the standard state-tracking exit (no stdout).
	if relayActive && res.EventName == EventNamePermissionRequest {
		clock := hc.Clock
		if clock == nil {
			clock = DefaultPollClock()
		}
		runRelay(ctx, stdout, st, hc.Cfg, clock, logger, instanceID, raw, fields)
	}

	return nil
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
