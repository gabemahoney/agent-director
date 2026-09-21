// toyconfig_test.go — the reusable toy gates-config fixture mechanism (SR-7.3).
//
// It builds trivial, seconds-scale gates-config documents that drive
// run-parallel.sh directly, so the executor's semantics can be proven without
// running any real coverage gate (SR-7.5). Three composable variants are
// provided: all-passing, single-failure-among-passing-siblings, and
// multi-failure (>=2 failing gates).
//
// The deliberately-failing gate commands ITSELF emit a well-formed SR-14 JSON
// diagnostic (one JSON object per line on stderr) with assertable content, so
// the single-failure semantics test can assert on both the failing gate's
// outcome and its diagnostic content in one gate (PM-required: the failing gate
// and the diagnostic-emitting gate are the same gate). A passing gate also
// emits a diagnostic, proving aggregation is independent of outcome.
//
// ─────────────────────────────────────────────────────────────────────────────
// CROSS-PACKAGE REUSE ANCHOR (SR-7.4, for Epic t1.2mt.wc / release-postconditions)
// ─────────────────────────────────────────────────────────────────────────────
// Go test helpers are NOT importable across packages, so reuse is a
// PATH-ADDRESSABLE artifact plus this documented pattern — not shared Go
// functions.
//
//	The SR-2.3 report-shape check in the release-postconditions package needs
//	consolidated executor output produced by this same toy mechanism. To obtain
//	it WITHOUT importing this package's Go code, that test:
//
//	  1. Resolves repoRoot via its own walk-up helper (same convention as here).
//	  2. Materializes a gates-config with the schema documented below into its
//	     own t.TempDir(): {phase_name, gates[]{name,command,cwd}, max_parallel}.
//	     The trivial toy gate commands in this file (a bare `true`, and a
//	     `printf <SR-14-json> >&2; exit 1`) are copy-stable and dependency-free;
//	     the release-postconditions test re-materializes the same shapes (see
//	     writeToyConfig, which is the single materialize seam).
//	  3. Invokes run-parallel.sh at
//	     skills/release-agent-director/gates/lib/run-parallel.sh (resolved from
//	     repoRoot) with the config path — the only interface run-parallel.sh
//	     exposes; there is no test-only seam.
//	  4. Parses stdout into the SR-2.1 consolidated shape (phase_name, outcome,
//	     sub_checks[]{name,outcome,duration_ms,exit_code,stderr_excerpt,
//	     diagnostics}).
//
//	The stable, path-addressable parts of the mechanism are therefore: (a) the
//	config schema, (b) the run-parallel.sh path under repoRoot, and (c) the toy
//	command snippets below. A consumer package reproduces steps 1-4 with its own
//	local helpers; nothing here needs to be exported.
// ─────────────────────────────────────────────────────────────────────────────

package coverageparallelphase_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ─── toy phase + gate names (declare-once, SR-7.3) ────────────────────────────

const toyPhaseName = "coverage-toy"

const (
	gatePass     = "coverage.toy-pass"      // always passes, no diagnostic
	gatePassDiag = "coverage.toy-pass-diag" // passes but emits a diagnostic
	gateFail     = "coverage.toy-fail"      // fails, emits its own SR-14 diagnostic
	gateFailAlt  = "coverage.toy-fail-alt"  // second distinct failing gate (multi-failure)
	gateSibling  = "coverage.toy-sibling"   // passing sibling in the failure variants
)

// ─── SR-14 diagnostic field values emitted by the toy failing gates ───────────
//
// Declared once so the semantics tests assert on the SAME literal the toy
// command emits. Each failing gate carries a DISTINCT description/artifact so
// the multi-failure no-masking assertions can tell the two apart.

const (
	failDiagArtifact = "toy-artifact-fail.txt"
	failDiagDesc     = "toy-fail gate deliberately failed for executor semantics coverage"
	failDiagAction   = "This is a synthetic toy gate; no action needed."

	failAltDiagArtifact = "toy-artifact-fail-alt.txt"
	failAltDiagDesc     = "toy-fail-alt gate deliberately failed with a distinct diagnostic"
	failAltDiagAction   = "This is a synthetic toy gate; no action needed."

	passDiagArtifact = "toy-artifact-pass.txt"
	passDiagDesc     = "toy-pass-diag emitted a diagnostic despite passing (aggregation is outcome-independent)"
	passDiagAction   = "Informational only; the gate passed."
)

