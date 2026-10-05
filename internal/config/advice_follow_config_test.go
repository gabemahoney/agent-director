package config_test

// advice_follow_config_test.go (b.fji G2): a refused [tmux] table's
// description, or a refused [defaults] expire_retention_days's (b.sgw),
// [relay] or [pause] timeout_seconds's (b.8q2) or [pre_trust]
// lock_wait_seconds's (b.kr4), ends "A missing key, or 0, gives the
// default."; following it literally (drop each refused key, or set it to 0)
// must make the file load. A key whose default is itself below its minimum
// states its own change that loads instead, and the closing sentence leaves
// it out (b.n4q).

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
)

// advPaneTail is the [tmux] refusal's closing advice (G2).
const advPaneTail = "A missing key, or 0, gives the default."

// advPaneMixedTail is the closing advice beside a pending_grace_seconds whose
// default is below its minimum (b.n4q).
const advPaneMixedTail = "For every refused key other than [tmux] pending_grace_seconds, a missing key, or 0, " +
	"gives the default."

// advPaneRaisedCreate is a create timeout whose derived grace minimum,
// advPaneRaisedMinimum (⌈(60000 + 100) / 1000⌉ + 20 s), is above the 60 s
// grace default.
const (
	advPaneRaisedCreate  = 60000
	advPaneRaisedMinimum = 81
)

// advPaneRefusedKeys returns the keys desc names as refused ("[tmux] <name> "),
// as a caller reading the description finds them, and their names.
func advPaneRefusedKeys(desc string) ([]config.TmuxKey, []string) {
	var keys []config.TmuxKey
	var names []string
	for _, k := range config.TmuxKeys() {
		if strings.Contains(desc, "[tmux] "+k.Name()+" ") {
			keys, names = append(keys, k), append(names, k.Name())
		}
	}
	return keys, names
}

// advPaneWith returns file with each of set in place of its key's setting.
func advPaneWith(file []tmuxSetting, set ...tmuxSetting) []tmuxSetting {
	out := slices.Clone(file)
	for _, s := range set {
		out = append(slices.DeleteFunc(out, func(o tmuxSetting) bool { return o.key == s.key }), s)
	}
	return out
}

// advPaneFollows are the two literal readings of the tail, applied to the
// refused keys only: drop each from the file, or set each to 0. value is what
// the follow writes for a refused key outside [tmux] ("" drops it; advRangeFollow).
var advPaneFollows = []struct {
	name  string
	value string
	apply func(file []tmuxSetting, refused []config.TmuxKey) []tmuxSetting
}{
	{"drop the refused key", "", func(file []tmuxSetting, refused []config.TmuxKey) []tmuxSetting {
		return slices.DeleteFunc(slices.Clone(file), func(s tmuxSetting) bool { return slices.Contains(refused, s.key) })
	}},
	{"set the refused key to 0", "0", func(file []tmuxSetting, refused []config.TmuxKey) []tmuxSetting {
		out := slices.DeleteFunc(slices.Clone(file), func(s tmuxSetting) bool { return slices.Contains(refused, s.key) })
		for _, k := range refused {
			out = append(out, tmuxSetting{k, 0})
		}
		return out
	}},
}

// advRangeFollow returns keys with value in place of each key outside [tmux]
// that desc names as refused, as a caller reading the description finds them.
func advRangeFollow(desc string, keys rangeKeys, value string) rangeKeys {
	for _, k := range []struct {
		name string
		key  *string
	}{
		{"[defaults] expire_retention_days ", &keys.days},
		{"[relay] timeout_seconds ", &keys.relay},
		{"[pause] timeout_seconds ", &keys.pause},
		{"[pre_trust] lock_wait_seconds ", &keys.preTrust},
	} {
		if strings.Contains(desc, k.name) {
			*k.key = value
		}
	}
	return keys
}

