package main_test

import "testing"

// helpStdoutBytes is the measured byte count of `agent-director help` stdout
// (trailing newline included). Epic 11 lowered it to 15160 when SR-18.2's
// short form replaced the full "not proof" sentence in five verb descriptions
// (decision-0930e); Epic 15 Task fj lowered it to 15143 when expire's
// description gained its cleanup text and other descriptions were trimmed to
// fit; Epic 16 Task 96 lowered it to 15136 when resume's description gained
// its pre-launch refusal text and other descriptions were trimmed to fit.
// On 2026-10-01 it was re-recorded up to 15154 with the user's approval, for
// Epic 16's resume text after "duplicate session". Epic 17 Task tm lowered it
// to 15101 when spawn's description gained reuse's two ErrInternal triggers
// and the spawn, status and hook descriptions were trimmed to fit. On
// 2026-10-01 it was set to 15127 (up from 15101) with the user's approval,
// for Epic 17's reuse text after version's install.sh sentence was cut. On
// 2026-10-01, Epic 19 Task 1 (unusable-name pointers on five verbs) was
// fitted by wording trims and the user-approved removal of serve's
// registration sentence and make-template's spawn --template clause; lowered
// to 15125. On 2026-10-03, re-recorded up to 15147 with the user's approval
// for b.9o4: send-keys' description gained " (empty text: Enter only)"
// (25 B), the shortest form that states empty text's Enter-only meaning. The
// user's cap is 15160 B: help must stay at or under it.
//
// SR-20.6: this guard checks growth only. Re-recording it downward, to the
// newly measured count after a trim, is free. Re-recording it upward needs
// the orchestrator's approval before the commit and a recorded reason in the
// commit message (what grew, by how many bytes, why it cannot be shorter);
// never to make a failing guard pass.
const helpStdoutBytes = 15147

// helpHardCap is the user's hard cap on `agent-director help` stdout, in
// bytes. Help stdout must stay at or under it, with no slack.
//
// The cap moves only with the user's approval, recorded here with the date
// and the reason. History:
//   - 2026-09-30: set to 15160 B by the user at the Epic 11 gate, so that
//     help, which every agent reads, stays small.
const helpHardCap = 15160

// TestHelpSizeGuard fails when help stdout grows past the recorded byte
// count plus 10% (SR-20.6), or past the user's hard cap (helpHardCap).
func TestHelpSizeGuard(t *testing.T) {
	stdout, stderr, code := runCLI(t, "help")
	if code != 0 {
		t.Fatalf("exit=%d want 0; stderr=%q", code, stderr)
	}
	limit := helpStdoutBytes + helpStdoutBytes/10
	got := len(stdout)
	t.Logf("help stdout %d bytes; recorded %d, limit %d; hard cap %d", got, helpStdoutBytes, limit, helpHardCap)
	if got > limit {
		t.Errorf("help stdout is %d bytes; recorded %d, limit %d (recorded + 10%%). "+
			"Trim help; raising helpStdoutBytes needs the user's approval and a recorded reason (SR-20.6).",
			got, helpStdoutBytes, limit)
	}
	if got > helpHardCap {
		t.Errorf("help stdout is %d bytes; the user's hard cap is %d (no slack). "+
			"Trim help; the cap moves only with the user's approval, recorded in helpHardCap's comment with the date and reason.",
			got, helpHardCap)
	}
}
