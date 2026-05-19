package main

import (
	"context"
	"flag"
	"io"
	"log"
	"os"
	"time"

	"github.com/gabemahoney/claude-director/internal/api"
	"github.com/gabemahoney/claude-director/internal/config"
	"github.com/gabemahoney/claude-director/internal/probe"
	"github.com/gabemahoney/claude-director/internal/store"
)

// findMissingHandlerWith implements `claude-director find-missing`.
// The verb takes no flags. The prober is selected by build tags
// (probe.New). Warnings (e.g. degraded-mode guard) route through the
// configured error log so cron operators see them in their usual
// monitoring stream.
func findMissingHandlerWith(st *store.Store, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("find-missing", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}

	result, err := api.FindMissing(context.Background(), st, probe.New(), newRecoveryLogger(cfg))
	if err != nil {
		name, desc := classifyError(err)
		return writeApiErrorAndDispatch(name, errMessageStartsWithName(name, desc))
	}
	if result.IDs == nil {
		result.IDs = []string{}
	}
	return writeJSON(os.Stdout, result)
}

// expireHandlerWith implements `claude-director expire`. --older-than
// accepts the same form Go's time.ParseDuration handles, plus a `d`
// suffix for days (Go's parser does not). Absent flag → cfg default.
func expireHandlerWith(st *store.Store, cfg config.Config, args []string) error {
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

	result, err := api.Expire(st, cfg, older, newRecoveryLogger(cfg))
	if err != nil {
		name, desc := classifyError(err)
		return writeApiErrorAndDispatch(name, errMessageStartsWithName(name, desc))
	}
	if result.IDs == nil {
		result.IDs = []string{}
	}
	return writeJSON(os.Stdout, result)
}

// deleteHandlerWith implements `claude-director delete`. --claude-instance-id
// is repeatable; at least one is required. The per-row result map is
// the entire JSON envelope.
func deleteHandlerWith(st *store.Store, args []string) error {
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
	result, err := api.Delete(st, ids)
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
	return d, nil
}

func errInvalidDuration(s string) error {
	return &durationParseError{Raw: s}
}

type durationParseError struct{ Raw string }

func (e *durationParseError) Error() string {
	return "invalid duration: " + e.Raw + " (expected Go duration form like \"12h\" or trailing-d days like \"7d\")"
}

// newRecoveryLogger returns the *log.Logger used by the cron-shaped
// verbs (find-missing, expire). The destination is the configured
// error log path, falling back to stderr if the file can't be opened.
// Best-effort: file is leaked for the lifetime of the verb call (the
// short-lived CLI process; the OS reclaims on exit).
func newRecoveryLogger(cfg config.Config) *log.Logger {
	dest := io.Writer(os.Stderr)
	if cfg.Log.ErrorLogPath != "" {
		if f, err := os.OpenFile(cfg.Log.ErrorLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			dest = f
		}
	}
	return log.New(dest, "claude-director ", log.LstdFlags)
}
