// Package config implements TOML-backed configuration loading for
// agent-director, per SRD §11.
//
// Load() returns Default() when the file is absent, and a typed
// *ConfigError wrapping the parse error when the file is malformed.
// Path fields support leading "~/" expansion and relative-path resolution
// against "~/.agent-director/". Shell variables like $HOME are preserved
// literally.
//
// The [tmux] table (type Tmux, tmux.go) holds the nine timing settings of
// agent-director's use of tmux, with their defaults, safe minimums and the
// pending grace period's minimum rule (SR-4.1). A missing key or 0 gives the
// default, or for pending_grace_seconds the larger of its default and its
// derived minimum (b.9e1); Load refuses a negative value and a positive value
// below a key's safe minimum exactly as it refuses a malformed file.
//
// [defaults] expire_retention_days, expire's default window in whole days,
// follows the same rule (b.sgw): a missing key or 0 gives
// DefaultExpireRetentionDays, and Load refuses a negative value and one above
// MaxExpireRetentionDays the same way. So do [relay] timeout_seconds and
// [pause] timeout_seconds (b.8q2), with DefaultRelayTimeoutSeconds and
// MaxRelayTimeoutSeconds, and DefaultPauseTimeoutSeconds and
// MaxPauseTimeoutSeconds; [pre_trust] lock_wait_seconds (b.kr4), with
// DefaultPreTrustLockWaitSeconds and MaxPreTrustLockWaitSeconds; and [store]
// busy_timeout_ms (b.c7f), with DefaultStoreBusyTimeoutMs and
// MaxStoreBusyTimeoutMs.
//
// Load also refuses a file that sets one key under names differing only in
// letter case ([Store] and [store] both setting db_path), whose value the
// decoder would otherwise pick at random (b.p8n); LoadTemplate refuses a
// spawn template of that shape the same way (b.2u1).
package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
)

// Config is the top-level configuration tree. Field layout mirrors SRD §11.
type Config struct {
	Defaults Defaults `toml:"defaults"`
	Relay    Relay    `toml:"relay"`
	Pause    Pause    `toml:"pause"`
	PreTrust PreTrust `toml:"pre_trust"`
	Store    Store    `toml:"store"`
	Log      Log      `toml:"log"`
	Tmux     Tmux     `toml:"tmux"`
}

// Defaults holds per-invocation default behavior toggles.
type Defaults struct {
	RelayMode string `toml:"relay_mode"`
	// ExpireRetentionDays is expire's default window, in whole days: the
	// file's value when the key is set (0 included), otherwise
	// DefaultExpireRetentionDays. Read it only through
	// EffectiveExpireRetentionDays, which gives the default for 0. Load
	// refuses a negative value and one above MaxExpireRetentionDays.
	ExpireRetentionDays    int  `toml:"expire_retention_days"`
	DisableAskUserQuestion bool `toml:"disable_askuserquestion"`
	// InjectHelpHook controls dynamic per-Spawn injection of a
	// SessionStart hook that runs `agent-director help`. Off by
	// default — operators opt in via install.sh (Q4=yes) so a Spawn's
	// inherited CLAUDE_CONFIG_DIR no longer has to carry the help hook
	// statically. See docs/settings.md and architecture.md "Spawn
	// launch" for the merge implications.
	InjectHelpHook bool `toml:"inject_help_hook,omitempty"`
}

// DefaultExpireRetentionDays is the default of expire_retention_days, in
// whole days (31). It is the value Default() seeds into
// Defaults.ExpireRetentionDays AND the fallback EffectiveExpireRetentionDays
// returns for a missing or 0 key, so the two never drift.
const DefaultExpireRetentionDays = 31

// MaxExpireRetentionDays is the largest whole number of days a time.Duration
// holds (106751): the upper limit of expire_retention_days, which Load
// refuses above it, and of older_than's day count, which pkg/api's
// ParseOlderThan refuses above it (b.sgw). A larger count would wrap expire's
// window, at worst to zero, which selects every finished row (pkg/api's
// Expire refuses a negative window, b.f4v).
const MaxExpireRetentionDays = int(math.MaxInt64 / int64(24*time.Hour))

