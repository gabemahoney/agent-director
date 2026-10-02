# agent-director

Supervise long-running Claude Code sessions ("Spawns") from a script,
another Claude, or an MCP-capable LLM client. One Go binary; one
SQLite file; everything else is tmux.

## What you get

- A **CLI** with one verb per supervision action — deterministic JSON
  on stdout, typed errors on stderr.
- An **MCP server** (`serve --stdio`) that exposes the supervision
  verbs to an LLM client — process-internal verbs (`hook`, `serve`,
  `trail-emit`) stay CLI-only. See
  [`docs/mcp-reference.md`](docs/mcp-reference.md) for the authoritative
  tool list.
- A **Go library** (`github.com/gabemahoney/agent-director/pkg/api`) —
  typed client for all verbs, no subprocess or network hop. See
  [`pkg/api/README.md`](pkg/api/README.md).
- A **TypeScript/Bun client** (`agent-director` on npm) — same API
  surface as the Go library. The Client spawns the bundled CLI binary
  as a subprocess per verb call (no FFI, no network hop). Consumed at
  Bun runtime via ESM; not designed to be webpacked. See
  [`pkg/ts-bun-client/README.md`](pkg/ts-bun-client/README.md).
- A **permission relay** (`relay_mode=on` + `decide`) so an orchestrator
  can intercept `PreToolUse` permission prompts and answer
  allow/deny out-of-band.
- A **persistent session model** — pause / resume preserves the
  JSONL transcript across Claude sessions.
- **Crash-recovery verbs** you schedule yourself — `find-missing` +
  `expire`: `find-missing` judges each live row by its
  agent's process, checks tmux when that process cannot be read, and marks
  the row `missing` or leaves it unverified. `missing` is the sweep's
  judgement on the evidence available to it, not proof that the agent has
  exited. `expire` removes old finished rows whose agent is gone and keeps
  those it cannot prove gone.

## 5-minute install

### Prerequisites

