package main_test

// operator_actions_absent_cli_test.go pins b.vqr's delete on the main CLI:
// delete is not an agent-director verb (it lives on the off-PATH
// agent-director-admin binary). Kill's former opt-in is in
// kill_optin_flag_cli_test.go, and help in help_test.go.

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestDeleteIsUnknownVerb: delete is ErrUnknownVerb on the main CLI, and the
// row it names stays (b.vqr).
func TestDeleteIsUnknownVerb(t *testing.T) {
	home := t.TempDir()
	id, err := apitest.SeedSpawn(stateDB(home), "", store.StateEnded, "", "", "", true)
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}

	stdout, stderr, code := runCLIWithHome(t, home, "delete", "--claude-instance-id", id)

	assertOnlyEnvelope(t, stdout, stderr, code, "ErrUnknownVerb")
	if _, err := apitest.ReadSpawnColumns(stateDB(home), id); err != nil {
		t.Errorf("row %s after delete: %v; want it kept", id, err)
	}
}