// EffectiveExpireRetentionDays returns expire's default window in whole
// days (expire_retention_days): the configured value when positive,
// otherwise DefaultExpireRetentionDays (31). It never returns 0 or a
// negative count, so a default expire run never selects every finished row.
// It performs no maximum check; Load refuses a value above
// MaxExpireRetentionDays.
func (d Defaults) EffectiveExpireRetentionDays() int {
	if d.ExpireRetentionDays > 0 {
		return d.ExpireRetentionDays
	}
	return DefaultExpireRetentionDays
}

// refusals returns the description of each refused [defaults] value, in
// table order, or nil when every value loads. Only expire_retention_days is
// checked: a negative value and one above MaxExpireRetentionDays are refused,
// never replaced by the default or capped; 0 gives the default.
func (d Defaults) refusals() []string {
	if v := d.ExpireRetentionDays; v < 0 || v > MaxExpireRetentionDays {
		return []string{fmt.Sprintf("[defaults] expire_retention_days = %d, outside its range 1 to %d days",
			v, MaxExpireRetentionDays)}
	}
	return nil
}

// DefaultRelayTimeoutSeconds is the canonical relay window (24h). It is
// the value Default() seeds into Relay.TimeoutSeconds AND the fallback
// EffectiveTimeoutSeconds returns for a missing or 0 configured value, so
// the two never drift.
const DefaultRelayTimeoutSeconds = 86400

// MaxRelayTimeoutSeconds is the largest relay.timeout_seconds Load accepts
// (2147483): the largest per-hook `timeout` Claude Code honours. Claude Code
// arms a hook's timeout as a JavaScript setTimeout of timeout × 1000 ms, and
// the runtime replaces a delay above 2^31-1 ms (math.MaxInt32) with 1 ms, so a
// larger value would have Claude Code cancel the relay hooks about 1 ms after
// they start (b.8q2). The same value is the hook's poll deadline, decide's
// window and the send-keys guard window; at this size none of the window, the
// window plus or minus api.RelayKillSafetyMargin, or now plus the window can
// overflow a time.Duration or time.Time.
const MaxRelayTimeoutSeconds = math.MaxInt32 / 1000

// Relay holds polling and timeout knobs for the relay loop.
type Relay struct {
	PollBaseMs   int `toml:"poll_base_ms"`
	PollJitterMs int `toml:"poll_jitter_ms"`
	// TimeoutSeconds is the relay window, in whole seconds: the file's value
	// when the key is set (0 included), otherwise DefaultRelayTimeoutSeconds.
	// Read it only through EffectiveTimeoutSeconds, which gives the default
	// for 0. Load refuses a negative value and one above
	// MaxRelayTimeoutSeconds (b.8q2).
	TimeoutSeconds       int `toml:"timeout_seconds"`
	PermissionRequestCap int `toml:"permission_request_cap"`
}

// EffectiveTimeoutSeconds returns the relay window that the hook poll loop's
// deadline, decide's window, the send-keys guard window and the synthesized
// per-hook `timeout` must all use: the configured TimeoutSeconds when
// positive, and DefaultRelayTimeoutSeconds (86400) otherwise. It is the single
// source of truth for the window so those boundaries can never disagree
// (SR-1.3), and it never returns a zero or negative window (b.p48). It
// performs no maximum check; Load refuses a negative value and one above
// MaxRelayTimeoutSeconds (b.8q2), so a loaded config's window is always
// between 1 and MaxRelayTimeoutSeconds.
func (r Relay) EffectiveTimeoutSeconds() int {
	if r.TimeoutSeconds > 0 {
		return r.TimeoutSeconds
	}
	return DefaultRelayTimeoutSeconds
}

// refusals returns the description of each refused [relay] value, in table
// order, or nil when every value loads. Only timeout_seconds is checked: a
// negative value and one above MaxRelayTimeoutSeconds are refused, never
// replaced by the default or capped; 0 gives the default (b.8q2).
func (r Relay) refusals() []string {
	if v := r.TimeoutSeconds; v < 0 || v > MaxRelayTimeoutSeconds {
		return []string{fmt.Sprintf("[relay] timeout_seconds = %d, outside its range 1 to %d seconds",
			v, MaxRelayTimeoutSeconds)}
	}
	return nil
}

