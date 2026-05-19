package hook

import (
	"context"
	"fmt"
	"io"
	"log"

	"github.com/gabemahoney/claude-director/internal/config"
	"github.com/gabemahoney/claude-director/internal/store"
)

// HookStore is the narrow store surface the handler needs. Production
// callers pass *store.Store; tests can pass a stub to drive failure
// branches (DB-unreachable, etc.) without scripting SQLite errors.
type HookStore interface {
	ApplyHookTransition(instanceID, newState string, softRefresh bool) error
	SetSessionID(instanceID, sessionID string) error
	UpsertOpenPermissionRequest(instanceID, toolName, toolInputJSON string) error
	GetPermissionRequest(instanceID string) (store.PermissionRow, error)
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
// the event is PermissionRequest AND CLAUDE_DIRECTOR_RELAY_MODE=on —
// runs the relay flow (DELETE-INSERT + polling loop + envelope on
// stdout per SRD §6.2/§6.3).
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

	// Any failure path before runRelay must still emit a deny envelope
	// when relay is active. We don't yet know whether this invocation
	// is a PermissionRequest, but defaulting to deny on relay-on is the
	// SRD §6.4 safe choice. Claude Code drops envelopes on
	// non-PermissionRequest events.
	failClosed := func(why string) {
		logf(logger, "hook: %s", why)
		if relayActive {
			_, _ = fmt.Fprintln(stdout, EncodeDecision("deny", ""))
		}
	}

	instanceID, err := ResolveInstanceID(env)
	if err != nil {
		failClosed(fmt.Sprintf("resolve instance id: %v", err))
		return nil
	}

	raw, err := ReadPayload(stdin)
	if err != nil {
		failClosed(fmt.Sprintf("read payload (instance=%s): %v", instanceID, err))
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
	if relayActive && res.EventName == "PermissionRequest" {
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
