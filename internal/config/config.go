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
// default; Load refuses a negative value and a positive value below a key's
// safe minimum exactly as it refuses a malformed file.
//
// [defaults] expire_retention_days, expire's default window in whole days,
// follows the same rule (b.sgw): a missing key or 0 gives
// DefaultExpireRetentionDays, and Load refuses a negative value and one above
// MaxExpireRetentionDays the same way. So do [relay] timeout_seconds and
// [pause] timeout_seconds (b.8q2), with DefaultRelayTimeoutSeconds and
// MaxRelayTimeoutSeconds, and DefaultPauseTimeoutSeconds and
// MaxPauseTimeoutSeconds; and [pre_trust] lock_wait_seconds (b.kr4), with
// DefaultPreTrustLockWaitSeconds and MaxPreTrustLockWaitSeconds.
package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

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
// window, at worst to zero or below, which selects every finished row.
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

// Store holds storage backend paths.
type Store struct {
	DbPath string `toml:"db_path"`
}

// Log holds logging paths.
type Log struct {
	ErrorLogPath string `toml:"error_log_path"`
}

// Default returns the canonical SRD §11 defaults.
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
			DbPath: "~/.agent-director/state.db",
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
// After a successful parse, Load validates the [tmux] table (SR-4.1): a
// negative value of any key, and a positive value below its key's safe
// minimum (for the pending grace period, the default too when its key is
// missing or 0 and the default is below the derived minimum), are refused,
// never raised to the minimum or replaced by the default. It validates
// [defaults] expire_retention_days the same way (b.sgw), [relay]
// timeout_seconds and [pause] timeout_seconds too (b.8q2), and [pre_trust]
// lock_wait_seconds (b.kr4): a negative value and one above the key's maximum
// (MaxExpireRetentionDays, MaxRelayTimeoutSeconds, MaxPauseTimeoutSeconds,
// MaxPreTrustLockWaitSeconds) are refused, never replaced by the default or
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
		if err := validate(cfg, meta); err != nil {
			return resolvePaths(Default(), home), &ConfigError{Path: path, Err: err}
		}
	case errors.Is(err, os.ErrNotExist):
		// fall through with cfg = Default(); path resolution still applies so
		// callers always get fully-resolved paths regardless of file presence.
	default:
		return resolvePaths(Default(), home), &ConfigError{Path: path, Err: err}
	}

	return resolvePaths(cfg, home), nil
}

// validate applies Load's refusal rules to cfg, decoded from a file whose
// metadata meta says which keys it sets. It returns nil when every value
// loads, otherwise an error whose text names the tables with a refused value
// as a list (tableList: "refused [tmux] values: ", "refused [defaults] and
// [tmux] values: ", "refused [defaults], [relay] and [tmux] values: "), then
// every refused key's description, tables in the order [defaults], [relay],
// [pause], [pre_trust], [tmux] and each table in its own order, then
// missingKeyAdvice. A file refused only for [tmux] values gets the SR-4.1
// description unchanged. Values are never changed.
func validate(cfg Config, meta toml.MetaData) error {
	var tables, refused, defaultRefused []string
	for _, t := range []struct {
		name     string
		refusals []string
	}{
		{"[defaults]", cfg.Defaults.refusals()},
		{"[relay]", cfg.Relay.refusals()},
		{"[pause]", cfg.Pause.refusals()},
		{"[pre_trust]", cfg.PreTrust.refusals()},
	} {
		if len(t.refusals) > 0 {
			tables, refused = append(tables, t.name), append(refused, t.refusals...)
		}
	}
	if r := tmuxRefusals(cfg.Tmux, meta); len(r) > 0 {
		tables = append(tables, "[tmux]")
		for _, x := range r {
			refused = append(refused, x.text)
			if x.defaultRefused {
				defaultRefused = append(defaultRefused, "[tmux] "+x.key.Name())
			}
		}
	}
	if len(refused) == 0 {
		return nil
	}
	return errors.New("refused " + tableList(tables) + " values: " + strings.Join(refused, "; ") + "." +
		missingKeyAdvice(len(refused), defaultRefused))
}

// tableList joins the refused tables' names as a list: one name alone, two
// joined by " and ", more separated by ", " with " and " before the last
// ("[defaults], [relay] and [tmux]").
func tableList(tables []string) string {
	if len(tables) <= 2 {
		return strings.Join(tables, " and ")
	}
	return strings.Join(tables[:len(tables)-1], ", ") + " and " + tables[len(tables)-1]
}

// missingKeyAdvice is the refusal's closing sentence, with its leading space.
// refused counts the refused keys; defaultRefused names those among them
// whose default is below their safe minimum (b.n4q). A missing key, or 0,
// gives the default, which loads for every refused key but those, whose own
// descriptions state a change that loads. So the sentence is the plain "A
// missing key, or 0, gives the default." when defaultRefused is empty, names
// the keys it leaves out when only some refused keys are in it, and is empty
// when every refused key is.
func missingKeyAdvice(refused int, defaultRefused []string) string {
	switch len(defaultRefused) {
	case 0:
		return " A missing key, or 0, gives the default."
	case refused:
		return ""
	default:
		return " For every refused key other than " + strings.Join(defaultRefused, " and ") +
			", a missing key, or 0, gives the default."
	}
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
