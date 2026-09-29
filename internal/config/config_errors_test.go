package config_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/gabemahoney/agent-director/internal/config"
)

// loadConfigError loads path, requires a *config.ConfigError for it and
// checks that the returned Config is Default() with resolved paths.
func loadConfigError(t *testing.T, path string) *config.ConfigError {
	t.Helper()
	cfg, err := config.Load(path)
	var ce *config.ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *config.ConfigError, got %T: %v", err, err)
	}
	if ce.Path != path {
		t.Errorf("ConfigError.Path: got %q, want %q", ce.Path, path)
	}
	if ce.Unwrap() == nil {
		t.Errorf("ConfigError should wrap the underlying error, got nil")
	}
	home := homeDir(t)
	wantDB := filepath.Join(home, ".agent-director/state.db")
	wantLog := filepath.Join(home, ".agent-director/errors.log")
	if cfg.Store.DbPath != wantDB || cfg.Log.ErrorLogPath != wantLog {
		t.Errorf("path fields not resolved on error: %+v", cfg)
	}
	def := config.Default()
	if cfg.Defaults != def.Defaults || cfg.Relay != def.Relay || cfg.Pause != def.Pause || cfg.Tmux != def.Tmux {
		t.Errorf("non-path defaults drifted on error: got=%+v want=%+v", cfg, def)
	}
	return ce
}

// srClosing reports whether desc says that a missing key, or 0, gives the default.
func srClosing(desc string) bool {
	lower := strings.ToLower(desc)
	return strings.Contains(lower, "missing key") && strings.Contains(lower, "or 0") &&
		strings.Contains(lower, "gives the default")
}

// refusalDescription loads a file Load must refuse and returns the SR-4.1
// description (ConfigError.Err, which excludes the path) after checking the
// parts every refusal shares.
func refusalDescription(t *testing.T, path string) string {
	t.Helper()
	desc := loadConfigError(t, path).Err.Error()
	if !srClosing(desc) {
		t.Errorf("description does not say that a missing key, or 0, gives the default: %q", desc)
	}
	lower := strings.ToLower(desc)
	for _, claim := range []string{"in force", "using", "instead", "raised", "clamp", "applied", "falls back"} {
		if strings.Contains(lower, claim) {
			t.Errorf("description claims a default or minimum is in force (%q): %q", claim, desc)
		}
	}
	if strings.ContainsRune(desc, '\u2212') {
		t.Errorf("description uses a Unicode minus sign, want ASCII '-': %q", desc)
	}
	return desc
}

// refusedClause returns the part of desc about key k: from "[tmux] <key>" to
// the next ';' or '.', failing the test when desc does not name k.
func refusedClause(t *testing.T, desc string, k config.TmuxKey) string {
	t.Helper()
	start := strings.Index(desc, "[tmux] "+k.Name())
	if start < 0 {
		t.Fatalf("description does not name [tmux] %s: %q", k.Name(), desc)
	}
	clause := desc[start:]
	if end := strings.IndexAny(clause, ";."); end >= 0 {
		clause = clause[:end]
	}
	return clause
}

// refusal is what the description must say about one refused key.
type refusal struct {
	key       config.TmuxKey
	value     int64    // the configured value, or the default for a grace period refused at it
	atDefault bool     // value is the grace period's default and must be said to be
	minimum   string   // safe minimum with its unit, e.g. "60 s"; "" when the value must be positive
	from      [2]int64 // grace period only: the effective create_timeout_ms and pipe_close_wait_ms
}

