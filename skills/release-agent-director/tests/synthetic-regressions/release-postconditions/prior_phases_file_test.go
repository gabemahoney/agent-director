// Package releasepostconditions_test — the publish orchestrator's
// --prior-phases file and its report write (b.2wr).
//
// # Background
//
// publish-orchestrator.sh passed the --prior-phases file's array to jq as one
// --argjson argument, which Linux caps at 128 KiB: a larger array (gate
// stderr_excerpt and diagnostics, which b.nsa lets through whole) left a 0-byte
// release-report.json behind an exit 0. A named file that was missing gave
// phases [] and a malformed one an empty report, both after the substeps ran.
// The file is now read once at startup, and a failed report write is reported
// as "release-report NOT written" instead of "written".
//
// # SLOW TEST
//
// Invokes publish-orchestrator.sh (bash + jq). Skipped in -short mode.
package releasepostconditions_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// orchestratorRun is the outcome of one publish-orchestrator.sh invocation.
type orchestratorRun struct {
	exit           int
	stdout, stderr string
	reportPath     string
}

// orchestratorSetup adjusts one publish-orchestrator.sh run; the zero value runs
// it from repoRoot with a writable report dir.
type orchestratorSetup struct {
	dir               string   // working directory and PWD; "" is repoRoot
	readOnlyReportDir bool     // report dir at mode 0555, so the report write fails
	env               []string // extra environment entries; later ones win
}

// runOrchestrator runs publish-orchestrator.sh with readable artifacts and an
// isolated report dir, args appended.
func runOrchestrator(t *testing.T, args ...string) orchestratorRun {
	t.Helper()
	return runOrchestratorWith(t, orchestratorSetup{}, args...)
}

// runOrchestratorWith is runOrchestrator adjusted by s.
func runOrchestratorWith(t *testing.T, s orchestratorSetup, args ...string) orchestratorRun {
	t.Helper()
	root := repoRoot(t)
	reportDir, reportPath := isolatedReportDir(t)
	if s.readOnlyReportDir {
		if err := os.Chmod(reportDir, 0o555); err != nil {
			t.Fatalf("chmod report dir: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(reportDir, 0o755) })
	}
	dir := s.dir
	if dir == "" {
		dir = root
	}
	tarball, notes, binaries := artifactSet(t, t.TempDir())
	cmd := exec.Command("bash", append([]string{
		filepath.Join(root, "skills", "release-agent-director", "gates", "publish", "publish-orchestrator.sh"),
		"--target", testTarget, "--bump-sha", "0000000",
		"--tarball", tarball, "--notes", notes, "--binaries", strings.Join(binaries, ","),
	}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "PWD="+dir, "RELEASE_REPORT_DIR="+reportDir), s.env...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("run publish-orchestrator.sh: %v", err)
		}
	}
	return orchestratorRun{cmd.ProcessState.ExitCode(), stdout.String(), stderr.String(), reportPath}
}

// TestPriorPhasesFileReachesReport (b.2wr): the report's phases is the
// --prior-phases array in full, over 128 KiB too, in a dry run and a failed
// release; without the flag it is [].
func TestPriorPhasesFileReachesReport(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: invokes publish-orchestrator.sh")
	}
	for _, tc := range []struct {
		name     string
		phases   []any // nil: no --prior-phases
		args     []string
		wantExit int
	}{
		{"dry_run_oversize", oversizePhases(), []string{"--dry-run"}, 0},
		{"failed_release_oversize", oversizePhases(),
			[]string{"--release", "--simulate-failure-at", "push-branch"}, 1},
		{"dry_run_without_prior_phases", nil, []string{"--dry-run"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args, want := tc.args, any([]any{})
			if tc.phases != nil {
				var path string
				path, want = writeJSONFile(t, tc.phases)
				args = append([]string{"--prior-phases", path}, args...)
			}
			run := runOrchestrator(t, args...)
			if run.exit != tc.wantExit {
				t.Fatalf("exit = %d, want %d\nstderr: %.2000s", run.exit, tc.wantExit, run.stderr)
			}
			if got := reportFields(t, run.reportPath)["phases"]; !reflect.DeepEqual(got, want) {
				t.Errorf("report phases differ from the --prior-phases array\n got %s\nwant %s",
					jsonExcerpt(got), jsonExcerpt(want))
			}
		})
	}
}

// TestPriorPhasesBadFileRefused (b.2wr): a named --prior-phases file that is
// not a readable regular file holding exactly one JSON array stops the run with
// exit 2 naming the path, before any substep and with no report.
func TestPriorPhasesBadFileRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: invokes publish-orchestrator.sh")
	}
	for name, badPath := range badArrayFiles {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := badPath(t)
			run := runOrchestrator(t, "--dry-run", "--prior-phases", path)
			if run.exit != 2 {
				t.Errorf("exit = %d, want 2", run.exit)
			}
			if !strings.Contains(run.stderr, "--prior-phases") || !strings.Contains(run.stderr, path) {
				t.Errorf("stderr does not name --prior-phases %s:\n%.2000s", path, run.stderr)
			}
			if strings.Contains(run.stdout, "would do") {
				t.Errorf("a substep ran before the refusal; stdout:\n%s", run.stdout)
			}
			assertNoReport(t, run.reportPath)
		})
	}
}

