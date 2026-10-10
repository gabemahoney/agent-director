package api_test

// sendkeys_trail_test.go covers send-keys' trail at the API level (SR-7.4,
// SR-14, SR-15): one ad.send_keys.called per Client.SendKeys call with its
// outcome by name and row_state, and ad.provenance.disagree (source
// ad_send_keys) at most once per reason per call on both entry points of
// send-keys and pause. A closed Client is TestPaneVerbsUnknownIDAndClosedClient's,
// the unwritable trail TestTrailFailOpen's.

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// sktCalled returns id's ad.send_keys.called records, in write order.
func sktCalled(t *testing.T, id string) []map[string]any {
	t.Helper()
	return pendTrail(t, "ad.send_keys.called", id)
}

// sktDisagrees returns id's ad.provenance.disagree records written by send-keys.
func sktDisagrees(t *testing.T, id string) []map[string]any {
	t.Helper()
	return verbDisagrees(t, "send-keys", id)
}

// sktSeed seeds a case's row and whatever it needs before the call.
type sktSeed func(*testing.T, *killEnv) killRow

// sktRow seeds a row from spec, then runs setups on it.
func sktRow(spec killRowSpec, setups ...func(*testing.T, *killEnv, *killRow)) sktSeed {
	return func(t *testing.T, e *killEnv) killRow {
		r := e.seedRow(t, spec)
		for _, s := range setups {
			s(t, e, &r)
		}
		return r
	}
}

// sktPending seeds a fresh spawn's pending row with shape v.
func sktPending(v pendingShape) sktSeed {
	return func(t *testing.T, e *killEnv) killRow { return e.seedPending(t, pendingFresh, v) }
}

// sktUnusablePending seeds a fresh spawn's pending row (launch start, token,
// then opts) recording the pre-b.gqe default name.
func sktUnusablePending(opts ...apitest.SpawnOption) sktSeed {
	return func(t *testing.T, e *killEnv) killRow {
		return e.seedUnusableRow(t, e.pendingSpec(pendingOurs, opts...), unusableFixture(t, "pre-b.gqe default name"))
	}
}

// sktHeld gives r one permission request in its relay window at the fixture
// clock: its relay hook may still answer it (b.146 rule 7).
func sktHeld(t *testing.T, e *killEnv, r *killRow) {
	storefix.SeedOpenPermissionRequests(t, e.st, r.ID, []string{storefix.TestRequestTokenA})
}

// sktReleased gives r one permission request past its relay window at the
// fixture clock, which Client.SendKeys judges the guard by: it has fallen back.
func sktReleased(t *testing.T, e *killEnv, r *killRow) {
	storefix.SeedOpenPermissionRequests(t, e.st, r.ID, []string{storefix.TestRequestTokenA})
	storefix.SeedUndeliverablePermissionRequest(t, e.st, e.dbPath, r.ID, storefix.TestRequestTokenA,
		time.Since(e.clock.Now())+2*sendKeysWindow())
}

// sktLeftover replaces r's session with one carrying an earlier launch's label.
func sktLeftover(t *testing.T, e *killEnv, r *killRow) {
	e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.old(), true))
}

// sktDuplicate seeds a second session carrying r's current label.
func sktDuplicate(_ *testing.T, e *killEnv, r *killRow) {
	e.rec.SeedSessions(r.Socket, tmuxfix.SeedSession{Name: "dup", Label: r.current()})
}

// sktSessionGoesAfterListing removes r's session once the pane listing
// returns; a bystander keeps the server holding a session.
func sktSessionGoesAfterListing(t *testing.T, e *killEnv, r *killRow) {
	e.seedBystander(t, r.Socket)
	e.rec.RemoveSessionAfter(tmux.CallListPanes, r.Socket, r.Session.ID)
}

// sktRebindAfterText binds a new server on r's socket once the text call returns.
func sktRebindAfterText(t *testing.T, e *killEnv, r *killRow) {
	e.rec.AfterCall(tmux.CallSendText, func(tmuxfix.SocketCall, error) { ktrRebind(t, e, r) })
}

