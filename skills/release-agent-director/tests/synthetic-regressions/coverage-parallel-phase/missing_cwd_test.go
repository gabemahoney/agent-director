// missing_cwd_test.go — run-parallel.sh's own missing-cwd SR-14 diagnostic (b.v46).
//
// The executor used to printf this line itself: a quote or TAB in the gate name
// or cwd made it invalid JSON, so the gate's diagnostics came back [], and a
// config without a cwd key gave offending_file_or_artifact the string "null".

package coverageparallelphase_test

import (
	"path/filepath"
	"testing"
)

func TestMissingCwdDiagnostic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: spawns run-parallel.sh subprocess (jq-heavy, ~1s)")
	}
	odd := "has \"quote\", back\\slash and\ttab"
	gone := filepath.Join(t.TempDir(), "gone "+odd)
	cases := []struct {
		name          string
		gate          toyGate
		wantOffending string // JSON null decodes to ""
		wantDesc      string
	}{
		{"cwd_with_quote_and_tab", toyGate{Name: gateFail + " " + odd, Command: passCmd(), Cwd: gone}, gone, "gate cwd does not exist: " + gone},
		{"cwd_key_missing", toyGate{Name: gateFail, Command: passCmd()}, "", "gate cwd does not exist: null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := toyConfig{PhaseName: toyPhaseName, Gates: []toyGate{tc.gate}, MaxParallel: 1}
			res := runExecutor(t, writeToyConfig(t, cfg))
			if res.exitCode != 1 {
				t.Fatalf("expected exit 1, got %d\nstdout:\n%s\nstderr:\n%s", res.exitCode, res.stdout, res.stderr)
			}
			sc := subCheckByName(t, parseConsolidated(t, res.stdout), tc.gate.Name)
			if sc.Outcome != outcomeFailed {
				t.Errorf("outcome = %q, want %q", sc.Outcome, outcomeFailed)
			}
			d := singleDiagnostic(t, sc) // [] when the line was invalid JSON
			if d.Gate != tc.gate.Name {
				t.Errorf("diagnostic gate = %q, want %q", d.Gate, tc.gate.Name)
			}
			if d.OffendingFileOrArtifact != tc.wantOffending {
				t.Errorf("diagnostic offending_file_or_artifact = %q, want %q", d.OffendingFileOrArtifact, tc.wantOffending)
			}
			if d.Description != tc.wantDesc {
				t.Errorf("diagnostic description = %q, want %q", d.Description, tc.wantDesc)
			}
			if d.CorrectiveAction == "" {
				t.Errorf("diagnostic corrective_action is empty")
			}
		})
	}
}
