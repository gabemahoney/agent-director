package tmux_test

import (
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Verdict tests of the shared lookup (SR-3.4, SR-3.13; AC-LKP-04, -05, -16,
// -18) over lookupFixture; the server check is lookup_server_test.go's.

// lookupCase is one table case: row options, table setup, holder name, want.
type lookupCase struct {
	name   string
	row    []lookupRowOpt
	setup  func(f *lookupFixture)
	holder string
	want   lookupWant
}

// runLookupCases runs each case once through Lookup and Classify.
func runLookupCases(t *testing.T, cases []lookupCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLookupFixture(t, tc.row...)
			if tc.setup != nil {
				tc.setup(f)
			}
			f.expect(tc.holder, tc.want)
		})
	}
}

// seeded is a setup that seeds one unnamed session per label kind.
func seeded(kinds ...lbl) func(*lookupFixture) {
	return func(f *lookupFixture) { f.seed(kinds...) }
}

// The wants of each verdict and variant; each outcome token is pinned here.
func wantOurs(i int, leftovers ...int) lookupWant {
	return lookupWant{Verdict: tmux.Ours, Token: "ours", Server: tmux.ServerMatch, Ours: at{i}, Leftovers: leftovers}
}

func wantLeftover(leftovers ...int) lookupWant {
	return lookupWant{Verdict: tmux.Leftover, Token: "leftover", Server: tmux.ServerMatch, Leftovers: leftovers}
}

func wantGone(server string, disagree ...string) lookupWant {
	return lookupWant{Verdict: tmux.Gone, Token: "gone", Server: server, Disagree: disagree}
}

func wantConflict(server string, disagree ...string) lookupWant {
	return lookupWant{Verdict: tmux.CantTell, CantTell: tmux.CantTellProvenanceConflict, Token: "provenance_conflict",
		Server: server, Disagree: disagree}
}

func wantUnreadable(cause tmux.Failure) lookupWant {
	return lookupWant{Verdict: tmux.CantTell, CantTell: tmux.CantTellUnreadable, Token: "cant_tell", Cause: cause}
}

func wantUnavailable(cause tmux.Failure) lookupWant {
	return lookupWant{Verdict: tmux.CantTell, CantTell: tmux.CantTellUnavailable, Token: "tmux_unavailable", Cause: cause}
}

// TestLookupOurs: exactly one current label is Ours whatever its name and
// neighbours; old-labelled sessions beside it are leftovers.
func TestLookupOurs(t *testing.T) {
	renamed := func(f *lookupFixture) { f.seedNamed("agent-x-work", lblUnset).seedNamed("renamed", lblCurrent) }
	holderOurs := wantOurs(1)
	holderOurs.Holder, holderOurs.HolderClass = at{0}, tmux.ClassNone
	runLookupCases(t, []lookupCase{
		{name: "one current label", setup: seeded(lblCurrent), want: wantOurs(0)},
		{name: "renamed while another session holds the recorded name", setup: renamed,
			holder: "agent-x-work", want: holderOurs},
		{name: "old labels beside it are leftovers", setup: seeded(lblOld, lblCurrent, lblOld), want: wantOurs(1, 0, 2)},
		{name: "foreign none unset and other-store beside it",
			setup: seeded(lblForeign, lblNone, lblUnset, lblOtherStoreForeign, lblCurrent), want: wantOurs(4)},
		{name: "another store's copy of the current label beside it",
			setup: seeded(lblOtherStore, lblCurrent, lblOtherStoreOld), want: wantOurs(1)},
	})
}

// TestLookupLeftover: no current label and one or more of this store's old
// labels of the row's id; another store's sessions are never listed.
func TestLookupLeftover(t *testing.T) {
	runLookupCases(t, []lookupCase{
		{name: "one old label", setup: seeded(lblOld), want: wantLeftover(0)},
		{name: "several old labels beside foreign and unset",
			setup: seeded(lblOld, lblForeign, lblOld, lblUnset), want: wantLeftover(0, 2)},
		{name: "old label beside other-store labels",
			setup: seeded(lblOtherStore, lblOld, lblOtherStoreOld, lblOtherStoreForeign), want: wantLeftover(1)},
		{name: "no launch token: the token-matching label is old", row: []lookupRowOpt{rowNoToken},
			setup: seeded(lblCurrent), want: wantLeftover(0)},
		{name: "no launch token: every own-id label is old", row: []lookupRowOpt{rowNoToken},
			setup: seeded(lblOld, lblCurrent), want: wantLeftover(0, 1)},
	})
}