// advRangeAssertDefaults fails unless cfg's keys outside [tmux] all read as their defaults.
func advRangeAssertDefaults(t *testing.T, cfg config.Config) {
	t.Helper()
	for _, k := range []struct {
		name      string
		got, want int
	}{
		{"expire_retention_days", cfg.Defaults.EffectiveExpireRetentionDays(), config.DefaultExpireRetentionDays},
		{"relay timeout_seconds", cfg.Relay.EffectiveTimeoutSeconds(), config.DefaultRelayTimeoutSeconds},
		{"pause timeout_seconds", cfg.Pause.EffectiveTimeoutSeconds(), config.DefaultPauseTimeoutSeconds},
		{"pre_trust lock_wait_seconds", cfg.PreTrust.EffectiveLockWaitSeconds(), config.DefaultPreTrustLockWaitSeconds},
	} {
		if k.got != k.want {
			t.Errorf("effective %s = %d after the follow; want its default %d", k.name, k.got, k.want)
		}
	}
}

// TestAdviceFollow_G2_TmuxRefusalMissingOrZeroGivesDefault: G2 "refused
// [tmux] values: ... . A missing key, or 0, gives the default." Each refused
// file is rewritten as the tail says and must then load with the default.
func TestAdviceFollow_G2_TmuxRefusalMissingOrZeroGivesDefault(t *testing.T) {
	cases := []struct {
		name string
		file []tmuxSetting
	}{
		{"fixed minimum, starting_session_seconds below it",
			[]tmuxSetting{{config.TmuxStartingSessionSeconds, config.MinStartingSessionSeconds - 1}}},
		{"fixed minimum, stopping_window_seconds below it",
			[]tmuxSetting{{config.TmuxStoppingWindowSeconds, config.MinStoppingWindowSeconds - 1}}},
		{"no minimum, kill_exit_wait_ms negative", []tmuxSetting{{config.TmuxKillExitWaitMs, -1}}},
		{"derived minimum at the defaults, pending_grace_seconds below it",
			[]tmuxSetting{{config.TmuxPendingGraceSeconds, config.PendingGraceFloorSeconds - 1}}},
	}
	for _, tc := range cases {
		for _, f := range advPaneFollows {
			t.Run(tc.name+"/"+f.name, func(t *testing.T) {
				desc := loadConfigError(t, tmuxConfigFile(t, tc.file...)).Err.Error()
				if !strings.Contains(desc, advPaneTail) {
					t.Fatalf("description %q does not end with the advice %q", desc, advPaneTail)
				}
				refused, names := advPaneRefusedKeys(desc)
				if len(refused) == 0 {
					t.Fatalf("description %q names no refused [tmux] key", desc)
				}

				cfg, err := config.Load(tmuxConfigFile(t, f.apply(tc.file, refused)...))

				if err != nil {
					t.Fatalf("after following %q (%s: %v), Load: %v", advPaneTail, f.name, names, err)
				}
				for _, k := range refused {
					if got, want := cfg.Tmux.Effective(k), (config.Tmux{}).Effective(k); got != want {
						t.Errorf("%s = %v after the follow; want its default %v", k.Name(), got, want)
					}
				}
			})
		}
	}
}

