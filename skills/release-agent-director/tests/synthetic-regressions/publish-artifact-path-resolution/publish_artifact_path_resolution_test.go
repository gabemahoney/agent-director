// Package publishartifactpathresolution_test — synthetic-regression tests for
// b.mjd (npm-publish substep broke on a relative --tarball path, after the tag
// and GitHub Release were already public).
//
// # Background
//
// publish-orchestrator.sh runs a 6-substep publish pipeline. Substep 4
// (npm-publish) runs inside a subshell that cd's into pkg/ts-bun-client:
//
//	(cd "${WORKTREE_ROOT}/pkg/ts-bun-client" && npm publish "${TARBALL}")
//
// Before the fix, TARBALL was used verbatim, so a relative --tarball (the form
// the pack phase emits, e.g. dist/agent-director-X.Y.Z.tgz) resolved against
// pkg/ts-bun-client and npm publish failed at substep 4 — AFTER substeps 1-3
// (push-branch / create-tag / gh-release) had already taken irreversible,
// externally-visible effect.
//
// The fix (b.mjd):
//   - _resolve_abs_path(): resolves --tarball / --notes / --binaries[] to
//     absolute paths at parse time, while $PWD is still the caller's CWD.
//   - validate_publish_artifacts(): a preflight run BEFORE substep 1 that fails
//     loudly (exit 1, no substep run) if any publish-phase file input is not a
//     readable regular file. Runs in both --release and --dry-run mode.
//
// These tests re-anchor the failure class:
//
//   - TestRelativeTarballResolvesAgainstCallerCWD — a RELATIVE --tarball
//     pointing at a real file resolves to an absolute path against the caller's
//     CWD (fails before the fix, passes after).
//   - TestNonexistentArtifactFailsPreflightBeforeAnySubstep — a nonexistent
//     --tarball / --notes / --binaries path fails preflight before any
//     irreversible substep runs.
//
// # SLOW TEST
//
// Both tests invoke publish-orchestrator.sh (bash + jq, ≈1–2 s).
// Skipped in -short mode.
package publishartifactpathresolution_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testTarget is the synthetic version tag used for all invocations here.
const testTarget = "0.0.0-b-mjd"

// priorPhasesAllPassedJSON represents the five pre-publish phases all passing —
// required by --prior-phases so the publish phase reaches its substeps.
const priorPhasesAllPassedJSON = `[
  {"name":"preflight","outcome":"passed","sub_checks":[]},
  {"name":"coverage", "outcome":"passed","sub_checks":[]},
  {"name":"compile",  "outcome":"passed","sub_checks":[]},
  {"name":"pack",     "outcome":"passed","sub_checks":[]},
  {"name":"notes",    "outcome":"passed","sub_checks":[]}
]`

// ─── report JSON types (subset needed here) ───────────────────────────────────

type reportSubstep struct {
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
	Command string `json:"command"`
}

type reportDiagnostic struct {
	WhichSubstepFailed     string   `json:"which_substep_failed"`
	PriorSubstepsSucceeded []string `json:"prior_substeps_succeeded"`
}

type releaseReport struct {
	Mode            string             `json:"mode"`
	PublishSubsteps []reportSubstep    `json:"publish_substeps"`
	Diagnostics     []reportDiagnostic `json:"diagnostics"`
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// repoRoot walks up from the package working directory until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("repoRoot: os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("repoRoot: could not find go.mod walking up from %s", dir)
		}
		dir = parent
	}
}

// orchScript returns the absolute path to publish-orchestrator.sh.
func orchScript(root string) string {
	return filepath.Join(root, "skills", "release-agent-director",
		"gates", "publish", "publish-orchestrator.sh")
}

// headSHA resolves the repo HEAD, required by --bump-sha.
func headSHA(t *testing.T, root string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// parseReport reads and decodes release-report.json at path.
func parseReport(t *testing.T, path string) releaseReport {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("parseReport: read %s: %v", path, err)
	}
	var r releaseReport
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("parseReport: unmarshal: %v\ncontents:\n%s", err, data)
	}
	return r
}

// artifactSet creates a real tarball, notes file, and three binary files under
// dir, returning their basenames (relative to dir). All are readable regular
// files so preflight passes.
func artifactSet(t *testing.T, dir string) (tarball, notes string, binaries []string) {
	t.Helper()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("artifactSet: write %s: %v", name, err)
		}
	}
	tarball = "agent-director-0.0.0.tgz"
	notes = "release-notes.md"
	binaries = []string{"bin-linux", "bin-darwin", "bin-windows"}
	write(tarball, "fake tarball bytes")
	write(notes, "# notes\n")
	for _, b := range binaries {
		write(b, "fake binary")
	}
	return tarball, notes, binaries
}

