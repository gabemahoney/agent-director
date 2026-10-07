package tmuxfix_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// sharedSeeded returns a Recorder whose sockA server holds pane %0 (pid 10,
// pane label Token) in "a" ($0, with %1), a grouped "b" ($1, %0 only) and a
// "c" ($2) that links %0's window at index 3 beside its own %2.
func sharedSeeded() *tmuxfix.Recorder {
	return tmuxfix.NewRecorder().SeedSessions(sockA,
		tmuxfix.SeedSession{ID: "$0", Name: "a", Panes: []tmuxfix.SeedPane{
			{ID: "%0", PID: 10, AdPane: tmuxfix.Token}, {Index: 1, ID: "%1", PID: 11}}},
		tmuxfix.SeedSession{ID: "$1", Name: "b", Panes: []tmuxfix.SeedPane{{ID: "%0", Shared: true}}},
		tmuxfix.SeedSession{ID: "$2", Name: "c", Panes: []tmuxfix.SeedPane{
			{ID: "%2", PID: 12}, {Window: 3, ID: "%0", PID: 10, AdPane: tmuxfix.Token, Shared: true}}})
}

// listing returns sockA's pane listing as "session:pane" entries, in order.
func listing(t *testing.T, r *tmuxfix.Recorder) []string {
	t.Helper()
	panes, err := r.ListPanes(sockA)
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	out := []string{}
	for _, p := range panes {
		out = append(out, p.SessionID+":"+p.ID)
	}
	return out
}

// TestRecorder_SharedPaneListing: list-panes shows a shared pane once per
// session, with that session's window and index and the pane's own pid and label.
func TestRecorder_SharedPaneListing(t *testing.T) {
	r := sharedSeeded()
	got, err := r.ListPanes(sockA)
	if err != nil {
		t.Fatal(err)
	}
	want := []tmux.Pane{
		{SessionID: "$0", ID: "%0", PID: 10, AdPane: tmuxfix.Token},
		{SessionID: "$0", Index: 1, ID: "%1", PID: 11},
		{SessionID: "$1", ID: "%0", PID: 10, AdPane: tmuxfix.Token},
		{SessionID: "$2", ID: "%2", PID: 12},
		{SessionID: "$2", Window: 3, ID: "%0", PID: 10, AdPane: tmuxfix.Token},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListPanes = %+v, want %+v", got, want)
	}
	for _, s := range r.Sessions(sockA) {
		for _, p := range s.Panes {
			if p.Shared {
				t.Errorf("Sessions: %s lists %+v with Shared set", s.ID, p)
			}
		}
	}
	reply, err := r.NewSession(sockA, "n", "/tmp", nil, nil, tmuxfix.Token, agent, tmuxfix.StoreID)
	if err != nil || reply.PaneID != "%3" {
		t.Errorf("next pane = %q, %v; want %%3 (a shared listing adds no pane)", reply.PaneID, err)
	}
}

