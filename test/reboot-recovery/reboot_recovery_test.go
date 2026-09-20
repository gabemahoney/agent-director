package rebootrecovery_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/spawn"
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

	// The stub derives its transcript path at runtime from $CLAUDE_CONFIG_DIR +
	// slug($PWD) + sid (b.1ba AC6) — no baked literal. The test derives the
	// expected path independently below via spawn.JsonlPathIn.
	stubDir := writeStubClaude(t, binDir, binaryAbs)
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

	// Derive the expected transcript path INDEPENDENTLY of the stub (b.1ba AC6):
	// the stub mints sid = "sess-<instanceID>" on a fresh spawn and composes the
	// path under CLAUDE_CONFIG_DIR; here we recompose it from the store-observable
	// inputs (customCfgDir, cwd, targetID) using the production path resolver.
	// If the two derivations disagreed, the pre-kill/get assertions below would
	// catch it — so the marker-in-transcript signal is non-circular.
	jsonlPath, err := spawn.JsonlPathIn(customCfgDir, cwd, stubSessionID(targetID))
	if err != nil {
		t.Fatalf("derive expected transcript path: %v", err)
	}

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
	// resume is a MUTATING verb: a partial success (it creates the tmux session,
	// then fails later) leaves the session name taken, so every retry would fail
	// on session-exists and mask the ORIGINAL failure behind an opaque timeout.
	// So DON'T retry resume. Instead poll a READINESS condition first — the killed
	// server left the canonical session name in use until the socket finishes
	// tearing down — then make exactly ONE resume attempt, surfacing its captured
	// stdout/stderr verbatim on failure.
	sessionName, _ := row["tmux_session_name"].(string)
	if sessionName == "" {
		t.Fatalf("target row has empty tmux_session_name; cannot gate resume readiness (row=%v)", row)
	}
	waitFor(t, "canonical tmux session name free after kill-server (has-session != 0)", func() bool {
		// has-session exits 0 iff the session exists; a non-zero exit (no server,
		// or session absent) means the name is free for resume to recreate.
		cmd := exec.Command("tmux", "has-session", "-t", sessionName)
		cmd.Env = env
		return cmd.Run() != nil
	})
	stdout, stderr, code = runCLI(t, binaryAbs, env, "resume", "--claude-instance-id", targetID)
	if code != 0 {
		t.Fatalf("resume exit=%d; stdout=%s stderr=%s", code, stdout, stderr)
	}

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

