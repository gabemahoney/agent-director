package inputdelivery

import "github.com/gabemahoney/agent-director/internal/inputjournal"

// LegacyJournal retains the canonical importer and original synchronization inode.
type LegacyJournal = inputjournal.LegacyJournal

// OpenLegacyJournal opens the shared importer; transport defines no second format.
func OpenLegacyJournal(dir string) (*LegacyJournal, error) {
	return inputjournal.OpenLegacyJournal(dir)
}
