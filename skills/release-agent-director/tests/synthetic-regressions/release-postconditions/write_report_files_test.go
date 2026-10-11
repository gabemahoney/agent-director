// Package releasepostconditions_test — write-report.sh reads its phases and
// diagnostics arrays from files (b.2wr).
//
// # Background
//
// write-report.sh took both arrays as positional arguments and passed them to
// jq as --argjson. Linux caps one argument at 128 KiB, so an array over that
// (gate stderr_excerpt and SR-14 payloads) could start neither the script nor
// jq. $6 and $7 are now paths of files holding exactly one JSON array each.
//
// # SLOW TEST
//
// Invokes write-report.sh (bash + jq). Skipped in -short mode.
package releasepostconditions_test

import (
	"encoding/json"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// runWriteReport runs a staged write-report.sh with fixed $1-$5 and args as $6
// onward, returning its exit code, stderr and report path.
func runWriteReport(t *testing.T, args ...string) (exit int, stderr, reportPath string) {
	t.Helper()
	script, reportPath := stageWriteReport(t, repoRoot(t))
	cmd := exec.Command("bash", append([]string{script,
		"2026-10-10T00:00:00Z", "dry-run", "patch", "0.0.0", "0.0.1"}, args...)...)
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("run write-report.sh: %v", err)
		}
	}
	return cmd.ProcessState.ExitCode(), errBuf.String(), reportPath
}

// assertReportFields fails the test for each field of the report at path that
// is not the wanted JSON value.
func assertReportFields(t *testing.T, path string, want map[string]any) {
	t.Helper()
	report := reportFields(t, path)
	for field, w := range want {
		if got := report[field]; !reflect.DeepEqual(got, w) {
			t.Errorf("report %s\n got %s\nwant %s", field, jsonExcerpt(got), jsonExcerpt(w))
		}
	}
}

// TestWriteReportOversizeArrayFiles (b.2wr): phases and diagnostics files each
// over 128 KiB reach the report in full.
func TestWriteReportOversizeArrayFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: invokes write-report.sh")
	}
	t.Parallel()
	phasesPath, wantPhases := writeJSONFile(t, oversizePhases())
	diagsPath, wantDiags := writeJSONFile(t, []any{
		map[string]any{"gate": "coverage.go-root", "description": oversizeText},
	})
	exit, stderr, reportPath := runWriteReport(t, phasesPath, diagsPath, "5")
	if exit != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %.2000s", exit, stderr)
	}
	assertReportFields(t, reportPath, map[string]any{
		"phases": wantPhases, "diagnostics": wantDiags, "elapsed_seconds": 5.0,
	})
}

// TestWriteReportWithoutDiagnostics: $7 omitted, "" or a number (taken as
// elapsed seconds) gives diagnostics [] and the right elapsed_seconds.
func TestWriteReportWithoutDiagnostics(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: invokes write-report.sh")
	}
	for _, tc := range []struct {
		name        string
		tail        []string // $7 onward
		wantElapsed float64
	}{
		{"omitted", nil, 0},
		{"empty", []string{"", "7"}, 7},
		{"number", []string{"7.5"}, 7.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			phasesPath, wantPhases := writeJSONFile(t, json.RawMessage(priorPhasesAllPassedJSON))
			exit, stderr, reportPath := runWriteReport(t, append([]string{phasesPath}, tc.tail...)...)
			if exit != 0 {
				t.Fatalf("exit = %d, want 0\nstderr: %.2000s", exit, stderr)
			}
			assertReportFields(t, reportPath, map[string]any{
				"phases": wantPhases, "diagnostics": []any{}, "elapsed_seconds": tc.wantElapsed,
			})
		})
	}
}

// TestWriteReportBadInputRefused (b.2wr): exit 2 and no report when $6 or $7 is
// inline JSON or a path that is not a readable regular file holding exactly one
// JSON array; stderr names the argument, and inline JSON gets a short hint.
func TestWriteReportBadInputRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: invokes write-report.sh")
	}
	const hint = "Pass the path of a file holding the JSON array, not the array itself."
	inline := func(s string) func(*testing.T) string { return func(*testing.T) string { return s } }
	type badInput struct {
		bad    func(*testing.T) string
		inline bool
	}
	inputs := map[string]badInput{
		"inline_array": {inline(`[{"name":"preflight","note":"` + strings.Repeat("x", 4096) + `"}]`), true},
		"inline_empty": {inline("[]"), true},
	}
	for name, badPath := range badArrayFiles {
		inputs[name] = badInput{bad: badPath}
	}
	for _, label := range []string{"phases_file", "diagnostics_file"} {
		for name, in := range inputs {
			t.Run(label+"/"+name, func(t *testing.T) {
				t.Parallel()
				bad := in.bad(t)
				args := []string{bad, "1"}
				if label == "diagnostics_file" {
					args = []string{writeTempFile(t, "[]"), bad, "1"}
				}
				exit, stderr, reportPath := runWriteReport(t, args...)
				if exit != 2 {
					t.Errorf("exit = %d, want 2", exit)
				}
				if !strings.Contains(stderr, "ERROR: "+label+" ") || !strings.Contains(stderr, bad[:min(len(bad), 200)]) {
					t.Errorf("stderr does not name %s %.200q:\n%.2000s", label, bad, stderr)
				}
				if in.inline && (!strings.Contains(stderr, hint) || len(stderr) > 1024) {
					t.Errorf("want the hint %q in under 1 KiB of stderr, got %d bytes:\n%.2000s",
						hint, len(stderr), stderr)
				}
				assertNoReport(t, reportPath)
			})
		}
	}
}
