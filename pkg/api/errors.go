package api

import (
	"errors"

	internalapi "github.com/gabemahoney/agent-director/internal/api"
)

// ErrClientClosed is returned by verb methods when they are called on a
// Client whose Close method has already been invoked. Callers should use
// errors.Is to detect it.
var ErrClientClosed = errors.New("api: client is closed")

// Verb-surface error sentinels re-exported from internal/api so callers of
// pkg/api need not import internal/* packages.
//
// Each is a variable alias pointing at the underlying sentinel so
// errors.Is(ErrX, err) and errors.Is(err, ErrX) both work across the
// package boundary.

// ErrSpawnNotInteractive is returned by send-keys when the Spawn's state
// is not interactive (not waiting/working/ask_user/check_permission).
var ErrSpawnNotInteractive = internalapi.ErrSpawnNotInteractive

// ErrSpawnNotPausable is returned by pause when the Spawn is in a state
// that cannot be paused.
var ErrSpawnNotPausable = internalapi.ErrSpawnNotPausable

// ErrPauseTimeout is returned by pause when the Spawn does not reach the
// ended state within the configured timeout.
var ErrPauseTimeout = internalapi.ErrPauseTimeout

// ErrSpawnNotResumable is returned by resume when the Spawn is not in an
// ended or missing state.
var ErrSpawnNotResumable = internalapi.ErrSpawnNotResumable

// ErrNoSessionId is returned by resume when the Spawn has no
// claude_session_id (no SessionStart hook has fired for it).
var ErrNoSessionId = internalapi.ErrNoSessionId

// ErrJsonlMissing is returned by resume when the JSONL transcript file
// does not exist on disk.
var ErrJsonlMissing = internalapi.ErrJsonlMissing

// ErrSendKeysWhileRelayed is returned by send-keys when the Spawn has
// relay_mode=on (send-keys is not supported in relay mode).
var ErrSendKeysWhileRelayed = internalapi.ErrSendKeysWhileRelayed

// ErrListInvalidLabel is returned by list when a label filter entry does
// not contain a literal '=' separator.
var ErrListInvalidLabel = internalapi.ErrListInvalidLabel

// ErrInvalidDecision is returned by decide when the decision string is
// neither "allow" nor "deny".
var ErrInvalidDecision = internalapi.ErrInvalidDecision

// ErrRelayModeOff is returned by decide when the Spawn's relay_mode is
// not "on".
var ErrRelayModeOff = internalapi.ErrRelayModeOff
