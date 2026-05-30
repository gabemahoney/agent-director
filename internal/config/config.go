// Package config implements TOML-backed configuration loading for
// agent-director, per SRD §11.
//
// Load() returns Default() when the file is absent, and a typed
// *ConfigError wrapping the parse error when the file is malformed.
// Path fields support leading "~/" expansion and relative-path resolution
// against "~/.agent-director/". Shell variables like $HOME are preserved
// literally.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the top-level configuration tree. Field layout mirrors SRD §11.
type Config struct {
	Defaults Defaults `toml:"defaults"`
	Relay    Relay    `toml:"relay"`
	Pause    Pause    `toml:"pause"`
	Store    Store    `toml:"store"`
	Log      Log      `toml:"log"`
}

// Defaults holds per-invocation default behavior toggles.
type Defaults struct {
	RelayMode              string `toml:"relay_mode"`
	ExpireRetentionDays    int    `toml:"expire_retention_days"`
	DisableAskUserQuestion bool   `toml:"disable_askuserquestion"`
	// InjectHelpHook controls dynamic per-Spawn injection of a
	// SessionStart hook that runs `agent-director help`. Off by
	// default — operators opt in via install.sh (Q4=yes) so a Spawn's
	// inherited CLAUDE_CONFIG_DIR no longer has to carry the help hook
	// statically. See docs/settings.md and architecture.md "Spawn
	// launch" for the merge implications.
	InjectHelpHook bool `toml:"inject_help_hook,omitempty"`
}

// Relay holds polling and timeout knobs for the relay loop.
type Relay struct {
	PollBaseMs     int `toml:"poll_base_ms"`
	PollJitterMs   int `toml:"poll_jitter_ms"`
	TimeoutSeconds int `toml:"timeout_seconds"`
}

// Pause holds the pause-verb timeout.
type Pause struct {
	TimeoutSeconds int `toml:"timeout_seconds"`
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
			ExpireRetentionDays:    31,
			DisableAskUserQuestion: false,
			InjectHelpHook:         false,
		},
		Relay: Relay{
			PollBaseMs:     100,
			PollJitterMs:   100,
			TimeoutSeconds: 86400,
		},
		Pause: Pause{
			TimeoutSeconds: 30,
		},
		Store: Store{
			DbPath: "~/.agent-director/state.db",
		},
		Log: Log{
			ErrorLogPath: "~/.agent-director/errors.log",
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
		if _, err := toml.Decode(string(data), &cfg); err != nil {
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
