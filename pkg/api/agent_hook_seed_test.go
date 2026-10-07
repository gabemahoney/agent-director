package api_test

// agent_hook_seed_test.go holds seeding writes the pkg/api tests make as a
// row's own agent through the store's gated hook path (SR-22.9): the gate
// comes from storefix.WithSeedPane, never built by hand. It holds no tests.

import (
	"fmt"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

// seedAgentState moves id's row in s, open on dbPath, to state through the
// gated hook transition of the row's own agent (row_version +1; nothing for
// pending); the row's pane and process identity are left as they were.
func seedAgentState(s *store.Store, dbPath, id, state string) error {
	if state == store.StatePending {
		return nil
	}
	return storefix.WithSeedPane(dbPath, id, func(gate store.HookGate) error {
		return storefix.SeedAgentWrites(s, gate, id, "", state)
	})
}

// relayHookTimeout does what id's live relay hook does at its poll deadline
// (internal/hook's timeout path): deny request token with reason timeout,
// then move the row to working, which closes the permission dialog.
func relayHookTimeout(t *testing.T, e *killEnv, id, token string) {
	t.Helper()
	if ok, err := e.st.DecidePermissionRequest(id, token, "deny", store.DecisionReasonTimeout, store.WriterProcessHook); err != nil || !ok {
		t.Fatalf("relay hook timeout deny of %s: updated=%v err=%v", token, ok, err)
	}
	if err := seedAgentState(e.st, e.dbPath, id, store.StateWorking); err != nil {
		t.Fatalf("relay hook timeout transition of %s: %v", id, err)
	}
}

// openAgentRequest inserts an open permission request for id in s (opened by
// storefix.OpenTempStore, an apitest fixture, or registered) through the gated
// INSERT of the row's own agent, with eviction cap (0: none).
func openAgentRequest(t *testing.T, s *store.Store, id, token, toolName, toolInput string, cap int) {
	t.Helper()
	dbPath := storefix.StorePath(t, s, "openAgentRequest")
	if err := storefix.WithSeedPane(dbPath, id, func(gate store.HookGate) error {
		applied, err := s.UpsertOpenPermissionRequest(id, gate, token, toolName, toolInput, cap, "")
		if err == nil && !applied.Applied {
			err = fmt.Errorf("not applied (reason %q)", applied.Reason)
		}
		return err
	}); err != nil {
		t.Fatalf("open permission request %s on %s: %v", token, id, err)
	}
}
