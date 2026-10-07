package main_test

// kill through the built CLI (SR-6.1, SR-6.8). Its refusals, trail and the
// finished-row no-op are pkg/api's kill_*_test.go; pause's CLI surface is
// test/envelope-diff's pause rows.

import (
	"reflect"
	"regexp"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestKillCLIHappyPath: the row's own labelled session is killed by pane id
// and session id on the row's socket, never by name; stdout is exactly
// {"kill_sent":true} and the row is unchanged.
func TestKillCLIHappyPath(t *testing.T) {
	home, id, socket := seedRowOnSocket(t, store.StateWaiting)
	sess := ownSession(t, home, id, "")
	faketmuxfix.Tables{}.Write(t, socket, fakeTable(sess))

	stdout, stderr, code := runSpawnCLI(t, home, buildFakeTmux(t), "kill", "--claude-instance-id", id)

	if code != 0 || stderr != "" || stdout != "{\"kill_sent\":true}\n" {
		t.Fatalf("kill exit = %d, stdout = %q, stderr = %q; want 0, {\"kill_sent\":true} and empty", code, stdout, stderr)
	}
	invs := assertInvocationKinds(t, home, "list-sessions", "list-panes", "kill-pane", "kill-session")
	for i, want := range [][]string{
		{"-u", "-S", socket, "kill-pane", "-t", apitest.TestPaneID},
		{"-u", "-S", socket, "kill-session", "-t", sess.ID},
	} {
		if !slices.Equal(invs[2+i], want) {
			t.Errorf("kill invocation %d = %q; want %q", i, invs[2+i], want)
		}
	}
	for _, argv := range invs {
		if slices.Contains(argv, sess.Name) {
			t.Errorf("invocation %q names the session %q; want ids only", argv, sess.Name)
		}
	}
	if left := (faketmuxfix.Tables{}).Read(t, socket).Sessions; len(left) != 0 {
		t.Errorf("sessions after kill = %+v; want none", left)
	}
	if st := rowColumns(t, home, id).State; st != store.StateWaiting {
		t.Errorf("row state after kill = %v; want %s unchanged", st, store.StateWaiting)
	}
}

// TestKillIncludeFinishedIsUnknownFlag: kill takes only --claude-instance-id
// (SR-6.8, b.vqr), so the former finished-row opt-in, in any form, and -h are
// ErrInvalidFlags with no usage text, no tmux call and no ad.kill.called
// line, leaving a finished row and its own session as they were (ending it is
// agent-director-admin kill-finished's alone).
func TestKillIncludeFinishedIsUnknownFlag(t *testing.T) {
	usageRe := regexp.MustCompile(`(?i)usage|-claude-instance-id`)
	for _, flag := range []string{"--include-finished", "--include-finished=false", "-h"} {
		t.Run(flag, func(t *testing.T) {
			home, id, socket := seedRowOnSocket(t, store.StateEnded)
			faketmuxfix.Tables{}.Write(t, socket, fakeTable(ownSession(t, home, id, "")))
			before := rowColumns(t, home, id)

			stdout, stderr, code := runSpawnCLI(t, home, buildFakeTmux(t), "kill", "--claude-instance-id", id, flag)

			env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags")
			if flag == "-h" && apitest.OperatorActionNames.MatchString(stderr) {
				t.Errorf("SR-6.8: kill %s names an operator action: %s", flag, stderr)
			}
			if usageRe.MatchString(env.ErrDescription) {
				t.Errorf("SR-6.8: kill %s prints usage text: %q", flag, env.ErrDescription)
			}
			assertInvocationKinds(t, home)
			if left := (faketmuxfix.Tables{}).Read(t, socket).Sessions; len(left) != 1 {
				t.Errorf("sessions after kill = %+v; want the row's own session untouched", left)
			}
			if after := rowColumns(t, home, id); !reflect.DeepEqual(after, before) {
				t.Errorf("row after kill = %+v; want unchanged %+v", after, before)
			}
			if kc := eventsOf(trailOrNil(t, home), "ad.kill.called"); len(kc) != 0 {
				t.Errorf("kill wrote ad.kill.called %v; want none (kill did not run)", kc)
			}
		})
	}
}
