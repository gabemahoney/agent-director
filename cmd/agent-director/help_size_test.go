package main_test

import "testing"

// helpStdoutBytes is the measured byte count of `agent-director help` stdout
// (trailing newline included) after the live-row sequence trim, the hook
// description's subagent clause and spawn's held-name contract.
//
// SR-20.6: this guard checks growth only. Deliberate help growth updates this
// constant, to the newly measured count, in the same commit as the change.
const helpStdoutBytes = 14023

// TestHelpSizeGuard fails when help stdout grows past the recorded
// byte count plus 10% (SR-20.6).
func TestHelpSizeGuard(t *testing.T) {
	stdout, stderr, code := runCLI(t, "help")
	if code != 0 {
		t.Fatalf("exit=%d want 0; stderr=%q", code, stderr)
	}
	limit := helpStdoutBytes + helpStdoutBytes/10
	got := len(stdout)
	t.Logf("help stdout %d bytes; recorded %d, limit %d", got, helpStdoutBytes, limit)
	if got > limit {
		t.Errorf("help stdout is %d bytes; recorded %d, limit %d (recorded + 10%%). "+
			"Deliberate growth updates helpStdoutBytes in the same commit (SR-20.6).",
			got, helpStdoutBytes, limit)
	}
}