// ─── gates-config schema (mirrors run-parallel.sh INPUT contract) ─────────────

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

// ─── toy command builders ─────────────────────────────────────────────────────

// passCmd is a trivial always-passing command with no diagnostic.
func passCmd() string { return "true" }

// diagCmd builds a shell snippet that emits a single well-formed SR-14 JSON
// object on stderr (one object per line, per the gate contract) then exits with
// the given code. Using jq -nc guarantees valid JSON regardless of the field
// values, and keeps every emitted key in one place.
func diagCmd(gate, artifact, description, action string, exitCode int) string {
	obj := sr14Diagnostic{
		Gate:                    gate,
		OffendingFileOrArtifact: artifact,
		Description:             description,
		CorrectiveAction:        action,
	}
	b, err := json.Marshal(obj)
	if err != nil {
		panic("diagCmd: marshal SR-14 object: " + err.Error())
	}
	// Single-quote the JSON for the shell; the JSON never contains single quotes
	// given the literals above. printf ... >&2 puts it on stderr as one line.
	return "printf '%s\\n' '" + string(b) + "' >&2; exit " + itoa(exitCode)
}

// itoa avoids pulling strconv into the fixture surface for a single small int.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ─── variant builders ─────────────────────────────────────────────────────────
//
// Every gate's cwd is "." — valid on any healthy tree once the config is
// materialized into t.TempDir() (SR-1.3: all configured cwd values must exist).

// allPassingConfig: two always-passing gates, one of which emits a diagnostic
// to prove aggregation is outcome-independent.
func allPassingConfig() toyConfig {
	return toyConfig{
		PhaseName: toyPhaseName,
		Gates: []toyGate{
			{Name: gatePass, Command: passCmd(), Cwd: "."},
			{Name: gatePassDiag, Command: diagCmd(gatePassDiag, passDiagArtifact, passDiagDesc, passDiagAction, 0), Cwd: "."},
		},
		MaxParallel: 2,
	}
}

// singleFailureConfig: one failing gate that emits its own SR-14 diagnostic,
// among two passing siblings.
func singleFailureConfig() toyConfig {
	return toyConfig{
		PhaseName: toyPhaseName,
		Gates: []toyGate{
			{Name: gatePass, Command: passCmd(), Cwd: "."},
			{Name: gateFail, Command: diagCmd(gateFail, failDiagArtifact, failDiagDesc, failDiagAction, 1), Cwd: "."},
			{Name: gateSibling, Command: passCmd(), Cwd: "."},
		},
		MaxParallel: 3,
	}
}

// multiFailureConfig: two distinct failing gates, EACH emitting its own
// distinct SR-14 diagnostic, plus a passing sibling — so no-masking assertions
// are possible.
func multiFailureConfig() toyConfig {
	return toyConfig{
		PhaseName: toyPhaseName,
		Gates: []toyGate{
			{Name: gateFail, Command: diagCmd(gateFail, failDiagArtifact, failDiagDesc, failDiagAction, 1), Cwd: "."},
			{Name: gateFailAlt, Command: diagCmd(gateFailAlt, failAltDiagArtifact, failAltDiagDesc, failAltDiagAction, 1), Cwd: "."},
			{Name: gateSibling, Command: passCmd(), Cwd: "."},
		},
		MaxParallel: 3,
	}
}

// ─── materialize seam ─────────────────────────────────────────────────────────

// writeToyConfig materializes a variant into a fresh gates-config.json inside
// t.TempDir() and returns its absolute path. This is the single materialize
// seam referenced by the cross-package reuse anchor above; a consumer package
// reproduces exactly this step (marshal the schema, write to its own TempDir).
func writeToyConfig(t *testing.T, cfg toyConfig) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gates-config.json")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("writeToyConfig: marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writeToyConfig: write %s: %v", path, err)
	}
	return path
}