// TestLookupGone: no session, or only foreign, none and other-store labels;
// an empty store id matches no label (fail closed).
func TestLookupGone(t *testing.T) {
	match := tmux.ServerMatch
	runLookupCases(t, []lookupCase{
		{name: "no session", row: []lookupRowOpt{rowNoServer}, want: wantGone(tmux.ServerUnknown)},
		{name: "only foreign labels", setup: seeded(lblForeign, lblForeign), want: wantGone(match)},
		{name: "only labels of class none", setup: seeded(lblNone, lblUnset), want: wantGone(match)},
		{name: "every other-store kind", setup: seeded(lblOtherStore, lblOtherStoreOld, lblOtherStoreForeign),
			want: wantGone(match)},
		{name: "empty store id", row: []lookupRowOpt{rowNoStoreID}, setup: seeded(lblCurrent, lblOld),
			want: wantGone(match)},
		{name: "no launch token and only foreign", row: []lookupRowOpt{rowNoToken}, setup: seeded(lblForeign),
			want: wantGone(match)},
		{name: "no launch token and only other store", row: []lookupRowOpt{rowNoToken},
			setup: seeded(lblOtherStore, lblOtherStoreOld), want: wantGone(match)},
	})
}

// TestLookupDuplicateLabel: two current labels are provenance_conflict, and
// no leftovers are reported.
func TestLookupDuplicateLabel(t *testing.T) {
	dup := wantConflict(tmux.ServerMatch, tmux.ReasonDuplicateLabel)
	runLookupCases(t, []lookupCase{
		{name: "two current labels", setup: seeded(lblCurrent, lblCurrent), want: dup},
		{name: "two current labels beside an old one", setup: seeded(lblCurrent, lblOld, lblCurrent), want: dup},
		{name: "beside another store's copy", setup: seeded(lblOtherStore, lblCurrent, lblCurrent), want: dup},
	})
}

// TestLookupScopeValue: a value at any one scope level is provenance_conflict
// for every row on the server, whatever the labels.
func TestLookupScopeValue(t *testing.T) {
	levels := map[string]tmuxfix.ScopeLevel{"global": tmuxfix.ScopeGlobal, "server": tmuxfix.ScopeServer,
		"global-window": tmuxfix.ScopeGlobalWindow}
	values := map[string]func(f *lookupFixture) tmuxfix.ScopeValue{
		"embedding a session's own id": func(f *lookupFixture) tmuxfix.ScopeValue {
			return tmuxfix.ScopeValue{SessionID: f.ID(0), Label: f.Label(lblCurrent)}
		},
		"embedding no session id": func(*lookupFixture) tmuxfix.ScopeValue { return tmuxfix.ScopeValue{} },
	}
	rows := map[string][]lookupRowOpt{"row": nil, "another row": {rowInstanceID("agent-y")}, "no token": {rowNoToken}}
	for level, lv := range levels {
		for value, v := range values {
			for name, opts := range rows {
				t.Run(level+" "+value+" "+name, func(t *testing.T) {
					f := newLookupFixture(t).seed(lblCurrent, lblOld, lblForeign, lblUnset)
					f.Rec.SetScope(testSocket, lv, v(f))
					f.Row = newLookupRow(opts...) // another row on the same server
					f.expect("", wantConflict(tmux.ServerMatch, tmux.ReasonScopeValue))
				})
			}
		}
	}
}

// TestLookupScopeValueNoSessions: a zero-session listing with a scope value
// is provenance_conflict too, with no identity recorded, on the recorded
// server (b.47f) or on a restarted one, which also logs server_restarted.
func TestLookupScopeValueNoSessions(t *testing.T) {
	scoped := func(f *lookupFixture) { f.Rec.SetScope(testSocket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{}) }
	runLookupCases(t, []lookupCase{
		{name: "no identity recorded", row: []lookupRowOpt{rowNoServer}, setup: scoped,
			want: wantConflict(tmux.ServerUnknown, tmux.ReasonScopeValue)},
		{name: "recorded server", setup: scoped, want: wantConflict(tmux.ServerMatch, tmux.ReasonScopeValue)},
		{name: "restarted, recorded server gone", setup: func(f *lookupFixture) {
			f.Rec.RestartServer(testSocket, lookupOther)
			f.syncProcs()
			scoped(f)
		}, want: wantConflict(tmux.ServerRestarted, tmux.ReasonServerRestarted, tmux.ReasonScopeValue)},
	})
}

