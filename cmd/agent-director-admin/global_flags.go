package main

import (
	"github.com/gabemahoney/agent-director/internal/adminapi"
	"github.com/gabemahoney/agent-director/internal/clisetup"
)

// parseCommandGlobalFlags resolves globals without parsing verb values or
// opening a client. Handlers still own validation and execution (b.t1b).
func parseCommandGlobalFlags(argv []string) (clisetup.GlobalFlags, []string, error) {
	g, args, err := clisetup.ParseGlobalFlags(argv)
	if err != nil || len(args) == 0 {
		return g, args, err
	}
	verb := args[0]
	g, tail, err := parseVerbGlobalFlags(verb, g, args[1:])
	if err != nil {
		return clisetup.GlobalFlags{}, nil, err
	}
	return g, append([]string{args[0]}, tail...), nil
}

func parseVerbGlobalFlags(verb string, g clisetup.GlobalFlags, args []string) (clisetup.GlobalFlags, []string, error) {
	switch verb {
	case "help", "--help", "-h":
		return clisetup.ParseVerbGlobalFlags(nil, g, args)
	case "--version", "-v":
		return clisetup.ParseVerbGlobalFlags(versionFlagSet(), g, args)
	case "migrate-journal":
		return clisetup.ParseVerbGlobalFlags(migrateJournalFlagSet(new(adminapi.JournalMigrationOptions), new(bool)), g, args)
	case "version":
		return clisetup.ParseVerbGlobalFlags(versionFlagSet(), g, args)
	case "kill-finished":
		return clisetup.ParseVerbGlobalFlags(killFinishedFlagSet(new(string)), g, args)
	case "delete":
		return clisetup.ParseVerbGlobalFlags(deleteFlagSet(new([]string)), g, args)
	case "snapshot-store":
		return clisetup.ParseVerbGlobalFlags(snapshotStoreFlagSet(new(string), new(int)), g, args)
	case "version-facts":
		return clisetup.ParseVerbGlobalFlags(versionFactsFlagSet(), g, args)
	case "publish-version":
		return clisetup.ParseVerbGlobalFlags(publishVersionFlagSet(new(string)), g, args)
	case "rollback-version":
		return clisetup.ParseVerbGlobalFlags(rollbackVersionFlagSet(new(string)), g, args)
	case "list-versions":
		return clisetup.ParseVerbGlobalFlags(listVersionsFlagSet(), g, args)
	default:
		return g, args, nil
	}
}
