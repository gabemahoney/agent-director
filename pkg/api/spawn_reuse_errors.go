package api

import (
	"errors"
	"fmt"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
)

// This file holds the descriptions only reuse (spawn with the reuse opt-in,
// SR-10) returns (SR-1.4, SR-1.5, SR-5.8): the lost race, the two ErrInternal
// failures of the reuse change, and the launch timeout's row sentence. The
// reuse path and its tests quote these and nothing else. No description
// carries another row's id, a label value, an environment value, a
// session-ending command or the opt-in's flag.

// reuseNothingChanged ends every refusal reuse returns once it decided to
// change the row and the change did not apply: no archive, no reset, no
// permission-request deletion and no launch (SR-10.3, SR-5.8).
const reuseNothingChanged = "nothing was changed"

// reuseRowResetStaysPending is reuse's row sentence for a launch timeout
// (SR-1.4 row "ErrTmuxUnresponsive, launch timeout"; SR-10.4), passed as the
// consequence of spawn.LaunchTimeoutError: the row was reset for its fresh launch
// and stays pending, since the session may have been created. It never says
// that nothing was done or to retry later.
const reuseRowResetStaysPending = "the row was reset; " + spawn.RowStaysPending

// reuseLostRaceError is reuse's lost race (SR-1.4 row "ErrInstanceIdCollision,
// reuse lost race"; SR-10.3, SR-10.5): the row changed or was removed after
// this spawn examined it, found by the reset's condition or by the one re-read
// after the old-row lookup found a left-over session (reuseLostRace). Nothing
// was changed. It wraps spawn.ErrInstanceIdCollision only (SR-1.5).
func reuseLostRaceError(instanceID string) error {
	return fmt.Errorf("%w: spawn of instance %s: the row changed or was removed after this spawn examined it and %s",
		spawn.ErrInstanceIdCollision, instanceID, reuseNothingChanged)
}

// reuseExitError is reuse's refusal of a covered conversation whose earlier
// Launch cannot be shown unable to deliver (SR-6.3; M1's ReusePublish): an
// owned process of it (the agent, or a hook or delivery job) still runs or
// cannot be checked, judged before the change or found changed by it.
// Nothing was changed. Like a live row, the id is still in use, so it wraps
// spawn.ErrInstanceIdCollision only (SR-1.5). The row is finished, where
// kill is a no-op, so the recovery is the spawn again when the condition
// clears (Main Agent decision 16): when, such as "once that process has
// exited", or "" when only a retry is needed (the processes changed after
// the judgement).
func reuseExitError(instanceID, why, when string) error {
	if when != "" {
		when = " " + when
	}
	return fmt.Errorf("%w: spawn of instance %s: %s, so its earlier Launch cannot be shown unable to deliver and %s; spawn again%s",
		spawn.ErrInstanceIdCollision, instanceID, why, reuseNothingChanged, when)
}

// reuseChangeError maps a failed reuse change (ResetForReuse's error) to one
// of reuse's two ErrInternal descriptions (SR-1.4, SR-5.8), the one place
// this mapping lives: an archive failure, told apart by errors.Is on
// store.ErrReuseArchive and never by its text, says that archiving the
// previous session failed; every other failure (a busy store past its
// timeout included) says that the reuse could not be applied. Both say that
// nothing was changed. The store error's text follows for diagnosis, written
// with %v and never wrapped, so the result matches no catalogued sentinel
// and every surface names it ErrInternal (SR-1.5), the pattern of
// spawn.PreCheckReadError and resumeMoveError.
func reuseChangeError(instanceID string, err error) error {
	what := "the reuse could not be applied"
	if errors.Is(err, store.ErrReuseArchive) {
		what = "archiving the previous session failed"
	}
	return fmt.Errorf("spawn of instance %s: %s and %s: %v", instanceID, what, reuseNothingChanged, err)
}