// TestRebootRecoveryNulledJsonlPathHealsViaConfigDir is the b.1ba AC6
// culminating E2E: it forces resume down the CONFIG_DIR-aware fallback by
// NULLing the row's persisted jsonl_path AFTER find-missing, so the
// persisted-path branch is provably NOT the thing exercised (with jsonl_path
// intact the existing test above passes on the persisted branch and never
// touches the new fallback code). Resume must still exit 0 and land the
// post-resume-continuation marker in the transcript at the CONFIG_DIR-derived
// path — the path the stub composes from $CLAUDE_CONFIG_DIR+argv and the test
// composes independently via spawn.JsonlPathIn.
func TestRebootRecoveryNulledJsonlPathHealsViaConfigDir(t *testing.T) {
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

	customCfgDir := filepath.Join(root, "custom-claude-config")
	if err := os.MkdirAll(customCfgDir, 0o700); err != nil {
		t.Fatalf("mkdir custom cfg: %v", err)
	}

	stubDir := writeStubClaude(t, binDir, binaryAbs)
	env := cliEnv(home, stubDir)

	_ = exec.Command("tmux", "kill-server").Run()

	if _, stderr, code := runCLI(t, binaryAbs, env, "list"); code != 0 {
		t.Fatalf("list bootstrap exit=%d stderr=%s", code, stderr)
	}

	cwd := filepath.Join(root, "cwd")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}

	// ── Spawn under a custom CLAUDE_CONFIG_DIR ────────────────────────────────
	targetID := mustSpawn(t, binaryAbs, env,
		"--cwd", cwd,
		"--extra-env", "CLAUDE_CONFIG_DIR="+customCfgDir)

	// Independently derive the CONFIG_DIR path the transcript must land at.
	jsonlPath, err := spawn.JsonlPathIn(customCfgDir, cwd, stubSessionID(targetID))
	if err != nil {
		t.Fatalf("derive expected transcript path: %v", err)
	}

	dbPath := filepath.Join(home, ".agent-director", "state.db")

	waitFor(t, "target SessionStart persists identity+jsonl and flips to waiting", func() bool {
		return rowReady(t, dbPath, targetID)
	})

	// Sanity: SessionStart persisted the derived path (proves stub↔test path
	// agreement before we NULL it).
	if got := jsonlPathColumn(t, dbPath, targetID); got != jsonlPath {
		t.Fatalf("persisted jsonl_path = %q; want derived CONFIG_DIR path %q", got, jsonlPath)
	}

	waitFor(t, "pre-kill transcript line written", func() bool {
		b, err := os.ReadFile(jsonlPath)
		return err == nil && strings.Contains(string(b), "pre-kill")
	})

	// ── Reboot simulation ─────────────────────────────────────────────────────
	if len(stubPIDs(t)) == 0 {
		t.Fatalf("expected at least one live stub before kill; found none")
	}
	recordedPIDs := []int64{recordedPID(t, dbPath, targetID)}
	if out, err := exec.Command("tmux", "kill-server").CombinedOutput(); err != nil {
		t.Logf("tmux kill-server: %v (%s) — proceeding to SIGKILL stubs", err, out)
	}
	killStubs(t)
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

	// ── find-missing marks the row missing ────────────────────────────────────
	stdout, stderr, code := runCLI(t, binaryAbs, env, "find-missing")
	if code != 0 {
		t.Fatalf("find-missing exit=%d stderr=%s", code, stderr)
	}
	fm := parseFindMissing(t, stdout)
	if !containsID(fm.IDs, targetID) {
		t.Fatalf("find-missing did not mark %s missing; ids=%v raw=%s", targetID, fm.IDs, stdout)
	}
	if st := getState(t, dbPath, targetID); st != "missing" {
		t.Fatalf("target state=%q; want missing", st)
	}

	// ── ESSENTIAL: NULL the persisted jsonl_path so resume CANNOT use it ───────
	// This forces the CONFIG_DIR-aware fallback. The transcript still lives at
	// the derived path on disk (we never removed it); only the row's recorded
	// pointer to it is gone — exactly the pre-0.8.0 legacy-row shape.
	nullOutJsonlPath(t, dbPath, targetID)
	if got := jsonlPathColumn(t, dbPath, targetID); got != "" {
		t.Fatalf("jsonl_path not NULLed: %q", got)
	}

	// ── Resume: must heal via CONFIG_DIR fallback, exit 0 ─────────────────────
	row := getRow(t, binaryAbs, env, targetID)
	sessionName, _ := row["tmux_session_name"].(string)
	if sessionName == "" {
		t.Fatalf("target row has empty tmux_session_name (row=%v)", row)
	}
	waitFor(t, "canonical tmux session name free after kill-server", func() bool {
		cmd := exec.Command("tmux", "has-session", "-t", sessionName)
		cmd.Env = env
		return cmd.Run() != nil
	})
	stdout, stderr, code = runCLI(t, binaryAbs, env, "resume", "--claude-instance-id", targetID)
	if code != 0 {
		t.Fatalf("resume (NULLed jsonl_path) exit=%d; stdout=%s stderr=%s", code, stdout, stderr)
	}

	// ── The continuation marker lands at the CONFIG_DIR-derived path ──────────
	waitFor(t, "post-resume continuation appended to the CONFIG_DIR-derived transcript", func() bool {
		b, err := os.ReadFile(jsonlPath)
		if err != nil {
			return false
		}
		s := string(b)
		return strings.Contains(s, "pre-kill") && strings.Contains(s, "post-resume-continuation")
	})

	// Self-healing: a successful fallback resume re-fires SessionStart, which
	// re-persists the correct jsonl_path (handler.go:213 / b.1ba scope 1).
	waitFor(t, "jsonl_path re-persisted after fallback resume (self-heal)", func() bool {
		return jsonlPathColumn(t, dbPath, targetID) == jsonlPath
	})

	_ = exec.Command("tmux", "kill-server").Run()
	killStubs(t)
}
