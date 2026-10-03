package main_test

// advice_follow_kill_cli_test.go: b.fji C9 through the built CLI and the fake
// tmux; kill's operator-only --include-finished exists only there. The API
// tier is pkg/api/advice_follow_kill_optin_test.go; C10's literal follow is in
// TestKillCLIIncludeFinishedOnLiveRow.

import (
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestAdviceFollow_C9_CLIStillStoppingRetryLater: inside the stopping window
// the CLI refuses with the advice, sends nothing and leaves the row as it was.
func TestAdviceFollow_C9_CLIStillStoppingRetryLater(t *testing.T) {
	// C9 ErrTmuxUnresponsive with --include-finished, own session still stopping: "...nothing was done; retry later".
	// The retry is followed on the virtual clock by pkg/api's
	// TestAdviceFollow_C9_OptInStillStoppingOrStartingRetryLater, and a CLI kill
	// past the window by TestKillIncludeFinishedCLIReportedInSession, so this
	// test does not wait out the window in real time.
	r := seedFinishedWithSession(t, apitest.WithEndedAt(time.Now().Truncate(time.Second)))

	stdout, stderr, code := runSpawnCLI(t, r.home, buildFakeTmux(t), "kill", "--claude-instance-id", r.id, "--include-finished")

	env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrTmuxUnresponsive")
	for _, want := range []string{"appears to still be stopping", "nothing was done; retry later"} {
		if !strings.Contains(env.ErrDescription, want) {
			t.Errorf("description %q lacks %q", env.ErrDescription, want)
		}
	}
	assertInvocationKinds(t, r.home, "list-sessions")
	if left := r.sessionsLeft(t); len(left) != 1 {
		t.Errorf("sessions after the refusal = %+v; want the row's session untouched", left)
	}
	assertCLIRowUnchanged(t, r.home, r.id, r.before)
}