// DefaultPauseTimeoutSeconds is the default pause wait, in whole seconds
// (30). It is the value Default() seeds into Pause.TimeoutSeconds AND the
// fallback Pause.EffectiveTimeoutSeconds returns for a missing or 0
// configured value, so the two never drift.
const DefaultPauseTimeoutSeconds = 30

// MaxPauseTimeoutSeconds is the largest whole number of seconds a
// time.Duration holds (9223372036): the upper limit of pause.timeout_seconds,
// which Load refuses above it (b.8q2). A larger count would wrap pause's
// wait, at worst to zero or below, which reports ErrPauseTimeout at once. It
// is an int64, not an int: the value does not fit a 32-bit int, and the
// package must build where int is 32 bits (GOARCH=386), where no int is above
// it.
const MaxPauseTimeoutSeconds = math.MaxInt64 / int64(time.Second)

// Pause holds the pause-verb timeout.
type Pause struct {
	// TimeoutSeconds is pause's wait for the row to reach ended, in whole
	// seconds: the file's value when the key is set (0 included), otherwise
	// DefaultPauseTimeoutSeconds. Read it only through
	// EffectiveTimeoutSeconds, which gives the default for 0. Load refuses a
	// negative value and one above MaxPauseTimeoutSeconds (b.8q2).
	TimeoutSeconds int `toml:"timeout_seconds"`
}

// EffectiveTimeoutSeconds returns pause's wait in whole seconds
// (pause.timeout_seconds): the configured value when positive, otherwise
// DefaultPauseTimeoutSeconds (30). It never returns 0 or a negative count, so
// a configured pause never times out before its first wait. It performs no
// maximum check; Load refuses a value above MaxPauseTimeoutSeconds.
func (p Pause) EffectiveTimeoutSeconds() int {
	if p.TimeoutSeconds > 0 {
		return p.TimeoutSeconds
	}
	return DefaultPauseTimeoutSeconds
}

// refusals returns the description of each refused [pause] value, in table
// order, or nil when every value loads. Only timeout_seconds is checked: a
// negative value and one above MaxPauseTimeoutSeconds are refused, never
// replaced by the default or capped; 0 gives the default (b.8q2).
func (p Pause) refusals() []string {
	if v := p.TimeoutSeconds; v < 0 || int64(v) > MaxPauseTimeoutSeconds {
		return []string{fmt.Sprintf("[pause] timeout_seconds = %d, outside its range 1 to %d seconds",
			v, MaxPauseTimeoutSeconds)}
	}
	return nil
}

// DefaultPreTrustLockWaitSeconds is the default of pre_trust.lock_wait_seconds,
// in whole seconds (12): just above the 10 s stale limit of Claude Code's lock
// on .claude.json, so a lock dir left by a holder killed while holding it goes
// stale, and pre-trust breaks it and takes the lock, within the wait (b.kr4).
// It is the value Default() seeds into PreTrust.LockWaitSeconds AND the
// fallback EffectiveLockWaitSeconds returns for a missing or 0 key, so the two
// never drift.
const DefaultPreTrustLockWaitSeconds = 12

// MaxPreTrustLockWaitSeconds is the largest whole number of seconds a
// time.Duration holds (9223372036): the upper limit of
// pre_trust.lock_wait_seconds, which Load refuses above it (b.kr4). A larger
// count would wrap pre-trust's wait, at worst to zero or below, which gives up
// on a held lock at the first attempt. It is an int64 for the reason
// MaxPauseTimeoutSeconds is: the value does not fit a 32-bit int.
const MaxPreTrustLockWaitSeconds = math.MaxInt64 / int64(time.Second)

// PreTrust holds the settings of the folder-trust pre-trust every launch runs
// (spawn.PreTrust).
type PreTrust struct {
	// LockWaitSeconds is pre-trust's total wait for Claude Code's lock on
	// .claude.json while another process holds it and it is not stale, in
	// whole seconds: the file's value when the key is set (0 included),
	// otherwise DefaultPreTrustLockWaitSeconds. Read it only through
	// EffectiveLockWaitSeconds, which gives the default for 0. Load refuses a
	// negative value and one above MaxPreTrustLockWaitSeconds (b.kr4).
	LockWaitSeconds int `toml:"lock_wait_seconds"`
}

