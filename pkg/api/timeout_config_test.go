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
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
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
			e.rowAgent = store.AgentCodex
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

			_, err = c.SendKeys(api.SendKeysParams{InstanceID: r.ID, Text: "1"})

			if !errors.Is(err, tc.want) {
				t.Fatalf("SendKeys err = %v; want %v", err, tc.want)
			}
			e.assertNoTmuxCall(t)
		})
	}
}

// TestClientPauseTimeoutConfigWaitsForEnd: P1 (b.8q2) — with [pause]
// timeout_seconds = 0 (the default) or the largest value Load accepts,
// Client.Pause waits, so an agent that ends at the wait's first sleep pauses
// cleanly. The row is Claude's, covered, so the pause is keyless (SR-6.4): its
// signalled agent exits at the first poll sleep and the Launch End is recorded.
func TestClientPauseTimeoutConfigWaitsForEnd(t *testing.T) {
	// Serial: it replaces keyless pause's signal and the pause knobs (process-wide).
	for _, n := range []int64{0, config.MaxPauseTimeoutSeconds} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			signals := 0
			t.Cleanup(api.SetPauseTerminateForTest(func(int, string) error { signals++; return nil }))
			slept := pauSleeps(t, time.Millisecond, false, func(n int) {
				if n == 1 {
					e.pc.Set(r.AgentPID, procfix.Gone())
				}
			})
			cfgPath := filepath.Join(t.TempDir(), "config.toml")
			writeConfig(t, cfgPath, fmt.Sprintf("[pause]\ntimeout_seconds = %d\n", n))
			c, _, err := e.clientFor(t, cfgPath)
			if err != nil {
				t.Fatalf("api.New: %v", err)
			}

			_, err = c.Pause(context.Background(), pauseParams(r))

			if err != nil || signals != 1 || len(*slept) == 0 {
				t.Fatalf("Pause = %v after %d signals and %d sleeps; want nil once the signalled agent ends at the wait", err, signals, len(*slept))
			}
			for _, l := range busyFacts(t, e.dbPath, r.ID).Launches {
				if l.Phase == "current" && l.EndCause.String != "process_exited" {
					t.Errorf("current Launch end = %+v; want process_exited", l.EndCause)
				}
			}
		})
	}
}
