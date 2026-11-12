# Maintenance

## Scheduling the sweeps

Choose an external cadence for recovery and separate retention cleanup.
Neither command runs an internal scheduler:

```sh
# Check all agents once; run often (e.g. every 2 min):
agent-director sweep
# Delete finished rows older than expire_retention_days that nothing runs:
agent-director expire
```

`sweep` selects an all-agent snapshot and runs the same eligible
[recovery checks as state reads](caller-contract.md#bounded-recovery-on-state-reads).
Finished rows skip live-agent checks; late exact input proof can still be
recorded without sending input.
`status`, `get`, `get-many` and `list` run those checks for their returned agents. Existing
`find-missing` schedules keep working: it is an alias of `sweep`.

For agents no `status`, `get`, `get-many` or `list` returns, a longer sweep interval
delays discovery of exits, missed Codex turn endings, stale permission state
and display changes that no agent event or command reports, leaving those
observations older. Discovery or confirmation within one interval is not
guaranteed: busy storage, pending-launch protection, missing or unreadable
evidence and the multi-sample display rule can delay it further, and a missed
or failed sweep leaves facts old until a later eligible event, command or check.
An installed cron entry or enabled timer does not prove a check ran or confirmed
anything.
Equal pane samples do not prove continuous inactivity; unknown or pending
results need later evidence. See the [caller contract](caller-contract.md)
for confirmation and completeness semantics. Without `expire`, finished rows
pile up past `expire_retention_days`.

### Let the install schedule them

Run `install.sh --schedule` as the agents' user (see [install](install.md)).
It schedules recovery every 2 minutes and `expire` daily at 03:17, for the
user running it. Choose these defaults or [set your own cadence](#schedule-them-by-hand).
The [installer selects a supported recovery command](install.md#schedule-the-sweeps)
and uses one of these backends:

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

Running it again with `--schedule` replaces its own entries with the template
cadence and current binary; every other crontab line is kept. Without that
flag, existing commands, cadence and timer enablement stay unchanged. It never
schedules as another user.

An install that does not find entries for both sweeps, with or without
`--schedule` (both tagged cron lines with a cron daemon running, or all four
units), ends with a warning on stderr (the exit status is unchanged). It says
it could not find active `--schedule` entries for both jobs, names the exact
crontab lines for this install's binary and says to run them as the agents'
user. Its own cron lines in a crontab with no cron daemon running do not count
as scheduled. It cannot see a schedule you made another way (launchd, your own cron line or unit): ignore the warning then. The
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
| `agent-director-find-missing.service`, `.timer` | recovery every 2 minutes |
| `agent-director-expire.service`, `.timer` | `expire` daily at 03:17 |

For a binary whose `help` advertises `sweep`, use `sweep` in the cron
recovery line and service `ExecStart`; otherwise keep `find-missing`. Keep the
`agent-director-find-missing` filenames and `agent-director-schedule` markers.
See [the installer's template handling](install.md#schedule-the-sweeps) for why
raw templates differ. On rollback, follow the schedule-restoration step in
[Going back to the previous release](operator-actions.md#going-back-to-the-previous-release).

Lines starting with `##` are notes, not part of the schedule. Choose the cron
times or timers' `OnCalendar` values and fill the placeholders. Add the cron
lines with `crontab -e`, or put the four unit files in the user manager's unit
directory described above and run:

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

See [Sweep results](caller-contract.md#sweep-results) for liveness outcomes,
counters and check coverage. A `pending` row is not judged inside its grace
period or while its launch owner is alive, except a Cursor row whose recorded
gateway is gone.

Past its grace period, a `pending` row whose agent runs but has not reported
in stays `pending` with `liveness_note` `unreported`. Read the pane, then answer
a startup choice only after judging the screen:

```sh
agent-director read-pane --instance-id <id>
agent-director send-keys --instance-id <id> --allow-pending --key <key>
```

For text answers, readiness waits and callers that cannot judge the pane, follow
[pending-row recovery](caller-contract.md#pending-rows-and-startup-screens).

The hold and repair conditions for stale `check_permission` rows are in
[Hooks](hooks.md#a-relay-row-left-in-check_permission), and response authority
remains governed by the [permission contract](caller-contract.md#permission-relay).
A relayed request's delivery is worked out each time the request is read.

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
`sweep` for all rows, then resume each wanted latest session. Run these
two steps in order from a startup script (use `find-missing` if the installed
binary's `help` does not advertise `sweep`):

```sh
agent-director sweep                     # end or mark dead rows `missing`
agent-director resume --instance-id <id>   # relaunch the latest reported session
```

`resume` acts on a finished row (`ended` or `missing`); `sweep` first
makes a dead live row `missing` (or `ended`, for a Codex row that reported its
end). Cursor loads only its saved latest session and protected transcript;
load may leave it `pending` until a real first prompt. Missing native session
or protected transcript never falls back to older history. For unavailable
latest sessions, fresh reuse or artifact-preserving fresh-instance recovery,
follow the [Cursor recovery procedure](caller-contract.md#cursor-operation-boundaries).

For Claude and Codex, without a session id or transcript (see
`transcript_status` in `get`), spawn the id again with reuse and the row's
agent, which `get` shows; the new agent has no memory of the old
conversation:

```sh
agent-director spawn --cwd <dir> --instance-id <id> --reuse-finished --agent <agent>
```

If the reboot removed a row's saved cwd (a tmpfs `/tmp` wiped, a container
restarted), `resume` returns `ErrCwdNotFound` and changes nothing; see
[Transcript recovery](caller-contract.md#transcript-recovery).
