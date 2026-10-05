package config_test

import (
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
)

// tmuxKeyCase is one row of the shared [tmux] key table (SR-20.6). Defaults
// and minimums come from the exported constants; TestTmuxConstantsMatchSRD
// pins those constants to the SR-4.1 literals.
type tmuxKeyCase struct {
	key      config.TmuxKey
	unit     time.Duration // one unit of the key's integer value
	def      int64         // the key's default
	minimum  int64         // safe minimum at the other keys' defaults; 0 when none
	accessor func(config.Tmux) time.Duration
	set      func(*config.Tmux, int64)
}

// dur returns v units of the key's value as a duration.
func (tc tmuxKeyCase) dur(v int64) time.Duration { return time.Duration(v) * tc.unit }

// tmuxKeyTable holds the nine [tmux] keys in SR-4.1 table order; it drives
// every per-key case in this package.
var tmuxKeyTable = []tmuxKeyCase{
	{config.TmuxStartingSessionSeconds, time.Second, config.DefaultStartingSessionSeconds, config.MinStartingSessionSeconds,
		config.Tmux.EffectiveStartingSession, func(t *config.Tmux, v int64) { t.StartingSessionSeconds = v }},
	{config.TmuxStoppingWindowSeconds, time.Second, config.DefaultStoppingWindowSeconds, config.MinStoppingWindowSeconds,
		config.Tmux.EffectiveStoppingWindow, func(t *config.Tmux, v int64) { t.StoppingWindowSeconds = v }},
	{config.TmuxPendingGraceSeconds, time.Second, config.DefaultPendingGraceSeconds,
		config.PendingGraceMinimumSeconds(config.DefaultCreateTimeoutMs, config.DefaultPipeCloseWaitMs),
		config.Tmux.EffectivePendingGrace, func(t *config.Tmux, v int64) { t.PendingGraceSeconds = v }},
	{config.TmuxQueryTimeoutMs, time.Millisecond, config.DefaultQueryTimeoutMs, 0,
		config.Tmux.EffectiveQueryTimeout, func(t *config.Tmux, v int64) { t.QueryTimeoutMs = v }},
	{config.TmuxActionTimeoutMs, time.Millisecond, config.DefaultActionTimeoutMs, 0,
		config.Tmux.EffectiveActionTimeout, func(t *config.Tmux, v int64) { t.ActionTimeoutMs = v }},
	{config.TmuxCreateTimeoutMs, time.Millisecond, config.DefaultCreateTimeoutMs, 0,
		config.Tmux.EffectiveCreateTimeout, func(t *config.Tmux, v int64) { t.CreateTimeoutMs = v }},
	{config.TmuxPipeCloseWaitMs, time.Millisecond, config.DefaultPipeCloseWaitMs, 0,
		config.Tmux.EffectivePipeCloseWait, func(t *config.Tmux, v int64) { t.PipeCloseWaitMs = v }},
	{config.TmuxSweepBudgetSeconds, time.Second, config.DefaultSweepBudgetSeconds, 0,
		config.Tmux.EffectiveSweepBudget, func(t *config.Tmux, v int64) { t.SweepBudgetSeconds = v }},
	{config.TmuxKillExitWaitMs, time.Millisecond, config.DefaultKillExitWaitMs, 0,
		config.Tmux.EffectiveKillExitWait, func(t *config.Tmux, v int64) { t.KillExitWaitMs = v }},
}

// maxDuration is the largest duration, which an accessor gives for a value
// too large to express as one.
const maxDuration = time.Duration(math.MaxInt64)

// tmuxSetting is one [tmux] key and the integer written for it.
type tmuxSetting struct {
	key   config.TmuxKey
	value int64
}

// tmuxConfigFile writes a config file whose [tmux] table holds settings.
func tmuxConfigFile(t *testing.T, settings ...tmuxSetting) string {
	t.Helper()
	return configFile(t, "", settings...)
}

// configFile writes a config file whose [defaults] table sets
// expire_retention_days to the TOML integer days (no [defaults] table when
// days is ""), then a [tmux] table holding settings.
func configFile(t *testing.T, days string, settings ...tmuxSetting) string {
	t.Helper()
	return keysFile(t, rangeKeys{days: days}, settings...)
}

// rangeKeys are the TOML integers a config file sets for the keys outside
// [tmux] with a range: [defaults] expire_retention_days (b.sgw), [relay]
// timeout_seconds and [pause] timeout_seconds (b.8q2). "" leaves the table out.
type rangeKeys struct{ days, relay, pause string }

