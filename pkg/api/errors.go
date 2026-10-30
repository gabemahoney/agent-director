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
// a startup prompt. State guards also apply to ordinary request reconciliation.
// send-keys returns this error for:
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

// ErrSubmissionUnconfirmed means ordinary delivery was refused or remains
// unconfirmed. SubmissionUnconfirmedDetails distinguishes not_started,
// partial_input_unconfirmed and submission_unconfirmed for both native delivery
// and Codex fallback paste. Native delivery requires durable exact transcript
// proof; pending Codex fallback uses bounded hook observation, whose failure
// carries only KeysSent, SubmissionConfirmed and RowState. Partial or uncertain
// input may still submit later; never replay text or Enter automatically.
// Only native attempts support passive reconciliation by repeating the original
// delivery_request_id after the state, relay and dialog guards pass. For fallback,
// inspect hook-derived get or status and escalate if submission remains unknown.
var ErrSubmissionUnconfirmed = errors.New("ErrSubmissionUnconfirmed")

// ErrSpawnNotPausable is returned by the pause verb when the target
// Spawn's state is not pausable (SRD §9). Pausable means `waiting`;
// `pending` / `working` / `ask_user` / `check_permission` all reject.
// `ended` / `missing` are not errors — they are no-op success, since
// the desired post-condition is already true.
var ErrSpawnNotPausable = errors.New("ErrSpawnNotPausable")

// ErrPauseTimeout is returned by the pause verb when the target Spawn
// did not transition to `ended` within `pause.timeout_seconds` after
// `/exit` and Enter keys were sent. Its PauseTimeoutDetails require session
// inspection and a caller's decision before more input, not an automatic
// retry. An exit prompt may be open, but no prompt or cause is proven.
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
// (SpawnParams.ReuseFinished, --reuse-finished) and passing the row's agent
// (SpawnParams.Agent, --agent; the description names the row's recorded one),
// since there is no conversation to resume. Cursor instead preserves the
// original row and artifacts and starts a fresh unique instance with --agent
// cursor. It is also returned when resume
// loses a race: the row changed between resume's read and its move to
// `pending`, and nothing was written.
var ErrSpawnNotResumable = errors.New("ErrSpawnNotResumable")

// ErrNoSessionId is returned by resume when the row's current session_id
// is empty, typically when a launch never reported SessionStart. It takes
// precedence over transcript lookup even if older sessions are listed.
// To recover, spawn again with the same id, opting in to reuse
// (SpawnParams.ReuseFinished) and passing the row's agent (SpawnParams.Agent,
// which get shows) to start a fresh conversation in the retained life.
// Cursor instead preserves the original row/session/transcript/artifacts and
// starts a fresh unique instance: from the intended project directory run
// agent-director spawn --agent cursor --cwd "$PWD", omitting instance_id.
// Older sessions remain listed for
// inspection only; resume never chooses them. An agent switch or
// switch back starts a new life with no memory of the earlier one.
var ErrNoSessionId = errors.New("ErrNoSessionId")

// ErrJsonlMissing is returned by resume when no current-session transcript
// candidate stats successfully, unless ErrJsonlNeverWritten applies. Claude
// tries the recorded jsonl_path and a path recomputed from cwd, session_id
// and a usable absolute CLAUDE_CONFIG_DIR (otherwise ~/.claude). Codex tries
// only its recorded path. Cursor instead validates its one saved protected
// gateway transcript and returns this error for a missing or unsafe reference;
// it never synthesizes a path or reads native storage. Cursor recovery can
// preserve the artifacts and spawn a new instance_id for a fresh conversation;
// Other agents' messages list only current-session attempts,
// with sources persisted and fallback; a Codex row with an empty recorded
// path and visible history has zero attempts and says that the current
// session has no recorded path. Older sessions are never tried, even when
// their transcripts exist. To recover, spawn again with the same id,
// opting in to reuse
// (SpawnParams.ReuseFinished) and passing the row's agent (SpawnParams.Agent,
// which get shows) for a fresh conversation in the retained life; older
// sessions remain listed for inspection only. An agent switch or switch back starts
// a new life with no memory of the earlier one.
var ErrJsonlMissing = errors.New("ErrJsonlMissing")

// ErrJsonlNeverWritten is returned by resume when the row has a current
// session_id, no current-session candidate exists, the recorded jsonl_path
// is empty and the visible history is empty. Visible history is the current
// life's entries minus the entry for the row's current session id. This
// classification applies to Claude/Codex; Cursor uses ErrJsonlMissing for an
// unavailable protected reference. Listed older history makes a missing current
// transcript ErrJsonlMissing even if no older transcript exists on disk.
// To recover, spawn again with the same id, opting in to reuse
// (SpawnParams.ReuseFinished) and passing the row's agent (SpawnParams.Agent,
// which get shows) for a fresh conversation in the retained life. An agent
// switch or switch back starts a new life with no memory of the earlier one.
var ErrJsonlNeverWritten = errors.New("ErrJsonlNeverWritten")

