# Configuration

`~/.agent-director/config.toml` (created on first run; all fields
optional):

```toml
[defaults]
relay_mode = "off"
expire_retention_days = 31   # 1 to 106751; 0 = use default (31)
disable_askuserquestion = false

[relay]
poll_base_ms = 100
poll_jitter_ms = 100
timeout_seconds = 86400   # 1 to 2147483; 0 = use default (86400)
permission_request_cap = 1000   # 0 = unbounded; negative = use default (1000)

[pause]
timeout_seconds = 30   # 1 to 9223372036; 0 = use default (30)

[pre_trust]
lock_wait_seconds = 12   # 1 to 9223372036; 0 = use default (12)

[store]
db_path = "~/.agent-director/state.db"
busy_timeout_ms = 10000   # 1 to 2147483647; 0 = use default (10000)

[log]
error_log_path = "~/.agent-director/errors.log"

[tmux]   # timing settings: omit a key (or set it to 0) to use its default (pending_grace_seconds: the default or its minimum, whichever is larger; see Timing settings)
# starting_session_seconds = 300
# stopping_window_seconds = 90
# pending_grace_seconds = 60
# query_timeout_ms = 1500
# action_timeout_ms = 2000
# create_timeout_ms = 5000
# pipe_close_wait_ms = 100
# sweep_budget_seconds = 15
# kill_exit_wait_ms = 5000
```

> **Warning:** a refused or malformed value stops agent-director — the
> CLI from its next call, each hook from its next fire, the MCP server
> from its next start (a server already running keeps its old values).
> After editing the file, check it at once with `agent-director list`.