// TestAdviceFollow_G2_TmuxRefusalDefaultBelowMinimumSetOrLower: G2 (b.n4q)
// "[tmux] pending_grace_seconds ..., so set it to at least 81, or lower the
// effective create_timeout_ms and pipe_close_wait_ms to a total of T ms or less
// (a missing or 0 key counts as its default)." Each change, made alone, loads;
// an effective total over T (a missing or 0 pipe_close_wait_ms counted as its
// 100) is still refused.
func TestAdviceFollow_G2_TmuxRefusalDefaultBelowMinimumSetOrLower(t *testing.T) {
	grace, create, pipe := config.TmuxPendingGraceSeconds, config.TmuxCreateTimeoutMs, config.TmuxPipeCloseWaitMs
	cases := []struct {
		name  string
		file  []tmuxSetting
		total int64 // T, (the grace, or its 60 s default, - 20 s) × 1000; 0 below the 30 s floor, where none loads
	}{
		{"derived minimum raised past the default, pending_grace_seconds missing",
			[]tmuxSetting{{create, advPaneRaisedCreate}}, 40000},
		{"derived minimum raised past the default, pending_grace_seconds below it",
			[]tmuxSetting{{create, advPaneRaisedCreate}, {grace, config.DefaultPendingGraceSeconds - 10}}, 30000},
		{"derived minimum raised past the default, pending_grace_seconds below the floor",
			[]tmuxSetting{{create, advPaneRaisedCreate}, {grace, config.PendingGraceFloorSeconds - 1}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			desc := loadConfigError(t, tmuxConfigFile(t, tc.file...)).Err.Error()
			advice := graceFix(advPaneRaisedMinimum, tc.total) + "."
			if !strings.HasSuffix(desc, advice) {
				t.Fatalf("description %q does not end with the advice %q", desc, advice)
			}

			t.Run("set it to at least the minimum", func(t *testing.T) {
				cfg, err := config.Load(tmuxConfigFile(t, advPaneWith(tc.file, tmuxSetting{grace, advPaneRaisedMinimum})...))
				if err != nil {
					t.Fatalf("after following %q, Load: %v", advice, err)
				}
				if got, want := cfg.Tmux.EffectivePendingGrace(), advPaneRaisedMinimum*time.Second; got != want {
					t.Errorf("pending_grace_seconds = %v after the follow; want %v", got, want)
				}
			})
			if tc.total == 0 {
				return
			}
			// The file leaves pipe_close_wait_ms out, or sets it to 0, so it counts
			// as its default 100 in the effective total.
			rest := tc.total - config.DefaultPipeCloseWaitMs
			lowers := []struct {
				name  string
				set   []tmuxSetting
				loads bool
			}{
				{"lower the total, pipe_close_wait_ms missing, create_timeout_ms the rest of it",
					[]tmuxSetting{{create, rest}}, true},
				{"lower the total, pipe_close_wait_ms missing, create_timeout_ms 1 ms over the rest of it",
					[]tmuxSetting{{create, rest + 1}}, false},
				{"lower the total, pipe_close_wait_ms missing, create_timeout_ms all of it",
					[]tmuxSetting{{create, tc.total}}, false},
				{"lower the total, pipe_close_wait_ms 0, create_timeout_ms all of it",
					[]tmuxSetting{{create, tc.total}, {pipe, 0}}, false},
			}
			for _, l := range lowers {
				t.Run(l.name, func(t *testing.T) {
					path := tmuxConfigFile(t, advPaneWith(tc.file, l.set...)...)
					if l.loads {
						if _, err := config.Load(path); err != nil {
							t.Fatalf("after following %q, Load: %v", advice, err)
						}
						return
					}
					if desc := loadConfigError(t, path).Err.Error(); !strings.Contains(desc, "[tmux] "+grace.Name()+" ") {
						t.Errorf("an effective total over %d is refused, but not for %s: %q", tc.total, grace.Name(), desc)
					}
				})
			}
		})
	}
}