- `claude` (Claude Code) 2.1.280 or later on PATH — install per
  <https://claude.com/claude-code>. agent-director launches only
  Claude Code; other agent CLIs are unsupported in this release. With an
  older Claude Code the agent's hooks may apply nothing: rows stay
  `pending` and the trail records `ad.hook.ignored` with reason
  `no_exec_form`; upgrade Claude Code.
  - The `claude` on PATH must be the agent process itself: Claude Code's
    native binary, or a wrapper that `exec`s it. A hook moves a row only
    when its parent process is the pane's own process, so a launcher or
    shim that runs Claude Code as a child (for example a JS launcher left
    by an install that skipped its scripts, or a version-manager shim)
    makes every hook `ad.hook.ignored` with reason `pid_mismatch`, and the
    rows stay `pending` (see
    [A row stays `pending` and the trail shows `pid_mismatch`](#a-row-stays-pending-and-the-trail-shows-pid_mismatch)).
- `tmux` 3.2 or later on PATH. Verified: 3.2a (by a scripted one-off
  run and recorded replies) and 3.3a (by the test suites).
  - Keep `remain-on-exit` off, the tmux default. With it on, a finished
    agent's session outlives its agent: `resume` and `spawn
    --reuse-finished` get `ErrTmuxUnresponsive`, then
    `ErrTmuxSessionConflict`, and `expire` keeps the row on every run.
    `find-missing` still marks a row whose agent process is dead,
    whatever tmux shows.
  - Keep `exit-empty` on, the tmux default. With it off, a server left
    with no sessions keeps running and reads to agent-director as "a
    different server" (`ErrTmuxNotAvailable`) until a session is created
    on it again; until then `resume`, reuse and `kill` of a `pending` row
    refuse, and `expire` keeps rows.
  - No tmux server needs to be running: agent-director starts one when
    it launches an agent.
- `jq`, `sqlite3` and `file` on PATH (the installer checks for them).

### Install the CLI

agent-director is a single Go binary. Install it on a fresh machine
with one copy-pasteable command:

```sh
curl -fsSL https://raw.githubusercontent.com/gabemahoney/agent-director/main/skills/install-agent-director/install.sh | bash -s -- --from-release
```

That fetches `install.sh` from `main`, then runs it with
`--from-release` so it auto-detects your OS/arch, downloads the
matching binary from the [latest GitHub release](https://github.com/gabemahoney/agent-director/releases/latest),
sets up `~/.agent-director/`, drops a PATH symlink, brings
`state.db` to the current schema (creating it on a fresh host,
or upgrading it in place; a schema problem fails the install
loudly rather than half-installing), and installs the
SessionStart/SessionEnd help hooks.

Optionally pass `--register-mcp` to also register the stdio MCP
server, or `--no-hooks` to leave `~/.claude/settings.json` untouched.

If you'd rather download the binary yourself first (and then point
the installer at it), grab the asset for your platform from the
[latest release](https://github.com/gabemahoney/agent-director/releases/latest):

```sh
# Linux amd64:
curl -L -o agent-director https://github.com/gabemahoney/agent-director/releases/latest/download/agent-director-linux-amd64
# Linux arm64:
curl -L -o agent-director https://github.com/gabemahoney/agent-director/releases/latest/download/agent-director-linux-arm64
# macOS Apple Silicon:
curl -L -o agent-director https://github.com/gabemahoney/agent-director/releases/latest/download/agent-director-darwin-arm64

chmod +x agent-director
bash skills/install-agent-director/install.sh --binary ./agent-director
```

Optional flags:

- `--register-mcp` — register the stdio MCP server with Claude Code.
  To register it yourself instead, run
  `claude mcp add agent-director ~/.agent-director/bin/agent-director serve --stdio`
  (or give the path of your binary).
- `--symlink-dir <dir>` — override the default PATH-symlink directory.
- `--binary <path>` — install from an explicit source binary.
- `--from-release [tag]` — download a pre-built binary for this host's
  OS/arch from GitHub Releases (latest, or a specific tag) and install
  it. Pair with `--sha256 <hex>` to verify the download.

#### From inside Claude Code

If you already have Claude Code running, just say:

> **install agent-director**

That triggers the `install-agent-director` skill, which walks you
through four choices interactively (binary source, PATH symlink,
MCP registration, persistent help hooks) and then runs `install.sh`
with the resolved flags.

The skill ships in this repo at
[`skills/install-agent-director/`](skills/install-agent-director/SKILL.md)
and is auto-discoverable by Claude Code if you've cloned the repo
under a directory it indexes.

Upgrading is the same skill: re-running the install brings an
existing `state.db` up to the current schema automatically. If a
session ever reports a schema-version error, re-run the install to
resolve it. If the store is newer than the binary, install the
matching agent-director release instead. If a command that opens the
store still reports a schema error after the install has run, restore
the copy of `~/.agent-director/state.db` (with its `-wal` and `-shm`
files) taken before the install, then re-run the install.

### Install the TS client

The `agent-director` npm package is the **TypeScript/Bun client only**
— it shells out to a separately-installed CLI binary on the host.
Installing the npm package does NOT install the CLI; you must run the
one-liner above (or otherwise drop the CLI on the host) first.

```sh
bun add agent-director
```

See [`pkg/ts-bun-client/README.md`](pkg/ts-bun-client/README.md) for
client API details. The TS package ships zero lifecycle scripts and no
native or optional platform dependencies.

#### From source (contributors)

If you've cloned this repo and want to install the binary you just
built:

```sh
make build
bash skills/install-agent-director/install.sh --binary ./bin/agent-director
```

`install.sh` uses `agent-director version` (its `{version, commit}`
stamp) to compare the local binary with the source tree: it checks the
binary's commit against `git rev-parse HEAD` and refuses option
`--binary` if the artifact is stale — re-run `make build` to refresh it.

To run the test suite without touching your `~/.agent-director`:

```sh
make test-sandbox
```

See `docs/engineering-guide.md` "Sandboxed execution" for when this is
required and the pod-quirk flags it uses.

### First spawn

```sh
id=$(agent-director spawn --cwd "$PWD" | jq -r '.claude_instance_id')

agent-director list --state waiting

agent-director send-keys --claude-instance-id "$id" --text "what is 2+2?"

agent-director read-pane --claude-instance-id "$id"

agent-director pause --claude-instance-id "$id"
```

A row is `pending` while its launch is in progress and its agent has not
reported in yet (it may be loading or waiting at a startup prompt); it
becomes `waiting` once the agent reports in.

`spawn` marks the folder as trusted so the agent does not stop at Claude
Code's folder-trust prompt; add `--no-pre-trust` to keep the prompt.

#### Naming the tmux session yourself

`spawn` accepts `--tmux-session-name <name>` so callers, test harnesses,
or operators debugging by hand can pick a readable session name instead
of the default `<basename(cwd)>-<id[:8]>`:

```sh
agent-director spawn --cwd "$PWD" --tmux-session-name status-agent
```

Rules (validated app-side, no silent rewrite): the name must be
non-empty, ≤ 64 bytes, valid UTF-8, and contain none of `#`, `:`,
`.`, `$`, `\`, or ASCII control bytes (`\x00`–`\x1f`, `\x7f`). There
is no DB uniqueness check — name reuse across **ended** spawns is supported.
A name already held by a tmux session makes `spawn` return an error
naming the blocking session and whether its label carries this instance
id; the new row is ended and agent-director never touches the holder. Omitting
the flag preserves today's `composeSessionName` default.

#### Finding a Spawn by its tmux session name

`list` accepts `--tmux-session-name <name>` to narrow rows by the
column verbatim — useful when the operator picked the name (above)
and wants to round-trip back from `tmux ls` to the persisted Spawn
without consulting the id:

```sh
agent-director list --tmux-session-name status-agent
```

The filter is exact-match, AND-combines with `--state`, `--label`,
`--parent`, `--cwd`, and `--limit`, and returns both live and ended
rows whose `tmux_session_name` matches — name reuse across ended
spawns is supported, so a single name can correlate to multiple
historic rows. Omitting the flag preserves today's permissive
behavior.

## Common workflows

### Drive a Spawn from another Claude

Install with `--register-mcp`. Inside the orchestrating Claude the
verbs appear as `mcp__agent-director__spawn`,
`mcp__agent-director__send_keys`, etc.

### Intercept permission prompts

```sh
id=$(agent-director spawn --cwd "$PWD" --relay-mode=on \
       | jq -r '.claude_instance_id')

# When Claude hits a PermissionRequest, state goes to check_permission.
# Extract the request_token from the open permission request, then decide.
token=$(agent-director get --claude-instance-id "$id" \
          | jq -r '.permission_requests[0].request_token')
agent-director decide --claude-instance-id "$id" \
    --request-token "$token" \
    --decision allow --reason "tool is on the allow-list"
```

After the row is closed, retrieve the verdict by `request_token`:

```sh
agent-director get-permission --request-token "$token"
```

The response is a single permission row:

```json
{
  "request_token": "9b1d8a8e-3a3d-4f4f-8a8e-3a3d4f4f8a8e",
  "request_id": 42,
  "tool_name": "Bash",
  "tool_input": "{\"command\":\"npm test\"}",
  "requested_at": "2026-05-31T12:00:00Z",
  "decision": "allow",
  "decision_reason": null,
  "decided_at": "2026-05-31T12:00:01Z"
}
```

### Templates

```sh
agent-director make-template --name dev --cwd /repos/widget \
    --label project=widget --allow 'Bash(npm test)'

agent-director spawn --template dev --label run=$(date +%s)
```

Pass `--overwrite` to replace any existing template atomically; without the flag, an existing template causes `ErrTemplateExists`.

## Configuration

`~/.agent-director/config.toml` (created on first run; all fields
optional):

```toml
[defaults]
relay_mode = "off"
expire_retention_days = 31
disable_askuserquestion = false

[relay]
poll_base_ms = 100
poll_jitter_ms = 100
timeout_seconds = 86400
permission_request_cap = 1000   # 0 = unbounded; negative = use default (1000)

[pause]
timeout_seconds = 30

[store]
db_path = "~/.agent-director/state.db"

[log]
error_log_path = "~/.agent-director/errors.log"

[tmux]   # timing settings: omit a key (or set it to 0) to use its default
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

Env vars passed at spawn time (via `--extra-env`) are stored in
`state.db` so `resume` can restore them. The file is owner-only (`0600`
in a `0700` directory).

### Timing settings (`[tmux]`)

> **Warning:** a refused or malformed value stops agent-director — the
> CLI from its next call, each hook from its next fire, the MCP server
> from its next start (a server already running keeps its old values).
> After editing the file, check it at once with `agent-director list`.

Each value is a whole integer in the unit its key names. Every default
can be changed, but never below its safe minimum.

| Key | Unit | Default | Safe minimum | What it bounds |
|---|---|---|---|---|
| `starting_session_seconds` | s | 300 | **60** | Starting-session bound: until a finished row's own tmux session is this old, it counts as "still starting, retry later" rather than a conflict needing a human. Claude Code reports SessionStart within seconds. |
| `stopping_window_seconds` | s | 90 | **30** | Stopping window: how long after an agent ends it counts as "still stopping, retry later". Covers Claude Code's SessionEnd hook budget plus teardown. |
| `pending_grace_seconds` | s | 60 | **30**, or more (see below) | Grace period: how long `find-missing` leaves a launch alone after it starts, and how long the launch's SessionStart hook waits for agent-director to record the launch (never more than 540 s). The setting has no maximum. |
| `query_timeout_ms` | ms | 1500 | none | Each tmux lookup and pane listing. |
| `action_timeout_ms` | ms | 2000 | none | Each tmux kill, key send and pane capture. |
| `create_timeout_ms` | ms | 5000 | none | The tmux call that creates a session (`spawn`, `resume`). |
| `pipe_close_wait_ms` | ms | 100 | none | How long a tmux call waits for its output pipes after its process exits. |
| `sweep_budget_seconds` | s | 15 | none | Total tmux time per run of `find-missing` and `expire`. |
| `kill_exit_wait_ms` | ms | 5000 | none | How long `kill` waits for the agent process to exit after killing its pane. Set from measured exit times under Claude Code's default SessionEnd hook budget. |

- **Validation.** A missing key, or 0, gives the default. A negative
  value, a positive value below the key's safe minimum, or a value that
  is not an integer is refused — never raised to the minimum or replaced
  by the default. Until the file is fixed, every store-backed verb fails
  with `ErrConfigMalformed` naming each refused key, its value and its
  minimum; `serve` does not start; hooks record nothing and relayed
  permission requests are denied. `help` and `version` still run. A
  misspelt key is ignored, so its default stays in force.
- **Grace period.** Separate from the bound and the stopping window. It
  must outlast the time from a launch's start until its tmux session
  exists, which agent-director enforces through its minimum: 30 s, or
  `create_timeout_ms` + `pipe_close_wait_ms` + 20 s (rounded up to whole
  seconds) when that is larger. Raising either can refuse a grace period,
  the default included: `create_timeout_ms = 40000` raises the minimum to
  61, so the default 60 is refused until `pending_grace_seconds` is set
  to 61 or more. The same grace period also bounds how long a launch's
  SessionStart hook waits for agent-director to record the launch, but
  that hook waits at most 540 s, however large the grace period. The
  setting itself has no maximum: `find-missing` still leaves a launch
  alone for the full grace period.
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
  period, a retry cadence for "still stopping") need the configured
  values: tell them when you change one.

## Maintenance

Two verbs keep `state.db` honest. Schedule both yourself, and run them
with the same user and tmux environment as the agents:

```sh
# Mark live rows whose agent process is gone as `missing` — run often (e.g. every 2 min):
agent-director find-missing

# Delete finished rows older than expire_retention_days whose agent's process
# is not running and whose agent has no tmux session; keep the others:
agent-director expire
```

Wire these into your platform's scheduler (launchd, systemd timer, cron,
Task Scheduler). Without `find-missing`, dead sessions linger in `list` as
stale `waiting`/`working` rows.

