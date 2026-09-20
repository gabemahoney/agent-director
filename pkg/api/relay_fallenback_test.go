package api_test

import (
	"errors"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// relayGuardWindow is the effective relay window used by the release-path
// tests. A short window keeps the backdate ages in SeedUndeliverablePermissionRequest
// small while staying well clear of the 1s safety margin. The recordingTmux
// fake, newTmux, sendKeysTestWindow/sendKeysTestNow live in sendkeys_test.go
// (same api_test package) and are reused here.
const relayGuardWindow = time.Hour

// TestRelayFallenBackIncidentRegression re-anchors the b.kk3 incident (SR-7.1):
// a relay-on check_permission spawn whose open request has fallen out of its
// delivery window must (a) refuse Decide with the typed ErrRelayFallenBack AND
// (b) let a subsequent SendKeys through so the operator can recover the wedged
// pane. Asserts the observed sequence only — the typed error out of Decide and
// the recorded keystroke out of SendKeys — not the signal's internal shape.
func TestRelayFallenBackIncidentRegression(t *testing.T) {
	s, dbPath := storefix.OpenTempStore(t)
	storefix.SeedCheckPermission(t, s, "id-inc")
	// Backdate the sole open row past the window so it is undeliverable.
	storefix.SeedUndeliverablePermissionRequest(t, s, dbPath, "id-inc", storefix.TestRequestTokenA, 2*relayGuardWindow)

	now := time.Now()

	// Decide refuses with the typed fallen-back error.
	_, err := api.Decide(s, relayGuardWindow, now, api.DecideParams{
		ClaudeInstanceID: "id-inc",
		RequestToken:     storefix.TestRequestTokenA,
		Decision:         "allow",
	})
	if !errors.Is(err, api.ErrRelayFallenBack) {
		t.Fatalf("Decide err = %v; want ErrRelayFallenBack", err)
	}

	// SendKeys releases and delivers the keystroke.
	tmux := newTmux()
	if _, err := api.SendKeys(s, tmux, relayGuardWindow, now, api.SendKeysParams{
		ClaudeInstanceID: "id-inc",
		Text:             "2",
	}); err != nil {
		t.Fatalf("SendKeys after fallen-back: %v", err)
	}
	if len(tmux.calls) != 1 || tmux.calls[0].text != "2" {
		t.Fatalf("tmux.calls = %v; want exactly one recorded keystroke %q", tmux.calls, "2")
	}
}

// TestSendKeysGuardMultiRowDeliverability exercises the per-row guard across
// several open rows for one spawn (SR-7.3): the guard holds while ANY row is
// still in-window and releases only once EVERY row has aged out. Parameterized
// over the deliverability mix; asserts observable behavior only (the error and
// the tmux fake's call count).
func TestSendKeysGuardMultiRowDeliverability(t *testing.T) {
	tokens := []string{
		storefix.TestRequestTokenA,
		storefix.TestRequestTokenB,
		storefix.TestRequestTokenC,
	}

	cases := []struct {
		name string
		// undeliverable[i] backdates tokens[i] past the window; the rest stay
		// freshly-created (in-window).
		undeliverable []bool
		wantRefuse    bool
	}{
		{
			name:          "all_in_window_refuses",
			undeliverable: []bool{false, false, false},
			wantRefuse:    true,
		},
		{
			name:          "one_undeliverable_rest_in_window_refuses",
			undeliverable: []bool{true, false, false},
			wantRefuse:    true,
		},
		{
			name:          "all_but_one_undeliverable_refuses",
			undeliverable: []bool{true, true, false},
			wantRefuse:    true,
		},
		{
			name:          "all_undeliverable_delivers",
			undeliverable: []bool{true, true, true},
			wantRefuse:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, dbPath := storefix.OpenTempStore(t)
			// SeedCheckPermission seeds the spawn (relay=on, check_permission)
			// plus the first open row (TestRequestTokenA); add the other two.
			storefix.SeedCheckPermission(t, s, "id-multi")
			storefix.SeedOpenPermissionRequests(t, s, "id-multi", tokens[1:])

			for i, undeliverable := range tc.undeliverable {
				if undeliverable {
					storefix.SeedUndeliverablePermissionRequest(t, s, dbPath, "id-multi", tokens[i], 2*relayGuardWindow)
				}
			}

			tmux := newTmux()
			_, err := api.SendKeys(s, tmux, relayGuardWindow, time.Now(), api.SendKeysParams{
				ClaudeInstanceID: "id-multi",
				Text:             "1",
			})

			if tc.wantRefuse {
				if !errors.Is(err, api.ErrSendKeysWhileRelayed) {
					t.Fatalf("err = %v; want ErrSendKeysWhileRelayed", err)
				}
				if len(tmux.calls) != 0 {
					t.Fatalf("tmux was called while any row still in-window: %v", tmux.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("SendKeys: unexpected error with every row undeliverable: %v", err)
			}
			if len(tmux.calls) != 1 {
				t.Fatalf("tmux.calls = %v; want exactly 1 (guard released)", tmux.calls)
			}
		})
	}
}

// TestSendKeysGuardHoldsForDecidedInWindowRow pins SR-4.2: a spawn whose SOLE
// permission row is decided but still within its delivery window keeps the
// guard shut — a live poller may still deliver that recorded decision, so a
// pane-side keystroke must not race it. Zero tmux calls.
func TestSendKeysGuardHoldsForDecidedInWindowRow(t *testing.T) {
	s, _ := storefix.OpenTempStore(t)
	storefix.SeedCheckPermission(t, s, "id-decided")
	// Decide the sole row in-window (created_at stays at ~now); the row is now
	// decided but still deliverable.
	if updated, err := s.DecidePermissionRequest("id-decided", storefix.TestRequestTokenA, "allow", "", store.WriterProcessDecide); err != nil || !updated {
		t.Fatalf("DecidePermissionRequest: updated=%v err=%v", updated, err)
	}

	tmux := newTmux()
	_, err := api.SendKeys(s, tmux, relayGuardWindow, time.Now(), api.SendKeysParams{
		ClaudeInstanceID: "id-decided",
		Text:             "1",
	})
	if !errors.Is(err, api.ErrSendKeysWhileRelayed) {
		t.Fatalf("err = %v; want ErrSendKeysWhileRelayed (decided-in-window holds)", err)
	}
	if len(tmux.calls) != 0 {
		t.Fatalf("tmux was called for a decided-in-window row: %v", tmux.calls)
	}
}

// TestSendKeysGuardRefusesZeroRows pins the PM-mandated zero-rows behavior: a
// relay-on check_permission spawn with NO permission_requests rows still
// refuses. Rationale: with no row there is no deliverability signal and no
// authority to release the guard; the state is a real mid-insert transient
// (the hook is between transitioning the spawn and inserting the request row),
// so releasing here would race a request that is about to appear.
func TestSendKeysGuardRefusesZeroRows(t *testing.T) {
	// A relay-on spawn transitioned into check_permission with no request row
	// written yet — OpenStoreWithRow leaves permission_requests empty.
	s, _ := storefix.OpenTempStore(t)
	if err := s.InsertPending(store.Spawn{
		ClaudeInstanceID: "id-zero",
		CWD:              "/tmp",
		TmuxSessionName:  "cd-zero",
		RelayMode:        "on",
	}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	if err := s.ApplyHookTransition("id-zero", store.StateCheckPermission, false, "test_seed"); err != nil {
		t.Fatalf("ApplyHookTransition: %v", err)
	}

	tmux := newTmux()
	_, err := api.SendKeys(s, tmux, relayGuardWindow, time.Now(), api.SendKeysParams{
		ClaudeInstanceID: "id-zero",
		Text:             "1",
	})
	if !errors.Is(err, api.ErrSendKeysWhileRelayed) {
		t.Fatalf("err = %v; want ErrSendKeysWhileRelayed (zero rows refuses)", err)
	}
	if len(tmux.calls) != 0 {
		t.Fatalf("tmux was called with zero permission rows: %v", tmux.calls)
	}
}