// EffectiveLockWaitSeconds returns pre-trust's wait for a held lock in whole
// seconds (pre_trust.lock_wait_seconds): the configured value when positive,
// otherwise DefaultPreTrustLockWaitSeconds (12). It never returns 0 or a
// negative count, so pre-trust never gives up on a held lock without waiting.
// It performs no maximum check; Load refuses a value above
// MaxPreTrustLockWaitSeconds.
func (p PreTrust) EffectiveLockWaitSeconds() int {
	if p.LockWaitSeconds > 0 {
		return p.LockWaitSeconds
	}
	return DefaultPreTrustLockWaitSeconds
}

// refusals returns the description of each refused [pre_trust] value, in
// table order, or nil when every value loads. Only lock_wait_seconds is
// checked: a negative value and one above MaxPreTrustLockWaitSeconds are
// refused, never replaced by the default or capped; 0 gives the default
// (b.kr4).
func (p PreTrust) refusals() []string {
	if v := p.LockWaitSeconds; v < 0 || int64(v) > MaxPreTrustLockWaitSeconds {
		return []string{fmt.Sprintf("[pre_trust] lock_wait_seconds = %d, outside its range 1 to %d seconds",
			v, MaxPreTrustLockWaitSeconds)}
	}
	return nil
}

// Store holds the store database's settings.
type Store struct {
	// DbPath is the store database, as Load resolved it: the file's value
	// when the key is set ("" for an empty db_path), otherwise
	// DefaultDbPath. Read it only through EffectiveDbPath, which gives the
	// default for "".
	DbPath string `toml:"db_path"`
	// BusyTimeoutMs is how long a store connection waits for a lock another
	// connection holds before its statement fails (SQLite's busy timeout),
	// in whole milliseconds: the file's value when the key is set (0
	// included), otherwise DefaultStoreBusyTimeoutMs. Read it only through
	// EffectiveBusyTimeoutMs, which gives the default for 0. Load refuses a
	// negative value and one above MaxStoreBusyTimeoutMs (b.c7f).
	//
	// Raising it couples with fixed limits nothing adjusts to it. Each store
	// write can wait up to this long for the lock on a contended store, and
	// a SessionStart hook's writes outside its bounded wait (up to four) must
	// fit in the 60 s between internal/hook's sessionStartWaitCap (540 s) and
	// Claude Code's 600 s hook timeout: that holds at the default (at most
	// 40 s) but not from about 15 s, when Claude Code can kill the hook. And
	// the TS client ends a call after its callTimeoutMs (30 s by default), so
	// a verb whose statements wait that long in total, one at 30 s or more
	// or several shorter ones, is cut off before SQLite gives up. Nothing
	// caps this key against either limit.
	BusyTimeoutMs int `toml:"busy_timeout_ms"`
}

// DefaultStoreBusyTimeoutMs is the default of [store] busy_timeout_ms, in
// whole milliseconds (10000). It is the value Default() seeds into
// Store.BusyTimeoutMs AND the fallback EffectiveBusyTimeoutMs returns for a
// missing or 0 key, so the two never drift.
const DefaultStoreBusyTimeoutMs = 10000

// MaxStoreBusyTimeoutMs is the largest [store] busy_timeout_ms Load accepts
// (2147483647, math.MaxInt32): the largest busy timeout SQLite holds. SQLite
// reads PRAGMA busy_timeout's value as a 32-bit int and takes a larger one as
// 0, and the sqlite3 shell's .timeout truncates one to 32 bits, so a larger
// value would turn the wait off and a locked store would fail at once
// (b.c7f).
const MaxStoreBusyTimeoutMs = math.MaxInt32

// EffectiveBusyTimeoutMs returns the busy timeout every store connection
// uses, in whole milliseconds (store.busy_timeout_ms): the configured value
// when positive, otherwise DefaultStoreBusyTimeoutMs (10000). It never
// returns 0 or a negative count, which would turn SQLite's wait for a lock
// off. It performs no maximum check; Load refuses a value above
// MaxStoreBusyTimeoutMs.
func (s Store) EffectiveBusyTimeoutMs() int {
	if s.BusyTimeoutMs > 0 {
		return s.BusyTimeoutMs
	}
	return DefaultStoreBusyTimeoutMs
}

