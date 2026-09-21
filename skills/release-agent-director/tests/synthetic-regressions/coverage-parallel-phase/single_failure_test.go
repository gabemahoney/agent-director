// single_failure_test.go — direct run-parallel.sh single-failure sibling-
// tolerance test, carrying the SR-2.1 consolidated-output-shape assertions
// (b.mgw parallel /release pipeline; SR-1.3, SR-2.1, SR-7.5).
//
// The single-failure toy variant produces the richest consolidated output the
// executor emits: passing sub-checks, a failing sub-check, and populated SR-14
// diagnostics. This test drives run-parallel.sh DIRECTLY with that variant
// (from toyconfig_test.go — Task A) and proves the executor's sibling-tolerance
// contract with falsifiable content, not just exit codes:
//
//   - the executor exits 1 (any gate failed);
//   - every configured toy gate ran to completion (each name present exactly
//     once in sub_checks[]);
//   - the failing gate reports outcome failed with its nonzero exit code, and
//     its diagnostics carry the SAME SR-14 field VALUES the toy gate emitted;
//   - every sibling reports outcome passed with exit code 0;
//   - the top-level outcome is failed and phase_name matches the toy config;
//   - the full SR-2.1 shape/types hold on BOTH a passing and the failing
//     sub-check.
//
// run-parallel.sh is never modified (SR-1.1); no real coverage gate runs
// (SR-7.5 package prohibition); gate-name / diagnostic-field / JSON-key
// literals come only from Task A's package declarations (SR-7.3).

package coverageparallelphase_test

import (
	"encoding/json"
	"sort"
	"testing"
)

func TestSingleFailureSiblingTolerance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: spawns run-parallel.sh subprocess (jq-heavy, ~1s)")
	}

	configPath := writeToyConfig(t, singleFailureConfig())
	res := runExecutor(t, configPath)

	// ─── exit-code contract (SR-1.3): any gate failed ⇒ exit 1 ────────────────
	if res.exitCode != 1 {
		t.Fatalf("single-failure toy config: expected exit 1, got %d\nstdout:\n%s\nstderr:\n%s",
			res.exitCode, res.stdout, res.stderr)
	}

	out := parseConsolidated(t, res.stdout)

	// ─── top-level shape (SR-2.1) ─────────────────────────────────────────────
	if out.PhaseName != toyPhaseName {
		t.Errorf("phase_name = %q, want %q", out.PhaseName, toyPhaseName)
	}
	if out.Outcome != outcomeFailed {
		t.Errorf("top-level outcome = %q, want %q", out.Outcome, outcomeFailed)
	}

	// Every configured gate ran to completion: the single-failure variant has
	// three gates, each present exactly once.
	if len(out.SubChecks) != 3 {
		t.Fatalf("expected 3 sub_checks, got %d: %+v", len(out.SubChecks), out.SubChecks)
	}
	seen := map[string]int{}
	for _, sc := range out.SubChecks {
		seen[sc.Name]++
	}
	for _, name := range []string{gatePass, gateFail, gateSibling} {
		if seen[name] != 1 {
			t.Errorf("gate %q appears %d times in sub_checks[], want exactly 1", name, seen[name])
		}
	}

	// ─── failing gate: outcome, nonzero exit, and DIAGNOSTIC CONTENT ──────────
	fail := subCheckByName(t, out, gateFail)
	if fail.Outcome != outcomeFailed {
		t.Errorf("failing gate %q outcome = %q, want %q", gateFail, fail.Outcome, outcomeFailed)
	}
	if fail.ExitCode == 0 {
		t.Errorf("failing gate %q exit_code = %d, want nonzero", gateFail, fail.ExitCode)
	}
	if len(fail.Diagnostics) != 1 {
		t.Fatalf("failing gate %q: expected 1 aggregated SR-14 diagnostic, got %d: %+v",
			gateFail, len(fail.Diagnostics), fail.Diagnostics)
	}
	// Parsed field-value CONTENT assertions — the diagnostic that fired is the
	// one the toy gate emitted, not merely a non-empty array (falsifiability).
	d := fail.Diagnostics[0]
	if d.Gate != gateFail {
		t.Errorf("failing diagnostic gate = %q, want %q", d.Gate, gateFail)
	}
	if d.OffendingFileOrArtifact != failDiagArtifact {
		t.Errorf("failing diagnostic offending_file_or_artifact = %q, want %q", d.OffendingFileOrArtifact, failDiagArtifact)
	}
	if d.Description != failDiagDesc {
		t.Errorf("failing diagnostic description = %q, want %q", d.Description, failDiagDesc)
	}
	if d.CorrectiveAction != failDiagAction {
		t.Errorf("failing diagnostic corrective_action = %q, want %q", d.CorrectiveAction, failDiagAction)
	}

	// ─── siblings: passed with exit 0, nothing masked by the failure ──────────
	for _, name := range []string{gatePass, gateSibling} {
		sc := subCheckByName(t, out, name)
		if sc.Outcome != outcomePassed {
			t.Errorf("sibling gate %q outcome = %q, want %q", name, sc.Outcome, outcomePassed)
		}
		if sc.ExitCode != 0 {
			t.Errorf("sibling gate %q exit_code = %d, want 0", name, sc.ExitCode)
		}
	}

	// ─── SR-2.1 full shape/types on BOTH a passing and the failing sub-check ──
	// The passing sibling gatePass emits no diagnostic, so its diagnostics array
	// must be present and empty (non-nil). The failing sub-check carries one.
	// rawByName preserves the exact key set the executor emitted so the shape
	// assertion can prove KEY PRESENCE (a missing key would zero-value under a
	// typed decode and pass silently), not just field types.
	rawByName := rawSubChecksByName(t, res.stdout)
	wantKeys := subCheckJSONKeys(t)
	pass := subCheckByName(t, out, gatePass)
	assertSubCheckShape(t, pass, rawByName[gatePass], wantKeys, 0)
	assertSubCheckShape(t, fail, rawByName[gateFail], wantKeys, 1)
}