// TestSendKeysTrailCalledPerReturnPath: every return path of Client.SendKeys
// writes exactly one ad.send_keys.called, outcome by name, row_state the state read.
func TestSendKeysTrailCalledPerReturnPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name              string
		seed              sktSeed // nil: an unknown id
		allow             bool
		outcome, rowState string
		guard             string // "" is not-applicable
		unusable          bool   // an unusable recorded name: no ad.provenance.disagree either
	}{
		{name: "unknown id", outcome: "ErrSpawnNotFound"},
		{name: "finished row", seed: sktRow(killRowSpec{State: store.StateEnded, NoSession: true}), allow: true,
			outcome: "ErrSpawnNotInteractive", rowState: "ended"},
		{name: "pending without allow_pending", seed: sktPending(pendingOurs),
			outcome: "ErrSpawnNotInteractive", rowState: "pending"},
		{name: "pending with no launch start", seed: func(t *testing.T, e *killEnv) killRow {
			return e.seedRow(t, e.pendingSpec(pendingOurs, apitest.WithNoLaunchStartedAt()))
		}, allow: true, outcome: "ErrSpawnNotInteractive", rowState: "pending"},
		{name: "relay held", seed: sktRow(killRowSpec{State: store.StateCheckPermission, RelayOn: true}, sktHeld),
			outcome: "ErrSendKeysWhileRelayed", rowState: "check_permission", guard: "held"},
		{name: "relay fallen back", seed: sktRow(killRowSpec{State: store.StateCheckPermission, RelayOn: true}, sktReleased),
			outcome: "ErrRelayFallenBack", rowState: "check_permission", guard: "held"},
		{name: "relay released", seed: sktRow(killRowSpec{State: store.StateCheckPermission, RelayOn: true}),
			outcome: "ok", rowState: "check_permission", guard: "released"},
		{name: "ours, live row", seed: sktRow(killRowSpec{}), outcome: "ok", rowState: "waiting"},
		{name: "ours, pending row", seed: sktPending(pendingOurs), allow: true, outcome: "ok", rowState: "pending"},
		{name: "ours, pending row turned live by a SessionStart after the lookup: row_state the state read",
			seed: func(t *testing.T, e *killEnv) killRow {
				r := e.seedPending(t, pendingFresh, pendingOurs)
				e.sessionStartAfter(t, tmux.CallLookup, r, "sess-"+uuid.NewString()[:8])
				return r
			}, allow: true, outcome: "ok", rowState: "pending"},
		{name: "pending leftover", seed: sktPending(pendingLeftover), allow: true,
			outcome: "ErrSpawnNotInteractive", rowState: "pending"},
		{name: "live leftover", seed: sktRow(killRowSpec{NoSession: true}, sktLeftover),
			outcome: "ErrTmuxSessionConflict", rowState: "waiting"},
		{name: "pane not found", seed: sktRow(killRowSpec{NoSession: true}, rpnSeedPane),
			outcome: "ErrTmuxSessionConflict", rowState: "waiting"},
		{name: "gone", seed: sktRow(killRowSpec{NoSession: true}), outcome: "ErrTmuxSendKeys", rowState: "waiting"},
		{name: "different server", seed: sktRow(killRowSpec{}, ktrRebind),
			outcome: "ErrTmuxNotAvailable", rowState: "waiting"},
		{name: "conflicting labels", seed: sktRow(killRowSpec{}, sktDuplicate),
			outcome: "ErrTmuxSessionConflict", rowState: "waiting"},
		{name: "unreadable lookup", seed: sktRow(killRowSpec{}, ktrScript(tmux.FailTimeout, tmux.CallLookup)),
			outcome: "ErrTmuxUnresponsive", rowState: "waiting"},
		{name: "tmux unavailable", seed: sktRow(killRowSpec{}, ktrScript(tmux.FailUnavailable, tmux.CallLookup)),
			outcome: "ErrTmuxNotAvailable", rowState: "waiting"},
		{name: "text call timed out", seed: sktRow(killRowSpec{}, ktrScript(tmux.FailTimeout, tmux.CallSendText)),
			outcome: "ErrTmuxUnresponsive", rowState: "waiting"},
		{name: "Enter call timed out", seed: sktRow(killRowSpec{}, ktrScript(tmux.FailTimeout, tmux.CallSendEnter)),
			outcome: "ErrTmuxUnresponsive", rowState: "waiting"},
		{name: "follow-up ours", seed: sktRow(killRowSpec{}, ktrScript(tmux.FailUnrecognized, tmux.CallSendText)),
			outcome: "ErrTmuxUnresponsive", rowState: "waiting"},
		{name: "follow-up gone", seed: sktRow(killRowSpec{}, sktSessionGoesAfterListing),
			outcome: "ErrTmuxSendKeys", rowState: "waiting"},
		{name: "follow-up different server",
			seed:    sktRow(killRowSpec{}, ktrScript(tmux.FailUnrecognized, tmux.CallSendText), sktRebindAfterText),
			outcome: "ErrTmuxNotAvailable", rowState: "waiting"},
		{name: "unusable name, waiting", seed: func(t *testing.T, e *killEnv) killRow {
			return e.seedUnusableRow(t, killRowSpec{}, unusableFixture(t, "empty"))
		}, unusable: true, outcome: "ErrInternal", rowState: "waiting"},
		{name: "unusable name, pending", seed: sktUnusablePending(), allow: true, unusable: true,
			outcome: "ErrInternal", rowState: "pending"},
		{name: "unusable name, pending with no launch start", seed: sktUnusablePending(apitest.WithNoLaunchStartedAt()),
			allow: true, unusable: true, outcome: "ErrSpawnNotInteractive", rowState: "pending"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			id := "sk-unknown-" + uuid.NewString()[:8]
			if tc.seed != nil {
				id = tc.seed(t, e).ID
			}
			p := api.SendKeysParams{ClaudeInstanceID: id, Text: "hi", AllowPending: tc.allow}

			_, _, err := e.sendKeysClient(t, p)

			recs := sktCalled(t, id)
			if len(recs) != 1 {
				t.Fatalf("ad.send_keys.called records = %d; want 1: %v", len(recs), recs)
			}
			guard := tc.guard
			if guard == "" {
				guard = "not-applicable"
			}
			sktAssertCalled(t, recs[0], p, tc.outcome, tc.rowState, guard)
			if name, _ := errnames.Classify(err); (err == nil) != (tc.outcome == "ok") || (err != nil && name != tc.outcome) {
				t.Errorf("err = %v (class %q); want outcome %s", err, name, tc.outcome)
			}
			if d := sktDisagrees(t, id); tc.unusable && len(d) != 0 {
				t.Errorf("ad.provenance.disagree records = %v; want none", d)
			}
		})
	}
}

