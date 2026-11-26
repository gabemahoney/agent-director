package main

import (
	"io"

	"github.com/gabemahoney/agent-director/internal/adminapi"
	"github.com/gabemahoney/agent-director/internal/clisetup"
)

// parseCommandGlobalFlags resolves globals before opening a client. A repeat
// of a verb flag triggers a silent trial parse; handlers retain their
// existing help and parse-error handling (b.t1b, b.51h).
func parseCommandGlobalFlags(argv []string) (clisetup.GlobalFlags, []string, error) {
	g, args, err := clisetup.ParseGlobalFlags(argv)
	if (err != nil && !clisetup.IsGlobalFlagRepeat(err)) || len(args) == 0 {
		return g, args, err
	}
	// Finish validating verb-position globals before reporting a prefix repeat.
	pendingGlobalRepeat := err
	verb := args[0]
	g, tail, err := parseVerbGlobalFlags(verb, g, args[1:])
	err = clisetup.GlobalFlagScanError(pendingGlobalRepeat, err)
	if err != nil {
		return clisetup.GlobalFlags{}, nil, err
	}
	return g, append([]string{args[0]}, tail...), nil
}

func parseVerbGlobalFlags(verb string, g clisetup.GlobalFlags, args []string) (clisetup.GlobalFlags, []string, error) {
	switch verb {
	case "help", "--help", "-h":
		return clisetup.ParseVerbGlobalFlags(nil, g, args, nil)
	case "--version", "-v":
		fs := versionFlagSet()
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "migrate-journal":
		fs := migrateJournalFlagSet(new(adminapi.JournalMigrationOptions), new(bool))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "version":
		fs := versionFlagSet()
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "kill-finished":
		fs := killFinishedFlagSet(new(string))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "delete":
		fs := deleteFlagSet(new([]string))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "snapshot-store":
		fs := snapshotStoreFlagSet(new(string), new(int))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "version-facts":
		fs := versionFactsFlagSet()
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "publish-version":
		fs := publishVersionFlagSet(new(string))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "rollback-version":
		fs := rollbackVersionFlagSet(new(string))
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	case "list-versions":
		fs := listVersionsFlagSet()
		return clisetup.ParseVerbGlobalFlags(fs, g, args, func(tail []string) error {
			fs.SetOutput(io.Discard)
			return fs.Parse(tail)
		})
	default:
		return g, args, nil
	}
}
