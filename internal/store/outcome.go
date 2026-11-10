package store

import "errors"

// CommitUnconfirmed distinguishes an abandoned COMMIT from a refused write.
// Callers must retain the old view until a later independent read confirms it.
func CommitUnconfirmed(err error) bool { return errors.Is(err, errCommitUnconfirmed) }

// UpsertOutcome categorizes a store write result for trail emission
// (SR-A-2.1). The value is returned by ApplyHookTransitionResult and
// UpsertOpenPermissionRequestResult so hook-layer callers never infer
// the outcome from error presence alone.
type UpsertOutcome string

const (
	// UpsertInserted indicates a new row was created.
	UpsertInserted UpsertOutcome = "inserted"
	// UpsertUpdated indicates an existing row was modified.
	UpsertUpdated UpsertOutcome = "updated"
	// UpsertNoChange indicates the call completed without mutating any
	// row (no-op guard, missing row, or similar).
	UpsertNoChange UpsertOutcome = "no_change"
	// UpsertError indicates the call failed.
	UpsertError UpsertOutcome = "error"
)

// RecoverySampleChanged identifies evidence rejected by an observation fence.
func RecoverySampleChanged(err error) bool { return errors.Is(err, errDisplaySampleChanged) }
