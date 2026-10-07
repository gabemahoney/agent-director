package main_test

// The pane label @ad_pane over the fake (WD 2026-09-29c): the create chain
// and the label by id set it on one pane, the listing's sixth field reports
// it, and set-option -p argv is checked as tmux would.

import (
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// paneLabels maps each pane id in socket's table to its raw @ad_pane value.
func paneLabels(t *testing.T, socket string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, s := range tables.Read(t, socket).Sessions {
		for _, p := range s.Panes {
			out[p.ID] = p.AdPane
		}
	}
	return out
}

// adPanes maps each listed pane id to its AdPane.
func adPanes(panes []tmux.Pane) map[string]string {
	out := map[string]string{}
	for _, p := range panes {
		out[p.ID] = p.AdPane
	}
	return out
}

// TestCreatePaneLabelOnNewPaneOnly checks a create labels its own new pane
// only: chained, it lists the token; a failed chain or a '$' name, nothing,
// the '$' name's session listed unlabelled under its stored form.
func TestCreatePaneLabelOnNewPaneOnly(t *testing.T) {
	var byID tmuxfix.StoredName
	for _, n := range tmuxfix.StoredNames() {
		if n.LabelByID && byID.Raw == "" {
			byID = n
		}
	}
	cases := []struct {
		name, session string
		inj           *faketmuxfix.Injection
		want          string // the new pane's AdPane
	}{
		{name: "chained", session: "fresh", want: tmuxfix.Token},
		{name: "chain fails", session: "fresh", inj: ptrInj(faketmuxfix.ChainFails())},
		{name: "label-by-id name", session: byID.Raw},
	}
	c := newClient(t, callTimeout)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			socket := seed(t)
			if tc.inj != nil {
				tables.Inject(t, socket, *tc.inj)
			}
			reply, _ := create(t, c, socket, tc.session, "agent-new")
			if reply.PaneID == "" {
				t.Fatalf("create gave no reply")
			}
			wantRaw := map[string]string{"%0": "", "%1": "", "%2": "", reply.PaneID: ""}
			if tc.want != "" {
				wantRaw[reply.PaneID] = tmuxfix.PaneLabelValue(tc.want, reply.PaneID)
			}
			if got := paneLabels(t, socket); !reflect.DeepEqual(got, wantRaw) {
				t.Errorf("stored @ad_pane = %q, want %q", got, wantRaw)
			}
			want := map[string]string{"%0": "", "%1": "", "%2": "", reply.PaneID: tc.want}
			if got := adPanes(listPanes(t, c, socket)); !reflect.DeepEqual(got, want) {
				t.Errorf("listed AdPane = %q, want %q", got, want)
			}
			if tc.session == byID.Raw {
				for _, s := range lookup(t, c, socket).Sessions {
					if s.ID == reply.SessionID && (s.Name != byID.Stored || s.Label != (tmux.Label{})) {
						t.Errorf("session %+v, want named %q and unlabelled", s, byID.Stored)
					}
				}
			}
		})
	}
}

// ptrInj returns a pointer to inj.
func ptrInj(inj faketmuxfix.Injection) *faketmuxfix.Injection { return &inj }

// TestSetLabelSetsPaneLabelByID checks the label by id sets the session
// label, with the caller's store id, then the named pane's label; an unknown
// pane leaves the session labelled, an unknown session changes nothing.
func TestSetLabelSetsPaneLabelByID(t *testing.T) {
	alpha := tmuxfix.Valid(tmuxfix.Token, "agent-a", tmuxfix.StoreID)
	valid := tmuxfix.Valid(tmuxfix.Token, "agent-b", tmuxfix.OtherStoreID)
	old := tmuxfix.PaneLabelValue(tmuxfix.OtherToken, "%1")
	cases := []struct {
		name, session, pane string
		reply               string // catalogue reply of the failure; "": success
		wantLabels          map[string]tmux.Label
		wantPanes           map[string]string // raw @ad_pane per pane id
	}{
		{name: "both steps", session: "$1", pane: "%2", wantLabels: map[string]tmux.Label{"$0": alpha, "$1": valid},
			wantPanes: map[string]string{"%0": "", "%1": old, "%2": tmuxfix.PaneLabelValue(tmuxfix.Token, "%2")}},
		{name: "overwrites an old pane label", session: "$0", pane: "%1", wantLabels: map[string]tmux.Label{"$0": valid, "$1": {}},
			wantPanes: map[string]string{"%0": "", "%1": tmuxfix.PaneLabelValue(tmuxfix.Token, "%1"), "%2": ""}},
		{name: "unknown pane", session: "$1", pane: "%99", reply: "reply/no-such-pane",
			wantLabels: map[string]tmux.Label{"$0": alpha, "$1": valid},
			wantPanes:  map[string]string{"%0": "", "%1": old, "%2": ""}},
		{name: "unknown session", session: "$99", pane: "%2", reply: "reply/no-such-session",
			wantLabels: map[string]tmux.Label{"$0": alpha, "$1": {}},
			wantPanes:  map[string]string{"%0": "", "%1": old, "%2": ""}},
	}
	c := newClient(t, callTimeout)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			socket := newSocket(t)
			tb := seeded()
			tb.Sessions[1].Panes[1].AdPane = old // alpha's %1
			tables.Write(t, socket, tb)
			err := c.SetLabel(socket, tc.session, tc.pane, tmuxfix.Token, "agent-b", tmuxfix.OtherStoreID)
			var e tmuxfix.Entry
			if tc.reply != "" {
				e = tmuxfix.Find(tmuxfix.Replies(socket), tc.reply)
			}
			assertFailure(t, err, tmux.CallSetLabel, e.Want[tmux.CallSetLabel], e)
			if got := paneLabels(t, socket); !reflect.DeepEqual(got, tc.wantPanes) {
				t.Errorf("stored @ad_pane = %q, want %q", got, tc.wantPanes)
			}
			labels := map[string]tmux.Label{}
			for _, s := range lookup(t, c, socket).Sessions {
				labels[s.ID] = s.Label
			}
			if !reflect.DeepEqual(labels, tc.wantLabels) {
				t.Errorf("session labels = %+v, want %+v", labels, tc.wantLabels)
			}
		})
	}
}

