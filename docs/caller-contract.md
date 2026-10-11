# Caller contract

For programs and agents that call agent-director. The full contract, with
the class of every tmux error, is in
[Caller contract: tmux refusal classes](architecture.md#caller-contract-tmux-refusal-classes).

- Only a GONE answer means a row's session is not there. Every other tmux
  error means "don't know", never "dead".
- No `agent-director` verb removes a row you name; only a scheduled
  `expire` removes finished rows (agents never run it).
- The row state, kept honest by `find-missing`, is the liveness authority;
  there is no liveness verb.
- `read-pane` changes nothing and is not a liveness check; a caller polling
  a launch polls `status`, and calls `read-pane` only while the row is
  `pending`, about once a second at most.
- Run as the same user and in the same tmux environment as the agents.
- A situation that needs a human is described in
  [Operator actions](operator-actions.md); callers never perform those
  actions.
- A `spawn` whose tmux session name is already held returns an error naming
  the holder and ends its new row; the holder is left to a human, never
  ended by the caller.
- A call refused before anything was written or sent is safe to re-issue
  later; that is how a caller learns a condition has cleared. Retry
  `ErrTmuxUnresponsive` and `ErrTmuxKillFailed` with backoff and a cap, and alert at the cap
  (a timed-out launch and a `send-keys` whose text may be typed are the
  exceptions below).
- When `send-keys` returns `ErrTmuxUnresponsive` saying its keys may have
  been delivered or its text may be typed, do the step the error names
  instead of sending the same text again. `send-keys` with empty text
  (`--text ""`) sends Enter only, submitting what is typed. A `pause` that
  returns `ErrTmuxUnresponsive` is simply retried later: before typing
  `/exit` it clears the line the agent's cursor is on, so an `/exit` the
  failed `pause` left typed is not doubled.
- A `resume` or `spawn --reuse-finished` refused because the row's own
  session or agent "appears to still be stopping" or "starting", or this
  id's own abandoned launch "appears to still be starting": wait and
  retry. Refused with "this row's own id", or with
  `ErrTmuxSessionConflict` "this id's own abandoned launch": stop and
  surface the named session to a human
  ([Operator actions](operator-actions.md)); never end it yourself.
- After a timed-out `spawn` or `resume` the row stays `pending`: do not
  retry until `get` shows it `ended` or `missing`. Then retry a `spawn`
  with `--claude-instance-id` by adding `--reuse-finished`; without it the
  spawn collides with the finished row. A row noted `unreported` instead
  has a running agent: use it or end it before you retry.
- `pending` means a launch is in progress; `status`, `get` and `list` show
  its start (`launch_started_at`). `kill` on a `pending` row aborts a stuck
  launch.
- To use or end a `pending` row noted `unreported`, see [Maintenance](maintenance.md).
- `ErrConfigMalformed` means agent-director cannot use its config file:
  take no action, alert once, never read it as "dead".
- A reused id starts with no memory of its earlier lives.
- Detect features by the binary's version (`agent-director version`, the
  MCP `version` tool, or the TypeScript client's `binaryVersion`); a
  release candidate `X.Y.Z-rc.N` counts as `X.Y.Z`.
- The contract also lists this release's
  [known limitations](architecture.md#known-limitations) and
  [meaning changes](architecture.md#meaning-changes).
- Right after a `kill` ends the last session on its tmux server, the
  server takes a moment to exit. A repeated `kill` in that moment can get
  `ErrTmuxUnresponsive` or `ErrTmuxNotAvailable`; the caller waits and
  checks again.

To end a live row (`pending` included) and relaunch its id, a caller
follows this bounded, paced sequence:

1. `agent-director kill --claude-instance-id <id>`, and check the result;
   on any error follow its class.
2. If the row is `pending`, wait until its launch start
   (`launch_started_at`, shown by
   `agent-director status --claude-instance-id <id>`) plus the pending
   grace period (60 s by default; the operator's
   [timing settings](configuration.md#timing-settings-tmux) can make it longer or
   shorter, so ask the operator for the value in force) has passed; a
   pending row inside its grace period means wait and check again later,
   never escalate.
3. Run `agent-director find-missing`, then confirm with
   `agent-director status --claude-instance-id <id>` (or `get`) that the
   row is `ended` or `missing`; if not, wait about 5 s and repeat, up to
   three `find-missing` runs in all.
4. If still live, `kill` once more, wait about 5 s, run `find-missing` once
   more and check.
5. If still live, stop and escalate to a human.
6. Once the row is `ended` or `missing`, resume it if it has a session id
   and the caller wants the conversation back
   (`agent-director resume --claude-instance-id <id>`); otherwise spawn with
   `--reuse-finished`
   (`agent-director spawn --claude-instance-id <id> --reuse-finished --cwd <dir>`).
   A caller whose ids agent-director mints spawns fresh instead of reusing.
   A `pending` row, a resumed one included, enters this sequence, so a
   stuck `resume` handled this way can still get its conversation back; a
   reuse makes that conversation unreachable for good, because a row's
   session history belongs to one life and a reuse starts a new one.
