package main

import (
	"context"
	"flag"
	"io"
	"os"
	"time"

	"github.com/gabemahoney/agent-director/internal/clisetup"
	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
)

// sweepHandlerWith implements the canonical all-agent check.
func sweepHandlerWith(client *pkgapi.Client, args []string) error {
	fs := sweepFlagSet()
	parseErr := fs.Parse(args)
	var repeats clisetup.SingleFlagCheck
	fs.VisitAll(repeats.Visit)
	if err := flagErr(fs, repeats.ParseError(parseErr)); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if err := rejectUnexpectedArgs(fs.Args()); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	return writeSweep(client)
}

// findMissingHandlerWith implements the compatibility alias `find-missing`.
// The verb takes no flags. Liveness comes only from each agent's process
// start time, read by the Client's start-time reader (probe.NewProcChecker,
// selected by build tags). Per-row store warnings route through the
// configured error log — the Client was constructed with a
// recovery logger (clisetup.Open Pin 3) so cron operators see them in their
// usual monitoring stream.
func findMissingHandlerWith(client *pkgapi.Client, args []string) error {
	fs := findMissingFlagSet()
	parseErr := fs.Parse(args)
	var repeats clisetup.SingleFlagCheck
	fs.VisitAll(repeats.Visit)
	if err := flagErr(fs, repeats.ParseError(parseErr)); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if err := rejectUnexpectedArgs(fs.Args()); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}

	return writeSweep(client)
}

func writeSweep(client *pkgapi.Client) error {
	result, err := client.Sweep(context.Background())
	if err != nil {
		return writeVerbError(err)
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
	fs := expireFlagSet(&olderThanStr)
	parseErr := fs.Parse(args)
	var repeats clisetup.SingleFlagCheck
	fs.VisitAll(repeats.Visit)
	if err := flagErr(fs, repeats.ParseError(parseErr)); err != nil {
		return writeApiErrorAndDispatch("ErrInvalidFlags", err.Error())
	}
	if err := rejectUnexpectedArgs(fs.Args()); err != nil {
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
		return writeVerbError(err)
	}
	// ids and kept_ids are never nil on success (ExpireResult), so both
	// encode as [] when empty.
	return writeJSON(os.Stdout, result)
}

// sweepFlagSet defines sweep's flags for both global scanning and the handler.
func sweepFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("sweep", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.VisitAll(clisetup.TrackSingleFlag)
	return fs
}

// findMissingFlagSet defines find-missing's flags for both global scanning and the handler.
func findMissingFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("find-missing", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.VisitAll(clisetup.TrackSingleFlag)
	return fs
}

// expireFlagSet defines expire's flags for both global scanning and the handler.
func expireFlagSet(olderThanStr *string) *flag.FlagSet {
	fs := flag.NewFlagSet("expire", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(olderThanStr, "older-than", "", "duration override (e.g. 7d, 12h, 0d). Default: cfg.defaults.expire_retention_days.")
	fs.VisitAll(clisetup.TrackSingleFlag)
	return fs
}