// TestRecorder_SharedPaneKills: a pane kill removes every listing (a session
// left empty goes); a session kill removes its listing only; an unknown id is
// FailUnrecognized with no first line. Another socket's table is untouched.
func TestRecorder_SharedPaneKills(t *testing.T) {
	cases := []struct {
		name  string
		kills func(r *tmuxfix.Recorder) error
		want  []string
	}{
		{"unknown-session", func(r *tmuxfix.Recorder) error { return r.KillSessionID(sockA, "$9") },
			[]string{"$0:%0", "$0:%1", "$1:%0", "$2:%2", "$2:%0"}},
		{"pane-kill", func(r *tmuxfix.Recorder) error { return r.KillPane(sockA, "%0") }, []string{"$0:%1", "$2:%2"}},
		{"kill-first-session", func(r *tmuxfix.Recorder) error { return r.KillSessionID(sockA, "$0") },
			[]string{"$1:%0", "$2:%2", "$2:%0"}},
		{"kill-grouped-session", func(r *tmuxfix.Recorder) error { return r.KillSessionID(sockA, "$1") },
			[]string{"$0:%0", "$0:%1", "$2:%2", "$2:%0"}},
		{"kill-all-but-one", func(r *tmuxfix.Recorder) error {
			if err := r.KillSessionID(sockA, "$0"); err != nil {
				return err
			}
			return r.KillSessionID(sockA, "$2")
		}, []string{"$1:%0"}},
		{"replaced-session", func(r *tmuxfix.Recorder) error {
			r.ReplaceSessionAfter(tmux.CallLookup, sockA, "$1", tmux.Label{})
			_, err := r.Lookup(sockA)
			return err
		}, []string{"$0:%0", "$0:%1", "$3:%3", "$2:%2", "$2:%0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := sharedSeeded().SeedSessions(sockB, sess("$0", "a", tmux.Label{}, false, "%0"))
			if err := tc.kills(r); tc.name == "unknown-session" {
				if ce := callErr(t, err); ce.Failure != tmux.FailUnrecognized || ce.FirstLine != "" || ce.ExitStatus != 1 {
					t.Errorf("CallError = %+v, want FailUnrecognized, no first line, exit 1", ce)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if got := paneCounts(r, sockB); !reflect.DeepEqual(got, map[string]int{"$0": 1}) {
				t.Errorf("other socket's table = %v", got)
			}
			got := listing(t, r)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("listing = %v, want %v", got, tc.want)
			}
			listed := map[string]bool{}
			for _, e := range got {
				listed[strings.SplitN(e, ":", 2)[0]] = true
			}
			for _, sess := range lookup(t, r, sockA).Sessions {
				if !listed[sess.ID] {
					t.Errorf("session %s outlived its last pane", sess.ID)
				}
			}
		})
	}
}

// TestRecorder_SharedPaneLivesUntilUnlisted: once no session lists the pane,
// every pane call on it fails; while one does, sends and captures reach it.
func TestRecorder_SharedPaneLivesUntilUnlisted(t *testing.T) {
	r := sharedSeeded().SetCapture(sockA, "%0", "out")
	for _, id := range []string{"$0", "$1"} {
		if err := r.KillSessionID(sockA, id); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := r.CapturePaneID(sockA, "%0", 5, false); err != nil || got != "out" {
		t.Errorf("capture while $2 lists %%0 = %q, %v", got, err)
	}
	if err := r.KillSessionID(sockA, "$2"); err != nil {
		t.Fatal(err)
	}
	if err := r.SendKeysPane(sockA, "%0", "hi", false); callErr(t, err).Failure != tmux.FailUnrecognized {
		t.Errorf("send after the last listing went = %v, want FailUnrecognized", err)
	}
	if err := r.KillPane(sockA, "%0"); callErr(t, err).Failure != tmux.FailUnrecognized {
		t.Errorf("pane kill after the last listing went = %v, want FailUnrecognized", err)
	}
}

// TestRecorder_SharedPaneSeedPanics: a shared entry must name a held pane
// once per session with its own pid and label; an unshared held id still panics.
func TestRecorder_SharedPaneSeedPanics(t *testing.T) {
	cases := []struct {
		name  string
		panes []tmuxfix.SeedPane
		want  string
	}{
		{"unheld", []tmuxfix.SeedPane{{ID: "%9", Shared: true}}, "not on"},
		{"no-id", []tmuxfix.SeedPane{{Shared: true}}, "not on"},
		{"twice-in-session", []tmuxfix.SeedPane{{ID: "%0", Shared: true}, {Index: 1, ID: "%0", Shared: true}}, "twice"},
		{"other-pid", []tmuxfix.SeedPane{{ID: "%0", PID: 99, Shared: true}}, "other than the pane's"},
		{"other-label", []tmuxfix.SeedPane{{ID: "%0", AdPane: tmuxfix.OtherToken, Shared: true}}, "other than the pane's"},
		{"unshared-held", []tmuxfix.SeedPane{{ID: "%0"}}, "already exists"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := sharedSeeded()
			defer func() {
				msg, _ := recover().(string)
				if !strings.Contains(msg, tc.want) {
					t.Errorf("panic = %q, want one containing %q", msg, tc.want)
				}
			}()
			r.SeedSessions(sockA, tmuxfix.SeedSession{Name: "v", Panes: tc.panes})
		})
	}
}
