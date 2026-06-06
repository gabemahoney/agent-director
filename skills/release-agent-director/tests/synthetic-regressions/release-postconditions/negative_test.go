// Package releasepostconditions_test — negative postcondition regression
// (b.wvr subtask 49.85).
//
// # Background
//
// The negative tests prove that publish-orchestrator.sh is NOT a vacuous
// pass-through that ignores failures.  Two scenarios are exercised:
//
//  1. [TestReleasePostconditionsNegativePriorFailure] — dry-run mode with a
//     prior-phases JSON where coverage=failed.  Asserts the failure is
//     preserved in report.phases[1]; the report is not a vacuous "all passed".
//
//  2. [TestReleasePostconditionsNegativeSimulateFailure] — release mode with
//     --simulate-failure-at push-branch.  push-branch is the FIRST substep, so
//     the simulate flag fires immediately without executing any real git
//     command.  Asserts exit 1, exactly 1 substep entry (halt-on-failure), and
//     a matching diagnostic with which_substep_failed set.
//
// # SLOW TEST
//
// Both tests invoke publish-orchestrator.sh (bash + jq, ≈1–2 s).
// Skipped in -short mode.
package releasepostconditions_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestReleasePostconditionsNegativePriorFailure invokes publish-orchestrator.sh
// in --dry-run mode with a prior-phases JSON where coverage failed.  It asserts:
//
//   - report.phases[1].outcome == "failed" (failure is preserved verbatim)
//   - report.publish_substeps has 6 entries, all "skipped" (dry-run runs all
//     substeps regardless of prior-phase outcomes — the orchestrator does not
//     gate on prior failures in dry-run mode)
//   - report.diagnostics is empty (prior-phase failures are not re-diagnosed
//     by the publish phase)
//
// This confirms the report is NOT a vacuous "always passed" result.
func TestReleasePostconditionsNegativePriorFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: invokes publish-orchestrator.sh")
	}

	root := repoRoot(t)
	registerReportCleanup(t, root)

	// ── 1. Write the failed-coverage prior-phases JSON ────────────────────
	phasesFile := filepath.Join(t.TempDir(), "prior-phases-failed.json")
	if err := os.WriteFile(phasesFile, []byte(priorPhasesWithFailureJSON), 0o644); err != nil {
		t.Fatalf("write prior-phases-failed.json: %v", err)
	}

	// ── 2. Resolve HEAD SHA ────────────────────────────────────────────────
	shaOut, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	sha := strings.TrimSpace(string(shaOut))

	// ── 3. Run publish-orchestrator.sh --dry-run ───────────────────────────
	orchScript := filepath.Join(root, "skills", "release-agent-director",
		"gates", "publish", "publish-orchestrator.sh")
	cmd := exec.Command("bash", orchScript,
		"--target", testTarget,
		"--bump-sha", sha,
		"--tarball", "/tmp/fake.tgz",
		"--notes", "/tmp/fake-notes.md",
		"--binaries", "/tmp/bin1,/tmp/bin2,/tmp/bin3",
		"--dry-run",
		"--prior-phases", phasesFile,
	)
	cmd.Dir = root
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("publish-orchestrator.sh --dry-run failed (exit %d):\n%s",
			cmd.ProcessState.ExitCode(), out)
	}
	t.Logf("publish-orchestrator.sh stdout+stderr:\n%s", out)

	// ── 4. Parse the report ────────────────────────────────────────────────
	report := parseReport(t, root)

	// ── 5. Assert coverage phase preserved as "failed" (non-vacuity) ──────
	if len(report.Phases) < 2 {
		t.Fatalf("report.phases: want ≥2 entries, got %d", len(report.Phases))
	}
	coveragePhase := report.Phases[1]
	if coveragePhase.Name != "coverage" {
		t.Errorf("phases[1].name: want %q, got %q", "coverage", coveragePhase.Name)
	}
	if coveragePhase.Outcome != "failed" {
		t.Errorf("phases[1] (coverage).outcome: want %q, got %q — "+
			"report must not erase prior failures",
			"failed", coveragePhase.Outcome)
	}

	// ── 6. Assert all 6 substeps skipped (dry-run ignores prior failures) ─
	if len(report.PublishSubsteps) != 6 {
		t.Errorf("report.publish_substeps: want 6 entries, got %d",
			len(report.PublishSubsteps))
	}
	for i, sub := range report.PublishSubsteps {
		if sub.Outcome != "skipped" {
			t.Errorf("publish_substeps[%d] (%s).outcome: want %q, got %q",
				i, sub.Name, "skipped", sub.Outcome)
		}
	}

	// ── 7. Assert diagnostics empty ───────────────────────────────────────
	if len(report.Diagnostics) != 0 {
		t.Errorf("report.diagnostics: want empty, got %d entries",
			len(report.Diagnostics))
	}

	t.Log("release-postconditions negative (prior failure preserved): non-vacuity confirmed ✓")
}

