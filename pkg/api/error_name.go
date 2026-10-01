package api

import (
	"errors"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// errorName names a verb error as errnames.Classify would, for the trail
// fields that carry an err_name: ad.resume.restored's and
// ad.spawn.reuse_restored's launch_error, ad.kill.called's outcome,
// ad.send_keys.called's outcome and ad.launch.name_held's outcome (SR-6.4,
// SR-7.4, SR-10.6, SR-14). pkg/api cannot import
// pkg/api/errnames (errnames imports pkg/api for its sentinels), so this is
// the one pkg/api mapping of those verbs' names; extend it here when one of
// them gains a name. Every name it gives matches exactly one catalogued
// sentinel (SR-1.5), so the order of the cases does not matter. An error that
// matches none of them, a store error or an unusable recorded name included,
// is ErrInternal. err must not be nil.
func errorName(err error) string {
	switch {
	case errors.Is(err, store.ErrSpawnNotFound):
		return "ErrSpawnNotFound"
	case errors.Is(err, tmux.ErrTmuxNotAvailable):
		return "ErrTmuxNotAvailable"
	case errors.Is(err, tmux.ErrTmuxSessionCreate):
		return "ErrTmuxSessionCreate"
	case errors.Is(err, tmux.ErrTmuxSessionConflict):
		return "ErrTmuxSessionConflict"
	case errors.Is(err, tmux.ErrTmuxUnresponsive):
		return "ErrTmuxUnresponsive"
	case errors.Is(err, tmux.ErrTmuxKillFailed):
		return "ErrTmuxKillFailed"
	case errors.Is(err, tmux.ErrTmuxSendKeys):
		return "ErrTmuxSendKeys"
	case errors.Is(err, ErrSpawnNotInteractive):
		return "ErrSpawnNotInteractive"
	case errors.Is(err, ErrSendKeysWhileRelayed):
		return "ErrSendKeysWhileRelayed"
	case errors.Is(err, ErrSpawnNotResumable):
		return "ErrSpawnNotResumable"
	}
	return "ErrInternal"
}
