package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// findMissingTickLines filters trail lines for ad.find_missing.tick events.
func findMissingTickLines(lines []map[string]any) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["event"] == "ad.find_missing.tick" {
			out = append(out, l)
		}
	}
	return out
}

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

// TestFindMissingTrailEmitsProcAbsentTick confirms that find-missing emits one
// ad.find_missing.tick line with reconciliation_reason="proc_absent" when a
// live spawn is absent from /proc and has no open permission_requests rows.
// The probe anchor (AGENT_DIRECTOR_INSTANCE_ID) keeps probeSet non-empty so
// the degraded-mode guard does not trip; the orphan ID is distinct from the
// anchor so the sweep correctly marks it missing.
func TestFindMissingTrailEmitsProcAbsentTick(t *testing.T) {
	home := t.TempDir()
	stateDir := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")

	const orphanID = "id-fm-tick-pa-1"
	// Use working state / relay_mode=off: no open permission_requests rows,
	// so only one tick (proc_absent) should be emitted.
	seedSpawnRow(t, dbPath, orphanID, "cd-fm-tick-pa-1", "working", "off")

	_, stderr, code := runCLIWithEnv(t, home,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": "id-fm-tick-pa-anchor",
			"AGENT_DIRECTOR_STATE_DIR":   stateDir,
		},
		"",
		"find-missing")
	if code != 0 {
		t.Fatalf("find-missing exit = %d; want 0\nstderr=%s", code, stderr)
	}

	lines := readTrailLines(t, stateDir)
	ticks := findMissingTickLines(lines)
	// One spawn, no open permission requests → exactly one proc_absent tick.
	if len(ticks) != 1 {
		t.Fatalf("ad.find_missing.tick line count = %d; want 1", len(ticks))
	}
	tick := ticks[0]
	if tick["reconciliation_reason"] != "proc_absent" {
		t.Errorf("reconciliation_reason = %v; want proc_absent", tick["reconciliation_reason"])
	}
	if tick["claude_instance_id"] != orphanID {
		t.Errorf("claude_instance_id = %v; want %q", tick["claude_instance_id"], orphanID)
	}
	if tick["new_state"] != "missing" {
		t.Errorf("new_state = %v; want missing", tick["new_state"])
	}
	if tick["source"] != "ad_find_missing" {
		t.Errorf("source = %v; want ad_find_missing", tick["source"])
	}
}

// TestFindMissingTrailEmitsPermissionOrphanCloseoutTick confirms that
// find-missing emits one ad.find_missing.tick line with
// reconciliation_reason="permission_orphan_closeout" — carrying the
// request_token — when it closes an open permission_requests row for a swept
// spawn. In addition to the orphan-closeout tick, a proc_absent tick must also
// appear for the spawn transition itself.
func TestFindMissingTrailEmitsPermissionOrphanCloseoutTick(t *testing.T) {
	home := t.TempDir()
	stateDir := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")

	const orphanID = "id-fm-tick-poc-1"
	seedSpawnRow(t, dbPath, orphanID, "cd-fm-tick-poc-1", "check_permission", "on")
	seedOpenPermissionRequest(t, dbPath, orphanID, testRequestToken, "Bash", `{"cmd":"ls"}`)

	_, stderr, code := runCLIWithEnv(t, home,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": "id-fm-tick-poc-anchor",
			"AGENT_DIRECTOR_STATE_DIR":   stateDir,
		},
		"",
		"find-missing")
	if code != 0 {
		t.Fatalf("find-missing exit = %d; want 0\nstderr=%s", code, stderr)
	}

	lines := readTrailLines(t, stateDir)
	ticks := findMissingTickLines(lines)

	// Expect two ticks: proc_absent (spawn sweep) + permission_orphan_closeout
	// (open permission_requests row closed by CloseOrphanedPermissionRequests).
	var procAbsent, orphanCloseout map[string]any
	for _, tk := range ticks {
		switch tk["reconciliation_reason"] {
		case "proc_absent":
			procAbsent = tk
		case "permission_orphan_closeout":
			orphanCloseout = tk
		}
	}

	if procAbsent == nil {
		t.Fatalf("no proc_absent tick found in %d ad.find_missing.tick line(s)", len(ticks))
	}
	if orphanCloseout == nil {
		t.Fatalf("no permission_orphan_closeout tick found in %d ad.find_missing.tick line(s)", len(ticks))
	}

	// proc_absent: the orphan's instance id and source must be present.
	if procAbsent["claude_instance_id"] != orphanID {
		t.Errorf("proc_absent: claude_instance_id = %v; want %q", procAbsent["claude_instance_id"], orphanID)
	}
	if procAbsent["source"] != "ad_find_missing" {
		t.Errorf("proc_absent: source = %v; want ad_find_missing", procAbsent["source"])
	}

	// permission_orphan_closeout: must carry the request_token and instance id.
	if orphanCloseout["claude_instance_id"] != orphanID {
		t.Errorf("permission_orphan_closeout: claude_instance_id = %v; want %q",
			orphanCloseout["claude_instance_id"], orphanID)
	}
	if orphanCloseout["request_token"] != testRequestToken {
		t.Errorf("permission_orphan_closeout: request_token = %v; want %q",
			orphanCloseout["request_token"], testRequestToken)
	}
	if orphanCloseout["source"] != "ad_find_missing" {
		t.Errorf("permission_orphan_closeout: source = %v; want ad_find_missing", orphanCloseout["source"])
	}
}

