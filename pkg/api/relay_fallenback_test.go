package api_test

import (
	"errors"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// relayGuardWindow is the effective relay window the relay-guard tests pass.
// A short window keeps the backdate ages in SeedUndeliverablePermissionRequest
// small while staying well clear of the safety margin.
const relayGuardWindow = time.Hour

// seedRelayRow seeds a relay-on check_permission row with its own labelled
// session and agent pane, and open permission requests for tokens (none when empty).
func seedRelayRow(t *testing.T, e *killEnv, tokens ...string) killRow {
	t.Helper()
	r := e.seedRow(t, killRowSpec{State: store.StateCheckPermission, RelayOn: true})
	if len(tokens) > 0 {
		storefix.SeedOpenPermissionRequests(t, e.st, r.ID, tokens)
	}
	return r
}

// TestRelayFallenBackIncidentRegression re-anchors the b.kk3 incident (SR-7.1):
// a relay-on row whose open request fell out of its window refuses Decide
// with ErrRelayFallenBack, and a later SendKeys delivers into the agent's pane.
func TestRelayFallenBackIncidentRegression(t *testing.T) {
	e := newKillEnv(t)
	r := seedRelayRow(t, e, storefix.TestRequestTokenA)
	storefix.SeedUndeliverablePermissionRequest(t, e.st, e.dbPath, r.ID, storefix.TestRequestTokenA, 2*relayGuardWindow)
	now := time.Now()

	_, err := api.Decide(e.st, relayGuardWindow, now, api.DecideParams{
		ClaudeInstanceID: r.ID, RequestToken: storefix.TestRequestTokenA, Decision: "allow"})
	if !errors.Is(err, api.ErrRelayFallenBack) {
		t.Fatalf("Decide err = %v; want ErrRelayFallenBack", err)
	}
	if _, err := e.sendKeysAt(relayGuardWindow, now, api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "2"}); err != nil {
		t.Fatalf("SendKeys after fallen-back: %v", err)
	}
	e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, "2")
}

// TestSendKeysGuardMultiRowDeliverability pins the per-row guard over several
// open rows (SR-7.3): it holds while any row is in-window, releases once all aged out.
func TestSendKeysGuardMultiRowDeliverability(t *testing.T) {
	tokens := []string{storefix.TestRequestTokenA, storefix.TestRequestTokenB, storefix.TestRequestTokenC}
	cases := []struct {
		name          string
		undeliverable []bool // undeliverable[i] backdates tokens[i] past the window
		wantRefuse    bool
	}{
		{"all_in_window_refuses", []bool{false, false, false}, true},
		{"one_undeliverable_rest_in_window_refuses", []bool{true, false, false}, true},
		{"all_but_one_undeliverable_refuses", []bool{true, true, false}, true},
		{"all_undeliverable_delivers", []bool{true, true, true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := seedRelayRow(t, e, tokens...)
			for i, undeliverable := range tc.undeliverable {
				if undeliverable {
					storefix.SeedUndeliverablePermissionRequest(t, e.st, e.dbPath, r.ID, tokens[i], 2*relayGuardWindow)
				}
			}

			_, err := e.sendKeysAt(relayGuardWindow, time.Now(), api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "1"})
			if tc.wantRefuse {
				if !errors.Is(err, api.ErrSendKeysWhileRelayed) {
					t.Fatalf("err = %v; want ErrSendKeysWhileRelayed", err)
				}
				e.assertNoTmuxCall(t)
				return
			}
			if err != nil {
				t.Fatalf("SendKeys with every row undeliverable: %v", err)
			}
			e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, "1")
		})
	}
}

// TestSendKeysGuardHoldsForDecidedInWindowRow pins SR-4.2: a sole permission
// row decided but still in its window keeps the guard shut; no tmux call.
func TestSendKeysGuardHoldsForDecidedInWindowRow(t *testing.T) {
	e := newKillEnv(t)
	r := seedRelayRow(t, e, storefix.TestRequestTokenA)
	if updated, err := e.st.DecidePermissionRequest(r.ID, storefix.TestRequestTokenA, "allow", "", store.WriterProcessDecide); err != nil || !updated {
		t.Fatalf("DecidePermissionRequest: updated=%v err=%v", updated, err)
	}

	_, err := e.sendKeysAt(relayGuardWindow, time.Now(), api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "1"})
	if !errors.Is(err, api.ErrSendKeysWhileRelayed) {
		t.Fatalf("err = %v; want ErrSendKeysWhileRelayed (decided-in-window holds)", err)
	}
	e.assertNoTmuxCall(t)
}

