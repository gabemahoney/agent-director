package store

import (
	"sync"
	"time"
)

// InputMigrationFaults lets tests reach MigrateInputJournal's commit-state
// failure points without waiting or racing (SRD SR-8.6 "Commit state"). Every
// field is zero in production; a zero field changes nothing. These are an
// attempt's only fault-injection points: a new one is a field here, never a
// second seam (docs/architecture.md "migrate-journal fault seams and child
// runner").
type InputMigrationFaults struct {
	// AfterStepCommit runs after an ordinary schema step commits, before the
	// cutover commit begins. A non-nil error is reported as that step's
	// commit unconfirmed.
	AfterStepCommit func(from int) error
	// BeforeCutoverCommit runs just before the cutover gate, so an interrupt
	// it causes arrives before the cutover commit began.
	BeforeCutoverCommit func()
	// AfterCutoverCommitBegan runs just after the gate records that the
	// cutover commit began.
	AfterCutoverCommitBegan func()
	// CutoverCommit wraps the cutover's COMMIT. A non-nil return is an
	// unconfirmed cutover commit, whether or not it called commit.
	CutoverCommit func(commit func() error) error
	// AttemptTimeout replaces the attempt's 30 s deadline when non-zero. A
	// negative value gives a deadline that has already passed.
	AttemptTimeout time.Duration
	// BusyTimeoutMs replaces DefaultBusyTimeoutMs for the attempt's snapshot
	// read and store open when positive.
	BusyTimeoutMs int
}

var (
	inputMigrationFaultsMu sync.Mutex
	inputMigrationFaults   InputMigrationFaults
)

// SetInputMigrationFaultsForTest installs faults for every later
// MigrateInputJournal call in this process until restore runs. Each attempt
// copies them when it starts. Normal administrator runs never call it. A test
// that fails an attempt at a chosen point uses it, never a second seam.
func SetInputMigrationFaultsForTest(faults InputMigrationFaults) (restore func()) {
	inputMigrationFaultsMu.Lock()
	previous := inputMigrationFaults
	inputMigrationFaults = faults
	inputMigrationFaultsMu.Unlock()
	return func() {
		inputMigrationFaultsMu.Lock()
		inputMigrationFaults = previous
		inputMigrationFaultsMu.Unlock()
	}
}

func currentInputMigrationFaults() InputMigrationFaults {
	inputMigrationFaultsMu.Lock()
	defer inputMigrationFaultsMu.Unlock()
	return inputMigrationFaults
}