// assertSubCheckShape verifies every SR-2.1 sub_check field is present with the
// correct type and that the diagnostics array is non-nil with the expected
// length. It asserts on two levels: (1) every SR-2.1 JSON KEY in wantKeys is
// physically present in the raw sub_check object — a missing key (e.g.
// stderr_excerpt, duration_ms, or a passing gate's exit_code) would otherwise
// zero-value under parseConsolidated's typed decode and pass unnoticed; (2) the
// decoded field VALUES/types hold. wantKeys and raw come from subCheckJSONKeys /
// rawSubChecksByName, so the asserted key spellings stay tied to the struct-tag
// declare-once home (SR-7.3).
func assertSubCheckShape(t *testing.T, sc subCheck, raw map[string]json.RawMessage, wantKeys []string, wantDiagnostics int) {
	t.Helper()
	if raw == nil {
		t.Fatalf("sub_check %q: no raw JSON object captured for key-presence assertions", sc.Name)
	}
	for _, key := range wantKeys {
		if _, ok := raw[key]; !ok {
			t.Errorf("sub_check %q missing SR-2.1 key %q; present keys=%v", sc.Name, key, rawKeys(raw))
		}
	}
	if sc.Name == "" {
		t.Errorf("sub_check name is empty: %+v", sc)
	}
	if sc.Outcome != outcomePassed && sc.Outcome != outcomeFailed {
		t.Errorf("sub_check %q outcome = %q, want %q or %q", sc.Name, sc.Outcome, outcomePassed, outcomeFailed)
	}
	if sc.DurationMS < 0 {
		t.Errorf("sub_check %q duration_ms = %d, want >= 0", sc.Name, sc.DurationMS)
	}
	// stderr_excerpt is always present (may be empty for a silent passing gate).
	if sc.Diagnostics == nil {
		t.Errorf("sub_check %q diagnostics array is nil, want present (possibly empty)", sc.Name)
	}
	if len(sc.Diagnostics) != wantDiagnostics {
		t.Errorf("sub_check %q has %d diagnostics, want %d: %+v",
			sc.Name, len(sc.Diagnostics), wantDiagnostics, sc.Diagnostics)
	}
}

// rawKeys returns the sorted key set of a raw sub_check object, for readable
// failure messages when an SR-2.1 key is missing.
func rawKeys(raw map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
