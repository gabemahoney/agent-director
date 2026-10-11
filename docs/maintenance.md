# Maintenance

## Scheduling the sweeps

Two verbs keep `state.db` honest. Schedule both yourself, and run them
with the same user and tmux environment as the agents:

```sh
# Mark live rows whose agent process is gone as `missing`, and note a running
# agent stuck in `pending` as `unreported` — run often (e.g. every 2 min):
agent-director find-missing

# Delete finished rows older than expire_retention_days whose agent's process
# is not running and whose agent has no tmux session; keep the others:
agent-director expire
```

Wire these into your platform's scheduler (launchd, systemd timer, cron,
Task Scheduler).

**Recovery depends on `find-missing` being scheduled.** Nothing else
reports a stuck or stale row: without it, a row stuck `pending` while its
agent runs is never noted, and a row whose agent is gone stays in `list` in
a stale live state (`waiting`, `working`, `ask_user`, `check_permission` or
`pending`).

`find-missing` judges each live row by its agent's process: a row whose
process runs stays live, and a row whose process is gone is marked
`missing`. Only when the process cannot be checked does it ask tmux, on the
row's recorded socket: the row is marked `missing` when tmux shows no
session of its current launch, and otherwise left unverified; a row whose
recorded tmux session name cannot be used is never looked up, but left
unverified with a note of its own. A `pending`
row is not judged until its grace period has passed, nor while the `spawn`
or `resume` that launched it is still running. Marked rows are
listed in `ids`; rows it cannot decide are listed in `unverified_ids`, and
each carries `liveness_unverified_since` and a `liveness_note` in `list`
and `get`. `missing` is the sweep's judgement on the evidence available to
it, not proof that the agent has exited. A run as another user, as root or
against another tmux server can mark live rows `missing`.

Past its grace period, a `pending` row whose agent runs but has not
reported in stays `pending` and gets the `liveness_note` `unreported`. Its
agent is alive, but no hook has reported since its launch: it may sit at a
Claude Code startup screen or idle at its prompt, and agent-director cannot
tell which. Such a row is in neither `ids` nor `unverified_ids`; find it
with `list` or `get`. To act on it, read the pane, then type:

```sh
agent-director read-pane --claude-instance-id "$id"
agent-director send-keys --claude-instance-id "$id" --allow-pending --text "<answer or prompt>"
```

Only a caller that looked at the pane should type: keys sent without a look
can answer a startup screen. A caller that cannot judge the pane ends the
launch with the live-row sequence of the [caller contract](caller-contract.md)
(`kill`, then `find-missing`) or hands it to a human. `send-keys` still
refuses a `pending` row without `--allow-pending`, noted or not. A later
sweep that finds the agent alive keeps the note; the agent's next hook
clears it and moves the row on.

Finished rows (`ended` or `missing`) are never removed on their own: no
`agent-director` verb removes a row you name, and only an `expire` you
schedule removes finished rows, at the default retention; a row's state
alone never gets it deleted. `expire` first keeps every row whose
recorded tmux session name cannot be used, on every run, without reading
its process or asking tmux (see "A row whose recorded name
cannot be used" under [Operator actions](operator-actions.md)). For every
other row it checks the agent process and, unless it runs, asks tmux on the
row's recorded socket (else yours): it deletes the row only when the process
is not running and tmux shows no session of the agent, and lists it in
`ids`. Rows whose session runs or that it cannot check are kept and listed
in `kept_ids`; the trail's `ad.expire.kept` records give each reason. Agents
never run `expire`, least of all with `--older-than 0d`. A run as another
user, as root or against another tmux server can wrongly delete rows whose
agent still runs.

## Recovering after a reboot

A reboot kills every session's process and tmux server, but the rows
survive in `state.db`. Bring a session's conversation back in two steps,
in this order:

```sh
# 1. Mark the rows whose agents' processes are gone as `missing`:
agent-director find-missing

# 2. Relaunch a session with its full history replayed:
agent-director resume --claude-instance-id <id>
```

`find-missing` must run first — `resume` only acts on a session that
`find-missing` has already marked `missing`. After a reboot `find-missing`
marks the rows whose agents' processes are gone; `missing` is the sweep's
judgement on the evidence available to it, not proof that the agent has
exited. `resume` relaunches under
the same id and restores the session's env and conversation transcript.
The resumed row shows `pending`, keeping its session id and history, until
its agent reports in, then `waiting`.
`resume` marks the folder as trusted too, unless the session was spawned
with `--no-pre-trust`: then no `resume` of it marks the folder, and the
agent may wait at the trust prompt.

`resume` also recovers history from a session that rotated (for example when
agents were restarted and Claude handed the session a new id) — it falls back
to the session's earlier transcripts automatically. A session's history
belongs to the life of its id, which starts at the spawn that created it
or at a reuse: `resume` falls back only to earlier transcripts of that
life, and a reuse starts the id over with no earlier history. If `resume`
reports that no transcript was ever written, the session was never
messaged before it stopped, so there is no conversation to bring back. Run
`agent-director get --claude-instance-id <id>` to see a session's
`transcript_status` and its prior sessions — the earlier sessions of the
current life, not the current one — before deciding anything.

When `resume` cannot bring the conversation back (for example it reports
no session id, or that the transcript was never written or is missing),
spawn the id again, opting in to reuse:

```sh
agent-director spawn --cwd <dir> --claude-instance-id <id> --reuse-finished
```

The new agent starts with no memory of the old conversation.

agent-director does not restart sessions for you. Deciding when to run
`find-missing` then `resume` after a boot — from a startup script,
service, or scheduler — is up to you.