// TestPaneLabelShapesListed checks each catalogue pane label form gives its
// AdPane through the client, from a table value and as an injected reply.
func TestPaneLabelShapesListed(t *testing.T) {
	c := newClient(t, callTimeout)
	for _, shape := range tmuxfix.PaneLabelShapes() {
		e := shape.Entry()
		t.Run(shape.Name+"/table", func(t *testing.T) {
			socket := newSocket(t)
			p := e.Panes[0]
			tables.Write(t, socket, faketmuxfix.Table{
				Server: &faketmuxfix.Server{PID: seedPID, Start: seedStart},
				Sessions: []faketmuxfix.Session{{ID: p.SessionID, Created: seedStart, Name: "s",
					Panes: []faketmuxfix.Pane{{ID: p.ID, PID: p.PID, AdPane: shape.Value}}}},
			})
			if got := listPanes(t, c, socket); !reflect.DeepEqual(got, e.Panes) {
				t.Errorf("ListPanes = %+v, want %+v", got, e.Panes)
			}
		})
		t.Run(shape.Name+"/injected", func(t *testing.T) {
			socket := seed(t)
			resolved, ok := faketmuxfix.ResolveEntry(e.Name, socket)
			if !ok {
				t.Fatalf("ResolveEntry(%q) found nothing", e.Name)
			}
			tables.Inject(t, socket, faketmuxfix.Reply(tmux.CallListPanes, resolved))
			if got := listPanes(t, c, socket); !reflect.DeepEqual(got, e.Panes) {
				t.Errorf("ListPanes = %+v, want %+v", got, e.Panes)
			}
		})
	}
}

// runFake runs the fake with -u -S socket and args, returning its exit
// status and standard error.
func runFake(t *testing.T, socket string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(faketmuxfix.Binary(t), append([]string{"-u", "-S", socket}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		t.Fatalf("run fake: %v", err)
	}
	return cmd.ProcessState.ExitCode(), stderr.String()
}

// TestSetOptionPaneArgv checks set-option -p on the seeded table: the
// option must match -p, -F expands #{pane_id}, =<name>: is the first pane,
// and targets that match nothing fail with the recorded lines.
func TestSetOptionPaneArgv(t *testing.T) {
	const value = "v #{pane_id}"
	noSuchPane := tmuxfix.Find(tmuxfix.Replies(""), "reply/no-such-pane").Stderr
	chainFailed := tmuxfix.Find(tmuxfix.CreateReplies(), "create/reply-then-label-failed").Stderr
	cases := []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
		wantPanes  map[string]string
	}{
		{name: "pane id, no -F keeps the value", args: []string{"-p", "-t", "%1", "@ad_pane", value}, wantPanes: map[string]string{"%1": value}},
		{name: "pane id with -F", args: []string{"-p", "-F", "-t", "%2", "@ad_pane", value}, wantPanes: map[string]string{"%2": "v %2"}},
		{name: "chain form is the first pane", args: []string{"-p", "-F", "-t", "=alpha:", "@ad_pane", value},
			wantPanes: map[string]string{"%0": "v %0"}},
		{name: "unknown pane id", args: []string{"-p", "-t", "%99", "@ad_pane", value}, wantExit: 1, wantStderr: noSuchPane},
		{name: "session id is not a pane", args: []string{"-p", "-t", "$0", "@ad_pane", value}, wantExit: 1, wantStderr: noSuchPane},
		{name: "chain form matching nothing", args: []string{"-p", "-t", "=nosuch:", "@ad_pane", value}, wantExit: 1, wantStderr: chainFailed},
		{name: "@ad_owner with -p", args: []string{"-p", "-t", "%0", "@ad_owner", value}, wantExit: 2},
		{name: "@ad_pane without -p", args: []string{"-t", "%0", "@ad_pane", value}, wantExit: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			socket := seed(t)
			exit, stderr := runFake(t, socket, append([]string{"set-option"}, tc.args...)...)
			if exit != tc.wantExit || (tc.wantStderr != "" && stderr != tc.wantStderr) {
				t.Fatalf("exit %d stderr %q, want %d %q", exit, stderr, tc.wantExit, tc.wantStderr)
			}
			want := map[string]string{"%0": "", "%1": "", "%2": ""}
			for id, v := range tc.wantPanes {
				want[id] = v
			}
			if got := paneLabels(t, socket); !reflect.DeepEqual(got, want) {
				t.Errorf("stored @ad_pane = %q, want %q", got, want)
			}
		})
	}
}
