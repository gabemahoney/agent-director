// large_stderr_test.go — a gate's stderr_excerpt or SR-14 diagnostics over
// Linux's 128 KiB single-argument limit (b.nsa). run-parallel.sh passed both to
// jq as arguments, so the exec failed and the gate's sub_check was lost.

package coverageparallelphase_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLargeStderrSurvives(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: spawns run-parallel.sh subprocess (jq-heavy, ~1s)")
	}
	chunk := "go: \"x\"\t\\ "
	long := strings.Repeat(chunk, (200<<10)/len(chunk)) // one line, so a 50-line tail keeps it
	diagLine, err := json.Marshal(sr14Diagnostic{Gate: gateFail, OffendingFileOrArtifact: failDiagArtifact, Description: long, CorrectiveAction: failDiagAction})
	if err != nil {
		t.Fatalf("marshal diagnostic: %v", err)
	}
	filler := strings.Repeat("filler\n", 50) // pushes the diagnostic out of the 50-line excerpt
	cases := []struct {
		name, stderr, wantExcerpt string
		wantDesc                  string // "": no diagnostic
	}{
		{"long_excerpt", long + "\nlast\tline\n", long + "\nlast\tline", ""},
		{"long_diagnostic", string(diagLine) + "\n" + filler, strings.TrimSuffix(filler, "\n"), long},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			errFile := filepath.Join(t.TempDir(), "gate.stderr")
			if err := os.WriteFile(errFile, []byte(tc.stderr), 0o644); err != nil {
				t.Fatalf("write %s: %v", errFile, err)
			}
			gate := toyGate{Name: gateFail, Command: "cat '" + errFile + "' >&2; exit 1", Cwd: "."}
			res := runExecutor(t, writeToyConfig(t, toyConfig{PhaseName: toyPhaseName, Gates: []toyGate{gate}, MaxParallel: 1}))
			if res.exitCode != 1 {
				t.Fatalf("expected exit 1, got %d\nstderr: %.2000q", res.exitCode, res.stderr)
			}
			sc := subCheckByName(t, parseConsolidated(t, res.stdout), gateFail) // absent when jq could not run
			if sc.StderrExcerpt != tc.wantExcerpt {
				t.Errorf("stderr_excerpt: got %d bytes, want %d", len(sc.StderrExcerpt), len(tc.wantExcerpt))
			}
			if tc.wantDesc == "" {
				if len(sc.Diagnostics) != 0 {
					t.Errorf("got %d diagnostics, want none", len(sc.Diagnostics))
				}
				return
			}
			if d := singleDiagnostic(t, sc); d.Description != tc.wantDesc {
				t.Errorf("diagnostic description: got %d bytes, want %d", len(d.Description), len(tc.wantDesc))
			}
		})
	}
}
