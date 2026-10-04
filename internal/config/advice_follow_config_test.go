package config_test

// advice_follow_config_test.go (b.fji G2): a refused [tmux] table's
// description, or a refused [defaults] expire_retention_days's (b.sgw), ends
// "A missing key, or 0, gives the default."; following it literally (drop
// each refused key, or set it to 0) must make the file load.

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
)

// advPaneTail is the [tmux] refusal's closing advice (G2).
const advPaneTail = "A missing key, or 0, gives the default."

// advPaneRaisedCreate is a create timeout whose derived grace minimum (81 s)
// is above the 60 s grace default.
const advPaneRaisedCreate = 60000

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

// advPaneFollows are the two literal readings of the tail, applied to the
// refused keys only: drop each from the file, or set each to 0. days is what
// the follow writes for a refused [defaults] expire_retention_days ("" drops it).
var advPaneFollows = []struct {
	name  string
	days  string
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

// TestAdviceFollow_G2_TmuxRefusalMissingOrZeroGivesDefault: G2 "refused
// [tmux] values: ... . A missing key, or 0, gives the default." Each refused
// file is rewritten as the tail says and must then load with the default.
func TestAdviceFollow_G2_TmuxRefusalMissingOrZeroGivesDefault(t *testing.T) {
	grace, create := config.TmuxPendingGraceSeconds, config.TmuxCreateTimeoutMs
	const raisedBroken = "the raised create_timeout_ms lifts the derived pending_grace_seconds minimum above its " +
		"60 s default, so a missing or 0 key is refused again; only a grace above the minimum or a lower " +
		"create_timeout_ms/pipe_close_wait_ms loads"
	cases := []struct {
		name   string
		file   []tmuxSetting
		broken string // non-empty: the follow is known not to load (why)
	}{
		{name: "fixed minimum, starting_session_seconds below it",
			file: []tmuxSetting{{config.TmuxStartingSessionSeconds, config.MinStartingSessionSeconds - 1}}},
		{name: "fixed minimum, stopping_window_seconds below it",
			file: []tmuxSetting{{config.TmuxStoppingWindowSeconds, config.MinStoppingWindowSeconds - 1}}},
		{name: "no minimum, kill_exit_wait_ms negative", file: []tmuxSetting{{config.TmuxKillExitWaitMs, -1}}},
		{name: "derived minimum at the defaults, pending_grace_seconds below it",
			file: []tmuxSetting{{grace, config.PendingGraceFloorSeconds - 1}}},
		{name: "derived minimum raised past the default, pending_grace_seconds below it",
			file:   []tmuxSetting{{create, advPaneRaisedCreate}, {grace, config.DefaultPendingGraceSeconds - 10}},
			broken: raisedBroken},
		{name: "derived minimum raised past the default, pending_grace_seconds missing",
			file: []tmuxSetting{{create, advPaneRaisedCreate}}, broken: raisedBroken},
	}
	for _, tc := range cases {
		for _, f := range advPaneFollows {
			t.Run(tc.name+"/"+f.name, func(t *testing.T) {
				_, err := config.Load(tmuxConfigFile(t, tc.file...))
				var ce *config.ConfigError
				if !errors.As(err, &ce) {
					t.Fatalf("Load = %v; want a *config.ConfigError refusing the [tmux] table", err)
				}
				desc := ce.Err.Error()
				if !strings.Contains(desc, advPaneTail) {
					t.Fatalf("description %q does not end with the advice %q", desc, advPaneTail)
				}
				refused, names := advPaneRefusedKeys(desc)
				if len(refused) == 0 {
					t.Fatalf("description %q names no refused [tmux] key", desc)
				}
				if tc.broken != "" {
					knownBrokenAdvice(t, "G2", tc.broken)
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

// TestAdviceFollow_G2_DefaultsRefusalMissingOrZeroGivesDefault: G2 "refused
// [defaults] values: ... . A missing key, or 0, gives the default." (b.sgw).
// Each refused file is rewritten as the tail says and must then load with the
// default retention.
func TestAdviceFollow_G2_DefaultsRefusalMissingOrZeroGivesDefault(t *testing.T) {
	cases := []struct {
		name string
		days string
		tmux []tmuxSetting
	}{
		{"negative", "-1", nil},
		{"above the largest", "106752", nil},
		{"above the largest, beside a refused [tmux] value", "365000",
			[]tmuxSetting{{config.TmuxKillExitWaitMs, -1}}},
	}
	for _, tc := range cases {
		for _, f := range advPaneFollows {
			t.Run(tc.name+"/"+f.name, func(t *testing.T) {
				_, err := config.Load(configFile(t, tc.days, tc.tmux...))
				var ce *config.ConfigError
				if !errors.As(err, &ce) {
					t.Fatalf("Load = %v; want a *config.ConfigError refusing expire_retention_days", err)
				}
				desc := ce.Err.Error()
				if !strings.Contains(desc, advPaneTail) {
					t.Fatalf("description %q does not end with the advice %q", desc, advPaneTail)
				}
				if !strings.Contains(desc, "[defaults] expire_retention_days ") {
					t.Fatalf("description %q does not name [defaults] expire_retention_days", desc)
				}
				refused, _ := advPaneRefusedKeys(desc)

				cfg, err := config.Load(configFile(t, f.days, f.apply(tc.tmux, refused)...))

				if err != nil {
					t.Fatalf("after following %q (%s), Load: %v", advPaneTail, f.name, err)
				}
				if got := cfg.Defaults.EffectiveExpireRetentionDays(); got != config.DefaultExpireRetentionDays {
					t.Errorf("effective expire_retention_days = %d after the follow; want its default %d",
						got, config.DefaultExpireRetentionDays)
				}
			})
		}
	}
}
