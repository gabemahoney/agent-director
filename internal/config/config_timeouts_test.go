package config_test

// config_timeouts_test.go pins the keys outside [tmux] with a range:
// [defaults] expire_retention_days (b.sgw), [relay] and [pause]
// timeout_seconds (b.8q2), [pre_trust] lock_wait_seconds (b.kr4) and [store]
// busy_timeout_ms (b.c7f). A missing key or 0 gives the default (31, 86400,
// 30, 12, 10000); 1 to the key's largest value (106751 days; 2147483, the
// largest per-hook timeout Claude Code honours; 9223372036, the largest a
// Duration holds; 2147483647, the largest busy timeout SQLite holds) loads as
// written; Load refuses a negative value and one above the largest, the way
// it refuses a [tmux] value, so no value wraps into a different window.

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
)

// The exact texts the refusal description tests share: the closing sentence
// (refusalTail), kill_exit_wait_ms -1 (killNegative, the file's [tmux] table
// killNegativeTmux), and pending_grace_seconds below a derived minimum that a
// create_timeout_ms raises above the default (graceBelowRaised,
// graceBelowRaisedTmux; b.9e1), after which the closing sentence is
// raisedTail. graceRaisedTmux raises the minimum with pending_grace_seconds
// missing, which loads (b.9e1).
const (
	refusalTail      = ". A missing key, or 0, gives the default."
	killNegative     = "[tmux] kill_exit_wait_ms = -1, which must be positive"
	graceBelowRaised = "[tmux] pending_grace_seconds = 60, below its safe minimum 81 s (computed from the effective " +
		"create_timeout_ms 60000 and pipe_close_wait_ms 100)"
)

var (
	killNegativeTmux     = []tmuxSetting{{config.TmuxKillExitWaitMs, -1}}
	graceRaisedTmux      = []tmuxSetting{{config.TmuxCreateTimeoutMs, 60000}}
	graceBelowRaisedTmux = []tmuxSetting{{config.TmuxCreateTimeoutMs, 60000}, {config.TmuxPendingGraceSeconds, 60}}
)

// retentionRefusal, relayRefusal, pauseRefusal, preTrustRefusal and
// busyTimeoutRefusal are a refused v's clause.
func retentionRefusal(v string) string {
	return "[defaults] expire_retention_days = " + v + ", outside its range 1 to 106751 days"
}

func relayRefusal(v string) string {
	return "[relay] timeout_seconds = " + v + ", outside its range 1 to 2147483 seconds"
}

func pauseRefusal(v string) string {
	return "[pause] timeout_seconds = " + v + ", outside its range 1 to 9223372036 seconds"
}

func preTrustRefusal(v string) string {
	return "[pre_trust] lock_wait_seconds = " + v + ", outside its range 1 to 9223372036 seconds"
}

func busyTimeoutRefusal(v string) string {
	return "[store] busy_timeout_ms = " + v + ", outside its range 1 to 2147483647 milliseconds"
}

