// Package clisetup opens the pkg/api Client for the command-line binaries
// agent-director and agent-director-admin (b.vqr), so that both open the same
// store with the same config, logger and schema checks, and parses and
// applies the global flags (--store-path, --home, --tmux-command) both take.
package clisetup

import (
	"errors"
	"io"
	"log"
	"os"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
)

// ConfigPath is the canonical TOML config location.
const ConfigPath = "~/.agent-director/config.toml"

// The err_names an OpenError carries.
const (
	errConfigMalformed         = "ErrConfigMalformed"
	errStoreOpen               = "ErrStoreOpen"
	errSchemaMismatch          = "ErrSchemaMismatch"
	errSchemaMigrationRequired = "ErrSchemaMigrationRequired"
)

// ErrConfigMalformed and ErrStoreOpen are the sentinels pkg/api/errnames.Catalog
// pairs with the err_names of the same names (b.vma); errors.Is matches an
// OpenError with the one its Name names (OpenError.Is). ErrSchemaMismatch and
// ErrSchemaMigrationRequired are not catalogued and have no sentinel here.
var (
	ErrConfigMalformed = errors.New(errConfigMalformed)
	ErrStoreOpen       = errors.New(errStoreOpen)
)

// Overrides are one run's overrides of the store path and the tmux command,
// from the global flags (GlobalFlags.Apply); an empty field is not set.
type Overrides struct {
	// StorePath replaces the configured store path; pkg/api.New tilde-expands it.
	StorePath string
	// TmuxCommand replaces the configured tmux command, used as given.
	TmuxCommand string
}

// OpenError is why Open could not open the Client. Name is the err_name of
// the error envelope the binary prints, and Err the cause, whose text is the
// envelope's err_description.
type OpenError struct {
	Name string
	Err  error
}

// Error returns the cause's text, which is the envelope's err_description.
func (e *OpenError) Error() string { return e.Err.Error() }

// Unwrap returns the cause.
func (e *OpenError) Unwrap() error { return e.Err }

// Is reports whether target is the sentinel of e's Name: ErrConfigMalformed
// or ErrStoreOpen. errors.Is goes on to the cause when it is not.
func (e *OpenError) Is(target error) bool {
	return (target == ErrConfigMalformed && e.Name == errConfigMalformed) ||
		(target == ErrStoreOpen && e.Name == errStoreOpen)
}

// Open constructs the pkg/api.Client every store-backed CLI verb uses, and
// returns the loaded config with it. On failure the error is an *OpenError:
// ErrConfigMalformed when the config cannot be loaded, ErrSchemaMismatch or
// ErrSchemaMigrationRequired when the store's schema refuses the open, and
// ErrStoreOpen otherwise.
//
// Design pins:
//   - Pin 1 (CreateIfMissing=true): the CLI is the one place that opts in to
//     first-run store creation; library callers get the strict default.
//   - Pin 2 (StorePath omitted by default): leaving StorePath="" lets the
//     three-tier precedence in pkg/api.New honor cfg.Store.DbPath, so users
//     who set a custom [store] db_path in their TOML get that path. When
//     o.StorePath is set (the global --store-path flag), tier 1 of the
//     precedence kicks in and overrides the config-file value.
//   - Pin 3 (Logger=NewRecoveryLogger): SRD §14.6 and §5 WARN messages must
//     reach cfg.Log.ErrorLogPath. Open loads the config once here to build
//     the logger BEFORE calling pkg/api.New, which also loads config
//     internally. The duplicate load is intentional — the alternative would
//     require a circular bootstrap.
//   - Pin H6 (no Client.Config() accessor): cfg is returned directly so the
//     serve verb can pass it to its MCP logger without a pkg/api accessor
//     that would leak internal/config.Config into the library's public
//     surface.
func Open(o Overrides) (*pkgapi.Client, config.Config, error) {
	cfg, err := config.Load(ConfigPath)
	if err != nil {
		return nil, config.Config{}, &OpenError{Name: errConfigMalformed, Err: err}
	}
	apiOpts := pkgapi.Options{
		ConfigPath:      ConfigPath,
		CreateIfMissing: true, // Pin 1
		// StorePath optionally set from o; otherwise Pin 2 applies.
		StorePath:   o.StorePath,
		TmuxCommand: o.TmuxCommand,
		Logger:      NewRecoveryLogger(cfg), // Pin 3
	}
	client, err := pkgapi.New(apiOpts)
	if err != nil {
		name := errStoreOpen
		switch {
		case errors.Is(err, store.ErrSchemaMismatch):
			name = errSchemaMismatch
		case errors.Is(err, store.ErrSchemaMigrationRequired):
			name = errSchemaMigrationRequired
		}
		return nil, config.Config{}, &OpenError{Name: name, Err: err}
	}
	return client, cfg, nil
}

// NewRecoveryLogger returns the *log.Logger Open injects into the
// pkg/api.Client (Pin 3). The Client's verb methods that still log (Spawn,
// Resume, FindMissing, Expire) surface WARN messages through it — SRD §14.6
// and §5. Kill writes no log lines: its errors are returned and its audit is
// the ad.kill.called trail event (SR-6.3). The destination is the configured
// error log path, falling back to stderr if the file can't be opened.
// Best-effort: the file is leaked for the lifetime of the CLI process; the OS
// reclaims it on exit.
func NewRecoveryLogger(cfg config.Config) *log.Logger {
	dest := io.Writer(os.Stderr)
	if cfg.Log.ErrorLogPath != "" {
		if f, err := os.OpenFile(cfg.Log.ErrorLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			dest = f
		}
	}
	return log.New(dest, "agent-director ", log.LstdFlags)
}
