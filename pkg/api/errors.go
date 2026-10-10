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
// any future verb that drives the agent's input) when the row cannot take
// keys. send-keys types into waiting, working, ask_user and check_permission
// rows. pending is a launch (spawn, reuse or resume) in progress whose agent
// has not reported in yet (a resumed row included); it may be loading or at
// a startup prompt. send-keys returns this error for:
//   - a finished row (ended or missing), with or without allow_pending;
//   - a pending row without allow_pending;
//   - a pending row with allow_pending whose launch start or launch token is
//     absent or unreadable, before any tmux call;
//   - a pending row with allow_pending whose lookup found only a session not
//     started by the row's current launch ("not this launch's session").
//
// With allow_pending, keys reach a pending row only in a session started by
// its current launch (SR-7.1, SR-18.14, SR-22.8).
var ErrSpawnNotInteractive = errors.New("ErrSpawnNotInteractive")

// ErrSpawnNotPausable is returned by the pause verb when the target
// Spawn's state is not pausable (SRD §9). Pausable means `waiting`;
// `pending` / `working` / `ask_user` / `check_permission` all reject.
// `ended` / `missing` are not errors — they are no-op success, since
// the desired post-condition is already true.
var ErrSpawnNotPausable = errors.New("ErrSpawnNotPausable")

// ErrPauseTimeout is returned by the pause verb when the target Spawn
// did not transition to `ended` within `pause.timeout_seconds` after
// the `/exit` command was sent. The caller's recourse is to retry the
// pause; the agent may still be running.
var ErrPauseTimeout = errors.New("ErrPauseTimeout")

// ErrSpawnNotResumable is returned by the resume verb when the target
// Spawn's state is not `ended` or `missing`: resume applies only to a
// finished row. A live row is refused because its agent is running. A
// `pending` row is refused too: it is a launch (spawn, reuse or resume) in
// progress whose agent has not reported in, a resumed row included, and the
// description says when the launch began and that, if the launch was
// abandoned or failed, find-missing marks the row missing once the pending
// grace period has passed since its launch start. For a row with no session
// id (typically a spawn's or reuse's launch), whose resume once missing
// returns ErrNoSessionId, the description names the step after that: if get
// still shows no session id, spawn the id again, opting in to reuse
// (SpawnParams.ReuseFinished, --reuse-finished), since there is no
// conversation to resume. It is also returned when resume
// loses a race: the row changed between resume's read and its move to
// `pending`, and nothing was written.
var ErrSpawnNotResumable = errors.New("ErrSpawnNotResumable")

// ErrNoSessionId is returned by the resume verb when the row's
// claude_session_id column is empty — typically because the Spawn
// was killed before its first SessionStart hook fired, or because a spawn's
// or reuse's launch never reported in and find-missing marked its pending
// row missing (resume's launch-in-progress refusal of that pending row,
// ErrSpawnNotResumable, names this recourse). With no
// session id there is no JSONL to point `claude --resume` at; the
// caller's recourse is to spawn again with the same id, opting in to reuse
// (SpawnParams.ReuseFinished, --reuse-finished). The reused id starts a new
// life with no memory of the earlier one.
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
// Resume cannot proceed; the recourse is to spawn again with the same id,
// opting in to reuse (SpawnParams.ReuseFinished, --reuse-finished). The
// reused id starts a new life with no memory of the earlier one, so the
// earlier conversation cannot be resumed through agent-director afterwards.
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
// composed) and has since rotted or been removed: ErrJsonlNeverWritten means
// the session never produced history, whereas ErrJsonlMissing means history
// existed but the file is gone. Callers map by error NAME; both remain
// stable. resume returns it only for a finished row, which send-keys
// refuses, so the recourse is to spawn again with the same id, opting in to
// reuse (SpawnParams.ReuseFinished, --reuse-finished); the reused id starts a
// new life with no memory of the earlier one.
var ErrJsonlNeverWritten = errors.New("ErrJsonlNeverWritten")