// TestRangeKeysLoad checks the values Load accepts: the file's value is kept
// as written and the effective value is never 0. The pause and pre_trust
// values are int64 so the file builds where int is 32 bits (GOARCH=386).
func TestRangeKeysLoad(t *testing.T) {
	cases := []struct {
		name                        string
		keys                        rangeKeys
		days, daysEffective         int
		relay, relayEffective       int
		pause, pauseEffective       int64
		preTrust, preTrustEffective int64
		busy, busyEffective         int
	}{
		{"missing", rangeKeys{}, 31, 31, 86400, 86400, 30, 30, 12, 12, 10000, 10000},
		{"zero", rangeKeys{days: "0", relay: "0", pause: "0", preTrust: "0", busyTimeout: "0"}, 0, 31, 0, 86400, 0, 30, 0, 12,
			0, 10000},
		{"one", rangeKeys{days: "1", relay: "1", pause: "1", preTrust: "1", busyTimeout: "1"}, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
		{"default_written", rangeKeys{relay: "86400", pause: "30", preTrust: "12", busyTimeout: "10000"}, 31, 31, 86400, 86400,
			30, 30, 12, 12, 10000, 10000},
		{"largest", rangeKeys{days: "106751", relay: "2147483", pause: "9223372036", preTrust: "9223372036",
			busyTimeout: "2147483647"}, 106751, 106751, 2147483, 2147483, 9223372036, 9223372036, 9223372036, 9223372036,
			2147483647, 2147483647},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(keysFile(t, tc.keys))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			for _, c := range []struct {
				name            string
				stored, written int64
				eff, wantEff    int64
			}{
				{"ExpireRetentionDays", int64(cfg.Defaults.ExpireRetentionDays), int64(tc.days),
					int64(cfg.Defaults.EffectiveExpireRetentionDays()), int64(tc.daysEffective)},
				{"Relay.TimeoutSeconds", int64(cfg.Relay.TimeoutSeconds), int64(tc.relay),
					int64(cfg.Relay.EffectiveTimeoutSeconds()), int64(tc.relayEffective)},
				{"Pause.TimeoutSeconds", int64(cfg.Pause.TimeoutSeconds), tc.pause,
					int64(cfg.Pause.EffectiveTimeoutSeconds()), tc.pauseEffective},
				{"PreTrust.LockWaitSeconds", int64(cfg.PreTrust.LockWaitSeconds), tc.preTrust,
					int64(cfg.PreTrust.EffectiveLockWaitSeconds()), tc.preTrustEffective},
				{"Store.BusyTimeoutMs", int64(cfg.Store.BusyTimeoutMs), int64(tc.busy),
					int64(cfg.Store.EffectiveBusyTimeoutMs()), int64(tc.busyEffective)},
			} {
				if c.stored != c.written || c.eff != c.wantEff {
					t.Errorf("%s = %d (effective %d); want %d as written (effective %d)", c.name, c.stored, c.eff, c.written, c.wantEff)
				}
			}
		})
	}
	// Load refuses a negative value; a Go caller's gets the default too.
	if got := (config.Defaults{ExpireRetentionDays: -1}).EffectiveExpireRetentionDays(); got != 31 {
		t.Errorf("EffectiveExpireRetentionDays() of -1 = %d; want 31", got)
	}
	if got := (config.Relay{TimeoutSeconds: -1}).EffectiveTimeoutSeconds(); got != config.DefaultRelayTimeoutSeconds {
		t.Errorf("Relay.EffectiveTimeoutSeconds() of -1 = %d; want %d", got, config.DefaultRelayTimeoutSeconds)
	}
	if got := (config.Pause{TimeoutSeconds: -1}).EffectiveTimeoutSeconds(); got != 30 {
		t.Errorf("Pause.EffectiveTimeoutSeconds() of -1 = %d; want 30", got)
	}
	if got := (config.PreTrust{LockWaitSeconds: -1}).EffectiveLockWaitSeconds(); got != 12 {
		t.Errorf("PreTrust.EffectiveLockWaitSeconds() of -1 = %d; want 12", got)
	}
	if got := (config.Store{BusyTimeoutMs: -1}).EffectiveBusyTimeoutMs(); got != 10000 {
		t.Errorf("Store.EffectiveBusyTimeoutMs() of -1 = %d; want 10000", got)
	}
}