// sktAssertCalled checks one ad.send_keys.called record of the call with p.
func sktAssertCalled(t *testing.T, rec map[string]any, p api.SendKeysParams, outcome, rowState, guard string) {
	t.Helper()
	fields := map[string]any{
		"claude_instance_id": p.ClaudeInstanceID, "allow_pending": p.AllowPending, "row_state": rowState,
		"guard_evaluation": guard, "outcome": outcome, "source": "ad_send_keys",
	}
	for k, v := range fields {
		if got, ok := rec[k]; !ok || got != v {
			t.Errorf("%s = %v (present %t); want %v", k, got, ok, v)
		}
	}
	ktrAssertCaller(t, rec)
}

// TestKeysVerbsTrailProvenanceDisagree: on both entry points of send-keys and
// pause, each reason is written once per call (source ad_send_keys) with what
// was typed as its action, never with a label value or another row's id; the
// normal Ours case writes none. Client.SendKeys adds its one
// ad.send_keys.called, holding none of those values (api.SendKeys none);
// pause writes nothing else.
func TestKeysVerbsTrailProvenanceDisagree(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"send-keys", "pause"} {
		cases := keysDisagreeCases()
		if verb == "pause" {
			cases = append(cases, ptrLineClearDisagreeCases()...)
		}
		for _, client := range []bool{false, true} {
			for _, tc := range cases {
				t.Run(fmt.Sprintf("%s/client=%v/%s", verb, client, tc.name), func(t *testing.T) {
					t.Parallel() // each case checks only its own row's records
					e := newKillEnv(t)
					r, other := e.seedKeysDisagreeCase(t, tc)
					mark := trailMark(t)
					if verb == "pause" {
						_ = ptrEntry{client: client}.pause(ptrCancelAfterEnter(t, e), t, e, r.ID)
						assertKeysDisagrees(t, e, pauseDisagrees(t, r.ID), r, "pause", other, tc.want)
						ptrAssertOnlyDisagrees(t, mark, r.ID, false)
						return
					}
					p := api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "hi"}
					if client {
						_, _, _ = e.sendKeysClient(t, p)
					} else {
						_, _ = e.sendKeys(p)
					}
					assertKeysDisagrees(t, e, sktDisagrees(t, r.ID), r, "send-keys", other, tc.want)
					called := sktCalled(t, r.ID)
					if want := map[bool]int{false: 0, true: 1}[client]; len(called) != want {
						t.Fatalf("ad.send_keys.called records = %d; want %d", len(called), want)
					}
					for _, c := range called {
						ktrAssertNoForeignContent(t, c, r.Token, e.storeID, other)
					}
				})
			}
		}
	}
}

