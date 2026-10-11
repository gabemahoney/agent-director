# Operator actions

These actions are for humans only: automated callers (programs, scripts,
agents, MCP clients) must not perform them.

Two of them run on the operator tool `agent-director-admin`, which
`install.sh` puts at `~/.agent-director/admin/agent-director-admin`, never
on PATH; run it by that full path. Its help opens with its rule: do not run
any of its commands without explicit approval from a human for that
specific run, and agents and automated callers must not run it.

Run every command as the agents' user. Every tmux command names the row's
socket with `-S '<socket>'`: the `tmux_socket` that
`agent-director get --claude-instance-id <id>` shows (a row that records
none uses the socket of your tmux environment, by default
`/tmp/tmux-<uid>/default`). Keep the single quotes, so the shell leaves the
`$` of a session id alone. A session's environment is never evidence of
whose it is. Trail records are lines of `~/.agent-director/ad-trail.jsonl`.
Never remove a row after a `kill` that did not succeed.

## This store's id

The last field of a session's `@ad_owner` label, and the `store_id` of a
trail record, is a store's id. Read this store's own id directly, as the
agents' user:

```sh
sqlite3 -readonly -batch -init /dev/null -cmd ".timeout <busy_timeout_ms>" ~/.agent-director/state.db "SELECT value FROM store_meta WHERE key = 'store_id'"
```

Replace `<busy_timeout_ms>` with your config's `[store] busy_timeout_ms`,
or its default if unset (see [Configuration](configuration.md)), so the read
waits for a locked store as long as agent-director does. It prints 16
lowercase hexadecimal characters, whatever your `~/.sqliterc` sets, and
changes nothing (if your config sets another `db_path`, use that file).
`sqlite3` is already a prerequisite of `install.sh`. This works in a
store that has no `ad.launch.name_held` record; where one exists, its
`store_id` is the same value. No verb changes the id. A store taken back to
schema v4, by the downgrade recipe or by restoring a copy from before the
install, gets a new id when it is migrated again; trail records written
before then carry the old one.

**Checking a label.** A valid `@ad_owner` label has five fields: `ad1`, a
launch token, the session's own id (`$N`), the row's id and, last, the
store id. Compare that last field with the id above. A label whose last
field is a different id belongs to another agent-director store's agent,
even when it names this row's id: never end it from this store (see "A
session of another agent-director store"). Every item below that checks a
label applies this rule.

## A finished row's own old session

`resume`, or a `spawn` with `--reuse-finished`, refuses with
`ErrTmuxSessionConflict` in two cases this item covers. "This row's own
id": the row is `ended` or `missing`, but its own session still runs past
the stopping window and the starting-session bound, or no session of it is
found while its agent process still runs. "This id's own abandoned
launch": the row's latest launch recorded no session of its own, and a
session agent-director launched earlier for this id, which the row does not
track, has run past the starting-session bound. If, after looking at it
(steps 1 to 3 of the leftover item below), you want that session or agent
gone, end it with the operator tool's `kill-finished`:

```sh
~/.agent-director/admin/agent-director-admin kill-finished --claude-instance-id <id>
```

It ends the agent's pane and the row's labelled session, then waits for the
agent process (or, when the process cannot be checked, looks the session up
once more), and succeeds with `kill_sent` true once the agent process is
gone, otherwise `ErrTmuxKillFailed` (retry later; never `delete` the row).
When that second lookup cannot answer, it returns `ErrTmuxUnresponsive` or
`ErrTmuxNotAvailable` with `kill_sent` true: the kill was sent and may or
may not have taken effect, so check again later.
The row's state and every other field stay unchanged, so the
conversation stays resumable: run the refused `resume` (or the
`spawn --reuse-finished`) again.

It ends the row's own session only if that session reported in to the
row: the row records the agent's process id (the agent's SessionStart reached
agent-director for the row's latest launch) and the session was created, in
whole seconds, before the row finished. A session created in the same
second, a row with no finish time, and an agent whose SessionStart could not
record its process are refused too. A row `kill-finished` acts on was finished by
its own agent, by `find-missing`, a restore, a plain spawn's end write or a
hand edit, never by a leftover or a nested `claude` carrying the id:
agent-director ignores hooks that do not come from the row's own agent, so a
working agent's row stays live and gets the live-row refusal below.

