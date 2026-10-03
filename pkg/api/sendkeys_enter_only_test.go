package api_test

// sendkeys_enter_only_test.go (b.9o4): send-keys with empty text is the
// Enter-only send its keys-failure advice names, and a dialog's answer on a
// pending row with allow_pending (CSCB's approver): one Enter to the agent's
// pane by id, nothing typed.

import (
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api"
)

// TestSendKeysEmptyTextPressesEnterOnly: empty text is accepted and sends one
// Enter to the agent's pane by id with nothing typed: a line left typed is
// submitted once; with allow_pending a pending row's launch pane gets the
// Enter too, its empty input submitting nothing.
func TestSendKeysEmptyTextPressesEnterOnly(t *testing.T) {
	cases := []struct {
		name         string
		seed         func(t *testing.T, e *killEnv) killRow
		allowPending bool
		typed        string   // left in the agent's input box before the call
		want         []string // the agent's submissions
	}{
		{name: "live row, text left typed", typed: skaText, want: []string{skaText},
			seed: func(t *testing.T, e *killEnv) killRow { return e.seedRow(t, killRowSpec{}) }},
		{name: "pending row with allow_pending", allowPending: true,
			seed: func(t *testing.T, e *killEnv) killRow { return e.seedPending(t, pendingFresh, pendingOurs) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := tc.seed(t, e)
			in := watchPaneInput(e, r.Spawn.Identity.PaneID, false)
			in.box = tc.typed

			_, err := e.sendKeys(api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "", AllowPending: tc.allowPending})

			if err != nil {
				t.Fatalf("send-keys with empty text: %v; want Enter delivered", err)
			}
			e.assertEnterOnly(t, r.Socket, r.Spawn.Identity.PaneID)
			if !slices.Equal(in.submitted, tc.want) || in.box != "" {
				t.Errorf("agent got submissions %q with %q left typed; want %q and nothing typed", in.submitted, in.box, tc.want)
			}
		})
	}
}