// ErrSendKeysWhileRelayed is returned by send-keys, plain or a pane answer
// (with a request token), while a relay hook of the Spawn may still answer its
// request (b.146 rule 7): the relay owns that answer, and keys typed now would
// race it. A relay hook may still answer while its process runs (judged by
// pid, start time and pid namespace, b.146 rule 14), or, when its process
// cannot be checked, until its request's confirm_by (its kill instant plus a
// 2 s reserve); a request recorded before schema v7 holds by its relay window,
// as before. A request whose relay hook acked its verdict holds only while the
// hook process is seen running (it is writing the verdict to Claude Code). A
// request that is closed (acked and its hook gone, answered at the pane, or
// closed with its Spawn) never holds.
//
// Its message names the request holding the guard — one still awaiting an
// answer in preference to an acked one, then the oldest — and states no
// release time (b.ah6). For an undecided request it advises answering it with
// decide. For one whose verdict is recorded it says the relay hook may still
// be delivering it and advises retrying send-keys later: decide on it would
// return ErrAlreadyDecided.
//
// The guard holds for no time window beyond the relay hook's own, and has no
// zero-rows rule: the relay hook records its request and the Spawn's move to
// check_permission in one transaction (b.146 rule 1). It reads requests of a
// Spawn with relay_mode on in every live state, not only check_permission: a
// Spawn can read waiting while a request is still open.
var ErrSendKeysWhileRelayed = errors.New("ErrSendKeysWhileRelayed")

// ErrPaneChanged is returned by send-keys and record-pane-answer given
// expect_pane_sha256 when the agent's pane, captured with the same n_lines
// and with ANSI stripped as read-pane gives it by default, no longer has that
// SHA-256 (b.146 rules 7, 8 and 13): its bytes changed since the caller read
// it. The comparison is byte equality; agent-director does not look at what
// the bytes say. Nothing was sent or recorded. The error does not carry the
// new hash: read the pane again (read-pane) and decide on what it shows
// before any retry. Its err_details (PaneChangedDetails) give n_lines.
var ErrPaneChanged = errors.New("ErrPaneChanged")

// ErrDialogMaybeOpen is returned by a plain send-keys (no request token)
// without expect_pane_sha256 while a permission request of the Spawn is not
// proven gone (b.146 step 2c): no hook of Claude Code has shown its dialog
// gone, so the dialog may still be on the pane, and typed keys, an Enter
// above all, would land on it. A request is proven gone by its tool's
// PostToolUse or PostToolUseFailure (the same tool_use_id), by the main
// agent's Stop or idle-prompt Notification written after it (a request with
// no agent_id: the main agent's, or one recorded before this release), or by
// its Spawn's end (marked missing, ended, or resumed). agent-director's own
// records prove nothing: a request whose relay hook acked its verdict
// (delivery delivered), one answered at the pane through send-keys, and one
// closed with record-pane-answer all stay unproven until then. The refusals
// for a relay hook that may still answer (ErrSendKeysWhileRelayed) and a
// fallen-back request (ErrRelayFallenBack) come first. A plain send-keys
// whose expect_pane_sha256 matches the pane as captured now is not refused
// with it (a mismatch is ErrPaneChanged). Nothing was sent. Its err_details
// (DialogMaybeOpenDetails) give the oldest such request with its delivery
// facts, pane_answer and unproven_since, the Spawn's state and the Spawn's
// other requests not proven gone.
var ErrDialogMaybeOpen = errors.New("ErrDialogMaybeOpen")

// ErrPaneAnswerInProgress is returned by send-keys with a request token, and
// by record-pane-answer, while another pane answer through send-keys on that
// request is still being sent (b.146 problem 2): its intent is recorded
// (pane_answer intent) and its sender process runs (pid, start time and pid
// namespace, b.146 rule 14), or, when the sender cannot be checked, the
// intent is younger than the tmux action timeout plus the pipe-close wait
// plus 2 s. It keeps a double click from typing two answers, the second
// landing on the next dialog or in the chat. Nothing was sent or recorded.
// Once that sender has ended (its call recorded sent, or ended without it and
// released the intent, or the process died), a retry with a fresh pane hash
// is accepted. Its err_details (PaneAnswerInProgressDetails) give the
// request, the claimed verdict, when the intent was written, whether the
// sender runs and, when it cannot be checked, not_before.
var ErrPaneAnswerInProgress = errors.New("ErrPaneAnswerInProgress")

