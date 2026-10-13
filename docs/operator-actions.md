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

This phrase means a session left by any earlier launch of this id, including
a launch in the same life. It does not imply that the agent type changed.

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

2. Confirm it is the agent, the Claude Code or Codex process started at
   launch: `ps -o pid,lstart,args -p '<pid>'`.
3. Look for its pane, on any other tmux server you know of too (steps 4b and 3);
   end the pane by id if not wanted. In no pane: recheck its start time, then
   `kill '<pid>'`. Do the same for a survivor.
4. Run `agent-director kill --instance-id <id>` again, or wait for
   `find-missing` to mark the row `missing` (a Codex row whose agent reported
   its end: `ended`), then resume or respawn as the
   [caller contract](caller-contract.md) says.

## A command left running after Esc on a Codex agent

Esc stops a Codex agent's turn, and the row reads `waiting`, while a command the
agent started keeps running (the agent's footer says "1 background terminal
running"). Nothing is needed while the agent runs: `pause` and `kill` end the
agent and the command with it, within seconds. One command outlives the agent:
one that left Codex's session and runs outside its sandbox (the agent started it
detached, for example with `setsid ... &`, after it was approved to run outside
the sandbox). It is reparented to init and runs on after `pause`, `kill` or a
killed agent. A human finds it once the agent is gone:

1. Read the row's folder and times:

   ```sh
   agent-director get --instance-id <id> | jq '{cwd, started_at, ended_at}'
   ```

2. List the agents' user's processes that init now owns (parent pid 1, or the
   user's `systemd --user`, where one runs), with their start times, and look
   for the command you expect:

   ```sh
   ps -u "$(id -un)" -o pid,ppid,lstart,args | awk -v sd="$(pgrep -u "$(id -u)" -x systemd | head -1)" '$2 == 1 || $2 == sd'
   ```

3. For a candidate, confirm it belongs to the row: it started after the row's
   `started_at`, and its folder is the row's `cwd` (on Linux
   `readlink /proc/<pid>/cwd` also prints it; macOS has no `/proc`):

   ```sh
   ps -o pid,lstart,args -p '<pid>'
   lsof -a -p '<pid>' -d cwd -Fn
   ```

4. A process you cannot tie to the row may be someone's own: leave it. Else
   recheck its start time (step 3), then `kill '<pid>'`, and run the `ps` of
   step 3 again: the pid is gone.

## A Codex agent shows its own approval dialog after a relayed verdict

A relayed Codex agent (`relay_mode` on) ignores a verdict and draws Codex's own
approval dialog ("Would you like to run the following command?") when its shell
prints text to stdout before the relay hook runs, while the request reads
`delivered` and the command waits for a person. The cause is a zsh `.zshenv`
that prints, or a file named by a `BASH_ENV` the caller set in `extra_env`
that prints (agent-director launches Codex with `BASH_ENV` empty). Text on
stderr is harmless. The consequence is described in
[Permissions](permissions.md#startup-files-that-print). A human checks and
fixes it:

1. Look at the pane (the dialog waits for a person; answer it there, or leave
   it), and confirm the request reads `delivered`:

   ```sh
   agent-director read-pane --instance-id <id>
   agent-director get-permission --request-token '<token>' | jq '{delivery, decision, proven_gone_at}'
   ```

2. Find the shell Codex starts for a hook: the account's login shell, bash or
   zsh (any other login shell gives bash), run with `-c`, not as a login
   shell. Run this as the agent's user, with the agent's `HOME`:

   ```sh
   getent passwd "$(id -un)" | cut -d: -f7
   ```

3. Print what a hook's shell prints. Output of 0 bytes is clean; anything else
   is the text that makes Codex ignore the verdict. For zsh, run it as is (it
   reads `.zshenv`, in `ZDOTDIR` when that is set); for bash, name the file
   the spawn's (or its template's) `extra_env` gives as `BASH_ENV`:

   ```sh
   zsh -c : | wc -c
   BASH_ENV='<the file>' bash -c : | wc -c
   ```

4. Fix what printed. In `.zshenv`, delete the printing lines, send them to
   stderr (`>&2`) or guard them with `[[ -o interactive ]]`. For `BASH_ENV`,
   remove it from the spawn's `extra_env` (or the template's), or make its
   file print nothing on stdout. Run step 3 again: 0 bytes.