// refusals returns the description of each refused [store] value, in table
// order, or nil when every value loads. Only busy_timeout_ms is checked: a
// negative value and one above MaxStoreBusyTimeoutMs are refused, never
// replaced by the default or capped; 0 gives the default (b.c7f).
func (s Store) refusals() []string {
	if v := s.BusyTimeoutMs; v < 0 || v > MaxStoreBusyTimeoutMs {
		return []string{fmt.Sprintf("[store] busy_timeout_ms = %d, outside its range 1 to %d milliseconds",
			v, MaxStoreBusyTimeoutMs)}
	}
	return nil
}

// DefaultDbPath is the default of [store] db_path. It is the value Default()
// seeds into Store.DbPath AND the fallback EffectiveDbPath returns for an
// empty db_path, so the two never drift.
const DefaultDbPath = "~/.agent-director/state.db"

// EffectiveDbPath returns the store database every opener uses: the verbs
// (pkg/api.New, tiers 2 and 3 of its StorePath precedence) and the hook
// handler, so the two always open the same store (b.8up). It is DbPath when
// non-empty, otherwise DefaultDbPath with "~/" joined onto $HOME. It never
// returns "", which the SQLite driver would take as a file name in the cwd.
// When os.UserHomeDir fails (on Unix, HOME unset or empty) the default is
// refused with an error, never resolved against another home.
func (s Store) EffectiveDbPath() (string, error) {
	if s.DbPath != "" {
		return s.DbPath, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand tilde: %w", err)
	}
	return expandTilde(DefaultDbPath, home), nil
}

// Log holds logging paths.
type Log struct {
	ErrorLogPath string `toml:"error_log_path"`
}

// Default returns the canonical SRD §11 defaults. Its Tmux.PendingGraceSeconds
// is pre-filled with DefaultPendingGraceSeconds (60), and nothing but Load
// checks it against the derived minimum, so a Go caller that raises
// Tmux.CreateTimeoutMs or Tmux.PipeCloseWaitMs on Default() should set
// PendingGraceSeconds to 0 to get the larger of 60 and the derived minimum
// (b.9e1).
func Default() Config {
	return Config{
		Defaults: Defaults{
			RelayMode:              "off",
			ExpireRetentionDays:    DefaultExpireRetentionDays,
			DisableAskUserQuestion: false,
			InjectHelpHook:         false,
		},
		Relay: Relay{
			PollBaseMs:           100,
			PollJitterMs:         100,
			TimeoutSeconds:       DefaultRelayTimeoutSeconds,
			PermissionRequestCap: 1000,
		},
		Pause: Pause{
			TimeoutSeconds: DefaultPauseTimeoutSeconds,
		},
		PreTrust: PreTrust{
			LockWaitSeconds: DefaultPreTrustLockWaitSeconds,
		},
		Store: Store{
			DbPath:        DefaultDbPath,
			BusyTimeoutMs: DefaultStoreBusyTimeoutMs,
		},
		Log: Log{
			ErrorLogPath: "~/.agent-director/errors.log",
		},
		Tmux: Tmux{
			StartingSessionSeconds: DefaultStartingSessionSeconds,
			StoppingWindowSeconds:  DefaultStoppingWindowSeconds,
			PendingGraceSeconds:    DefaultPendingGraceSeconds,
			QueryTimeoutMs:         DefaultQueryTimeoutMs,
			ActionTimeoutMs:        DefaultActionTimeoutMs,
			CreateTimeoutMs:        DefaultCreateTimeoutMs,
			PipeCloseWaitMs:        DefaultPipeCloseWaitMs,
			SweepBudgetSeconds:     DefaultSweepBudgetSeconds,
			KillExitWaitMs:         DefaultKillExitWaitMs,
		},
	}
}

// ConfigError wraps a config file read or parse failure with the path that
// produced it. Callers can recover both via errors.As.
type ConfigError struct {
	Path string
	Err  error
}

// Error implements the error interface.
func (e *ConfigError) Error() string {
	return fmt.Sprintf("config %s: %v", e.Path, e.Err)
}

// Unwrap exposes the underlying error for errors.Is / errors.As.
func (e *ConfigError) Unwrap() error {
	return e.Err
}

