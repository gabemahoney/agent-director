package main

import (
	"flag"

	"github.com/gabemahoney/agent-director/internal/adminapi"
)

// FlagSetsForTest exposes real registrations so new flags join b.51h coverage.
func FlagSetsForTest() []*flag.FlagSet {
	return []*flag.FlagSet{
		versionFlagSet(), killFinishedFlagSet(new(string)), deleteFlagSet(new([]string)),
		migrateJournalFlagSet(new(adminapi.JournalMigrationOptions), new(bool)),
		snapshotStoreFlagSet(new(string), new(int)), versionFactsFlagSet(),
		publishVersionFlagSet(new(string)), rollbackVersionFlagSet(new(string)), listVersionsFlagSet(),
	}
}
