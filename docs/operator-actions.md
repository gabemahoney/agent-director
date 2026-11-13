# Operator actions

Human-only procedures. Automated callers (programs, scripts, agents, MCP
clients) must not perform them.

`kill-finished`, `delete`, `migrate-journal` and `snapshot-store` are verbs of `agent-director-admin`
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
Read this store's id (16 hexadecimal characters) from a private copy to leave
the store folder unchanged; with another `db_path`, use that file:

```sh
(
  umask 077
  store="$HOME/.config/agent-director/state.db"
  copy=$(mktemp -d "${TMPDIR:-/tmp}/ad-store-id.XXXXXX") || exit 1
  trap 'rm -rf "$copy"' EXIT
  if [ -f "$store-wal" ]; then
    cp "$store-wal" "$copy/state.db-wal" || exit 1
  fi
  cp "$store" "$copy/state.db" || exit 1
  sqlite3 -batch -init /dev/null "$copy/state.db" "SELECT value FROM store_meta WHERE key = 'store_id'"
)
```

If it fails, run it again.

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

Cursor failure reasons and the latest-resume/fresh-instance recovery boundary are in the
[caller contract](caller-contract.md#cursor-operation-boundaries). Use the
same inspection and ownership procedures above for a Cursor launch. A
process-start reason naming an unavailable executable or cwd calls for checking
the selected [`cursor_path`](configuration.md) and project directory; an
operating-system permission denial calls for checking executable and project
directory permissions. A structured authentication-required reason calls for
checking the selected runtime's existing login; it does not authorize AD to
sign in, sign out or change credentials. An unknown reason does not establish
an authentication or configuration fault. Resolve surviving work using the
steps above before the caller contract's explicit fresh-instance procedure.
A transport failure alone does not authorize deleting a row or adopting an
unrelated process. Public `get`, `get-many`, `status` and scoped `list` can reconcile a lost
gateway, including one lost before runtime registration; their scope, binding
conflict refusals and `ErrStoreBusy` behavior are in the
[caller contract](caller-contract.md#cursor-operation-boundaries).
Missing state or a finished-row public `kill` no-op does not prove that a Cursor
runtime exited. When public operations cannot resolve survivors, the caller
must STOP for a human to follow [Inspect and end a session by hand](#inspect-and-end-a-session-by-hand)
before any fresh launch; follow the
[Cursor recovery boundary](caller-contract.md#cursor-operation-boundaries).
The resume and reuse ownership and conflict procedures below also apply to
Cursor, including switches. A failed-label error may report a restored row
while its new session may still run: follow the error's surviving-session
warning and the inspection steps above before retrying. If its latest native
session or protected transcript is unavailable, the fresh-instance command in
the caller contract preserves the original row and artifacts. Same-id reuse
starts a fresh conversation under the documented
[life rules](usage.md#naming-the-agent); do not substitute an older session or
repair saved identity by hand.

For a Cursor selection refusal, decide whether the intended account is the
existing personal login with separate configuration/data directories, as in
[Multi-Account](multi-account.md#cursor-configuration-and-data-directories).
The credential profile is unreleased; do not discard account intent merely to
make a launch succeed. Resume cannot edit saved selection. Preserve the original
row, life, history and artifacts; use the caller contract's fresh-instance
procedure only when a supported selection meets the intended account and
configuration, after resolving survivors through the procedures above. Do not
read, export or edit the protected saved environment to reconstruct a launch.
If intent is unavailable or requires protected data, retain the refusal for a
human decision. The fresh procedure uses caller-known supported `extra_env` or
template intent, the same `HOME` and a generated unique id; no selection is
appropriate only for deliberate personal login without custom directories.
Automated input correction belongs
in the [caller contract](caller-contract.md#spawn-and-template-input-refusals).

## Unresolved ordinary input

Cursor's `gateway_refused` is not unresolved input: the text was discarded.
Follow [Cursor input outcomes](caller-contract.md#cursor-input-outcomes).

For `send-keys` with `partial_input_unconfirmed` or `submission_unconfirmed`,
or `ErrSubmissionUnconfirmed` with `keys_sent: true` and no native `outcome`,
the original input may still submit later. Keep other writers away. For a native
attempt, re-issue the original text with its `delivery_request_id`
to observe any late transcript proof while the row's state, permission-relay
and dialog guards allow it; this does not replay input. Finished rows are refused.
For an unconfirmed manual or fallback send, check hook-derived `get` or `status`
without resending. The outcome fields and supported
launches are in [Ordinary text submission](caller-contract.md#ordinary-text-submission).

If still unresolved, inspect the exact labelled session read-only using steps
1 to 3 above. A visible draft helps a human choose a recovery action; it is
not proof that a turn was or was not submitted. Do not blindly send another
Enter or paste the text again. Continue through direct operator control only
after judging the agent's state, or end the owned session and verify its
process has stopped before starting a fresh conversation. Ending a session
does not undo a turn or its effects.

Manual input does not reset native delivery eligibility. To return to
native ordinary input, use a fresh supported launch with an empty composer;
`resume` does not provide that contract. Do not delete or edit stored input
records or provider evidence to bypass an unresolved attempt. A released lock,
timeout or uncertain database commit does not prove that input was not delivered.

A manual `send-keys` can also return `ErrSubmissionUnconfirmed`
with `outcome: "not_started"` when it cannot own the input writer or read or
save its journal. `pause` returns `ErrInternal` for these failures. That call
sent nothing. Let any active writer finish, then
re-issue the call. For a persistent refusal, check access to the configured
database and private [delivery evidence directory](settings.md#ordinary-delivery-launch-settings)
as the agents' user. Preserve both; never clear either to force input.

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
   and 5); on a `pending` row, first wait until a recovery check (a state read
   or `sweep`) marks it `missing` and check the session still shows the
   `session_created` you noted.
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
   `get`, `get-many` and the trail cannot show them).
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

2. Confirm it is the recorded agent process started at launch: Claude Code,
   Codex, or the Cursor gateway. Check its start time and command:
   `ps -o pid,lstart,args -p '<pid>'`.
3. Look for its pane, on any other tmux server you know of too (steps 4b and 3);
   end the pane by id if not wanted. In no pane: recheck its start time, then
   `kill '<pid>'`. Do the same for a survivor.
4. Run `agent-director kill --instance-id <id>` again, or wait for
   a recovery check (a state read or `sweep`) to mark the row `missing`
   (a Codex row whose agent reported its end: `ended`), then resume or respawn
   as the [caller contract](caller-contract.md) says.

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
stderr is harmless. A human checks and fixes it:

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

- Nothing of value runs there: end the session (steps 4 and 5); a recovery check
  (a state read or `sweep`) then marks the row; resume or reuse the id.
- Another program someone uses runs there: leave it, remove only its claim with
  `tmux -u -S '<socket>' set-option -t '<session id>' -u @ad_owner`, and tell
  its owner; a recovery check (a state read or `sweep`) then settles the row.
- The agent runs in another pane (its `ps` pid matches the row's): leave it;
  only the pane verbs fail, and `agent-director kill` still ends the session.

## The agent's pane was not adopted after a lost create reply

The row stays `pending` with the agent working and no pane recorded. Read the
session's label (steps 1 and 2): its second field is the launch token. In its
panes (step 4b), the agent's `@ad_pane` is `<token> <pane id>`.

- Wanted agent: leave it; a recovery check (a state read or `sweep`) adopts
  its pane after the grace period.
- No pane carries the token: a recovery check (a state read or `sweep`) marks
  the row `missing` though the agent may still run. End the session with
  `agent-director kill` while the row is live, by hand once `missing`
  (steps 4 and 5), then resume or reuse the id.

## A `pending` row with no launch start or token

`ErrSpawnNotInteractive` from `send-keys` saying the row's launch start or
launch token is "not recorded": a row from before an upgrade, or written by an
agent-director process started before it.

1. Restart any `agent-director` process (`serve` too) from before the upgrade.
2. Find the agent's session by the row's recorded name (step 1); read its
   environment (step 2b) and look (step 3). Unwanted: end it (steps 4 and 5).
   Wanted, or someone else's: leave it; a recovery check (a state read or
   `sweep`) marks the row `missing` once nothing of it runs. Then resume or
   reuse the id.

## Stopping a set of agents before a binary change

When the caller's own stop path cannot run, a human stops one labelled set of
agents (for example `service=<name>`) in this order before agent-director's
binary changes. Never delete a row or edit `state.db`.

1. Stop whatever starts agents (the caller's service, schedulers, cron jobs).
2. List the set: `agent-director list --label <key>=<value>`.
   Keep each row's id and `parent_id` to resume it with its parent as
   [step 6 of the upgrade skill](../skills/upgrade-agent-director/SKILL.md#6-resume-the-agents) says.
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

For a native-input journal upgrade, this set must include every writer that
can reach the store or its input roots, including untracked callers. Keep
launchers, hooks, helpers, in-flight commands and all `serve` processes stopped
through committed verification. Check actual process start times and access;
stored `ended`/`missing`, a successful finished-row `kill` no-op and absent tmux
sessions do not prove exit. Follow the maintenance window below.

## Upgrading the native-input journal

This upgrade moves operational input records into SQLite. Every upgrade of an
existing store below schema v16 crosses it, once; a fresh install or a store at
schema v16 or later does not. `install.sh` checks the journal flags (step 3) and
inventories holders in pre-flight, before restore copies, the binary swap or
migration authorization. A refusal changes nothing. It runs the cutover at its
store step, after the binary swap.

Run it from an independent shell as the agents' user, never as root, with
explicit operator authorization. The administrator checks actual processes and
never kills them:

- On Linux it reads `/proc`.
- On macOS it lists the user's processes with the system `/usr/sbin/lsof`; the
  real migration also reads access-control entries with `/bin/ls`. Run as root, it
  refuses with `process_inventory_unreadable`. macOS may ask for consent to read
  a protected folder: answer it.
- Any other host refuses with `process_inventory_unsupported`, changing
  nothing.

**One run at a time.** Start no second `install.sh` or `migrate-journal`
against the same install until the first has ended. A second run can let old
binaries run unseen by the first one's checks, and if it commits the cutover,
a release the first run restored refuses the store with `ErrSchemaMismatch`.

Step 1 of the upgrade skill chooses the release tag and reads the configured
store's schema before stopping rows. For a crossing upgrade, after the operator
confirms the stops, maintenance window and recovery set below, check that no
`serve` remains. The skill checks the kernel program name before checking arguments for `serve`;
another program merely mentioning `agent-director serve` in its arguments does
not count.
Run the strictly read-only `install.sh --check-holders` before stopping any row.
Repeat it with the same tag and journal flags after step 2's sweep, just before
step 3's install. On either check's non-zero exit, stop and show the operator
the complete output, including listed processes. If the first check refuses,
no row has been stopped. Skip both checks and the journal flags with no crossing.
See [Upgrade](install.md#upgrade) for the command, source selection, exit statuses
and read-only guarantees.

For a direct check with this release's candidate administrator:

```sh
/absolute/candidate/agent-director-admin --store-path /absolute/actual-state.db migrate-journal --check-only --maintenance-window --roots-complete --backup-dir /absolute/new-backup-directory --legacy-root /absolute/additional/input
```

Omit or repeat `--legacy-root` as needed. With an explicit absolute
`--store-path`, `--check-only` checks arguments, then inventories processes for
the current DB, its `-wal`/`-shm` and the complete legacy roots, resolving aliases.
It leaves ownership, modes, link-component and access-control checks to the real
migration; an unresolvable inventory path refuses with `process_inventory_unreadable`.
Without `--store-path`, it checks arguments only and resolves no configured
store. Both forms are strictly read-only: no config load, store open,
migration lock, backup or sentinel write. Success is `{}` and exit 0;
refusal is exit 1 with `details.refusal` (reason, processes and action),
`details.cutover: "not_committed"`, `details.steps_committed: false` and no
`details.result`. Neither check stops a process. A clear inventory does not
establish the maintenance boundary below or replace the later migration checks.

1. Establish and maintain the stopped-writer boundary above. Include the
   actual configured database and every legacy input root. The invoking
   `HOME`'s `.config/agent-director/input` and `.agent-director/input` roots are
   included even with a custom database. Declare every additional root used
   by another invoking home or installation; do not infer roots from the DB's
   directory. Prevent privileged or untracked writers from starting too.
   The check does not look for renamed copies of agent-director, so stopping
   every caller and `serve` is the guard.
2. Keep the matching prior release and a stopped-writer copy of the actual
   DB, its existing `-wal`/`-shm`, and all legacy evidence before upgrading.
   Preserve provider logs/transcripts outside those roots separately. Do not
   open the only backup with a newer binary. The journal cutover also creates
   an exclusive backup before any schema hop; it does not replace copies
   needed before an old-directory move or config/template rewrite.
3. Run this release's installer with the original flags plus:

   ```sh
   bash install.sh --journal-maintenance-window --journal-roots-complete --journal-backup /absolute/new-backup-directory --journal-legacy-root /absolute/additional/input
   ```

   Omit the last pair when no extra root exists; repeat it for each extra
   root. The backup directory must not exist and must be outside all input
   roots. A failed backup stays incomplete: keep it and use a new directory
   on retry. `COMPLETE` attests to a finished copy. Its `manifest.json` maps
   `database`, `database-wal`, `database-shm` and numbered `legacy-N` trees to
   their original paths. Original lock files stay in place and are not copied.
4. Keep the boundary until the administrator reports `authority: "sqlite"`
   and the installer verifies the target version. Before commit the legacy
   records remain authoritative; after commit SQLite remains authoritative
   even with `retirement_pending: true`. Retained operational files are then
   evidence, not permission to run an older writer. Logs, transcripts and
   original launch locks are preserved. Restart only compatible callers.

A refusal's envelope gives its `reason` and `action`, and the process `pid`,
`starttime` and `role` when available; [Upgrade refusal
reasons](#upgrade-refusal-reasons) gives each reason's next step. After a
`committed` or `unknown` outcome, once the installer's re-run has finished any
pending schema step, keep the boundary and retry administrator verification
directly:

```sh
~/.config/agent-director/admin/agent-director-admin --store-path /absolute/actual-state.db migrate-journal --maintenance-window --roots-complete --backup-dir /absolute/new-verification-backup --legacy-root /absolute/additional/input
```

Use the same invoking `HOME`, actual DB and complete root list as the failed
run; omit or repeat `--legacy-root` as in step 3. Choose a new exclusive backup
directory. This retry backup can contain the already-migrated store: retain
the original pre-upgrade recovery backup separately. Require exit 0 and
`authority: "sqlite"`; an authority field on an error is not successful
verification. An already-current store needs no new sentinel. Rerunning the
installer on it uses ordinary `list` and does not replace this administrator
verification. If the schema transition is still pending, step 3 supplies its
authorization.

A failed cutover ends `install.sh` with exit 5. Its last stderr line names the
cause (`install.sh: err_name=<Name>`), and the JSON envelope above it carries
`details.cutover`, which says whether the cutover committed. Linux and macOS
give the same outcomes:

| Cause line | `details.cutover` | Installed afterwards | Next |
|---|---|---|---|
| `ErrJournalMigrationRefused` | `not_committed` | As before the run | [Refused](#installsh-refuses-with-errjournalmigrationrefused) |
| `ErrSchemaVerifyFailed`, after `the restore could not finish`, with `details.copies` | `not_committed` | Partly restored | [Putting back a kept install](#putting-back-a-kept-install) |
| `ErrJournalMigrationIncomplete` | `not_committed` | This release | [Stopped](#installsh-stops-with-errjournalmigrationincomplete) |
| `ErrJournalMigrationIncomplete` | `committed`, `unknown` or unreadable | This release | [Stopped](#installsh-stops-with-errjournalmigrationincomplete) |

A cutover that committed, or may have, switched authority: never reset the
version or reconstruct replayable journals. Recovery is
[restore-only](#going-back-to-the-previous-release).

Installer preflight reads a private consistent database/WAL snapshot through
the candidate administrator; it leaves the original DB, sidecars and directory
unchanged on a live/unreadable refusal. Candidate platform, architecture and
version checks precede the first database query. Busy or hot-journal snapshot
refusals need their cause resolved before retry. The operator-only
[`snapshot-store` reference](admin-reference.md#snapshot-store) describes the
private destination; it is not a recovery backup or migration authorization.

### Unused journal flags

When journal flags are given and no cutover is crossed, a completed `install.sh`
run warns on stderr that the flags were not used and no journal backup was made.
It names the store's schema version before this install, or says there was no store then.
The flags' values are not checked. The warning also appears when installation
finishes with exit 6 because MCP registration failed; a refused install adds
no such warning. Do not treat `--journal-backup` as a backup in this case.

### Upgrade refusal reasons

Each reason is `details.refusal.reason` in the envelope, and its action is
`details.refusal.action`; the installer's message repeats both as `reason:` and
`action:` lines. Actions name `migrate-journal`'s flag with `install.sh`'s
spelling beside it.

Pre-flight uses two refusal headlines:

- Argument refusals: `install.sh: pre-flight refused the journal flags (agent-director-admin migrate-journal --check-only)`.
- Holder or inventory refusals: `install.sh: pre-flight holder check refused (agent-director-admin migrate-journal --check-only --store-path)`.

**Retry**, below, is how most actions end: re-run this release's installer with
a new `--journal-backup` directory, which finishes any pending schema step, then
run the [direct verification retry](#upgrading-the-native-input-journal) with a
new `--backup-dir` until it exits 0 with `authority: "sqlite"`. Each new backup
directory must be absolute, must not exist yet, and must lie outside every
legacy input root and AD's old and new directories. A pre-flight refusal makes
no backup, so its `--journal-backup` directory can be given again.

**Store changed** is "no" for a reason only reached before the backup. "Envelope"
means `details.cutover` says: `not_committed` with `details.steps_committed`
`false` leaves the store unchanged, `true` means earlier schema steps raised its
version, `committed` means the cutover committed, and `unknown` means it may
have.

The flag checks, which pre-flight's `--check-only` and `migrate-journal` make
first:

| Reason | Means | Store changed | Next step |
|---|---|---|---|
| `maintenance_boundary_required` | `--journal-maintenance-window` was not given. | No | Establish the maintenance window (steps 1 and 2 above), then pass it. |
| `legacy_roots_not_established` | `--journal-roots-complete` was not given. | No | Declare every additional root with `--journal-legacy-root`, then pass `--journal-roots-complete`. |
| `invoking_home_unavailable` | `HOME` is not an absolute home directory. | No | Run with `HOME` set to the invoking user's absolute home directory. |
| `legacy_root_not_absolute` | A `--journal-legacy-root`, or the explicit `--store-path` in `--check-only`, is relative. | No | Give every root and the explicit store as an absolute path. |
| `legacy_root_not_clean` | A `--journal-legacy-root` is not in clean form. | No | Give every root with no trailing `/`, no repeated `/` and no `.` or `..` element. |
| `absolute_backup_required` | `--journal-backup` is missing or relative. | No | Pass a new absolute directory outside every legacy input root. |
| `backup_inside_legacy_root` | `--journal-backup` lies inside a legacy input root. | No | Choose one outside every legacy input root. |
| `backup_exists` | `--journal-backup` already exists. | No | Keep it (it may be an earlier attempt's backup) and choose one that does not exist yet. |
| `backup_unreadable` | A parent of `--journal-backup` is missing or cannot be searched. | No | Choose one whose parent directories exist and can be searched. |

Explicit-store `--check-only` and installer pre-flight run the process inventory
only, leaving the store unchanged. They do not make the real migration's
store/root path-access checks. The real `migrate-journal` makes both checks
before its lock, inside the cutover step and after its commit. The installer's
own pre-flight link refusal is listed separately in the shared reason's row:

| Reason | Means | Store changed | Next step |
|---|---|---|---|
| `legacy_root_unreadable` | A legacy root, or a directory above it, cannot be read or searched. | Envelope | Make each root and its parents readable and searchable by the user running the upgrade, keep the window, then Retry. |
| `legacy_root_symlink` | `migrate-journal`: the store path, `HOME` or a legacy root has a link in it; the invoking `HOME`'s `~/.agent-director/input` is always checked. `install.sh`, in crossing pre-flight: `~/.config/agent-director` or `~/.agent-director` is a link, including a compatibility link. | Envelope; `install.sh`'s: no | Make the paths real. For a compatibility link at `~/.agent-director`, follow the [checks and link removal below](#installsh-refuses-with-errjournalmigrationrefused). For a linked `~/.config/agent-director`, replace the link with the directory it points to. Keep the window, then Retry. |
| `legacy_root_access_unestablished` | The store or a legacy root is not owned by and private to the user, or carries an access-control entry, or its entries cannot be read. | Envelope | Make the store and every legacy root owned by the user running the upgrade (a repair by hand, path by path, not a recursive `chown`), then private to it: `chmod 0600` the store, `chmod 0700` each root, remove any access-control entry (`chmod -N <path>`). Keep the window, then Retry. |
| `process_inventory_unreadable` | The process inventory as a whole could not be read, or a read-only inventory could not resolve a store, sidecar or root path (on macOS, see below). | No for read-only checks; otherwise Envelope | Make the store, sidecar and root paths resolvable; run as the agents' user with every mounted file system reachable or unmounted (on Linux, with `/proc` mounted and readable). After a read-only refusal, repeat the check; for a migration attempt, keep the window, then Retry; report it if it persists. |
| `inventory_deadline` | The process inventory did not finish within the attempt's time limit. | No for read-only checks; otherwise Envelope | Make every mounted file system reachable or unmount it, or answer any privacy consent prompt. After a read-only refusal, repeat the check; for a migration attempt, keep the maintenance window, then Retry. |
| `old_or_unverified_mutators` | Listed processes may still write (roles below), including holders found by either read-only check before installation. | No for read-only checks; otherwise Envelope | Stop each listed `agent_director_mutator` (an agent-director program or a process holding the store, its sidecars or a file under a legacy root); each may run again once the cutover is verified. After a read-only refusal, repeat the check; for a migration attempt, keep the maintenance window, then Retry. |
| `process_inventory_unsupported` | The host is outside the supported set: Linux on x86_64 or arm64 (aarch64), and macOS 13 or later on arm64. | No | Run the upgrade on a supported host. |

The store's causes:

| Reason | Means | Store changed | Next step |
|---|---|---|---|
| `migration_lock_held` | Another administrator's `migrate-journal` holds `<store>.input-migration.lock`. | Envelope | Let that run finish, then Retry. |
| `migration_lock_unestablished` | The lock file exists, but whether a process holds it cannot be checked. | Envelope | Only once no administrator runs (check 1 of [Putting back a kept install](#putting-back-a-kept-install)), `rm -- '<store>.input-migration.lock'`, then Retry. On a read-only file system the `rm` fails: make it writable first. |
| `authorization_missing` | No migration authorization for the store's version. | No | Retry: the installer writes it. |
| `store_version_unreadable` | The store's version could not be read from a private copy. | No | Stop every process that holds the store open, make it a readable SQLite database, leave room in `TMPDIR` for a copy of it, then Retry. |
| `store_newer_than_release` | A newer release made the store. | No | Install that release or a later one. |
| `backup_failed` | The journal backup could not be made; it stays incomplete. | No | Keep it. Make the store and everything under the legacy roots readable regular files and directories, choose a new backup directory with room for their copy, then Retry. |
| `store_open_failed` | The store could not be opened. | Envelope | Point `[store] db_path` at the existing store, in a directory the user can write, and make it a readable and writable SQLite database owned by that user. If it already is one, its metadata is damaged: inspect it or restore its pre-upgrade copy. Then Retry. |
| `store_busy` | Another process holds the store. | Envelope | Stop every process that holds it open, keep the window, then Retry. |
| `schema_step_failed` | A schema step failed. | Envelope | Fix its cause (disk space, write access, a schema changed by hand), keep the window, then Retry. |
| `legacy_writer_active` | An old agent-director process holds a legacy launch lock. | Envelope | Stop it, keep the window, then Retry. |
| `legacy_evidence_unestablished` | Launch evidence under a legacy root cannot be read or written, or is not this store's, or the store records one launch token twice. | Envelope | Make the evidence readable and writable by the user and remove what is not this store's; for a token recorded twice, inspect the store or restore its pre-upgrade copy. Keep the window, then Retry. |
| `native_input_state_inconsistent` | The store's native-input state was changed by hand. | Envelope | Inspect the store or restore its pre-upgrade copy, keep the window, then Retry. |
| `attempt_deadline` | The attempt passed its time limit. | Envelope | Make it finish in time (a less busy disk for the store and the backup), keep the window, then Retry. |
| `migration_incomplete_or_commit_unknown` | The attempt did not finish: either it could not confirm whether its cutover committed (`details.cutover` `unknown`), or the cutover committed and the rest of the schema chain did not (`committed`). | Envelope | Keep the window, then Retry. |
| `committed_verification_incomplete` | The cutover committed; the check after it did not pass. | Yes | Keep the window, stop any listed process, then run the direct verification retry with a new `--backup-dir` until it exits 0 with `authority: "sqlite"`. |

The installer's own reasons, and the one both tools write:

| Reason | Means | Store changed | Next step |
|---|---|---|---|
| `cutover_state_unreadable` | A store at schema v16 or later whose `store_meta` table cannot be read. | No | [A store at schema v16 or later that the upgrade cannot read](#a-store-at-schema-v16-or-later-that-the-upgrade-cannot-read). |
| `native_input_authority_missing` | A store at schema v16 or later with no native-input authority recorded. | No | The same. |
| `agents_live` | With no move due, the store holds rows in a live state (`pending`, `waiting`, `working`, `check_permission`, `ask_user`). The message lists each row's state and instance id, with the command that stops its agent. | No | Stop each listed agent with the command under it, clear the row of an agent you have established is gone with `find-missing` (as the agents' user), then re-run the same install command from a shell agent-director did not start ([the commands](#installsh-refuses-with-errjournalmigrationrefused)). |
| `agent_rows_unreadable` | With no move due, the store's agent rows could not be read, so pre-flight cannot tell whether an agent is live. The message gives sqlite3's error. | No | Re-run the same install command once the store is no longer busy. If the rows still cannot be read, inspect the store with that error. |
| `check_only_unestablished` | `--check-only` gave neither `{}` with exit 0 nor an envelope. | No | Install this release's `agent-director-admin` with this release's `agent-director` (`--admin-binary`, or both from one checkout or release), then re-run; report it if it persists. |
| `backup_inside_install_directory` | With a move due, `--journal-backup` lies inside `~/.agent-director` or `~/.config/agent-director`, or its place there cannot be judged. | No | Choose a directory outside both, then re-run. When the message says `To fix: repair the path …`, first repair that directory's path: rename aside any part of it that is not a directory, make each part one the user can enter, break any link loop, and make its file identity readable. |
| `previous_release_unknown` | The previous install's `agent-director-admin` answered `migrate-journal --help` with neither exit 0 nor exit 1 with `ErrUnknownVerb`. The message names it, and its output. | No | Make it runnable, or remove it if it is not that release's, then re-run with the same flags. |
| `restore_copies_unavailable` | The copies a restore needs could not be made (the message's `item`, `copy` and `cause` lines). | No | Make room or write access beside the installed binaries, the store and the managed PATH link; if the sentinel beside the store is not a regular file, or an installed binary or the managed PATH entry is neither a link nor a regular file, remove that entry; then re-run with the same flags. Remove each `not removed:` copy the message names, by that name. |
| `outcome_unestablished` | `migrate-journal` exited with no envelope. | Envelope (`unknown`) | [`install.sh` stops with `ErrJournalMigrationIncomplete`](#installsh-stops-with-errjournalmigrationincomplete). |
| `restore_incomplete` | The restore could not finish, and no `migrate-journal` envelope was relayed. When one was, the unfinished restore keeps that envelope's own reason and action. | No | [Putting back a kept install](#putting-back-a-kept-install). |
| An `err_name` | `migrate-journal` or `--check-only` ended with that error and no refusal: `ErrInvalidFlags`, `ErrConfigMalformed` or `ErrStoreOpen`. | Envelope | Fix what its `err_description` names (`config.toml` for `ErrConfigMalformed`, the store's path and permissions for `ErrStoreOpen`), then follow the cause line's section. |
| `interrupted` | INT, TERM or HUP stopped the run. | Envelope; before `migrate-journal` started, no; during a run that then committed, yes | `migrate-journal`, before its cutover commit: `the attempt was interrupted before its cutover commit; keep the maintenance window, then re-run this release's installer with a new --journal-backup, which finishes any pending schema step, then run migrate-journal with a new --backup-dir until it exits 0 with authority "sqlite"` (Retry). `install.sh`, before `migrate-journal` started: `re-run this release's installer with a new --journal-backup directory, and let it finish`. `install.sh`, during a `migrate-journal` run that then committed: `re-run this release's installer with a new --journal-backup directory, which finishes the install, then run the administrator's verification retry until it exits 0 with authority sqlite`; the hook, MCP and schedule steps have not run, and the re-run does them. |

Once `migrate-journal` has failed, the installer ignores further interrupts
until it has settled the outcome. When it can detect one, the message says it
arrived and was ignored; an ignored INT or HUP leaves no note.

To replace a linked `~/.config/agent-director` with the directory it points to:

```sh
target=$(cd -P ~/.config/agent-director && pwd -P)
rm -- ~/.config/agent-director
mv -- "$target" ~/.config/agent-director
```

**Roles.** On Linux and macOS, `agent_director_mutator`
(`stop_and_restart_after_cutover`) is the only role: readable evidence shows
that the program's file name contains `agent-director` or the process holds
the store or a file under a legacy root. Any other process needs no action,
including one the check cannot fully inspect, such as a non-dumpable Linux
process or an incomplete macOS open-file entry.

**On macOS:**

- An access-control entry on the database or a legacy root: remove it, for
  example `chmod -N <path>`, then Retry.
- `process_inventory_unreadable`: for a mounted file system `lsof` cannot
  reach, make it reachable or unmount it; for a run as root, run as the agents'
  user; for a failed process selection or listing, or a process missing from
  the selection, Retry with a new backup directory, and report it if it
  persists.
- `inventory_deadline`: make every mount reachable or unmount it, or answer
  any consent prompt, then Retry.

### A store at schema v16 or later that the upgrade cannot read

`install.sh` refused in pre-flight and changed nothing:
`cutover_state_unreadable` means the store's `store_meta` table cannot be read,
and `native_input_authority_missing` means it records no native-input
authority, which only a hand edit leaves. Keep writers stopped and read the
authority from a private copy, with `store` set to the message's `state.db`
path (Apple's sqlite3 cannot open the copy read-only, so it is opened
read-write):

```sh
store='<state.db path>'
d=$(mktemp -d)
if [ -n "$d" ] && cp -p -- "$store" "$d/state.db" &&
  { [ ! -e "$store-wal" ] || cp -p -- "$store-wal" "$d/state.db-wal"; }
then
  sqlite3 -batch -init /dev/null "$d/state.db" "SELECT value FROM store_meta WHERE key = 'native_input_authority';"
  rc=$?
else
  echo 'private copy failed: nothing was read' >&2
  rc=1
fi
rm -rf -- "$d"
[ "$rc" = 0 ]
```

The `rm -rf` removes only the private copy. `private copy failed` means
nothing was read: fix the failure and read again, or get help. A `sqlite-v1:`
value means the authority is recorded: re-run the install. Anything else
(nothing, or an error): restore the stopped-writer pre-upgrade copy of the
store as [Going back to the previous release](#going-back-to-the-previous-release)
describes, or get help. Never write `store_meta` by hand.

## agent-director was installed outside the caller's switch-over

Pre-upgrade agents may still run, and the caller's old version cannot use the
migrated store. Stop the caller and whatever starts agents (step 1 of
"Stopping a set of agents before a binary change"), then choose.

**Forward** (preferred; keeps every write since the upgrade):

1. Identify the live pre-upgrade agents from the set the caller was running
   and end each by hand, then run `agent-director sweep`. Missing
   `tmux_socket` and `launch_started_at` identify older rows without spawn
   identity; handle those as "A `pending` row with no launch start or token"
   says. Schema 9→10→11 preserves existing identity fields, so populated
   fields do not exclude a pre-upgrade agent.
2. Deploy the caller's version; it resumes or reuses those rows (never delete
   them: `resume` needs their current session and launch settings).

**Back** (the caller's version cannot be deployed): follow
[Going back to the previous release](#going-back-to-the-previous-release); it
loses every write since the upgrade.

## A user hook interferes with supervised agents

A user hook that stalls, blocks a tool or emits malformed permission output
can interfere with a supervised agent. To skip that hook only inside
agent-director spawns, put this guard at the start of its command:

```sh
if [ -n "$AGENT_DIRECTOR_INSTANCE_ID" ]; then
  exit 0
fi
```

The same variable is present in Claude and Codex agents. Keep any hook that
should run for those agents outside this exclusion; other sessions still
run the guarded command. Hook layering is described in [Hooks](hooks.md#fire-order).

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
`pending` past the pending grace period, noted `unreported` (the agent runs
but no hook reported), or `missing` (the agent's process is
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

  Fix: remove `hooks = false` or `codex_hooks = false` under `[features]`, or
  the managed-hooks-only setting, from the layer that has it. A spawn's own
  arguments cannot do this: `spawn` refuses `--disable hooks`,
  `features.hooks` and the `codex_hooks` spellings of both (see the [caller
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
another server. Find the old server's process and decide; a recovery check
(a state read or `sweep`) settles the row once it is gone.

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
  A `[launch]` line is outside the form `install.sh` reads. Keep `claude_path`,
  `codex_path` and `cursor_path` each once, in lower case, under one `[launch]`
  header, each on one line as `key = "<path>"` or `key = '<path>'`, with no backslash and at
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

## `install.sh` refuses with `ErrJournalMigrationRefused`

`install.sh` (exit 5, `err_name=ErrJournalMigrationRefused`) stopped a
crossing upgrade with the install as it was before the run. Either pre-flight
refused, before any change (the message says `Nothing was installed or
changed.`), or `migrate-journal` refused its attempt before changing the
previous install's store and the installer put the previous install back (`the
previous install was put back as it was before the run.`). The binaries, the migration sentinel and the
managed PATH link are as before the run. After an old-layout move, the old
directory is back, and this run's `~/.config/agent-director` was set aside as
`~/.config/agent-director.after-upgrade-<YYYYmmdd-HHMMSS>` (`-2`, `-3` and so on
when that name is taken), or emptied when it is a link; a `move    : undone:`
line on stderr says which. The previous install's store was not changed. After
a move, the set-aside directory's store, the one the attempt ran on, may hold
earlier schema steps it committed; the previous release never reads it, and
putting the install back reverted no database.

The envelope above the cause line gives the reason and action, which the
message repeats. Check which release is installed:

```sh
agent-director version
```

Then:

1. Fix the cause from [Upgrade refusal reasons](#upgrade-refusal-reasons),
   keeping the maintenance boundary.
2. Re-run this release's installer with a new `--journal-backup` directory.
   The message prints the command, with `<new-backup-directory>` to replace
   (a piped install says to run the same install command again). The re-run
   moves the old directory again; delete a set-aside directory once the
   upgrade is confirmed.

**Compatibility link** (`legacy_root_symlink`). Both `install.sh`'s crossing
pre-flight and `migrate-journal` refuse a link at `~/.agent-director`, even
when it points to `~/.config/agent-director`: the cutover always checks
`~/.agent-director/input`.

Keep the maintenance boundary. Confirm it is that link:

```sh
ls -ld ~/.agent-director ~/.config/agent-director
readlink ~/.agent-director
```

Before removing it, check `[store] db_path` in `config.toml`, hooks in
`~/.claude/settings.json`, MCP registrations, scheduled sweeps, PATH links
and entries, and scripts for paths under `~/.agent-director`. Point those
references at `~/.config/agent-director`, then remove only the link:

```sh
rm -- ~/.agent-director
```

Use no trailing slash: this removes the link, leaving its target intact.
Never replace it with an empty directory: both directories would be present
and the installer would refuse with `ErrMoveConflict`. Re-run this release's
installer with a new `--journal-backup` directory, then finish verification
as [Upgrading the native-input journal](#upgrading-the-native-input-journal)
describes. Recreate the link only if still needed and only after verification;
a later `migrate-journal` run would refuse it again.

**Live agents** (`agents_live`, with no move due). The message lists each
row in a live state, and under it the command that stops its agent, run with
the installed binary, which is still the previous release's:

```sh
~/.config/agent-director/bin/agent-director pause --instance-id <id>  # a waiting row
~/.config/agent-director/bin/agent-director kill --instance-id <id>   # any other live row
```

Stop whatever starts agents (a scheduler, a loop, a caller) first. A row
whose agent you have established is gone stays live until
`~/.config/agent-director/bin/agent-director find-missing` clears it; run it
as the agents' user, in their tmux environment. A `missing` row does not
prove the agent stopped: a run as another user or against another tmux server
can mark a running agent missing
([Scheduling the sweeps](maintenance.md#scheduling-the-sweeps)). Then re-run
the same install command from a shell agent-director did not start:
pre-flight made nothing, so the same `--journal-backup` directory can be given
again.

## `install.sh` stops with `ErrJournalMigrationIncomplete`

`install.sh` (exit 5, `err_name=ErrJournalMigrationIncomplete`) did not finish
the install successfully and left this release installed; the cutover may still
have committed. `details.cutover` and the
message say which case it is, and the message gives the reason and action.

- **`not_committed`, not restored** (`the cutover did not commit; this release
  stays installed.`). The message says why the installer did not restore:
  earlier schema steps committed, the store's version changed during the
  attempt, or the release installed before the run does not predate the
  cutover. Fix the cause from [Upgrade refusal
  reasons](#upgrade-refusal-reasons), then re-run this release's installer
  with a new `--journal-backup` directory; the message prints the command.
- **`committed`, `unknown` or unreadable** (`the cutover attempt did not finish
  successfully; this release stays installed.`). The cutover committed, or may
  have, and the previous release cannot open the store. Fix the reported cause;
  for `migration_lock_held`, let the other administrator's run end. Re-run this
  release's installer with a new `--journal-backup` directory, which finishes
  any pending schema step, then run the [direct verification
  retry](#upgrading-the-native-input-journal) until it exits 0 with
  `authority: "sqlite"`. The message prints both commands, with
  `<new-backup-directory>` and `<new-verification-backup-directory>` to
  replace.
- **`outcome_unestablished`** (`the installer could not establish how the
  attempt ended`). `migrate-journal` exited with no envelope, so
  `details.cutover` is `unknown`. The installer kept its copies: the message
  lists each as `kept copy: <copy> -> <where it belongs>`, and `details.copies`
  holds them. Complete forward as for `unknown`, or put them back after the
  checks in [Putting back a kept install](#putting-back-a-kept-install).
- **Interrupted** (reason `interrupted`): see its row in [Upgrade refusal
  reasons](#upgrade-refusal-reasons).

**The store moved past the previous release.** An attempt on a store at schema
v14 or earlier, in place or outside the moved directory (`[store] db_path`, or
a store reached through a link inside it), runs earlier schema steps, each
committing on its own, before the cutover. When it ends before its cutover
commit, `details.cutover` is `not_committed` and `details.steps_committed`
`true`: the version was raised, so the previous release cannot open the store
and this release stays. The message says the installer cannot put the previous
release back and gives both ways on: the re-run command, which completes
forward, or the way back, provided the previous release's own `install.sh`
[supports this host](install.md#requirements).

The way back restores the store from the journal backup directory the message
names, which `migrate-journal` filled before any schema step. Keep writers
stopped. `COMPLETE` must exist, and `manifest.json`'s `Database` names the
store; `database`, `database-wal` and `database-shm` are its copies:

```sh
backup='<journal backup directory>'
ls -- "$backup/COMPLETE"
db=$(jq -r .Database "$backup/manifest.json")
rm -f -- "$db-wal" "$db-shm"
cp -p -- "$backup/database" "$db"
if [ -e "$backup/database-wal" ]; then cp -p -- "$backup/database-wal" "$db-wal"; fi
if [ -e "$backup/database-shm" ]; then cp -p -- "$backup/database-shm" "$db-shm"; fi
```

The legacy input roots are unchanged, because no cutover committed; the
backup's `legacy-<k>/` holds the copy of `manifest.json`'s `Roots[k]`. Then
install the previous release as [Going back to the previous
release](#going-back-to-the-previous-release) describes; after an old-layout
move, follow its numbered steps, where this restore takes the place of step 4.
Restoring loses every write made after the backup, which the maintenance
boundary prevents; reconcile uncertain attempts before an older runtime
resumes.

## `install.sh` warns that nothing schedules the sweeps

`install.sh` ended with `install.sh: WARNING: could not find active --schedule entries
for both agent-director's sweep and expire jobs.` (`find-missing` replaces `sweep` when
the target binary's help does not advertise it.) It comes after `install.sh:
done`; when the MCP registration failed (exit 6), it comes just before the last
line, `install.sh: installed, but the MCP registration failed`. The exit status
is unchanged. Absent or failed checks leave observations old; reads also
perform the shared checks on supporting versions. `expire` separately removes
eligible finished rows. See [Maintenance](maintenance.md#scheduling-the-sweeps)
for cadence and evidence limits.

Without `--schedule`, the warning means this install's own entries for both
sweeps are not there: no tagged cron lines for both with a cron daemon running,
and not all four units. With `--schedule`, it adds a line, `--schedule was asked
for and installed nothing:` (or `could not schedule the sweeps:` when it put the
cron lines in anyway), that gives the reason.

Copy the rendered cron lines from the warning. For manual units, follow
[Schedule them by hand](maintenance.md#schedule-them-by-hand).

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

## The MCP registration or deregistration failed (exit 6)

`install.sh --register-mcp` ended with `install.sh: installed, but the MCP
registration failed (see above); exit 6`: `claude mcp add`, or the `claude mcp
remove` before a re-add, exited non-zero. Every other install step is done and
kept: the binaries, the store, the hooks and the links. The read of an existing
registration (`claude mcp get`) never fails the install.

`uninstall.sh --mcp-also` ended with `uninstall.sh: done, except the MCP
deregistration failed (see above); exit 6`: `claude mcp remove agent-director`
failed for a reason other than finding no registration. Every other step,
`--purge` included, still ran.

Either way, stderr shows the command that ran, its exit status, its stderr and
the command to run by hand, with the Claude Code program it used. From the
directory the install or uninstall ran in, check what is registered (see
[which directories see a registration](install.md#mcp-registration)):

```sh
claude mcp get agent-director
```

Fix the cause the stderr names, then run the printed command. Two causes need
a scope:

- **The name is registered for another command** (install). If that
  registration should be replaced, remove it, then run the install again or the
  printed add:

  ```sh
  claude mcp remove agent-director -s <scope>
  ```

- **The name is registered in several scopes** (uninstall). `claude mcp remove`
  without a scope refuses it. Run the command above once per scope; `claude mcp
  get agent-director` shows one scope at a time, so run it again after each
  removal until it finds none.

## Putting back a kept install

This puts the previous release back from the copies a crossing upgrade kept:
after a restore that could not finish (`ErrSchemaVerifyFailed`, the message
`the restore could not finish; the install is partly restored`, and
`details.copies`, whatever the reason: a relayed `migrate-journal` refusal
keeps its own, and `restore_incomplete` is the reason only when nothing was
relayed), after `ErrJournalMigrationIncomplete` with reason
`outcome_unestablished`, or from the copies a killed run left. After an
unfinished automatic restore, the previous install's store is unchanged, and so
is an original store kept inside a pre-move directory. After an
`outcome_unestablished` or killed run, the store may have changed: the checks
below decide whether a put-back is possible. Putting binaries or directories
back rolls back no step committed in the store the attempt ran on
(`details.attempt_store`). If any check
below fails, put nothing back and complete forward as "[`install.sh` stops with
`ErrJournalMigrationIncomplete`](#installsh-stops-with-errjournalmigrationincomplete)"
says.

**The copies.** Each is named `<path>.restore-<YYYYmmdd-HHMMSS>.<suffix>`,
beside the `<path>` it copies, with its mode: the two installed binaries, the
migration sentinel beside the store, and the managed PATH link. The link's copy
is a directory of that name holding the copied link, `agent-director`, in the
`--symlink-dir` directory. That is outside the install directory, so
`uninstall.sh --purge` does not remove it. When the upgrade moves the old
directory, nothing inside the old or new directory is copied: the pre-move
directory, `~/.agent-director.pre-move-<YYYYmmdd-HHMMSS>`, is the copy.

The envelope lists what was kept in `details.copies`, one `{"copy": "<path>",
"belongs_at": "<path>"}` entry each (a link's entry names the link inside its
directory); what the restore could not remove in `details.remove`; and the
store the attempt ran on in `details.attempt_store`. The message prints them as
`kept copy: <copy> -> <belongs_at>`, `still to remove: <path>` and `set aside:
<path>` lines. A killed run reports nothing: its copies share one timestamp,
each belongs at its own name without `.restore-<YYYYmmdd-HHMMSS>.<suffix>` (a
link's, at its directory's place), and the store is the configured one. Use
one run's copies only. Remove copies you do not put back one by one, by name,
once the upgrade is settled; never by a wildcard.

**The checks**, in this order, on `details.attempt_store`:

1. No administrator holds the migration lock: no `agent-director-admin` process
   runs, so this prints nothing.

   ```sh
   ps -A -o pid= -o args= | grep '[a]gent-director-admin'
   ```

2. The store has no native-input authority. Read it from a private copy
   (Apple's sqlite3 cannot open the copy read-only, so it is opened
   read-write):

   ```sh
   store='<details.attempt_store>'
   d=$(mktemp -d)
   if [ -n "$d" ] && cp -p -- "$store" "$d/state.db" &&
     { [ ! -e "$store-wal" ] || cp -p -- "$store-wal" "$d/state.db-wal"; }
   then
     sqlite3 -batch -init /dev/null "$d/state.db" "SELECT value FROM store_meta WHERE key = 'native_input_authority';"
     rc=$?
   else
     echo 'private copy failed: nothing was read' >&2
     rc=1
   fi
   rm -rf -- "$d"
   [ "$rc" = 0 ]
   ```

   The query runs only on a full copy: the database and, when there is one,
   its `-wal`. Only two results mean no authority: exit 0 with nothing
   printed, or exit 1 with sqlite3's error naming `no such table: store_meta`
   as the only output. A `sqlite-v1:` value means the cutover committed:
   complete forward. Anything else, `private copy failed` included, leaves the
   outcome unknown: put nothing back, and complete forward or fix the failure
   and run the checks again. The `rm -rf` removes only the private copy.

   One exception, only after an unfinished automatic restore: when that
   restore emptied a linked `~/.config/agent-director` (its directory holds
   only `.move-incomplete`) and `details.attempt_store` lay in it, the store is
   gone and check 2 has nothing to read. Skip check 2 then: after an
   unfinished automatic restore, the original store kept in the pre-move
   directory is unchanged, and that is the store the put-back returns. After
   an `outcome_unestablished` or killed run, a store check 2 cannot read is
   never skipped.
3. The copied release opens the store: its `list` exits 0. In place, run the
   `agent-director` entry's `copy` (or, with no such entry, the restore already
   put it back: `~/.config/agent-director/bin/agent-director`):

   ```sh
   '<copy>' list
   ```

   It comes last because it runs an old binary. After a move it is step 5
   below.

**In place** (no entry's `belongs_at` is `~/.agent-director`), after the three
checks: rename each `details.copies` entry onto its `belongs_at`, the
`agent-director` binary last, then remove only the `details.remove` paths and
each now-empty link-copy directory. Never copy over an installed file, and stop
if a `belongs_at` is a directory or a link to one: `mv` would put the copy
inside it.

```sh
mv -f -- '<copy>' '<belongs_at>'
rm -- '<details.remove path>'
rmdir -- '<link copy directory>'
```

**After an old-layout move** (an entry's `belongs_at` is `~/.agent-director`),
run every step in one shell, starting with `aside= occupant= nested=`:

1. Run checks 1 and 2 on `details.attempt_store`.
2. Set `~/.config/agent-director` aside, unless the restore already did: skip
   this step when the `set aside:` line or `details.attempt_store` names a
   `~/.config/agent-director.after-upgrade-<YYYYmmdd-HHMMSS>` directory, when
   `~/.config/agent-director` is absent, or when it is a link whose directory
   holds only `.move-incomplete`. Otherwise rename it, or, when it is a link,
   the link itself (no trailing slash; do not empty it here):

   ```sh
   aside=~/.config/agent-director.after-upgrade-$(date +%Y%m%d-%H%M%S)
   [ ! -e "$aside" ] && mv -- ~/.config/agent-director "$aside"
   ```

   If `~/.config/agent-director` is still there, the name was taken: run both
   lines again.
3. Set `premove` to the `copy` of the entry whose `belongs_at` is
   `~/.agent-director`: `premove='<copy>'`. If `~/.agent-director` exists (the
   restore stopped because it was occupied, or something recreated it), look
   at it (`ls -la ~/.agent-director`) and set it aside, or the rename in step 4
   fails or puts the pre-move directory inside it. When `premove` lies inside
   `~/.agent-director` (the occupant appeared while the restore ran), first
   rename it out to the free name it ends in:

   ```sh
   nested=$premove
   premove=~/"${nested##*/}"
   mv -- "$nested" "$premove"
   ```

   Only then set the occupant aside:

   ```sh
   occupant=~/.agent-director.recreated-$(date +%Y%m%d-%H%M%S)
   mv -- ~/.agent-director "$occupant"
   ```

4. Rename the pre-move directory back:

   ```sh
   mv -- "$premove" ~/.agent-director
   ```

5. Check 3, last:

   ```sh
   ~/.agent-director/bin/agent-director list
   ```

   If it does not exit 0, undo, in reverse order, only the renames this
   procedure made, then complete forward:

   ```sh
   mv -- ~/.agent-director "$premove"
   if [ -n "$occupant" ]; then mv -- "$occupant" ~/.agent-director; fi
   if [ -n "$nested" ]; then mv -- "$premove" "$nested"; fi
   if [ -n "$aside" ]; then mv -- "$aside" ~/.config/agent-director; fi
   ```

6. Once `list` exits 0: when step 2 renamed a link, rename it back, empty its
   target and write the move's marker in it, as the restore does:

   ```sh
   if [ -L "$aside" ]; then mv -- "$aside" ~/.config/agent-director && find ~/.config/agent-director/ -mindepth 1 -maxdepth 1 -exec rm -rf -- {} + && : >~/.config/agent-director/.move-incomplete; fi
   ```

   Then rename every other `details.copies` entry onto its `belongs_at`, as in
   place, the managed-link copy in the `--symlink-dir` directory included, so
   `agent-director` on `PATH` runs the previous release again. Then remove only
   the `details.remove` paths and the empty link-copy directory.

Delete the set-aside and `recreated` directories once the previous release is
confirmed working.

## Going back to the previous release

Before either procedure restores or moves anything, confirm that the previous
release's own `install.sh` [supports this host](install.md#requirements).
If that release's install.sh refuses this host, keep this release.

For an upgrade within the current `~/.config/agent-director` layout,
including schema v9→v10 through v19→v20, the old-layout steps below do not
apply. Before upgrading, stop all writers and keep a copy of the actual store
(`[store] db_path` when set), its existing `-wal`/`-shm`, and all legacy input
evidence. Keep provider logs/transcripts outside those roots too. The journal
cutover creates a separate stopped-writer backup before schema changes;
other installer steps do not supply this complete recovery set.

To roll back, maintain the same exclusion, preserve the post-upgrade evidence,
and restore the matching pre-upgrade DB and input evidence at their original
paths. Restore the DB with its companion files, removing a `-wal` or `-shm`
that the copy lacks, as in step 4 below. Keep original launch-lock inodes;
do not replace or delete them. Install the matching previous release with its
own `install.sh` and the original flags, omitting `--schedule` to preserve a
custom cadence. Before restarting any older runtime, restore `find-missing`
as the recovery command in every schedule that runs `sweep`: the tagged cron line
(`crontab -e`), the service `ExecStart`, or a schedule you made another way,
keeping your chosen cadence and environment. After editing a service, run
`systemctl --user daemon-reload`. Then reconcile uncertain attempts against
the preserved evidence: restoration loses all later writes and can lose proof
of input that already reached an agent.
Never replay an attempt merely because the restored copy lacks its proof.

Schema v20 display evidence has no supported reverse recipe.
Schema v19 confirmation evidence has no supported reverse recipe.
Schema v18 Cursor input and proof evidence has no supported reverse recipe.
Schema v17 permission provenance and response claims have no supported reverse
recipe. Schema v16 has no in-place downgrade or replayable file-journal
export. A binary swap or version reset cannot undo these changes. Schema v15,
v14 and v13 are also restore-only. The v12→v11 family-history recipe applies
only to genuine family-only v12 stores; it cannot operate on v13–v20. No
supported reverse path crosses v11→v10 or v10→v9. A copy made after migration
cannot replace the pre-upgrade set. Without that set, or while an uncertain
attempt remains unreconciled, stop and obtain human help.

The numbered procedure below applies only to an upgrade that moved
`~/.agent-director` to `~/.config/agent-director` and retained a
`~/.agent-director.pre-move-*` directory. Whatever was written since that
upgrade is lost: the previous release reads the directory as it was before
the move.

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
   this when the store was inside the directory. Restore any additional
   legacy input roots from the same stopped-writer copy, preserving original
   launch locks and keeping post-upgrade evidence for reconciliation.
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
   registration made by hand, and any schedule you made another way, which
   must run `find-missing` (the previous release has no `sweep`).
9. Reconcile uncertain input against the preserved post-upgrade evidence
   before restarting any older runtime; a restored record cannot prove later
   input was never delivered. Then start the caller's previous version.

Delete the `after-upgrade` and `recreated` directories once the previous
release is confirmed working; `uninstall.sh --purge` removes them.