// Load reads a TOML config file from path and returns a Config with any
// values from the file applied over Default(). A missing file returns
// Default() with a nil error. Any other read or parse failure returns
// Default() wrapped in *ConfigError.
//
// After a successful parse, Load refuses a file that sets one key under two
// or more names differing only in letter case, such as db_path under both
// [Store] and [store] (caseVariantRefusal, b.p8n): the decoder matches names
// regardless of letter case, and which of the values it keeps changes from
// one load to the next. A single spelling ([Store] alone), and two spellings
// of a table setting different keys, load as before. This refusal comes
// first, alone, so its text never depends on which value the decoder kept.
//
// Load then validates the [tmux] table (SR-4.1): a negative value of any key,
// and a positive value below its key's safe minimum, are refused, never
// raised to the minimum or replaced by the default. A missing key, or 0, is
// never refused: it takes its default or its safe minimum, whichever is
// larger, which only pending_grace_seconds' derived minimum can be (b.9e1);
// Load writes that value into the field of each key the file does not set
// (resolveUnsetTmux), and the accessors give it for 0. A key is checked
// whatever the letter case of its name and its table's ([Tmux]
// STOPPING_WINDOW_SECONDS, b.g7h), as the decoder reads it in any. It validates
// [defaults] expire_retention_days the same way (b.sgw), [relay]
// timeout_seconds and [pause] timeout_seconds too (b.8q2), [pre_trust]
// lock_wait_seconds (b.kr4) and [store] busy_timeout_ms (b.c7f): a negative
// value and one above the key's maximum (MaxExpireRetentionDays,
// MaxRelayTimeoutSeconds, MaxPauseTimeoutSeconds, MaxPreTrustLockWaitSeconds,
// MaxStoreBusyTimeoutMs) are refused, never replaced by the default or
// capped. A refusal behaves exactly like a malformed file: Load returns a
// *ConfigError for the file whose Err describes every refused key
// (validate). A value that is not a TOML integer already fails the parse.
// The Config returned alongside any *ConfigError exists only to mirror the
// parse-failure contract pinned by TestLoadMalformedReturnsTypedError; no
// caller may run with it.
//
// Path fields (Store.DbPath, Log.ErrorLogPath) are post-processed:
//   - A leading "~/" is expanded to the current user's home directory.
//   - A relative path (neither absolute nor "~/"-prefixed) is resolved
//     against "~/.agent-director/", since the hook handler runs in
//     Claude's CWD rather than the director's own directory.
//   - "$VAR" patterns are left literal — no shell expansion.
func Load(path string) (Config, error) {
	home, _ := os.UserHomeDir()
	path = expandTilde(path, home)
	cfg := Default()

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		meta, err := toml.Decode(string(data), &cfg)
		if err != nil {
			return resolvePaths(Default(), home), &ConfigError{Path: path, Err: err}
		}
		// Before validate, which would check whichever value of a key set
		// twice the decoder happened to keep (b.p8n).
		if err := caseVariantRefusal(meta); err != nil {
			return resolvePaths(Default(), home), &ConfigError{Path: path, Err: err}
		}
		if err := validate(cfg, meta); err != nil {
			return resolvePaths(Default(), home), &ConfigError{Path: path, Err: err}
		}
		cfg.Tmux = resolveUnsetTmux(cfg.Tmux, meta)
	case errors.Is(err, os.ErrNotExist):
		// fall through with cfg = Default(); path resolution still applies so
		// callers always get fully-resolved paths regardless of file presence.
	default:
		return resolvePaths(Default(), home), &ConfigError{Path: path, Err: err}
	}

	return resolvePaths(cfg, home), nil
}

