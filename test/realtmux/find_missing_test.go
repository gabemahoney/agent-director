package realtmux_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// find-missing through the production pkg/api client on real tmux (SRD
// SR-3.8, SR-11.1, SR-11.3, SR-20.7; PRD AC-FM-19, AC-LKP-08 real-tmux half,
// AC-CLS-02 find-missing half), on Epic 10's kill fixture.

// fmSweep is one find-missing run: its result and, when recorded, every tmux
// call the client made.
type fmSweep struct {
	Res   api.FindMissingResult
	Calls []recordedCall
}

// findMissing runs find-missing through a client from open, with the grace
// period and sweep budget at config defaults; recorded runs tmux through the
// harness's recording wrapper (/bin/sh, so not for locale cases).
func (f *killFix) findMissing(t testing.TB, recorded bool) fmSweep {
	t.Helper()
	bin, log := "", (*callLog)(nil)
	if recorded {
		_, log = newRecordingClient(t)
		bin = filepath.Join(log.dir, "tmux-recorder")
	}
	c := f.open(t, 0, bin)
	defer c.Close() //nolint:errcheck
	res, err := c.FindMissing(context.Background())
	if err != nil {
		t.Fatalf("find-missing: %s", describe(err))
	}
	s := fmSweep{Res: res}
	if log != nil {
		s.Calls = log.calls(t)
	}
	return s
}

// assertListed checks where the sweep put id: "ids", "unverified_ids" or ""
// for neither list.
func (s fmSweep) assertListed(t testing.TB, id, want string) {
	t.Helper()
	var got []string
	if slices.Contains(s.Res.IDs, id) {
		got = append(got, "ids")
	}
	if slices.Contains(s.Res.UnverifiedIDs, id) {
		got = append(got, "unverified_ids")
	}
	if strings.Join(got, ",") != want {
		t.Errorf("row %s listed in %v, want %q", id, got, want)
	}
}

// assertStateNote checks the stored state and liveness note of id (note ""
// is none).
func (f *killFix) assertStateNote(t testing.TB, id, state, note string) {
	t.Helper()
	row := readRow(t, f.DBPath, id)
	gotNote := ""
	if row.LivenessNote != nil {
		gotNote = fmt.Sprint(row.LivenessNote)
	}
	if fmt.Sprint(row.State) != state || gotNote != note {
		t.Errorf("row %s: state %v, note %q; want %s, %q", id, row.State, gotNote, state, note)
	}
}

// TestFindMissingRemainOnExitDeadAgentMarked: a dead agent whose session
// remain-on-exit keeps is marked missing with no tmux call; the session stays.
func TestFindMissingRemainOnExitDeadAgentMarked(t *testing.T) {
	f := newKillFix(t)
	r := f.liveRow(t, killRowSpec{})
	f.must(t, "set-option", "-w", "-t", r.Reply.SessionID, "remain-on-exit", "on")
	label := f.label(t, r.Reply.SessionID)
	if err := syscallKill(r.Reply.PanePID); err != nil {
		t.Fatalf("end the agent's pane process %d: %v", r.Reply.PanePID, err)
	}
	waitFor(t, "agent's pane reads pane_dead 1",
		func() bool { return f.format(t, r.Reply.PaneID, "#{pane_dead}") == "1" },
		func() string { return "pane_dead " + f.format(t, r.Reply.PaneID, "#{pane_dead}") })

	s := f.findMissing(t, true)
	s.assertListed(t, r.InstanceID, "ids")
	f.assertStateNote(t, r.InstanceID, "missing", "")
	if len(s.Calls) != 0 { // proc_absent is decided on the process alone (SR-11.1)
		t.Errorf("find-missing made %d tmux calls, want none for a dead recorded process", len(s.Calls))
	}
	f.assertSession(t, r.Reply.SessionID, true)
	if f.label(t, r.Reply.SessionID) != label {
		t.Errorf("session %s label changed across the sweep", r.Reply.SessionID)
	}
	if got := f.format(t, r.Reply.PaneID, "#{pane_dead}"); got != "1" {
		t.Errorf("agent's pane after the sweep: pane_dead %s, want 1 (left in place)", got)
	}
}

