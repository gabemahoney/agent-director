# Caller contract

Rules for programs and agents that call agent-director. The class of every tmux
error:
[tmux refusal classes](architecture.md#caller-contract-tmux-refusal-classes).

- Only a GONE answer means a row's session is not there. Any other tmux error
  means "don't know", never "dead".
- Row state, kept honest by `find-missing`, is the liveness authority. Poll a
  launch with `status`; call `read-pane` only while the row is `pending`, at
  most once a second.
- Run as the same user, in the same tmux environment, as the agents.
- No verb removes a row you name; only a scheduled `expire` does.
- A situation that needs a human is in [Operator actions](operator-actions.md);
  never perform those steps. A `spawn` whose tmux session name is held returns
  an error naming the holder and ends its new row: leave the holder to a human.
- A call refused before anything was written or sent is safe to re-issue. Retry
  `ErrTmuxUnresponsive` and `ErrTmuxKillFailed` with backoff and a cap, and
  alert at the cap, but not a timed-out `spawn` or `resume`, nor a `send-keys`
  whose text may be typed (see below). After a `kill` ends the last session on
  a tmux server, a
  repeated `kill` can get `ErrTmuxUnresponsive` or `ErrTmuxNotAvailable`: wait
  and check again.
- `send-keys` returning `ErrTmuxUnresponsive` with "may have been delivered" or
  "may be typed": do the step the error names, never resend the same text.
  `--text ""` sends Enter only. Retry a `pause` that returns
  `ErrTmuxUnresponsive` later.
- `resume` or `spawn --reuse-finished` refused as "still stopping" or "still
  starting": wait and retry. Refused with "this row's own id", or
  `ErrTmuxSessionConflict` "this id's own abandoned launch": surface the named
  session to a human; never end it yourself.
- After a timed-out `spawn` or `resume` the row stays `pending`: do not retry
  until `get` shows `ended` or `missing`, then `spawn --claude-instance-id`
  with `--reuse-finished`. A row noted `unreported` has a running agent: use it
  or end it first (see [Maintenance](maintenance.md)).
- `ErrConfigMalformed` (a config value is refused): store-backed verbs fail,
  `serve` does not start, hooks record nothing and relayed permission requests
  are denied. Take no action, alert once, never read it as "dead"; a human fixes
  the value it names. An unknown argument or wrong-typed value returns
  `ErrInvalidFlags` and does nothing; `make-template` on an existing name
  without `--overwrite` returns `ErrTemplateExists`.
- Detect features by the binary's version (`agent-director version`, MCP
  `version`, TypeScript `binaryVersion`); `X.Y.Z-rc.N` counts as `X.Y.Z`.
- A TypeScript call that outlasts `callTimeoutMs` (30 s by default) returns
  `ErrCallTimeout` while the verb may still complete: check the row first.

To end a live row (`pending` included) and relaunch its id:

1. `agent-director kill --claude-instance-id <id>`; on an error follow its
   class.
2. A `pending` row: wait until its `launch_started_at` (in `status`) plus
   `pending_grace_seconds` (ask the operator for the value in force, see
   [timing settings](configuration.md#timing-settings-tmux)) has passed. Inside
   the grace period, wait and check later; never escalate.
3. Run `agent-director find-missing`, then check with `status` that the row is
   `ended` or `missing`; if not, wait about 5 s and repeat, up to three runs.
4. Still live: `kill` once more, wait about 5 s, run `find-missing`, check.
5. Still live: stop and escalate to a human.
6. Once `ended` or `missing`:
   - with a session id, to get the conversation back:
     `agent-director resume --claude-instance-id <id>`
   - otherwise, a reuse (the old conversation is unreachable for good):
     `agent-director spawn --claude-instance-id <id> --reuse-finished --cwd <dir>`

   A caller whose ids agent-director mints spawns fresh instead.
