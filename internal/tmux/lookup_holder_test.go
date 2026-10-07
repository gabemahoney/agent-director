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
// reported on every verdict that read an answer, and absent on the others or
// when no listed session holds the name; asking for a name changes only the
// holder fields.
func TestLookup_HolderOnEveryVerdict(t *testing.T) {
	n := escapedName(t)
	seedAs := func(k lbl) func(f *lookupFixture) { return func(f *lookupFixture) { f.seedNamed(n.Stored, k) } }
	held := func(v tmux.Verdict, token string, class tmux.LabelClass) lookupWant {
		return lookupWant{Verdict: v, Token: token, Server: tmux.ServerMatch, Holder: at{0}, HolderClass: class}
	}
	type holderCase struct {
		name  string
		opts  []lookupRowOpt
		setup func(f *lookupFixture)
		want  lookupWant
		words string
	}
	ours, old := held(tmux.Ours, "ours", tmux.ClassCurrent), held(tmux.Ours, "ours", tmux.ClassOld)
	ours.Ours, old.Ours, old.Leftovers, old.Holder = at{0}, at{0}, at{1}, at{1}
	leftover := held(tmux.Leftover, "leftover", tmux.ClassOld)
	leftover.Leftovers = at{0}
	restarted := ours
	restarted.Server, restarted.Disagree = tmux.ServerRestarted, []string{tmux.ReasonServerRestarted}
	different := held(tmux.CantTell, "different_server", tmux.ClassForeign)
	different.CantTell, different.Server, different.Disagree = tmux.CantTellDifferentServer, tmux.ServerDiffers, []string{tmux.ReasonServerMismatch}
	duplicate := held(tmux.CantTell, "provenance_conflict", tmux.ClassCurrent)
	duplicate.CantTell, duplicate.Disagree = tmux.CantTellProvenanceConflict, []string{tmux.ReasonDuplicateLabel}
	scope := held(tmux.CantTell, "provenance_conflict", tmux.ClassOld)
	scope.CantTell, scope.Disagree = tmux.CantTellProvenanceConflict, []string{tmux.ReasonScopeValue}
	const lifeWords, storeWords = "left over from an earlier life", "another agent-director store"
	cases := []holderCase{
		{name: "ours/holder-is-own-session", setup: seedAs(lblCurrent), want: ours},
		{name: "ours/holder-old", setup: func(f *lookupFixture) { f.seed(lblCurrent).seedNamed(n.Stored, lblOld) },
			want: old, words: lifeWords},
		{name: "ours/name-not-held", setup: func(f *lookupFixture) { f.seedNamed(dollarNames()[1].Stored, lblCurrent) },
			want: lookupWant{Verdict: tmux.Ours, Token: "ours", Server: tmux.ServerMatch, Ours: at{0}}},
		{name: "leftover/holder-old", setup: seedAs(lblOld), want: leftover, words: lifeWords},
		{name: "ours/server-restarted", setup: func(f *lookupFixture) {
			f.Rec.RestartServer(testSocket, lookupOther)
			f.syncProcs().seedNamed(n.Stored, lblCurrent)
		}, want: restarted},
		{name: "different-server", setup: func(f *lookupFixture) {
			f.Rec.RebindServer(testSocket, lookupOther)
			f.syncProcs().seedNamed(n.Stored, lblForeign)
		}, want: different, words: "a different instance id"},
		{name: "provenance-conflict/duplicate-label",
			setup: func(f *lookupFixture) { f.seedNamed(n.Stored, lblCurrent).seed(lblCurrent) }, want: duplicate},
		{name: "provenance-conflict/scope-value", setup: func(f *lookupFixture) {
			f.seedNamed(n.Stored, lblOld)
			f.Rec.SetScope(testSocket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{SessionID: f.ID(0), Label: f.Label(lblCurrent)})
		}, want: scope, words: lifeWords},
		{name: "gone/no-server-reply",
			setup: func(f *lookupFixture) { f.seedNamed(n.Stored, lblForeign).noServer(tmux.FailNoServer).syncProcs() },
			want:  lookupWant{Verdict: tmux.Gone, Token: "gone", Server: tmux.ServerRestarted}},
		{name: "unreadable", setup: func(f *lookupFixture) { f.seedNamed(n.Stored, lblForeign).fail(tmux.FailTimeout) },
			want: wantUnreadable(tmux.FailTimeout)},
		{name: "tmux-unavailable", setup: func(f *lookupFixture) { f.seedNamed(n.Stored, lblForeign).fail(tmux.FailUnavailable) },
			want: wantUnavailable(tmux.FailUnavailable)},
	}
	for _, g := range []struct {
		name  string
		opts  []lookupRowOpt
		k     lbl
		class tmux.LabelClass
		words string
	}{
		{"holder-foreign", nil, lblForeign, tmux.ClassForeign, "a different instance id"},
		{"holder-other-store-row-id-and-token", nil, lblOtherStore, tmux.ClassOtherStore, storeWords},
		{"holder-other-store-old", nil, lblOtherStoreOld, tmux.ClassOtherStore, storeWords},
		{"holder-other-store-foreign", nil, lblOtherStoreForeign, tmux.ClassOtherStore, storeWords},
		{"row-without-store-id", []lookupRowOpt{rowNoStoreID}, lblCurrent, tmux.ClassOtherStore, storeWords},
		{"holder-invalid-label", nil, lblNone, tmux.ClassNone, "no valid instance id"},
		{"holder-unlabelled", nil, lblUnset, tmux.ClassNone, "no valid instance id"},
	} {
		cases = append(cases, holderCase{name: "gone/" + g.name, opts: g.opts, setup: seedAs(g.k),
			want: held(tmux.Gone, "gone", g.class), words: g.words})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLookupFixture(t, tc.opts...)
			tc.setup(f)
			got := f.expect(n.Raw, tc.want)
			if w := got.HolderClass.CaseWords(); w != tc.words {
				t.Errorf("holder case words %q, want %q", w, tc.words)
			}
			unasked := f.run("")
			got.Holder, got.HolderClass = nil, 0
			if !reflect.DeepEqual(got, unasked) {
				t.Errorf("with the holder %+v, without %+v", got, unasked)
			}
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
