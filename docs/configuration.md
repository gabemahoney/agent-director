# Configuration

`~/.config/agent-director/config.toml` (`install.sh` creates it, or adds to it,
unless `--no-hooks`; all fields optional). `HOME` decides the directory;
`XDG_CONFIG_HOME` is not read. The templates directory
(`~/.config/agent-director/templates/`) lives in the same directory, and a
relative `db_path` or `error_log_path` is joined onto it:

```toml
[defaults]
relay_mode = "off"
expire_retention_days = 31
disable_ask_user = false

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
db_path = "~/.config/agent-director/state.db"
busy_timeout_ms = 10000

[state]
settle_ms = 0   # 0 = off

[launch]   # optional; absolute or "~/" paths; empty or omitted = unset
# claude_path = "/opt/claude/bin/claude"
# codex_path = "/opt/codex/bin/codex"

[log]
error_log_path = "~/.config/agent-director/errors.log"

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
| `[defaults] relay_mode` | `on`, `off` | `off` | Whether a spawn relays the agent's permission prompts to `decide`; `--relay-mode` overrides it per call. |
| `[defaults] expire_retention_days` | 1 to 106751 | 31 | Days a finished row must have ended before `expire` (without `--older-than`) may delete it. |
| `[defaults] disable_ask_user` | `true`, `false` | `false` | Whether every spawn denies the agent its question-asking tool. See [permissions](permissions.md). |
| `[defaults] inject_help_hook` | `true`, `false` | `false` | Whether every spawn adds a help SessionStart hook; `install.sh` sets `true` unless `--no-hooks`. See [settings](settings.md). |
| `[relay] poll_base_ms` | ms | 100 | Wait between a relay hook's polls for a decision (never under 50 ms; 0 or negative gives that floor plus jitter). |
| `[relay] poll_jitter_ms` | ms | 100 | Random time added to each poll wait; 0 or negative adds none. |
| `[relay] timeout_seconds` | 1 to 2147483 | 86400 | The relay hook's time limit: Claude Code ends the hook that long after it starts. A request has this less 3 s to be decided (see [The relay hook's time limit](#the-relay-hooks-time-limit)). |
| `[relay] permission_request_cap` | 0 or more | 1000 | Permission requests kept before the oldest closed ones go; 0 is unbounded, negative gives the default. |
| `[pause] timeout_seconds` | 1 to 9223372036 | 30 | How long `pause` waits for the agent to exit. |
| `[pre_trust] lock_wait_seconds` | 1 to 9223372036 | 12 | How long `spawn` and `resume` wait for Claude Code's lock on `.claude.json`. On timeout the launch goes ahead with `pre_trust` `failed`. |
| `[store] db_path` | path | `~/.config/agent-director/state.db` | Where the store lives (owner-only `0600`; holds the `--extra-env` values `resume` restores); a relative path is joined onto `~/.config/agent-director`. Keep it and `busy_timeout_ms` on one line each: `install.sh` refuses anything else. |
| `[store] busy_timeout_ms` | 1 to 2147483647 | 10000 | How long a call waits for a locked store before it fails. Above about 15 s a busy store can get a launch's SessionStart hook killed, leaving the row `pending` (noted `unreported`). |
| `[state] settle_ms` | 0 to 60000 | 0 | 0 is off. How long a new state must hold before `status`, `get` and `list` show it (see [Settle time](#settle-time-state)). |
| `[launch] claude_path` | absolute path or `~/` path | unset | The program a `claude` spawn, reuse or resume runs; a `~/` path is joined onto `HOME` at load. Empty or unset runs `claude` from the `PATH` of the agents' tmux server; set it when that `PATH` differs from the caller's. A set path runs as written: no `PATH` lookup and no shell parsing, and agent-director does not check the program. A change takes effect at the next launch (spawn, reuse or resume). Keep it on one line (`claude_path = "<path>"`, or single quotes, no backslash): `install.sh` refuses anything else (see [`install.sh` refuses config.toml or a template](operator-actions.md#installsh-refuses-configtoml-or-a-template)). When it names an executable file, `install.sh` runs it for every Claude Code command it issues. |
| `[launch] codex_path` | absolute path or `~/` path | unset | The path of the Codex program, in the same form and under the same refusals as `claude_path`. Checked at load only; no launch runs it. One line, as for `claude_path`. When it names an executable file, `install.sh` counts Codex as installed; it never runs it. Codex installed with npm needs the native program here: see [Requirements](install.md#requirements). |
| `[log] error_log_path` | path | `~/.config/agent-director/errors.log` | Where hooks and the CLI append errors (stderr if it cannot be opened); a relative path is joined onto `~/.config/agent-director`. |

## Refused values

A negative, out-of-range, below-minimum or non-integer value is refused, never
defaulted (the `[relay] poll_*` keys and `permission_request_cap` aside); a
misspelt key is ignored. The old name of `[defaults] disable_ask_user` is
refused with `ErrConfigMalformed`, which names `disable_ask_user`, not ignored.
A `[launch]` value that is not an absolute path or a `~/` path (a blank, a
relative path, `~` alone or `~user/...`), or a `~/` path while `HOME` is unset
or empty, is refused with `ErrConfigMalformed`, which names the key; an empty
value is unset. After editing, check the file with `agent-director list`.
What a refusal stops: [`ErrConfigMalformed`](caller-contract.md).

## When a change takes effect

The CLI and the TypeScript client: on their next call. Each hook: on its next
fire. The MCP server: after a restart. A Go `Client`: when it is built.

## The relay hook's time limit

The relay hook keeps the last 3 s of its time limit for itself: it waits
for `decide` until 3 s before Claude Code would end it, then records and
returns a deny. So a request has `timeout_seconds` less 3 s to be decided,
and a value of 3 or less leaves no time at all: at 3 the request is denied
at once, and at 2 or less the hook gives no answer, so Claude Code asks in
its own permission prompt. An agent keeps the value in force when it was
launched (`spawn` or `resume`).

## Waits that keep their time limits

Three waits keep their time limits whatever `busy_timeout_ms` is:

| Who | Time limit | How it keeps it |
|---|---|---|
| `decide --max-wait-ms N` | the caller's deadline | waits for the store at most until N ms after it started (`ErrStoreBusy`, nothing recorded) |
| the relay hook | Claude Code ending it | each write waits for the store at most until 2 s before that end; if the store stays busy, its first write gives way to a deny, and any later one to no answer, so Claude Code asks in its own permission prompt |
| `get`, `list`, `get-permission` | the caller's | never wait for the store's write lock |

`decide` without `--max-wait-ms` waits up to `busy_timeout_ms` per write,
and the other hooks can still be killed by a long store wait.

## Settle time (`[state]`)

While an agent uses tools, its state can flip between `working` and
`waiting` several times a second. `[state] settle_ms` makes `status`,
`get` and `list` (CLI, MCP and the TypeScript client) show a new state only
once it has held:

- **Range.** Whole milliseconds from 0 to 60000. A missing key, or 0, turns
  it off: every read shows the latest state. A negative value or one above
  60000 is refused, never capped (see [Refused values](#refused-values)).
- **What `state` shows.** With a settle time T, `state` moves to a new
  value only once that value has held for T; until then it shows the last
  settled value. Every change of value restarts the wait; a repeat of the
  same state does not. No field or state value is added, and nothing marks
  a state as late. `list --state` filters on the state `list` shows.
- **Flaps never show.** A change that comes back within T is never seen: a
  working agent that reads `waiting` for a moment reads `working` throughout.
- **At once.** `ended` and `missing`, and a state a command sets (`spawn`,
  `resume`, `find-missing`), show at once. A row that its own agent moves
  out of `missing` or `ended` waits like any other change: it reads
  `missing` or `ended` for T, then the new state.
- **Short turns are invisible.** A settled `waiting` agent that gets a
  message and replies in less than T reads `waiting` throughout. Keep T at
  1 to 2 s (1000 to 2000), shorter than a real turn; most flips come back
  within 1 s.
- **Every caller on the host.** The setting covers every caller on the
  host, programs that never asked for it included, and each can see a
  state up to T late: for example, a new agent reads `pending`, without
  `launch_started_at`, for up to T after it reports in.
- **Verbs act on the live state.** `send-keys`, `decide`, `pause`,
  `resume`, `kill`, `find-missing`, `expire` and `spawn` judge a row's
  latest state, which can be newer than what `status` shows, so a verb can
  refuse on a state `status` does not show yet; the refusal is a named
  error. Permission requests are never delayed: a row can read any state,
  even `ended` or `missing`, while `get` lists an open request, or
  `check_permission` with none, for up to T.
- **Reads never wait** for a state to settle. A change of the key takes
  effect as [any other](#when-a-change-takes-effect).

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
