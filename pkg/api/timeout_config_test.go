package api_test

// timeout_config_test.go pins b.8q2 at the Client: api.New refuses a [relay]
// or [pause] timeout_seconds outside its range, the send-keys relay guard uses
// the configured relay window, and a [pause] timeout_seconds of 0 or the
// largest accepted value waits for the row to end instead of timing out at once.

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

// TestNewRefusesTimeoutConfig: api.New returns no Client and the
// *config.ConfigError naming the out-of-range value. 9223372036 is the relay
// value whose guard cutoff wrapped and released the send-keys guard at once;
// 9223372037 wrapped the relay and pause windows themselves.
func TestNewRefusesTimeoutConfig(t *testing.T) {
	cases := []struct {
		keys    apitest.ConfigKeys
		refusal apitest.ConfigRefusal
	}{
		{apitest.ConfigKeys{RelayTimeoutSeconds: -1}, apitest.ConfigRefusal{RelayTimeout: true, Value: -1}},
		{apitest.ConfigKeys{RelayTimeoutSeconds: 2147484}, apitest.ConfigRefusal{RelayTimeout: true, Value: 2147484}},
		{apitest.ConfigKeys{RelayTimeoutSeconds: 9223372036}, apitest.ConfigRefusal{RelayTimeout: true, Value: 9223372036}},
		{apitest.ConfigKeys{RelayTimeoutSeconds: 9223372037}, apitest.ConfigRefusal{RelayTimeout: true, Value: 9223372037}},
		{apitest.ConfigKeys{PauseTimeoutSeconds: -1}, apitest.ConfigRefusal{PauseTimeout: true, Value: -1}},
		{apitest.ConfigKeys{PauseTimeoutSeconds: 9223372037}, apitest.ConfigRefusal{PauseTimeout: true, Value: 9223372037}},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("relay_%d_pause_%d", tc.keys.RelayTimeoutSeconds, tc.keys.PauseTimeoutSeconds), func(t *testing.T) {
			e := newKillEnv(t)
			cfgPath := filepath.Join(t.TempDir(), "config.toml")
			apitest.WriteKeysConfig(t, cfgPath, tc.keys)

			c, err := api.New(api.Options{StorePath: e.dbPath, ConfigPath: cfgPath, TmuxClient: e.rec})

			if c != nil {
				_ = c.Close()
				t.Error("api.New returned a Client for a refused timeout_seconds")
			}
			var ce *config.ConfigError
			if !errors.As(err, &ce) {
				t.Fatalf("api.New err = %v; want a *config.ConfigError", err)
			}
			apitest.AssertDescription(t, ce.Error(), apitest.DescConfigRefused(cfgPath, tc.refusal))
		})
	}
}

// TestClientSendKeysRelayGuardUsesConfiguredWindow: R3 (b.8q2) — Client.SendKeys
// on a relay-on row whose request is two default windows old delivers under the
// default relay window and is refused, typing nothing, under the largest one.
func TestClientSendKeysRelayGuardUsesConfiguredWindow(t *testing.T) {
	cases := []struct {
		relay   int64
		deliver bool
	}{
		{config.DefaultRelayTimeoutSeconds, true},
		{config.MaxRelayTimeoutSeconds, false},
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

			if tc.deliver {
				if err != nil {
					t.Fatalf("SendKeys: %v; want the keys delivered once the request is past the window", err)
				}
				e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, "1")
				return
			}
			if !errors.Is(err, api.ErrSendKeysWhileRelayed) {
				t.Fatalf("SendKeys err = %v; want ErrSendKeysWhileRelayed", err)
			}
			e.assertNoTmuxCall(t)
		})
	}
}

// TestClientPauseTimeoutConfigWaitsForEnd: P1 (b.8q2) — with [pause]
// timeout_seconds = 0 (the default) or the largest value Load accepts,
// Client.Pause waits, so an agent that ends at the wait's first sleep pauses cleanly.
func TestClientPauseTimeoutConfigWaitsForEnd(t *testing.T) {
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
