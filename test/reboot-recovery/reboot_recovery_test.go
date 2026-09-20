package rebootrecovery_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRebootRecoveryEndToEnd is the plan's culminating acceptance, run against
// REAL tmux and a stub `claude` on PATH (no fake-tmux, no recorder):
//
//  1. Build agent-director; isolate HOME; put a stub `claude` first on PATH.
//  2. Spawn with --extra-env CLAUDE_CONFIG_DIR=<custom>; wait for its
//     SessionStart to persist identity (pid+starttime) + jsonl_path. Spawn one
//     OTHER row too (a second dead row for the "ALL dead rows" assertion).
//  3. tmux kill-server + SIGKILL every stub process; poll until their /proc
//     entries vanish (provably dead to the checker's ENOENT path).
//  4. ONE find-missing run: every dead row → missing, count>0, no refusal.
//  5. Resume the extra-env spawn: succeeds; the resurrected stub's
//     /proc/<pid>/environ carries CLAUDE_CONFIG_DIR == the custom dir.
//  6. The jsonl transcript holds BOTH pre-kill content and a post-resume
//     continuation for the same session.
//
// See writeStubClaude for the PM-pinned stub hook mechanism and the
// PM-required same-file-transcript fidelity note.
func TestRebootRecoveryEndToEnd(t *testing.T) {
	requireTmux(t)

	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	binaryAbs := buildBinary(t, binDir)

	// Custom CLAUDE_CONFIG_DIR carried as extra_env — the value we prove is
	// restored on the resurrected process.
	customCfgDir := filepath.Join(root, "custom-claude-config")
	if err := os.MkdirAll(customCfgDir, 0o700); err != nil {
		t.Fatalf("mkdir custom cfg: %v", err)
	}

	// The stub writes/reads ONE transcript file (same-file simplification).
	jsonlPath := filepath.Join(root, "transcript", "session.jsonl")
	stubDir := writeStubClaude(t, binDir, binaryAbs, jsonlPath)
	env := cliEnv(home, stubDir)

	// Fresh tmux server so `new-session` inherits OUR PATH (stub first), not a
	// lingering server's. Best-effort — a missing server is fine.
	_ = exec.Command("tmux", "kill-server").Run()

	// ── Step 1: bootstrap the store (list triggers schema migration) ──────────
	if _, stderr, code := runCLI(t, binaryAbs, env, "list"); code != 0 {
		t.Fatalf("list bootstrap exit=%d stderr=%s", code, stderr)
	}

	cwd := filepath.Join(root, "cwd")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}

	// ── Step 2: spawn the extra-env row + one other row ───────────────────────
	targetID := mustSpawn(t, binaryAbs, env,
		"--cwd", cwd,
		"--extra-env", "CLAUDE_CONFIG_DIR="+customCfgDir)

	otherID := mustSpawn(t, binaryAbs, env, "--cwd", cwd)

	dbPath := filepath.Join(home, ".agent-director", "state.db")

	// Wait for BOTH stubs' SessionStart hooks to persist identity + flip state
	// to waiting. The find-missing checker needs a recorded pid+starttime; the
	// jsonl pre-flight on resume needs a persisted jsonl_path.
	waitFor(t, "target SessionStart persists identity+jsonl and flips to waiting", func() bool {
		return rowReady(t, dbPath, targetID)
	})
	waitFor(t, "other SessionStart persists identity and flips to waiting", func() bool {
		return rowReady(t, dbPath, otherID)
	})

	// Confirm get surfaces the persisted jsonl_path for the target (resume
	// pre-flight reads exactly this).
	row := getRow(t, binaryAbs, env, targetID)
	if got, _ := row["jsonl_path"].(string); got != jsonlPath {
		t.Fatalf("get.jsonl_path = %q; want %q (persisted by SessionStart)", got, jsonlPath)
	}

	// The pre-kill transcript content must exist on disk before the kill.
	waitFor(t, "pre-kill transcript line written", func() bool {
		b, err := os.ReadFile(jsonlPath)
		return err == nil && strings.Contains(string(b), "pre-kill")
	})

	// ── Step 3: reboot simulation — kill tmux server + all stub processes ─────
	if len(stubPIDs(t)) == 0 {
		t.Fatalf("expected at least one live stub before kill; found none")
	}
	// Capture the recorded identity pids BEFORE the kill so we can wait for those
	// exact /proc entries to be fully gone — not merely for the marker-carrying
	// live set to empty. A SIGKILLed pane process lingers as a <defunct> zombie
	// (empty environ, but /proc/<pid> still present with a matching starttime)
	// until its parent is reaped; find-missing's checker treats a live pid whose
	// starttime still matches as evidence to weigh, so we must let the reboot
	// simulation fully retire the pid before the single sweep runs.
	recordedPIDs := []int64{recordedPID(t, dbPath, targetID), recordedPID(t, dbPath, otherID)}

	if out, err := exec.Command("tmux", "kill-server").CombinedOutput(); err != nil {
		t.Logf("tmux kill-server: %v (%s) — proceeding to SIGKILL stubs", err, out)
	}
	killStubs(t)
	// Poll until every recorded pid's /proc entry is entirely gone (ENOENT) —
	// the checker's provably-dead path the acceptance describes. reapZombies
	// clears any <defunct> pane the SIGKILL left behind (this process is a
	// child-subreaper, so the reparented panes are waitable here); without
	// reaping, a zombie's /proc entry lingers with a matching starttime and the
	// checker would not see the process as gone.
	waitFor(t, "recorded identity pids fully retired (/proc ENOENT)", func() bool {
		killStubs(t)
		reapZombies()
		for _, pid := range recordedPIDs {
			if _, err := os.Stat("/proc/" + itoa(pid)); err == nil {
				return false
			}
		}
		return true
	})

	// ── Step 4: ONE find-missing marks ALL dead rows missing, no refusal ──────
	stdout, stderr, code := runCLI(t, binaryAbs, env, "find-missing")
	if code != 0 {
		t.Fatalf("find-missing exit=%d stderr=%s", code, stderr)
	}
	fm := parseFindMissing(t, stdout)
	if fm.Count <= 0 {
		t.Fatalf("find-missing count=%d; want > 0 (raw=%s)", fm.Count, stdout)
	}
	// No refusal / no degraded behavior: every dead row must be verified-missing,
	// none left unverified (a permission-wall refusal would land here).
	if fm.Unverified != 0 || len(fm.UnverifiedIDs) != 0 {
		t.Fatalf("find-missing left rows unverified (refusal/degraded): unverified=%d ids=%v raw=%s",
			fm.Unverified, fm.UnverifiedIDs, stdout)
	}
	for _, want := range []string{targetID, otherID} {
		if !containsID(fm.IDs, want) {
			t.Fatalf("find-missing did not mark %s missing; ids=%v raw=%s", want, fm.IDs, stdout)
		}
	}
	// Both rows now read state=missing.
	if st := getState(t, dbPath, targetID); st != "missing" {
		t.Fatalf("target state=%q; want missing", st)
	}
	if st := getState(t, dbPath, otherID); st != "missing" {
		t.Fatalf("other state=%q; want missing", st)
	}

	// ── Step 5: resume the extra-env row; assert restored CLAUDE_CONFIG_DIR ───
	// A stale tmux session name would block resume; the server was killed so the
	// canonical name is free. Poll for readiness in case the server socket is
	// still tearing down.
	waitFor(t, "resume succeeds", func() bool {
		_, _, code := runCLI(t, binaryAbs, env, "resume", "--claude-instance-id", targetID)
		return code == 0
	})

	// The resurrected stub is a NEW process; wait for it to appear, then read
	// its environ and assert the custom config dir was restored verbatim.
	var resumedEnviron map[string]string
	waitFor(t, "resurrected stub process appears with readable environ", func() bool {
		for _, pid := range stubPIDs(t) {
			e, ok := readEnviron(pid)
			if !ok {
				continue
			}
			if e["AGENT_DIRECTOR_INSTANCE_ID"] == targetID {
				resumedEnviron = e
				return true
			}
		}
		return false
	})
	if got := resumedEnviron["CLAUDE_CONFIG_DIR"]; got != customCfgDir {
		t.Fatalf("resurrected CLAUDE_CONFIG_DIR = %q; want %q (extra_env restored on resume)",
			got, customCfgDir)
	}

	// ── Step 6: transcript continues the pre-kill session ─────────────────────
	waitFor(t, "post-resume continuation appended to the same transcript", func() bool {
		b, err := os.ReadFile(jsonlPath)
		if err != nil {
			return false
		}
		s := string(b)
		return strings.Contains(s, "pre-kill") && strings.Contains(s, "post-resume-continuation")
	})

	// Cleanup: kill the resurrected stub + its tmux server so we do not leak
	// processes across the sandbox run.
	_ = exec.Command("tmux", "kill-server").Run()
	killStubs(t)
}