5. A changed `.zshenv` takes effect at the agent's next hook. A changed
   `extra_env` takes effect only when the agent is launched again: end the
   agent (`pause` or `kill`) and launch it again with the corrected
   `extra_env`.

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
   them: `resume` needs their current session and launch settings).

**Back** (the caller's version cannot be deployed): follow
[Going back to the previous release](#going-back-to-the-previous-release); it
loses every write since the upgrade.

## A row stays `pending` and the trail shows `no_exec_form`

Claude Code is too old for exec-form hooks. Upgrade `claude` to the minimum in
[Requirements](install.md#requirements), then end and relaunch each `pending`
row as the [caller contract](caller-contract.md) says.

```sh
jq -c 'select(.event == "ad.hook.ignored" and .reason == "no_exec_form") | {ts, instance_id, hook_event, parent_command}' ~/.config/agent-director/ad-trail.jsonl | tail -n 5
```

## A row stays `pending` and the trail shows `pid_mismatch`

Hooks are ignored when the agent program the row runs is not the process the
hook finds as its parent: a launcher or shim that runs the agent as a child, not
by `exec`. The second query's `parent_command` is the agent (Claude Code or
Codex) and `pane_command` is the launcher. The usual causes:

- **Claude Code:** `claude` on PATH is a shim that runs Claude Code as a child.
- **Codex:** `codex` on PATH, or `[launch] codex_path`, is the node launcher an
  npm install of Codex puts there, a wrapper that does not `exec` the native
  program, or a program such as `timeout`, `setsid` or `sh -c` that runs it as a
  child (`nohup` makes Codex exit instead).
- **Either agent:** a `[launch]` path names a launcher or a wrapper that does
  not `exec` the agent.

```sh
jq -c 'select(.event == "ad.hook.ignored" and .reason == "pid_mismatch") | {ts, instance_id, hook_event, parent_pid, parent_command, row_pane_pid}' ~/.config/agent-director/ad-trail.jsonl | tail -n 5
jq -c 'select(.event == "ad.hook.pane_is_grandparent") | {ts, instance_id, pane_command, parent_command, advice}' ~/.config/agent-director/ad-trail.jsonl | tail -n 5
```

The `advice` in the second record follows the row's agent. Fix it per
[Requirements](install.md#requirements): point the agent's `[launch]` path
(`claude_path` or `codex_path`) at the native program, or at a wrapper that
`exec`s it. Then end and relaunch each `pending` row as in the previous item.

## A Codex row stays `pending` or goes `missing` for another reason

A Codex agent reports in at its first prompt, not at its start, so a fresh
Codex row is `pending` until then; this is not a fault. These causes leave a row
`pending` past the pending grace period, noted `unreported` by `find-missing`
(the agent runs but no hook reported), or `missing` (the agent's process is
gone). The `pid_mismatch` cause has its own item above. Look at the pane
first, read-only, as step 3 of "Inspect and end a session by hand" does: Codex
usually says what it refuses. Whatever the cause, end and relaunch each
`pending` row as the [caller contract](caller-contract.md) says once fixed.

Every command below that runs Codex (`codex --version` or any other) writes
Codex's own files, such as `tmp/arg0/...`, into the Codex folder it runs
against: the default `~/.codex`, or the folder `CODEX_HOME` names. Run it
against a folder you accept that in.

- **`codex` or `codex_path` is missing, too old, or does not qualify.** The
  program the launch runs is not there, so the pane ends and the row goes
  `missing`; or it is older than the minimum in
  [Requirements](install.md#requirements) and does not understand the launch's
  settings, so its pane shows an error or the agent never reports. Check the
  path the launch uses (`[launch] codex_path`, else `codex` on the PATH of the
  agents' tmux server) and its version:

  ```sh
  command -v codex
  codex --version
  ```

  Fix: install Codex at the minimum version, or set `[launch] codex_path` to
  the native program (see [Requirements](install.md#requirements)); a program
  that runs Codex as a child is the `pid_mismatch` item above. A Claude row
  whose `[launch] claude_path` names no program goes `missing` the same way:
  check the path with `ls -l '<claude_path>'`, then fix or unset it.
- **The group folder is missing, not signed in, or under the system temp
  folder.** A `CODEX_HOME` in `extra_env` that is not an existing folder, or one
  with no login, leaves Codex at its sign-in or error screen: the row stays
  `pending`, noted `unreported`, or goes `missing` if Codex exits. A folder
  under the system temp folder makes Codex create none of its helper programs
  (such as `apply_patch`), so use a folder outside it. Check:

  ```sh
  ls -ld '<folder>'
  CODEX_HOME='<folder>' codex login status
  ```

  Fix: create the folder and sign it in once by hand as [Setting up a group
  folder](multi-account.md#setting-up-a-group-folder) says, or spawn with an
  absolute `CODEX_HOME` outside the system temp folder.
- **A Codex config turns hooks off or allows managed hooks only.** Codex then
  runs none of agent-director's hooks, so the row never reports in: it stays
  `pending`, noted `unreported`, while the agent works at its prompt. The
  layers that can do it are the default or group folder's `config.toml`, the
  project's `.codex/` and `/etc/codex`. Look in each for a `hooks` setting
  (`<folder>` is `~/.codex` or the group folder):

  ```sh
  grep -sn 'hooks' '<folder>/config.toml' '<project>/.codex/config.toml' /etc/codex/*.toml
  ```

  Fix: remove `hooks = false` under `[features]`, or the managed-hooks-only
  setting, from the layer that has it. A spawn's own arguments cannot do this:
  `spawn` refuses `--disable hooks` and `features.hooks` (see the [caller
  contract](caller-contract.md#spawn-and-template-input-refusals)).
- **A newer Codex rejects the hook-trust hash agent-director computes.**
  agent-director trusts each of its hook entries by a hash of the entry, so a
  Codex that computes that hash differently asks for a review of
  agent-director's own hooks. With pre-trust `ok` the launch's bypass of the
  hook review covers them and nothing shows. With `pre_trust` `skipped` (the
  spawn or the row set `no_pre_trust`) or `failed`, the pane shows Codex's hook
  review naming agent-director's hooks, and the row stays `pending`, noted
  `unreported`. Check the `pre_trust` of the spawn's result (`get` shows
  `no_pre_trust`) and the pane. Fix: relaunch with pre-trust on (an absolute
  `CODEX_HOME`, no `no_pre_trust`), or answer the review in the pane yourself
  by trusting the hooks.

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

## `install.sh` refuses with `ErrMoveConflict`

`install.sh` (exit 5, `err_name=ErrMoveConflict`) found a layout of AD's
directory it will not move as it is. It changed nothing: the message names the
cause and a remedy, and the install can be run again once the remedy is done.
The old directory is `~/.agent-director`; the new one is
`~/.config/agent-director`. A link at the old path that points to the new
directory counts as no old directory and never causes this.

Find the cause by its first line, then apply its remedy.

- **The new directory is a dangling link, or not a directory** (on every
  install). Remove it, or fix it so it is a directory (or a link to one), and
  re-run.
- **The old directory is a dangling link** that does not point to the new
  directory (on every install). Remove it (`rm -- ~/.agent-director`) and
  re-run.
- **Both directories exist.** The message names both and the time each one's
  store last changed. This release moves the old directory into the new place,
  so one of them has to be set aside. Compare the two, then set one aside by
  renaming it (a timestamp keeps the name free), and re-run:
  - When the old directory holds the install in use, rename the new one. The
    re-run moves the old directory in.
  - When the new directory holds the install in use, rename the old one.
  - A new directory that holds only what `make-template` (or the Go
    client's `MakeTemplate`) created, such as a `templates` directory, next to
    a real old install is the first case: rename the new directory. Afterwards
    copy any template you want from it into the moved directory's `templates`.
    This is a way to end up with both directories: on a host that has only the
    old directory, the Go client's `MakeTemplate` and the CLI's `make-template`
    run with `--store-path` create `~/.config/agent-director/templates/`
    themselves, so running either before the install makes the next install
    refuse. The CLI's `make-template` without `--store-path` creates nothing
    there: it returns `ErrOldLayout`.

  ```sh
  mv -- ~/.config/agent-director ~/.config/agent-director.aside-$(date +%Y%m%d-%H%M%S)
  mv -- ~/.agent-director ~/.agent-director.aside-$(date +%Y%m%d-%H%M%S)
  ```

  Run only the command for the case that applies. Delete the set-aside
  directory once you are sure it holds nothing you need; `uninstall.sh
  --purge` leaves it and names it.
- **The old directory is a link or not a directory, or its `config.toml`,
  `templates`, one template or its store is a link** (while the move is due).
  The move copies the files and will not follow a link. Replace the link with a
  copy of what it points at (the message prints the commands), re-run, and make
  the link again in the new directory afterwards if you want it. A dangling
  link is removed. When the old path is not a directory at all, rename it aside
  and re-run: the install then makes a fresh one and moves nothing.
- **A path key cannot be placed.** `[store] db_path` or `[log] error_log_path`
  in `config.toml` names a place whose directory is missing or cannot be read,
  so `install.sh` cannot tell whether it is inside the old directory. Create
  that directory, or correct the value, and re-run.

## `install.sh` refuses with `ErrAgentsLive`

`install.sh` (exit 5, `err_name=ErrAgentsLive`) found something using the old
directory. A move would change the path every hook and every running process
names, so none may run across it. It changed nothing.

The message lists what it found:

- **Rows in a live state** (`pending`, `waiting`, `working`,
  `check_permission` or `ask_user`) in the old store, with each row's
  instance id.
- **Processes running the old binary**, with each pid. A Claude Code session's
  own MCP server (`agent-director serve --stdio`) is one: it runs the old binary
  until that session ends.
- **Rows that could not be read**: the store stayed busy past its busy
  timeout, or is unreadable, so the install cannot tell whether an agent runs.
  Nothing else is wrong until a re-run reads them again.
- **The process check did not run**: this host has no `/proc` and no usable
  `ps` or `lsof`, so no process was looked for. Check by hand that nothing
  runs the old binary.

Fix:

1. Stop whatever starts agents (a scheduler, a loop, a caller), then every
   agent, every `serve` and every caller, as "Stopping a set of agents before a
   binary change" says.
2. A row whose agent is already gone stays live until the old binary clears
   it. The message prints the command, with the old binary's path:

   ```sh
   ~/.agent-director/bin/agent-director find-missing
   ```

   A row whose agent still runs is never cleared by it: end that agent first.
3. Run `install.sh` again from a shell agent-director did not start (not from
   inside an agent's session).

## `install.sh` stops with `ErrMoveFailed`

`install.sh` (exit 5, `err_name=ErrMoveFailed`) failed while copying the old
directory into the new one, or was interrupted. The message names the cause.
The old directory was not changed: the install removed its partial copy of the
new directory, and `~/.config` as well when the move had created it and left it
empty. When the failure came before any copy, the message says nothing was
changed.

Look at the cause the message gives, then:

1. A full disk is the usual cause: free space on the disk that holds `~/` (and
   on the one that holds the system temp directory, where `install.sh` stages
   both binaries), or fix the cause the message names.
2. A fifo, a socket or a device inside the old directory cannot be copied.
   Remove it or move it out of the old directory.
3. A temp directory inside either AD directory is refused: set `TMPDIR` to a
   directory outside them.
4. Run `install.sh` again. If an earlier run was killed (a power loss, `kill
   -9`) and left the new directory with a `.move-incomplete` file in it, the
   re-run deletes that partial directory and moves again. The one case it does
   not repeat: the file names the kept directory (`~/.agent-director.pre-move-*`)
   and that directory exists, so the move had finished; the re-run only removes
   the file. A leftover old directory is then the both-present case in
   "`install.sh` refuses with `ErrMoveConflict`".

**Stopped after the rename.** A signal (`Ctrl-C`, `kill`) can arrive after the
old directory was renamed but before the install finished the move. The message
then says the install was stopped after the move had renamed the old directory.
The move itself finished: the old directory is now the kept name
(`~/.agent-director.pre-move-*`), `~/.config/agent-director` holds the complete
copy, and nothing was removed. Run `install.sh` again: it continues from
`~/.config/agent-director`, with no move due. The kept directory is the old one
as it was; delete it once the upgrade is confirmed.

## `install.sh` refuses config.toml or a template

`install.sh` (exit 5; `err_name=ErrConfigMalformed` for `config.toml`,
`err_name=ErrTemplateMalformed` for a template) read `config.toml` or a stored
template and will not go on. It changed nothing: the message names the file,
each line to change and what to do, and the install can be run again once the
file is fixed. While the directory move is due the files are read in the old
directory (see [Upgrade](install.md#upgrade)): the message names the copy it
read there, and that copy is the one to fix.

Find the cause by the message's first line.

`config.toml`:

- **`cannot tell which programs agent-director launches (the [launch] table)`.**
  A `[launch]` line is outside the form `install.sh` reads. Keep `claude_path`
  and `codex_path` each once, in lower case, under one `[launch]` header, each
  on one line as `key = "<path>"` or `key = '<path>'`, with no backslash and at
  most a `#` comment after it. A value is an absolute path or starts with `~/`;
  `""`, or no line, leaves the key unset. A blank value, a relative path, `~`
  alone and `~user/...` are refused (see [configuration](configuration.md)).
- **`config.toml sets the question-tool key more than once under [defaults]`.**
  The message lists each line that sets the key, under its old name or its new
  one, `disable_ask_user`. Keep one, named `disable_ask_user`, and delete the
  others.
- **`config.toml sets defaults as a key and names ... in it`.** A line before
  any header sets `defaults` as a key (an inline table, `defaults = { ... }`)
  and names the old name of the question-tool key, which the rename cannot reach
  inside it. Remove the line. Set each key it set on its own line under a
  `[defaults]` header, the question-tool key as `disable_ask_user`, adding the
  header at the end of the file when there is none. Add no header in the
  line's place: the lines below it would fall under that header.

A template (only the lines before its first `[table]` header are read; its
tables never are):

- **`template <file> holds a line the migration cannot read`.** A line there is
  none of: blank, a `#` comment, or one `name = value` line with a bare name
  (letters, digits, `_`, `-`) and a value that ends on the line. A quoted or
  dotted name, or a value that goes on to the next line (a multi-line array or
  string), is refused. Rewrite it as one bare `name = value` line.
- **`template <file> sets ... more than once`** (the old name of the arguments
  key, set on more than one line) and **`template <file> sets both ... and
  agent_args`** (the old name and `agent_args` both set). Keep one line, named
  `agent_args`, and delete the others.

Either file:

- **`cannot read <file>, which the agent-rename migration reads`.** Make it
  readable by the user running the install.
- **`cannot migrate <file> through its symlink`.** The file is a link the
  migration cannot write through: its links loop, or the file they reach is in a
  directory that is missing or cannot be written in (a dotfiles repository not
  cloned yet, a read-only `/nix/store`). The migration changes the file, with
  or without `--no-hooks`, so the link has to be fixed to reach a file in a
  directory you can write in. Or make the change yourself where the file comes
  from: in `config.toml` the question-tool key is named `disable_ask_user`; in a
  template the arguments key is named `agent_args`, and a template with no
  `agent` among the lines before its first `[table]` header gets the line
  `agent = "claude"` there. A file that needs no change is neither written nor
  refused.

Run `install.sh` again after the change. It reads the same files again and
changes nothing until every one is accepted.

## A migration write failed after the binary swap

`install.sh` (exit 1, no `err_name` line) could not write a file of the
migration of `config.toml` and the templates, after it had installed the new
binaries and before it opened the store. The message starts `the agent-rename
migration could not write <file>; stopping before the store is opened`, and its
`file` and `error` lines name the file and the cause (a full disk, a permission
that changed since the checks, a failed copy). The same stop, exit 5, comes with
the message `the agent-rename migration refuses <file>, which pre-flight
accepted`: the file changed after the checks and now needs the fix in
"`install.sh` refuses config.toml or a template".

Nothing is lost. That file is as it was, every copy the install made is kept (a
`<file>.pre-agent-rename.<YYYYmmdd-HHMMSS>` beside each file it had already
migrated, and the directory the move kept), and the store is not opened. When
the install moved the directory first, both stops also print, before the ways on,
the `references:` lines of `config.toml` and the templates that still name the
old directory: a re-run has no move due and would not print them, so change
each by hand now (see [Upgrade](install.md#upgrade)). Look at what the message
names:

```sh
ls -l <file> <file>.pre-agent-rename.*
df -h <file>
```

Then either:

1. Fix the cause the `error` line names (free space on the disk that holds the
   file, or restore the permission, or fix the link and its target's
   directory), then run this release's `install.sh` again, from a shell
   agent-director did not start. It completes the migration: it changes nothing
   in a file already migrated and makes no new copy of it, and then migrates the
   store.
2. Or take the way back to the previous release:
   [Going back to the previous release](#going-back-to-the-previous-release).

## `install.sh` warns that nothing schedules the sweeps

`install.sh` ended with `install.sh: WARNING: nothing schedules agent-director's
find-missing and expire sweeps on this machine.` It comes after `install.sh:
done`; when the MCP registration failed (exit 6), it comes just before the last
line, `install.sh: installed, but the MCP registration failed`. The exit status
is unchanged and nothing is broken, but until `find-missing` runs, a row whose
agent was killed, crashed or lost in a reboot stays live, and until `expire`
runs, finished rows are never deleted.

Without `--schedule`, the warning means this install's own entries for both
sweeps are not there: no tagged cron lines for both with a cron daemon running,
and not all four units. With `--schedule`, it adds a line, `--schedule was asked
for and installed nothing:` (or `could not schedule the sweeps:` when it put the
cron lines in anyway), that gives the reason.

The warning, with or without `--schedule`, also prints the exact command to run
again: `cd <the directory the install ran in> && bash <script> <the original
arguments> --schedule` (a piped install says to run the same install command
again with `--schedule` added). It leaves the command out only when the schedule
files could not be found or rendered; it then points at them in the repository
instead. Any `schedule:` lines
the run printed come before the warning; a run that only warns prints none,
except that a piped run that fetched the schedule files prints `  schedule: the
schedule files are not beside this script; fetched them from ...` on stdout
even then. The reason names the step that failed. The usual ones:

- **`HOME` belongs to another user.** `--schedule` schedules only for the user
  running it, who must be the agents' user. Run `install.sh --schedule` as the
  agents' user.
- **Neither cron nor a systemd user manager is usable.** `crontab` is not
  installed or `crontab -l` fails, and `systemctl --user show-environment`
  fails. Check both as the agents' user:

  ```sh
  crontab -l
  systemctl --user show-environment
  ```

  Install cron, or start the user manager (log in as that user, or run
  `loginctl enable-linger <user>` as root), then run `install.sh --schedule`
  again. Or add the crontab lines the warning prints yourself, as the agents'
  user.
- **No cron daemon runs.** `crontab` works but no process named `cron` or
  `crond` runs (WSL, a container, a minimal VM) and systemd is not usable. The
  install still adds the cron lines and says on stderr that no cron daemon
  runs. Start the cron service, or make the user manager usable and run
  `install.sh --schedule` again.
- **`crontab -` refused the new crontab, or `crontab -l` does not show the
  lines afterwards.** The reason holds the program's own message. Fix what it
  says, or add the lines by hand with `crontab -e`.
- **`systemctl --user` could not enable the timers.** The units the run wrote
  are put back as they were before it (units an earlier run installed are
  kept). Fix the user manager, then run `install.sh --schedule` again.
- **A path cannot be written into a schedule file.** The binary's path holds a
  quote or a backslash, or a path (the tmux directory, `TMUX_TMPDIR`, `HOME` or
  the binary) holds a control character. Install the binary under a plain path
  or fix the variable, then run `install.sh --schedule` again.
- **A schedule file still holds an unfilled `@@` placeholder.** The files come
  from another version than this `install.sh`. Use the files beside the script
  you run, or run it from a checkout or the installed skill.
- **The schedule files were not found and could not be fetched** (a piped
  install with no network, or a binary whose commit stamp is not a full commit
  id; a release tag with no `schedule/` is retried once at `main` first). The
  warning gives the reason and the repository directory on `main`,
  `skills/install-agent-director/schedule/`. Copy the files from there, or run
  `install.sh --schedule` from a checkout or the installed skill.

Check whether a schedule is already in place, as the agents' user:

```sh
crontab -l | grep agent-director-schedule
systemctl --user list-timers 'agent-director-*'
```

The warning looks only for the entries `--schedule` adds. A schedule made
another way (launchd, your own cron line or unit) is not seen, so the warning
appears anyway: ignore it. Never run the crontab lines as another user or as
root. What the schedule is, how to set it up by hand and how long a user timer
runs are in [Maintenance](maintenance.md#scheduling-the-sweeps).

`uninstall.sh` removes the schedule it finds. When `crontab -l` fails for a
reason other than "no crontab", it says so and leaves the crontab alone: remove
the lines tagged `agent-director-schedule` with `crontab -e`. It does the same
when it cannot write the crontab back.

## Going back to the previous release

Use this when the upgrade to this release must be undone. Whatever was written
since the upgrade is lost: the previous release reads the directory as it was
before the upgrade. The previous release's schema cannot read this release's
store, so there is no other way back; a binary swap or a downgrade of the store
does not work.

1. Stop whatever starts agents, then every agent, every `serve` and every
   caller. Remove the schedule `install.sh --schedule` added, if any, because
   its entries run the binary in the directory step 2 sets aside: delete the
   crontab lines tagged `agent-director-schedule` (`crontab -e`), or disable
   the timers and delete the four units:

   ```sh
   systemctl --user disable --now agent-director-find-missing.timer agent-director-expire.timer
   rm -- ~/.config/systemd/user/agent-director-find-missing.service ~/.config/systemd/user/agent-director-find-missing.timer ~/.config/systemd/user/agent-director-expire.service ~/.config/systemd/user/agent-director-expire.timer
   systemctl --user daemon-reload
   ```

   (Use `$XDG_CONFIG_HOME/systemd/user/` when that is set to an absolute path.
   `uninstall.sh` would also remove the binaries and hooks: do not run it for
   this.)
2. Set the new directory aside:

   ```sh
   mv -- ~/.config/agent-director ~/.config/agent-director.after-upgrade-$(date +%Y%m%d-%H%M%S)
   ```

3. If `~/.agent-director` exists again (something recreated it since the
   upgrade), set it aside too. Then rename the newest kept directory back. It
   holds the store, `config.toml` and templates as they were before the upgrade.

   ```sh
   mv -- ~/.agent-director ~/.agent-director.recreated-$(date +%Y%m%d-%H%M%S)
   ls -d ~/.agent-director.pre-move-*
   mv -- ~/.agent-director.pre-move-<newest> ~/.agent-director
   ```

   Run the first command only when the old path exists. The newest name is the
   one with the latest timestamp.
4. A store kept outside AD's directory (`[store] db_path`) was migrated in
   place by the upgrade. Put back the copy made before the upgrade: `state.db`
   with its `-wal` and `-shm`, deleting a `-wal` or `-shm` the copy lacks. Skip
   this when the store was inside the directory.
5. In `~/.claude/settings.json`, remove only the entries that hold a hook
   command starting with `~/.config/agent-director/bin/agent-director` (written
   out with your home directory). Leave every other entry. Check the result is
   valid JSON.
6. From the directory the upgrade ran in (for a `local` registration made
   inside a git repository, any directory of that repository), remove AD's MCP
   registration, if there is one, in the scope `claude mcp get agent-director`
   shows. A name registered in several scopes needs this once per scope:

   ```sh
   claude mcp get agent-director
   claude mcp remove agent-director -s <scope>
   ```

7. From the same directory, install the previous release with that release's
   own `install.sh`, fetched from its tag, with the flags the original install
   used (`--register-mcp`, `--no-hooks`, `--symlink-dir` and the rest). It puts
   back its help hooks. Its `--register-mcp` registers nothing with the current
   Claude Code (it prints `registration failed (continuing anyway)`), so when
   step 6 showed a registration, add it back by hand, in the scope step 6
   showed (`local`, `user` or `project`), with the old path:

   ```sh
   claude mcp add agent-director -s <scope> -- ~/.agent-director/bin/agent-director serve --stdio
   claude mcp get agent-director
   ```

   This release's `install.sh` cannot install the previous release: it refuses
   an earlier release's binaries.
8. Change back by hand what was changed by hand to the new path after the
   upgrade: help hooks in the `settings.json` of other `CLAUDE_CONFIG_DIR`
   folders, Codex MCP and help entries, scripts, `$PATH` entries and an MCP
   registration made by hand.
9. Start the caller's previous version.

Delete the `after-upgrade` and `recreated` directories once the previous
release is confirmed working; `uninstall.sh --purge` removes them.