// TestRangeKeysRefusalDescription pins each out-of-range value's exact
// refusal: just above the largest, where the relay guard window plus its
// margin wraps (9223372036) and where a window itself wraps (9223372037),
// alone and beside refused keys of other tables, whose names the header lists
// in table order. A [tmux]-only refusal is unchanged. A create_timeout_ms that
// raises the grace minimum above its default refuses no missing grace, and
// beside a set grace below it the closing sentence says a missing key, or 0,
// gives that minimum (raisedTail; b.9e1).
func TestRangeKeysRefusalDescription(t *testing.T) {
	cases := []struct {
		name string
		keys rangeKeys
		tmux []tmuxSetting
		want string
	}{
		{"days_negative", rangeKeys{days: "-1"}, nil, "refused [defaults] values: " + retentionRefusal("-1") + refusalTail},
		{"days_above_largest", rangeKeys{days: "106752"}, nil, "refused [defaults] values: " + retentionRefusal("106752") + refusalTail},
		{"days_int64_min", rangeKeys{days: "-9223372036854775808"}, nil,
			"refused [defaults] values: " + retentionRefusal("-9223372036854775808") + refusalTail},
		{"days_int64_max", rangeKeys{days: "9223372036854775807"}, nil,
			"refused [defaults] values: " + retentionRefusal("9223372036854775807") + refusalTail},
		{"days_and_tmux", rangeKeys{days: "-1"}, killNegativeTmux,
			"refused [defaults] and [tmux] values: " + retentionRefusal("-1") + "; " + killNegative + refusalTail},
		{"tmux_only_unchanged", rangeKeys{days: "31"}, killNegativeTmux, "refused [tmux] values: " + killNegative + refusalTail},
		{"days_beside_raised_create_timeout_grace_missing", rangeKeys{days: "-1"}, graceRaisedTmux,
			"refused [defaults] values: " + retentionRefusal("-1") + refusalTail},
		{"days_and_tmux_grace_below_raised_minimum", rangeKeys{days: "-1"}, graceBelowRaisedTmux,
			"refused [defaults] and [tmux] values: " + retentionRefusal("-1") + "; " + graceBelowRaised + raisedTail},
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
		{"pre_trust_negative", rangeKeys{preTrust: "-1"}, nil,
			"refused [pre_trust] values: " + preTrustRefusal("-1") + refusalTail},
		{"pre_trust_wait_wraps", rangeKeys{preTrust: "9223372037"}, nil,
			"refused [pre_trust] values: " + preTrustRefusal("9223372037") + refusalTail},
		{"pre_trust_int64_min", rangeKeys{preTrust: "-9223372036854775808"}, nil,
			"refused [pre_trust] values: " + preTrustRefusal("-9223372036854775808") + refusalTail},
		{"relay_and_pause", rangeKeys{relay: "2147484", pause: "-1"}, nil,
			"refused [relay] and [pause] values: " + relayRefusal("2147484") + "; " + pauseRefusal("-1") + refusalTail},
		{"pause_and_pre_trust", rangeKeys{pause: "-1", preTrust: "-1"}, nil,
			"refused [pause] and [pre_trust] values: " + pauseRefusal("-1") + "; " + preTrustRefusal("-1") + refusalTail},
		{"pre_trust_and_tmux", rangeKeys{preTrust: "9223372037"}, killNegativeTmux,
			"refused [pre_trust] and [tmux] values: " + preTrustRefusal("9223372037") + "; " + killNegative + refusalTail},
		{"busy_timeout_negative", rangeKeys{busyTimeout: "-5"}, nil,
			"refused [store] values: " + busyTimeoutRefusal("-5") + refusalTail},
		// SQLite reads a larger busy timeout as 0, which turns the wait off.
		{"busy_timeout_above_largest", rangeKeys{busyTimeout: "2147483648"}, nil,
			"refused [store] values: " + busyTimeoutRefusal("2147483648") + refusalTail},
		{"busy_timeout_int64_min", rangeKeys{busyTimeout: "-9223372036854775808"}, nil,
			"refused [store] values: " + busyTimeoutRefusal("-9223372036854775808") + refusalTail},
		{"busy_timeout_int64_max", rangeKeys{busyTimeout: "9223372036854775807"}, nil,
			"refused [store] values: " + busyTimeoutRefusal("9223372036854775807") + refusalTail},
		{"pre_trust_and_busy_timeout", rangeKeys{preTrust: "-1", busyTimeout: "-1"}, nil,
			"refused [pre_trust] and [store] values: " + preTrustRefusal("-1") + "; " + busyTimeoutRefusal("-1") + refusalTail},
		{"busy_timeout_and_tmux", rangeKeys{busyTimeout: "2147483648"}, killNegativeTmux,
			"refused [store] and [tmux] values: " + busyTimeoutRefusal("2147483648") + "; " + killNegative + refusalTail},
		{"defaults_and_relay", rangeKeys{days: "-1", relay: "-1"}, nil,
			"refused [defaults] and [relay] values: " + retentionRefusal("-1") + "; " + relayRefusal("-1") + refusalTail},
		{"pause_and_tmux", rangeKeys{pause: "-1"}, killNegativeTmux,
			"refused [pause] and [tmux] values: " + pauseRefusal("-1") + "; " + killNegative + refusalTail},
		{"three_tables", rangeKeys{relay: "-1", pause: "9223372037"}, killNegativeTmux,
			"refused [relay], [pause] and [tmux] values: " + relayRefusal("-1") + "; " + pauseRefusal("9223372037") +
				"; " + killNegative + refusalTail},
		{"every_table", rangeKeys{days: "106752", relay: "2147484", pause: "-1", preTrust: "-1", busyTimeout: "-1"},
			killNegativeTmux,
			"refused [defaults], [relay], [pause], [pre_trust], [store] and [tmux] values: " + retentionRefusal("106752") +
				"; " + relayRefusal("2147484") + "; " + pauseRefusal("-1") + "; " + preTrustRefusal("-1") + "; " +
				busyTimeoutRefusal("-1") + "; " + killNegative + refusalTail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := refusalDescription(t, keysFile(t, tc.keys, tc.tmux...)); got != tc.want {
				t.Errorf("description = %q\nwant          %q", got, tc.want)
			}
		})
	}
}
