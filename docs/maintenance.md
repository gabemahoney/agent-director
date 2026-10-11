# Maintenance

## Scheduling the sweeps

Schedule both verbs yourself (launchd, systemd timer, cron, Task Scheduler):

```sh
# Mark live rows whose agent is gone `missing`; run often (e.g. every 2 min):
agent-director find-missing
# Delete finished rows older than expire_retention_days that nothing runs:
agent-director expire
```

`find-missing` marks a live row `missing` when its agent's process is gone or,
when the process cannot be checked, when tmux shows no session of its launch;
otherwise the row stays unverified. A `pending` row is not judged until its
grace period has passed. Marked rows are in `ids`; undecided rows are in
`unverified_ids`, each with `liveness_unverified_since` and `liveness_note`
in `list` and `get`.

Past its grace period, a `pending` row whose agent runs but has not reported
in stays `pending` with `liveness_note` `unreported`. Read the pane, then type
only if you looked at it:

```sh
agent-director read-pane --claude-instance-id <id>
agent-director send-keys --claude-instance-id <id> --allow-pending --text "<answer>"
```

A caller that cannot judge the pane follows the
[caller contract](caller-contract.md).

A row with `relay_mode=on` stays in `check_permission` until the agent's
next hook after its last permission request. A request answered at the
pane outside agent-director stops holding the row once Claude Code shows
its dialog gone: its tool runs, or, for the main agent's request (not a
subagent's), the turn ends. When the agent's next hook is lost (for
example the end of a turn after a denied request), the notification Claude
Code sends while the agent sits idle at its prompt moves the row to
`waiting` once none of its requests still holds it. Failing that,
`find-missing` moves the row out once none of its requests still holds it
and none of its relay hooks can still run: to `waiting` when that idle
notification was the agent's last hook, otherwise to `working`. Such a row
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
can wrongly mark or delete rows whose agent still runs.

## Recovering after a reboot

A reboot kills every agent and tmux server; the rows survive. Run these two
steps in order from a startup script:

```sh
agent-director find-missing                       # mark dead rows `missing`
agent-director resume --claude-instance-id <id>   # relaunch with history
```

`resume` acts on any finished row (`ended` or `missing`); `find-missing` first
makes a dead live row `missing`. Without a session id or transcript (see
`transcript_status` in `get`), spawn the id again with reuse; the new agent
has no memory of the old conversation:

```sh
agent-director spawn --cwd <dir> --claude-instance-id <id> --reuse-finished
```