// TestSendKeysGuardDeadBandAsymmetry pins the Decide/SendKeys asymmetry at the
// window boundary (SR-4.2, SR-4.4): in the dead band Decide refuses while the
// guard holds; aged to window + margin the guard releases and the keys are delivered.
func TestSendKeysGuardDeadBandAsymmetry(t *testing.T) {
	e := newKillEnv(t)
	r := seedRelayRow(t, e, storefix.TestRequestTokenA)
	row, err := e.st.GetPermissionRequest(r.ID, storefix.TestRequestTokenA)
	if err != nil {
		t.Fatalf("GetPermissionRequest: %v", err)
	}
	// Elapsed == window: past Decide's cutoff (window - margin), short of the
	// guard's release (window + margin).
	deadBandNow := row.CreatedAt.Add(relayGuardWindow)

	_, err = api.Decide(e.st, relayGuardWindow, deadBandNow, api.DecideParams{
		ClaudeInstanceID: r.ID, RequestToken: storefix.TestRequestTokenA, Decision: "allow"})
	if !errors.Is(err, api.ErrRelayFallenBack) {
		t.Fatalf("Decide err = %v; want ErrRelayFallenBack in dead band", err)
	}
	if row, _ = e.st.GetPermissionRequest(r.ID, storefix.TestRequestTokenA); row.Decision != "" {
		t.Errorf("decision = %q after dead-band Decide refusal; want none", row.Decision)
	}

	_, err = e.sendKeysAt(relayGuardWindow, deadBandNow, api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "1"})
	if !errors.Is(err, api.ErrSendKeysWhileRelayed) {
		t.Fatalf("SendKeys err = %v; want ErrSendKeysWhileRelayed (guard holds in dead band)", err)
	}
	e.assertNoTmuxCall(t)

	releasedNow := row.CreatedAt.Add(relayGuardWindow + api.RelayKillSafetyMargin + time.Second)
	if _, err := e.sendKeysAt(relayGuardWindow, releasedNow, api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "1"}); err != nil {
		t.Fatalf("SendKeys err = %v; want release past window + margin", err)
	}
	e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, "1")
}

// TestSendKeysGuardReleasesForDecidedAgedRow pins SR-4.2's "whether or not a
// decision was recorded": a sole decided row aged past window + margin releases the guard.
func TestSendKeysGuardReleasesForDecidedAgedRow(t *testing.T) {
	e := newKillEnv(t)
	r := seedRelayRow(t, e, storefix.TestRequestTokenA)
	if updated, err := e.st.DecidePermissionRequest(r.ID, storefix.TestRequestTokenA, "allow", "", store.WriterProcessDecide); err != nil || !updated {
		t.Fatalf("DecidePermissionRequest: updated=%v err=%v", updated, err)
	}
	row, err := e.st.GetPermissionRequest(r.ID, storefix.TestRequestTokenA)
	if err != nil {
		t.Fatalf("GetPermissionRequest: %v", err)
	}

	agedNow := row.CreatedAt.Add(relayGuardWindow + api.RelayKillSafetyMargin + time.Second)
	if _, err := e.sendKeysAt(relayGuardWindow, agedNow, api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "1"}); err != nil {
		t.Fatalf("SendKeys err = %v; want release for decided row aged past window + margin", err)
	}
	e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, "1")
}

// TestSendKeysGuardRefusesZeroRows pins the zero-rows rule: a relay-on
// check_permission row with no permission request (mid-insert) still refuses.
func TestSendKeysGuardRefusesZeroRows(t *testing.T) {
	e := newKillEnv(t)
	r := seedRelayRow(t, e)

	_, err := e.sendKeysAt(relayGuardWindow, time.Now(), api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "1"})
	if !errors.Is(err, api.ErrSendKeysWhileRelayed) {
		t.Fatalf("err = %v; want ErrSendKeysWhileRelayed (zero rows refuses)", err)
	}
	e.assertNoTmuxCall(t)
}