// TestFindMissingTrailEmitsDegradedModeSkipTick confirms that find-missing
// emits one ad.find_missing.tick line with reconciliation_reason=
// "degraded_mode_skip" and claude_instance_id=null when the probe returns zero
// IDs but live DB rows exist (SRD §14.6), and that the sweep writes zero rows.
//
// Degraded mode is triggered by running find-missing without
// AGENT_DIRECTOR_INSTANCE_ID in the child process's env so the child is not
// visible to the probe. The test is skipped when the test runner itself has
// AGENT_DIRECTOR_INSTANCE_ID set (i.e. when running inside an
// agent-director-managed Claude session) — that process would appear in /proc
// and make the probeSet non-empty, bypassing the guard.
func TestFindMissingTrailEmitsDegradedModeSkipTick(t *testing.T) {
	if os.Getenv("AGENT_DIRECTOR_INSTANCE_ID") != "" {
		t.Skip("AGENT_DIRECTOR_INSTANCE_ID is set in the test runner; " +
			"the probe would find this process in /proc and the degraded-mode guard would not trip")
	}

	home := t.TempDir()
	stateDir := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")

	const liveID = "id-fm-tick-dm-1"
	seedSpawnRow(t, dbPath, liveID, "cd-fm-tick-dm-1", "working", "off")

	// Omit AGENT_DIRECTOR_INSTANCE_ID from the child env entirely. The
	// find-missing process itself will not appear in the probe's /proc walk;
	// in a clean test environment no other process holds this env var, so
	// probeSet is empty and the degraded-mode guard trips.
	stdout, stderr, code := runCLIWithEnv(t, home,
		map[string]string{
			"AGENT_DIRECTOR_STATE_DIR": stateDir,
		},
		"",
		"find-missing")
	if code != 0 {
		t.Fatalf("find-missing exit = %d; want 0\nstderr=%s", code, stderr)
	}

	// Degraded mode returns count=0, ids=[] — the sweep refused to write.
	if !strings.Contains(stdout, `"count":0`) {
		t.Errorf("stdout = %q; want count=0 (degraded mode must refuse to sweep)", stdout)
	}

	lines := readTrailLines(t, stateDir)
	ticks := findMissingTickLines(lines)
	if len(ticks) != 1 {
		t.Fatalf("ad.find_missing.tick line count = %d; want 1 (degraded_mode_skip)", len(ticks))
	}
	tick := ticks[0]
	if tick["reconciliation_reason"] != "degraded_mode_skip" {
		t.Errorf("reconciliation_reason = %v; want degraded_mode_skip", tick["reconciliation_reason"])
	}
	// claude_instance_id is null in the degraded-mode event (no specific instance).
	if tick["claude_instance_id"] != nil {
		t.Errorf("claude_instance_id = %v; want null", tick["claude_instance_id"])
	}
	if tick["source"] != "ad_find_missing" {
		t.Errorf("source = %v; want ad_find_missing", tick["source"])
	}
}
