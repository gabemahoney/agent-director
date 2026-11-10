package store

import "testing"

// JournalMigrationFailureFixture exposes only the existing genuine legacy
// fixture's failure/retry boundary to store_test, which can import storefix.
// It creates no second schema or evidence fixture and never opens a Store.
type JournalMigrationFailureFixture struct {
	Path, LegacyDir string
	Migrate         func() (InputMigrationResult, error)
	Dump            func() map[string]any
}

func NewJournalMigrationFailureFixture(t *testing.T) JournalMigrationFailureFixture {
	t.Helper()
	f := newJournalMigrationFixture(t)
	return JournalMigrationFailureFixture{
		Path: f.path, LegacyDir: f.dir,
		Migrate: func() (InputMigrationResult, error) { return f.migrate(t, nil) },
		Dump: func() map[string]any {
			dump := dbDump(t, f.path)
			dump["user_version"] = readUserVersion(t, f.path)
			return dump
		},
	}
}