// TestFindMissingNonASCIIRowsUnderHostileLocale: under LC_ALL=C and with no locale variables, lost-reply
// rows for ü-x and agent-ü1 adopt their panes and stay live; a sessionless control row is marked.
func TestFindMissingNonASCIIRowsUnderHostileLocale(t *testing.T) {
	forms := tmuxfix.LocaleForms() // [0] U2/U3's name ü-x, [1] its instance id agent-ü1
	name, idForm := forms[0], forms[1]
	cases := []struct {
		desc string
		set  []string // KEY=VALUE set after every locale variable is removed
	}{
		{desc: "LC_ALL=C", set: []string{"LC_ALL=C"}},
		{desc: "no locale variables"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			clearLocale(t)
			for _, kv := range tc.set {
				k, v, _ := strings.Cut(kv, "=")
				t.Setenv(k, v)
			}
			f := newKillFix(t) // a fresh private socket: the create starts its server under this locale
			rows := []killRow{
				f.liveRow(t, killRowSpec{Name: name.Exact, NoPane: true}),
				f.liveRow(t, killRowSpec{InstanceID: newInstanceID(idForm.Exact), NoPane: true}),
			}
			if out := f.raw().withoutU().must(t, "list-sessions", "-F", "#{session_name}"); strings.Contains(out, name.Exact) {
				t.Fatalf("list-sessions without -u shows %q exactly: the case's locale is not in effect", name.Exact)
			}
			srv := rows[0].Server
			control := newInstanceID("agent")
			f.seedRow(t, control, uniqueName(), "", store.LaunchIdentity{Token: newToken(t), Socket: f.Socket,
				ServerPID: srv.PID, ServerStart: srv.Start, ServerStarttime: srv.Starttime})

			s := f.findMissing(t, false)
			for _, r := range rows {
				s.assertListed(t, r.InstanceID, "")
				f.assertStateNote(t, r.InstanceID, "waiting", "")
				got := readRow(t, f.DBPath, r.InstanceID)
				if gotPane, want := fmt.Sprint(got.PaneID, " ", got.PanePID, " ", got.PaneStarttime),
					fmt.Sprint(r.Reply.PaneID, " ", r.Reply.PanePID, " ", r.PaneStart); gotPane != want {
					t.Errorf("row %s pane after the sweep = %s, want %s (adopted from @ad_pane)", r.InstanceID, gotPane, want)
				}
				f.assertSession(t, r.Reply.SessionID, true)
				assertProcs(t, false, r.Reply.PanePID)
			}
			s.assertListed(t, control, "ids")
			f.assertStateNote(t, control, "missing", "")
		})
	}
}

// TestFindMissingSocketDeniedStopsCalls: with the socket at mode 000, rows with no process identity
// stay live with process_not_seen_tmux_unchecked after one refused tmux call.
func TestFindMissingSocketDeniedStopsCalls(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: root's access ignores the socket's mode 000, so tmux would still connect")
	}
	f := newKillFix(t)
	rows := []killRow{f.liveRow(t, killRowSpec{NoPane: true}), f.liveRow(t, killRowSpec{NoPane: true})}
	chmodSocket(t, f.Socket, 0o000)

	s := f.findMissing(t, true)
	chmodSocket(t, f.Socket, 0o600)
	if len(s.Res.IDs) != 0 {
		t.Errorf("ids = %v, want none marked", s.Res.IDs)
	}
	for _, r := range rows {
		s.assertListed(t, r.InstanceID, "unverified_ids")
		f.assertStateNote(t, r.InstanceID, "waiting", "process_not_seen_tmux_unchecked")
		f.assertSession(t, r.Reply.SessionID, true)
		assertProcs(t, false, r.Reply.PanePID)
	}
	if len(s.Calls) != 1 || s.Calls[0].Exit == 0 {
		t.Errorf("find-missing made %d tmux calls (%v); want one, refused, then no more on the socket", len(s.Calls), callArgs(s.Calls))
	}
}

// callArgs lists each recorded call's tmux command word (the argument after
// -S <socket>), for failure messages; no other argument is printed.
func callArgs(calls []recordedCall) []string {
	var out []string
	for _, c := range calls {
		word := "?"
		if i := slices.Index(c.Args, "-S"); i >= 0 && i+2 < len(c.Args) {
			word = c.Args[i+2]
		}
		out = append(out, word)
	}
	return out
}
