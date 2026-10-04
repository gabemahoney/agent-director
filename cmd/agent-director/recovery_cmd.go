package main

import (
	"context"
	"flag"
	"io"
	"os"
	"time"

	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// findMissingHandlerWith implements `agent-director find-missing`.
// The verb takes no flags. Liveness comes only from each agent's process
// start time, read by the Client's start-time reader (probe.NewProcChecker,
// selected by build tags). Per-row store warnings route through the
// configured error log — the Client was constructed with a
// recovery logger (clisetup.Open Pin 3) so cron operators see them in their
// usual monitoring stream.
func findMissingHandlerWith(client *pkgapi.Client, args []string) error {
	fs := flag.NewFlagSet("find-missing", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}

	result, err := client.FindMissing(context.Background())
	if err != nil {
		name, desc := errnames.Classify(err)
		return writeApiErrorAndDispatch(name, errnames.TrimNamePrefix(name, desc))
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
		name, desc := errnames.Classify(err)
		return writeApiErrorAndDispatch(name, errnames.TrimNamePrefix(name, desc))
	}
	// ids and kept_ids are never nil on success (ExpireResult), so both
	// encode as [] when empty.
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
