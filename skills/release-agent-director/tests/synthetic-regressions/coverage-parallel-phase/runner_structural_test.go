// runner_structural_test.go — structural assertion on the coverage-phase
// runner's live-path TEXT (SR-7.5, N1 disposition; b.mgw parallel /release
// pipeline).
//
// N1 DISPOSITION (tickets/Ideas/b.mgw/req-review-findings.md)
// ==========================================================
// The runner has no config seam (SR-1.1), so its exit-code propagation on a
// LIVE run cannot be exercised without running the real coverage phase — which
// SR-7.5 forbids in this package. The N1 disposition pins the sanctioned
// verification to a STRUCTURAL assertion on the script text: a test (explicitly
// NOT human review, and NEVER a live coverage-phase run) asserts the runner's
// live path is a bare delegation to run-parallel.sh followed by unmodified exit
// propagation — the docker-epics tail shape. This file is that test, kept
// SEPARATE from the dry-run/argument test file for an unambiguous
// subtask-per-file mapping.
//
// This test only READS the script text from disk (runnerPath, declared once in
// runner_dryrun_test.go and reused here); it never executes the runner's live
// path, run-parallel.sh, or any real gate.
//
// FALSIFIABILITY
// ==============
// The reference tail is two physical lines (as the real runner copies from
// docker-epics.sh):
//
//	bash "$RUN_PARALLEL" "$CONFIG_FILE"
//	exit $?
//
// The test locates the delegation line, then requires the very next
// significant (non-blank, non-comment) line to be exactly `exit $?`. It fails
// if the delegation line is wrapped (piped, `||`-guarded, `if`-tested, its exit
// code remapped, captured into a variable, or backgrounded), or if any
// statement is inserted between the delegation and the exit propagation — any
// of which could alter or swallow the executor's exit status.

package coverageparallelphase_test

import (
	"os"
	"strings"
	"testing"
)

// Expected significant tail lines, normalized (single-spaced, trimmed). These
// are the docker-epics delegation-plus-exit-propagation shape; declared once
// here as this file is the sole owner of the structural surface.
const (
	// The bare delegation: run RUN_PARALLEL with the config file, nothing else
	// on the line. Any wrapping (pipe, &&/||, backticks, redirection, capture)
	// changes the token stream and fails the exact match.
	expectedDelegationLine = `bash "$RUN_PARALLEL" "$CONFIG_FILE"`
	// Unmodified exit propagation: the executor's exact status, unremapped.
	expectedExitPropLine = `exit $?`
)

// normalizeLine collapses internal runs of whitespace to single spaces and
// trims the ends, so an insignificant reindent or aligned spacing does not
// defeat the match while any change to the actual token stream still does.
func normalizeLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// significant reports whether a normalized line carries a statement (i.e. is
// neither blank nor a comment). Blank lines and `#` comments between the
// delegation and the exit are tolerated; any real statement is not.
func significant(normalized string) bool {
	return normalized != "" && !strings.HasPrefix(normalized, "#")
}

// TestRunnerLivePathIsBareDelegation asserts, on the script TEXT alone, that the
// runner's live path is a bare delegation to run-parallel.sh immediately
// followed by unmodified exit propagation (N1 disposition). It never runs the
// runner, run-parallel.sh, or any gate.
func TestRunnerLivePathIsBareDelegation(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(runnerPath(t, root))
	if err != nil {
		t.Fatalf("read runner script: %v", err)
	}
	rawLines := strings.Split(string(data), "\n")

	// Collect the significant lines with their normalized form, so we can assert
	// on the delegation and the line that follows it while ignoring blank/comment
	// filler.
	var sig []struct{ norm string }
	for _, raw := range rawLines {
		n := normalizeLine(raw)
		if significant(n) {
			sig = append(sig, struct{ norm string }{norm: n})
		}
	}

	// Find the delegation line. Exactly one bare delegation is expected; more
	// than one would mean an ambiguous live path.
	delegationIdx := -1
	delegationCount := 0
	for i, l := range sig {
		if l.norm == expectedDelegationLine {
			delegationCount++
			if delegationIdx == -1 {
				delegationIdx = i
			}
		}
	}
	if delegationCount != 1 {
		t.Fatalf("expected exactly one bare delegation line %q, found %d\nsignificant lines:\n%s",
			expectedDelegationLine, delegationCount, dumpNorm(sig))
	}

	// The delegation must not be the last significant line — the exit
	// propagation has to follow it.
	if delegationIdx == len(sig)-1 {
		t.Fatalf("delegation line %q is the last significant statement; expected %q to follow it",
			expectedDelegationLine, expectedExitPropLine)
	}

	// The VERY NEXT significant line must be the unmodified exit propagation —
	// nothing may sit between them, and the propagation must not be remapped.
	next := sig[delegationIdx+1].norm
	if next != expectedExitPropLine {
		t.Errorf("line following the delegation = %q, want %q (bare, unmodified exit propagation; N1 disposition)\nsignificant lines:\n%s",
			next, expectedExitPropLine, dumpNorm(sig))
	}
}

// dumpNorm renders the collected significant lines for failure diagnostics.
func dumpNorm(sig []struct{ norm string }) string {
	var b strings.Builder
	for i, l := range sig {
		b.WriteString("  [")
		b.WriteString(itoa(i))
		b.WriteString("] ")
		b.WriteString(l.norm)
		b.WriteString("\n")
	}
	return b.String()
}
