# Maintenance

## Scheduling the sweeps

Nothing runs the two sweep verbs until they are scheduled:

```sh
# Mark live rows whose agent is gone `missing` (`ended` if it reported its end);
# run often (e.g. every 2 min):
agent-director find-missing
# Delete finished rows older than expire_retention_days that nothing runs:
agent-director expire
```

Without `find-missing`, a killed or crashed agent's row, and the row of an
agent lost in a reboot, stays live. Without `expire`, finished rows pile up
past `expire_retention_days`.

### Let the install schedule them

Run `install.sh --schedule` as the agents' user (see [install](install.md)).
It schedules `find-missing` every 2 minutes and `expire` daily at 03:17, for the
user running it, from the files below:

- **cron**, when `crontab -l` works and a cron daemon (a process named `cron`
  or `crond`; checked on Linux only) is running: two job lines and a header
  comment in the user's crontab, all tagged `agent-director-schedule`.
- **systemd**, when cron is not usable and `systemctl --user show-environment`
  works: a user `.service` and `.timer` for each sweep in the user manager's
  unit directory (the absolute `XDG_CONFIG_HOME` that command reports, plus
  `/systemd/user`; else its absolute `HOME`, plus `/.config/systemd/user`; when
  it reports neither, this shell's `~/.config/systemd/user/` or an absolute
  `$XDG_CONFIG_HOME/systemd/user/`), both timers enabled. A user timer runs only while the
  user's systemd manager does: while the user is logged in, or always after
  `loginctl enable-linger <user>`.
- **neither:** it installs nothing, prints why, and ends with the warning
  below. When `crontab` works but no cron daemon runs and systemd is unusable
  too, it still adds the two cron lines (they run once a cron daemon starts),
  says on stderr that no cron daemon runs, and ends with the warning.

Running it again replaces its own entries, so there is never a second copy,
and points them at the current binary; every other crontab line is kept. It
never schedules as another user.

An install that does not find entries for both sweeps, with or without
`--schedule` (both tagged cron lines with a cron daemon running, or all four
units), ends with a warning on stderr (the exit status is unchanged). It says what is
unscheduled, names the exact crontab lines for this install's binary and says
to run them as the agents' user. Its own cron lines in a crontab with no cron
daemon running do not count as scheduled. It cannot see a schedule you made
another way (launchd, your own cron line or unit): ignore the warning then. The
reasons it gives and their fixes are in
[Operator actions](operator-actions.md#installsh-warns-that-nothing-schedules-the-sweeps).
To remove what `--schedule` added, run `uninstall.sh` (see
[Uninstall](install.md#uninstall)).

### Schedule them by hand

The files are in `skills/install-agent-director/schedule/` in a checkout or the
installed skill, and in `~/.config/agent-director/schedule/` after a piped
install (`curl ... | bash`) that fetched them:

| File | Holds |
|---|---|
| `agent-director.cron` | the two crontab lines |
| `agent-director-find-missing.service`, `.timer` | `find-missing` every 2 minutes |
| `agent-director-expire.service`, `.timer` | `expire` daily at 03:17 |

Lines starting with `##` are notes, not part of the schedule. Fill the
placeholders, then add the cron lines with `crontab -e`, or put the four unit
files in `~/.config/systemd/user/` and run:

```sh
systemctl --user daemon-reload
systemctl --user enable --now agent-director-find-missing.timer agent-director-expire.timer
```

| Placeholder | Value |
|---|---|
| `@@AGENT_DIRECTOR@@` | `~/.config/agent-director/bin/agent-director`, written out in full |
| `@@ENV_PREFIX@@` (cron) | `PATH=<the directory of tmux>:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin` (without the tmux directory and its colon when tmux is not on the PATH); then `TMUX_TMPDIR=<dir>`, only when the agents' tmux server lives in one; then `HOME=<your $HOME>`, only when `$HOME` is not the home directory of your passwd entry |
| `@@ENV_LINES@@` (units) | the same as `Environment="NAME=value"` lines |

To skip the filling, copy the filled-in crontab lines the install's warning
prints. cron and systemd give a job a minimal environment, which is why the
lines carry `PATH` and `TMUX_TMPDIR`: the sweep must reach the tmux server the
agents use. `HOME` is carried when it differs from the passwd home because the
binary keeps its files in `$HOME/.config/agent-director`.

`find-missing` marks a live row `missing` when its agent's process is gone or,
when the process cannot be checked, when tmux shows no session of its launch;
otherwise the row stays unverified. A Codex row whose agent reported its end
and whose process is gone is marked `ended` instead, and is in neither `ids`,
`count` nor `unverified_ids` (see
[How a Codex row ends](hooks.md#how-a-codex-row-ends)). A `pending` row is not
judged until its grace period has passed. Marked rows are in `ids`; undecided
rows are in `unverified_ids`, each with `liveness_unverified_since` and
`liveness_note` in `list` and `get`.

Past its grace period, a `pending` row whose agent runs but has not reported
in stays `pending` with `liveness_note` `unreported`. Read the pane, then type
only if you looked at it:

```sh
agent-director read-pane --instance-id <id>
agent-director send-keys --instance-id <id> --allow-pending --text "<answer>"
```

A caller that cannot judge the pane follows the
[caller contract](caller-contract.md).

A row with `relay_mode=on` stays in `check_permission` until the agent's
next hook after its last permission request. A request answered at the
pane outside agent-director stops holding the row once the agent's hooks
show its dialog gone: its tool runs (a Claude agent), or, for the main
agent's request (not a subagent's), the turn ends. When the agent's next
hook is lost (for example the end of a turn after a denied request), the
notification Claude Code sends while the agent sits idle at its prompt moves
the row to `waiting` once none of its requests still holds it. Failing that,
`find-missing` moves the row out once none of its requests still holds it
and none of its relay hooks can still run: to `waiting` when that idle
notification was the agent's last hook, otherwise to `working`. A Codex
row always goes to `working`, since Codex never sends the idle
notification. Such a row
is in neither `ids` nor `unverified_ids`. Without a scheduled
`find-missing`, a relay row left in `check_permission` after its agent
moved on stays there. A relayed request's delivery needs no sweep: it is
worked out each time the request is read.

`expire` deletes a finished row (`ended` or `missing`, listed in `ids`) only
when its agent process is not running and tmux shows no session of the agent.
The others are in `kept_ids`; the trail's `ad.expire.kept` records give each
reason. A row whose recorded tmux session name cannot be used is always kept
([Operator actions](operator-actions.md)).

A run of either verb as another user, as root or against another tmux server
can wrongly mark or delete rows whose agent still runs: schedule both as the
agents' user, in their tmux environment, against their
`~/.config/agent-director`.

## Recovering after a reboot

A reboot kills every agent and tmux server; the rows survive. Run
`find-missing` for all rows. Cursor cannot currently resume or reuse a
finished row; its recovery is a fresh unique instance (see
[Cursor operation boundaries](caller-contract.md#cursor-operation-boundaries)).
For Claude and Codex, run these two steps in order from a startup script:

```sh
agent-director find-missing                       # end or mark dead rows `missing`
agent-director resume --instance-id <id>   # relaunch the latest reported session
```

`resume` acts on a finished Claude or Codex row (`ended` or `missing`); `find-missing` first
makes a dead live row `missing` (or `ended`, for a Codex row that reported its
end). Without a session id or transcript (see
`transcript_status` in `get`), spawn the id again with reuse and the row's
agent, which `get` shows; the new agent has no memory of the old
conversation:

```sh
agent-director spawn --cwd <dir> --instance-id <id> --reuse-finished --agent <agent>
```