// keysFile writes a config file setting k's keys, then a [tmux] table holding settings.
func keysFile(t *testing.T, k rangeKeys, settings ...tmuxSetting) string {
	t.Helper()
	var b strings.Builder
	for _, kv := range []struct{ table, key, value string }{
		{"defaults", "expire_retention_days", k.days},
		{"relay", "timeout_seconds", k.relay},
		{"pause", "timeout_seconds", k.pause},
	} {
		if kv.value != "" {
			fmt.Fprintf(&b, "[%s]\n%s = %s\n", kv.table, kv.key, kv.value)
		}
	}
	b.WriteString("[tmux]\n")
	for _, s := range settings {
		fmt.Fprintf(&b, "%s = %d\n", s.key.Name(), s.value)
	}
	return makeConfigFile(t, b.String())
}

// loadTmux loads path, fails the test on any error and returns its [tmux] section.
func loadTmux(t *testing.T, path string) config.Tmux {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	return cfg.Tmux
}

// checkEffective asserts that key tc reads as want through both its named
// accessor and Tmux.Effective.
func checkEffective(t *testing.T, tm config.Tmux, tc tmuxKeyCase, want time.Duration) {
	t.Helper()
	if got := tc.accessor(tm); got != want {
		t.Errorf("named accessor = %v, want %v", got, want)
	}
	if got := tm.Effective(tc.key); got != want {
		t.Errorf("Effective(%s) = %v, want %v", tc.key.Name(), got, want)
	}
}

// TestTmuxConstantsMatchSRD pins every [tmux] constant to its SR-4.1 literal.
func TestTmuxConstantsMatchSRD(t *testing.T) {
	cases := []struct {
		name string
		got  int64
		want int64
	}{
		{"DefaultStartingSessionSeconds", config.DefaultStartingSessionSeconds, 300},
		{"DefaultStoppingWindowSeconds", config.DefaultStoppingWindowSeconds, 90},
		{"DefaultPendingGraceSeconds", config.DefaultPendingGraceSeconds, 60},
		{"DefaultQueryTimeoutMs", config.DefaultQueryTimeoutMs, 1500},
		{"DefaultActionTimeoutMs", config.DefaultActionTimeoutMs, 2000},
		{"DefaultCreateTimeoutMs", config.DefaultCreateTimeoutMs, 5000},
		{"DefaultPipeCloseWaitMs", config.DefaultPipeCloseWaitMs, 100},
		{"DefaultSweepBudgetSeconds", config.DefaultSweepBudgetSeconds, 15},
		{"DefaultKillExitWaitMs", config.DefaultKillExitWaitMs, 5000},
		{"MinStartingSessionSeconds", config.MinStartingSessionSeconds, 60},
		{"MinStoppingWindowSeconds", config.MinStoppingWindowSeconds, 30},
		{"PendingGraceFloorSeconds", config.PendingGraceFloorSeconds, 30},
		{"PendingGraceMarginSeconds", config.PendingGraceMarginSeconds, 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %d, want %d", tc.got, tc.want)
			}
		})
	}
}

// TestTmuxKeyDefinitions checks each exported key definition against the
// shared table: order, unit, default, minimum and field mapping.
func TestTmuxKeyDefinitions(t *testing.T) {
	var tableKeys []config.TmuxKey
	for _, tc := range tmuxKeyTable {
		tableKeys = append(tableKeys, tc.key)
	}
	if got := config.TmuxKeys(); !slices.Equal(got, tableKeys) {
		t.Fatalf("TmuxKeys() = %v, want the nine keys in table order %v", got, tableKeys)
	}
	for _, tc := range tmuxKeyTable {
		t.Run(tc.key.Name(), func(t *testing.T) {
			wantUnit := config.TmuxUnitSeconds
			if tc.unit == time.Millisecond {
				wantUnit = config.TmuxUnitMilliseconds
			}
			if got := tc.key.Unit(); got != wantUnit {
				t.Errorf("Unit() = %v, want %v", got, wantUnit)
			}
			if got := tc.key.DefaultValue(); got != tc.def {
				t.Errorf("DefaultValue() = %d, want %d", got, tc.def)
			}
			if got := config.Default().Tmux.Value(tc.key); got != tc.def {
				t.Errorf("Default().Tmux.Value = %d, want %d", got, tc.def)
			}
			gotMin, hasMin := config.Tmux{}.Minimum(tc.key)
			if gotMin != tc.minimum || hasMin != (tc.minimum > 0) {
				t.Errorf("Minimum() = (%d, %v), want (%d, %v)", gotMin, hasMin, tc.minimum, tc.minimum > 0)
			}
			if hasNone := tc.key.MinimumKind() == config.TmuxMinimumNone; hasNone != (tc.minimum == 0) {
				t.Errorf("MinimumKind() = %v, but the table's minimum is %d", tc.key.MinimumKind(), tc.minimum)
			}
			var tm config.Tmux
			tc.set(&tm, 42)
			if got := tm.Value(tc.key); got != 42 {
				t.Errorf("Value after setting the field = %d, want 42", got)
			}
		})
	}
}

