package api

import "errors"

// Verb-surface errors per SRD §13.1. The CLI's errCatalog matches via
// errors.Is so the canonical err_name envelope is stable.

// ErrSpawnNotInteractive is returned by interactive verbs (send-keys, and
// any future verb that drives the Spawn's input) when the target Spawn's
// state is not one of the live conversational states. Live states for
// send-keys are pending, waiting, working, ask_user, and check_permission;
// ended / missing reject (SRD §4.3, §5.1). pending is *technically* live
// but a Spawn that has not yet emitted SessionStart has no readable TUI,
// so this verb treats pending as non-interactive too — the caller must
// wait for the first hook to flip to waiting.
var ErrSpawnNotInteractive = errors.New("ErrSpawnNotInteractive")

// ErrSendKeysWhileRelayed is returned when a caller tries to send keys
// into a Spawn that is currently sitting on a relayed permission prompt
// (relay_mode=on AND state=check_permission). The relay path needs to
// own the modal answer; a parallel send-keys would race the relay's
// decide() write and split the answer across two pane events.
//
// The full relay flow lands in Epic 10. This Epic stubs the guard so the
// state-precondition surface is correct from day one: a caller hitting a
// check_permission row with relay_mode=on gets a clean typed error
// instead of a silent collision later.
var ErrSendKeysWhileRelayed = errors.New("ErrSendKeysWhileRelayed")
