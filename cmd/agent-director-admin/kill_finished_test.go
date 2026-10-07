package main_test

// kill_finished_test.go covers agent-director-admin kill-finished, kill's
// operator-only finished-row opt-in (SR-6.5, SR-6.7, b.vqr), through the built
// binary and the fake tmux: a finished row's own reported-in session is ended
// by ids and a live row is refused with no tmux call. The opt-in's other rows
// (never reported in, unknown id) are pkg/api's kill_optin_*_test.go.

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// optInRe matches the opt-in's former flag in any spelling (include-finished,
// ...), which no refusal names (SR-6.8).
var optInRe = regexp.MustCompile(`(?i)include.?finished`)

// assertOneKillCalled fails unless the trail under home holds exactly one
// ad.kill.called record, for id, written by agent-director-admin with
// include_finished true and kill_sent sent.
func assertOneKillCalled(t *testing.T, home, id string, sent bool) {
	t.Helper()
	recs := killCalled(t, home)
	if len(recs) != 1 {
		t.Fatalf("ad.kill.called records = %v; want exactly one", recs)
	}
	want := map[string]any{"claude_instance_id": id, "include_finished": true, "kill_sent": sent,
		"caller_process": "agent-director-admin"}
	for k, v := range want {
		if recs[0][k] != v {
			t.Errorf("ad.kill.called %s = %v; want %v (record %v)", k, recs[0][k], v, recs[0])
		}
	}
}

// TestKillFinishedReportedInSession: on an ended row whose own session
// reported in, kill-finished ends the agent's pane and the session by tmux id
// and prints kill_sent true; the row is unchanged and still ended.
func TestKillFinishedReportedInSession(t *testing.T) {
	r := seedFinishedWithSession(t)

	stdout, stderr, code := runAdmin(t, r.home, "kill-finished", "--claude-instance-id", r.id)

	if code != 0 || stderr != "" {
		t.Fatalf("kill-finished exit = %d, stderr = %q; want 0 and empty", code, stderr)
	}
	assertKillSent(t, stdout, true)
	invs := assertInvocationKinds(t, r.home, "list-sessions", "list-panes", "kill-pane", "kill-session")
	wantKills := [][]string{
		{"-u", "-S", r.socket, "kill-pane", "-t", apitest.TestPaneID},
		{"-u", "-S", r.socket, "kill-session", "-t", "$5"},
	}
	for i, want := range wantKills {
		if got := invs[2+i]; !slices.Equal(got, want) {
			t.Errorf("kill invocation %d = %q; want %q", i, got, want)
		}
	}
	if left := sessionsLeft(t, r.socket); len(left) != 0 {
		t.Errorf("sessions after kill-finished = %+v; want none", left)
	}
	assertRowUnchanged(t, r.home, r.id, r.before)
	assertOneKillCalled(t, r.home, r.id, true)

	stdout, stderr, code = runMain(t, r.home, "get", "--claude-instance-id", r.id)
	var got struct {
		State string `json:"state"`
	}
	if code != 0 || json.Unmarshal([]byte(stdout), &got) != nil || got.State != store.StateEnded {
		t.Errorf("agent-director get exit = %d, stdout = %q (stderr %q); want state %s", code, stdout, stderr, store.StateEnded)
	}
}

// TestKillFinishedRefusesLiveRow: a live row (pending included) is refused
// with ErrSpawnNotResumable before any tmux call; the plain kill the refusal
// points to, agent-director kill, then ends the row's session (b.fji C10).
func TestKillFinishedRefusesLiveRow(t *testing.T) {
	for _, state := range []string{store.StatePending, store.StateWaiting} {
		t.Run(state, func(t *testing.T) {
			home, id, socket := seedRow(t, state, "")
			ownSession(t, home, id, socket, time.Now())
			before := rowColumns(t, home, id)

			stdout, stderr, code := runAdmin(t, home, "kill-finished", "--claude-instance-id", id)

			env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrSpawnNotResumable")
			if m := optInRe.FindString(stderr); m != "" {
				t.Errorf("SR-6.8: the live-row refusal names the opt-in %q: %s", m, stderr)
			}
			assertInvocationKinds(t, home)
			assertRowUnchanged(t, home, id, before)

			// b.fji C10 literal follow: "the finished-row option applies only to an ended or missing row; no lookup was made and nothing was sent", so kill without the option.
			const advice = "the finished-row option applies only to an ended or missing row; no lookup was made and nothing was sent"
			if !strings.Contains(env.ErrDescription, advice) {
				t.Errorf("description %q lacks the advice %q", env.ErrDescription, advice)
			}
			stdout, stderr, code = runMain(t, home, "kill", "--claude-instance-id", id)
			if code != 0 || stderr != "" {
				t.Fatalf("agent-director kill: exit = %d, stderr = %q; want 0 and empty", code, stderr)
			}
			assertKillSent(t, stdout, true)
			if invs := invocations(t, home); len(invs) == 0 || !slices.Contains(invs[0], "list-sessions") {
				t.Errorf("agent-director kill: tmux invocations = %q; want the lookup first", invs)
			}
			if left := sessionsLeft(t, socket); len(left) != 0 {
				t.Errorf("sessions after the kill = %+v; want none", left)
			}
			if st := rowColumns(t, home, id).State; st != state {
				t.Errorf("state = %v; want %s kept", st, state)
			}
		})
	}
}

// TestAdviceFollow_C9_AdminStillStoppingRetryLater: inside the stopping window
// kill-finished refuses with the advice, sends nothing and leaves the row as
// it was.
func TestAdviceFollow_C9_AdminStillStoppingRetryLater(t *testing.T) {
	// C9 ErrTmuxUnresponsive from kill-finished, own session still stopping: "...nothing was done; retry later".
	// The retry is followed on the virtual clock by pkg/api's
	// TestAdviceFollow_C9_OptInStillStoppingOrStartingRetryLater, and a run
	// past the window by TestKillFinishedReportedInSession, so this test does
	// not wait out the window in real time.
	r := seedFinishedWithSession(t, apitest.WithEndedAt(time.Now().Truncate(time.Second)))

	stdout, stderr, code := runAdmin(t, r.home, "kill-finished", "--claude-instance-id", r.id)

	env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrTmuxUnresponsive")
	for _, want := range []string{"appears to still be stopping", "nothing was done; retry later"} {
		if !strings.Contains(env.ErrDescription, want) {
			t.Errorf("description %q lacks %q", env.ErrDescription, want)
		}
	}
	assertInvocationKinds(t, r.home, "list-sessions")
	if left := sessionsLeft(t, r.socket); len(left) != 1 {
		t.Errorf("sessions after the refusal = %+v; want the row's session untouched", left)
	}
	assertRowUnchanged(t, r.home, r.id, r.before)
}