// TestTmuxFieldTagsMatchKeyNames checks that each config.Tmux field's toml
// tag equals the Name of the one key whose Value reads that field.
func TestTmuxFieldTagsMatchKeyNames(t *testing.T) {
	typ := reflect.TypeOf(config.Tmux{})
	keys := config.TmuxKeys()
	if typ.NumField() != len(keys) {
		t.Fatalf("config.Tmux has %d fields, want one per key (%d)", typ.NumField(), len(keys))
	}
	seen := map[config.TmuxKey]string{}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		t.Run(field.Name, func(t *testing.T) {
			var tm config.Tmux
			reflect.ValueOf(&tm).Elem().Field(i).SetInt(1)
			var matched []config.TmuxKey
			for _, k := range keys {
				if tm.Value(k) == 1 {
					matched = append(matched, k)
				}
			}
			if len(matched) != 1 {
				t.Fatalf("field is read by %d keys (%v), want exactly one", len(matched), matched)
			}
			k := matched[0]
			if prev, dup := seen[k]; dup {
				t.Errorf("key %s reads both %s and %s", k.Name(), prev, field.Name)
			}
			seen[k] = field.Name
			if tag := field.Tag.Get("toml"); tag != k.Name() {
				t.Errorf("toml tag = %q, want key name %q", tag, k.Name())
			}
		})
	}
}

// TestTmuxKeyLoad loads each key through a config file and reads it back
// through its accessor (SR-4.1, SR-20.6; AC-CFG-01, AC-CFG-04).
func TestTmuxKeyLoad(t *testing.T) {
	for _, tc := range tmuxKeyTable {
		t.Run(tc.key.Name(), func(t *testing.T) {
			name := tc.key.Name()
			lowest := tc.minimum
			if lowest == 0 {
				lowest = 1 // no safe minimum: the smallest positive value loads
			}
			// A huge create timeout or pipe-close wait raises the grace
			// minimum, so the grace period is set high in the same file.
			overflow := []tmuxSetting{{tc.key, math.MaxInt64}}
			if tc.key != config.TmuxPendingGraceSeconds {
				overflow = append(overflow, tmuxSetting{config.TmuxPendingGraceSeconds, math.MaxInt64})
			}
			cases := []struct {
				name string
				path string
				want time.Duration
			}{
				{"file_absent", filepath.Join(t.TempDir(), "absent.toml"), tc.dur(tc.def)},
				{"file_empty", makeConfigFile(t, ""), tc.dur(tc.def)},
				{"no_tmux_table", makeConfigFile(t, "[relay]\npoll_base_ms = 250\n"), tc.dur(tc.def)},
				{"empty_tmux_table", tmuxConfigFile(t), tc.dur(tc.def)},
				{"zero_gives_default", tmuxConfigFile(t, tmuxSetting{tc.key, 0}), tc.dur(tc.def)},
				{"positive_value_read", tmuxConfigFile(t, tmuxSetting{tc.key, tc.def + 7}), tc.dur(tc.def + 7)},
				{"lowest_accepted_value_loads", tmuxConfigFile(t, tmuxSetting{tc.key, lowest}), tc.dur(lowest)},
				// Value 1 is below every fixed minimum, so a misspelt key that
				// were read would either be refused or change the value.
				{"misspelt_key_ignored", makeConfigFile(t, "[tmux]\n"+name[:len(name)-1]+" = 1\n"), tc.dur(tc.def)},
				{"overflow_gives_largest_duration", tmuxConfigFile(t, overflow...), maxDuration},
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					checkEffective(t, loadTmux(t, c.path), tc, c.want)
				})
			}
			t.Run("other_keys_keep_defaults", func(t *testing.T) {
				tm := loadTmux(t, tmuxConfigFile(t, tmuxSetting{tc.key, tc.def + 7}))
				for _, other := range tmuxKeyTable {
					if other.key != tc.key && other.accessor(tm) != other.dur(other.def) {
						t.Errorf("%s = %v, want its default %v", other.key.Name(), other.accessor(tm), other.dur(other.def))
					}
				}
			})
		})
	}
}