// ErrClaimTooSoon is returned by record-pane-answer when the request's relay
// hook has not been gone for at least 2 s (b.146 rule 13, decision 5): Claude
// Code draws a permission dialog only after the hook is gone, so a pane read
// just after the hook's end may not show the dialog yet. It is measured from
// the request's hook_gone_at, when a reader first found the hook gone (a
// request with none yet gets it written now), or, when the hook cannot be
// checked (or the request was recorded before schema v7) and its confirm_by
// is earlier, from confirm_by: such a hook is gone by then at the latest. It
// is also returned while the relay hook may still answer the request (its
// process runs, or cannot be checked and its confirm_by has not passed).
// Nothing was recorded. Its err_details (ClaimTooSoonDetails) give
// not_before, the earliest time a record can be accepted (null only while the
// hook is seen running).
var ErrClaimTooSoon = errors.New("ErrClaimTooSoon")

// ErrInvalidFlags is returned when a flag or parameter value fails basic
// validation. It has four sources:
//   - CLI flag parsing, for every verb: when a required flag is absent or a
//     flag value fails basic validation (empty string, unrecognised enum
//     member, etc.), the handlers in cmd/agent-director/*.go write it as the
//     err_name string literal in the JSON error envelope via
//     writeApiErrorAndDispatch("ErrInvalidFlags", …).
//   - MCP argument checking, for every tool, before anything runs
//     (internal/mcp): an `arguments` key that is not one of the verb's
//     manifest param names (an old dashed name such as reuse-finished
//     included), refused before anything is decoded, with a description
//     naming the unknown key(s) and listing the valid param names (b.c4u);
//     an `arguments` value that is not a JSON object, or a param value of
//     the wrong JSON type, with a description naming the param and its
//     expected type, such as "an integer" or "an array of strings" (b.ewa);
//     and a `label` entry that is not key=value (spawn, make_template) or
//     an `older_than` that is not a non-negative duration, or whose day
//     count is above 106751 (expire), with a description naming the param
//     and the expected form (b.anw, b.hxn, b.sgw).
//   - The shared verb layer, for spawn, decide, send-keys and
//     record-pane-answer: runSpawn returns it (wrapped) when an explicit
//     instance id contains an ASCII control character (0x00-0x1f or 0x7f),
//     so the CLI, MCP, the Go client and the TypeScript client all return it
//     (SR-9.1); decide returns it for a negative max_wait_ms
//     (DecideParams.MaxWaitMs, b.146 decision 9 B), which the CLI refuses
//     first; send-keys for a pane answer without as, key or
//     expect_pane_sha256 or with text, and for any other combination its
//     params do not allow (planSendKeys, b.146 rule 8); record-pane-answer
//     for a missing token, an as other than allow, deny or unknown, or a
//     missing or malformed hash (b.146 rule 13).
//   - The exported Go function Expire, for a negative retentionDays or a
//     negative olderThan, before anything runs (b.f4v). Client.Expire passes
//     the configured retention, from 1 to config.MaxExpireRetentionDays, so
//     only a negative olderThan reaches the refusal through it; the CLI, MCP
//     and the TypeScript client never pass one, because they parse
//     older_than with ParseOlderThan, which refuses a negative value first
//     (the CLI flag-parse and MCP argument sources above).
//
// So spawn's, decide's, send-keys' and record-pane-answer's manifest
// ErrorNames list it; no other callable verb lists it, because the CLI
// flag-parse and MCP argument emissions are
// not verb-specific and the expire verb's surfaces never reach Expire's
// refusal: only a Go caller passing Client.Expire a negative olderThan does.
// (The internal, non-callable trail-emit verb also lists it.)
// It stays in five-way coherence check 3's exceptions per SR-1.7; while
// spawn lists it, spawn's listing already satisfies check 3, so the
// exception changes nothing.
var ErrInvalidFlags = errors.New("ErrInvalidFlags")
