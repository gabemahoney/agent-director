# Operator actions

Human-only procedures. Automated callers (programs, scripts, agents, MCP
clients) must not perform them.

`kill-finished` and `delete` are verbs of `agent-director-admin`
(`~/.config/agent-director/admin/agent-director-admin`, never on PATH). Run it by that
full path, only with a human's explicit approval for that run.

Run every command as the agents' user. Every tmux command names the row's socket
with `-S '<socket>'`: the `tmux_socket` of
`agent-director get --instance-id <id>` (none: your tmux socket, by
default `/tmp/tmux-<uid>/default`). Keep the quotes. Never remove a row after a
`kill` that did not succeed.

## This store's id

A session's `@ad_owner` label has five fields: `ad1`, a launch token, the
session's own id (`$N`), the row's id and the store id (the trail's `store_id`).
Read this store's id (16 hexadecimal characters), with `<busy_timeout_ms>` set
to your `[store] busy_timeout_ms` ([Configuration](configuration.md)); with
another `db_path`, use that file:

```sh
sqlite3 -readonly -batch -init /dev/null -cmd ".timeout <busy_timeout_ms>" ~/.config/agent-director/state.db "SELECT value FROM store_meta WHERE key = 'store_id'"
```

## Inspect and end a session by hand

Other items cite these steps by number. A session with no valid label may be a
person's own: look before ending it. Step 2 checks the label's last field
against [This store's id](#this-stores-id).

```sh
# 1. List sessions; note the wanted one's id ($N) and session_created.
tmux -u -S '<socket>' list-sessions -F '#{session_id} #{session_created} #{session_name}'
# 2. Read its label and check it (none: empty, or `invalid option: @ad_owner` on tmux 3.3a).
tmux -u -S '<socket>' show-options -t '<session id>' -v @ad_owner
# 2b. No label: its environment is a hint only.
tmux -u -S '<socket>' show-environment -t '<session id>' AGENT_DIRECTOR_INSTANCE_ID
# 3. Look read-only (detach with the tmux prefix key, then d).
tmux -u -S '<socket>' attach-session -r -t '<session id>'
# 4. If not wanted, end it by session id, never by name.
tmux -u -S '<socket>' kill-session -t '<session id>'
# 4b. A window shared with another session keeps running: end its pane by id (-s -t '<session id>' lists one session's).
tmux -u -S '<socket>' list-panes -a -F '#{pane_id} #{pane_pid} #{session_id} #{session_name} #{pane_current_command} #{pane_dead} #{@ad_pane}'
tmux -u -S '<socket>' kill-pane -t '<pane id>'
# 5. List again (step 1): the id is gone (`no such session` or `no server running` also mean gone).
```

## A finished row's own old session

`resume` or `spawn --reuse-finished` refuses with `ErrTmuxSessionConflict`
because the `ended` or `missing` row's own session, or an untracked earlier
launch's session of its id ("this id's own abandoned launch"), still runs. Look
(steps 1 to 3), then end it:

```sh
~/.config/agent-director/admin/agent-director-admin kill-finished --instance-id <id>
```

| Answer | What to do |
|---|---|
| Success (`kill_sent` true; false: nothing of the row ran) | Run the refused command again. |
| `ErrTmuxKillFailed` | The agent still runs: retry later; never `delete` the row. No session of the current launch: see "An agent process that runs with no session or pane of its launch". |
| `ErrTmuxUnresponsive` ("still stopping" or "still starting"), or `ErrTmuxNotAvailable` or `ErrTmuxUnresponsive` with `kill_sent` true | Wait, check, run it again. |
| `ErrTmuxUnresponsive` before any kill | Retry later. |
| `ErrSpawnNotResumable` ("the row is live") | `agent-director kill --instance-id <id>`. |
| `ErrTmuxSessionConflict` ("never reported in") | End the session by hand (steps 1 to 5), then rerun. |
| `ErrInternal` | See "A row whose recorded name cannot be used". |
| `ErrTmuxSessionConflict` ("conflicting labels") | See "A stray `@ad_owner` value or a duplicate label". |
| `ErrTmuxNotAvailable` before any kill | Fix tmux or socket access; for "not the tmux server the agent was launched on" see "A row on a different tmux server". |

## A leftover, or a session with no valid label, or one that never reported in on a finished row

A leftover is a session of an earlier launch (its label names the row's id with
another launch token). `agent-director kill`, `resume` and
`spawn --reuse-finished` refuse it with `ErrTmuxSessionConflict` ("not this
launch's session", "left over from an earlier life", "this id's own abandoned
launch", "no valid instance id"), naming each session by `$N` ("and N more":
use step 1). Inspect each session first (steps 1 to 3), then:

1. A leftover or "no valid instance id" session: if not wanted, end it (steps 4
   and 5); on a `pending` row, first wait until `find-missing` marks it
   `missing` and check the session still shows the `session_created` you noted.
2. "This id's own abandoned launch": `kill-finished` ends them all (not on a
   `pending` row).
3. Rerun the refused command; if the error said the row was not restored, only
   once `get` shows it `ended` or `missing`.

## A row whose recorded name cannot be used

A recorded session name that is empty or holds a control character, `.`, `:` or
invalid UTF-8 cannot be used: verbs that look the row up return `ErrInternal`.
A human removes the row, in order:

1. Identify it: a live row's `list` entry has the `liveness_note`
   `tmux_session_name_empty`, `tmux_session_name_control_char` or
   `tmux_session_name_rewritten`; a finished row is in `expire`'s `kept_ids`
   (`ad.expire.kept` reasons: `empty_session_name`,
   `control_char_session_name`, `rewritten_session_name`). Get its name:

   ```sh
   jq -c 'select(.event == "ad.expire.kept" and .instance_id == "<id>") | {reason, tmux_session_name}' ~/.config/agent-director/ad-trail.jsonl | tail -n 1
   agent-director read-pane --instance-id <id>
   ```

   Its `ErrInternal` quotes the exact recorded name with bytes escaped (`list`,
   `get` and the trail cannot show them).
2. Work out the name tmux stores (the `$` escaping is that of tmux 3.2 and 3.3;
   other versions differ, so compare by eye): `.` and `:` become `_`; an invalid
   byte `\` and three octal digits (0xff is `\377`); a control character its
   escape (`\t`, `\n`, `\033`); `\` becomes `\\`; `$` before a letter, `_` or
   `{` becomes `\$`. List sessions (step 1); note the `$N` of each with that
   name (empty name: every session).
3. Confirm ownership. A candidate is this row's only if its label (step 2) is
   valid with this `$N`, this row's id and [This store's id](#this-stores-id),
   or, with no label, its environment (step 2b) prints exactly
   `AGENT_DIRECTOR_INSTANCE_ID=<this row's id>`. Another id is another row's
   agent: never end it.
4. End this row's session by its session id (steps 4 and 5).
5. Remove the row, its permission requests, its session history and its
   subagent records:

   ```sh
   ~/.config/agent-director/admin/agent-director-admin delete --instance-id <id>
   ```

## A spawn refused as "left over from an earlier life"

`ErrTmuxSessionConflict` ("left over from an earlier life") from a plain
`spawn` (for `--reuse-finished`, see the leftover item): a session of this store
carries the `--instance-id` and no row exists, or holds the
`--tmux-session-name`. The trail gives the details:

```sh
jq -c 'select(.event == "ad.launch.name_held" and .instance_id == "<id>") | {tmux_socket, store_id, tmux_session_id, session_created, row_result}' ~/.config/agent-director/ad-trail.jsonl | tail -n 1
```

Inspect each session (steps 1 to 3), then:

- No row: end each (steps 4 and 5), spawn the id again with the same
  `--agent`.
- Row `ended`: end the leftover with `kill-finished` or by hand once it is older
  than `starting_session_seconds`, then run the same `spawn` again with
  `--reuse-finished` and the row's agent (`get` shows it) as `--agent`.
- "Stays pending", "was left as it is" or "No valid instance id": as the
  leftover item. "A different instance id": another row's agent, and this spawn
  ended its own row: spawn again with `--reuse-finished`, the row's agent
  (`get` shows it) as `--agent`, and another `--tmux-session-name`.

## A session of another agent-director store

`ErrTmuxSessionConflict` ("another agent-director store"), or a label whose last
field is not [This store's id](#this-stores-id): another store owns it. Never
end it from here: stop its agents with that store's agent-director, or use
another `--tmux-session-name`.

## An agent process that runs with no session or pane of its launch

`agent-director kill` returns `ErrTmuxKillFailed` (no kill sent), or `read-pane`
returns `ErrTmuxCaptureFailed`, while the agent process runs. A human decides:

1. Take the pid from the error or the trail (`agent_pid`, `survivor_pids`):

   ```sh
   jq -c 'select(.event == "ad.kill.called" and .instance_id == "<id>") | {agent_pid, survivor_pids, outcome}' ~/.config/agent-director/ad-trail.jsonl | tail -n 1
   ```

2. Confirm it is the agent, a Claude Code process started at launch:
   `ps -o pid,lstart,args -p '<pid>'`.
3. Look for its pane, on any other tmux server you know of too (steps 4b and 3);
   end the pane by id if not wanted. In no pane: recheck its start time, then
   `kill '<pid>'`. Do the same for a survivor.
4. Run `agent-director kill --instance-id <id>` again, or wait for
   `find-missing` to mark the row `missing`, then resume or respawn as the
   [caller contract](caller-contract.md) says.

## The agent's pane was not found

`ErrTmuxSessionConflict` ("the agent's pane was not found") from `read-pane`,
`send-keys` or `pause` (with "not adopted": the next item). Read the quoted
session's label (steps 1 and 2), list its panes (step 4b), look (step 3), then:

- Nothing of value runs there: end the session (steps 4 and 5); `find-missing`
  then marks the row; resume or reuse the id.
- Another program someone uses runs there: leave it, remove only its claim with
  `tmux -u -S '<socket>' set-option -t '<session id>' -u @ad_owner`, and tell
  its owner; `find-missing` then settles the row.
- The agent runs in another pane (its `ps` pid matches the row's): leave it;
  only the pane verbs fail, and `agent-director kill` still ends the session.

## The agent's pane was not adopted after a lost create reply

The row stays `pending` with the agent working and no pane recorded. Read the
session's label (steps 1 and 2): its second field is the launch token. In its
panes (step 4b), the agent's `@ad_pane` is `<token> <pane id>`.

- Wanted agent: leave it; `find-missing` adopts its pane after the grace period.
- No pane carries the token: `find-missing` marks the row `missing` though the
  agent may still run. End the session with `agent-director kill` while the row
  is live, by hand once `missing` (steps 4 and 5), then resume or reuse the id.

## A `pending` row with no launch start or token

`ErrSpawnNotInteractive` from `send-keys` saying the row's launch start or
launch token is "not recorded": a row from before an upgrade, or written by an
agent-director process started before it.

1. Restart any `agent-director` process (`serve` too) from before the upgrade.
2. Find the agent's session by the row's recorded name (step 1); read its
   environment (step 2b) and look (step 3). Unwanted: end it (steps 4 and 5).
   Wanted, or someone else's: leave it; `find-missing` marks the row `missing`
   once nothing of it runs. Then resume or reuse the id.

## Stopping a set of agents before a binary change

When the caller's own stop path cannot run, a human stops one labelled set of
agents (for example `service=<name>`) in this order before agent-director's
binary changes. Never delete a row or edit `state.db`.

1. Stop whatever starts agents (the caller's service, schedulers, cron jobs).
2. List the set: `agent-director list --label <key>=<value>`.
   `ErrSchemaMismatch` (store newer): use the binary that wrote it.
   `ErrSchemaMigrationRequired` (store older): use the previous binary.
   `ErrConfigMalformed`: fix the value it names.
3. `agent-director pause --instance-id <id>` each `waiting` row. For
   every other live row, and any row `pause` refuses (a relay-on row held by
   a permission request, for example) or that does not end in time: `agent-director kill`, then steps 1 to 5 of the live-row sequence in
   the [caller contract](caller-contract.md).
4. Handle what still runs with the items above, then list the set again (step
   2) and the sessions (step 1 of "Inspect and end a session by hand"): no live
   row and no session of the set remains. Stop every `serve`, then change the
   binary, keeping a copy of the previous one.

## agent-director was installed outside the caller's switch-over

Pre-upgrade agents may still run, and the caller's old version cannot use the
migrated store. Stop the caller and whatever starts agents (step 1 of
"Stopping a set of agents before a binary change"), then choose.

**Forward** (preferred; keeps every write since the upgrade):

1. End the agent of each live pre-upgrade row (`get` shows no `tmux_socket` and
   no `launch_started_at`) by hand, as "A `pending` row with no launch start or
   token" says, then run `agent-director find-missing`.
2. Deploy the caller's version; it resumes or reuses those rows (never delete
   them: `resume` uses their history).

**Back** (the caller's version cannot be deployed):

1. With the new binary, stop every agent of the set and every `serve`, as in
   "Stopping a set of agents before a binary change".
2. Put back the pre-upgrade copy of `state.db` (with `-wal` and `-shm`; delete
   any `-wal` or `-shm` the copy lacks; this loses every write since). It is
   the only way back: the migrated store is v9, and there is no v9 → v8
   downgrade recipe (see
   [Emergency downgrade recipe](migration-guide.md#emergency-downgrade-recipe)
   in the migration guide), so the older recipes (v6 → v5 and below) cannot reach it. Install
   the previous binary and start the caller's old version.

## A row stays `pending` and the trail shows `no_exec_form`

Claude Code is too old for exec-form hooks. Upgrade `claude` to the minimum in
[Requirements](install.md#requirements), then end and relaunch each `pending`
row as the [caller contract](caller-contract.md) says.

```sh
jq -c 'select(.event == "ad.hook.ignored" and .reason == "no_exec_form") | {ts, instance_id, hook_event, parent_command}' ~/.config/agent-director/ad-trail.jsonl | tail -n 5
```

## A row stays `pending` and the trail shows `pid_mismatch`

Hooks are ignored when `claude` on PATH is a shim that runs Claude Code as a
child, not by `exec` (the second query's `parent_command` is Claude Code).

```sh
jq -c 'select(.event == "ad.hook.ignored" and .reason == "pid_mismatch") | {ts, instance_id, hook_event, parent_pid, parent_command, row_pane_pid}' ~/.config/agent-director/ad-trail.jsonl | tail -n 5
jq -c 'select(.event == "ad.hook.pane_is_grandparent") | {ts, instance_id, pane_command, parent_command, advice}' ~/.config/agent-director/ad-trail.jsonl | tail -n 5
```

Fix it per [Requirements](install.md#requirements), then end and relaunch each
`pending` row as in the previous item.

## A row on a different tmux server

`ErrTmuxNotAvailable` ("not the tmux server the agent was launched on"), or
the `liveness_note` `tmux_server_changed`: the recorded socket now reaches
another server. Find the old server's process and decide; `find-missing`
settles the row once it is gone.

## A stray `@ad_owner` value or a duplicate label

`ErrTmuxSessionConflict` ("conflicting labels"). For two sessions with one
label, end the unwanted one (steps 1 to 5). For a stray value, look, then remove
it (shown for `-g`; repeat both commands with `-s` and `-gw`):

```sh
tmux -u -S '<socket>' show-options -g -v @ad_owner
tmux -u -S '<socket>' set-option -g -u @ad_owner
```

## A verb refuses with `ErrOldLayout`

Every verb that opens the store (the CLI, `agent-director-admin`, `serve` and
the Go and TypeScript clients) returns `ErrOldLayout` when all of these hold: it
is about to open the default store (no `--store-path` or `StorePath`, and no
`[store] db_path` in `config.toml`), no store exists in
`~/.config/agent-director`, and `~/.agent-director` exists. The refusal creates
nothing. Meanwhile each hook records nothing and exits 0 (a relayed permission
request is denied), and `trail-emit` returns `ErrTrailWrite`. Callers take no
action; see the [caller contract](caller-contract.md).

Check:

```sh
ls -ld ~/.agent-director ~/.config/agent-director
agent-director version
```

The old directory is there and the new one is missing, or has no `state.db`;
`config_dir` in the `version` result is the directory this binary uses. A
symbolic link at `~/.agent-director` that points to `~/.config/agent-director`
is not the old directory.

Fix: run this release's `install.sh` as the agents' user, by any install
path in [Upgrade](install.md#upgrade). It is the only supported way to bring the old
directory forward. Do not create `~/.config/agent-director` or copy files into
it by hand.
