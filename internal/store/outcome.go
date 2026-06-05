package store

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
