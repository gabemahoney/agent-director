// multi_failure_test.go — SR-7.5 multi-failure no-masking test.
//
// When two or more toy gates fail concurrently, run-parallel.sh must surface
// every failure independently in the consolidated output — no failure masks
// another — because the executor waits on all gates and aggregates per-gate
// results (SR-1.3, SR-7.5). This drives the UNMODIFIED run-parallel.sh (SR-1.1)
// directly with multiFailureConfig (two distinct failing gates, each emitting
// its own distinct SR-14 diagnostic, plus a passing sibling) from Task A.
//
// PM-required: the two failures are distinguished by each gate's own DISTINCT
// SR-14 diagnostic CONTENT (parsed field values), not merely by exit code —
// distinct exit codes are asserted additionally, never as a substitute.
// Removing either failing gate's entry from the output would fail this test.

package coverageparallelphase_test

import "testing"

func TestMultiFailureNoMasking(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: spawns run-parallel.sh subprocess (jq-heavy, ~1s)")
	}

	configPath := writeToyConfig(t, multiFailureConfig())
	res := runExecutor(t, configPath)

	// The executor exits 1 when any gate fails (SR-1.3 exit-code contract).
	if res.exitCode != 1 {
		t.Fatalf("multi-failure toy config: expected exit 1, got %d\nstdout:\n%s\nstderr:\n%s",
			res.exitCode, res.stdout, res.stderr)
	}

	out := parseConsolidated(t, res.stdout)

	// Top-level outcome is failed.
	if out.Outcome != outcomeFailed {
		t.Errorf("outcome = %q, want %q", out.Outcome, outcomeFailed)
	}

	// All three configured gates must appear — completion despite multiple
	// sibling failures (nothing dropped by an early failure).
	if len(out.SubChecks) != 3 {
		t.Fatalf("expected 3 sub_checks, got %d: %+v", len(out.SubChecks), out.SubChecks)
	}
	for _, name := range []string{gateFail, gateFailAlt, gateSibling} {
		subCheckByName(t, out, name) // fails the test if the gate is absent
	}

	// Both failing gates must each report their own failed outcome and their
	// own nonzero exit code (exit codes asserted ADDITIONALLY, per PM).
	failGate := subCheckByName(t, out, gateFail)
	if failGate.Outcome != outcomeFailed {
		t.Errorf("gate %q outcome = %q, want %q", gateFail, failGate.Outcome, outcomeFailed)
	}
	if failGate.ExitCode == 0 {
		t.Errorf("gate %q exit_code = %d, want nonzero", gateFail, failGate.ExitCode)
	}

	failAltGate := subCheckByName(t, out, gateFailAlt)
	if failAltGate.Outcome != outcomeFailed {
		t.Errorf("gate %q outcome = %q, want %q", gateFailAlt, failAltGate.Outcome, outcomeFailed)
	}
	if failAltGate.ExitCode == 0 {
		t.Errorf("gate %q exit_code = %d, want nonzero", gateFailAlt, failAltGate.ExitCode)
	}

	// PM-REQUIRED no-masking core: each failing gate carries its OWN distinct
	// SR-14 diagnostic, distinguished by parsed CONTENT. Removing either gate's
	// entry — or letting one failure's diagnostic stand in for the other —
	// would fail these assertions.
	failDiag := singleDiagnostic(t, failGate)
	if failDiag.OffendingFileOrArtifact != failDiagArtifact {
		t.Errorf("gate %q diagnostic artifact = %q, want %q",
			gateFail, failDiag.OffendingFileOrArtifact, failDiagArtifact)
	}
	if failDiag.Description != failDiagDesc {
		t.Errorf("gate %q diagnostic description = %q, want %q",
			gateFail, failDiag.Description, failDiagDesc)
	}

	failAltDiag := singleDiagnostic(t, failAltGate)
	if failAltDiag.OffendingFileOrArtifact != failAltDiagArtifact {
		t.Errorf("gate %q diagnostic artifact = %q, want %q",
			gateFailAlt, failAltDiag.OffendingFileOrArtifact, failAltDiagArtifact)
	}
	if failAltDiag.Description != failAltDiagDesc {
		t.Errorf("gate %q diagnostic description = %q, want %q",
			gateFailAlt, failAltDiag.Description, failAltDiagDesc)
	}

	// The two failures are genuinely DISTINCT content, not one masking the
	// other: their diagnostic descriptions must differ.
	if failDiag.Description == failAltDiag.Description {
		t.Errorf("multi-failure masking: both failing gates report identical diagnostic description %q; each must surface its own",
			failDiag.Description)
	}

	// The passing sibling still reports passed and exits 0.
	sib := subCheckByName(t, out, gateSibling)
	if sib.Outcome != outcomePassed {
		t.Errorf("sibling gate %q outcome = %q, want %q", gateSibling, sib.Outcome, outcomePassed)
	}
	if sib.ExitCode != 0 {
		t.Errorf("sibling gate %q exit_code = %d, want 0", gateSibling, sib.ExitCode)
	}
}

// singleDiagnostic returns the sole SR-14 diagnostic aggregated for a failing
// gate, failing the test if the count is anything other than one (a gate whose
// diagnostic was dropped or duplicated would be caught here).
func singleDiagnostic(t *testing.T, sc subCheck) sr14Diagnostic {
	t.Helper()
	if len(sc.Diagnostics) != 1 {
		t.Fatalf("gate %q: expected exactly 1 aggregated diagnostic, got %d: %+v",
			sc.Name, len(sc.Diagnostics), sc.Diagnostics)
	}
	return sc.Diagnostics[0]
}
