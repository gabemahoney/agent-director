package config_test

// config_retention_test.go pins [defaults] expire_retention_days (b.sgw): a
// missing key or 0 gives the 31-day default, 1 to 106751 load as written, and
// Load refuses a negative value and one above 106751 the way it refuses a
// [tmux] value, so a default expire run never selects every finished row.

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
)

// TestExpireRetentionDaysLoads checks the values Load accepts: the file's
// value is kept as written and the effective window is never below one day.
func TestExpireRetentionDaysLoads(t *testing.T) {
	cases := []struct {
		name              string
		days              string // "" omits the key
		stored, effective int
	}{
		{"missing", "", 31, 31},
		{"zero", "0", 0, 31},
		{"one", "1", 1, 1},
		{"largest", "106751", 106751, 106751},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(configFile(t, tc.days))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.Defaults.ExpireRetentionDays; got != tc.stored {
				t.Errorf("ExpireRetentionDays = %d; want %d as written", got, tc.stored)
			}
			if got := cfg.Defaults.EffectiveExpireRetentionDays(); got != tc.effective {
				t.Errorf("EffectiveExpireRetentionDays() = %d; want %d", got, tc.effective)
			}
		})
	}
	// Load refuses a negative value; a Go caller's gets the default too.
	if got := (config.Defaults{ExpireRetentionDays: -1}).EffectiveExpireRetentionDays(); got != 31 {
		t.Errorf("EffectiveExpireRetentionDays() of -1 = %d; want 31", got)
	}
}

// TestDefaultsRefusalDescription pins each out-of-range expire_retention_days's
// exact refusal, alone and beside a [tmux] one; a [tmux]-only refusal is
// unchanged. Beside a [tmux] key whose default is below its minimum, the
// closing sentence leaves that key out (b.n4q).
func TestDefaultsRefusalDescription(t *testing.T) {
	const tail = ". A missing key, or 0, gives the default."
	retention := func(v string) string {
		return "[defaults] expire_retention_days = " + v + ", outside its range 1 to 106751 days"
	}
	const killNegative = "[tmux] kill_exit_wait_ms = -1, which must be positive"
	kill := []tmuxSetting{{config.TmuxKillExitWaitMs, -1}}
	const graceAtDefault = "[tmux] pending_grace_seconds is missing or 0, and its default, 60, is below its safe " +
		"minimum 81 s (computed from the effective create_timeout_ms 60000 and pipe_close_wait_ms 100), so set it " +
		"to at least 81, or lower the effective create_timeout_ms and pipe_close_wait_ms to a total of 40000 ms " +
		"or less (a missing or 0 key counts as its default)"
	const graceLeftOut = ". For every refused key other than [tmux] pending_grace_seconds, a missing key, or 0, " +
		"gives the default."
	cases := []struct {
		name string
		days string
		tmux []tmuxSetting
		want string
	}{
		{"negative", "-1", nil, "refused [defaults] values: " + retention("-1") + tail},
		{"above_largest", "106752", nil, "refused [defaults] values: " + retention("106752") + tail},
		{"thousand_years", "365000", nil, "refused [defaults] values: " + retention("365000") + tail},
		{"int64_min", "-9223372036854775808", nil,
			"refused [defaults] values: " + retention("-9223372036854775808") + tail},
		{"int64_max", "9223372036854775807", nil,
			"refused [defaults] values: " + retention("9223372036854775807") + tail},
		{"with_tmux_refusal", "-1", kill,
			"refused [defaults] and [tmux] values: " + retention("-1") + "; " + killNegative + tail},
		{"tmux_only_unchanged", "31", kill, "refused [tmux] values: " + killNegative + tail},
		{"with_tmux_default_below_minimum", "-1", []tmuxSetting{{config.TmuxCreateTimeoutMs, 60000}},
			"refused [defaults] and [tmux] values: " + retention("-1") + "; " + graceAtDefault + graceLeftOut},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := refusalDescription(t, configFile(t, tc.days, tc.tmux...)); got != tc.want {
				t.Errorf("description = %q\nwant          %q", got, tc.want)
			}
		})
	}
}
