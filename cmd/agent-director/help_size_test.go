package main_test

import "testing"

// helpStdoutBytes is the measured byte count of `agent-director help` stdout
// (trailing newline included) after SR-18.2's short form replaced the full
// "not proof" sentence in five verb descriptions (decision-0930e).
//
// SR-20.6: this guard checks growth only. Re-recording it downward, to the
// newly measured count after a trim, is free. Re-recording it upward needs
// the orchestrator's approval before the commit and a recorded reason in the
// commit message (what grew, by how many bytes, why it cannot be shorter);
// never to make a failing guard pass.
const helpStdoutBytes = 15170

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
			"Trim help; raising helpStdoutBytes needs the orchestrator's approval and a recorded reason (SR-20.6).",
			got, helpStdoutBytes, limit)
	}
}