This id's own abandoned launch never reports in, so that rule does not
apply to it. When the row records a launch token but no tmux server or pane
of that launch, `kill-finished` ends every session that carries an earlier
launch's label of the row's id in this store, once the youngest of them has
run for at least the starting-session bound. For each session, lowest `$N`
first, it ends the pane that launch created (the one labelled with that
launch), then the session by its id. It then waits for the youngest
session's agent process, the other processes of those sessions' panes and
each older session's agent process, even one whose pane was moved into
another session. When the youngest session's agent process cannot be
checked, it first looks the sessions up once more and, once none of them
is listed, waits for those other processes. Its answers mean what they
mean above, and the row is never changed.

Every other answer sends no kill and changes nothing:

- `ErrSpawnNotResumable` ("the row is live"): the row is live (`pending`
  included); no lookup was made. To end a live agent, or to abort a launch
  stuck at a startup prompt, run `agent-director kill --claude-instance-id <id>`
  instead.
- `ErrInternal`: the row's recorded tmux session name cannot be used; no
  lookup and no tmux call were made. See "A row whose recorded name cannot
  be used".
- `ErrTmuxUnresponsive` ("appears to still be stopping" or "appears to
  still be starting"): the row ended less than the stopping window ago, or
  the session (for this id's own abandoned launch, its youngest session) is
  younger than the starting-session bound (the configured
  `stopping_window_seconds` and `starting_session_seconds`). Wait and run
  it again.
- `ErrTmuxSessionConflict` ("never reported in"): the row's own session
  past both, or a leftover of an earlier launch other than this id's own
  abandoned launch (the error names its session
  and its id, `$N`), that never reported in to the row. `send-keys` refuses
  a finished row, so ending the session is your decision: check its
  ownership and end it by hand by its session id, as steps 1 to 4 of
  "A leftover, or a session with no valid label, or one that never reported in on a finished row"
  below describe, then run the refused `resume` (or the
  `spawn --reuse-finished`) again.
- `ErrTmuxSessionConflict` ("conflicting labels"): see "A stray `@ad_owner`
  value or a duplicate label".
- `ErrTmuxNotAvailable` before any kill: tmux could not be
  run or its socket is not accessible, or the socket reaches another server
  ("not the tmux server the agent was launched on"; see "A row on a
  different tmux server").
- `ErrTmuxUnresponsive` otherwise, before any kill: tmux did not answer
  usably; retry later.

If no session carries the row's current label, it acts as on a live row:
success with `kill_sent` false unless the agent process still runs; a
surviving agent pane is ended and the process waited for; otherwise
`ErrTmuxKillFailed` (see "An agent process that runs with no session or
pane of its launch", which also covers a process that outlives the kill).

`kill_sent` is true exactly when a pane or session kill was sent. The
`ad.kill.called` trail record of a `kill-finished` run carries
`include_finished` true and `caller_process` `agent-director-admin`; a
refusal records `kill_sent` false, except an error that follows a sent
kill:

```sh
jq -c 'select(.event == "ad.kill.called" and .claude_instance_id == "<id>") | {include_finished, kill_sent, lookup_outcome, outcome}' ~/.agent-director/ad-trail.jsonl | tail -n 1
```

Only `kill-finished` ends a finished row's session or this id's own
abandoned launch: the `agent-director`
CLI, its MCP tools and the Go and TypeScript clients have no way to, and
their `kill` on a finished row is the no-op success. `kill-finished` exists
on `agent-director-admin` from 0.11.0, the release that first ships the
operator tool. Check with
`~/.agent-director/admin/agent-director-admin version`; a release
candidate `0.11.0-rc.N` sorts before 0.11.0 but counts as 0.11.0. Every
development build of the operator tool (`0.0.0-dev` from `make`, `dev`
from a plain `go build`) has it.

Accepted risks: on a row wrongly marked `missing` (a hand edit of the
store, or an agent-director process from before the install) it ends a
healthy agent whose row says finished; the conversation stays resumable,
and you chose it. On this id's own abandoned launch it ends sessions that
never reported in, judged only by their label and age, so it may end a
`claude` that started and is working for this id untracked: look at each
session first. The checks above fail closed, so some sessions you may
want gone are refused and left to you. A live row is always refused.
`agent-director-admin` is off PATH, not locked: agents run as your user,
so an agent that learns of it, for example from this page, could run it
by its full path; nothing but its human-approval rule stops it.

## A leftover, or a session with no valid label, or one that never reported in on a finished row

A leftover is a session of an earlier launch of the agent: its label names
the row's id with another launch token. Whether the row is `pending` or
live, `agent-director kill` refuses it with `ErrTmuxSessionConflict` ("not
this launch's session"), sends nothing and names the session's id (`$N`).
While the current launch's session runs, `read-pane` of the id shows the
agent's pane. With no session of the current launch, it shows the
leftover's pane if there is one leftover (not the agent's), and answers
`ErrTmuxSessionConflict` if there are several. The leftover never keeps the
row live: its hooks change nothing on the row, and `find-missing` judges
the row by its own agent's process. On an `ended` or `missing` row,
`agent-director-admin kill-finished` answers a leftover (other than this
id's own abandoned launch, which it ends; see "A finished row's own old
session"), or the row's own session that never reported in to it, with
`ErrTmuxSessionConflict` ("never reported in") and sends no kill; these
steps end such a session by hand. A session with no valid label may be a
person's own, so look before acting.

1. Find the session and note its `session_created`:

   ```sh
   tmux -u -S '<socket>' list-sessions -F '#{session_id} #{session_created} #{session_name}'
   ```

2. Check its label:

   ```sh
   tmux -u -S '<socket>' show-options -t '<session id>' -v @ad_owner
   ```

   Read it as [This store's id](#this-stores-id) says: its last field must
   be this store's id (the `store_id` of an `ad.launch.name_held` record),
   as in every leftover `kill` names. A session with no label prints
   nothing
   (`invalid option: @ad_owner` on tmux 3.3a); an invalid label prints
   something malformed. `no such session` means it has gone.
3. Look at it read-only (detach with the tmux prefix key, then `d`):

   ```sh
   tmux -u -S '<socket>' attach-session -r -t '<session id>'
   ```

4. If it is not wanted, end it by its session id, never by its name (a name
   target can match another session whose name begins with it):

   ```sh
   tmux -u -S '<socket>' kill-session -t '<session id>'
   ```

   If its window is shared with another session (a grouped session or a
   linked window), ending the session leaves the program in its pane
   running: end the pane by its id instead, taken from the listing:

   ```sh
   tmux -u -S '<socket>' list-panes -a -F '#{pane_id} #{pane_pid} #{session_id} #{session_name}'
   tmux -u -S '<socket>' kill-pane -t '<pane id>'
   ```

5. `agent-director find-missing` marks the row once its agent process is
   gone. Then resume the id (if the row has a session id and the
   conversation is wanted) or spawn it again with `--reuse-finished`.

On a `pending` row, first wait until `find-missing` marks the row `missing`
(after the pending grace period), then, before step 4, list the sessions
again and check that the session id still shows the `session_created` you
noted; afterwards spawn the id with `--reuse-finished`.

When `resume`, or a `spawn` with `--reuse-finished`, refuses with
`ErrTmuxSessionConflict` ("left over from an earlier life", "this id's own
abandoned launch" or "no valid instance id"), the row stays `ended` or
`missing` (after "duplicate session" the error says whether the row was
restored). The error names the session and its id (`$N`); the socket is the
row's `tmux_socket`. For a leftover, handle each session it names as in
steps 2 to 4 (if it says "and N more", find the others with the listing of
step 1). "This id's own abandoned launch" is a session of an earlier
launch of this id that the row does not track: look at each session it
names (steps 2 and 3) and, if they are not wanted, end them all with
`kill-finished` as "A finished row's own old session" describes (it
refuses a `pending` row, so first wait until `get` shows the row `ended`
or `missing`). For "no valid instance id", look first (steps 2 and 3): it
may be a person's own session; end it as in step 4 only if it is not
wanted. Then
run the refused command again, whichever it was (if the error said the
row was not restored, only once `get` shows it `ended` or `missing`):

```sh
# if resume was refused
agent-director resume --claude-instance-id <id>
# if the spawn with --reuse-finished was refused
agent-director spawn --cwd <dir> --claude-instance-id <id> --reuse-finished
```

## A row whose recorded name cannot be used

A row's recorded tmux session name cannot be used when it is empty, holds a
control character, or holds `.`, `:` or bytes that are not valid UTF-8.
This includes a default name made by releases before 2026-05-27, which kept a `.`
from its id: the id `b.18k-fix` gave `<folder>-b.18k-fi`. agent-director
never touches such a row's session: every verb that would look the row up
returns `ErrInternal` with no tmux call, `find-missing` leaves a live row
unverified with a note of its own when the row's process cannot decide it,
and `expire` keeps a finished row on every run. A human removes such a row,
working as the agents' user and against their tmux server, in this order:

1. Identify the row and its recorded name. On a live row,
   `agent-director list` shows the `liveness_note` `tmux_session_name_empty`,
   `tmux_session_name_control_char` or `tmux_session_name_rewritten`. A
   finished row is in `expire`'s `kept_ids` on every run, and its
   `ad.expire.kept` trail record gives the reason `empty_session_name`,
   `control_char_session_name` or `rewritten_session_name`:

   ```sh
   jq -c 'select(.event == "ad.expire.kept" and .claude_instance_id == "<id>") | {reason, tmux_session_name}' ~/.agent-director/ad-trail.jsonl | tail -n 1
   ```

   Every verb that would look the row up answers `ErrInternal`, and `list`
   shows every row's recorded name (`tmux_session_name`). For the exact
   name, use `read-pane`, which changes nothing:

   ```sh
   agent-director read-pane --claude-instance-id <id>
   ```

   Its `ErrInternal` quotes the recorded name, writing each control
   character and each byte that is not valid UTF-8 as an escape. `list`,
   `get` and the `ad.expire.kept` record cannot show such a byte: their JSON
   replaces it.
2. Work out the name tmux stores and find the candidate sessions. In the
   stored form, `.` and `:` become `_`, a byte that is not valid UTF-8
   becomes a backslash and three octal digits (byte 0xff becomes `\377`),
   and a control character becomes its escape (tab `\t`, newline `\n`, ESC
   `\033`). A `\` of the name is stored as `\\`, and a `$` followed by a
   letter, `_` or `{` as `\$`, so `mix.$b` is stored as `mix_\$b` (tmux 3.2
   and 3.3; other versions escape `$` differently, so compare by eye). List
   the sessions and note the session id (`$N`) of each one whose name equals
   the stored form:

   ```sh
   tmux -u -S '<socket>' list-sessions -F '#{session_id} #{session_created} #{session_name}'
   ```

   An empty name has no stored form, so every listed session is a
   candidate.
3. Confirm ownership by hand. Such a row's session usually carries no
   label. For each candidate, check its label:

   ```sh
   tmux -u -S '<socket>' show-options -t '<session id>' -v @ad_owner
   ```

   If it prints nothing, or `invalid option: @ad_owner` (tmux 3.3a), the
   session has no label.

   A label marks the session as this row's only if it is a valid label (see
   [This store's id](#this-stores-id)) whose session id is the `$N` you
   passed to `-t`, whose row id is this row's and whose last field is this
   store's id (the `store_id` of an `ad.launch.name_held` record). For a
   session with no label, its
   environment is a hint only, because environments are inherited:

   ```sh
   tmux -u -S '<socket>' show-environment -t '<session id>' AGENT_DIRECTOR_INSTANCE_ID
   ```

   Only a session that prints exactly `AGENT_DIRECTOR_INSTANCE_ID=` followed
   by this row's id may be this row's. A session that names another id is
   another row's agent, whose own valid name can equal the stored form (for
   `<folder>-b.18k-fi`, another row's `<folder>-b_18k-fi`): never end it.
   Handle a session with no valid id as "A leftover, or a session with no
   valid label, or one that never reported in on a finished row" says.
4. If this row's session runs, end it by its session id from the listing,
   keeping the quotes, then list again and check that the id is gone:

   ```sh
   tmux -u -S '<socket>' kill-session -t '<session id>'
   tmux -u -S '<socket>' list-sessions -F '#{session_id} #{session_created} #{session_name}'
   ```

   If the session you ended was the server's last one, the second listing
   prints `no server running on <socket>` instead; this also means the
   session is gone.

   Never target it by name, not even with tmux's exact-match `=` prefix: the
   recorded name is not the name tmux holds, tmux reads `.` and `:` in a
   target as window and pane separators, and the stored form can be another
   session's name.
5. Remove the row with the operator tool's `delete`:

   ```sh
   ~/.agent-director/admin/agent-director-admin delete --claude-instance-id <id>
   ```

   This also removes the row's permission requests and session history,
   and clears it as the parent of any other row; it touches no tmux session
   and no transcript. It is the only way to remove such a row, because
   `resume` and reuse refuse it and `expire` keeps it.

## A spawn refused as "left over from an earlier life"

`ErrTmuxSessionConflict` ("left over from an earlier life") from a plain
`spawn` (without `--reuse-finished`; a refused reuse is handled as in the
leftover item above) comes in two cases:

- A `spawn` with a `--claude-instance-id` that has no row, when a tmux
  session of this agent-director store still carries that id. Nothing was
  written. The error names each such session by name and session id.
- A `spawn` whose `--tmux-session-name` is held by such a session. The
  error names the session id and says "the new row was ended": the spawn
  has already ended its row.

The spawn's trail record gives the socket, this store's id, the session's
id and its creation time (for the second case also `row_result`, and the
record's `attach_command` and `end_command` are the commands of steps 3
and 4 of "A leftover, or a session with no valid label, or one that never
reported in on a finished row"):

```sh
jq -c 'select(.event == "ad.launch.name_held" and .claude_instance_id == "<id>") | {tmux_socket, store_id, tmux_session_id, session_created, row_result}' ~/.agent-director/ad-trail.jsonl | tail -n 1
```

In the second case the row the spawn ended records no session of its own
launch, so the leftover is this id's own abandoned launch. A
`--reuse-finished` spawn of the id made while the leftover still runs is
refused with "this id's own abandoned launch": `ErrTmuxUnresponsive`
("appears to still be starting") while the leftover is younger than the
starting-session bound (`starting_session_seconds`),
`ErrTmuxSessionConflict` after. `kill-finished` answers the same way:
"appears to still be starting", sending no kill, while the leftover is
younger than the bound, and past it, it ends the leftover (see "A finished
row's own old session").

Look at each session first (steps 2 and 3 of "A leftover, or a session
with no valid label, or one that never reported in on a finished row";
this store's id is the record's `store_id`, the same id
[This store's id](#this-stores-id) prints). In the first case no row
exists: end each session by hand as in step 4 of that item, then spawn the
id again; no reuse opt-in is needed. In the second the row is `ended`: end
the leftover with `kill-finished` once it is past the bound (or by hand as
in step 4), then spawn with `--reuse-finished`:

```sh
agent-director spawn --cwd <dir> --tmux-session-name <name> --claude-instance-id <id> --reuse-finished
```

If you leave the row `ended`, `expire` keeps it while the leftover runs
(reason `leftover_running`, listed in `kept_ids`); its first run after the
leftover is gone deletes the row, once the row is older than the retention
window.

If the error instead says the new row "stays pending" or "was left as it
is", check the row with `agent-director get --claude-instance-id <id>`; if
it is `pending`, follow the `pending` paragraph of the leftover item above
before spawning with `--reuse-finished`.

In the first case, if the error says "and N more", find the others with the
listing of step 1 of "A leftover, or a session with no valid label, or one
that never reported in on a finished row". The check before a spawn sees only this socket: it misses
leftovers on another tmux server or socket, and sessions with no label.

When a held name's error says "no valid instance id", handle the session
as in the leftover item (look first: it may be a person's own session).
When it says "a different instance id", the session is another row's agent:
do not end it; give this spawn a different `--tmux-session-name`. The
spawn has already ended its own row, so spawning the same
`--claude-instance-id` again also needs `--reuse-finished`.

## A session of another agent-director store

`ErrTmuxSessionConflict` ("another agent-director store"), or a label whose
last field is not this store's id (see [This store's id](#this-stores-id);
the `store_id` of an `ad.launch.name_held` record is the same value):
another store on the same tmux server (a
test sandbox, a second `HOME`, a CI container using the host's socket) owns
the session. Never end it from this store. Find which store it belongs to
(its `HOME`) and stop or move that store's agents with that store's own
agent-director, or give this store's agent a different session name:

```sh
agent-director spawn --cwd <dir> --tmux-session-name <other name> --claude-instance-id <id> --reuse-finished
```

## An agent process that runs with no session or pane of its launch

`agent-director kill` returns `ErrTmuxKillFailed` saying that no session or
pane of this launch was found while the agent process still runs, and that
no kill was sent. `read-pane` answers `ErrTmuxCaptureFailed` although the
agent runs (a gone answer about the launch's session, not proof that the
agent has exited). `find-missing` keeps a live row live, because its
process is alive, and `expire` keeps a finished one (`process_alive`).
agent-director never signals a process itself; ending it
is a human's decision:

1. Take the pid from the error, or from the trail (`agent_pid`; for a
   survivor, `survivor_pids`):

   ```sh
   jq -c 'select(.event == "ad.kill.called" and .claude_instance_id == "<id>") | {agent_pid, survivor_pids, outcome}' ~/.agent-director/ad-trail.jsonl | tail -n 1
   ```

2. Confirm it is the agent, a Claude Code process started when the launch
   began:

   ```sh
   ps -o pid,lstart,args -p '<pid>'
   ```

3. Look for its pane (also on any other tmux server you know of), look at
   its session read-only as above, and if it is not wanted end the pane by
   its id:

   ```sh
   tmux -u -S '<socket>' list-panes -a -F '#{pane_id} #{pane_pid} #{session_id} #{session_name}'
   tmux -u -S '<socket>' kill-pane -t '<pane id>'
   ```

4. If it is in no pane, run `ps` again to check that the pid still has the
   same start time, then end it:

   ```sh
   kill '<pid>'
   ```

5. For the agent process: run
   `agent-director kill --claude-instance-id <id>` again (it now succeeds),
   or wait for `find-missing` to mark the row `missing`, then resume or
   respawn the id as the [caller contract](caller-contract.md) says. For a
   survivor named by `survivor_pids`: end it as in step 4. A later `kill`
   does not check it again, so its success says nothing about the survivor.

The same procedure applies to a survivor that `kill` names after its wait:
a process of a pane of the agent's session that outlived the pane kill and
the session kill (for example one that ignores SIGHUP). Its pid is in the
error's description and in `ad.kill.called`'s `survivor_pids`.

## The agent's pane was not found

`ErrTmuxSessionConflict` ("the agent's pane was not found") from
`read-pane`, `send-keys` or `pause`: nothing was read or sent. The row's
current launch still has its labelled session, but the pane it recorded is
not there with the agent's pid. Either the agent's program ended while the
session stayed (another window keeps it open, or `remain-on-exit` is on),
or the pane was respawned with another program. If the error also says the
pane was not adopted, see the next item.

1. Find the labelled session and read its label (steps 1 and 2 of the
   leftover item) for the session the error quotes, and for others if it
   was renamed. The agent's session prints a valid label with this row's id
   and this store's id (see [This store's id](#this-stores-id)).
2. Look at what runs in it, and look read-only as above:

   ```sh
   tmux -u -S '<socket>' list-panes -s -t '<session id>' -F '#{pane_id} #{pane_pid} #{pane_current_command} #{pane_dead}'
   ```

3. Decide:
   - Nothing of value runs there (the agent's program has ended, a dead
     pane, an idle shell): end the session by its session id (step 4 of
     the leftover item). `find-missing` marks the row once its process is
     gone; then resume or reuse the id.
   - Another program that someone uses runs there: leave the session and
     only remove its claim to the launch, then tell its owner.
     `find-missing` then settles the row by its process. Never end a
     session someone uses without asking:

     ```sh
     tmux -u -S '<socket>' set-option -t '<session id>' -u @ad_owner
     ```

   - The agent still runs in another pane of that session (its pid, from
     `ps`, matches the row's SessionStart pid): leave it. Only the pane
     verbs cannot reach it; `agent-director kill` still ends the session.

## The agent's pane was not adopted after a lost create reply

When the reply to the call that created a launch's session was lost,
agent-director has not recorded the agent's pane, so the agent's hooks do
not apply to the row, which can stay `pending` although its agent works.
Once the grace period has passed, `find-missing` adopts the agent's pane,
notes the row `unreported` when the agent runs, and the hooks apply from
then on. Each created pane carries the pane label
`@ad_pane`, `<token> <pane id>`.

1. Find the labelled session and read its label (steps 1 and 2 of the
   leftover item); its second field is this launch's token. Then list its
   panes with their pane labels, and look read-only as above:

   ```sh
   tmux -u -S '<socket>' list-panes -s -t '<session id>' -F '#{pane_id} #{pane_pid} #{pane_current_command} #{@ad_pane}'
   ```

   The agent's pane is the one whose value starts with this launch's token
   and ends with its own pane id.
2. A wanted agent can be left running: `find-missing` adopts its pane, and
   so does any `kill`, `send-keys` or `pause` of the row (while tmux cannot
   answer, or two panes carry the token, the row stays unverified). If no pane carries the token, `find-missing` marks the row
   `missing` although the agent may still run. To end the session while the
   row is live, use `agent-director kill --claude-instance-id <id>`, not a
   hand kill: it is this launch's session. `kill` adopts the labelled pane,
   kills the session and waits for every pane process of it. Once the row
   is `missing`, end the session by its session id (steps 3 and 4 of the
   leftover item). Then resume or reuse the id, so that the next launch
   records its pane.

## A `pending` row with no launch start or token

`ErrSpawnNotInteractive` from `send-keys` saying the row's launch start or
launch token is "not recorded": nothing was sent and no tmux call was made.
Only a row that was `pending` when agent-director was upgraded, one written
by an agent-director process started before the install and not restarted,
or a hand edit can be like this. agent-director never acts on any session
for such a row.

1. Look at the row (state `pending`, no `launch_started_at`), and look in
   `ps` for an `agent-director` process (an MCP `serve` included) started
   before the install; restart it:

   ```sh
   agent-director get --claude-instance-id <id>
   ```

2. Look for a session of this agent on the socket (for a row from before
   the install, the socket of your tmux environment): the session with the
   row's recorded name (step 1 of the leftover item). Read its label (step
   2; a session from before the install has none) and, as a hint only, its
   environment, then look at it read-only as above:

   ```sh
   tmux -u -S '<socket>' show-environment -t '<session id>' AGENT_DIRECTOR_INSTANCE_ID
   ```

3. Decide. An agent from before the install that is not wanted: end its
   session by its session id (step 4 of the leftover item), because
   `agent-director kill` never ends a session for such a row. An agent that
   is wanted: leave it running until it can be stopped and started again
   properly. A session that belongs to someone else, or shows no sign of
   this agent: leave it alone. `find-missing` marks the row `missing` at
   once when no recorded process of it runs and no session carries its
   current label; then resume or reuse the id.

## Stopping a set of agents before a binary change

Every agent is stopped before agent-director's binary changes. When the
caller's own stop path cannot run, a human stops one labelled set of agents
(for example the label `service=<name>`) in this order. Never delete,
hand-edit or migrate `state.db` to stop agents, and never delete a row.

1. Stop whatever starts agents first: the caller's service, its schedulers
   and its cron jobs. Otherwise they relaunch what you stop.
2. Use a binary that opens the store. List the set with the installed
   binary:

   ```sh
   agent-director list --label <key>=<value>
   ```

   If it refuses with `ErrSchemaMismatch`, the store is newer than the
   binary: use the binary that wrote the store. If it refuses with
   `ErrSchemaMigrationRequired`, the store is older: use the previous
   binary. Keep a copy of the previous binary before any install, because
   a rollback needs it too. If it refuses with `ErrConfigMalformed`, first
   fix the config value it names.
3. Note every row of the set in a live state (`pending`, `waiting`,
   `working`, `ask_user` or `check_permission`).
4. Only with a binary from before this release (0.11.0): its `kill`,
   `pause` and `send-keys` find a session by name with tmux's prefix match,
   and its `kill` reports success when tmux failed. For each live row, look
   for another session whose name begins with the row's name (such rows
   record no socket, so use the socket of your tmux environment, by default
   `/tmp/tmux-<uid>/default`):

   ```sh
   tmux -u -S '<socket>' list-sessions -F '#{session_id} #{session_name}'
   ```

   Stop such a row by hand (step 6) instead.
5. Stop each live row. `pause` asks the agent to exit and waits until the
   row reads `ended`:

   ```sh
   agent-director pause --claude-instance-id <id>
   ```

   `pause` stops only a `waiting` row. For every other live row, and for
   any row `pause` refuses (a row this binary cannot reach, for example) or
   that does not end in time, use `agent-director kill` and steps 1 to 5 of
   the live-row sequence of the [caller contract](caller-contract.md), which
   `kill`'s description also gives. With a binary from before this release,
   confirm each result with `agent-director status --claude-instance-id
   <id>` and the session listing. Never delete a row.
6. Handle anything still running by the items of this section: "An agent
   process that runs with no session or pane of its launch", "A leftover,
   or a session with no valid label, or one that never reported in on a
   finished row" and "A `pending` row with no launch start or token". With
   this release's binary, the last covers every row from before the
   install.
7. Confirm that the listing shows no live row of the set and that no
   session of the set remains:

   ```sh
   agent-director list --label <key>=<value>
   tmux -u -S '<socket>' list-sessions -F '#{session_id} #{session_created} #{session_name}'
   ```

   Then stop every other long-running agent-director process, every
   `agent-director serve` included, and change the binary.

## agent-director was installed outside the caller's switch-over

This release was installed, and the store migrated, before the caller's
version for it was deployed. Agents and long-running agent-director
processes from before the install may still run, and the caller's old
version cannot run against the migrated store. Stop the caller and whatever
starts agents (step 1 of "Stopping a set of agents before a binary
change"), then choose a direction.

**Forward**, preferred when the caller's version for this release can be
deployed now; it keeps every write since the install:

1. Find the rows from before the install: `get` shows no `tmux_socket` and
   no `launch_started_at` for them.

   ```sh
   agent-director list --label <key>=<value>
   agent-director get --claude-instance-id <id>
   ```

2. End each live one's agent by hand by its session id, as "A `pending`
   row with no launch start or token" says. This release's `kill` and
   `pause` never end such a row's session: it has no label to prove which
   session is its own.
3. Restart every long-running agent-director process started before the
   install, every `agent-director serve` included.
4. Run `agent-director find-missing`. It marks each such row `missing` once
   its process is gone.
5. Deploy the caller's version, which brings its agents back by `resume` or
   by a spawn with `--reuse-finished`.

Never delete those rows: their history is what `resume` uses.

**Back**, when the caller's version cannot be deployed:

1. Stop every agent of the set as "Stopping a set of agents before a binary
   change" says, with this release's binary, the only one that opens the
   migrated store. End the rows from before the install by hand, as in
   Forward step 2.
2. Stop every long-running agent-director process.
3. Restore the store, then the previous binary. For the store, either put
   back the copy of `state.db` (with its `-wal` and `-shm` files) taken
   before the install and delete any `state.db-wal` or `state.db-shm` the
   copy does not include, which loses every write since the install, or run
   the downgrade recipes down to the previous binary's schema, newest first
   ([v6 → v5](migration-guide.md#v6--v5-reverses-migratev5tov6) for
   0.11.x, then
   [v5 → v4](migration-guide.md#v5--v4-reverses-migratev4tov5) for an
   older release), which keep the rows but lose the values of the columns
   the migrations added.
4. Start the caller's old version.
5. Later, follow the caller's switch-over runbook from its start.

## A row stays `pending` and the trail shows `no_exec_form`

The agent runs a Claude Code that does not run exec-form hooks, so none
of its hooks apply; the supported minimum is 2.1.280. List the records:

```sh
jq -c 'select(.event == "ad.hook.ignored" and .reason == "no_exec_form") | {ts, claude_instance_id, hook_event, parent_command}' ~/.agent-director/ad-trail.jsonl | tail -n 5
```

Upgrade `claude` on PATH to 2.1.280 or later. Then end and relaunch each
row that stays `pending` as the [caller contract](caller-contract.md) says.

## A row stays `pending` and the trail shows `pid_mismatch`

A hook moves a row only when its parent process is the row's recorded pane
process. When the `claude` on PATH is a launcher or shim that runs Claude
Code as a child instead of `exec`ing it, the pane process is the launcher,
so every hook of every agent is ignored and rows stay `pending`. List the
records:

```sh
jq -c 'select(.event == "ad.hook.ignored" and .reason == "pid_mismatch") | {ts, claude_instance_id, hook_event, parent_pid, parent_command, row_pane_pid}' ~/.agent-director/ad-trail.jsonl | tail -n 5
```

`parent_command` is the command name of the process that fired the hook.
A few such records are expected and need no action: a nested `claude`, a
teammate pane or a leftover session carrying the id is never the row's own
agent. When the pane process started that process as a child, each refused
SessionStart of a `pending` row also writes `ad.hook.pane_is_grandparent`:

```sh
jq -c 'select(.event == "ad.hook.pane_is_grandparent") | {ts, claude_instance_id, pane_command, parent_command, advice}' ~/.agent-director/ad-trail.jsonl | tail -n 5
```

When its `parent_command` is Claude Code, the `claude` on PATH is a
launcher: replace it with Claude Code's native binary or a wrapper that
`exec`s it (see [Prerequisites](install.md#requirements)), then end and relaunch
each row that stays `pending` as the [caller contract](caller-contract.md)
says. When it is a shell, the hook ran through a shell instead of in exec
form.

## A row on a different tmux server

`ErrTmuxNotAvailable` ("not the tmux server the agent was launched on";
the trail's `lookup_outcome` is `different_server`; when `find-missing`
cannot check the agent's process it leaves the row unverified with the
`liveness_note` `tmux_server_changed`): the
server the row
recorded still runs, but its socket now reaches another server (for
example the old server's socket file was removed and a new server started
there), or the server's identity cannot be checked. Find the old server's process and
decide; `find-missing` settles the row once the old server process is gone.

## A stray `@ad_owner` value or a duplicate label

`ErrTmuxSessionConflict` ("conflicting labels"). agent-director never sets
`@ad_owner` at these scopes. Look:

```sh
tmux -u -S '<socket>' show-options -g -v @ad_owner
tmux -u -S '<socket>' show-options -s -v @ad_owner
tmux -u -S '<socket>' show-options -gw -v @ad_owner
```

Remove a stray value with the matching form:

```sh
tmux -u -S '<socket>' set-option -g -u @ad_owner
tmux -u -S '<socket>' set-option -s -u @ad_owner
tmux -u -S '<socket>' set-option -gw -u @ad_owner
```

For two sessions with the same current label, look at both read-only and
end the one that is not wanted by its session id, as for a leftover.
