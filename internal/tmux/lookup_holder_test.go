package tmux_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// The name holder of SR-3.10: stored forms, the match on the lookup's
// listing, the holder's class and case words, and more than one match.

// isDollarName reports a catalogue name with $ or \ whose stored form is
// SR-3.10's escaping alone: E.10 N6's '.' and ':' names are also rewritten.
func isDollarName(n tmuxfix.StoredName) bool {
	return strings.ContainsAny(n.Raw, `$\`) && !strings.ContainsAny(n.Raw, ".:")
}

// dollarNames are the catalogue's $ and \ cases (Appendix E N1, N5, F3).
func dollarNames() []tmuxfix.StoredName {
	var out []tmuxfix.StoredName
	for _, n := range tmuxfix.StoredNames() {
		if isDollarName(n) {
			out = append(out, n)
		}
	}
	return out
}

// escapedName is the first catalogue case whose stored form differs from its
// raw name.
func escapedName(t *testing.T) tmuxfix.StoredName {
	t.Helper()
	for _, n := range dollarNames() {
		if n.Stored != n.Raw {
			return n
		}
	}
	t.Fatal("the catalogue has no escaped $ or \\ name")
	return tmuxfix.StoredName{}
}

// TestStoredForms_Catalogue: a $ or \ name gives its raw and stored forms and
// nothing else; any other name gives only itself.
func TestStoredForms_Catalogue(t *testing.T) {
	for _, n := range tmuxfix.StoredNames() {
		if strings.ContainsAny(n.Raw, `$\`) && !isDollarName(n) {
			continue
		}
		t.Run(n.Source+"/"+n.Stored, func(t *testing.T) {
			want := []string{n.Raw}
			if isDollarName(n) && n.Stored != n.Raw {
				want = append(want, n.Stored)
			}
			got := tmux.StoredForms(n.Raw)
			sorted := slices.Clone(got)
			slices.Sort(sorted)
			slices.Sort(want)
			if !slices.Equal(sorted, want) {
				t.Errorf("StoredForms(%q) = %q, want %q", n.Raw, got, want)
			}
		})
	}
}

// TestLookup_HolderStoredForms: a session listed under a $ or \ name's
// catalogue stored form, or under its raw form, holds that name.
func TestLookup_HolderStoredForms(t *testing.T) {
	for _, n := range dollarNames() {
		for _, form := range []struct{ name, stored string }{{"stored", n.Stored}, {"raw", n.Raw}} {
			t.Run(n.Source+"/"+n.Stored+"/"+form.name, func(t *testing.T) {
				f := newLookupFixture(t).seedNamed(form.stored, lblForeign)
				f.expect(n.Raw, lookupWant{Verdict: tmux.Gone, Token: "gone", Server: tmux.ServerMatch,
					Holder: at{0}, HolderClass: tmux.ClassForeign})
			})
		}
	}
}

// TestLookup_HolderOnEveryVerdict: the holder, its class and case words are
// reported on every verdict that read an answer, and absent on the others.
func TestLookup_HolderOnEveryVerdict(t *testing.T) {
	n := escapedName(t)
	cases := []struct {
		name  string
		opts  []lookupRowOpt
		setup func(f *lookupFixture)
		want  lookupWant
		words string
	}{
		{name: "ours/holder-is-own-session",
			setup: func(f *lookupFixture) { f.seedNamed(n.Stored, lblCurrent) },
			want: lookupWant{Verdict: tmux.Ours, Token: "ours", Server: tmux.ServerMatch, Ours: at{0},
				Holder: at{0}, HolderClass: tmux.ClassCurrent}},
		{name: "ours/holder-old",
			setup: func(f *lookupFixture) { f.seed(lblCurrent).seedNamed(n.Stored, lblOld) },
			want: lookupWant{Verdict: tmux.Ours, Token: "ours", Server: tmux.ServerMatch, Ours: at{0},
				Leftovers: at{1}, Holder: at{1}, HolderClass: tmux.ClassOld},
			words: "left over from an earlier life"},
		{name: "leftover/holder-old",
			setup: func(f *lookupFixture) { f.seedNamed(n.Stored, lblOld) },
			want: lookupWant{Verdict: tmux.Leftover, Token: "leftover", Server: tmux.ServerMatch, Leftovers: at{0},
				Holder: at{0}, HolderClass: tmux.ClassOld},
			words: "left over from an earlier life"},
		{name: "gone/holder-foreign",
			setup: func(f *lookupFixture) { f.seedNamed(n.Stored, lblForeign) },
			want: lookupWant{Verdict: tmux.Gone, Token: "gone", Server: tmux.ServerMatch,
				Holder: at{0}, HolderClass: tmux.ClassForeign},
			words: "a different instance id"},
		{name: "gone/holder-other-store-row-id-and-token",
			setup: func(f *lookupFixture) { f.seedNamed(n.Stored, lblOtherStore) },
			want: lookupWant{Verdict: tmux.Gone, Token: "gone", Server: tmux.ServerMatch,
				Holder: at{0}, HolderClass: tmux.ClassOtherStore},
			words: "another agent-director store"},
		{name: "gone/holder-other-store-old",
			setup: func(f *lookupFixture) { f.seedNamed(n.Stored, lblOtherStoreOld) },
			want: lookupWant{Verdict: tmux.Gone, Token: "gone", Server: tmux.ServerMatch,
				Holder: at{0}, HolderClass: tmux.ClassOtherStore},
			words: "another agent-director store"},
		{name: "gone/holder-other-store-foreign",
			setup: func(f *lookupFixture) { f.seedNamed(n.Stored, lblOtherStoreForeign) },
			want: lookupWant{Verdict: tmux.Gone, Token: "gone", Server: tmux.ServerMatch,
				Holder: at{0}, HolderClass: tmux.ClassOtherStore},
			words: "another agent-director store"},
		{name: "gone/row-without-store-id", opts: []lookupRowOpt{rowNoStoreID},
			setup: func(f *lookupFixture) { f.seedNamed(n.Stored, lblCurrent) },
			want: lookupWant{Verdict: tmux.Gone, Token: "gone", Server: tmux.ServerMatch,
				Holder: at{0}, HolderClass: tmux.ClassOtherStore},
			words: "another agent-director store"},
		{name: "gone/holder-invalid-label",
			setup: func(f *lookupFixture) { f.seedNamed(n.Stored, lblNone) },
			want: lookupWant{Verdict: tmux.Gone, Token: "gone", Server: tmux.ServerMatch,
				Holder: at{0}, HolderClass: tmux.ClassNone},
			words: "no valid instance id"},
		{name: "gone/holder-unlabelled",
			setup: func(f *lookupFixture) { f.seedNamed(n.Stored, lblUnset) },
			want: lookupWant{Verdict: tmux.Gone, Token: "gone", Server: tmux.ServerMatch,
				Holder: at{0}, HolderClass: tmux.ClassNone},
			words: "no valid instance id"},
		{name: "ours/server-restarted",
			setup: func(f *lookupFixture) {
				f.Rec.RestartServer(testSocket, lookupOther)
				f.syncProcs().seedNamed(n.Stored, lblCurrent)
			},
			want: lookupWant{Verdict: tmux.Ours, Token: "ours", Server: tmux.ServerRestarted, Ours: at{0},
				Holder: at{0}, HolderClass: tmux.ClassCurrent, Disagree: []string{tmux.ReasonServerRestarted}}},
		{name: "different-server",
			setup: func(f *lookupFixture) {
				f.Rec.RebindServer(testSocket, lookupOther)
				f.syncProcs().seedNamed(n.Stored, lblForeign)
			},
			want: lookupWant{Verdict: tmux.CantTell, CantTell: tmux.CantTellDifferentServer, Token: "different_server",
				Server: tmux.ServerDiffers, Holder: at{0}, HolderClass: tmux.ClassForeign,
				Disagree: []string{tmux.ReasonServerMismatch}},
			words: "a different instance id"},
		{name: "provenance-conflict/duplicate-label",
			setup: func(f *lookupFixture) { f.seedNamed(n.Stored, lblCurrent).seed(lblCurrent) },
			want: lookupWant{Verdict: tmux.CantTell, CantTell: tmux.CantTellProvenanceConflict, Token: "provenance_conflict",
				Server: tmux.ServerMatch, Holder: at{0}, HolderClass: tmux.ClassCurrent,
				Disagree: []string{tmux.ReasonDuplicateLabel}}},
		{name: "provenance-conflict/scope-value",
			setup: func(f *lookupFixture) {
				f.seedNamed(n.Stored, lblOld)
				f.Rec.SetScope(testSocket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{SessionID: f.ID(0), Label: f.Label(lblCurrent)})
			},
			want: lookupWant{Verdict: tmux.CantTell, CantTell: tmux.CantTellProvenanceConflict, Token: "provenance_conflict",
				Server: tmux.ServerMatch, Holder: at{0}, HolderClass: tmux.ClassOld, Disagree: []string{tmux.ReasonScopeValue}},
			words: "left over from an earlier life"},
		{name: "gone/no-server-reply",
			setup: func(f *lookupFixture) { f.seedNamed(n.Stored, lblForeign).noServer(tmux.FailNoServer).syncProcs() },
			want:  lookupWant{Verdict: tmux.Gone, Token: "gone", Server: tmux.ServerRestarted}},
		{name: "unreadable",
			setup: func(f *lookupFixture) { f.seedNamed(n.Stored, lblForeign).fail(tmux.FailTimeout) },
			want: lookupWant{Verdict: tmux.CantTell, CantTell: tmux.CantTellUnreadable, Token: "cant_tell",
				Cause: tmux.FailTimeout}},
		{name: "tmux-unavailable",
			setup: func(f *lookupFixture) { f.seedNamed(n.Stored, lblForeign).fail(tmux.FailUnavailable) },
			want: lookupWant{Verdict: tmux.CantTell, CantTell: tmux.CantTellUnavailable, Token: "tmux_unavailable",
				Cause: tmux.FailUnavailable}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLookupFixture(t, tc.opts...)
			tc.setup(f)
			got := f.expect(n.Raw, tc.want)
			if w := got.HolderClass.CaseWords(); w != tc.words {
				t.Errorf("holder case words %q, want %q", w, tc.words)
			}
		})
	}
}

// TestLookup_HolderNotHeld: a name no listed session holds, or no name, gives
// no holder, no Can't tell and the labels' verdict.
func TestLookup_HolderNotHeld(t *testing.T) {
	names := dollarNames()
	n, other := names[0], names[1]
	cases := []struct {
		name   string
		holder string
		setup  func(f *lookupFixture)
		want   lookupWant
	}{
		{name: "other-name-listed", holder: n.Raw,
			setup: func(f *lookupFixture) { f.seedNamed(other.Stored, lblCurrent) },
			want:  lookupWant{Verdict: tmux.Ours, Token: "ours", Server: tmux.ServerMatch, Ours: at{0}}},
		{name: "no-name-asked", holder: "",
			setup: func(f *lookupFixture) { f.seed(lblOld) },
			want:  lookupWant{Verdict: tmux.Leftover, Token: "leftover", Server: tmux.ServerMatch, Leftovers: at{0}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLookupFixture(t)
			tc.setup(f)
			f.expect(tc.holder, tc.want)
		})
	}
}

// TestLookup_HolderAmbiguous: a name listed both raw and escaped is Can't tell
// for the holder check; the labels still decide the row's verdict.
func TestLookup_HolderAmbiguous(t *testing.T) {
	n := escapedName(t)
	cases := []struct {
		name     string
		raw, esc lbl
		want     lookupWant
	}{
		{name: "ours", raw: lblCurrent, esc: lblOld,
			want: lookupWant{Verdict: tmux.Ours, Token: "ours", Ours: at{0}, Leftovers: at{1}}},
		{name: "leftover", raw: lblForeign, esc: lblOld,
			want: lookupWant{Verdict: tmux.Leftover, Token: "leftover", Leftovers: at{1}}},
		{name: "gone", raw: lblOtherStore, esc: lblNone,
			want: lookupWant{Verdict: tmux.Gone, Token: "gone"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLookupFixture(t).seedNamed(n.Raw, tc.raw).seedNamed(n.Stored, tc.esc)
			tc.want.Server, tc.want.HolderAmbiguous = tmux.ServerMatch, true
			f.expect(n.Raw, tc.want)
		})
	}
}

// TestLookup_HolderNeverChangesVerdict: asking for the name a session holds
// changes only the holder fields of the Result.
func TestLookup_HolderNeverChangesVerdict(t *testing.T) {
	n := escapedName(t)
	kinds := []struct {
		name string
		k    lbl
	}{{"current", lblCurrent}, {"old", lblOld}, {"foreign", lblForeign}, {"other-store", lblOtherStore},
		{"none", lblNone}, {"unset", lblUnset}}
	for _, tc := range kinds {
		t.Run(tc.name, func(t *testing.T) {
			f := newLookupFixture(t).seedNamed(n.Stored, tc.k)
			held, unasked := f.run(n.Raw), f.run("")
			if held.Holder == nil {
				t.Fatalf("no holder for %q listed as %q", n.Raw, n.Stored)
			}
			held.Holder, held.HolderClass = nil, 0
			if !reflect.DeepEqual(held, unasked) {
				t.Errorf("with the holder %+v, without %+v", held, unasked)
			}
		})
	}
}
