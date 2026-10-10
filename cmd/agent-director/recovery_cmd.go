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

// expireHandlerWith implements `agent-director expire`. --older-than is
// parsed by pkgapi.ParseOlderThan, the parser MCP's older_than shares, and a
// value it rejects (neither form, a leading + or -, or a day count above
// config.MaxExpireRetentionDays, 106751) is refused with ErrInvalidFlags
// before Expire runs. Absent flag → cfg default.
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
		d, ok := pkgapi.ParseOlderThan(olderThanStr)
		if !ok {
			return writeApiErrorAndDispatch("ErrInvalidFlags",
				"--older-than: invalid duration: "+olderThanStr+" (expected "+pkgapi.OlderThanForm+")")
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