// checkRefusal asserts that desc describes r in r.key's own clause.
func checkRefusal(t *testing.T, desc string, r refusal) {
	t.Helper()
	clause := refusedClause(t, desc, r.key)
	if v := fmt.Sprint(r.value); !strings.Contains(clause, v) {
		t.Errorf("clause does not name the value %s: %q", v, clause)
	}
	if said := strings.Contains(strings.ToLower(clause), "default"); said != r.atDefault {
		t.Errorf("clause says the value is the default = %v, want %v: %q", said, r.atDefault, clause)
	}
	if r.minimum == "" {
		if !strings.Contains(clause, "must be positive") || strings.Contains(clause, "minimum") {
			t.Errorf("clause should say the value must be positive, naming no minimum: %q", clause)
		}
	} else if !strings.Contains(clause, r.minimum) {
		t.Errorf("clause does not name the safe minimum %q: %q", r.minimum, clause)
	}
	if r.key == config.TmuxPendingGraceSeconds {
		for i, k := range []config.TmuxKey{config.TmuxCreateTimeoutMs, config.TmuxPipeCloseWaitMs} {
			if !strings.Contains(clause, k.Name()) || !strings.Contains(clause, fmt.Sprint(r.from[i])) {
				t.Errorf("clause does not name the effective %s %d: %q", k.Name(), r.from[i], clause)
			}
		}
	}
}

// minimumText renders a safe minimum of tc with its unit symbol, as one piece.
func minimumText(tc tmuxKeyCase, v int64) string {
	if tc.unit == time.Millisecond {
		return fmt.Sprintf("%d ms", v)
	}
	return fmt.Sprintf("%d s", v)
}

// defaultsFrom is the grace rule's input at the create timeout and pipe-close wait defaults.
var defaultsFrom = [2]int64{config.DefaultCreateTimeoutMs, config.DefaultPipeCloseWaitMs}

func TestLoadMalformedReturnsTypedError(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"unclosed_array", "key = [unclosed\n"},
		{"duplicate_key_in_same_table", "\n[defaults]\nrelay_mode = \"off\"\nrelay_mode = \"on\"\n"},
		{"non_toml_garbage", "<<< this is not toml >>>\n"},
		// A refused [tmux] value in a file that does not parse: the parse
		// error wins and no value is checked (SR-4.1).
		{"refused_tmux_value_then_syntax_error", "[tmux]\nstarting_session_seconds = -1\nkey = [unclosed\n"},
		{"refused_tmux_value_duplicated", "[tmux]\nkill_exit_wait_ms = -1\nkill_exit_wait_ms = -2\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ce := loadConfigError(t, makeConfigFile(t, tc.content))
			var pe toml.ParseError
			if !errors.As(ce, &pe) {
				t.Errorf("expected the TOML parse error, got %T: %v", ce.Err, ce.Err)
			}
			if srClosing(ce.Err.Error()) {
				t.Errorf("parse failure reported as a [tmux] refusal: %q", ce.Err)
			}
		})
	}
}

// TestTmuxRefusesNonInteger checks that a float, string or boolean value of
// each key fails Load as malformed, never falling back to the default.
func TestTmuxRefusesNonInteger(t *testing.T) {
	values := []struct{ name, toml string }{
		{"float", "60.5"},
		{"string", `"60"`},
		{"boolean", "true"},
	}
	for _, tc := range tmuxKeyTable {
		t.Run(tc.key.Name(), func(t *testing.T) {
			for _, v := range values {
				t.Run(v.name, func(t *testing.T) {
					path := makeConfigFile(t, fmt.Sprintf("[tmux]\n%s = %s\n", tc.key.Name(), v.toml))
					desc := loadConfigError(t, path).Err.Error()
					if !strings.Contains(desc, tc.key.Name()) {
						t.Errorf("error does not name the key: %q", desc)
					}
				})
			}
		})
	}
}

// TestTmuxRefusesValue checks the SR-4.1 refusal of -1 for every key and of
// the safe minimum minus one for the keys with a minimum.
func TestTmuxRefusesValue(t *testing.T) {
	for _, tc := range tmuxKeyTable {
		t.Run(tc.key.Name(), func(t *testing.T) {
			minimum := ""
			if tc.minimum > 0 {
				minimum = minimumText(tc, tc.minimum)
			}
			t.Run("negative_one", func(t *testing.T) {
				desc := refusalDescription(t, tmuxConfigFile(t, tmuxSetting{tc.key, -1}))
				checkRefusal(t, desc, refusal{key: tc.key, value: -1, minimum: minimum, from: defaultsFrom})
			})
			if tc.minimum == 0 {
				return
			}
			t.Run("minimum_minus_one", func(t *testing.T) {
				below := tc.minimum - 1
				desc := refusalDescription(t, tmuxConfigFile(t, tmuxSetting{tc.key, below}))
				checkRefusal(t, desc, refusal{key: tc.key, value: below, minimum: minimum, from: defaultsFrom})
			})
		})
	}
}

