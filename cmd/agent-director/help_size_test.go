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
// (25 B), the shortest form that states empty text's Enter-only meaning. On
// 2026-10-03 b.vqr lowered it to 14600 when delete left the agent-facing
// manifest for agent-director-admin and kill's "never delete" texts went. On
// 2026-10-09 b.7vf lowered it from 14600 to 14596 after trims. The 14600 had
// gone stale: help had grown, never re-recorded, to 15159 B (b.pzy, b.t6e,
// b.fv2, b.kdf, b.3oc and others: decide, the help/version aliases,
// find-missing's unreported note, the pane-answer verbs). b.7vf trimmed it
// back with no fact lost: shorter wording in the kill, find-missing, spawn,
// resume and read-pane descriptions and in the shared forms
// (missingNotProofShort, sameEnvConsequences), kill's repeated "not
// verification" sentence dropped, and kill's success rule (the agent process
// and every other process found in the session's panes gone) folded into its
// opening sentence. Its review restored expireCleanupPointer's "finished-row",
// that a process kill's error named is not the agent, and kill's three
// unusable-name kinds in a form that cannot read as the name being a
// character, all paid for by further lossless trims. b.7vf also dropped the
// 10% slack, whose limit (16060 B) sat above the hard cap and so never fired:
// any growth above this count now fails. The user's cap is 15160 B: help must
// stay at or under it. On 2026-10-11 it took the free downward step to 14593
// (SR-20.6, b198cae5 having shrunk help by 3 B) and was then raised to 14700
// with the user's approval, 107 B for list-subagents' own entry only.
// On 2026-10-12 latest-spawn wording trimmed it to 14662 (free downward step).
// On 2026-10-13 caller wording trimmed it to 14642 (free downward step).
// On 2026-10-16 b.jf1 resume wording trimmed it to 14637 (free downward step).
// On 2026-10-17 b.jf1 E6 Task 1 lossless spawn/resume wording trims lowered
// it to 14634 (free downward step).
// On 2026-10-20 b.9nu pause wording trims lowered it to 14631 (free downward step).
//
// SR-20.6: this guard checks growth only, with no slack: help stdout one byte
// over this count fails. Re-recording it downward, to the newly measured
// count after a trim, is free and is done in the trim's commit. Re-recording
// it upward needs the user's approval before the commit and a recorded reason
// in the commit message (what grew, by how many bytes, why it cannot be
// shorter); never to make a failing guard pass.
const helpStdoutBytes = 14631

// helpHardCap is the user's hard cap on `agent-director help` stdout, in
// bytes. Help stdout must stay at or under it, with no slack.
//
// The cap moves only with the user's approval, recorded here with the date
// and the reason. History:
//   - 2026-09-30: set to 15160 B by the user at the Epic 11 gate, so that
//     help, which every agent reads, stays small.
const helpHardCap = 15160

// TestHelpSizeGuard fails when help stdout grows past the recorded byte
// count helpStdoutBytes by even one byte (SR-20.6, no slack since b.7vf), or
// past the user's hard cap (helpHardCap). A shrink passes, and its log line
// asks for the free downward re-record.
func TestHelpSizeGuard(t *testing.T) {
	stdout, stderr, code := runCLI(t, "help")
	if code != 0 {
		t.Fatalf("exit=%d want 0; stderr=%q", code, stderr)
	}
	got := len(stdout)
	t.Logf("help stdout %d bytes; recorded %d (no slack); hard cap %d", got, helpStdoutBytes, helpHardCap)
	if got < helpStdoutBytes {
		t.Logf("help shrank by %d bytes: re-record helpStdoutBytes down to %d in this commit (free, SR-20.6)",
			helpStdoutBytes-got, got)
	}
	overRecorded, overCap := helpSizeOver(got, helpStdoutBytes, helpHardCap)
	if overRecorded {
		t.Errorf("help stdout is %d bytes, %d over the recorded %d (no slack: any growth fails). "+
			"Trim help; raising helpStdoutBytes needs the user's approval and a recorded reason (SR-20.6).",
			got, got-helpStdoutBytes, helpStdoutBytes)
	}
	if overCap {
		t.Errorf("help stdout is %d bytes; the user's hard cap is %d (no slack). "+
			"Trim help; the cap moves only with the user's approval, recorded in helpHardCap's comment with the date and reason.",
			got, helpHardCap)
	}
}

// helpSizeOver is TestHelpSizeGuard's rule: whether help stdout of got bytes
// is over the recorded count (SR-20.6, no slack) and over the hard cap.
func helpSizeOver(got, recorded, hardCap int) (overRecorded, overCap bool) {
	return got > recorded, got > hardCap
}

// TestHelpSizeOver pins the guard's rule on b.7vf's numbers (recorded 14600,
// cap 15160): one byte of growth fails, well before the cap.
func TestHelpSizeOver(t *testing.T) {
	const recorded, hardCap = 14600, 15160
	for _, tc := range []struct {
		name                          string
		got                           int
		wantOverRecorded, wantOverCap bool
	}{
		{"at the recorded count", recorded, false, false},
		{"shrunk", recorded - 1, false, false},
		{"one byte over the recorded count", recorded + 1, true, false},
		{"at the hard cap", hardCap, true, false},
		{"one byte over the hard cap", hardCap + 1, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			overRecorded, overCap := helpSizeOver(tc.got, recorded, hardCap)
			if overRecorded != tc.wantOverRecorded || overCap != tc.wantOverCap {
				t.Errorf("helpSizeOver(%d, %d, %d) = (%v, %v); want (%v, %v)",
					tc.got, recorded, hardCap, overRecorded, overCap, tc.wantOverRecorded, tc.wantOverCap)
			}
		})
	}
	if helpStdoutBytes > helpHardCap {
		t.Errorf("helpStdoutBytes %d is over helpHardCap %d: the recorded-count check must bind at or before the cap (b.7vf)",
			helpStdoutBytes, helpHardCap)
	}
}
