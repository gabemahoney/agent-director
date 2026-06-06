// Package releasepostconditions_test contains synthetic-regression tests that
// verify the postcondition contract of publish-orchestrator.sh (b.wvr / E9
// publish phase).
//
// # Test structure
//
//   - helpers_test.go  — shared JSON types, fixtures, and test helpers
//   - positive_test.go — [TestReleasePostconditionsPositive] dry-run all-pass
//   - negative_test.go — [TestReleasePostconditionsNegativePriorFailure] and
//     [TestReleasePostconditionsNegativeSimulateFailure]
//
// # SLOW TEST
//
// All tests invoke publish-orchestrator.sh (≈1–2 s due to jq calls).
// They are skipped in -short mode.
package releasepostconditions_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// testTarget is the synthetic version tag used for all postcondition test
// invocations.  It is deliberately unusual so artifacts are easy to identify
// if cleanup ever fails.
const testTarget = "0.0.0-postcondition"

// ─── prior-phases JSON fixtures ───────────────────────────────────────────────

// priorPhasesAllPassedJSON represents the five pre-publish phases all
// completing successfully.  Used by the positive test.
const priorPhasesAllPassedJSON = `[
  {"name":"preflight","outcome":"passed","sub_checks":[]},
  {"name":"coverage", "outcome":"passed","sub_checks":[]},
  {"name":"compile",  "outcome":"passed","sub_checks":[]},
  {"name":"pack",     "outcome":"passed","sub_checks":[]},
  {"name":"notes",    "outcome":"passed","sub_checks":[]}
]`

// priorPhasesWithFailureJSON is the same five phases but with coverage=failed.
// Used by the negative test to verify the report does NOT erase prior failures.
const priorPhasesWithFailureJSON = `[
  {"name":"preflight","outcome":"passed","sub_checks":[]},
  {"name":"coverage", "outcome":"failed","sub_checks":[{"name":"coverage.bun-test","outcome":"failed"}]},
  {"name":"compile",  "outcome":"passed","sub_checks":[]},
  {"name":"pack",     "outcome":"passed","sub_checks":[]},
  {"name":"notes",    "outcome":"passed","sub_checks":[]}
]`

// ─── report JSON types ────────────────────────────────────────────────────────

// reportPhase mirrors one entry in the "phases" array of release-report.json.
type reportPhase struct {
	Name      string            `json:"name"`
	Outcome   string            `json:"outcome"`
	SubChecks []json.RawMessage `json:"sub_checks"`
}

// reportSubstep mirrors one entry in the "publish_substeps" array.
type reportSubstep struct {
	Name            string `json:"name"`
	Outcome         string `json:"outcome"`
	Command         string `json:"command"`
	StartedAt       string `json:"started_at"`
	ResponseExcerpt string `json:"response_excerpt"`
}

// reportDiagnostic mirrors one entry in the "diagnostics" array.
type reportDiagnostic struct {
	Gate                     string          `json:"gate"`
	OffendingFileOrArtifact  json.RawMessage `json:"offending_file_or_artifact"`
	Description              string          `json:"description"`
	CorrectiveAction         string          `json:"corrective_action"`
	WhichSubstepFailed       string          `json:"which_substep_failed"`
	PriorSubstepsSucceeded   []string        `json:"prior_substeps_succeeded"`
	UpstreamResponseVerbatim string          `json:"upstream_response_verbatim"`
}

// releaseReport mirrors the top-level structure of
// skills/release-agent-director/dist/release-report.json as written by
// publish-orchestrator.sh.
type releaseReport struct {
	InvocationTimestamp string             `json:"invocation_timestamp"`
	Mode                string             `json:"mode"`
	BumpKind            string             `json:"bump_kind"`
	SourceVersion       string             `json:"source_version"`
	TargetVersion       string             `json:"target_version"`
	Phases              []reportPhase      `json:"phases"`
	PublishSubsteps     []reportSubstep    `json:"publish_substeps"`
	Diagnostics         []reportDiagnostic `json:"diagnostics"`
	ElapsedSeconds      int                `json:"elapsed_seconds"`
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
	panic("unreachable")
}

// orchReportPath returns the absolute path to the release-report.json written
// by publish-orchestrator.sh.  The report lives inside the skill's own dist/
// directory (skills/release-agent-director/dist/), NOT the repo-root dist/.
func orchReportPath(root string) string {
	return filepath.Join(root, "skills", "release-agent-director", "dist", "release-report.json")
}

// registerReportCleanup schedules deletion of dist/release-report.json when t
// completes.  Call this before every publish-orchestrator.sh invocation so the
// generated report does not linger between test runs.
func registerReportCleanup(t *testing.T, root string) {
	t.Helper()
	t.Cleanup(func() {
		if err := os.Remove(orchReportPath(root)); err != nil && !os.IsNotExist(err) {
			t.Errorf("registerReportCleanup: remove release-report.json: %v", err)
		}
	})
}

// parseReport reads and JSON-decodes the release-report.json produced by the
// most recent publish-orchestrator.sh invocation.
func parseReport(t *testing.T, root string) releaseReport {
	t.Helper()
	path := orchReportPath(root)
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
