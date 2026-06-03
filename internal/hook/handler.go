package hook

import (
	"context"
	"fmt"
	"io"
	"log"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
)

// HookStore is the narrow store surface the handler needs. Production
// callers pass *store.Store; tests can pass a stub to drive failure
// branches (DB-unreachable, etc.) without scripting SQLite errors.
type HookStore interface {
	ApplyHookTransition(instanceID, newState string, softRefresh bool) error
	SetSessionID(instanceID, sessionID string) error
	UpsertOpenPermissionRequest(instanceID, requestToken, toolName, toolInputJSON string, cap int) error
	GetPermissionRequest(instanceID, requestToken string) (store.PermissionRow, error)
	DecidePermissionRequest(instanceID, requestToken, decision, reason string) (bool, error)
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
// Stdout is reserved for the decision envelope; state-tracking events
// (everything except an on-relay PermissionRequest) leave it empty.
func Handle(ctx context.Context, stdin io.Reader, stdout io.Writer, st HookStore, hc HandleConfig, logger *log.Logger) error {
	env := hc.Env
	if env == nil {
		env = func(string) string { return "" }
	}

	relayActive := env(EnvRelayMode) == RelayModeOn

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

	res, err := ClassifyEvent(raw)
	if err != nil {
		failClosed(fmt.Sprintf("classify (instance=%s): %v", instanceID, err))
		return nil
	}

	if res.UnknownEvent {
		logf(logger, "hook: unknown event %q (instance=%s) — treating as soft refresh", res.EventName, instanceID)
	}

	if err := st.ApplyHookTransition(instanceID, res.NewState, res.SoftRefresh); err != nil {
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
		runRelay(ctx, stdout, st, hc.Cfg, clock, logger, instanceID, raw)
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

// Compile-time assertion that *store.Store satisfies HookStore. Keeps the
// production wiring honest if the interface or the store grows.
var _ HookStore = (*store.Store)(nil)
