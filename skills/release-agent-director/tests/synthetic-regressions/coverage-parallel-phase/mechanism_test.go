// mechanism_test.go — the sole exit-0 mechanism-validation test for the toy
// gates-config mechanism (SR-1.3, PM-approved exit-0 boundary).
//
// This asserts the all-passing variant drives run-parallel.sh to a clean exit 0
// and that its consolidated stdout parses into the typed SR-2.1 shape with the
// expected phase name and per-gate entries. It is a behavior assertion on the
// executor via the mechanism — not a meta-test of the Go helpers. The
// executor-semantics tests (single-failure, multi-failure, exit-2) live in the
// sibling Task's test files.

package coverageparallelphase_test

import "testing"

func TestToyMechanismAllPassing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: spawns run-parallel.sh subprocess (jq-heavy, ~1s)")
	}

	configPath := writeToyConfig(t, allPassingConfig())
	res := runExecutor(t, configPath)

	if res.exitCode != 0 {
		t.Fatalf("all-passing toy config: expected exit 0, got %d\nstdout:\n%s\nstderr:\n%s",
			res.exitCode, res.stdout, res.stderr)
	}

	out := parseConsolidated(t, res.stdout)

	if out.PhaseName != toyPhaseName {
		t.Errorf("phase_name = %q, want %q", out.PhaseName, toyPhaseName)
	}
	if out.Outcome != outcomePassed {
		t.Errorf("outcome = %q, want %q", out.Outcome, outcomePassed)
	}
	if len(out.SubChecks) != 2 {
		t.Fatalf("expected 2 sub_checks, got %d: %+v", len(out.SubChecks), out.SubChecks)
	}

	// Both configured gates must be present and passed (proves each ran to
	// completion and was reported).
	for _, name := range []string{gatePass, gatePassDiag} {
		sc := subCheckByName(t, out, name)
		if sc.Outcome != outcomePassed {
			t.Errorf("gate %q outcome = %q, want %q", name, sc.Outcome, outcomePassed)
		}
		if sc.ExitCode != 0 {
			t.Errorf("gate %q exit_code = %d, want 0", name, sc.ExitCode)
		}
	}

	// The passing gate that emits a diagnostic proves aggregation is
	// outcome-independent: a passed gate still carries its parsed SR-14 object.
	diagGate := subCheckByName(t, out, gatePassDiag)
	if len(diagGate.Diagnostics) != 1 {
		t.Fatalf("gate %q: expected 1 aggregated diagnostic, got %d: %+v",
			gatePassDiag, len(diagGate.Diagnostics), diagGate.Diagnostics)
	}
	if got := diagGate.Diagnostics[0].Description; got != passDiagDesc {
		t.Errorf("gate %q diagnostic description = %q, want %q", gatePassDiag, got, passDiagDesc)
	}
}
