package realtmux_test

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Two agent-director stores on one real tmux server (SRD SR-3.4, SR-3.5,
// SR-3.10, SR-20.7; WD 2026-09-29 STORE; AC-LKP-20): the five-field label,
// the other-store verdict and holder class, two stores under one instance id,
// and an empty store id. Label values are compared, never printed.

// otherStoreWords are the case words of an other-store name holder (SR-3.10).
const otherStoreWords = "another agent-director store"

// twoStores creates, on f's server, this store's agent and another store's
// agent under one instance id; sameToken gives both the same launch token.
func twoStores(t *testing.T, f *lookupFix, sameToken bool) (a, b agent) {
	t.Helper()
	a = f.agent(t, createSpec{InstanceID: newInstanceID("shared")})
	spec := createSpec{InstanceID: a.InstanceID, StoreID: tmuxfix.OtherStoreID}
	if sameToken {
		spec.Token = a.Token
	}
	b = f.agent(t, spec)
	if b.Server.PID != a.Server.PID || b.Server.Start != a.Server.Start {
		t.Fatalf("the two stores' agents run on different servers (pids %d and %d)", a.Server.PID, b.Server.PID)
	}
	return a, b
}

// assertOtherStoreHolder checks the holder class gives the other-store case
// words.
func assertOtherStoreHolder(t *testing.T, what string, got tmux.Result) {
	t.Helper()
	if w := got.HolderClass.CaseWords(); w != otherStoreWords {
		t.Errorf("%s: holder case words = %q, want %q", what, w, otherStoreWords)
	}
}

// TestLookupStoreFiveFieldLabel: the production create writes ad1 <token>
// <$N> <id> <store id> for either store (a spaced id too); its store's row is Ours.
func TestLookupStoreFiveFieldLabel(t *testing.T) {
	f := newLookupFix(t)
	cases := []struct {
		name string
		spec createSpec
	}{
		{"this store", createSpec{}},
		{"this store, id with spaces", createSpec{InstanceID: newInstanceID("agent with spaces")}},
		{"other store", createSpec{StoreID: tmuxfix.OtherStoreID}},
		{"other store, id with spaces", createSpec{InstanceID: newInstanceID("agent with spaces"), StoreID: tmuxfix.OtherStoreID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := f.agent(t, tc.spec)
			id := a.Reply.SessionID
			if f.label(t, id) != tmuxfix.LabelValue(a.Token, id, a.InstanceID, a.StoreID) {
				t.Errorf("session %s: raw @ad_owner is not the five-field value ending with the store id it was given", id)
			}
			got := f.lookup(a.row(withStoreID(a.StoreID)), a.Name)
			assertResult(t, "row of the creating store", got, wantResult{
				Verdict: tmux.Ours, Token: "ours", Session: id,
				Holder: id, HolderClass: tmux.ClassCurrent, Server: tmux.ServerMatch,
			})
			want := tmuxfix.Valid(a.Token, a.InstanceID, a.StoreID)
			if l := got.Session.Label; l != want {
				t.Errorf("session %s: listed label = {Kind %d, token match %v, id match %v, store id match %v}, want Kind %d",
					id, l.Kind, l.Token == want.Token, l.InstanceID == want.InstanceID, l.StoreID == want.StoreID, want.Kind)
			}
		})
	}
}

// TestLookupStoreOtherStoreReadsGone: another store's session with the row's
// id reads Gone for this store's row, whatever its token; its holder class is other store.
func TestLookupStoreOtherStoreReadsGone(t *testing.T) {
	f := newLookupFix(t)
	b := f.agent(t, createSpec{StoreID: tmuxfix.OtherStoreID})
	cases := []struct {
		name   string
		opts   []rowOption
		server string
	}{
		{"row token", nil, tmux.ServerMatch},
		{"another token", []rowOption{withToken(newToken(t))}, tmux.ServerMatch},
		{"no token", []rowOption{withToken("")}, tmux.ServerMatch},
		{"row token, no identity recorded", []rowOption{withoutIdentity()}, tmux.ServerUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := f.lookup(b.row(tc.opts...), b.Name)
			assertResult(t, "this store's row", got, wantResult{
				Verdict: tmux.Gone, Token: "gone",
				Holder: b.Reply.SessionID, HolderClass: tmux.ClassOtherStore, Server: tc.server,
			})
			assertOtherStoreHolder(t, "other store's session", got)
			if got.Adopt {
				t.Errorf("Adopt = true for a Gone row")
			}
		})
	}
}

// TestLookupStoreSameIDEachStoreOurs: both stores' agents under one id on one
// server; each store's row is Ours with its own session only, before and after renames.
func TestLookupStoreSameIDEachStoreOurs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		sameToken bool
	}{{"different tokens", false}, {"same token", true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLookupFix(t)
			a, b := twoStores(t, f, tc.sameToken)
			aName, bName := a.Name, b.Name
			check := func(step string) {
				t.Helper()
				gotA := f.lookup(a.row(), bName)
				assertResult(t, step+": this store's row", gotA, wantResult{
					Verdict: tmux.Ours, Token: "ours", Session: a.Reply.SessionID,
					Holder: b.Reply.SessionID, HolderClass: tmux.ClassOtherStore, Server: tmux.ServerMatch,
				})
				assertOtherStoreHolder(t, step+": this store's row", gotA)
				gotB := f.lookup(b.row(withStoreID(tmuxfix.OtherStoreID)), aName)
				assertResult(t, step+": other store's row", gotB, wantResult{
					Verdict: tmux.Ours, Token: "ours", Session: b.Reply.SessionID,
					Holder: a.Reply.SessionID, HolderClass: tmux.ClassOtherStore, Server: tmux.ServerMatch,
				})
				assertOtherStoreHolder(t, step+": other store's row", gotB)
			}
			check("created")
			aName = uniqueName()
			f.must(t, "rename-session", "-t", a.Reply.SessionID, aName)
			check("this store's session renamed")
			bName = uniqueName()
			f.must(t, "rename-session", "-t", b.Reply.SessionID, bName)
			check("both sessions renamed")
		})
	}
}

// TestLookupStoreEmptyStoreIDNeverOurs: a row with an empty store id reads
// Gone against both stores' sessions for its id, whatever its token.
func TestLookupStoreEmptyStoreIDNeverOurs(t *testing.T) {
	f := newLookupFix(t)
	a, b := twoStores(t, f, false)
	cases := []struct {
		name   string
		token  string
		holder agent
	}{
		{"this store's token", a.Token, a},
		{"other store's token", b.Token, b},
		{"no token", "", a},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := rowFor(a.InstanceID, tc.token, f.Socket, a.Server, withStoreID(""))
			got := f.lookup(row, tc.holder.Name)
			assertResult(t, "row with no store id", got, wantResult{
				Verdict: tmux.Gone, Token: "gone",
				Holder: tc.holder.Reply.SessionID, HolderClass: tmux.ClassOtherStore, Server: tmux.ServerMatch,
			})
		})
	}
}