// sktFailOpenRuns runs Client.SendKeys on one row per trail shape, ids
// prefix-<name>, and returns one line per call: its error, calls and row
// columns. With a working trail each call wrote its ad.send_keys.called, and
// the adoption, restart and rebind their disagree record.
func sktFailOpenRuns(t *testing.T, prefix string, working bool) []string {
	t.Helper()
	cases := []struct {
		name     string
		pending  bool // a fresh spawn's pending row whose only session is a leftover
		spec     killRowSpec
		setups   []func(*testing.T, *killEnv, *killRow)
		disagree int
	}{
		{name: "ours"},
		{name: "adopted", spec: killRowSpec{NoServerIdentity: true, NoPane: true}, disagree: 1},
		{name: "restarted-text-timeout", disagree: 1,
			setups: []func(*testing.T, *killEnv, *killRow){ktrRestart, ktrScript(tmux.FailTimeout, tmux.CallSendText)}},
		{name: "rebound", setups: []func(*testing.T, *killEnv, *killRow){ktrRebind}, disagree: 1},
		{name: "pending-leftover", pending: true, setups: []func(*testing.T, *killEnv, *killRow){sktLeftover}},
		{name: "gone", spec: killRowSpec{NoSession: true}},
	}
	var lines []string
	for _, tc := range cases {
		e := newKillEnv(t)
		spec := tc.spec
		if tc.pending {
			spec = e.pendingSpec(pendingLeftover)
		}
		spec.ID = prefix + "-" + tc.name
		r := sktRow(spec, tc.setups...)(t, e)
		_, _, err := e.sendKeysClient(t, api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "hi", AllowPending: tc.pending})
		if working && (len(sktCalled(t, r.ID)) != 1 || len(sktDisagrees(t, r.ID)) != tc.disagree) {
			t.Fatalf("working trail: %s wrote %d ad.send_keys.called and %d disagree records; want 1 and %d",
				r.ID, len(sktCalled(t, r.ID)), len(sktDisagrees(t, r.ID)), tc.disagree)
		}
		lines = append(lines, failOpenLine(t, e, tc.name, r.ID, err))
	}
	return lines
}
