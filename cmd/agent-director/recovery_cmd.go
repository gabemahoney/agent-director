package main

import (
	"context"
	"flag"
	"io"
	"log"
	"os"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
)

// findMissingHandlerWith implements `agent-director find-missing`.
// The verb takes no flags. The prober is selected by build tags
// (probe.New). Warnings (e.g. degraded-mode guard) route through the
// configured error log — the Client was constructed with a recovery
// logger (setupClient Pin 3) so cron operators see them in their usual
// monitoring stream.
func findMissingHandlerWith(client *pkgapi.Client, args []string) error {
	fs := flag.NewFlagSet("find-missing", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}

	result, err := client.FindMissing(context.Background())
	if err != nil {
		name, desc := classifyError(err)
		return writeApiErrorAndDispatch(name, errMessageStartsWithName(name, desc))
	}
	if result.IDs == nil {
		result.IDs = []string{}
	}
	return writeJSON(os.Stdout, result)
}

// expireHandlerWith implements `agent-director expire`. --older-than
// accepts the same form Go's time.ParseDuration handles, plus a `d`
// suffix for days (Go's parser does not). Absent flag → cfg default.
func expireHandlerWith(client *pkgapi.Client, args []string) error {
	var olderThanStr string
	fs := flag.NewFlagSet("expire", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&olderThanStr, "older-than", "", "duration override (e.g. 7d, 12h, 0d). Default: cfg.defaults.expire_retention_days.")
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}

	var older *time.Duration
	if olderThanStr != "" {
		d, err := parseDaysOrDuration(olderThanStr)
		if err != nil {
			return writeApiErrorAndDispatch("ErrInvalidFlags", "--older-than: "+err.Error())
		}
		older = &d
	}

	result, err := client.Expire(older)
	if err != nil {
		name, desc := classifyError(err)
		return writeApiErrorAndDispatch(name, errMessageStartsWithName(name, desc))
	}
	if result.IDs == nil {
		result.IDs = []string{}
	}
	return writeJSON(os.Stdout, result)
}

// deleteHandlerWith implements `agent-director delete`. --claude-instance-id
// is repeatable; at least one is required. The per-row result map is
// the entire JSON envelope.
func deleteHandlerWith(client *pkgapi.Client, args []string) error {
	var ids []string
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Var(newStringSlice(&ids), "claude-instance-id", "id to delete (repeatable; ≥1 required)")
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if len(ids) == 0 {
		return writeApiErrorAndDispatch("ErrInvalidFlags", "--claude-instance-id is required (≥1)")
	}
	result, err := client.Delete(ids)
	if err != nil {
		name, desc := classifyError(err)
		return writeApiErrorAndDispatch(name, errMessageStartsWithName(name, desc))
	}
	return writeJSON(os.Stdout, result)
}

// parseDaysOrDuration accepts either Go's standard time.ParseDuration
// format or a trailing `d` for days. SRD §11 uses days for retention
// because the user-facing config is days; this keeps `--older-than`
// consistent with that.
//
// Negative durations are rejected: the store treats `older <= 0` as
// "delete every terminal row", so silently accepting `--older-than -2h`
// would reap the entire terminal history. A caller that really wants
// the all-rows behavior should pass `0d` explicitly.
func parseDaysOrDuration(s string) (time.Duration, error) {
	if n := len(s); n > 1 && s[n-1] == 'd' {
		var days int
		for _, c := range s[:n-1] {
			if c < '0' || c > '9' {
				return 0, errInvalidDuration(s)
			}
			days = days*10 + int(c-'0')
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, errInvalidDuration(s)
	}
	if d < 0 {
		return 0, errInvalidDuration(s)
	}
	return d, nil
}

func errInvalidDuration(s string) error {
	return &durationParseError{Raw: s}
}

type durationParseError struct{ Raw string }

func (e *durationParseError) Error() string {
	return "invalid duration: " + e.Raw + " (expected Go duration form like \"12h\" or trailing-d days like \"7d\")"
}

// newRecoveryLogger returns the *log.Logger used by setupClient (Pin 3) to
// construct the recovery logger injected into the pkg/api.Client at startup.
// The Client's verb methods (Kill, FindMissing, Expire) surface WARN messages
// via c.logger — SRD §14.6 and §5. The destination is the configured
// error log path, falling back to stderr if the file can't be opened.
// Best-effort: file is leaked for the lifetime of the CLI process; the OS
// reclaims on exit.
func newRecoveryLogger(cfg config.Config) *log.Logger {
	dest := io.Writer(os.Stderr)
	if cfg.Log.ErrorLogPath != "" {
		if f, err := os.OpenFile(cfg.Log.ErrorLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			dest = f
		}
	}
	return log.New(dest, "agent-director ", log.LstdFlags)
}