// caseVariantRefusal refuses a file, whose metadata is meta, that sets one key
// under two or more names differing only in letter case: db_path under both
// [Store] and [store] in config.toml (Load, b.p8n), say, DB_PATH and db_path
// under [store], or RELAY_MODE and relay_mode in a template (LoadTemplate,
// b.2u1). TOML reads them as different keys, but the decoder matches each name
// to a field regardless of letter case (strings.EqualFold), so all of them land
// on one field, in Go's random map order. Only keys holding a value that the
// decoder read count: tables do not, so [Store] and [store] setting different
// keys load, and neither do keys agent-director does not read.
//
// maps names the top-level tables, if any, that decode into a Go map, such as
// a template's [extra_env] and [labels]. The decoder matches such a table's
// own name regardless of letter case but keeps the names of its keys as
// written, so FOO and foo under [extra_env] are two keys, both loaded, and are
// not refused; FOO under both [extra_env] and [EXTRA_ENV] is (decoderKey).
//
// It returns nil when no key is set twice, otherwise an error listing each
// such key's names (keyName, in file order, by nameList), keys in the order of
// their first name, then the change that loads. MetaData records no line
// numbers, so the names stand in for them.
func caseVariantRefusal(meta toml.MetaData, maps ...string) error {
	undecoded := make(map[string]bool)
	for _, k := range meta.Undecoded() {
		undecoded[k.String()] = true
	}
	var order []string                 // each key, by decoderKey, in the order of its first name
	names := make(map[string][]string) // each such key's names, in file order
	for _, k := range meta.Keys() {
		// MetaData.Type names a table, inline or not, "Hash", and an array
		// of tables "ArrayHash".
		if t := meta.Type(k...); undecoded[k.String()] || t == "Hash" || t == "ArrayHash" {
			continue
		}
		f := decoderKey(k, maps)
		if names[f] == nil {
			order = append(order, f)
		}
		names[f] = append(names[f], keyName(k))
	}
	var refused []string
	for _, f := range order {
		if len(names[f]) > 1 {
			refused = append(refused, nameList(names[f]))
		}
	}
	if len(refused) == 0 {
		return nil
	}
	matched := "table and key names regardless of letter case"
	if len(maps) > 0 {
		tables := make([]string, len(maps))
		for i, m := range maps {
			tables[i] = "[" + m + "]"
		}
		matched += ", except the names of keys in " + nameList(tables)
	}
	return errors.New("refused keys set more than once, under names that differ only in letter case: " +
		strings.Join(refused, "; ") + ". agent-director matches " + matched + "," +
		" so for each key it would read one of its values at random on each load." +
		" Set each key once, removing all but one of the names listed for it.")
}

// foldCase maps each letter of name to the smallest letter of its Unicode
// case-folding orbit, so two names are equal after foldCase exactly when
// strings.EqualFold, which the decoder matches names with, holds them equal:
// s, S and ſ (U+017F) all give S.
func foldCase(name string) string {
	return strings.Map(func(r rune) rune {
		least := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			least = min(least, f)
		}
		return least
	}, name)
}

// foldKey returns key k, each of its names folded by foldCase, as a string:
// two keys give the same string exactly when they have as many names and each
// pair of names is equal under strings.EqualFold, as the decoder matches them.
func foldKey(k toml.Key) string {
	folded := make(toml.Key, len(k))
	for i, part := range k {
		folded[i] = foldCase(part)
	}
	return folded.String()
}

// decoderKey returns key k as caseVariantRefusal compares it: foldKey's form,
// except that the names below a top-level table named in maps, the keys of a
// Go map, stay as written, as the decoder keeps them. Two keys give the same
// string exactly when the decoder reads them into one field or map entry.
func decoderKey(k toml.Key, maps []string) string {
	if len(k) > 1 && slices.ContainsFunc(maps, func(m string) bool { return strings.EqualFold(m, k[0]) }) {
		return append(toml.Key{foldCase(k[0])}, k[1:]...).String()
	}
	return foldKey(k)
}

// isDefined reports whether the file whose metadata is meta sets key, given as
// for meta.IsDefined (the table's name, then the key's own), matching each
// name regardless of letter case as the decoder does (foldKey): [Tmux]
// STOPPING_WINDOW_SECONDS sets [tmux] stopping_window_seconds. meta.IsDefined
// compares names exactly, so it misses a key the decoder still read into its
// field (b.g7h); every "is this key set" check in this package asks isDefined
// instead.
func isDefined(meta toml.MetaData, key ...string) bool {
	want := foldKey(key)
	for _, k := range meta.Keys() {
		if foldKey(k) == want {
			return true
		}
	}
	return false
}

// keyName names key k as caseVariantRefusal lists it, as written in the file:
// its table in brackets, then its own name ("[Store] db_path"), each part
// quoted when TOML needs it ("[\"ſtore\"] db_path").
func keyName(k toml.Key) string {
	name := toml.Key{k[len(k)-1]}.String()
	if len(k) == 1 {
		return name
	}
	return "[" + k[:len(k)-1].String() + "] " + name
}

