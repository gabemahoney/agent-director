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

// gates/finalize/write-report.sh, relative to repoRoot. The staging helper
// copies this script at runtime so its own two-level ../.. DIST_DIR walk
// resolves inside an isolated tree (b.mgw / SR-2.3 / SR-8 — the script is
// copied byte-for-byte, never modified).
var writeReportScriptRel = filepath.Join(
	"skills", "release-agent-director", "gates", "finalize", "write-report.sh",
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

// reportPhase mirrors one entry in the "phases" array of release-report.json
// (SR-15 / SR-2.2 report phase shape: name, outcome, started_at, elapsed_ms,
// and a typed sub_checks[]).
type reportPhase struct {
	Name      string           `json:"name"`
	Outcome   string           `json:"outcome"`
	StartedAt string           `json:"started_at"`
	ElapsedMS int64            `json:"elapsed_ms"`
	SubChecks []reportSubCheck `json:"sub_checks"`
}

// reportSubCheck mirrors one entry in a phase's "sub_checks" array. Per the
// SR-2.2 report shape a sub-check carries name, outcome, and a scalar
// diagnostic that is the FIRST executor diagnostic object or null. Diagnostic
// is json.RawMessage so it tolerates an SR-14 object, an explicit null, or the
// key being absent (existing {"name","outcome"}-only fixture entries still
// unmarshal — Diagnostic stays nil).
type reportSubCheck struct {
	Name       string          `json:"name"`
	Outcome    string          `json:"outcome"`
	Diagnostic json.RawMessage `json:"diagnostic"`
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

// releaseReport mirrors the top-level structure of release-report.json as
// written by publish-orchestrator.sh (into RELEASE_REPORT_DIR).
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

// isolatedReportDir returns a per-test t.TempDir() and its release-report.json
// path. Passing RELEASE_REPORT_DIR=<dir> to publish-orchestrator.sh (b.aur)
// keeps the report out of the shared skills/release-agent-director/dist/, so
// concurrent tests never read each other's report. The Go runner auto-cleans
// t.TempDir(), so no explicit report deletion is needed.
func isolatedReportDir(t *testing.T) (dir, reportPath string) {
	t.Helper()
	dir = t.TempDir()
	return dir, filepath.Join(dir, "release-report.json")
}

// stageWriteReport stages a RUNTIME copy of gates/finalize/write-report.sh into
// an isolated tree and returns (stagedScript, reportPath). write-report.sh
// hardcodes DIST_DIR from its own location (two-level ../.. walk) and does not
// honor RELEASE_REPORT_DIR, and publish-orchestrator.sh never invokes it — so
// isolation is achieved by copying the script (unmodified — SR-8) to
// <isolated>/gates/finalize/write-report.sh, from which its ../.. walk resolves
// DIST_DIR to <isolated>/dist. The shared skills/release-agent-director/dist/
// is therefore never read or written. This is the single sanctioned place the
// report path is constructed (SR-7.2 — no path construction outside helpers).
func stageWriteReport(t *testing.T, root string) (stagedScript, reportPath string) {
	t.Helper()
	isolated := t.TempDir()
	finalizeDir := filepath.Join(isolated, "gates", "finalize")
	if err := os.MkdirAll(finalizeDir, 0o755); err != nil {
		t.Fatalf("stageWriteReport: mkdir %s: %v", finalizeDir, err)
	}
	src := filepath.Join(root, writeReportScriptRel)
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("stageWriteReport: read %s: %v", src, err)
	}
	stagedScript = filepath.Join(finalizeDir, "write-report.sh")
	if err := os.WriteFile(stagedScript, data, 0o755); err != nil {
		t.Fatalf("stageWriteReport: write %s: %v", stagedScript, err)
	}
	reportPath = filepath.Join(isolated, "dist", "release-report.json")
	return stagedScript, reportPath
}

// artifactSet creates a real tarball, notes file, and three binary files under
// dir, returning their absolute paths. All are readable regular files so the
// b.mjd validate_publish_artifacts preflight (which runs in --dry-run and
// --release before any substep) passes. Mirrors the same-named helper in the
// publish-artifact-path-resolution package; the tests tree has no shared
// testutil package (every synthetic-regression package is a self-contained
// _test package that re-declares its helpers), so this small duplication
// follows the established precedent.
func artifactSet(t *testing.T, dir string) (tarball, notes string, binaries []string) {
	t.Helper()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("artifactSet: write %s: %v", p, err)
		}
		return p
	}
	tarball = write("agent-director-0.0.0.tgz", "fake tarball bytes")
	notes = write("release-notes.md", "# notes\n")
	for _, b := range []string{"bin-linux", "bin-darwin", "bin-windows"} {
		binaries = append(binaries, write(b, "fake binary"))
	}
	return tarball, notes, binaries
}

// parseReport reads and JSON-decodes the release-report.json produced by the
// most recent publish-orchestrator.sh invocation from the given path.
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