`expire_retention_days` is how many days ago a finished row must have
ended before `expire` without `--older-than` may delete it: a whole number
from 1 to 106751. A missing key, or 0, gives the default (31). A negative
value or one above 106751 is refused, never replaced by the default or
capped, with the same effect as a refused timing setting (see
**Validation** under [Timing settings](#timing-settings-tmux)).

`[relay] timeout_seconds` is how long a relayed permission request waits
for `decide` before it is denied: whole seconds from 1 to 2147483 (about
24.8 days, the longest hook timeout Claude Code honours). `[pause]
timeout_seconds` is how long `pause` waits for the agent to exit: whole
seconds from 1 to 9223372036. `[pre_trust] lock_wait_seconds` is how long
`spawn` and `resume` wait to mark the folder as trusted while another
process holds Claude Code's lock on `.claude.json`: whole seconds from 1 to
9223372036. `[store] busy_timeout_ms` is how long agent-director waits for
the store while another of its processes holds it locked, before the call
fails: whole milliseconds from 1 to 2147483647. For each of the four, a
missing key, or 0, gives the default, and a negative value or one above its
range is refused the same way as `expire_retention_days`.

`lock_wait_seconds` defaults to 12, just over the 10 s after which a lock
left by a killed process counts as abandoned and is cleared. If the wait
runs out, the launch still goes ahead with `pre_trust` `failed`, and the
agent may stop at the folder-trust prompt. A `spawn` or `resume` that
finds the lock held takes up to `lock_wait_seconds` longer: at the
defaults its tmux calls and the wait take at most about 23 s (10.9 s plus
12 s, not counting start-up or a wait for a busy store), under the
TypeScript client's default 30 s call timeout. Each
second added to `lock_wait_seconds` adds up to a second, so above about
19 s such a launch can outlast that timeout, which then returns
`ErrCallTimeout` while the launch may still complete — raise TypeScript
callers' `callTimeoutMs` to match.

`busy_timeout_ms` defaults to 10000 (10 s). Raising it moves no other
limit. On a busy store, each write can wait that long. A launch's
SessionStart hook makes up to four writes besides its wait for
agent-director to record the launch, and Claude Code kills the hook 600 s
after it starts, 60 s after that wait's 540 s limit: at the default the
writes take at most 40 s, but from about 15 s a long store wait can get
the hook killed before it records the agent. So can any other death of
the hook (a crash, an out-of-memory kill). The agent then runs while its
row stays `pending`. A scheduled `find-missing` notes such a row
`unreported` within `pending_grace_seconds` plus one `find-missing` period
(see [Maintenance](maintenance.md)).
And once a call's waits for the store add up to the TypeScript client's
`callTimeoutMs` (30 s by default), the client ends the call with
`ErrCallTimeout` — one wait of 30 s or more is cut off before the store
gives up. Raise TypeScript callers' `callTimeoutMs` to match.
agent-director checks this key against neither limit.

`db_path` moves the store, and `install.sh` follows it; its reads of the
store wait up to `busy_timeout_ms` too. Keep both keys in the one-line form
shown; `install.sh` refuses anything else and says what to fix.

Env vars passed at spawn time (via `--extra-env`) are stored in
`state.db` so `resume` can restore them. The file is owner-only (`0600`
in a `0700` directory).

## Timing settings (`[tmux]`)

Each value is a whole integer in the unit its key names. Every default
can be changed, but never below its safe minimum.

| Key | Unit | Default | Safe minimum | What it bounds |
|---|---|---|---|---|
| `starting_session_seconds` | s | 300 | **60** | Starting-session bound: until a finished row's own tmux session, or a session of an earlier launch of its id that the row does not track ("this id's own abandoned launch"), is this old, it counts as "still starting, retry later" rather than a conflict needing a human. Claude Code reports SessionStart within seconds. |
| `stopping_window_seconds` | s | 90 | **30** | Stopping window: how long after an agent ends it counts as "still stopping, retry later". Covers Claude Code's SessionEnd hook budget plus teardown. |
| `pending_grace_seconds` | s | 60, or more (see below) | **30**, or more (see below) | Grace period: how long `find-missing` leaves a launch alone after it starts, and how long the launch's SessionStart hook waits for agent-director to record the launch (never more than 540 s). If you don't set this, agent-director uses 60 s or the minimum your other [tmux] timeouts require, whichever is larger. If you set it below that minimum, the config is refused. Example: with `create_timeout_ms = 40000` and this key unset, the grace period is 61 s. The setting has no maximum. |
| `query_timeout_ms` | ms | 1500 | none | Each tmux lookup and pane listing. |
| `action_timeout_ms` | ms | 2000 | none | Each tmux kill, key send and pane capture. |
| `create_timeout_ms` | ms | 5000 | none | The tmux call that creates a session (`spawn`, `resume`). |
| `pipe_close_wait_ms` | ms | 100 | none | How long a tmux call waits for its output pipes after its process exits. |
| `sweep_budget_seconds` | s | 15 | none | Total tmux time per run of `find-missing` and `expire`. |
| `kill_exit_wait_ms` | ms | 5000 | none | How long `kill` waits for the agent process to exit after killing its pane. Set from measured exit times under Claude Code's default SessionEnd hook budget. |

- **Validation.** A missing key, or 0, gives the default (for
  `pending_grace_seconds`, the default or its minimum, whichever is
  larger). A negative value, a positive value below the key's safe
  minimum, or a value that is not an integer is refused — never raised
  to the minimum or replaced by the default. Until the file is fixed, every store-backed verb fails
  with `ErrConfigMalformed` naming each refused key, its value and the
  values it allows; `serve` does not start; hooks record nothing and relayed
  permission requests are denied. `help` and `version` (also `--help`,
  `-h`, `--version` and `-v`) still run. A
  misspelt key is ignored, so its default stays in force.
- **Grace period.** Separate from the bound and the stopping window. Two
  settings time a launch, and you need both. `create_timeout_ms` limits
  one tmux call, the one that creates the session.
  `pending_grace_seconds` limits the whole launch: tmux creating the
  session, then Claude starting and checking in with agent-director.
  Until it runs out, `find-missing` leaves the launch alone; after it,
  `find-missing` may mark the launch `missing` (or note it `unreported`
  when its agent runs but has not reported in). So the grace period must
  outlast the create timeout plus a margin, or `find-missing` could give
  up on a launch that is still starting normally. agent-director
  enforces this through its minimum: 30 s, or `create_timeout_ms` +
  `pipe_close_wait_ms` + 20 s (rounded up to whole seconds) when that is
  larger. For example, `create_timeout_ms = 40000` (with
  `pipe_close_wait_ms` at its default) raises the minimum to 61 s, above
  the default 60, so leaving `pending_grace_seconds` unset gives a grace
  period of 61 s, while setting it to 60 is refused: set 61 or more, or
  leave it unset or at 0. The same grace period also bounds how long a
  launch's SessionStart hook waits for agent-director to record the
  launch, but that hook waits at most 540 s, however large the grace
  period. The setting itself has no maximum: `find-missing` still leaves
  a launch alone for the full grace period.
- **When a change takes effect.** The CLI and the TypeScript client: on
  their next call. The MCP server: only after a restart. A Go `Client`:
  when it is built.
- **Setting a value too low.** The six keys with no minimum fail closed:
  healthy calls become `ErrTmuxUnresponsive` refusals or uncertain
  results ("the keys may have been delivered", "the session may have
  been created"), sweeps leave rows unverified or kept, and too low a
  `kill_exit_wait_ms` returns `ErrTmuxKillFailed` for an agent still
  exiting. Lower none of them without cause. A shorter stopping window
  or bound brings `ErrTmuxSessionConflict` sooner for an agent still
  stopping or starting; a longer one only delays a human's attention.
- **Setting a value higher.** Stated worst-case call times hold only at
  the defaults. Raising a timeout, `pipe_close_wait_ms`,
  `sweep_budget_seconds` or `kill_exit_wait_ms` (which counts in full
  toward `kill`'s time) can take a call past the TypeScript client's
  default 30 s call timeout, which then returns `ErrCallTimeout` while
  the verb may still complete — raise TypeScript callers' `callTimeoutMs`
  to match.
- **Agents with a raised SessionEnd hook budget.** Such an agent exits
  only once its SessionEnd hooks finish, so it can take up to the whole
  budget. With a 4 s SessionEnd hook under a budget raised to 10 s,
  agents took up to 4.2 s to exit when a hook's own `timeout` raised it,
  and up to 4.4 s (idle) or 4.3 s (mid-turn) when
  `CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS` did. For such agents, set
  `kill_exit_wait_ms` to at least twice the budget, and keep
  `stopping_window_seconds` at least twice the budget too.
- Callers whose own waits use these values (waiting out the grace
  period, a retry cadence for "still stopping") need the values in
  force: tell them when you change one, including a `create_timeout_ms`
  or `pipe_close_wait_ms` change that moves an unset grace period.