// TestLookupCallFailures: typed failures of the one call are cant_tell or
// tmux_unavailable with Cause set, whatever the table holds.
func TestLookupCallFailures(t *testing.T) {
	failing := func(fl tmux.Failure) func(*lookupFixture) {
		return func(f *lookupFixture) { f.seedNamed("held", lblCurrent).fail(fl) }
	}
	runLookupCases(t, []lookupCase{
		{name: "timeout", setup: failing(tmux.FailTimeout), holder: "held", want: wantUnreadable(tmux.FailTimeout)},
		{name: "unrecognised reply", setup: failing(tmux.FailUnrecognized), holder: "held",
			want: wantUnreadable(tmux.FailUnrecognized)},
		{name: "binary unavailable", setup: failing(tmux.FailUnavailable), holder: "held",
			want: wantUnavailable(tmux.FailUnavailable)},
		{name: "socket permission", setup: failing(tmux.FailSocketDenied), holder: "held",
			want: wantUnavailable(tmux.FailSocketDenied)},
	})
}

// TestLookupControlCharIDs (SR-3.13): a row whose id holds a control
// character is never Ours or Leftover, even from a hand-built valid label.
func TestLookupControlCharIDs(t *testing.T) {
	for _, id := range []string{"agent\tx", "agent\nx", "agent\x1bx", "agent\x7fx"} {
		t.Run(id, func(t *testing.T) {
			f := newLookupFixture(t, rowInstanceID(id)).seed(lblCurrent, lblOld)
			f.expect("", wantGone(tmux.ServerMatch))
		})
	}
}

// TestLookupNewlineInLabelID: a raw newline in a label's id puts the rest on a
// scope line, so the listing reads provenance_conflict.
func TestLookupNewlineInLabelID(t *testing.T) {
	f := newLookupFixture(t, rowInstanceID("agent\nx"))
	e := tmuxfix.Answer("lookup/label:newline-in-id", "SR-3.13; LFR H6", lookupRecorded.PID, lookupRecorded.Start,
		[]tmuxfix.Listed{{ID: tmuxfix.LabelLineID, Created: lookupRecorded.Start, Name: "a",
			Label: tmuxfix.LabelValue(tmuxfix.Token, tmuxfix.LabelLineID, f.Row.InstanceID, tmuxfix.StoreID)}})
	c, _ := newReplay(t, e)
	f.check(f.runOn(c, ""), wantConflict(tmux.ServerMatch, tmux.ReasonScopeValue))
}

// withAgentEnv gives every running server and pane process of the table the
// row's AGENT_DIRECTOR_INSTANCE_ID.
func withAgentEnv(f *lookupFixture) {
	env := map[string]string{probe.EnvKey: f.Row.InstanceID}
	for _, s := range f.Rec.Servers() {
		if s.Running {
			f.PC.Set(s.PID, procfix.Alive(s.ProcStart).WithEnv(env))
		}
	}
	for _, s := range f.Rec.Sessions(testSocket) {
		for _, p := range s.Panes {
			f.PC.Set(p.PID, procfix.Alive(procstarttimefix.LinuxProcStarttime).WithEnv(env))
		}
	}
}

// TestLookupIgnoresEnvironment (AC-LKP-04): an unlabelled session whose
// processes carry the row's id is never Ours, and no environment is read.
func TestLookupIgnoresEnvironment(t *testing.T) {
	restarted := func(f *lookupFixture) { f.Rec.RestartServer(testSocket, lookupOther); f.syncProcs() }
	cases := []lookupCase{
		{name: "server match", want: wantGone(tmux.ServerMatch)},
		{name: "no identity recorded", row: []lookupRowOpt{rowNoServer}, want: wantGone(tmux.ServerUnknown)},
		{name: "recorded server restarted", setup: restarted,
			want: wantGone(tmux.ServerRestarted, tmux.ReasonServerRestarted)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var results []tmux.Result
			for _, env := range []bool{false, true} {
				f := newLookupFixture(t, tc.row...)
				if tc.setup != nil {
					tc.setup(f)
				}
				f.seed(lblUnset)
				if env {
					withAgentEnv(f)
				}
				results = append(results, f.expect("", tc.want))
				if n := f.PC.EnvReads(); n != 0 {
					t.Errorf("env=%v: %d environment reads, want 0", env, n)
				}
			}
			if !reflect.DeepEqual(results[0], results[1]) {
				t.Errorf("the environment changed the result:\nwithout %+v\nwith    %+v", results[0], results[1])
			}
		})
	}
}
