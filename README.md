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
- A **crash-recovery cron** — `find-missing` + `expire` reconcile the
  DB against actually-live processes, marking each row it can prove dead
  and skipping any it can't read.

## 5-minute install

### Prerequisites

- `claude` (Claude Code) 2.1.285 or later on PATH — install per
  <https://claude.com/claude-code>. agent-director launches only
  Claude Code; other agent CLIs are unsupported in this release.
- `tmux` 3.2 or later on PATH. Verified: 3.2a (by a scripted one-off
  run and recorded replies) and 3.3a (by the test suites).
  - Keep `remain-on-exit` off, the tmux default.
  - No tmux server needs to be running: agent-director starts one when
    it launches an agent.
- `jq` on PATH.

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

`install.sh` version-checks the local binary against `git rev-parse HEAD`
and refuses option `--binary` if the artifact is stale — re-run
`make build` to refresh it.

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

`spawn` accepts `--tmux-session-name <name>` so Slack-channel bots,
test harnesses, or manual-debug operators can pick a readable session
name instead of the default `<basename(cwd)>-<id[:8]>`:

```sh
agent-director spawn --cwd "$PWD" --tmux-session-name bot-claude-status
```

Rules (validated app-side, no silent rewrite): the name must be
non-empty, ≤ 64 bytes, valid UTF-8, and contain none of `#`, `:`,
`.`, `$`, `\`, or ASCII control bytes (`\x00`–`\x1f`, `\x7f`). There
is no DB uniqueness check — name reuse across **ended** spawns is supported.
A collision against a currently-live tmux session surfaces as
tmux's own `new-session` error (no app-layer sentinel). Omitting
the flag preserves today's `composeSessionName` default.

#### Finding a Spawn by its tmux session name

`list` accepts `--tmux-session-name <name>` to narrow rows by the
column verbatim — useful when the operator picked the name (above)
and wants to round-trip back from `tmux ls` to the persisted Spawn
without consulting the id:

```sh
agent-director list --tmux-session-name bot-claude-status
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
| `pending_grace_seconds` | s | 60 | **30**, or more (see below) | `find-missing`'s grace period: how long a launch is left alone after it starts. |
| `query_timeout_ms` | ms | 1500 | none | Each tmux lookup and pane listing. |
| `action_timeout_ms` | ms | 2000 | none | Each tmux kill, key send and pane capture. |
| `create_timeout_ms` | ms | 5000 | none | The tmux call that creates a session (`spawn`, `resume`). |
| `pipe_close_wait_ms` | ms | 100 | none | How long a tmux call waits for its output pipes after its process exits. |
| `sweep_budget_seconds` | s | 15 | none | Total tmux time per run of `find-missing` and `expire`. |
| `kill_exit_wait_ms` | ms | 5000 (provisional) | none | How long `kill` waits for the agent process to exit after killing its pane. |

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
  to 61 or more.
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
- Callers whose own waits use these values (waiting out the grace
  period, a retry cadence for "still stopping") need the configured
  values: tell them when you change one.

## Maintenance

Two verbs keep `state.db` honest. Run both on a recurring schedule, and
run them as the same user that spawns the sessions so every row is
readable:

```sh
# Mark spawns whose process has died as `missing` — run often (e.g. every 2 min):
agent-director find-missing

# Delete terminal rows older than expire_retention_days — run daily:
agent-director expire
```

Wire these into your platform's scheduler (launchd, systemd timer, cron,
Task Scheduler). Without `find-missing`, dead sessions linger in `list` as
stale `waiting`/`working` rows.

`find-missing` works per row: it marks the rows it can prove dead, leaves
the ones it verifies alive, and skips any it can't read (for example a run
by the wrong user, or right after a reboot) rather than refusing the whole
pass. Skipped rows are reported in its `unverified` count and
`unverified_ids`, and each carries `liveness_unverified_since` and a
`liveness_note` in `list` and `get` so you can re-run as the owning user.

## Recovering after a reboot

A reboot kills every session's process and tmux server, but the rows
survive in `state.db`. Bring a session's conversation back in two steps,
in this order:

```sh
# 1. Reconcile the frozen rows so dead sessions become `missing`:
agent-director find-missing

# 2. Relaunch a session with its full history replayed:
agent-director resume --claude-instance-id <id>
```