// TestTmuxAccessorOnGoStruct reads each key from a config.Tmux built in Go:
// non-positive gives the default; too large saturates, never wraps.
func TestTmuxAccessorOnGoStruct(t *testing.T) {
	for _, tc := range tmuxKeyTable {
		t.Run(tc.key.Name(), func(t *testing.T) {
			largest := int64(math.MaxInt64) / int64(tc.unit) // largest value expressible as a duration
			cases := []struct {
				name  string
				value int64
				want  time.Duration
			}{
				{"zero_value_struct", 0, tc.dur(tc.def)},
				{"negative_gives_default", -1, tc.dur(tc.def)},
				{"positive_value_read", tc.def + 7, tc.dur(tc.def + 7)},
				{"largest_expressible_exact", largest, tc.dur(largest)},
				{"one_past_expressible_saturates", largest + 1, maxDuration},
				{"max_int64_saturates", math.MaxInt64, maxDuration},
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					var tm config.Tmux
					tc.set(&tm, c.value)
					checkEffective(t, tm, tc, c.want)
				})
			}
		})
	}
}

// TestTmuxDefaultsAtOrAboveMinimums checks each default against its safe
// minimum at the other keys' defaults (SR-4.1).
func TestTmuxDefaultsAtOrAboveMinimums(t *testing.T) {
	for _, tc := range tmuxKeyTable {
		if tc.minimum == 0 {
			continue
		}
		t.Run(tc.key.Name(), func(t *testing.T) {
			if tc.def < tc.minimum {
				t.Errorf("default %d is below its safe minimum %d", tc.def, tc.minimum)
			}
			if got, _ := config.Default().Tmux.Minimum(tc.key); got != tc.minimum {
				t.Errorf("Default().Tmux.Minimum = %d, want %d", got, tc.minimum)
			}
		})
	}
}

// TestTmuxGraceRuleAccepts loads the grace-rule files SR-20.6 says must load.
func TestTmuxGraceRuleAccepts(t *testing.T) {
	cases := []struct {
		name       string
		settings   []tmuxSetting
		wantGrace  time.Duration
		wantCreate time.Duration
	}{
		{"grace_30_at_default_create_timeout",
			[]tmuxSetting{{config.TmuxPendingGraceSeconds, 30}},
			30 * time.Second, config.DefaultCreateTimeoutMs * time.Millisecond},
		{"create_15000_grace_36",
			[]tmuxSetting{{config.TmuxCreateTimeoutMs, 15000}, {config.TmuxPendingGraceSeconds, 36}},
			36 * time.Second, 15 * time.Second},
		{"create_39900_default_grace",
			[]tmuxSetting{{config.TmuxCreateTimeoutMs, 39900}},
			config.DefaultPendingGraceSeconds * time.Second, 39900 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tm := loadTmux(t, tmuxConfigFile(t, tc.settings...))
			if got := tm.EffectivePendingGrace(); got != tc.wantGrace {
				t.Errorf("EffectivePendingGrace() = %v, want %v", got, tc.wantGrace)
			}
			if got := tm.EffectiveCreateTimeout(); got != tc.wantCreate {
				t.Errorf("EffectiveCreateTimeout() = %v, want %v", got, tc.wantCreate)
			}
		})
	}
}

// TestPendingGraceMinimumSeconds covers the grace rule directly and through
// Tmux.Minimum: floor, rounding up, non-positive as default, no overflow.
func TestPendingGraceMinimumSeconds(t *testing.T) {
	cases := []struct {
		name          string
		create, pipe  int64
		wantMinSecond int64
	}{
		{"defaults", 5000, 100, 30},
		{"unset_counts_as_default", 0, 0, 30},
		{"negative_counts_as_default", -1, -1, 30},
		{"create_15000", 15000, 100, 36},
		{"create_39900_equals_default_grace", 39900, 100, 60},
		{"create_40000_exceeds_default_grace", 40000, 100, 61},
		{"whole_second_sum_not_rounded", 15900, 100, 36},
		{"one_ms_over_rounds_up", 15901, 100, 37},
		{"unset_pipe_uses_default_100", 15000, 0, 36},
		{"max_create_default_pipe", math.MaxInt64, 100, 9223372036854796},
		{"max_create_max_pipe_no_overflow", math.MaxInt64, math.MaxInt64, 18446744073709572},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := config.PendingGraceMinimumSeconds(tc.create, tc.pipe); got != tc.wantMinSecond {
				t.Errorf("PendingGraceMinimumSeconds(%d, %d) = %d, want %d", tc.create, tc.pipe, got, tc.wantMinSecond)
			}
			tm := config.Tmux{CreateTimeoutMs: tc.create, PipeCloseWaitMs: tc.pipe}
			if got, ok := tm.Minimum(config.TmuxPendingGraceSeconds); !ok || got != tc.wantMinSecond {
				t.Errorf("Tmux.Minimum(pending_grace_seconds) = (%d, %v), want (%d, true)", got, ok, tc.wantMinSecond)
			}
		})
	}
}
