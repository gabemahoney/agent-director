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

// ErrSpawnNotPausable is returned by the pause verb when the target
// Spawn's state is not pausable (SRD §9). Pausable means `waiting`;
// `pending` / `working` / `ask_user` / `check_permission` all reject.
// `ended` / `missing` are not errors — they are no-op success, since
// the desired post-condition is already true.
var ErrSpawnNotPausable = errors.New("ErrSpawnNotPausable")

// ErrPauseTimeout is returned by the pause verb when the target Spawn
// did not transition to `ended` within `pause.timeout_seconds` after
// the `/exit` command was sent. The caller's recourse is to retry or
// escalate to `kill`.
var ErrPauseTimeout = errors.New("ErrPauseTimeout")

// ErrSpawnNotResumable is returned by the resume verb when the target
// Spawn's state is not terminal. Resume only resurrects rows in
// `ended` or `missing`; any live state means the original Spawn is
// still running and the caller should attach or send-keys, not
// resurrect.
var ErrSpawnNotResumable = errors.New("ErrSpawnNotResumable")

// ErrNoSessionId is returned by the resume verb when the row's
// claude_session_id column is empty — typically because the Spawn
// was killed before its first SessionStart hook fired. With no
// session id there is no JSONL to point `claude --resume` at; the
// caller's recourse is to `delete` and `spawn` fresh.
var ErrNoSessionId = errors.New("ErrNoSessionId")

// ErrJsonlMissing is returned by the resume verb when the JSONL
// transcript file resolved from cwd + claude_session_id does not
// exist on disk. The file may have been hand-deleted, archived by
// the operator, or never written. Resume cannot proceed; `delete` +
// fresh `spawn` is the recourse.
var ErrJsonlMissing = errors.New("ErrJsonlMissing")

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