`find-missing` judges each live row by its agent's process: a row whose
process runs stays live, and a row whose process is gone is marked
`missing`. Only when the process cannot be checked does it ask tmux, on the
row's recorded socket: the row is marked `missing` when tmux shows no
session of its current launch, and otherwise left unverified; a row whose
recorded tmux session name cannot be used is never looked up, but left
unverified with a note of its own. A `pending`
row is not judged until its grace period has passed. Marked rows are
listed in `ids`; rows it cannot decide are listed in `unverified_ids`, and
each carries `liveness_unverified_since` and a `liveness_note` in `list`
and `get`. `missing` is the sweep's judgement on the evidence available to
it, not proof that the agent has exited. A run as another user, as root or
against another tmux server can mark live rows `missing`.

Finished rows (`ended` or `missing`) are never removed on their own: an
`expire` you schedule removes them at the default retention, and the
deprecated `delete` (not a cleanup or recovery step) removes one row when
asked; a row's state alone never gets it deleted. `expire` first keeps
every row whose recorded tmux session name cannot be used, on every run,
without reading its process or asking tmux (see "A row whose recorded name
cannot be used" under [Operator actions](#operator-actions)). For every
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

Session rotations are archived automatically, so `resume` recovers them on
its own — no manual step is needed.

When `resume` cannot bring the conversation back (for example it reports
no session id, or that the transcript was never written or is missing),
spawn the id again, opting in to reuse:

```sh
agent-director spawn --cwd <dir> --claude-instance-id <id> --reuse-finished
```

The new agent starts with no memory of the old conversation. `delete` is
not a recovery step.

agent-director does not restart sessions for you. Deciding when to run
`find-missing` then `resume` after a boot — from a startup script,
service, or scheduler — is up to you.

## Caller contract

For programs and agents that call agent-director. The full contract, with
the class of every tmux error, is in
[Caller contract: tmux refusal classes](docs/architecture.md#caller-contract-tmux-refusal-classes).

- Only a GONE answer means a row's session is not there. Every other tmux
  error means "don't know", never "dead".
- Never `delete` a row after a `kill` that did not succeed.
- The row state, kept honest by `find-missing`, is the liveness authority;
  there is no liveness verb.
- `read-pane` changes nothing and is not a liveness check; a caller polling
  a launch polls `status`, and calls `read-pane` only while the row is
  `pending`, about once a second at most.
- Run as the same user and in the same tmux environment as the agents.
- A situation that needs a human is described in
  [Operator actions](#operator-actions); callers never perform those
  actions.
- A `spawn` whose tmux session name is already held returns an error naming
  the holder and ends its new row; the holder is left to a human, never
  ended by the caller.
- A call refused before anything was written or sent is safe to re-issue
  later; that is how a caller learns a condition has cleared. Retry
  `ErrTmuxUnresponsive` and `ErrTmuxKillFailed` with backoff and a cap, and alert at the cap
  (a timed-out launch is the exception below).
- A `resume` or `spawn --reuse-finished` refused because the row's own
  session or agent "appears to still be stopping" or "starting": wait and
  retry. Refused with "this row's own id": stop and surface the named
  session to a human ([Operator actions](#operator-actions)); never end it
  yourself.
- After a timed-out `spawn` or `resume` the row stays `pending`: do not
  retry until `get` shows it `ended` or `missing`.
- `pending` means a launch is in progress; `status`, `get` and `list` show
  its start (`launch_started_at`). `kill` on a `pending` row aborts a stuck
  launch.
- `ErrConfigMalformed` means agent-director cannot use its config file:
  take no action, alert once, never read it as "dead".
- A reused id starts with no memory of its earlier lives.
- Detect features by the binary's version (`agent-director version`, the
  MCP `version` tool, or the TypeScript client's `binaryVersion`); a
  release candidate `X.Y.Z-rc.N` counts as `X.Y.Z`.
- The contract also lists this release's
  [known limitations](docs/architecture.md#known-limitations) and
  [meaning changes](docs/architecture.md#meaning-changes).
- Right after a `kill` ends the last session on its tmux server, the
  server takes a moment to exit. A repeated `kill` in that moment can get
  `ErrTmuxUnresponsive` or `ErrTmuxNotAvailable`; the caller waits and
  checks again.

To end a live row (`pending` included) and relaunch its id, a caller
follows this bounded, paced sequence:

1. `agent-director kill --claude-instance-id <id>`, and check the result;
   on any error follow its class and never delete the row.
2. If the row is `pending`, wait until its launch start
   (`launch_started_at`, shown by
   `agent-director status --claude-instance-id <id>`) plus the pending
   grace period (60 s unless the operator configured another value) has
   passed; a pending row inside its grace period means wait and check
   again later, never escalate.
3. Run `agent-director find-missing`, then confirm with
   `agent-director status --claude-instance-id <id>` (or `get`) that the
   row is `ended` or `missing`; if not, wait about 5 s and repeat, up to
   three `find-missing` runs in all.
4. If still live, `kill` once more, wait about 5 s, run `find-missing` once
   more and check.
5. If still live, stop and escalate to a human.
6. Once the row is `ended` or `missing`, resume it if it has a session id
   and the caller wants the conversation back
   (`agent-director resume --claude-instance-id <id>`); otherwise spawn with
   `--reuse-finished`
   (`agent-director spawn --claude-instance-id <id> --reuse-finished --cwd <dir>`).
   A caller whose ids agent-director mints spawns fresh instead of reusing.
   A `pending` row, a resumed one included, enters this sequence, so a
   stuck `resume` handled this way can still get its conversation back; a
   reuse makes that conversation unreachable for good, because a row's
   session history belongs to one life and a reuse starts a new one.

## Operator actions

These actions are for humans only: automated callers (programs, scripts,
agents, MCP clients) must not perform them.

Run every command as the agents' user. Every tmux command names the row's
socket with `-S '<socket>'`: the `tmux_socket` that
`agent-director get --claude-instance-id <id>` shows (a row that records
none uses the socket of your tmux environment, by default
`/tmp/tmux-<uid>/default`). Keep the single quotes, so the shell leaves the
`$` of a session id alone. A session's environment is never evidence of
whose it is. Trail records are lines of `~/.agent-director/ad-trail.jsonl`.
Never `delete` a row after a `kill` that did not succeed.

### This store's id

The last field of a session's `@ad_owner` label, and the `store_id` of a
trail record, is a store's id. Read this store's own id directly, as the
agents' user:

```sh
sqlite3 -readonly ~/.agent-director/state.db "SELECT value FROM store_meta WHERE key = 'store_id'"
```

It prints 16 lowercase hexadecimal characters and changes nothing (if your
config sets another `db_path`, use that file). `sqlite3` is already a
prerequisite of `install.sh`. This works in a store that has no
`ad.launch.name_held` record; where one exists, its `store_id` is the same
value. No verb changes the id. A store taken back to schema v4, by the
downgrade recipe or by restoring a copy from before the install, gets a new
id when it is migrated again; trail records written before then carry the
old one.

**Checking a label.** A valid `@ad_owner` label has five fields: `ad1`, a
launch token, the session's own id (`$N`), the row's id and, last, the
store id. Compare that last field with the id above. A label whose last
field is a different id belongs to another agent-director store's agent,
even when it names this row's id: never end it from this store (see "A
session of another agent-director store"). Every item below that checks a
label applies this rule.

### A finished row's own old session

`resume`, or a `spawn` with `--reuse-finished`, refuses with
`ErrTmuxSessionConflict` ("this row's own id"): the row is `ended` or
`missing`, but its own session still runs past the stopping window and the
starting-session bound, or no session of it is found while its agent
process still runs. If, after looking at it (steps 1 to 3 of the
leftover item below), you want that session or agent gone, end it with the
finished-row opt-in on `kill`:

```sh
agent-director kill --include-finished --claude-instance-id <id>
```

It ends the agent's pane and the row's labelled session, then waits for the
agent process (or, when the process cannot be checked, looks the session up
once more), and succeeds with `kill_sent` true once the agent process is
gone, otherwise `ErrTmuxKillFailed` (retry later; never `delete` the row).
When that second lookup cannot answer, it returns `ErrTmuxUnresponsive` or
`ErrTmuxNotAvailable` with `kill_sent` true: the kill was sent and may or
may not have taken effect, so check again later.
The row's state and every other field stay unchanged, so the
conversation stays resumable: run the refused `resume` (or the
`spawn --reuse-finished`) again.

It ends a session only if that session reported in to the row: the row
records the agent's process id (the agent's SessionStart reached
agent-director for the row's latest launch) and the session was created, in
whole seconds, before the row finished. A session created in the same
second, a row with no finish time, and an agent whose SessionStart could not
record its process are refused too. A row the opt-in acts on was finished by
its own agent, by `find-missing`, a restore, a plain spawn's end write or a
hand edit, never by a leftover or a nested `claude` carrying the id:
agent-director ignores hooks that do not come from the row's own agent, so a
working agent's row stays live and gets the live-row refusal below.

Every other answer sends no kill and changes nothing:

- `ErrSpawnNotResumable` ("the row is live"): the row is live (`pending`
  included); no lookup was made. To end a live agent, or to abort a launch
  stuck at a startup prompt, run `agent-director kill --claude-instance-id <id>`
  without the opt-in.
- `ErrInternal`: the row's recorded tmux session name cannot be used; no
  lookup and no tmux call were made. See "A row whose recorded name cannot
  be used".
- `ErrTmuxUnresponsive` ("appears to still be stopping" or "appears to
  still be starting"): the row ended less than the stopping window ago, or
  the session is younger than the starting-session bound (the configured
  `stopping_window_seconds` and `starting_session_seconds`). Wait and run
  it again.
- `ErrTmuxSessionConflict` ("never reported in"): the row's own session
  past both, or a leftover of an earlier launch (the error names its session
  and its id, `$N`), that never reported in to the row. `send-keys` refuses
  a finished row, so ending the session is your decision: check its
  ownership and end it by hand by its session id, as steps 1 to 4 of
  "A leftover, or a session with no valid label, or one that never reported in on a finished row"
  below describe, then run the refused `resume` (or the
  `spawn --reuse-finished`) again.
- `ErrTmuxSessionConflict` ("conflicting labels"): see "A stray `@ad_owner`
  value or a duplicate label".
- `ErrTmuxNotAvailable` before any kill: tmux could not be
  run or its socket is not accessible, or the socket reaches another server
  ("not the tmux server the agent was launched on"; see "A row on a
  different tmux server").
- `ErrTmuxUnresponsive` otherwise, before any kill: tmux did not answer
  usably; retry later.

If no session carries the row's current label, it acts as on a live row:
success with `kill_sent` false unless the agent process still runs; a
surviving agent pane is ended and the process waited for; otherwise
`ErrTmuxKillFailed` (see "An agent process that runs with no session or
pane of its launch", which also covers a process that outlives the kill).

`kill_sent` is true exactly when a pane or session kill was sent. The
`ad.kill.called` trail record shows whether the opt-in was set
(`include_finished`); a refusal records `kill_sent` false, except an error
that follows a sent kill:

```sh
jq -c 'select(.event == "ad.kill.called" and .claude_instance_id == "<id>") | {include_finished, kill_sent, lookup_outcome, outcome}' ~/.agent-director/ad-trail.jsonl | tail -n 1
```

The opt-in exists on the CLI and in the Go (`KillParams.IncludeFinished`)
and TypeScript (`include_finished`) client libraries, not over MCP: an MCP
`kill` on a finished row is always the no-op success. Detect it by the
version of the binary that serves you. CLI: the `version` verb. TypeScript:
`binaryVersion` from `Client.create()` or the `version` that
`resolveSystemBinary()` returns, never `version()` (the npm package's
version). Go: at compile time. Over MCP the opt-in never exists, whatever
the MCP `version` tool (the running `serve` process's own version) reports.
It exists from 0.11.0 on; a release candidate `0.11.0-rc.N` sorts before
0.11.0 but counts as 0.11.0. Development builds (`0.0.0-dev` from `make`,
which the TypeScript client accepts as its sentinel; `dev` from a plain
`go build`, which it rejects) cannot be compared, so expect the
older-binary answer: `ErrInvalidFlags` on the CLI and in the TypeScript
client.

Accepted risks: on a row wrongly marked `missing` (a hand edit of the
store, or an agent-director process from before the install) it ends a
healthy agent whose row says finished; the conversation stays resumable,
and you chose it. The checks above fail closed, so some sessions you may
want gone are refused and left to you. A live row is always refused. An
agent that learns of the opt-in, for example from this README, could run
it; nothing enforces that only humans do.

### A leftover, or a session with no valid label, or one that never reported in on a finished row

A leftover is a session of an earlier launch of the agent: its label names
the row's id with another launch token. Whether the row is `pending` or
live, `agent-director kill` refuses it with `ErrTmuxSessionConflict` ("not
this launch's session"), sends nothing and names the session's id (`$N`).
While the current launch's session runs, `read-pane` of the id shows the
agent's pane. With no session of the current launch, it shows the
leftover's pane if there is one leftover (not the agent's), and answers
`ErrTmuxSessionConflict` if there are several. The leftover never keeps the
row live: its hooks change nothing on the row, and `find-missing` judges
the row by its own agent's process. On an `ended` or `missing` row,
`kill --include-finished` answers a leftover, or the row's own session that
never reported in to it, with `ErrTmuxSessionConflict` ("never reported
in") and sends no kill; these steps end such a session by hand. A session
with no valid label may be a person's own, so look before acting.

1. Find the session and note its `session_created`:

   ```sh
   tmux -u -S '<socket>' list-sessions -F '#{session_id} #{session_created} #{session_name}'
   ```

2. Check its label:

   ```sh
   tmux -u -S '<socket>' show-options -t '<session id>' -v @ad_owner
   ```

   Read it as [This store's id](#this-stores-id) says: its last field must
   be this store's id (the `store_id` of an `ad.launch.name_held` record),
   as in every leftover `kill` names. A session with no label prints
   nothing
   (`invalid option: @ad_owner` on tmux 3.3a); an invalid label prints
   something malformed. `no such session` means it has gone.
3. Look at it read-only (detach with the tmux prefix key, then `d`):

   ```sh
   tmux -u -S '<socket>' attach-session -r -t '<session id>'
   ```

4. If it is not wanted, end it by its session id, never by its name (a name
   target can match another session whose name begins with it):

   ```sh
   tmux -u -S '<socket>' kill-session -t '<session id>'
   ```

   If its window is shared with another session (a grouped session or a
   linked window), ending the session leaves the program in its pane
   running: end the pane by its id instead, taken from the listing:

   ```sh
   tmux -u -S '<socket>' list-panes -a -F '#{pane_id} #{pane_pid} #{session_id} #{session_name}'
   tmux -u -S '<socket>' kill-pane -t '<pane id>'
   ```

5. `agent-director find-missing` marks the row once its agent process is
   gone. Then resume the id (if the row has a session id and the
   conversation is wanted) or spawn it again with `--reuse-finished`.

On a `pending` row, first wait until `find-missing` marks the row `missing`
(after the pending grace period), then, before step 4, list the sessions
again and check that the session id still shows the `session_created` you
noted; afterwards spawn the id with `--reuse-finished`.

When `resume`, or a `spawn` with `--reuse-finished`, refuses with
`ErrTmuxSessionConflict` ("left over from an earlier life" or "no valid
instance id"), the row stays `ended` or `missing` (after "duplicate
session" the error says whether the row was restored). The error names the
session and its id (`$N`); the socket is the row's `tmux_socket`. For a
leftover, handle each session it names as in steps 2 to 4 (if it says "and
N more", find the others with the listing of step 1). For "no valid
instance id", look first (steps 2 and 3): it may be a person's own session;
end it as in step 4 only if it is not wanted. Then run the refused command
again, whichever it was:

```sh
# if resume was refused
agent-director resume --claude-instance-id <id>
# if the spawn with --reuse-finished was refused
agent-director spawn --cwd <dir> --claude-instance-id <id> --reuse-finished
```

### A row whose recorded name cannot be used

A row's recorded tmux session name cannot be used when it is empty, holds a
control character, or holds `.`, `:` or bytes that are not valid UTF-8.
This includes a default name made by releases before 2026-05-27, which kept a `.`
from its id: the id `b.18k-fix` gave `<folder>-b.18k-fi`. agent-director
never touches such a row's session: every verb that would look the row up
returns `ErrInternal` with no tmux call, `find-missing` leaves a live row
unverified with a note of its own when the row's process cannot decide it,
and `expire` keeps a finished row on every run. A human removes such a row,
working as the agents' user and against their tmux server, in this order:

1. Identify the row and its recorded name. On a live row,
   `agent-director list` shows the `liveness_note` `tmux_session_name_empty`,
   `tmux_session_name_control_char` or `tmux_session_name_rewritten`. A
   finished row is in `expire`'s `kept_ids` on every run, and its
   `ad.expire.kept` trail record gives the reason `empty_session_name`,
   `control_char_session_name` or `rewritten_session_name`:

   ```sh
   jq -c 'select(.event == "ad.expire.kept" and .claude_instance_id == "<id>") | {reason, tmux_session_name}' ~/.agent-director/ad-trail.jsonl | tail -n 1
   ```

   Every verb that would look the row up answers `ErrInternal`, and `list`
   shows every row's recorded name (`tmux_session_name`). For the exact
   name, use `read-pane`, which changes nothing:

   ```sh
   agent-director read-pane --claude-instance-id <id>
   ```

   Its `ErrInternal` quotes the recorded name, writing each control
   character and each byte that is not valid UTF-8 as an escape. `list`,
   `get` and the `ad.expire.kept` record cannot show such a byte: their JSON
   replaces it.
2. Work out the name tmux stores and find the candidate sessions. In the
   stored form, `.` and `:` become `_`, a byte that is not valid UTF-8
   becomes a backslash and three octal digits (byte 0xff becomes `\377`),
   and a control character becomes its escape (tab `\t`, newline `\n`, ESC
   `\033`). A `\` of the name is stored as `\\`, and a `$` followed by a
   letter, `_` or `{` as `\$`, so `mix.$b` is stored as `mix_\$b` (tmux 3.2
   and 3.3; other versions escape `$` differently, so compare by eye). List
   the sessions and note the session id (`$N`) of each one whose name equals
   the stored form:

   ```sh
   tmux -u -S '<socket>' list-sessions -F '#{session_id} #{session_created} #{session_name}'
   ```

   An empty name has no stored form, so every listed session is a
   candidate.
3. Confirm ownership by hand. Such a row's session usually carries no
   label. For each candidate, check its label:

   ```sh
   tmux -u -S '<socket>' show-options -t '<session id>' -v @ad_owner
   ```

   If it prints nothing, or `invalid option: @ad_owner` (tmux 3.3a), the
   session has no label.

   A label marks the session as this row's only if it is a valid label (see
   [This store's id](#this-stores-id)) whose session id is the `$N` you
   passed to `-t`, whose row id is this row's and whose last field is this
   store's id (the `store_id` of an `ad.launch.name_held` record). For a
   session with no label, its
   environment is a hint only, because environments are inherited:

   ```sh
   tmux -u -S '<socket>' show-environment -t '<session id>' AGENT_DIRECTOR_INSTANCE_ID
   ```

   Only a session that prints exactly `AGENT_DIRECTOR_INSTANCE_ID=` followed
   by this row's id may be this row's. A session that names another id is
   another row's agent, whose own valid name can equal the stored form (for
   `<folder>-b.18k-fi`, another row's `<folder>-b_18k-fi`): never end it.
   Handle a session with no valid id as "A leftover, or a session with no
   valid label, or one that never reported in on a finished row" says.
4. If this row's session runs, end it by its session id from the listing,
   keeping the quotes, then list again and check that the id is gone:

   ```sh
   tmux -u -S '<socket>' kill-session -t '<session id>'
   tmux -u -S '<socket>' list-sessions -F '#{session_id} #{session_created} #{session_name}'
   ```

   If the session you ended was the server's last one, the second listing
   prints `no server running on <socket>` instead; this also means the
   session is gone.

   Never target it by name, not even with tmux's exact-match `=` prefix: the
   recorded name is not the name tmux holds, tmux reads `.` and `:` in a
   target as window and pane separators, and the stored form can be another
   session's name.
5. Remove the row:

   ```sh
   agent-director delete --claude-instance-id <id>
   ```

   This also removes the row's permission requests and session history,
   and clears it as the parent of any other row; it touches no tmux session
   and no transcript. `delete` is deprecated for agents, but stays as the
   operator-only way to remove such a row, because `resume` and reuse refuse
   it and `expire` keeps it.

### A spawn refused as "left over from an earlier life"

`ErrTmuxSessionConflict` ("left over from an earlier life") from a plain
`spawn` (without `--reuse-finished`; a refused reuse is handled as in the
leftover item above) comes in two cases:

- A `spawn` with a `--claude-instance-id` that has no row, when a tmux
  session of this agent-director store still carries that id. Nothing was
  written. The error names each such session by name and session id.
- A `spawn` whose `--tmux-session-name` is held by such a session. The
  error names the session id and says "the new row was ended": the spawn
  has already ended its row.

The spawn's trail record gives the socket, this store's id, the session's
id and its creation time (for the second case also `row_result`, and the
record's `attach_command` and `end_command` are the commands of steps 3
and 4 of "A leftover, or a session with no valid label, or one that never
reported in on a finished row"):

```sh
jq -c 'select(.event == "ad.launch.name_held" and .claude_instance_id == "<id>") | {tmux_socket, store_id, tmux_session_id, session_created, row_result}' ~/.agent-director/ad-trail.jsonl | tail -n 1
```

In the second case the leftover never reported in to the row the spawn
ended, so `kill --include-finished` answers "never reported in" and sends
no kill: end the leftover by hand by its session id, as the leftover item
above describes.

Handle each session as a leftover (steps 2 to 4 of "A leftover, or a
session with no valid label, or one that never reported in on a finished
row"; this store's id is
the record's `store_id`, the same id [This store's id](#this-stores-id)
prints), then spawn the id again. In the first case no row
exists, so no reuse opt-in is needed. In the second the row is `ended`, so
spawn with `--reuse-finished`:

```sh
agent-director spawn --cwd <dir> --tmux-session-name <name> --claude-instance-id <id> --reuse-finished
```

If you leave the row `ended`, `expire` keeps it while the leftover runs
(reason `leftover_running`, listed in `kept_ids`); its first run after the
leftover is gone deletes the row, once the row is older than the retention
window.

If the error instead says the new row "stays pending" or "was left as it
is", check the row with `agent-director get --claude-instance-id <id>`; if
it is `pending`, follow the `pending` paragraph of the leftover item above
before spawning with `--reuse-finished`.

In the first case, if the error says "and N more", find the others with the
listing of step 1 of "A leftover, or a session with no valid label, or one
that never reported in on a finished row". The check before a spawn sees only this socket: it misses
leftovers on another tmux server or socket, and sessions with no label.

When a held name's error says "no valid instance id", handle the session
as in the leftover item (look first: it may be a person's own session).
When it says "a different instance id", the session is another row's agent:
do not end it; give this spawn a different `--tmux-session-name`. The
spawn has already ended its own row, so spawning the same
`--claude-instance-id` again also needs `--reuse-finished`.

### A session of another agent-director store

`ErrTmuxSessionConflict` ("another agent-director store"), or a label whose
last field is not this store's id (see [This store's id](#this-stores-id);
the `store_id` of an `ad.launch.name_held` record is the same value):
another store on the same tmux server (a
test sandbox, a second `HOME`, a CI container using the host's socket) owns
the session. Never end it from this store. Find which store it belongs to
(its `HOME`) and stop or move that store's agents with that store's own
agent-director, or give this store's agent a different session name:

```sh
agent-director spawn --cwd <dir> --tmux-session-name <other name> --claude-instance-id <id> --reuse-finished
```

### An agent process that runs with no session or pane of its launch

`agent-director kill` returns `ErrTmuxKillFailed` saying that no session or
pane of this launch was found while the agent process still runs, and that
no kill was sent. `read-pane` answers `ErrTmuxCaptureFailed` although the
agent runs (a gone answer about the launch's session, not proof that the
agent has exited). `find-missing` keeps a live row live, because its
process is alive, and `expire` keeps a finished one (`process_alive`).
agent-director never signals a process itself; ending it
is a human's decision:

1. Take the pid from the error, or from the trail (`agent_pid`; for a
   survivor, `survivor_pids`):

   ```sh
   jq -c 'select(.event == "ad.kill.called" and .claude_instance_id == "<id>") | {agent_pid, survivor_pids, outcome}' ~/.agent-director/ad-trail.jsonl | tail -n 1
   ```

2. Confirm it is the agent, a Claude Code process started when the launch
   began:

   ```sh
   ps -o pid,lstart,args -p '<pid>'
   ```

3. Look for its pane (also on any other tmux server you know of), look at
   its session read-only as above, and if it is not wanted end the pane by
   its id:

   ```sh
   tmux -u -S '<socket>' list-panes -a -F '#{pane_id} #{pane_pid} #{session_id} #{session_name}'
   tmux -u -S '<socket>' kill-pane -t '<pane id>'
   ```

4. If it is in no pane, run `ps` again to check that the pid still has the
   same start time, then end it:

   ```sh
   kill '<pid>'
   ```

5. For the agent process: run
   `agent-director kill --claude-instance-id <id>` again (it now succeeds),
   or wait for `find-missing` to mark the row `missing`, then resume or
   respawn the id as the [caller contract](#caller-contract) says. For a
   survivor named by `survivor_pids`: end it as in step 4. A later `kill`
   does not check it again, so its success says nothing about the survivor.

The same procedure applies to a survivor that `kill` names after its wait:
a process of a pane of the agent's session that outlived the pane kill and
the session kill (for example one that ignores SIGHUP). Its pid is in the
error's description and in `ad.kill.called`'s `survivor_pids`.

### The agent's pane was not found

`ErrTmuxSessionConflict` ("the agent's pane was not found") from
`read-pane`, `send-keys` or `pause`: nothing was read or sent. The row's
current launch still has its labelled session, but the pane it recorded is
not there with the agent's pid. Either the agent's program ended while the
session stayed (another window keeps it open, or `remain-on-exit` is on),
or the pane was respawned with another program. If the error also says the
pane was not adopted, see the next item.

1. Find the labelled session and read its label (steps 1 and 2 of the
   leftover item) for the session the error quotes, and for others if it
   was renamed. The agent's session prints a valid label with this row's id
   and this store's id (see [This store's id](#this-stores-id)).
2. Look at what runs in it, and look read-only as above:

   ```sh
   tmux -u -S '<socket>' list-panes -s -t '<session id>' -F '#{pane_id} #{pane_pid} #{pane_current_command} #{pane_dead}'
   ```

3. Decide:
   - Nothing of value runs there (the agent's program has ended, a dead
     pane, an idle shell): end the session by its session id (step 4 of
     the leftover item). `find-missing` marks the row once its process is
     gone; then resume or reuse the id.
   - Another program that someone uses runs there: leave the session and
     only remove its claim to the launch, then tell its owner.
     `find-missing` then settles the row by its process. Never end a
     session someone uses without asking:

     ```sh
     tmux -u -S '<socket>' set-option -t '<session id>' -u @ad_owner
     ```

   - The agent still runs in another pane of that session (its pid, from
     `ps`, matches the row's SessionStart pid): leave it. Only the pane
     verbs cannot reach it; `agent-director kill` still ends the session.

### The agent's pane was not adopted after a lost create reply

When the reply to the call that created a launch's session was lost,
agent-director has not recorded the agent's pane, so the agent's hooks do
not apply to the row, which can stay `pending` although its agent works.
Once the grace period has passed, `find-missing` adopts the agent's pane,
and the hooks apply from then on. Each created pane carries the pane label
`@ad_pane`, `<token> <pane id>`.

1. Find the labelled session and read its label (steps 1 and 2 of the
   leftover item); its second field is this launch's token. Then list its
   panes with their pane labels, and look read-only as above:

   ```sh
   tmux -u -S '<socket>' list-panes -s -t '<session id>' -F '#{pane_id} #{pane_pid} #{pane_current_command} #{@ad_pane}'
   ```

   The agent's pane is the one whose value starts with this launch's token
   and ends with its own pane id.
2. A wanted agent can be left running: `find-missing` adopts its pane, and
   so does any `kill`, `send-keys` or `pause` of the row (while tmux cannot
   answer, or two panes carry the token, the row stays unverified). If no pane carries the token, `find-missing` marks the row
   `missing` although the agent may still run. To end the session while the
   row is live, use `agent-director kill --claude-instance-id <id>`, not a
   hand kill: it is this launch's session. `kill` adopts the labelled pane,
   kills the session and waits for every pane process of it. Once the row
   is `missing`, end the session by its session id (steps 3 and 4 of the
   leftover item). Then resume or reuse the id, so that the next launch
   records its pane.

### A `pending` row with no launch start or token

`ErrSpawnNotInteractive` from `send-keys` saying the row's launch start or
launch token is "not recorded": nothing was sent and no tmux call was made.
Only a row that was `pending` when agent-director was upgraded, one written
by an agent-director process started before the install and not restarted,
or a hand edit can be like this. agent-director never acts on any session
for such a row.

1. Look at the row (state `pending`, no `launch_started_at`), and look in
   `ps` for an `agent-director` process (an MCP `serve` included) started
   before the install; restart it:

   ```sh
   agent-director get --claude-instance-id <id>
   ```

2. Look for a session of this agent on the socket (for a row from before
   the install, the socket of your tmux environment): the session with the
   row's recorded name (step 1 of the leftover item). Read its label (step
   2; a session from before the install has none) and, as a hint only, its
   environment, then look at it read-only as above:

   ```sh
   tmux -u -S '<socket>' show-environment -t '<session id>' AGENT_DIRECTOR_INSTANCE_ID
   ```

3. Decide. An agent from before the install that is not wanted: end its
   session by its session id (step 4 of the leftover item), because
   `agent-director kill` never ends a session for such a row. An agent that
   is wanted: leave it running until it can be stopped and started again
   properly. A session that belongs to someone else, or shows no sign of
   this agent: leave it alone. `find-missing` marks the row `missing` at
   once when no recorded process of it runs and no session carries its
   current label; then resume or reuse the id.

### Stopping a set of agents before a binary change

Every agent is stopped before agent-director's binary changes. When the
caller's own stop path cannot run, a human stops one labelled set of agents
(for example the label `service=<name>`) in this order. Never delete,
hand-edit or migrate `state.db` to stop agents, and never delete a row.

1. Stop whatever starts agents first: the caller's service, its schedulers
   and its cron jobs. Otherwise they relaunch what you stop.
2. Use a binary that opens the store. List the set with the installed
   binary:

   ```sh
   agent-director list --label <key>=<value>
   ```

   If it refuses with `ErrSchemaMismatch`, the store is newer than the
   binary: use the binary that wrote the store. If it refuses with
   `ErrSchemaMigrationRequired`, the store is older: use the previous
   binary. Keep a copy of the previous binary before any install, because
   a rollback needs it too. If it refuses with `ErrConfigMalformed`, first
   fix the `[tmux]` value it names.
3. Note every row of the set in a live state (`pending`, `waiting`,
   `working`, `ask_user` or `check_permission`).
4. Only with a binary from before this release (0.11.0): its `kill`,
   `pause` and `send-keys` find a session by name with tmux's prefix match,
   and its `kill` reports success when tmux failed. For each live row, look
   for another session whose name begins with the row's name (such rows
   record no socket, so use the socket of your tmux environment, by default
   `/tmp/tmux-<uid>/default`):

   ```sh
   tmux -u -S '<socket>' list-sessions -F '#{session_id} #{session_name}'
   ```

   Stop such a row by hand (step 6) instead.
5. Stop each live row. `pause` asks the agent to exit and waits until the
   row reads `ended`:

   ```sh
   agent-director pause --claude-instance-id <id>
   ```

   `pause` stops only a `waiting` row. For every other live row, and for
   any row `pause` refuses (a row this binary cannot reach, for example) or
   that does not end in time, use `agent-director kill` and steps 1 to 5 of
   the live-row sequence of the [caller contract](#caller-contract), which
   `kill`'s description also gives. With a binary from before this release,
   confirm each result with `agent-director status --claude-instance-id
   <id>` and the session listing. Never delete a row.
6. Handle anything still running by the items of this section: "An agent
   process that runs with no session or pane of its launch", "A leftover,
   or a session with no valid label, or one that never reported in on a
   finished row" and "A `pending` row with no launch start or token". With
   this release's binary, the last covers every row from before the
   install.
7. Confirm that the listing shows no live row of the set and that no
   session of the set remains:

   ```sh
   agent-director list --label <key>=<value>
   tmux -u -S '<socket>' list-sessions -F '#{session_id} #{session_created} #{session_name}'
   ```

   Then stop every other long-running agent-director process, every
   `agent-director serve` included, and change the binary.

### agent-director was installed outside the caller's switch-over

This release was installed, and the store migrated, before the caller's
version for it was deployed. Agents and long-running agent-director
processes from before the install may still run, and the caller's old
version cannot run against the migrated store. Stop the caller and whatever
starts agents (step 1 of "Stopping a set of agents before a binary
change"), then choose a direction.

**Forward**, preferred when the caller's version for this release can be
deployed now; it keeps every write since the install:

1. Find the rows from before the install: `get` shows no `tmux_socket` and
   no `launch_started_at` for them.

   ```sh
   agent-director list --label <key>=<value>
   agent-director get --claude-instance-id <id>
   ```

2. End each live one's agent by hand by its session id, as "A `pending`
   row with no launch start or token" says. This release's `kill` and
   `pause` never end such a row's session: it has no label to prove which
   session is its own.
3. Restart every long-running agent-director process started before the
   install, every `agent-director serve` included.
4. Run `agent-director find-missing`. It marks each such row `missing` once
   its process is gone.
5. Deploy the caller's version, which brings its agents back by `resume` or
   by a spawn with `--reuse-finished`.

Never delete those rows: their history is what `resume` uses.

**Back**, when the caller's version cannot be deployed:

1. Stop every agent of the set as "Stopping a set of agents before a binary
   change" says, with this release's binary, the only one that opens the
   migrated store. End the rows from before the install by hand, as in
   Forward step 2.
2. Stop every long-running agent-director process.
3. Restore the store, then the previous binary. For the store, either put
   back the copy of `state.db` (with its `-wal` and `-shm` files) taken
   before the install and delete any `state.db-wal` or `state.db-shm` the
   copy does not include, which loses every write since the install, or run
   the [downgrade recipe](docs/migration-guide.md#v5--v4-reverses-migratev4tov5),
   which keeps the rows but loses the values of the columns the migration
   added.
4. Start the caller's old version.
5. Later, follow the caller's switch-over runbook from its start.

### A row stays `pending` and the trail shows `no_exec_form`

The agent runs a Claude Code that does not run exec-form hooks, so none
of its hooks apply; the supported minimum is 2.1.280. List the records:

```sh
jq -c 'select(.event == "ad.hook.ignored" and .reason == "no_exec_form") | {ts, claude_instance_id, hook_event, parent_command}' ~/.agent-director/ad-trail.jsonl | tail -n 5
```

Upgrade `claude` on PATH to 2.1.280 or later. Then end and relaunch each
row that stays `pending` as the [caller contract](#caller-contract) says.

### A row stays `pending` and the trail shows `pid_mismatch`

A hook moves a row only when its parent process is the row's recorded pane
process. When the `claude` on PATH is a launcher or shim that runs Claude
Code as a child instead of `exec`ing it, the pane process is the launcher,
so every hook of every agent is ignored and rows stay `pending`. List the
records:

```sh
jq -c 'select(.event == "ad.hook.ignored" and .reason == "pid_mismatch") | {ts, claude_instance_id, hook_event, parent_pid, parent_command, row_pane_pid}' ~/.agent-director/ad-trail.jsonl | tail -n 5
```

`parent_command` is the command name of the process that fired the hook,
which is Claude Code itself. A few such records are expected and need no
action: a nested `claude`, a teammate pane or a leftover session carrying
the id is never the row's own agent. When every row's hooks are refused,
and the pane process (`row_pane_pid`) is a launcher rather than Claude Code
(compare `ps -o pid,comm -p <row_pane_pid>` with `parent_command`), replace
the `claude` on PATH with Claude Code's native binary or a wrapper that
`exec`s it (see [Prerequisites](#prerequisites)). Then end and relaunch
each row that stays `pending` as the [caller contract](#caller-contract)
says.

### A row on a different tmux server

`ErrTmuxNotAvailable` ("not the tmux server the agent was launched on";
the trail's `lookup_outcome` is `different_server`; when `find-missing`
cannot check the agent's process it leaves the row unverified with the
`liveness_note` `tmux_server_changed`): the
server the row
recorded still runs, but its socket now reaches another server (for
example the old server's socket file was removed and a new server started
there), or the server's identity cannot be checked. Find the old server's process and
decide; `find-missing` settles the row once the old server process is gone.

### A stray `@ad_owner` value or a duplicate label

`ErrTmuxSessionConflict` ("conflicting labels"). agent-director never sets
`@ad_owner` at these scopes. Look:

```sh
tmux -u -S '<socket>' show-options -g -v @ad_owner
tmux -u -S '<socket>' show-options -s -v @ad_owner
tmux -u -S '<socket>' show-options -gw -v @ad_owner
```

Remove a stray value with the matching form:

```sh
tmux -u -S '<socket>' set-option -g -u @ad_owner
tmux -u -S '<socket>' set-option -s -u @ad_owner
tmux -u -S '<socket>' set-option -gw -u @ad_owner
```

For two sessions with the same current label, look at both read-only and
end the one that is not wanted by its session id, as for a leftover.

## Uninstall

```sh
skills/install-agent-director/uninstall.sh           # preserve state.db + templates
skills/install-agent-director/uninstall.sh --purge   # remove everything
```

## Cutting a release

Releases are cut via the `/release` skill — see [docs/release-skill.md](docs/release-skill.md)
for the operator runbook. The skill discovers and runs every test surface,
cross-compiles the three CLI binaries, packs and install-verifies the npm
tarball, generates release notes, and only after all gates pass executes the
irreversible publish sequence. Defaults to dry-run.
