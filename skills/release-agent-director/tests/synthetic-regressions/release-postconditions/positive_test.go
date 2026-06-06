// Package releasepostconditions_test — positive postcondition regression
// (b.wvr subtask 49.oz).
//
// # Background
//
// publish-orchestrator.sh is the E9 publish phase end-to-end executor.  In
// --dry-run mode it emits "[publish.<name>] would do: <cmd>" lines for each of
// its 6 substeps, records all 6 with outcome=skipped, and writes the final
// report to skills/release-agent-director/dist/release-report.json.
//
// This test verifies the POSITIVE postconditions:
//
//   - report.mode == "dry-run"
//   - report.phases carries the 5 prior-phase entries verbatim, all "passed"
//   - report.publish_substeps has exactly 6 entries, all outcome="skipped"
//   - each substep's command is non-empty and begins with the expected verb
//   - report.diagnostics is empty (no failures, no diagnostics emitted)
//
// # SLOW TEST
//
// Invokes publish-orchestrator.sh (bash + jq, ≈1–2 s).
// Skipped in -short mode.
package releasepostconditions_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestReleasePostconditionsPositive invokes publish-orchestrator.sh in
// --dry-run mode with a synthetic prior-phases JSON where all five pre-publish
// phases passed.  It asserts the full positive postcondition set.
func TestReleasePostconditionsPositive(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: invokes publish-orchestrator.sh")
	}

	root := repoRoot(t)
	registerReportCleanup(t, root)

	// ── 1. Write the all-passed prior-phases JSON to a temp file ──────────
	phasesFile := filepath.Join(t.TempDir(), "prior-phases.json")
	if err := os.WriteFile(phasesFile, []byte(priorPhasesAllPassedJSON), 0o644); err != nil {
		t.Fatalf("write prior-phases.json: %v", err)
	}

	// ── 2. Resolve HEAD SHA (required by --bump-sha) ───────────────────────
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

	// ── 5. Assert mode ─────────────────────────────────────────────────────
	if report.Mode != "dry-run" {
		t.Errorf("report.mode: want %q, got %q", "dry-run", report.Mode)
	}

	// ── 6. Assert phases: 5 entries, all passed ────────────────────────────
	wantPhaseNames := []string{"preflight", "coverage", "compile", "pack", "notes"}
	if len(report.Phases) != len(wantPhaseNames) {
		t.Fatalf("report.phases: want %d entries, got %d",
			len(wantPhaseNames), len(report.Phases))
	}
	for i, wantName := range wantPhaseNames {
		phase := report.Phases[i]
		if phase.Name != wantName {
			t.Errorf("phases[%d].name: want %q, got %q", i, wantName, phase.Name)
		}
		if phase.Outcome != "passed" {
			t.Errorf("phases[%d] (%s).outcome: want %q, got %q",
				i, phase.Name, "passed", phase.Outcome)
		}
	}

	// ── 7. Assert publish_substeps: 6 entries, all skipped ────────────────
	// cmdVerb is the expected prefix of each substep's command string.
	// publish.npm-publish's display command starts with "cd" (it cd's into
	// pkg/ts-bun-client before running npm).
	wantSubsteps := []struct {
		name    string
		cmdVerb string
	}{
		{"publish.push-branch",          "git"},
		{"publish.create-tag",           "git"},
		{"publish.gh-release",           "gh"},
		{"publish.npm-publish",          "cd"},
		{"publish.fast-forward-main",    "git"},
		{"publish.delete-remote-branch", "git"},
	}
	if len(report.PublishSubsteps) != len(wantSubsteps) {
		t.Fatalf("report.publish_substeps: want %d entries, got %d",
			len(wantSubsteps), len(report.PublishSubsteps))
	}
	for i, want := range wantSubsteps {
		sub := report.PublishSubsteps[i]
		if sub.Name != want.name {
			t.Errorf("publish_substeps[%d].name: want %q, got %q",
				i, want.name, sub.Name)
		}
		if sub.Outcome != "skipped" {
			t.Errorf("publish_substeps[%d] (%s).outcome: want %q, got %q",
				i, sub.Name, "skipped", sub.Outcome)
		}
		if sub.Command == "" {
			t.Errorf("publish_substeps[%d] (%s).command: must be non-empty", i, sub.Name)
		}
		if !strings.HasPrefix(sub.Command, want.cmdVerb) {
			t.Errorf("publish_substeps[%d] (%s).command: want prefix %q, got %q",
				i, sub.Name, want.cmdVerb, sub.Command)
		}
	}

	// ── 8. Assert diagnostics: empty ──────────────────────────────────────
	if len(report.Diagnostics) != 0 {
		t.Errorf("report.diagnostics: want empty slice, got %d entries",
			len(report.Diagnostics))
	}

	t.Log("release-postconditions positive: all assertions passed ✓")
}