// TestReleasePostconditionsNegativeSimulateFailure invokes
// publish-orchestrator.sh in --release mode with --simulate-failure-at
// push-branch.  Because push-branch is the FIRST substep, the simulate flag
// fires before any real git command is attempted — making this test safe to run
// without a live git remote.  It asserts:
//
//   - script exits 1 (non-zero — the harness FAILS when a substep fails)
//   - report.publish_substeps has exactly 1 entry (halt-on-failure stops
//     subsequent substeps from running)
//   - that entry: name="publish.push-branch", outcome="failed"
//   - report.diagnostics has exactly 1 entry with which_substep_failed set to
//     "publish.push-branch"
//
// This is the definitive proof of non-vacuity: the harness propagates failure.
func TestReleasePostconditionsNegativeSimulateFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: invokes publish-orchestrator.sh")
	}

	root := repoRoot(t)
	registerReportCleanup(t, root)

	// ── 1. Resolve HEAD SHA ────────────────────────────────────────────────
	shaOut, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	sha := strings.TrimSpace(string(shaOut))

	// ── 2. Run --release --simulate-failure-at push-branch ────────────────
	// push-branch is the very first substep, so the simulate flag fires
	// immediately — no earlier substep can attempt a real git push.
	orchScript := filepath.Join(root, "skills", "release-agent-director",
		"gates", "publish", "publish-orchestrator.sh")
	cmd := exec.Command("bash", orchScript,
		"--target", testTarget,
		"--bump-sha", sha,
		"--tarball", "/tmp/fake.tgz",
		"--notes", "/tmp/fake-notes.md",
		"--binaries", "/tmp/bin1,/tmp/bin2,/tmp/bin3",
		"--release",
		"--simulate-failure-at", "push-branch",
	)
	cmd.Dir = root
	var outBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	_ = cmd.Run() // non-zero exit is expected

	exitCode := cmd.ProcessState.ExitCode()
	t.Logf("publish-orchestrator.sh --release --simulate-failure-at push-branch: exit %d\noutput:\n%s",
		exitCode, outBuf.String())

	// ── 3. Assert non-zero exit ────────────────────────────────────────────
	if exitCode == 0 {
		t.Fatal("want exit 1 from simulated substep failure, got exit 0 — harness is vacuous")
	}

	// ── 4. Parse the report ────────────────────────────────────────────────
	report := parseReport(t, root)

	// ── 5. Assert exactly 1 substep recorded (halt-on-failure) ────────────
	if len(report.PublishSubsteps) != 1 {
		t.Fatalf("report.publish_substeps: want 1 entry (halt-on-failure stops "+
			"subsequent substeps), got %d", len(report.PublishSubsteps))
	}

	// ── 6. Assert failed substep is push-branch ───────────────────────────
	failed := report.PublishSubsteps[0]
	if failed.Name != "publish.push-branch" {
		t.Errorf("publish_substeps[0].name: want %q, got %q",
			"publish.push-branch", failed.Name)
	}
	if failed.Outcome != "failed" {
		t.Errorf("publish_substeps[0].outcome: want %q, got %q",
			"failed", failed.Outcome)
	}

	// ── 7. Assert exactly 1 diagnostic with correct which_substep_failed ──
	if len(report.Diagnostics) != 1 {
		t.Fatalf("report.diagnostics: want 1 entry, got %d", len(report.Diagnostics))
	}
	diag := report.Diagnostics[0]
	if diag.WhichSubstepFailed != "publish.push-branch" {
		t.Errorf("diagnostics[0].which_substep_failed: want %q, got %q",
			"publish.push-branch", diag.WhichSubstepFailed)
	}

	t.Log("release-postconditions negative (simulate failure): halt-on-failure + non-vacuity confirmed ✓")
}
