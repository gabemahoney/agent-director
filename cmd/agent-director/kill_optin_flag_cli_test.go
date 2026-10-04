package main_test

// kill_optin_flag_cli_test.go covers kill's flags through the built CLI
// (SR-6.8, b.vqr): kill takes only --claude-instance-id, so every spelling of
// the former finished-row opt-in, and -h/--help, is ErrInvalidFlags with no
// usage text, no tmux call and no ad.kill.called line, on a live row and on a
// finished row beside its own reported-in session alike. The finished-row
// kill itself is agent-director-admin's kill-finished.

import (
	"regexp"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// usageRe matches flag usage text, which kill never prints.
var usageRe = regexp.MustCompile(`(?i)usage|-claude-instance-id`)

// assertOnlyEnvelope fails unless the run exited 1 with empty stdout and
// stderr holding exactly one error envelope named want; it returns it.
func assertOnlyEnvelope(t *testing.T, stdout, stderr string, code int, want string) errorEnvelope {
	t.Helper()
	if code != 1 || stdout != "" {
		t.Fatalf("exit = %d, stdout = %q; want 1 and empty (stderr=%q)", code, stdout, stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != want {
		t.Errorf("err_name = %q (%s); want %s", env.ErrName, env.ErrDescription, want)
	}
	return env
}

// TestKillIncludeFinishedIsUnknownFlag: each spelling of the former opt-in,
// and -h/--help, is ErrInvalidFlags with no usage text, no tmux call and no
// ad.kill.called line, leaving the row and its own session as they were, on a
// waiting row and on a finished row (b.vqr).
func TestKillIncludeFinishedIsUnknownFlag(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	rows := []struct {
		name string
		seed func(t *testing.T) cliRow
	}{
		{"waiting row", func(t *testing.T) cliRow { return seedWithOwnSession(t, store.StateWaiting, time.Now()) }},
		{"ended row with its own reported-in session", seedFinishedWithSession},
	}
	flags := []struct {
		flag     string
		echoesIt bool // the envelope may echo the caller's own spelling
	}{
		{"--include-finished", true},
		{"--include-finished=true", true},
		{"--include-finished=false", true},
		{"--include_finished", true},
		{"--includeFinished", true},
		{"--help", false},
		{"-h", false},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			for _, f := range flags {
				t.Run(f.flag, func(t *testing.T) {
					r := row.seed(t)

					stdout, stderr, code := runSpawnCLI(t, r.home, fakeDir, "kill", "--claude-instance-id", r.id, f.flag)

					env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags")
					if !f.echoesIt && apitest.OperatorActionNames.MatchString(stderr) {
						t.Errorf("SR-6.8: kill %s names an operator action: %s", f.flag, stderr)
					}
					if usageRe.MatchString(env.ErrDescription) {
						t.Errorf("SR-6.8: kill %s prints usage text: %q", f.flag, env.ErrDescription)
					}
					assertInvocationKinds(t, r.home)
					if left := (faketmuxfix.Tables{}).Read(t, r.socket).Sessions; len(left) != 1 {
						t.Errorf("sessions after kill = %+v; want the row's own session untouched", left)
					}
					assertCLIRowUnchanged(t, r.home, r.id, r.before)
					for _, l := range trailLinesOrNil(t, trailDir(r.home)) {
						if l["event"] == "ad.kill.called" {
							t.Errorf("kill wrote ad.kill.called %v; want none (kill did not run)", l)
						}
					}
				})
			}
		})
	}
}
