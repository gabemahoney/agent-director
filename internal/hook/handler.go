package hook

import (
	"errors"
	"io"
	"log"

	"github.com/gabemahoney/claude-director/internal/store"
)

// HookStore is the narrow store surface the handler needs. Production
// callers pass *store.Store; tests can pass a stub to drive failure
// branches (DB-unreachable, etc.) without scripting SQLite errors.
type HookStore interface {
	ApplyHookTransition(instanceID, newState string, softRefresh bool) error
	SetSessionID(instanceID, sessionID string) error
}

// Handle is the entry point cmd/ dispatches into. It reads the payload
// from stdin, classifies the event, and writes the row UPSERT. The
// function is fail-open: any internal failure logs to logger and returns
// nil so the caller exits 0 with empty stdout (SRD §3.2).
//
// The relay-mode permission decision envelope is Epic 10's deliverable;
// for Epic 3 every event (including PermissionRequest) takes the
// state-tracking path.
func Handle(stdin io.Reader, env func(string) string, st HookStore, logger *log.Logger) error {
	instanceID, err := ResolveInstanceID(env)
	if err != nil {
		logf(logger, "hook: %v", err)
		return nil
	}

	raw, err := ReadPayload(stdin)
	if err != nil {
		logf(logger, "hook: read payload: %v", err)
		return nil
	}

	res, err := ClassifyEvent(raw)
	if err != nil {
		logf(logger, "hook: classify (instance=%s): %v", instanceID, err)
		return nil
	}

	if res.UnknownEvent {
		logf(logger, "hook: unknown event %q (instance=%s) — treating as soft refresh", res.EventName, instanceID)
	}

	if err := st.ApplyHookTransition(instanceID, res.NewState, res.SoftRefresh); err != nil {
		logf(logger, "hook: apply transition (instance=%s, event=%s): %v", instanceID, res.EventName, err)
		return nil
	}
	if res.SessionID != "" {
		if err := st.SetSessionID(instanceID, res.SessionID); err != nil {
			logf(logger, "hook: set session id (instance=%s): %v", instanceID, err)
			return nil
		}
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

// Sentinel re-exports so cmd/ can do errors.Is on the canonical names
// without re-importing io.
var (
	_ = ErrPayloadTooLarge
	_ = ErrInstanceIDMissing
	_ = ErrInstanceIDInvalid
	_ = errors.Is // keep errors imported even if cmd/ does the matching
)
