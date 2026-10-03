package main_test

// kill_optin_flag_cli_test.go covers kill's operator-only --include-finished
// through the built CLI (SR-6.5, SR-6.8): only the exact name parses, it
// reaches Kill (a live row is refused with no tmux call, and the plain kill
// the refusal points to then works: b.fji C10), off is the default, and no
// path prints usage text. Its finished-row effect is in kill_optin_cli_test.go.

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// cliOptInRe matches the opt-in in any spelling (include-finished, ...).
var cliOptInRe = regexp.MustCompile(`(?i)include.?finished`)

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

// TestKillCLIIncludeFinishedUnknownID: the flag parses in either order and
// has no effect on an unknown id: ErrSpawnNotFound, no tmux call.
func TestKillCLIIncludeFinishedUnknownID(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for name, args := range map[string][]string{
		"flag first": {"kill", "--include-finished", "--claude-instance-id", "absent"},
		"flag last":  {"kill", "--claude-instance-id", "absent", "--include-finished"},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			bootstrapDB(t, home)
			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, args...)
			assertOnlyEnvelope(t, stdout, stderr, code, "ErrSpawnNotFound")
			assertInvocationKinds(t, home)
		})
	}
}

// TestKillCLIIncludeFinishedFalseOnEndedRow: an explicit false is the plain
// finished-row no-op, even beside the row's own reported-in session.
func TestKillCLIIncludeFinishedFalseOnEndedRow(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	t.Run("no session", func(t *testing.T) {
		home, id, _ := seedKillRow(t, store.StateEnded)
		before := rowColumns(t, home, id)

		stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "kill", "--claude-instance-id", id, "--include-finished=false")
		if code != 0 || stderr != "" {
			t.Fatalf("kill exit = %d, stderr = %q; want 0 and empty", code, stderr)
		}
		assertKillSent(t, stdout, false)
		assertInvocationKinds(t, home)
		assertCLIRowUnchanged(t, home, id, before)
	})
	t.Run("own reported-in session", func(t *testing.T) {
		r := seedFinishedWithSession(t)

		stdout, stderr, code := runSpawnCLI(t, r.home, fakeDir, "kill", "--claude-instance-id", r.id, "--include-finished=false")
		if code != 0 || stderr != "" {
			t.Fatalf("kill exit = %d, stderr = %q; want 0 and empty", code, stderr)
		}
		assertKillSent(t, stdout, false)
		assertInvocationKinds(t, r.home)
		if left := r.sessionsLeft(t); len(left) != 1 {
			t.Errorf("sessions after kill = %+v; want the row's session untouched", left)
		}
		assertCLIRowUnchanged(t, r.home, r.id, r.before)
	})
}

// TestKillCLIOptInNearMissesAndHelp: near-miss spellings, a non-boolean value
// and -h/--help are ErrInvalidFlags (what an older binary gives the opt-in),
// with no usage text; help names no opt-in (SR-6.8).
func TestKillCLIOptInNearMissesAndHelp(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	cases := []struct {
		flag     string
		echoesIt bool // the envelope may echo the caller's own spelling
	}{
		{"--include_finished", true},
		{"--includeFinished", true},
		{"--include-finished=notabool", true},
		{"--help", false},
		{"-h", false},
	}
	for _, tc := range cases {
		t.Run(tc.flag, func(t *testing.T) {
			home := t.TempDir()
			bootstrapDB(t, home)
			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "kill", "--claude-instance-id", "absent", tc.flag)
			env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags")
			if !tc.echoesIt && cliOptInRe.MatchString(stderr) {
				t.Errorf("SR-6.8: kill %s names the opt-in: %s", tc.flag, stderr)
			}
			if usageRe.MatchString(env.ErrDescription) {
				t.Errorf("SR-6.8: kill %s prints usage text: %q", tc.flag, env.ErrDescription)
			}
			assertInvocationKinds(t, home)
		})
	}
}

// TestKillCLIIncludeFinishedOnLiveRow: with the flag a live row (pending
// included) is refused with ErrSpawnNotResumable before any tmux call; without
// it the same row takes the ordinary path, lookup first, and its session ends.
func TestKillCLIIncludeFinishedOnLiveRow(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, state := range []string{store.StatePending, store.StateWaiting} {
		t.Run(state, func(t *testing.T) {
			home, id, socket := seedKillRow(t, state)
			token, _, storeID := launchIdentity(t, home, id)
			before := rowColumns(t, home, id)
			name, _ := before.TmuxSessionName.(string)
			const sessionID = "$3"
			faketmuxfix.Tables{}.Write(t, socket, killTable(faketmuxfix.Session{
				ID: sessionID, Created: time.Now().Unix(), Name: name, Label: tmuxfix.LabelValue(token, sessionID, id, storeID),
				Panes: []faketmuxfix.Pane{{ID: apitest.TestPaneID, PID: apitest.TestPanePID}},
			}))

			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "kill", "--claude-instance-id", id, "--include-finished")
			env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrSpawnNotResumable")
			if m := cliOptInRe.FindString(stderr); m != "" {
				t.Errorf("SR-6.8: the live-row refusal names the opt-in %q: %s", m, stderr)
			}
			assertInvocationKinds(t, home)
			assertCLIRowUnchanged(t, home, id, before)

			// b.fji C10 literal follow: "the finished-row option applies only to an ended or missing row; no lookup was made and nothing was sent", so kill again without the flag.
			const advice = "the finished-row option applies only to an ended or missing row; no lookup was made and nothing was sent"
			if !strings.Contains(env.ErrDescription, advice) {
				t.Errorf("description %q lacks the advice %q", env.ErrDescription, advice)
			}
			stdout, stderr, code = runSpawnCLI(t, home, fakeDir, "kill", "--claude-instance-id", id)
			if code != 0 || stderr != "" {
				t.Fatalf("kill without the flag: exit = %d, stderr = %q; want 0 and empty", code, stderr)
			}
			assertKillSent(t, stdout, true)
			invs := fakeTmuxInvocations(t, home)
			if len(invs) == 0 || !slices.Contains(invs[0], "list-sessions") {
				t.Errorf("kill without the flag: tmux invocations = %q; want the lookup first", invs)
			}
			if left := (faketmuxfix.Tables{}).Read(t, socket).Sessions; len(left) != 0 {
				t.Errorf("sessions after the kill = %+v; want none", left)
			}
			if st := rowColumns(t, home, id).State; st != state {
				t.Errorf("state = %v; want %s kept", st, state)
			}
		})
	}
}