// TestAdviceFollow_G2_MixedRefusalOtherKeysGiveDefault: G2 (b.n4q) "For every
// refused key other than [tmux] pending_grace_seconds, a missing key, or 0,
// gives the default." beside that key's own "so set it to at least 81".
// Following both, the file loads.
func TestAdviceFollow_G2_MixedRefusalOtherKeysGiveDefault(t *testing.T) {
	grace, create := config.TmuxPendingGraceSeconds, config.TmuxCreateTimeoutMs
	cases := []struct {
		name string
		keys rangeKeys
		tmux []tmuxSetting
	}{
		{"beside a [tmux] key below its fixed minimum", rangeKeys{}, []tmuxSetting{
			{config.TmuxStartingSessionSeconds, config.MinStartingSessionSeconds - 1}, {create, advPaneRaisedCreate}}},
		{"beside a refused [defaults] expire_retention_days", rangeKeys{days: "-1"},
			[]tmuxSetting{{create, advPaneRaisedCreate}}},
		{"beside a refused [relay] and [pause] timeout_seconds", rangeKeys{relay: "2147484", pause: "-1"},
			[]tmuxSetting{{create, advPaneRaisedCreate}}},
	}
	for _, tc := range cases {
		for _, f := range advPaneFollows {
			t.Run(tc.name+"/"+f.name, func(t *testing.T) {
				desc := loadConfigError(t, keysFile(t, tc.keys, tc.tmux...)).Err.Error()
				fix := graceFix(advPaneRaisedMinimum, 40000)
				if !strings.Contains(desc, fix) || !strings.HasSuffix(desc, ". "+advPaneMixedTail) {
					t.Fatalf("description %q does not state %q and end with %q", desc, fix, advPaneMixedTail)
				}
				refused, _ := advPaneRefusedKeys(desc)
				others := slices.DeleteFunc(refused, func(k config.TmuxKey) bool { return k == grace })

				cfg, err := config.Load(keysFile(t, advRangeFollow(desc, tc.keys, f.value),
					advPaneWith(f.apply(tc.tmux, others), tmuxSetting{grace, advPaneRaisedMinimum})...))

				if err != nil {
					t.Fatalf("after following %q (%s) and %q, Load: %v", advPaneMixedTail, f.name, fix, err)
				}
				if got, want := cfg.Tmux.EffectivePendingGrace(), advPaneRaisedMinimum*time.Second; got != want {
					t.Errorf("pending_grace_seconds = %v after the follow; want %v", got, want)
				}
				for _, k := range others {
					if got, want := cfg.Tmux.Effective(k), (config.Tmux{}).Effective(k); got != want {
						t.Errorf("%s = %v after the follow; want its default %v", k.Name(), got, want)
					}
				}
				advRangeAssertDefaults(t, cfg)
			})
		}
	}
}

// TestAdviceFollow_G2_RangeRefusalMissingOrZeroGivesDefault: G2 "refused
// [defaults] values: ... . A missing key, or 0, gives the default." (b.sgw),
// and the same for [relay] and [pause] timeout_seconds (b.8q2) and [pre_trust]
// lock_wait_seconds (b.kr4), alone or listed with other tables. Each refused file is rewritten as the tail says
// and must then load with the defaults.
func TestAdviceFollow_G2_RangeRefusalMissingOrZeroGivesDefault(t *testing.T) {
	cases := []struct {
		name string
		keys rangeKeys
		tmux []tmuxSetting
	}{
		{"retention negative", rangeKeys{days: "-1"}, nil},
		{"retention above the largest", rangeKeys{days: "106752"}, nil},
		{"retention above the largest, beside a refused [tmux] value", rangeKeys{days: "365000"}, killNegativeTmux},
		{"relay timeout negative", rangeKeys{relay: "-1"}, nil},
		{"relay timeout above the largest", rangeKeys{relay: "2147484"}, nil},
		{"pause timeout above the largest", rangeKeys{pause: "9223372037"}, nil},
		{"pre_trust lock wait negative", rangeKeys{preTrust: "-1"}, nil},
		{"pre_trust lock wait above the largest", rangeKeys{preTrust: "9223372037"}, nil},
		{"every table refused", rangeKeys{days: "-1", relay: "9223372036", pause: "-1", preTrust: "-1"}, killNegativeTmux},
	}
	for _, tc := range cases {
		for _, f := range advPaneFollows {
			t.Run(tc.name+"/"+f.name, func(t *testing.T) {
				desc := loadConfigError(t, keysFile(t, tc.keys, tc.tmux...)).Err.Error()
				if !strings.HasSuffix(desc, advPaneTail) {
					t.Fatalf("description %q does not end with the advice %q", desc, advPaneTail)
				}
				follow := advRangeFollow(desc, tc.keys, f.value)
				if follow == tc.keys {
					t.Fatalf("description %q names no refused key outside [tmux]", desc)
				}
				refused, _ := advPaneRefusedKeys(desc)

				cfg, err := config.Load(keysFile(t, follow, f.apply(tc.tmux, refused)...))

				if err != nil {
					t.Fatalf("after following %q (%s), Load: %v", advPaneTail, f.name, err)
				}
				advRangeAssertDefaults(t, cfg)
			})
		}
	}
}