// TestPriorPhasesRelativePath (b.2wr): a relative --prior-phases resolves against
// the caller's working directory; a missing one exits 2 naming <cwd>/<path>.
func TestPriorPhasesRelativePath(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: invokes publish-orchestrator.sh")
	}
	t.Run("present", func(t *testing.T) {
		t.Parallel()
		path, want := writeJSONFile(t, json.RawMessage(priorPhasesWithFailureJSON))
		run := runOrchestratorWith(t, orchestratorSetup{dir: filepath.Dir(path)},
			"--dry-run", "--prior-phases", filepath.Base(path))
		if run.exit != 0 {
			t.Fatalf("exit = %d, want 0\nstderr: %.2000s", run.exit, run.stderr)
		}
		if got := reportFields(t, run.reportPath)["phases"]; !reflect.DeepEqual(got, want) {
			t.Errorf("report phases\n got %s\nwant %s", jsonExcerpt(got), jsonExcerpt(want))
		}
	})
	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		cwd := t.TempDir()
		run := runOrchestratorWith(t, orchestratorSetup{dir: cwd}, "--dry-run", "--prior-phases", "absent.json")
		if run.exit != 2 {
			t.Errorf("exit = %d, want 2", run.exit)
		}
		if want := filepath.Join(cwd, "absent.json"); !strings.Contains(run.stderr, want) {
			t.Errorf("stderr does not name %s:\n%.2000s", want, run.stderr)
		}
		assertNoReport(t, run.reportPath)
	})
}

// TestPriorPhasesReadOnce (b.2wr): a --prior-phases file deleted, emptied or
// rewritten while the substeps run changes neither the report nor the summary.
func TestPriorPhasesReadOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: invokes publish-orchestrator.sh")
	}
	for name, change := range map[string]string{
		"deleted":   `rm -f -- "$PRIOR_PHASES_PATH"`,
		"emptied":   `: > "$PRIOR_PHASES_PATH"`,
		"rewritten": `printf '%s' '[{"name":"rewritten","outcome":"passed","sub_checks":[]}]' > "$PRIOR_PHASES_PATH"`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path, want := writeJSONFile(t, json.RawMessage(priorPhasesWithFailureJSON))
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			// The fake git serves publish.push-branch, the one substep before the
			// simulated create-tag failure: it changes the file and succeeds.
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/usr/bin/env bash\n"+change+"\n"), 0o755); err != nil {
				t.Fatalf("write fake git: %v", err)
			}
			run := runOrchestratorWith(t, orchestratorSetup{env: []string{
				"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"PRIOR_PHASES_PATH=" + path,
			}}, "--prior-phases", path, "--release", "--simulate-failure-at", "create-tag")
			if run.exit != 1 {
				t.Fatalf("exit = %d, want 1\nstderr: %.2000s", run.exit, run.stderr)
			}
			if after, err := os.ReadFile(path); err == nil && string(after) == string(before) {
				t.Fatal("the fake git did not change the --prior-phases file during the run")
			}

			if got := reportFields(t, run.reportPath)["phases"]; !reflect.DeepEqual(got, want) {
				t.Errorf("report phases differ from the array read at startup\n got %s\nwant %s",
					jsonExcerpt(got), jsonExcerpt(want))
			}
			report := parseReport(t, run.reportPath)
			var substeps []string
			for _, s := range report.PublishSubsteps {
				substeps = append(substeps, s.Name+"="+s.Outcome)
			}
			if wantSub := []string{"publish.push-branch=succeeded", "publish.create-tag=failed"}; !reflect.DeepEqual(substeps, wantSub) {
				t.Errorf("publish_substeps = %v, want %v", substeps, wantSub)
			}
			if len(report.Diagnostics) != 1 || report.Diagnostics[0].WhichSubstepFailed != "publish.create-tag" {
				t.Errorf("diagnostics = %+v, want one for publish.create-tag", report.Diagnostics)
			}
			const summary = "  phases:    preflight=passed, coverage=failed, compile=passed, pack=passed, notes=passed\n"
			if !strings.Contains(run.stdout, summary) {
				t.Errorf("terminal summary lacks %q:\n%s", summary, run.stdout)
			}
		})
	}
}

// TestReleaseReportWriteFailure (b.2wr): a report that cannot be written is
// reported as "release-report NOT written", never as written, and none is left.
func TestReleaseReportWriteFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: invokes publish-orchestrator.sh")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode, so the report write cannot be made to fail")
	}
	for name, args := range map[string][]string{
		"dry_run":        {"--dry-run"},
		"failed_release": {"--release", "--simulate-failure-at", "push-branch"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run := runOrchestratorWith(t, orchestratorSetup{readOnlyReportDir: true}, args...)
			if !strings.Contains(run.stderr, "release-report NOT written") {
				t.Errorf("stderr lacks %q:\n%.2000s", "release-report NOT written", run.stderr)
			}
			if strings.Contains(run.stderr, "release-report written") {
				t.Errorf("stderr claims the report was written:\n%.2000s", run.stderr)
			}
			assertNoReport(t, run.reportPath)
		})
	}
}