// TestTmuxGraceRuleRefusals checks the grace period's derived-minimum
// refusals; 36, 15000, 100, 60 and 61 are the SR-4.1 worked-example literals.
func TestTmuxGraceRuleRefusals(t *testing.T) {
	grace, create, pipe := config.TmuxPendingGraceSeconds, config.TmuxCreateTimeoutMs, config.TmuxPipeCloseWaitMs
	cases := []struct {
		name     string
		settings []tmuxSetting
		want     refusal
		notNamed config.TmuxKey // a key the description must not name as refused
	}{
		{"grace_30_create_15000",
			[]tmuxSetting{{create, 15000}, {grace, 30}},
			refusal{key: grace, value: 30, minimum: "36 s", from: [2]int64{15000, 100}}, create},
		{"create_40000_grace_missing",
			[]tmuxSetting{{create, 40000}},
			refusal{key: grace, value: 60, atDefault: true, minimum: "61 s", from: [2]int64{40000, 100}}, create},
		{"create_40000_grace_zero",
			[]tmuxSetting{{create, 40000}, {grace, 0}},
			refusal{key: grace, value: 60, atDefault: true, minimum: "61 s", from: [2]int64{40000, 100}}, create},
		{"create_40000_grace_explicit_60",
			[]tmuxSetting{{create, 40000}, {grace, 60}},
			refusal{key: grace, value: 60, minimum: "61 s", from: [2]int64{40000, 100}}, create},
		// A refused negative create timeout or pipe-close wait counts as its
		// default in the grace rule, so grace 30 still loads.
		{"negative_create_counts_as_default",
			[]tmuxSetting{{create, -1}, {grace, 30}},
			refusal{key: create, value: -1}, grace},
		{"negative_pipe_counts_as_default",
			[]tmuxSetting{{pipe, -1}, {grace, 30}},
			refusal{key: pipe, value: -1}, grace},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			desc := refusalDescription(t, tmuxConfigFile(t, tc.settings...))
			checkRefusal(t, desc, tc.want)
			if strings.Contains(desc, "[tmux] "+tc.notNamed.Name()) {
				t.Errorf("description names [tmux] %s as refused: %q", tc.notNamed.Name(), desc)
			}
		})
	}
}

// TestTmuxRefusalNamesKeysInTableOrder checks that every refused key is named,
// in SR-4.1 table order whatever its order in the file.
func TestTmuxRefusalNamesKeysInTableOrder(t *testing.T) {
	var allNegative []tmuxSetting
	for i := len(tmuxKeyTable) - 1; i >= 0; i-- {
		allNegative = append(allNegative, tmuxSetting{tmuxKeyTable[i].key, -1})
	}
	cases := []struct {
		name     string
		settings []tmuxSetting // written in this order, the reverse of table order
	}{
		{"two_keys_reversed", []tmuxSetting{
			{config.TmuxKillExitWaitMs, -3},
			{config.TmuxStartingSessionSeconds, config.MinStartingSessionSeconds - 1},
		}},
		{"all_nine_reversed", allNegative},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			desc := refusalDescription(t, tmuxConfigFile(t, tc.settings...))
			last := -1
			for i := len(tc.settings) - 1; i >= 0; i-- {
				k := tc.settings[i].key
				refusedClause(t, desc, k)
				at := strings.Index(desc, "[tmux] "+k.Name())
				if at < last {
					t.Errorf("[tmux] %s is named out of table order: %q", k.Name(), desc)
				}
				last = at
			}
		})
	}
}

func TestLoadIgnoresUnknownKey(t *testing.T) {
	// BurntSushi/toml ignores unknown top-level keys by default. This test
	// pins that behavior so a future opt-in to strict mode is a conscious
	// choice rather than an accidental regression.
	path := makeConfigFile(t, `
unknown_top_level_key = 42

[relay]
poll_base_ms = 150
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Relay.PollBaseMs != 150 {
		t.Errorf("known field still applied: got PollBaseMs=%d, want 150", cfg.Relay.PollBaseMs)
	}
}
