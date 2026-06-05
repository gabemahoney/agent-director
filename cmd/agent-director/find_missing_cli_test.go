package main_test

import (
	"path/filepath"
	"testing"
)

// TestFindMissingTrailEmitsRowMutation runs `agent-director find-missing`
// against a DB that has one live spawn with an open permission_requests row
// (simulating a Claude instance whose process has vanished). It asserts that
// exactly one ad.row_mutation.committed trail line is emitted with
// writer_process="find_missing" and mutation_kind="update" — confirming that
// CloseOrphanedPermissionRequests drives the row-mutation event via the same
// DecidePermissionRequest path as the decide verb.
//
// Probe anchor: The Linux probe walks /proc for AGENT_DIRECTOR_INSTANCE_ID env
// values. To avoid the degraded-mode guard (probe returns 0 IDs + live DB rows
// exist → sweep aborted), AGENT_DIRECTOR_INSTANCE_ID is set in the child
// process's environment. The running find-missing process therefore appears in
// /proc with that ID, keeping probeSet non-empty without adding a DB row for it.
// The orphan ID ("id-fm-trail-1") is intentionally different from the anchor so
// the probe does NOT find it and the sweep correctly marks it missing.
func TestFindMissingTrailEmitsRowMutation(t *testing.T) {
	home := t.TempDir()
	stateDir := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")

	// Seed a spawn in a live state with an open permission request.
	const orphanID = "id-fm-trail-1"
	seedSpawnRow(t, dbPath, orphanID, "cd-fm-trail-1", "check_permission", "on")
	seedOpenPermissionRequest(t, dbPath, orphanID, testRequestToken, "Bash", `{"cmd":"ls"}`)

	// Run find-missing. AGENT_DIRECTOR_INSTANCE_ID is set so the probe finds the
	// find-missing process itself; the orphanID is absent from /proc so the sweep
	// marks it missing and closes its open permission request.
	_, stderr, code := runCLIWithEnv(t, home,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": "id-fm-probe-anchor",
			"AGENT_DIRECTOR_STATE_DIR":   stateDir,
		},
		"",
		"find-missing")
	if code != 0 {
		t.Fatalf("find-missing exit = %d; want 0\nstderr=%s", code, stderr)
	}

	// CloseOrphanedPermissionRequests calls DecidePermissionRequest("deny",
	// DecisionReasonFindMissing, WriterProcessFindMissing) once per open row.
	// With one open row, exactly one ad.row_mutation.committed line must appear.
	lines := readTrailLines(t, stateDir)
	rm := rowMutationCommittedLines(lines)
	if len(rm) != 1 {
		t.Fatalf("ad.row_mutation.committed line count = %d; want 1", len(rm))
	}
	if rm[0]["writer_process"] != "find_missing" {
		t.Errorf("writer_process = %v; want find_missing", rm[0]["writer_process"])
	}
	if rm[0]["mutation_kind"] != "update" {
		t.Errorf("mutation_kind = %v; want update", rm[0]["mutation_kind"])
	}
}
