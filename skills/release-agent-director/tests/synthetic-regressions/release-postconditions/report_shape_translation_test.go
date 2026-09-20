// Package releasepostconditions_test — SR-2.3 end-to-end report-shape
// translation check (b.mgw / Epic t1.2mt.wc).
//
// # Background
//
// The coverage phase runs its gates through gates/lib/run-parallel.sh, whose
// consolidated output shape differs from the report phase shape that
// finalize/write-report.sh consumes. write-report.sh validates only that the
// phases value is a JSON array — the LLM orchestrator performs the translation
// at release time. This test is the only automated proof that the
// README-documented mapping (gates/README.md "Field-mapping: executor output →
// report phase object") actually produces a well-formed report when applied to
// REAL executor output.
//
// The pipeline under test:
//
//  1. Materialize a cheap toy gates-config (per the coverage-parallel-phase
//     REUSE.md path-addressable pattern — Go helpers are not importable
//     cross-package, so the shapes are re-materialized here).
//  2. Run run-parallel.sh DIRECTLY against it to obtain real consolidated
//     output (tolerating the deliberately-failing toy gate's non-zero exit).
//  3. Apply a codified copy of the README mapping table in test code.
//  4. Feed the translated phases (and any spilled diagnostics) to a RUNTIME
//     copy of write-report.sh staged in an isolated tree (stageWriteReport).
//  5. Parse the produced report with parseReport and assert phase/sub-check
//     values DERIVED from the executor run (SR-2.3 — not mere key presence).
//
// # SLOW TEST
//
// Invokes run-parallel.sh and write-report.sh (bash + jq subprocesses).
// Skipped in -short mode.
package releasepostconditions_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// ─── toy gates-config (re-materialized per coverage-parallel-phase REUSE.md) ──
//
// Go test helpers are not importable across packages; the toy command snippets
// (a bare `true`; a pair of `printf <SR-14-json> >&2` lines then `exit 1`) are
// copy-stable and dependency-free, so the singleFailureConfig shape is
// reproduced here with local builders. One failing gate emits TWO distinct
// SR-14 diagnostics (exercises the first-or-null rule with a REAL object AND
// the remainder-spill branch — diagnostics[1:] → top-level diagnostics[])
// among two passing siblings (exercises the empty-diagnostics → null branch).
// Declare-once literals (SR-7.3): the field values the toy command emits are
// the same constants the assertions check.

const toyPhaseName = "coverage-toy"

const (
	gatePass    = "coverage.toy-pass"
	gateFail    = "coverage.toy-fail"
	gateSibling = "coverage.toy-sibling"
)

// SR-14 diagnostic field values emitted by the failing toy gate. Declared once
// so the assertions check the SAME literals the toy command emits on stderr.
// The failing gate emits TWO distinct SR-14 lines: the first maps to the
// sub-check's scalar diagnostic, and the second (the "spill") exercises the
// README remainder rule — diagnostics[1:] append to the report's top-level
// diagnostics[]. The two objects carry distinct content so the assertions can
// tell which landed where.
const (
	failDiagArtifact = "toy-artifact-fail.txt"
	failDiagDesc     = "toy-fail gate deliberately failed for executor semantics coverage"
	failDiagAction   = "This is a synthetic toy gate; no action needed."
)

// Second (spilled) SR-14 diagnostic from the failing toy gate — distinct
// content so the top-level diagnostics[] assertion proves the remainder, not
// the first object, spilled.
const (
	spillDiagArtifact = "toy-artifact-spill.txt"
	spillDiagDesc     = "toy-fail gate second diagnostic — exercises the remainder-spill branch"
	spillDiagAction   = "Synthetic spill diagnostic; no action needed."
)

// gates-config schema mirrors run-parallel.sh's INPUT contract.
type toyGate struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
}

type toyConfig struct {
	PhaseName   string    `json:"phase_name"`
	Gates       []toyGate `json:"gates"`
	MaxParallel int       `json:"max_parallel"`
}