// ─── TestRelativeTarballResolvesAgainstCallerCWD ──────────────────────────────
//
// b.mjd AC1/AC3: drive the script with a RELATIVE --tarball pointing at a real
// file, with the process CWD set to the directory that contains it. Assert the
// npm-publish substep's recorded command carries the ABSOLUTE resolved tarball
// path (cwd + "/" + relative), proving resolution happened against the caller's
// CWD rather than against pkg/ts-bun-client inside the subshell.
//
// Before the fix, TARBALL was used verbatim, so the recorded command was
// "cd pkg/ts-bun-client && npm publish <relative>" — the assertion below fails.
func TestRelativeTarballResolvesAgainstCallerCWD(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: invokes publish-orchestrator.sh")
	}

	root := repoRoot(t)
	sha := headSHA(t, root)

	// The caller's CWD holds the artifacts; --tarball etc. are passed RELATIVE
	// to it (the form the pack phase emits).
	workDir := t.TempDir()
	tarball, notes, binaries := artifactSet(t, workDir)

	reportDir := t.TempDir()
	reportPath := filepath.Join(reportDir, "release-report.json")

	phasesFile := filepath.Join(t.TempDir(), "prior-phases.json")
	if err := os.WriteFile(phasesFile, []byte(priorPhasesAllPassedJSON), 0o644); err != nil {
		t.Fatalf("write prior-phases.json: %v", err)
	}

	cmd := exec.Command("bash", orchScript(root),
		"--target", testTarget,
		"--bump-sha", sha,
		"--tarball", tarball,
		"--notes", notes,
		"--binaries", strings.Join(binaries, ","),
		"--dry-run",
		"--prior-phases", phasesFile,
	)
	// CWD is the artifact dir, so a RELATIVE tarball resolves against it.
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "RELEASE_REPORT_DIR="+reportDir)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("publish-orchestrator.sh --dry-run failed (exit %d):\n%s",
			cmd.ProcessState.ExitCode(), out)
	}
	t.Logf("publish-orchestrator.sh stdout+stderr:\n%s", out)

	report := parseReport(t, reportPath)
	if report.Mode != "dry-run" {
		t.Errorf("report.mode: want %q, got %q", "dry-run", report.Mode)
	}

	// The npm-publish substep must have been reached (preflight passed on the
	// resolved paths) and recorded with the ABSOLUTE resolved tarball path.
	wantAbs := filepath.Join(workDir, tarball)
	var npmSub *reportSubstep
	for i := range report.PublishSubsteps {
		if report.PublishSubsteps[i].Name == "publish.npm-publish" {
			npmSub = &report.PublishSubsteps[i]
			break
		}
	}
	if npmSub == nil {
		t.Fatalf("publish.npm-publish substep not found — preflight or an earlier "+
			"substep aborted the run; substeps=%+v", report.PublishSubsteps)
	}
	if !strings.Contains(npmSub.Command, wantAbs) {
		t.Errorf("npm-publish command must carry the resolved ABSOLUTE tarball path.\n"+
			"want substring: %s\ngot command:    %s\n"+
			"(before the b.mjd fix the relative path was used verbatim and would "+
			"resolve against pkg/ts-bun-client inside the subshell)",
			wantAbs, npmSub.Command)
	}
	// Guard: the bare relative form must NOT be what npm receives.
	if strings.Contains(npmSub.Command, "npm publish "+tarball) {
		t.Errorf("npm-publish command still uses the bare relative tarball %q — "+
			"resolution did not happen: %s", tarball, npmSub.Command)
	}

	// The gh-release substep display string interpolates the resolved ${NOTES}
	// and the space-joined resolved ${BINARY_PATHS[*]} — assert BOTH the notes
	// and every binary appear as ABSOLUTE paths, and that no bare relative form
	// leaks through. Before the b.mjd fix these were emitted verbatim (relative).
	wantAbsNotes := filepath.Join(workDir, notes)
	var ghSub *reportSubstep
	for i := range report.PublishSubsteps {
		if report.PublishSubsteps[i].Name == "publish.gh-release" {
			ghSub = &report.PublishSubsteps[i]
			break
		}
	}
	if ghSub == nil {
		t.Fatalf("publish.gh-release substep not found; substeps=%+v",
			report.PublishSubsteps)
	}
	if !strings.Contains(ghSub.Command, "--notes-file "+wantAbsNotes) {
		t.Errorf("gh-release command must carry the resolved ABSOLUTE notes path.\n"+
			"want substring: --notes-file %s\ngot command:    %s",
			wantAbsNotes, ghSub.Command)
	}
	// The bare relative --notes-file form must NOT survive resolution.
	if strings.Contains(ghSub.Command, "--notes-file "+notes) {
		t.Errorf("gh-release command still uses the bare relative notes path %q: %s",
			notes, ghSub.Command)
	}
	for _, b := range binaries {
		wantAbsBin := filepath.Join(workDir, b)
		if !strings.Contains(ghSub.Command, wantAbsBin) {
			t.Errorf("gh-release command must carry the resolved ABSOLUTE binary path.\n"+
				"want substring: %s\ngot command:    %s", wantAbsBin, ghSub.Command)
		}
		// A binary basename appended as a bare relative arg (space-delimited)
		// must not appear — resolution must have absolutized it.
		if strings.Contains(ghSub.Command, " "+b) {
			t.Errorf("gh-release command still carries the bare relative binary %q: %s",
				b, ghSub.Command)
		}
	}
}

