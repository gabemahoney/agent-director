package api_test

// spawn_reuse_provenance_held_test.go covers the ad.provenance.disagree
// records of reuse's re-lookup after "duplicate session" (SR-14, SR-10.4):
// after the create, action the restore's row result, tmux_session_name the
// recorded name, never a reason the old-row lookup already wrote, never
// adopted or pid_mismatch. It runs on rutHeld (arrangeHeld under the
// requested name).

import (
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
)

// rupHeldCase is one "duplicate session" arrangement and its records: pre
// from the old-row lookup, then post from the re-lookup, whose
// tmux_session_id is session's (nil: null).
type rupHeldCase struct {
	name      string
	spec      heldSpec
	restore   func(t *testing.T, e *killEnv, r reuseRow, w *hookedReuseStore) // the restore's result; nil: applied
	pre, post []disagreeWant
	session   func(sc *heldScene) string
}

// rupHeldCases is the re-lookup's reason table: each reason it can meet, a
// reason both lookups meet, the restore results as action, and none.
func rupHeldCases() []rupHeldCase {
	holder := func(sc *heldScene) string { return sc.Holder().ID }
	conflict := func(reason, server string) []disagreeWant {
		return []disagreeWant{{reason: reason, server: server, verdict: "provenance_conflict", action: "restored"}}
	}
	mismatch := func(action string) []disagreeWant {
		return []disagreeWant{{reason: "server_mismatch", server: "differs", verdict: "different_server", action: action}}
	}
	restarted := []disagreeWant{{reason: "server_restarted", server: "restarted", verdict: "gone", action: "proceeded"}}
	rebound := heldSpec{Holder: holderNone, Server: heldServerRebound}
	scope := func(level tmuxfix.ScopeLevel) heldSpec { return heldSpec{Holder: holderCurrent, Scope: level} }
	return []rupHeldCase{
		{name: "no reason at the re-lookup writes none", spec: heldSpec{Holder: holderOld}},
		{name: "server_restarted at both lookups is written once", spec: heldSpec{Holder: holderOld, Server: heldServerRestarted},
			pre: restarted},
		{name: "server_mismatch first seen at the re-lookup", spec: rebound, post: mismatch("restored"), session: holder},
		{name: "server_mismatch with no holder", spec: heldSpec{Holder: holderVanished, Server: heldServerRebound},
			post: mismatch("restored")},
		{name: "server_mismatch, row changed before the restore", spec: rebound, post: mismatch("left_changed"), session: holder,
			restore: func(t *testing.T, e *killEnv, r reuseRow, w *hookedReuseStore) {
				parent := e.seedRelative(t, r.ID, false)
				w.afterReset(func() {
					if err := e.st.SetParentID(r.ID, parent); err != nil {
						t.Errorf("SetParentID: %v", err)
					}
				})
			}},
		{name: "server_mismatch, restore store error", spec: rebound, post: mismatch("still_pending"), session: holder,
			restore: func(t *testing.T, e *killEnv, r reuseRow, _ *hookedReuseStore) {
				storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, r.ID)
			}},
		{name: "duplicate_label", spec: heldSpec{Holder: holderCurrent, OursRenamed: "dup-reuse"},
			post: conflict("duplicate_label", "match"), session: holder},
		{name: "scope_value global", spec: scope(tmuxfix.ScopeGlobal), post: conflict("scope_value", "match"), session: holder},
		{name: "scope_value server", spec: scope(tmuxfix.ScopeServer), post: conflict("scope_value", "match"), session: holder},
		{name: "scope_value global-window", spec: scope(tmuxfix.ScopeGlobalWindow), post: conflict("scope_value", "match"),
			session: holder},
		{name: "name_changed", spec: heldSpec{Holder: holderForeign, OursRenamed: "renamed-reuse"},
			post: []disagreeWant{{reason: "name_changed", server: "match", verdict: "ours", action: "restored",
				current: "renamed-reuse"}},
			session: func(sc *heldScene) string { return sc.Ours.ID }},
		{name: "name_changed: the row's own session holds the requested name",
			spec: heldSpec{Holder: holderCurrent},
			post: []disagreeWant{{reason: "name_changed", server: "match", verdict: "ours", action: "restored",
				current: rutRequested}},
			session: holder},
		{name: "server_restarted at both lookups, scope_value new at the re-lookup",
			spec: heldSpec{Holder: holderCurrent, Scope: tmuxfix.ScopeGlobal, Server: heldServerRestarted},
			pre:  restarted, post: conflict("scope_value", "restarted"), session: holder},
	}
}

// TestSpawnReuseProvenanceAfterDuplicateSession: the re-lookup writes, after the
// create, each reason the old-row lookup did not, once, with the recorded name.
func TestSpawnReuseProvenanceAfterDuplicateSession(t *testing.T) {
	t.Parallel()
	for _, tc := range rupHeldCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Held: true, Age: rceSettled(e)})
			w := &hookedReuseStore{st: e.st}
			if tc.restore != nil {
				tc.restore(t, e, r, w)
			}
			atCreate := -1

			run := e.rutHeld(t, r, tc.spec, w, func() { atCreate = len(verbDisagrees(t, "spawn", r.ID)) })

			if !run.sc.Placed {
				t.Fatalf("the create never answered %q", "duplicate session")
			}
			if atCreate != len(tc.pre) {
				t.Errorf("records written by the create = %d; want the old-row lookup's %d", atCreate, len(tc.pre))
			}
			recs := verbDisagrees(t, "spawn", r.ID)
			if len(recs) != len(tc.pre)+len(tc.post) {
				t.Fatalf("ad.provenance.disagree records = %d; want %d: %v", len(recs), len(tc.pre)+len(tc.post), recs)
			}
			post := r.killRow
			post.Session = tmuxfix.SeedSession{}
			if tc.session != nil {
				post.Session.ID = tc.session(run.sc)
			}
			for i, want := range append(slices.Clone(tc.pre), tc.post...) {
				row := r.killRow
				if i >= len(tc.pre) {
					row, want.ours = post, post.Session.ID != ""
				}
				assertDisagreeRecord(t, recs[i], row, "spawn", "ad_spawn", want)
				ktrAssertNoForeignContent(t, recs[i], rhtForbid(e, run.sc)...)
			}
			rupAssertNoReason(t, recs, "pid_mismatch")
			if n := adoptedRecords(t, "spawn", r.ID); n != 0 {
				t.Errorf("adopted records = %d; want none", n)
			}
		})
	}
}
