package api

import "fmt"

// inputCleanupError preserves the operation's result and error independently
// of writer cleanup. Cleanup cannot retract positive proof or turn observed
// key dispatch into a pre-input refusal that invites replay.
func inputCleanupError(row Spawn, facts keysFacts, result SendKeysResult, primary, cleanup error) error {
	if primary != nil {
		return fmt.Errorf("%w; input writer cleanup failed: %w", primary, cleanup)
	}
	if result.Outcome == "submitted" {
		return &DetailedError{
			Err:     fmt.Errorf("send-keys: %s: input writer cleanup failed after confirmed submission: %w", row.InstanceID, cleanup),
			Details: result,
		}
	}
	attempt := facts.deliveryAttempt()
	reason := "input writer cleanup failed: " + cleanup.Error()
	details := submissionDetails(row, attempt, unresolvedOutcome(attempt), reason)
	// Named keys have no literal bytes and need not be Enter, but their
	// dispatch is still input. Sent is recorded before calling the transport.
	details.KeysSent = facts.Sent
	err := deliveryError(row, attempt, details.Outcome, reason)
	return &DetailedError{Err: fmt.Errorf("%w: %w", err, cleanup), Details: details}
}
