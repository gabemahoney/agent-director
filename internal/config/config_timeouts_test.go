package config_test

// config_timeouts_test.go pins [relay] timeout_seconds and [pause]
// timeout_seconds (b.8q2): a missing key or 0 gives the default (86400, 30),
// 1 to the key's largest value (2147483, the largest per-hook timeout Claude
// Code honours; 9223372036, the largest a Duration holds) load as written, and
// Load refuses a negative value and one above the largest, so no value wraps
// into a different window.

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
)

// relayRefusal and pauseRefusal are a refused timeout_seconds v's clause.
func relayRefusal(v string) string {
	return "[relay] timeout_seconds = " + v + ", outside its range 1 to 2147483 seconds"
}

func pauseRefusal(v string) string {
	return "[pause] timeout_seconds = " + v + ", outside its range 1 to 9223372036 seconds"
}

// TestTimeoutSecondsLoads checks the values Load accepts: the file's value is
// kept as written and the effective window is never 0. The pause values are
// int64 so the file builds where int is 32 bits (GOARCH=386).
func TestTimeoutSecondsLoads(t *testing.T) {
	cases := []struct {
		name                  string
		keys                  rangeKeys
		relay, relayEffective int
		pause, pauseEffective int64
	}{
		{"missing", rangeKeys{}, 86400, 86400, 30, 30},
		{"zero", rangeKeys{relay: "0", pause: "0"}, 0, 86400, 0, 30},
		{"one", rangeKeys{relay: "1", pause: "1"}, 1, 1, 1, 1},
		{"default_written", rangeKeys{relay: "86400", pause: "30"}, 86400, 86400, 30, 30},
		{"largest", rangeKeys{relay: "2147483", pause: "9223372036"}, 2147483, 2147483, 9223372036, 9223372036},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(keysFile(t, tc.keys))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.Relay.TimeoutSeconds; got != tc.relay {
				t.Errorf("Relay.TimeoutSeconds = %d; want %d as written", got, tc.relay)
			}
			if got := cfg.Relay.EffectiveTimeoutSeconds(); got != tc.relayEffective {
				t.Errorf("Relay.EffectiveTimeoutSeconds() = %d; want %d", got, tc.relayEffective)
			}
			if got := int64(cfg.Pause.TimeoutSeconds); got != tc.pause {
				t.Errorf("Pause.TimeoutSeconds = %d; want %d as written", got, tc.pause)
			}
			if got := int64(cfg.Pause.EffectiveTimeoutSeconds()); got != tc.pauseEffective {
				t.Errorf("Pause.EffectiveTimeoutSeconds() = %d; want %d", got, tc.pauseEffective)
			}
		})
	}
	// Load refuses a negative value; a Go caller's gets the default too.
	if got := (config.Pause{TimeoutSeconds: -1}).EffectiveTimeoutSeconds(); got != 30 {
		t.Errorf("Pause.EffectiveTimeoutSeconds() of -1 = %d; want 30", got)
	}
}

// TestTimeoutSecondsRefusalDescription pins each out-of-range timeout's exact
// refusal: just above the largest, where the guard window plus its margin
// wraps (relay 9223372036) and where the window itself wraps (9223372037),
// alone and beside refused keys of other tables, whose names the header lists.
func TestTimeoutSecondsRefusalDescription(t *testing.T) {
	cases := []struct {
		name string
		keys rangeKeys
		tmux []tmuxSetting
		want string
	}{
		{"relay_negative", rangeKeys{relay: "-1"}, nil, "refused [relay] values: " + relayRefusal("-1") + refusalTail},
		{"relay_above_largest", rangeKeys{relay: "2147484"}, nil,
			"refused [relay] values: " + relayRefusal("2147484") + refusalTail},
		{"relay_guard_cutoff_wraps", rangeKeys{relay: "9223372036"}, nil,
			"refused [relay] values: " + relayRefusal("9223372036") + refusalTail},
		{"relay_window_wraps", rangeKeys{relay: "9223372037"}, nil,
			"refused [relay] values: " + relayRefusal("9223372037") + refusalTail},
		{"relay_int64_min", rangeKeys{relay: "-9223372036854775808"}, nil,
			"refused [relay] values: " + relayRefusal("-9223372036854775808") + refusalTail},
		{"pause_negative", rangeKeys{pause: "-1"}, nil, "refused [pause] values: " + pauseRefusal("-1") + refusalTail},
		{"pause_window_wraps", rangeKeys{pause: "9223372037"}, nil,
			"refused [pause] values: " + pauseRefusal("9223372037") + refusalTail},
		{"pause_int64_max", rangeKeys{pause: "9223372036854775807"}, nil,
			"refused [pause] values: " + pauseRefusal("9223372036854775807") + refusalTail},
		{"relay_and_pause", rangeKeys{relay: "2147484", pause: "-1"}, nil,
			"refused [relay] and [pause] values: " + relayRefusal("2147484") + "; " + pauseRefusal("-1") + refusalTail},
		{"defaults_and_relay", rangeKeys{days: "-1", relay: "-1"}, nil,
			"refused [defaults] and [relay] values: " + retentionRefusal("-1") + "; " + relayRefusal("-1") + refusalTail},
		{"pause_and_tmux", rangeKeys{pause: "-1"}, killNegativeTmux,
			"refused [pause] and [tmux] values: " + pauseRefusal("-1") + "; " + killNegative + refusalTail},
		{"three_tables", rangeKeys{relay: "-1", pause: "9223372037"}, killNegativeTmux,
			"refused [relay], [pause] and [tmux] values: " + relayRefusal("-1") + "; " + pauseRefusal("9223372037") +
				"; " + killNegative + refusalTail},
		{"every_table", rangeKeys{days: "106752", relay: "2147484", pause: "-1"}, killNegativeTmux,
			"refused [defaults], [relay], [pause] and [tmux] values: " + retentionRefusal("106752") + "; " +
				relayRefusal("2147484") + "; " + pauseRefusal("-1") + "; " + killNegative + refusalTail},
		{"three_tables_with_tmux_default_below_minimum", rangeKeys{relay: "-1", pause: "-1"}, graceRaisedTmux,
			"refused [relay], [pause] and [tmux] values: " + relayRefusal("-1") + "; " + pauseRefusal("-1") + "; " +
				graceAtDefault + graceLeftOut},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := refusalDescription(t, keysFile(t, tc.keys, tc.tmux...)); got != tc.want {
				t.Errorf("description = %q\nwant          %q", got, tc.want)
			}
		})
	}
}
