package config_test

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
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
	if cfg.Defaults != def.Defaults || cfg.Relay != def.Relay || cfg.Pause != def.Pause || cfg.PreTrust != def.PreTrust ||
		cfg.Store.BusyTimeoutMs != def.Store.BusyTimeoutMs || cfg.Tmux != def.Tmux {
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
// parts every refusal shares, its advice among them (checkAdvice).
func refusalDescription(t *testing.T, path string) string {
	t.Helper()
	desc := loadConfigError(t, path).Err.Error()
	checkAdvice(t, desc)
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

// statedMinimum matches the safe minimum a refused key's clause states.
var statedMinimum = regexp.MustCompile(`safe minimum (\d+) `)

// raisedTail is the closing sentence when a refused pending_grace_seconds'
// safe minimum is above its default, so a missing key, or 0, gives that
// minimum (b.9e1).
const raisedTail = ". A missing key, or 0, gives the default, or for [tmux] pending_grace_seconds its safe " +
	"minimum when that is larger."

// checkAdvice checks desc's advice (b.9e1): no refused key's description
// contains "; ", the separator between them, so the list after "values: "
// splits on it into one part per refused key, and desc ends with raisedTail
// exactly when a refused [tmux] key states a safe minimum above its default,
// otherwise with refusalTail, that a missing key, or 0, gives the default.
// The refused keys outside [tmux] are b.sgw's, b.8q2's, b.kr4's and b.c7f's.
func checkAdvice(t *testing.T, desc string) {
	t.Helper()
	_, list, ok := strings.Cut(desc, " values: ")
	if !ok {
		t.Fatalf("description lists no refused values: %q", desc)
	}
	// raisedTail names [tmux] pending_grace_seconds; count only the list's keys.
	list = strings.TrimSuffix(strings.TrimSuffix(list, raisedTail), refusalTail)
	refused := 0
	for _, k := range []string{"[defaults] expire_retention_days ", "[relay] timeout_seconds ", "[pause] timeout_seconds ",
		"[pre_trust] lock_wait_seconds ", "[store] busy_timeout_ms "} {
		refused += strings.Count(list, k)
	}
	above := false
	for _, k := range config.TmuxKeys() {
		if !strings.Contains(list, "[tmux] "+k.Name()+" ") {
			continue
		}
		refused++
		clause := refusedClause(t, desc, k)
		if m := statedMinimum.FindStringSubmatch(clause); m != nil {
			minimum, err := strconv.ParseInt(m[1], 10, 64)
			if err != nil {
				t.Fatalf("clause states an unreadable safe minimum %q: %q", m[1], clause)
			}
			above = above || minimum > k.DefaultValue()
		}
		if strings.Contains(clause, "default") {
			t.Errorf("clause speaks of the default: %q", clause)
		}
	}
	if parts := strings.Split(list, "; "); len(parts) != refused {
		t.Errorf("the refused values split on \"; \" into %d parts; want one per refused key, %d: %q", len(parts), refused, desc)
	}
	tail := refusalTail
	if above {
		tail = raisedTail
	}
	if !strings.HasSuffix(desc, tail) {
		t.Errorf("description does not end with %q: %q", tail, desc)
	}
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
	key     config.TmuxKey
	value   int64    // the configured value
	minimum string   // safe minimum with its unit, e.g. "60 s"; "" when the value must be positive
	from    [2]int64 // grace period only: the effective create_timeout_ms and pipe_close_wait_ms
	raised  bool     // grace period only: the minimum is above the default, so desc ends with raisedTail
}

// checkRefusal asserts that desc describes r in r.key's own clause, and ends
// with raisedTail exactly when r.raised.
func checkRefusal(t *testing.T, desc string, r refusal) {
	t.Helper()
	clause := refusedClause(t, desc, r.key)
	if v := fmt.Sprint(r.value); !strings.Contains(clause, v) {
		t.Errorf("clause does not name the value %s: %q", v, clause)
	}
	if strings.Contains(strings.ToLower(clause), "default") {
		t.Errorf("clause speaks of the default: %q", clause)
	}
	if got := strings.HasSuffix(desc, raisedTail); got != r.raised {
		t.Errorf("description ends with %q = %v, want %v: %q", raisedTail, got, r.raised, desc)
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

// caseVariantAdvice is the letter-case refusal's closing advice (b.p8n; G3).
const caseVariantAdvice = "Set each key once, removing all but one of the names listed for it."

// caseVariantText is Load's refusal of a file setting keys under names that
// differ only in letter case (b.p8n), each of groups listing one key's names.
func caseVariantText(groups ...string) string { return caseVariantTextExcept("", groups...) }

// caseVariantTextExcept is caseVariantText whose except, unless "", lists the
// map tables whose key names keep their letter case ("[extra_env] and [labels]").
func caseVariantTextExcept(except string, groups ...string) string {
	if except != "" {
		except = ", except the names of keys in " + except
	}
	return "refused keys set more than once, under names that differ only in letter case: " +
		strings.Join(groups, "; ") + ". agent-director matches table and key names regardless of letter case" +
		except + ", so for each key it would read one of its values at random on each load. " + caseVariantAdvice
}

// TestLoadRefusesCaseVariantKeys is the b.p8n regression: a key set under names
// equal by strings.EqualFold (ſ, the Kelvin sign) is refused alone, naming them
// as written in file order, with one text on each of 100 loads (once random).
func TestLoadRefusesCaseVariantKeys(t *testing.T) {
	const dbPathTwice = "[Store]\ndb_path = \"/upper.db\"\n\n[store]\ndb_path = \"/lower.db\"\n"
	cases := []struct{ name, content, want string }{
		{"two_table_spellings", dbPathTwice, caseVariantText("[Store] db_path and [store] db_path")},
		{"two_key_spellings", "[store]\nDB_PATH = \"/upper.db\"\ndb_path = \"/lower.db\"\n",
			caseVariantText("[store] DB_PATH and [store] db_path")},
		{"long_s_table_quoted", "[\"ſtore\"]\ndb_path = \"/upper.db\"\n\n[store]\ndb_path = \"/lower.db\"\n",
			caseVariantText(`["ſtore"] db_path and [store] db_path`)},
		{"kelvin_sign_key_quoted", "[tmux]\n\"\u212aill_exit_wait_ms\" = 1000\nkill_exit_wait_ms = 2000\n",
			caseVariantText("[tmux] \"\u212aill_exit_wait_ms\" and [tmux] kill_exit_wait_ms")},
		{"dotted_key", "store.db_path = \"/upper.db\"\n\n[Store]\ndb_path = \"/lower.db\"\n",
			caseVariantText("[store] db_path and [Store] db_path")},
		{"inline_table", "store = { db_path = \"/upper.db\", DB_PATH = \"/lower.db\" }\n",
			caseVariantText("[store] db_path and [store] DB_PATH")},
		// validate would have checked whichever of the two values the decoder kept.
		{"refused_value_under_one_name", "[relay]\ntimeout_seconds = -1\n\n[Relay]\ntimeout_seconds = 60\n",
			caseVariantText("[relay] timeout_seconds and [Relay] timeout_seconds")},
		{"busy_timeout_refused_value_under_one_name", "[store]\nBUSY_TIMEOUT_MS = -1\nbusy_timeout_ms = 2000\n",
			caseVariantText("[store] BUSY_TIMEOUT_MS and [store] busy_timeout_ms")},
		{"beside_refused_value", "[pause]\ntimeout_seconds = -1\n\n" + dbPathTwice,
			caseVariantText("[Store] db_path and [store] db_path")},
		{"two_keys_interleaved", "[Store]\ndb_path = \"/upper.db\"\n\n[relay]\ntimeout_seconds = 60\n\n" +
			"[store]\ndb_path = \"/lower.db\"\n\n[RELAY]\ntimeout_seconds = 120\n\n[STORE]\ndb_path = \"/third.db\"\n",
			caseVariantText("[Store] db_path, [store] db_path and [STORE] db_path",
				"[relay] timeout_seconds and [RELAY] timeout_seconds")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := makeConfigFile(t, tc.content)
			for i := range 100 {
				if got := loadConfigError(t, path).Err.Error(); got != tc.want {
					t.Fatalf("load %d: description = %q\nwant                %q", i+1, got, tc.want)
				}
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
// refusals of a set value; 36, 15000, 100, 60 and 61 are the SR-4.1
// worked-example literals. A minimum above the 60 s default is what a missing
// key, or 0, gives, as the closing sentence says (raisedTail; b.9e1);
// TestTmuxGraceRuleAccepts loads those.
func TestTmuxGraceRuleRefusals(t *testing.T) {
	grace, create, pipe := config.TmuxPendingGraceSeconds, config.TmuxCreateTimeoutMs, config.TmuxPipeCloseWaitMs
	const huge = math.MaxInt64
	cases := []struct {
		name     string
		settings []tmuxSetting
		want     refusal
		notNamed config.TmuxKey // a key the description must not name as refused
	}{
		{"grace_30_create_15000",
			[]tmuxSetting{{create, 15000}, {grace, 30}},
			refusal{key: grace, value: 30, minimum: "36 s", from: [2]int64{15000, 100}}, create},
		{"create_39900_grace_59_minimum_equals_default",
			[]tmuxSetting{{create, 39900}, {grace, 59}},
			refusal{key: grace, value: 59, minimum: "60 s", from: [2]int64{39900, 100}}, create},
		{"create_40000_grace_explicit_60",
			[]tmuxSetting{{create, 40000}, {grace, 60}},
			refusal{key: grace, value: 60, minimum: "61 s", from: [2]int64{40000, 100}, raised: true}, create},
		{"create_40000_grace_below_floor",
			[]tmuxSetting{{create, 40000}, {grace, config.PendingGraceFloorSeconds - 1}},
			refusal{key: grace, value: config.PendingGraceFloorSeconds - 1, minimum: "61 s", from: [2]int64{40000, 100},
				raised: true}, create},
		{"create_40000_grace_negative",
			[]tmuxSetting{{create, 40000}, {grace, -1}},
			refusal{key: grace, value: -1, minimum: "61 s", from: [2]int64{40000, 100}, raised: true}, create},
		{"create_40000_pipe_2000_grace_61",
			[]tmuxSetting{{create, 40000}, {pipe, 2000}, {grace, 61}},
			refusal{key: grace, value: 61, minimum: "62 s", from: [2]int64{40000, 2000}, raised: true}, create},
		// create + pipe overflows an int64 of ms; the clause still states the exact minimum.
		{"create_and_pipe_max_int64_states_huge_minimum",
			[]tmuxSetting{{create, huge}, {pipe, huge}, {grace, 1e16}},
			refusal{key: grace, value: 1e16, minimum: "18446744073709572 s", from: [2]int64{huge, huge}, raised: true}, create},
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
// in SR-4.1 table order, when the file sets all nine in reverse order.
func TestTmuxRefusalNamesKeysInTableOrder(t *testing.T) {
	var reversed []tmuxSetting
	for i := len(tmuxKeyTable) - 1; i >= 0; i-- {
		reversed = append(reversed, tmuxSetting{tmuxKeyTable[i].key, -1})
	}
	desc := refusalDescription(t, tmuxConfigFile(t, reversed...))
	last := -1
	for _, tc := range tmuxKeyTable {
		refusedClause(t, desc, tc.key)
		at := strings.Index(desc, "[tmux] "+tc.key.Name())
		if at < last {
			t.Errorf("[tmux] %s is named out of table order: %q", tc.key.Name(), desc)
		}
		last = at
	}
}