// ─── TestNonexistentArtifactFailsPreflightBeforeAnySubstep ────────────────────
//
// b.mjd AC2/AC4: a nonexistent publish-phase artifact path must fail during
// preflight — exit 1, with which_substep_failed == publish.preflight-publish-
// artifacts and prior_substeps_succeeded == [] — so no irreversible substep
// (push-branch / create-tag / gh-release) can run first.
//
// Parameterized over each artifact input (--tarball, --notes, --binaries) so a
// single bad path in any of them halts preflight. The other two inputs point at
// real files, isolating the failure to the one under test.
func TestNonexistentArtifactFailsPreflightBeforeAnySubstep(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: invokes publish-orchestrator.sh")
	}

	root := repoRoot(t)
	sha := headSHA(t, root)

	cases := []struct {
		name       string
		mutateArgs func(tarball, notes, binaries string) (string, string, string)
	}{
		{
			name: "tarball",
			mutateArgs: func(_, notes, binaries string) (string, string, string) {
				return "/nonexistent/does-not-exist.tgz", notes, binaries
			},
		},
		{
			name: "notes",
			mutateArgs: func(tarball, _, binaries string) (string, string, string) {
				return tarball, "/nonexistent/does-not-exist.md", binaries
			},
		},
		{
			name: "binaries",
			mutateArgs: func(tarball, notes, binaries string) (string, string, string) {
				// One good binary, one bad — a single bad asset must halt.
				return tarball, notes, binaries + ",/nonexistent/missing-bin"
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Real files so the other two inputs pass preflight.
			workDir := t.TempDir()
			tarball, notes, binaries := artifactSet(t, workDir)
			absTarball := filepath.Join(workDir, tarball)
			absNotes := filepath.Join(workDir, notes)
			absBins := make([]string, len(binaries))
			for i, b := range binaries {
				absBins[i] = filepath.Join(workDir, b)
			}

			tArg, nArg, bArg := tc.mutateArgs(absTarball, absNotes, strings.Join(absBins, ","))

			reportDir := t.TempDir()
			reportPath := filepath.Join(reportDir, "release-report.json")

			// --release mode: proves the preflight halts BEFORE any real,
			// irreversible substep. push-branch would be substep 1 but must
			// never run — no live remote is contacted because preflight exits
			// first.
			cmd := exec.Command("bash", orchScript(root),
				"--target", testTarget,
				"--bump-sha", sha,
				"--tarball", tArg,
				"--notes", nArg,
				"--binaries", bArg,
				"--release",
			)
			cmd.Dir = workDir
			cmd.Env = append(os.Environ(), "RELEASE_REPORT_DIR="+reportDir)
			out, _ := cmd.CombinedOutput()
			exitCode := cmd.ProcessState.ExitCode()
			t.Logf("publish-orchestrator.sh --release (bad %s): exit %d\noutput:\n%s",
				tc.name, exitCode, out)

			if exitCode != 1 {
				t.Fatalf("want exit 1 from preflight failure, got %d", exitCode)
			}

			report := parseReport(t, reportPath)

			// The ONLY substep recorded is the failed preflight — no
			// irreversible substep ran.
			if len(report.PublishSubsteps) != 1 {
				t.Fatalf("want exactly 1 substep (the failed preflight), got %d: %+v",
					len(report.PublishSubsteps), report.PublishSubsteps)
			}
			sub := report.PublishSubsteps[0]
			if sub.Name != "publish.preflight-publish-artifacts" {
				t.Errorf("substep name: want %q, got %q",
					"publish.preflight-publish-artifacts", sub.Name)
			}
			if sub.Outcome != "failed" {
				t.Errorf("substep outcome: want %q, got %q", "failed", sub.Outcome)
			}

			if len(report.Diagnostics) != 1 {
				t.Fatalf("want 1 diagnostic, got %d", len(report.Diagnostics))
			}
			diag := report.Diagnostics[0]
			if diag.WhichSubstepFailed != "publish.preflight-publish-artifacts" {
				t.Errorf("which_substep_failed: want %q, got %q",
					"publish.preflight-publish-artifacts", diag.WhichSubstepFailed)
			}
			if len(diag.PriorSubstepsSucceeded) != 0 {
				t.Errorf("prior_substeps_succeeded: want [] (no substep ran before "+
					"preflight), got %v", diag.PriorSubstepsSucceeded)
			}
		})
	}
}