// sr14Diag is one SR-14 diagnostic object emitted by a toy gate.
type sr14Diag struct {
	gate, artifact, description, action string
}

// diagJSON marshals one SR-14 diagnostic to its on-the-wire JSON. jq is not
// available at build time, so the object is marshaled in Go.
func diagJSON(t *testing.T, d sr14Diag) string {
	t.Helper()
	obj := map[string]string{
		"gate":                       d.gate,
		"offending_file_or_artifact": d.artifact,
		"description":                d.description,
		"corrective_action":          d.action,
	}
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("diagJSON: marshal SR-14 object: %v", err)
	}
	return string(b)
}

// diagCmd builds a shell snippet emitting one SR-14 JSON object per diag on
// stderr (one object per line, per the gate contract — run-parallel.sh
// collects every `^{`-prefixed stderr line into the gate's diagnostics array,
// preserving order) then exiting with code. The marshaled objects are
// single-quoted for the shell (the literals above contain no single quotes).
func diagCmd(t *testing.T, exitCode int, diags ...sr14Diag) string {
	t.Helper()
	snippet := ""
	for _, d := range diags {
		snippet += "printf '%s\\n' '" + diagJSON(t, d) + "' >&2; "
	}
	code := "0"
	if exitCode == 1 {
		code = "1"
	}
	return snippet + "exit " + code
}

// singleFailureConfig: one failing gate emitting TWO distinct SR-14
// diagnostics (the first maps to the sub-check scalar diagnostic, the second
// spills to the report's top-level diagnostics[]), among two passing siblings.
func singleFailureConfig(t *testing.T) toyConfig {
	t.Helper()
	// Prepend a short sleep so the failing gate has a guaranteed-measurable
	// duration_ms: the executor sums per-sub-check duration_ms into elapsed_ms,
	// and the downstream wantElapsed>0 guard would hard-fail if every gate ran in
	// under a millisecond (the two passing `true` siblings can). 50ms keeps the
	// total comfortably above the millisecond floor without materially slowing the
	// test.
	failCmd := "sleep 0.05; " + diagCmd(t, 1,
		sr14Diag{gateFail, failDiagArtifact, failDiagDesc, failDiagAction},
		sr14Diag{gateFail, spillDiagArtifact, spillDiagDesc, spillDiagAction},
	)
	return toyConfig{
		PhaseName: toyPhaseName,
		Gates: []toyGate{
			{Name: gatePass, Command: "true", Cwd: "."},
			{Name: gateFail, Command: failCmd, Cwd: "."},
			{Name: gateSibling, Command: "true", Cwd: "."},
		},
		MaxParallel: 3,
	}
}

// writeToyConfig materializes a variant into a gates-config.json inside
// t.TempDir() and returns its path (the REUSE.md materialize seam, reproduced).
func writeToyConfig(t *testing.T, cfg toyConfig) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gates-config.json")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("writeToyConfig: marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writeToyConfig: write %s: %v", path, err)
	}
	return path
}

// ─── executor consolidated-output shape (SR-2.1) ──────────────────────────────
//
// The subset of run-parallel.sh's stdout that the mapping consumes. Fields the
// mapping ignores (exit_code, stderr_excerpt) are omitted.

type execConsolidated struct {
	PhaseName string         `json:"phase_name"`
	Outcome   string         `json:"outcome"`
	SubChecks []execSubCheck `json:"sub_checks"`
}

type execSubCheck struct {
	Name        string            `json:"name"`
	Outcome     string            `json:"outcome"`
	DurationMS  int64             `json:"duration_ms"`
	Diagnostics []json.RawMessage `json:"diagnostics"`
}

