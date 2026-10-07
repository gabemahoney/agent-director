package config_test

// advice_follow_config_test.go (b.fji G2): a refused [tmux] table's
// description, or a refused [defaults] expire_retention_days's (b.sgw),
// [relay] or [pause] timeout_seconds's (b.8q2) or [pre_trust]
// lock_wait_seconds's (b.kr4), ends "A missing key, or 0, gives the
// default."; following it literally (drop each refused key, or set it to 0)
// must make the file load. Beside a refused pending_grace_seconds whose derived
// minimum is above its default, the closing sentence adds that a missing key,
// or 0, gives that minimum (advPaneRaisedTail), and following that loads it
// (b.9e1). G3 (b.p8n): a key
// set under names that differ only in letter case is refused, ending with
// caseVariantAdvice; removing all but one of its names must make the file load
// that name's value, and a spawn template's likewise (b.2u1).

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
)

// advPaneTail is the [tmux] refusal's closing advice (G2).
const advPaneTail = "A missing key, or 0, gives the default."

// advPaneRaisedTail is the closing advice beside a refused
// pending_grace_seconds whose derived minimum is above its 60 s default (b.9e1).
const advPaneRaisedTail = "A missing key, or 0, gives the default, or for [tmux] pending_grace_seconds its safe " +
	"minimum when that is larger."

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

