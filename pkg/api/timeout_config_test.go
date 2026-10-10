package api_test

// timeout_config_test.go pins b.8q2 at the Client: the send-keys relay guard
// uses the configured relay window, and a [pause] timeout_seconds of 0 or the
// largest accepted value waits for the row to end instead of timing out at
// once. api.New's refusal of an out-of-range value is TestNewRefusesConfig's.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestClientSendKeysRelayGuardUsesConfiguredWindow: R3 (b.8q2) — Client.SendKeys
// on a relay-on row whose request (recorded before schema v7, judged by time)
// is two default windows old finds it fallen back under the default relay
// window (ErrRelayFallenBack, b.146 rule 7) and its relay hook possibly alive
// under the largest one (ErrSendKeysWhileRelayed); either way it types nothing.
func TestClientSendKeysRelayGuardUsesConfiguredWindow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		relay int64
		want  error
	}{
		{config.DefaultRelayTimeoutSeconds, api.ErrRelayFallenBack},
		{config.MaxRelayTimeoutSeconds, api.ErrSendKeysWhileRelayed},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.relay), func(t *testing.T) {
			e := newKillEnv(t)
			r := seedRelayRow(t, e, storefix.TestRequestTokenA)
			storefix.SeedUndeliverablePermissionRequest(t, e.st, e.dbPath, r.ID, storefix.TestRequestTokenA,
				2*config.DefaultRelayTimeoutSeconds*time.Second)
			cfgPath := filepath.Join(t.TempDir(), "config.toml")
			apitest.WriteKeysConfig(t, cfgPath, apitest.ConfigKeys{RelayTimeoutSeconds: tc.relay})
			c, _, err := e.clientFor(t, cfgPath)
			if err != nil {
				t.Fatalf("api.New: %v", err)
			}
			api.SetClockForTest(c, time.Now) // the request's age is on the wall clock

			_, err = c.SendKeys(api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "1"})

			if !errors.Is(err, tc.want) {
				t.Fatalf("SendKeys err = %v; want %v", err, tc.want)
			}
			e.assertNoTmuxCall(t)
		})
	}
}

// TestClientPauseTimeoutConfigWaitsForEnd: P1 (b.8q2) — with [pause]
// timeout_seconds = 0 (the default) or the largest value Load accepts,
// Client.Pause waits, so an agent that ends at the wait's first sleep pauses cleanly.
func TestClientPauseTimeoutConfigWaitsForEnd(t *testing.T) {
	// Serial: it changes the pause wait's process-wide poll knobs (api.SetPauseTestKnobs).
	for _, n := range []int64{0, config.MaxPauseTimeoutSeconds} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			e.endAtFirstWait(t, r)
			cfgPath := filepath.Join(t.TempDir(), "config.toml")
			writeConfig(t, cfgPath, fmt.Sprintf("[pause]\ntimeout_seconds = %d\n", n))
			c, _, err := e.clientFor(t, cfgPath)
			if err != nil {
				t.Fatalf("api.New: %v", err)
			}

			_, err = c.Pause(context.Background(), pauseParams(r))

			if err != nil {
				t.Fatalf("Pause: %v; want nil once the agent ends", err)
			}
			if state, err := e.st.GetSpawnState(r.ID); err != nil || state != store.StateEnded {
				t.Errorf("row state = %q (err %v); want %s", state, err, store.StateEnded)
			}
		})
	}
}