// ─── codified README mapping ──────────────────────────────────────────────────
//
// A verbatim copy of gates/README.md "Field-mapping: executor output → report
// phase object" applied in Go, so the report the LLM would produce at release
// time is reproduced here and fed through the real write-report.sh:
//
//	phase_name            → phase name        (direct copy)
//	duration_ms           → phase elapsed_ms  (per-sub-check durations summed
//	                                            into the phase total — the
//	                                            executor emits duration_ms only
//	                                            per sub-check)
//	per-sub-check name/outcome                 (carried over unchanged)
//	per-sub-check diagnostics[0] or null → sub-check scalar diagnostic
//	per-sub-check diagnostics[1:]        → appended to top-level diagnostics[]
//	(not emitted)          → started_at        (test-captured UTC ISO-8601)

// translate applies the README mapping to one consolidated executor output,
// emitting the shared reportPhase/reportSubCheck types (helpers_test.go) so the
// same structs are used to build the phases array AND to assert the report the
// staged write-report.sh writes back. It returns the report phase object plus
// any remainder diagnostics that spill to the report's top-level diagnostics[]
// (README first-or-null rule). A nil Diagnostic (empty executor diagnostics)
// marshals as JSON null, which write-report.sh passes through verbatim.
func translate(exec execConsolidated, startedAt string) (reportPhase, []json.RawMessage) {
	phase := reportPhase{
		Name:      exec.PhaseName, // phase_name → name
		Outcome:   exec.Outcome,
		StartedAt: startedAt, // test-captured UTC ISO-8601
	}
	var spill []json.RawMessage
	for _, sc := range exec.SubChecks {
		phase.ElapsedMS += sc.DurationMS // duration_ms → elapsed_ms (summed)
		out := reportSubCheck{
			Name:    sc.Name,    // name carried over
			Outcome: sc.Outcome, // outcome carried over
		}
		if len(sc.Diagnostics) > 0 {
			out.Diagnostic = sc.Diagnostics[0]           // FIRST diagnostic object
			spill = append(spill, sc.Diagnostics[1:]...) // remainder → top-level
		}
		// else: Diagnostic stays nil → marshals as JSON null.
		phase.SubChecks = append(phase.SubChecks, out)
	}
	return phase, spill
}