// ErrSendKeysWhileRelayed is returned by send-keys, plain or a pane answer
// (with a request token), and by pause, which is held as a plain send-keys
// (b.zdz), while a relay hook of the Spawn may still answer its request
// (b.146 rule 7): the relay owns that answer, and keys typed now would race
// it. A relay hook may still answer while its process runs (judged by
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
// be delivering it and advises retrying later ("retry send-keys later" from
// send-keys, "retry later" from pause): decide on it would return
// ErrAlreadyDecided.
//
// The guard holds for no time window beyond the relay hook's own, and has no
// zero-rows rule: the relay hook records its request and the Spawn's move to
// check_permission in one transaction (b.146 rule 1). It reads requests of a
// Spawn with relay_mode on in every live state, not only check_permission: a
// Spawn can read waiting, working or ask_user while a request is still open.
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
// without expect_pane_sha256, and by pause, which types `/exit` and Enter as
// such a send-keys would (b.zdz), while a permission request of the Spawn
// holds the dialog (PermissionRow.HoldsDialog): it is not proven gone (b.146
// step 2c), so no hook of the agent has shown its dialog gone, the dialog may
// still be on the pane, and typed keys, an Enter above all, would land on it;
// and it is not a Cursor request of an earlier launch. A request is proven
// gone by its tool's PostToolUse or PostToolUseFailure (the same tool_use_id;
// Claude rows only), on a Cursor row by the gateway's validated completed or
// failed update of its exact tool_use_id under the launch that admitted it,
// by a turn end under the turn_end rule (store.ProvenGoneTurnEnd; the
// proven_gone_how field of get-permission gives which hooks prove it and
// which requests they cover), or, on a non-Cursor row, by its Spawn's end
// (marked missing, ended, or resumed). agent-director's own records prove nothing: a
// request whose relay hook acked its verdict (delivery delivered), one
// answered at the pane through send-keys, and one closed with
// record-pane-answer all keep holding until then. A Cursor request stops
// holding, without being proven gone, once a resume replaces the launch that
// admitted it. The refusals for a relay hook that may still answer
// (ErrSendKeysWhileRelayed) and a fallen-back request (ErrRelayFallenBack)
// come first. A plain send-keys whose expect_pane_sha256 matches the pane as
// captured now is not refused with it (a mismatch is ErrPaneChanged). Pause
// takes no pane hash: a caller that has read the pane and answered any dialog
// on it types `/exit` with such a send-keys, or kills the Spawn. Nothing was
// sent. Its err_details (DialogMaybeOpenDetails) give the oldest such request
// with its delivery facts, pane_answer and unproven_since, the Spawn's state
// and the Spawn's other requests that hold the dialog.
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
// hook has not been gone for at least 2 s (b.146 rule 13, decision 5): the agent
// draws a permission dialog only after the hook is gone, so a pane read
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
//     an `older_than` that is in neither duration form, has a leading + or
//     -, or whose day count is above 106751 (expire), with a description
//     naming the param and the expected form (b.anw, b.hxn, b.sgw, b.c4n).
//   - The shared verb layer, for spawn, read-pane, list, decide, send-keys
//     and record-pane-answer, so the CLI, MCP, the Go client and the
//     TypeScript client all return it: runSpawn returns it (wrapped) when an
//     explicit instance id contains an ASCII control character (0x00-0x1f or
//     0x7f) (SR-9.1); ReadPane for a negative NLines and List for a negative
//     Limit, before anything is read (b.c4n); decide for a negative
//     max_wait_ms (DecideParams.MaxWaitMs, b.146 decision 9 B), which the CLI
//     refuses first; send-keys for a pane answer without as, key or
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
// So spawn's, read-pane's, list's, decide's, send-keys' and
// record-pane-answer's manifest ErrorNames list it; no other callable verb
// lists it, because the CLI flag-parse and MCP argument emissions are not
// verb-specific and the expire verb's surfaces never reach Expire's refusal:
// only a Go caller passing Client.Expire a negative olderThan does. (The
// internal, non-callable trail-emit verb also lists it.)
// It stays in five-way coherence check 3's exceptions per SR-1.7; while a
// callable verb lists it, that listing already satisfies check 3, so the
// exception changes nothing.
var ErrInvalidFlags = errors.New("ErrInvalidFlags")
