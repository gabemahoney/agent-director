package api_test

// spawn_reuse_provenance_test.go covers the ad.provenance.disagree records of
// reuse's old-row lookup (SR-14, SR-3.16, SR-10.2): one per distinct reason
// per call, verb spawn and source ad_spawn, tmux_session_name the row's
// recorded name (never the requested one), action refused or proceeded;
// written before the reset, on a refusal too; never adopted and never
// pid_mismatch. The re-lookup's after "duplicate session" are in
// spawn_reuse_provenance_held_test.go.

import (
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rupCase is one old-row lookup arrangement and the records one reuse writes.
// held seeds a foreign-label holder of the requested name, whose session
// tmux_session_id then names; proceeds says the reuse reaches its reset.
type rupCase struct {
	name             string
	noServerIdentity bool
	opts             []apitest.SpawnOption
	setup            []rpvSetup
	held             bool
	want             []disagreeWant
	proceeds         bool
}

// rupCases is the reason table: every reason the old-row lookup meets, a
// refusal at the new-name pre-check, and the arrangements that write none.
func rupCases() []rupCase {
	unreadable := procfix.Unreadable()
	mismatch := []disagreeWant{{reason: "server_mismatch", server: "differs", verdict: "different_server", action: "refused"}}
	conflict := func(reason string) []disagreeWant {
		return []disagreeWant{{reason: reason, server: "match", verdict: "provenance_conflict", action: "refused"}}
	}
	renamed := func(server, current string) []disagreeWant {
		return []disagreeWant{{reason: "name_changed", server: server, verdict: "ours", action: "refused",
			current: current, ours: true}}
	}
	dup := func(_ *testing.T, e *killEnv, r *resumeRow) {
		e.rec.SeedSessions(r.Socket, tmuxfix.SeedSession{Name: "dup", Label: r.current()})
	}
	restarted := disagreeWant{reason: "server_restarted", server: "restarted", verdict: "gone", action: "proceeded"}
	return []rupCase{
		{name: "normal gone, names free, writes none", proceeds: true},
		{name: "normal ours at the recorded name writes none", setup: []rpvSetup{rpvOwn("")}},
		{name: "SessionStart pid differs from the pane pid writes no pid_mismatch", proceeds: true,
			opts: []apitest.SpawnOption{apitest.WithPID(apitest.TestPanePID + 5)}},
		{name: "ours with no server identity is not adopted", noServerIdentity: true, setup: []rpvSetup{rpvOwn("")}},
		{name: "server_restarted, gone, reuse proceeds", setup: []rpvSetup{rpvRestart}, want: []disagreeWant{restarted},
			proceeds: true},
		{name: "server_restarted, ours on the new server", setup: []rpvSetup{rpvRestart, rpvOwn("")},
			want: []disagreeWant{{reason: "server_restarted", server: "restarted", verdict: "ours", action: "refused", ours: true}}},
		{name: "server_restarted, requested name held: refused at the pre-check", setup: []rpvSetup{rpvRestart}, held: true,
			want: []disagreeWant{{reason: "server_restarted", server: "restarted", verdict: "gone", action: "refused", ours: true}}},
		{name: "server_mismatch, recorded server runs", setup: []rpvSetup{rpvServer("rebind", nil), rpvBystander}, want: mismatch},
		{name: "server_mismatch, recorded server uncheckable",
			setup: []rpvSetup{rpvServer("rebind", &unreadable), rpvBystander}, want: mismatch},
		{name: "duplicate_label", setup: []rpvSetup{rpvOwn(""), dup}, want: conflict("duplicate_label")},
		{name: "scope_value global", setup: []rpvSetup{rpvOwn(""), rpvScope(tmuxfix.ScopeGlobal)}, want: conflict("scope_value")},
		{name: "scope_value server", setup: []rpvSetup{rpvOwn(""), rpvScope(tmuxfix.ScopeServer)}, want: conflict("scope_value")},
		{name: "scope_value global-window", setup: []rpvSetup{rpvOwn(""), rpvScope(tmuxfix.ScopeGlobalWindow)},
			want: conflict("scope_value")},
		{name: "name_changed", setup: []rpvSetup{rpvOwn("renamed-reuse")}, want: renamed("match", "renamed-reuse")},
		{name: "name_changed to the requested name", setup: []rpvSetup{rpvOwn(rutRequested)},
			want: renamed("match", rutRequested)},
		{name: "name_changed with no server identity, not adopted", noServerIdentity: true,
			setup: []rpvSetup{rpvOwn("renamed-reuse")}, want: renamed("unknown", "renamed-reuse")},
	}
}

// seedRUPCase seeds tc's reusable row with another row's label on its
// server, runs tc's setups and places tc's holder; it returns the row, the
// row whose session the records name and that other row's id, which no
// record may hold.
func (e *killEnv) seedRUPCase(t *testing.T, tc rupCase) (reuseRow, killRow, string) {
	t.Helper()
	opts := tc.opts
	if tc.noServerIdentity {
		opts = append(opts, apitest.WithLaunchIdentity(store.LaunchIdentity{Token: newToken(), Socket: e.defaultSocket}))
	}
	r := e.seedReusable(t, agentGone, reuseRowSpec{Age: rceSettled(e), Opts: opts})
	other := "other-" + uuid.NewString()[:8]
	e.rec.SeedSessions(r.Socket, tmuxfix.SeedSession{Name: "foreign-" + uuid.NewString()[:8], Label: r.foreign(other)})
	for _, s := range tc.setup {
		s(t, e, &r.resumeRow)
	}
	named := r.killRow
	if tc.held {
		named.Session = e.seedHolder(t, r.withName(rutRequested), holderForeign)
	}
	return r, named, other
}

// rupAssertNoReason fails when one of recs has reason.
func rupAssertNoReason(t *testing.T, recs []map[string]any, reason string) {
	t.Helper()
	for _, rec := range recs {
		if rec["reason"] == reason {
			t.Errorf("ad.provenance.disagree record with reason %s: %v; want none", reason, rec)
		}
	}
}

// TestSpawnReuseProvenanceDisagree: each old-row lookup reason is written once,
// with the recorded name, before the reset or on a refusal; never adopted or pid_mismatch.
func TestSpawnReuseProvenanceDisagree(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	for _, tc := range rupCases() {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r, named, other := e.seedRUPCase(t, tc)
			w := &hookedReuseStore{st: e.st}
			atReset := -1
			w.beforeReset(func() { atReset = len(verbDisagrees(t, "spawn", r.ID)) })
			before := e.snapshotReuse(t, r)

			_, logs, err := e.reuseWith(t, w, reuseParams(t, r, reuseRequest{Name: rutRequested}))

			if (err == nil) != tc.proceeds {
				t.Fatalf("reuse err = %v (log %q); want proceeds %t", err, logs, tc.proceeds)
			}
			recs := verbDisagrees(t, "spawn", r.ID)
			if len(recs) != len(tc.want) {
				t.Fatalf("ad.provenance.disagree records = %d; want %d: %v", len(recs), len(tc.want), recs)
			}
			newTok, _ := e.columns(t, r.ID).LaunchToken.(string)
			for i, want := range tc.want {
				assertDisagreeRecord(t, recs[i], named, "spawn", "ad_spawn", want)
				ktrAssertNoForeignContent(t, recs[i], rupNonEmpty(r.Token, newTok, e.storeID, other)...)
			}
			rupAssertNoReason(t, recs, "pid_mismatch")
			if n := adoptedRecords(t, "spawn", r.ID); n != 0 {
				t.Errorf("adopted records = %d; want none", n)
			}
			switch {
			case tc.proceeds && atReset != len(tc.want):
				t.Errorf("records written before the reset = %d; want all %d", atReset, len(tc.want))
			case !tc.proceeds && atReset >= 0:
				t.Errorf("the refused reuse reached the reset")
			case !tc.proceeds:
				e.assertWroteNothing(t, before)
			}
		})
	}
}

// rupNonEmpty is values without the empty ones.
func rupNonEmpty(values ...string) []string {
	var out []string
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
