package api

import "errors"

// ErrClientClosed is returned by verb methods when they are called on a
// Client whose Close method has already been invoked. Callers should use
// errors.Is to detect it.
var ErrClientClosed = errors.New("api: client is closed")

// Verb-surface error sentinels per SRD §13.1. These sentinels MUST have a
// matching entry in pkg/api/errnames.Catalog. Coherence is enforced by the
// catalog test in pkg/api/errnames/catalog_test.go (Task 6, subtask ib).
// Adding a sentinel here without a Catalog entry will cause the doc-drift
// CI gate to diverge and the full test suite to fail.
//
// Note: pkg/api cannot import pkg/api/errnames (that would create a cycle
// since errnames imports pkg/api). The dependency direction is
// pkg/api/errnames → pkg/api. Coherence is runtime-tested, not static.

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

// ErrJsonlMissing is returned by the resume verb when NO candidate
// JSONL transcript path stats successfully on disk. Resume tries the
// persisted jsonl_path first, then a CLAUDE_CONFIG_DIR-aware fallback
// recomputed from the row's ExtraEnv (bug b.1ba), then each entry of the
// row's visible history, newest first (its recorded path, then its
// recomputed fallback); this sentinel fires only when every candidate
// fails. It is decided on the current life's history: session history
// belongs to a life, and the visible history is the current life's
// history minus the entry for the row's current session id. The file may
// have been hand-deleted, archived by the operator, or never written. The
// error message names every path tried and its source (persisted,
// fallback or history; all from the current life) with the stat error for
// each, so callers can log which candidates failed —
// the error NAME is stable, so name-based mapping is unaffected.
// Resume cannot proceed; `delete` + fresh `spawn` is the recourse.
var ErrJsonlMissing = errors.New("ErrJsonlMissing")

// ErrJsonlNeverWritten is returned by the resume verb when the row carries a
// claude_session_id but no transcript has EVER been written for it — the
// persisted jsonl_path is NULL (the SessionStart hook found no file on disk),
// the CLAUDE_CONFIG_DIR-aware fallback path also does not exist, and the
// row's visible history is empty, so there is nothing to fall back on. It is
// decided on the current life's history: session history belongs to a life,
// and the visible history is the current life's history minus the entry for
// the row's current session id, so that entry never counts as history. This
// is the b.v2c case of a freshly restarted agent that has not been messaged:
// a fresh Claude session writes no .jsonl until its first user turn, so there
// is genuinely nothing to resume.
//
// It is deliberately distinct from ErrJsonlMissing, whose meaning is
// "candidates were tried and none matched" — a path was once recorded (or
// composed) and has since rotted or been removed. The recourse differs:
// ErrJsonlNeverWritten means the session never produced history (send it a
// message, or delete + re-spawn), whereas ErrJsonlMissing means history existed
// but the file is gone. Callers map by error NAME; both remain stable.
var ErrJsonlNeverWritten = errors.New("ErrJsonlNeverWritten")

// ErrSendKeysWhileRelayed is returned when a caller tries to send keys
// into a Spawn that is currently sitting on a live relayed permission prompt
// (relay_mode=on AND state=check_permission). The relay path needs to own the
// modal answer; a parallel send-keys would race the relay's decide() write and
// split the answer across two pane events.
//
// The refusal is time-bounded, not unconditional: Claude Code kills the relay
// hook at its per-hook timeout, after which the poller can no longer deliver a
// decision. The guard consults the shared deliverability signal across every
// one of the Spawn's permission-request rows and RELEASES once every request's
// delivery window has elapsed — at that point send-keys is the sanctioned
// recovery surface for a Spawn wedged in check_permission behind a dead relay.
// The refusal stands only while at least one request row is still within its
// window (or the Spawn has zero request rows).
var ErrSendKeysWhileRelayed = errors.New("ErrSendKeysWhileRelayed")

// ErrInvalidFlags is returned when a flag or parameter value fails basic
// validation. It has two sources:
//   - CLI flag parsing, for every verb: when a required flag is absent or a
//     flag value fails basic validation (empty string, unrecognised enum
//     member, etc.), the handlers in cmd/agent-director/*.go write it as the
//     err_name string literal in the JSON error envelope via
//     writeApiErrorAndDispatch("ErrInvalidFlags", …).
//   - The shared verb layer, for spawn only: runSpawn returns it (wrapped)
//     when an explicit instance id contains an ASCII control character
//     (0x00-0x1f or 0x7f), so the CLI, MCP, the Go client and the TypeScript
//     client all return it (SR-9.1).
//
// So spawn's manifest ErrorNames lists it; no other callable verb lists it,
// because the CLI flag-parse emission is not verb-specific. (The internal,
// non-callable trail-emit verb also lists it.) It stays in five-way coherence
// check 3's exceptions per SR-1.7; while spawn lists it, spawn's listing
// already satisfies check 3, so the exception changes nothing.
var ErrInvalidFlags = errors.New("ErrInvalidFlags")
