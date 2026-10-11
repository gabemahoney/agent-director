# Configuration

`~/.agent-director/config.toml` (`install.sh` creates it, or adds to it, unless
`--no-hooks`; all fields optional):

```toml
[defaults]
relay_mode = "off"
expire_retention_days = 31
disable_askuserquestion = false

[relay]
poll_base_ms = 100
poll_jitter_ms = 100
timeout_seconds = 86400
permission_request_cap = 1000   # 0 = unbounded; negative = use default

[pause]
timeout_seconds = 30

[pre_trust]
lock_wait_seconds = 12

[store]
db_path = "~/.agent-director/state.db"
busy_timeout_ms = 10000

[log]
error_log_path = "~/.agent-director/errors.log"

[tmux]   # omit a key (or set 0) for its default
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

## Keys

Whole numbers unless stated; 0 gives the default (not for `[relay] poll_*`).

| Key | Range | Default | What it bounds |
|---|---|---|---|
| `[defaults] relay_mode` | `on`, `off` | `off` | Whether a spawn relays Claude's permission prompts to `decide`; `--relay-mode` overrides it per call. |
| `[defaults] expire_retention_days` | 1 to 106751 | 31 | Days a finished row must have ended before `expire` (without `--older-than`) may delete it. |
| `[defaults] disable_askuserquestion` | `true`, `false` | `false` | Whether every spawn denies Claude's AskUserQuestion tool. |
| `[defaults] inject_help_hook` | `true`, `false` | `false` | Whether every spawn adds a help SessionStart hook; `install.sh` sets `true` unless `--no-hooks`. See [settings](settings.md). |
| `[relay] poll_base_ms` | ms | 100 | Wait between a relay hook's polls for a decision (never under 50 ms; 0 or negative gives that floor plus jitter). |
| `[relay] poll_jitter_ms` | ms | 100 | Random time added to each poll wait; 0 or negative adds none. |
| `[relay] timeout_seconds` | 1 to 2147483 | 86400 | How long a relayed permission request waits for `decide` before it is denied. |
| `[relay] permission_request_cap` | 0 or more | 1000 | Permission requests kept before the oldest closed ones go; 0 is unbounded, negative gives the default. |
| `[pause] timeout_seconds` | 1 to 9223372036 | 30 | How long `pause` waits for the agent to exit. |
| `[pre_trust] lock_wait_seconds` | 1 to 9223372036 | 12 | How long `spawn` and `resume` wait for Claude Code's lock on `.claude.json`. On timeout the launch goes ahead with `pre_trust` `failed`. |
| `[store] db_path` | path | `~/.agent-director/state.db` | Where the store lives (owner-only `0600`; holds the `--extra-env` values `resume` restores). Keep it and `busy_timeout_ms` on one line each: `install.sh` refuses anything else. |
| `[store] busy_timeout_ms` | 1 to 2147483647 | 10000 | How long a call waits for a locked store before it fails. Above about 15 s a busy store can get a launch's SessionStart hook killed, leaving the row `pending` (noted `unreported`). |
| `[log] error_log_path` | path | `~/.agent-director/errors.log` | Where hooks and the CLI append errors (stderr if it cannot be opened). |

## Refused values

A negative, out-of-range, below-minimum or non-integer value is refused, never
defaulted (the `[relay] poll_*` keys and `permission_request_cap` aside); a
misspelt key is ignored. After editing, check the file with
`agent-director list`. What a refusal stops:
[`ErrConfigMalformed`](caller-contract.md).

## When a change takes effect

The CLI and the TypeScript client: on their next call. Each hook: on its next
fire. The MCP server: after a restart. A Go `Client`: when it is built.

## Timing settings (`[tmux]`)

Whole integers, never below the safe minimum.

| Key | Unit | Default | Min | What it bounds |
|---|---|---|---|---|
| `starting_session_seconds` | s | 300 | **60** | Until a session is this old, it counts as "still starting, retry later", not a conflict. |
| `stopping_window_seconds` | s | 90 | **30** | How long after an agent ends it counts as "still stopping, retry later". |
| `pending_grace_seconds` | s | 60 or more | **30** or more | How long `find-missing` leaves a new launch alone, and how long its SessionStart hook waits for the launch to be recorded (at most 540 s). No maximum. |
| `query_timeout_ms` | ms | 1500 | none | Each tmux lookup and pane listing. |
| `action_timeout_ms` | ms | 2000 | none | Each tmux kill, key send and pane capture. |
| `create_timeout_ms` | ms | 5000 | none | The tmux call that creates a session. |
| `pipe_close_wait_ms` | ms | 100 | none | How long a tmux call waits for its output pipes after its process exits. |
| `sweep_budget_seconds` | s | 15 | none | Total tmux time per `find-missing` or `expire` run. |
| `kill_exit_wait_ms` | ms | 5000 | none | How long `kill` waits for the agent process to exit. |

- **Grace period minimum.** 30 s, or `create_timeout_ms` + `pipe_close_wait_ms`
  + 20 s (rounded up) when larger; a lower value is refused. Unset gives 60 s or
  the minimum, if larger.
- **Timeouts.** Too low fails healthy calls as uncertain or
  `ErrTmuxUnresponsive`; too high can exceed the TS client's `callTimeoutMs`.
- **Raised SessionEnd hook budget** (`CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS`,
  or a SessionEnd hook's own `timeout`): set `kill_exit_wait_ms` and
  `stopping_window_seconds` to at least twice the budget.
- **Callers** that wait on these values need to be told when you change one.