`find-missing` must run first — `resume` only acts on a session that
`find-missing` has already marked `missing`. `resume` relaunches under
the same id and restores the session's env and conversation transcript.
The resumed row shows `pending`, keeping its session id and history, until
its agent reports in, then `waiting`.
`resume` marks the folder as trusted too, unless the session was spawned
with `--no-pre-trust`: then no `resume` of it marks the folder, and the
agent may wait at the trust prompt.

`resume` also recovers history from a session that rotated (for example when
agents were restarted and Claude handed the session a new id) — it falls back
to the session's earlier transcripts automatically. A session's history
belongs to the life of its id, which starts at the spawn that created it:
`resume` falls back only to earlier transcripts of that life. If `resume`
reports that no transcript was ever written, the session simply hasn't been
messaged yet; send it a message and its transcript appears. Run
`agent-director get --claude-instance-id <id>` to see a session's
`transcript_status` and its prior sessions — the earlier sessions of the
current life, not the current one — before deciding anything.

Session rotations are archived automatically, so `resume` recovers them on
its own — no manual step is needed.

Do **not** use `delete` to recover — it permanently removes the row and
its conversation history, so there is nothing left to resume.

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
- Run as the same user and in the same tmux environment as the agents.
- A situation that needs a human is described in
  [Operator actions](#operator-actions); callers never perform those
  actions.
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

### A leftover, or a session with no valid label

A leftover is a session of an earlier launch of the agent: its label names
the row's id with another launch token. Whether the row is `pending` or
live, `agent-director kill` refuses it with `ErrTmuxSessionConflict` ("not
this launch's session"), sends nothing and names the session's id (`$N`).
A session with no valid label may be a person's own, so look before acting.

1. Find the session and note its `session_created`:

   ```sh
   tmux -u -S '<socket>' list-sessions -F '#{session_id} #{session_created} #{session_name}'
   ```

2. Check its label:

   ```sh
   tmux -u -S '<socket>' show-options -t '<session id>' -v @ad_owner
   ```

   A valid label has five fields: `ad1`, a launch token, the session's own
   id, the row's id and, last, the store id. Every leftover `kill` names
   carries this store's id; a label whose last field is a different id
   belongs to another agent-director store's agent: never end it from this
   store. A session with no label prints nothing
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

### A spawn refused as "left over from an earlier life"

A `spawn` with a `--claude-instance-id` that has no row is refused with
`ErrTmuxSessionConflict` ("left over from an earlier life") when a tmux
session of this agent-director store still carries that id. Nothing was
written. The error names each such session by name and session id. The
spawn's trail record gives the socket, this store's id and the first
session's id:

```sh
jq -c 'select(.event == "ad.launch.name_held" and .claude_instance_id == "<id>") | {tmux_socket, store_id, tmux_session_id, session_created}' ~/.agent-director/ad-trail.jsonl | tail -n 1
```

Handle each session as a leftover (steps 2 to 4 above; this store's id is
the record's `store_id`), then spawn the id again: no row exists, so no
reuse opt-in is needed. If the error says "and N more", find the others
with the listing of step 1. The check before a spawn sees only this socket:
it misses leftovers on another tmux server or socket, and sessions with no
label.

### An agent process that runs with no session or pane of its launch

`agent-director kill` returns `ErrTmuxKillFailed` saying that no session or
pane of this launch was found while the agent process still runs, and that
no kill was sent. agent-director never signals a process itself; ending it
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

### The agent's pane was not adopted after a lost create reply

When the reply to the call that created a launch's session was lost,
agent-director has not recorded the agent's pane, so the agent's hooks do
not apply to the row, which can stay `pending` although its agent works.
Each created pane carries the pane label `@ad_pane`, `<token> <pane id>`.

1. Find the labelled session and read its label (steps 1 and 2 of the
   leftover item); its second field is this launch's token. Then list its
   panes with their pane labels, and look read-only as above:

   ```sh
   tmux -u -S '<socket>' list-panes -s -t '<session id>' -F '#{pane_id} #{pane_pid} #{pane_current_command} #{@ad_pane}'
   ```

   The agent's pane is the one whose value starts with this launch's token
   and ends with its own pane id.
2. End it with `agent-director kill --claude-instance-id <id>`, not by hand:
   it is this launch's session. `kill` adopts the labelled pane, kills the
   session and waits for every pane process of it. Then resume or reuse the
   id, so that the next launch records its pane. An agent that is wanted can
   be left running until then.

### A row on a different tmux server

`ErrTmuxNotAvailable` ("not the tmux server the agent was launched on";
the trail's `lookup_outcome` is `different_server`): the server the row
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
