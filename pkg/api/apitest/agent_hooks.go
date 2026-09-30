package apitest

import (
	"fmt"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

// ApplyAgentHook fires one Claude Code hook event at id's row in the store at
// dbPath as the row's own agent: the hook's parent is the row's recorded pane
// process (its pane_pid, with its pane_starttime or, when NULL,
// storefix.SeedPaneStarttime), so the gate holds whenever the row records a
// pane (SR-22.9). It returns the store's store.HookApplied; a row with no
// pane reports no_pane_recorded, and no row reports nothing. sessionID is the
// payload's Claude session id ("" for none), recorded by SessionStart and by
// an ordinary hook on a row that records none. The event is classified as the
// hook handler classifies it, with SessionEnd a terminal one (the row ends)
// and PreToolUse for the Bash tool (storefix.FireHook); HookTranscript adds a
// transcript path. A store error fails the test. Tests use it rather than building a store.HookGate by hand.
func ApplyAgentHook(t *testing.T, dbPath, id, event, sessionID string, opts ...HookOption) store.HookApplied {
	t.Helper()
	s := openHookStore(t, dbPath, "ApplyAgentHook")
	defer func() { _ = s.Close() }()
	return storefix.ApplyAgentHook(t, s, id, event, sessionID, opts...)
}

// ApplyForeignHook fires one Claude Code hook event at id's row in the store
// at dbPath from another process carrying the row's id (a nested agent, a
// teammate pane, a leftover): the hook's parent is a pid other than the row's
// pane pid, so the gate never holds (SR-22.9) and the store reports
// pid_mismatch, or no_pane_recorded for a row with no pane, and writes
// nothing. Arguments and failures as ApplyAgentHook.
func ApplyForeignHook(t *testing.T, dbPath, id, event, sessionID string, opts ...HookOption) store.HookApplied {
	t.Helper()
	s := openHookStore(t, dbPath, "ApplyForeignHook")
	defer func() { _ = s.Close() }()
	return storefix.ApplyForeignHook(t, s, id, event, sessionID, opts...)
}

// HookOption adds a payload field to a hook ApplyAgentHook or
// ApplyForeignHook fires (storefix.HookOption).
type HookOption = storefix.HookOption

// HookTranscript makes the hook report transcript path jsonlPath, present on
// disk or not; the store records it under the presence rule
// (storefix.HookTranscript).
func HookTranscript(jsonlPath string, present bool) HookOption {
	return storefix.HookTranscript(jsonlPath, present)
}

// SeedSessionID records sessionID on id's existing row, which records no
// session id yet, through the gated soft-refresh hook write played by the
// row's own agent (storefix.WithSeedPane: a seed pane when the row records
// none, the pane and process identity written back afterwards). The state is
// unchanged; row_version advances by one. For a fixture row that needs a
// session id after it was seeded (OpenStoreWithRow).
func SeedSessionID(t *testing.T, dbPath, id, sessionID string) {
	t.Helper()
	s := openHookStore(t, dbPath, "SeedSessionID")
	defer func() { _ = s.Close() }()
	if err := storefix.WithSeedPane(dbPath, id, func(gate store.HookGate) error {
		return storefix.SeedAgentWrites(s, gate, id, sessionID, store.StatePending)
	}); err != nil {
		t.Fatalf("apitest.SeedSessionID(%q, %q): %v", id, sessionID, err)
	}
}

// seedAgentState moves id's freshly inserted row in s to state through the
// gated hook transition played by the row's own agent, leaving the row with
// no pane and no process identity, as InsertPending left it (row_version +1;
// nothing for pending). s must be registered with storefix.RegisterStorePath.
func seedAgentState(t *testing.T, s *store.Store, id, state, caller string) {
	t.Helper()
	if state == store.StatePending {
		return
	}
	dbPath := storefix.StorePath(t, s, caller)
	if err := storefix.WithSeedPane(dbPath, id, func(gate store.HookGate) error {
		return storefix.SeedAgentWrites(s, gate, id, "", state)
	}); err != nil {
		t.Fatalf("%s: %s → %s: %v", caller, id, state, err)
	}
}

// seedOpenRequest inserts an open permission request for id in s through the
// gated INSERT played by the row's own agent (storefix.WithSeedPane; the
// row's pane and process identity are left as they were). s must be
// registered with storefix.RegisterStorePath.
func seedOpenRequest(t *testing.T, s *store.Store, id, token, toolName, toolInput, caller string) {
	t.Helper()
	dbPath := storefix.StorePath(t, s, caller)
	if err := storefix.WithSeedPane(dbPath, id, func(gate store.HookGate) error {
		applied, err := s.UpsertOpenPermissionRequest(id, gate, token, toolName, toolInput, 0, store.WriterProcessHook)
		if err == nil && !applied.Applied {
			err = fmt.Errorf("hook not applied (reason %q)", applied.Reason)
		}
		return err
	}); err != nil {
		t.Fatalf("%s: open permission request on %s: %v", caller, id, err)
	}
}

// openHookStore opens the existing store at dbPath for one helper call.
func openHookStore(t *testing.T, dbPath, caller string) *store.Store {
	t.Helper()
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("apitest.%s: open store %q: %v", caller, dbPath, err)
	}
	return s
}