// TestAdviceFollow_G2_RaisedGraceMinimumMissingOrZeroGivesIt: G2 (b.9e1)
// "[tmux] pending_grace_seconds = <n>, below its safe minimum 81 s (computed
// from ...). A missing key, or 0, gives the default, or for [tmux]
// pending_grace_seconds its safe minimum when that is larger." With
// create_timeout_ms raised, a missing pending_grace_seconds is never refused,
// and the plain advPaneTail closes a refusal of other keys; dropping each
// refused key, or setting it to 0, loads the 81 s minimum and every other
// refused key's default.
func TestAdviceFollow_G2_RaisedGraceMinimumMissingOrZeroGivesIt(t *testing.T) {
	grace := config.TmuxPendingGraceSeconds
	raised := tmuxSetting{config.TmuxCreateTimeoutMs, advPaneRaisedCreate}
	startingBelow := tmuxSetting{config.TmuxStartingSessionSeconds, config.MinStartingSessionSeconds - 1}
	cases := []struct {
		name string
		keys rangeKeys
		tmux []tmuxSetting
	}{
		{"pending_grace_seconds at its default, below the minimum", rangeKeys{},
			[]tmuxSetting{raised, {grace, config.DefaultPendingGraceSeconds}}},
		{"pending_grace_seconds below the floor", rangeKeys{}, []tmuxSetting{raised, {grace, config.PendingGraceFloorSeconds - 1}}},
		{"pending_grace_seconds negative", rangeKeys{}, []tmuxSetting{raised, {grace, -1}}},
		{"pending_grace_seconds below it, beside a [tmux] key below its fixed minimum", rangeKeys{},
			[]tmuxSetting{startingBelow, raised, {grace, 50}}},
		{"pending_grace_seconds below it, beside a refused [relay] timeout_seconds", rangeKeys{relay: "-1"},
			[]tmuxSetting{raised, {grace, 50}}},
		{"pending_grace_seconds missing, beside a [tmux] key below its fixed minimum", rangeKeys{},
			[]tmuxSetting{startingBelow, raised}},
		{"pending_grace_seconds missing, beside a refused [defaults] expire_retention_days", rangeKeys{days: "-1"},
			[]tmuxSetting{raised}},
		{"pending_grace_seconds missing, beside refused [relay] and [pause] timeout_seconds",
			rangeKeys{relay: "2147484", pause: "-1"}, []tmuxSetting{raised}},
	}
	for _, tc := range cases {
		graceSet := slices.ContainsFunc(tc.tmux, func(s tmuxSetting) bool { return s.key == grace })
		for _, f := range advPaneFollows {
			t.Run(tc.name+"/"+f.name, func(t *testing.T) {
				desc := loadConfigError(t, keysFile(t, tc.keys, tc.tmux...)).Err.Error()
				tail := advPaneTail
				if graceSet {
					tail = advPaneRaisedTail
				}
				if !strings.HasSuffix(desc, " "+tail) {
					t.Fatalf("description %q does not end with the advice %q", desc, tail)
				}
				refused, names := advPaneRefusedKeys(desc)
				if named := slices.Contains(refused, grace); named != graceSet {
					t.Fatalf("description names %s as refused = %v; want %v, as the file sets it: %q",
						grace.Name(), named, graceSet, desc)
				}

				cfg, err := config.Load(keysFile(t, advRangeFollow(desc, tc.keys, f.value), f.apply(tc.tmux, refused)...))

				if err != nil {
					t.Fatalf("after following the advice (%s: %v), Load: %v", f.name, names, err)
				}
				if got, want := cfg.Tmux.EffectivePendingGrace(), advPaneRaisedMinimum*time.Second; got != want {
					t.Errorf("pending_grace_seconds = %v after the follow; want its minimum %v", got, want)
				}
				for _, k := range refused {
					if got, want := cfg.Tmux.Effective(k), (config.Tmux{}).Effective(k); k != grace && got != want {
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

// advCaseEntry is one key a G3 file sets: its table ("" for a top-level key)
// and its own name as written, and its TOML value.
type advCaseEntry struct{ table, key, value string }

// name is e's name as the letter-case refusal lists it.
func (e advCaseEntry) name() string {
	if e.table == "" {
		return e.key
	}
	return "[" + e.table + "] " + e.key
}

// advCaseBody is the TOML of entries: top-level keys first, then each table's
// keys under one header, tables in the order of their first entry.
func advCaseBody(entries []advCaseEntry) string {
	tables := []string{""}
	lines := make(map[string]string)
	for _, e := range entries {
		if _, ok := lines[e.table]; !ok && e.table != "" {
			tables = append(tables, e.table)
		}
		lines[e.table] += e.key + " = " + e.value + "\n"
	}
	var b strings.Builder
	for _, tb := range tables {
		if tb != "" {
			b.WriteString("[" + tb + "]\n")
		}
		b.WriteString(lines[tb] + "\n")
	}
	return b.String()
}

// advCaseFile writes entries as advCaseBody and returns the file's path.
func advCaseFile(t *testing.T, entries []advCaseEntry) string {
	t.Helper()
	return makeConfigFile(t, advCaseBody(entries))
}

// advCaseListed returns the names desc lists for each refused key, as a
// caller reading the description finds them.
func advCaseListed(t *testing.T, desc string) [][]string {
	t.Helper()
	_, list, ok := strings.Cut(desc, "differ only in letter case: ")
	list, _, ok2 := strings.Cut(list, ". agent-director matches")
	if !ok || !ok2 {
		t.Fatalf("description %q lists no names", desc)
	}
	var groups [][]string
	for _, g := range strings.Split(list, "; ") {
		groups = append(groups, strings.Split(strings.Replace(g, " and ", ", ", 1), ", "))
	}
	return groups
}

// advCaseKeeps are two literal follows of G3's advice: the name of each listed
// key that pick keeps.
var advCaseKeeps = []struct {
	name string
	pick func(names []string) string
}{
	{"keep the first name", func(names []string) string { return names[0] }},
	{"keep the last name", func(names []string) string { return names[len(names)-1] }},
}

// advCaseFollow returns file without the listed names that pick does not keep.
func advCaseFollow(file []advCaseEntry, listed [][]string, pick func(names []string) string) []advCaseEntry {
	removed := make(map[string]bool)
	for _, names := range listed {
		for _, n := range names {
			removed[n] = n != pick(names)
		}
	}
	return slices.DeleteFunc(slices.Clone(file), func(e advCaseEntry) bool { return removed[e.name()] })
}

// TestAdviceFollow_G3_CaseVariantKeysSetEachOnce: G3 (b.p8n) "refused keys set
// more than once, under names that differ only in letter case: ... . Set each
// key once, removing all but one of the names listed for it." Keeping the first
// or the last name of each, the file loads the kept values.
func TestAdviceFollow_G3_CaseVariantKeysSetEachOnce(t *testing.T) {
	file := []advCaseEntry{
		{"Store", "db_path", `"/upper.db"`},
		{"relay", "timeout_seconds", "60"},
		{"store", "db_path", `"/lower.db"`},
		{"relay", "TIMEOUT_SECONDS", "120"},
		{"STORE", "db_path", `"/third.db"`},
		{"defaults", "relay_mode", `"on"`}, // set once, so never listed
	}
	desc := loadConfigError(t, advCaseFile(t, file)).Err.Error()
	if !strings.HasSuffix(desc, caseVariantAdvice) {
		t.Fatalf("description %q does not end with the advice %q", desc, caseVariantAdvice)
	}
	listed := advCaseListed(t, desc)

	for _, keep := range advCaseKeeps {
		t.Run(keep.name, func(t *testing.T) {
			kept := advCaseFollow(file, listed, keep.pick)

			cfg, err := config.Load(advCaseFile(t, kept))

			if err != nil {
				t.Fatalf("after following %q (%s of %q), Load: %v", caseVariantAdvice, keep.name, listed, err)
			}
			read := map[string]string{"db_path": quoted(cfg.Store.DbPath),
				"timeout_seconds": fmt.Sprint(cfg.Relay.TimeoutSeconds), "relay_mode": quoted(cfg.Defaults.RelayMode)}
			for _, e := range kept {
				if got := read[strings.ToLower(e.key)]; got != e.value {
					t.Errorf("%s = %s after the follow; want the kept %s", e.name(), got, e.value)
				}
			}
		})
	}
}

// TestAdviceFollow_G3_TemplateCaseVariantKeysSetEachOnce: G3 (b.2u1), the same
// advice from LoadTemplate. Keeping the first or the last name of each, the
// template loads the kept values.
func TestAdviceFollow_G3_TemplateCaseVariantKeysSetEachOnce(t *testing.T) {
	file := []advCaseEntry{
		{"", "RELAY_MODE", `"on"`},
		{"extra_env", "FOO", `"a"`},
		{"", "relay_mode", `"off"`},
		{"extra_env", "foo", `"b"`}, // another variable, so never listed
		{"EXTRA_ENV", "FOO", `"c"`},
	}
	dir := templatesDir(t)
	writeTemplate(t, dir, "dup", advCaseBody(file))
	_, err := config.LoadTemplate("dup")
	if !errors.Is(err, config.ErrTemplateMalformed) || !strings.HasSuffix(err.Error(), caseVariantAdvice) {
		t.Fatalf("LoadTemplate = %v; want ErrTemplateMalformed ending with the advice %q", err, caseVariantAdvice)
	}
	listed := advCaseListed(t, err.Error())

	for _, keep := range advCaseKeeps {
		t.Run(keep.name, func(t *testing.T) {
			kept := advCaseFollow(file, listed, keep.pick)
			writeTemplate(t, dir, "dup", advCaseBody(kept))

			tf, err := config.LoadTemplate("dup")

			if err != nil {
				t.Fatalf("after following %q (%s of %q), LoadTemplate: %v", caseVariantAdvice, keep.name, listed, err)
			}
			for _, e := range kept {
				got := quoted(tf.RelayMode)
				if e.table != "" {
					got = quoted(tf.ExtraEnv[e.key])
				}
				if got != e.value {
					t.Errorf("%s = %s after the follow; want the kept %s", e.name(), got, e.value)
				}
			}
		})
	}
}
