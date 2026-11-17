package api

import (
	"errors"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// errorName names a verb error as errnames.Classify would, for the trail
// fields that carry an err_name: ad.resume.restored's and
// ad.spawn.reuse_restored's launch_error, ad.kill.called's outcome,
// ad.send_keys.called's outcome (b.146 step 2b's and 2c's refusals included) and
// ad.launch.name_held's outcome (SR-6.4, SR-7.4, SR-10.6, SR-14). pkg/api cannot import
// pkg/api/errnames (errnames imports pkg/api for its sentinels), so this is
// the one pkg/api mapping of those verbs' names; extend it here when one of
// them gains a name. Every name it gives matches exactly one catalogued
// sentinel (SR-1.5), so the order of the cases does not matter. An error that
// matches none of them, a store error or an unusable recorded name included,
// is ErrInternal. err must not be nil.
// A delegated resume additionally retains the recipient's explicit name,
// including one unknown to this older runtime's catalog.
func errorName(err error) string {
	var named interface{ ErrorName() string }
	if errors.As(err, &named) {
		return named.ErrorName()
	}
	switch {
	case errors.Is(err, spawn.ErrCwdMissing):
		return "ErrCwdMissing"
	case errors.Is(err, spawn.ErrCwdNotAPath):
		return "ErrCwdNotAPath"
	case errors.Is(err, spawn.ErrRelayModeInvalid):
		return "ErrRelayModeInvalid"
	case errors.Is(err, spawn.ErrSpawnDeniedFlag):
		return "ErrSpawnDeniedFlag"
	case errors.Is(err, ErrAgentMissing):
		return "ErrAgentMissing"
	case errors.Is(err, ErrAgentInvalid):
		return "ErrAgentInvalid"
	case errors.Is(err, ErrAgentArgsMismatch):
		return "ErrAgentArgsMismatch"
	case errors.Is(err, ErrPermissionsUnsupported):
		return "ErrPermissionsUnsupported"
	case errors.Is(err, spawn.ErrInstanceIdCollision):
		return "ErrInstanceIdCollision"
	case errors.Is(err, spawn.ErrTmuxSessionNameEmpty):
		return "ErrTmuxSessionNameEmpty"
	case errors.Is(err, spawn.ErrTmuxSessionNameInvalid):
		return "ErrTmuxSessionNameInvalid"
	case errors.Is(err, spawn.ErrTmuxSessionNameTooLong):
		return "ErrTmuxSessionNameTooLong"
	case errors.Is(err, ErrTmuxCaptureFailed):
		return "ErrTmuxCaptureFailed"
	case errors.Is(err, ErrSpawnNotPausable):
		return "ErrSpawnNotPausable"
	case errors.Is(err, ErrPauseTimeout):
		return "ErrPauseTimeout"
	case errors.Is(err, ErrListInvalidLabel):
		return "ErrListInvalidLabel"
	case errors.Is(err, config.ErrTemplateNameUnsafe):
		return "ErrTemplateNameUnsafe"
	case errors.Is(err, config.ErrTemplateNotFound):
		return "ErrTemplateNotFound"
	case errors.Is(err, config.ErrTemplateMalformed):
		return "ErrTemplateMalformed"
	case errors.Is(err, config.ErrTemplateExists):
		return "ErrTemplateExists"
	case errors.Is(err, ErrTemplateAgentMissing):
		return "ErrTemplateAgentMissing"
	case errors.Is(err, probe.ErrProbeUnsupported):
		return "ErrProbeUnsupported"
	case errors.Is(err, ErrRelayModeOff):
		return "ErrRelayModeOff"
	case errors.Is(err, ErrClaimTooSoon):
		return "ErrClaimTooSoon"
	case errors.Is(err, ErrInvalidDecision):
		return "ErrInvalidDecision"
	case errors.Is(err, ErrTooManyIds):
		return "ErrTooManyIds"
	case errors.Is(err, ErrMissingRequestToken):
		return "ErrMissingRequestToken"
	case errors.Is(err, ErrPermissionRequestNotFound):
		return "ErrPermissionRequestNotFound"
	case errors.Is(err, store.ErrAmbiguousRequest):
		return "ErrAmbiguousRequest"
	case errors.Is(err, ErrNoSessionId):
		return "ErrNoSessionId"
	case errors.Is(err, ErrJsonlMissing):
		return "ErrJsonlMissing"
	case errors.Is(err, ErrJsonlNeverWritten):
		return "ErrJsonlNeverWritten"
	case errors.Is(err, spawn.ErrReservedEnvKey):
		return "ErrReservedEnvKey"
	case errors.Is(err, spawn.ErrCwdNotFound):
		return "ErrCwdNotFound"
	case errors.Is(err, spawn.ErrCwdNotADirectory):
		return "ErrCwdNotADirectory"
	case errors.Is(err, ErrSchemaMismatch):
		return "ErrSchemaMismatch"
	case errors.Is(err, ErrSchemaMigrationRequired):
		return "ErrSchemaMigrationRequired"
	case errors.Is(err, ErrOldLayout):
		return "ErrOldLayout"
	case errors.Is(err, ErrHostCredentialMissing):
		return "ErrHostCredentialMissing"
	case errors.Is(err, ErrPermissionRulesRefused):
		return "ErrPermissionRulesRefused"
	case errors.Is(err, ErrModelMissing):
		return "ErrModelMissing"
	case errors.Is(err, ErrAgentCapabilityUnsupported):
		return "ErrAgentCapabilityUnsupported"
	case errors.Is(err, ErrExperimentalAgentDisabled):
		return "ErrExperimentalAgentDisabled"
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
	case errors.Is(err, ErrSubmissionUnconfirmed):
		return "ErrSubmissionUnconfirmed"
	case errors.Is(err, ErrCantSend):
		return "ErrCantSend"
	case errors.Is(err, ErrSendKeysWhileRelayed):
		return "ErrSendKeysWhileRelayed"
	case errors.Is(err, ErrSpawnNotResumable):
		return "ErrSpawnNotResumable"
	case errors.Is(err, ErrRelayFallenBack):
		return "ErrRelayFallenBack"
	case errors.Is(err, ErrPaneChanged):
		return "ErrPaneChanged"
	case errors.Is(err, ErrDialogMaybeOpen):
		return "ErrDialogMaybeOpen"
	case errors.Is(err, ErrPaneAnswerInProgress):
		return "ErrPaneAnswerInProgress"
	case errors.Is(err, ErrInvalidFlags):
		return "ErrInvalidFlags"
	case errors.Is(err, store.ErrNoOpenPermissionRequest):
		return "ErrNoOpenPermissionRequest"
	case errors.Is(err, store.ErrAlreadyDecided):
		return "ErrAlreadyDecided"
	case errors.Is(err, store.ErrStoreBusy):
		return "ErrStoreBusy"
	}
	return "ErrInternal"
}