// validate applies Load's refusal rules to cfg, decoded from a file whose
// metadata meta says which keys it sets. It returns nil when every value
// loads, otherwise an error whose text names the tables with a refused value
// as a list (nameList: "refused [tmux] values: ", "refused [defaults] and
// [tmux] values: ", "refused [defaults], [relay] and [tmux] values: "), then
// every refused key's description, tables in the order [defaults], [relay],
// [pause], [pre_trust], [store], [tmux] and each table in its own order, then
// missingKeyAdvice. A file refused only for [tmux] values gets the SR-4.1
// description unchanged. Values are never changed.
//
// A missing key, or 0, is never refused (b.9e1): for [tmux]
// pending_grace_seconds it takes the larger of its default and its derived
// minimum (Tmux.unsetValue), so a raised create_timeout_ms alone never
// refuses a key the operator did not set. No refused key's description
// contains "; ", the separator between them.
func validate(cfg Config, meta toml.MetaData) error {
	tmuxRefused, raised := tmuxRefusals(cfg.Tmux, meta)
	var tables, refused []string
	for _, t := range []struct {
		name     string
		refusals []string
	}{
		{"[defaults]", cfg.Defaults.refusals()},
		{"[relay]", cfg.Relay.refusals()},
		{"[pause]", cfg.Pause.refusals()},
		{"[pre_trust]", cfg.PreTrust.refusals()},
		{"[store]", cfg.Store.refusals()},
		{"[tmux]", tmuxRefused},
	} {
		if len(t.refusals) > 0 {
			tables, refused = append(tables, t.name), append(refused, t.refusals...)
		}
	}
	if len(refused) == 0 {
		return nil
	}
	return errors.New("refused " + nameList(tables) + " values: " + strings.Join(refused, "; ") + "." +
		missingKeyAdvice(raised))
}

// missingKeyAdvice is the refusal's closing sentence, with its leading space,
// true for every refused key: a missing key, or 0, always loads (b.9e1).
// raised names the refused keys for which it gives their safe minimum, as it
// is above their default (tmuxRefusals); only [tmux] pending_grace_seconds'
// derived minimum can be. With none, the sentence is the plain "A missing
// key, or 0, gives the default."; otherwise it adds that those keys take
// their safe minimum when that is larger.
func missingKeyAdvice(raised []string) string {
	if len(raised) == 0 {
		return " A missing key, or 0, gives the default."
	}
	return " A missing key, or 0, gives the default, or for " + nameList(raised) +
		" its safe minimum when that is larger."
}

// nameList joins names as a refusal lists them, the refused tables in
// validate and a key's names in caseVariantRefusal: one name alone, two
// joined by " and ", more separated by ", " with " and " before the last
// ("[defaults], [relay] and [tmux]").
func nameList(names []string) string {
	if len(names) <= 2 {
		return strings.Join(names, " and ")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// resolvePaths applies the SRD §11 path rules to every filesystem-bearing
// field in cfg. Called unconditionally so Default()'s "~/" placeholders are
// always expanded before reaching callers.
func resolvePaths(cfg Config, home string) Config {
	base := filepath.Join(home, ".agent-director")
	cfg.Store.DbPath = resolvePathField(cfg.Store.DbPath, home, base)
	cfg.Log.ErrorLogPath = resolvePathField(cfg.Log.ErrorLogPath, home, base)
	return cfg
}

// expandTilde replaces a leading "~/" with the given home directory. If
// home is empty (UserHomeDir failed) or the path does not start with "~/",
// it is returned unchanged.
func expandTilde(p, home string) string {
	if home == "" || !strings.HasPrefix(p, "~/") {
		return p
	}
	return filepath.Join(home, p[2:])
}

// resolvePathField applies the SRD §11 path rules to a single config field:
// tilde-expand, leave absolute paths alone, and join relative paths against
// base. "$VAR" sequences are preserved as literal text.
func resolvePathField(p, home, base string) string {
	if p == "" {
		return p
	}
	if strings.HasPrefix(p, "~/") {
		return expandTilde(p, home)
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}
