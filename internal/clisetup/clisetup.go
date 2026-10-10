// Package clisetup opens the pkg/api Client for the command-line binaries
// agent-director and agent-director-admin (b.vqr), so that both open the same
// store with the same config, logger and schema checks, and parses and
// applies the global flags (--store-path, --home, --tmux-command) both take.
// It also declares the sentinels pkg/api/errnames.Catalog pairs with the
// err_names the binaries give outside any verb handler.
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

// ErrConfigMalformed, ErrStoreOpen, ErrSchemaMismatch and
// ErrSchemaMigrationRequired are the sentinels pkg/api/errnames.Catalog pairs
// with the err_names of the same names (b.vma, b.cm7); errors.Is matches an
// OpenError with the one its Name names (OpenError.Is). The two schema
// sentinels are the store's own, store.ErrSchemaMismatch and
// store.ErrSchemaMigrationRequired (pkg/api re-exports them under the same
// names), so errnames.Classify gives a schema refusal the same name whether it
// comes as an OpenError or straight from pkg/api.New (b.x8s). The store
// returns them only when it opens, so no verb error wraps them.
var (
	ErrConfigMalformed         = errors.New(errConfigMalformed)
	ErrStoreOpen               = errors.New(errStoreOpen)
	ErrSchemaMismatch          = store.ErrSchemaMismatch
	ErrSchemaMigrationRequired = store.ErrSchemaMigrationRequired
)

// ErrUnknownVerb, ErrJSONMarshal and ErrTrailWrite are the sentinels
// pkg/api/errnames.Catalog pairs with err_names the command binaries write
// themselves, outside any verb handler (b.cm7): ErrUnknownVerb for a verb the
// binary does not know, ErrJSONMarshal when the binary cannot write a verb's
// JSON result, and ErrTrailWrite when agent-director trail-emit cannot write
// its trail event. No error wraps them: the binaries write the names directly.
var (
	ErrUnknownVerb = errors.New("ErrUnknownVerb")
	ErrJSONMarshal = errors.New("ErrJSONMarshal")
	ErrTrailWrite  = errors.New("ErrTrailWrite")
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

// Is reports whether target is the sentinel of e's Name: ErrConfigMalformed,
// ErrStoreOpen, ErrSchemaMismatch or ErrSchemaMigrationRequired. errors.Is
// goes on to the cause when it is not.
func (e *OpenError) Is(target error) bool {
	return (target == ErrConfigMalformed && e.Name == errConfigMalformed) ||
		(target == ErrStoreOpen && e.Name == errStoreOpen) ||
		(target == ErrSchemaMismatch && e.Name == errSchemaMismatch) ||
		(target == ErrSchemaMigrationRequired && e.Name == errSchemaMigrationRequired)
}

// APIOptions returns the pkg/api.Options every Client the command-line
// binaries open is built from: the canonical config path, first-run store
// creation (Pin 1 below) and o's store path and tmux command (Pin 2 below).
// Logger is left nil: Open sets the recovery logger (Pin 3), and
// agent-director serve keeps its MCP dispatcher's Client silent (Pin H4). One
// builder for both means the MCP tools use the same store and tmux as
// setupClient's Client, with or without overrides in o (b.wb7).
func APIOptions(o Overrides) pkgapi.Options {
	return pkgapi.Options{
		ConfigPath:      ConfigPath,
		CreateIfMissing: true, // Pin 1
		// StorePath optionally set from o; otherwise Pin 2 applies.
		StorePath:   o.StorePath,
		TmuxCommand: o.TmuxCommand,
	}
}

// Open constructs the pkg/api.Client every store-backed CLI verb uses, from
// APIOptions(o) with the recovery logger, and returns the loaded config with
// it. On failure the error is an *OpenError: ErrConfigMalformed when the
// config cannot be loaded, ErrSchemaMismatch or ErrSchemaMigrationRequired
// when the store's schema refuses the open, and ErrStoreOpen otherwise.
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
	apiOpts := APIOptions(o)
	apiOpts.Logger = NewRecoveryLogger(cfg) // Pin 3
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