// TestReportShapeTranslationEndToEnd runs the full executor → mapping →
// write-report.sh pipeline on real executor output and asserts the report
// carries values derived from that output (SR-2.3).
func TestReportShapeTranslationEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: invokes run-parallel.sh and write-report.sh (bash+jq)")
	}

	root := repoRoot(t)

	// ── 1. Materialize the toy config and run the real executor ────────────
	cfgPath := writeToyConfig(t, singleFailureConfig(t))
	runParallel := filepath.Join(root, "skills", "release-agent-director",
		"gates", "lib", "run-parallel.sh")

	// Capture started_at exactly as the README prescribes (UTC ISO-8601),
	// immediately before invoking the phase runner.
	startedAt := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	cmd := exec.Command("bash", runParallel, cfgPath)
	cmd.Dir = root
	// HOME redirect (b.93m, SR-5.1) — matches every other executor invocation in
	// the bee: the gate subprocesses resolve ~/.agent-director from HOME, so an
	// isolated per-test HOME keeps any nested emitter from writing ad-trail.jsonl
	// into the real home and racing the trail-leak canary.
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	stdout, runErr := cmd.Output()
	// The toy fail gate makes the executor exit 1 — expected, not a test error.
	if runErr != nil {
		if _, ok := runErr.(*exec.ExitError); !ok {
			t.Fatalf("run-parallel.sh: %v", runErr)
		}
	}

	var consolidated execConsolidated
	if err := json.Unmarshal(stdout, &consolidated); err != nil {
		t.Fatalf("parse executor output: %v\nstdout:\n%s", err, stdout)
	}

	// ── 2. Apply the codified README mapping ───────────────────────────────
	phase, spill := translate(consolidated, startedAt)
	phasesJSON, err := json.Marshal([]reportPhase{phase})
	if err != nil {
		t.Fatalf("marshal phases: %v", err)
	}
	spillArr := spill
	if spillArr == nil {
		spillArr = []json.RawMessage{}
	}
	diagJSON, err := json.Marshal(spillArr)
	if err != nil {
		t.Fatalf("marshal diagnostics: %v", err)
	}

	// ── 3. Feed the translated report through the staged write-report.sh ───
	stagedScript, reportPath := stageWriteReport(t, root)
	writeCmd := exec.Command("bash", stagedScript,
		startedAt, // invocation_timestamp
		"dry-run", // mode
		"patch",   // bump_kind
		"0.0.0",   // source_version
		"0.0.1",   // target_version
		string(phasesJSON),
		string(diagJSON),
		"1", // elapsed_seconds
	)
	if out, err := writeCmd.CombinedOutput(); err != nil {
		t.Fatalf("staged write-report.sh failed: %v\noutput:\n%s", err, out)
	}

	// ── 4. Parse and assert values DERIVED from the executor run ───────────
	report := parseReport(t, reportPath)

	if len(report.Phases) != 1 {
		t.Fatalf("report.phases: want 1 entry, got %d", len(report.Phases))
	}
	got := report.Phases[0]

	// phase_name → name.
	if got.Name != toyPhaseName {
		t.Errorf("phase.name: want %q (executor phase_name), got %q", toyPhaseName, got.Name)
	}
	// overall outcome is failed (one gate failed) — derived, not fabricated.
	if got.Outcome != "failed" {
		t.Errorf("phase.outcome: want %q, got %q", "failed", got.Outcome)
	}
	// started_at is the test-captured UTC timestamp, carried through verbatim.
	if got.StartedAt != startedAt {
		t.Errorf("phase.started_at: want %q, got %q", startedAt, got.StartedAt)
	}
	// elapsed_ms derives from summing the executor's per-sub-check duration_ms.
	var wantElapsed int64
	for _, sc := range consolidated.SubChecks {
		wantElapsed += sc.DurationMS
	}
	// Guard against a vacuous elapsed_ms assertion: if every gate ran in under a
	// millisecond the executor durations sum to 0, and `got.ElapsedMS == 0` would
	// pass trivially without proving the sum was carried through. Fail loudly so
	// the assertion below only runs when there is a non-zero total to compare.
	if wantElapsed <= 0 {
		t.Fatalf("executor per-sub-check duration_ms summed to %d; expected a "+
			"positive total so the elapsed_ms assertion is not vacuously satisfied "+
			"by sub-millisecond gate runs", wantElapsed)
	}
	if got.ElapsedMS != wantElapsed {
		t.Errorf("phase.elapsed_ms: want %d (sum of executor duration_ms), got %d",
			wantElapsed, got.ElapsedMS)
	}

	// Sub-checks: name/outcome carried over; diagnostic derived from the
	// executor's diagnostics array (first-or-null rule).
	if len(got.SubChecks) != len(consolidated.SubChecks) {
		t.Fatalf("phase.sub_checks: want %d entries, got %d",
			len(consolidated.SubChecks), len(got.SubChecks))
	}
	var sawFailingWithDiagnostic, sawPassingWithNull bool
	for i, sc := range got.SubChecks {
		src := consolidated.SubChecks[i]
		if sc.Name != src.Name {
			t.Errorf("sub_checks[%d].name: want %q, got %q", i, src.Name, sc.Name)
		}
		if sc.Outcome != src.Outcome {
			t.Errorf("sub_checks[%d] (%s).outcome: want %q, got %q",
				i, sc.Name, src.Outcome, sc.Outcome)
		}

		diagIsNull := len(sc.Diagnostic) == 0 || string(sc.Diagnostic) == "null"
		if len(src.Diagnostics) == 0 {
			// Empty executor diagnostics → scalar diagnostic must be null.
			if !diagIsNull {
				t.Errorf("sub_checks[%d] (%s).diagnostic: want null (empty executor "+
					"diagnostics), got %s", i, sc.Name, sc.Diagnostic)
			}
			if sc.Outcome == "passed" {
				sawPassingWithNull = true
			}
			continue
		}
		// Non-empty executor diagnostics → scalar diagnostic is the FIRST
		// object, with its content matching the toy gate's emitted SR-14 fields.
		if diagIsNull {
			t.Errorf("sub_checks[%d] (%s).diagnostic: want the first executor "+
				"diagnostic, got null", i, sc.Name)
			continue
		}
		var diag reportDiagnostic
		if err := json.Unmarshal(sc.Diagnostic, &diag); err != nil {
			t.Errorf("sub_checks[%d] (%s).diagnostic: unmarshal: %v", i, sc.Name, err)
			continue
		}
		if sc.Name == gateFail {
			if diag.Gate != gateFail {
				t.Errorf("failing sub-check diagnostic.gate: want %q, got %q", gateFail, diag.Gate)
			}
			if diag.Description != failDiagDesc {
				t.Errorf("failing sub-check diagnostic.description: want %q, got %q",
					failDiagDesc, diag.Description)
			}
			if diag.CorrectiveAction != failDiagAction {
				t.Errorf("failing sub-check diagnostic.corrective_action: want %q, got %q",
					failDiagAction, diag.CorrectiveAction)
			}
			var artifact string
			if err := json.Unmarshal(diag.OffendingFileOrArtifact, &artifact); err != nil {
				t.Errorf("failing sub-check diagnostic.offending_file_or_artifact: unmarshal: %v", err)
			} else if artifact != failDiagArtifact {
				t.Errorf("failing sub-check diagnostic.offending_file_or_artifact: want %q, got %q",
					failDiagArtifact, artifact)
			}
			sawFailingWithDiagnostic = true
		}
	}

	if !sawFailingWithDiagnostic {
		t.Error("expected the failing toy gate's sub-check to carry the mapped SR-14 diagnostic")
	}
	if !sawPassingWithNull {
		t.Error("expected at least one passing sub-check to map to a null diagnostic")
	}

	// Remainder-spill branch: the failing gate emits TWO SR-14 diagnostics. The
	// FIRST maps to the sub-check's scalar diagnostic (asserted above via the
	// failDiag* literals); the SECOND spills to the report's top-level
	// diagnostics[]. Assert exactly that one spilled diagnostic is present and
	// carries the DISTINCT spill content (proving the remainder — not the first
	// object — landed here).
	if len(report.Diagnostics) != 1 {
		t.Fatalf("report.diagnostics: want exactly 1 spilled diagnostic "+
			"(the failing gate's second SR-14 line), got %d", len(report.Diagnostics))
	}
	spilled := report.Diagnostics[0]
	if spilled.Gate != gateFail {
		t.Errorf("spilled diagnostic.gate: want %q, got %q", gateFail, spilled.Gate)
	}
	if spilled.Description != spillDiagDesc {
		t.Errorf("spilled diagnostic.description: want %q (the SECOND SR-14 line), got %q",
			spillDiagDesc, spilled.Description)
	}
	if spilled.CorrectiveAction != spillDiagAction {
		t.Errorf("spilled diagnostic.corrective_action: want %q, got %q",
			spillDiagAction, spilled.CorrectiveAction)
	}
	var spilledArtifact string
	if err := json.Unmarshal(spilled.OffendingFileOrArtifact, &spilledArtifact); err != nil {
		t.Errorf("spilled diagnostic.offending_file_or_artifact: unmarshal: %v", err)
	} else if spilledArtifact != spillDiagArtifact {
		t.Errorf("spilled diagnostic.offending_file_or_artifact: want %q, got %q",
			spillDiagArtifact, spilledArtifact)
	}

	t.Log("report-shape translation end-to-end: values derived from real executor output ✓")
}
