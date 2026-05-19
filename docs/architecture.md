# claude-director — Architecture

## What it is

A single Go binary that:

- Spawns Claude Code instances inside tmux sessions.
- Hooks into those Claude sessions (via Claude Code's hooks mechanism) to track state, capture transcripts, and relay events.
- Exposes a CLI for humans and a stdio MCP server for LLM callers — both implemented by the same binary in different modes.

## Surfaces

- **CLI** — `claude-director spawn | send-keys | list | wipe | find-missing | install | mcp` (see SRD and `--help` for the canonical list).
- **Hook entrypoint** — the same binary invoked by Claude Code on hook events (SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, Stop, Notification, SessionEnd, PermissionRequest).
- **MCP server** — same binary in `mcp` mode, stdio transport, lifetime scoped to a single Claude Code session.

## Data

- SQLite at `~/.claude-director/state.db` for spawn state, parent/child links, permission requests, and labels.
- TOML config at `~/.claude-director/config.toml`.
- Templates as plain files in `~/.claude-director/templates/`.

See the SRD (Apiary Ideas hive: `t1.jus.x5`) for the full design.

## Package Layout & Layer Boundaries

The binary is layered so each package owns exactly one concern. Imports flow
in one direction only: `cmd/` depends on `internal/api/*`, which depends on
`internal/store` (and on `internal/config` for read-only configuration).
Nothing flows back upward and nothing skips a layer.

### Package inventory

| Path | Responsibility | Allowed imports | Prohibited imports |
| --- | --- | --- | --- |
| `cmd/claude-director` | CLI entrypoint: argv dispatch, exit codes, JSON error envelopes. Wires `internal/config` and `internal/api/*` into runnable verbs. | stdlib; any `internal/api/*`; `internal/config`; `internal/store` only via constructor wiring. | Direct `database/sql` use; raw SQL strings; ad-hoc subprocess management that bypasses `internal/api`. |
| `internal/store` | Sole owner of the SQLite database file. Opens the DB, enforces file/dir permissions, manages schema (v1 per SRD §4.2), exposes typed CRUD primitives (added in later Tasks). | stdlib (`database/sql`, `os`, `os/user`, `path/filepath`, `errors`, etc.); `modernc.org/sqlite` for the driver side-effect import. | `internal/api/*`; `internal/config`; `cmd/*`; any package outside this one. The dependency arrow points *into* `store`, never out. |
| `internal/config` | Loads, validates, and serves the TOML config at `~/.claude-director/config.toml`. Read-only after load. | stdlib; `github.com/BurntSushi/toml`. | `database/sql`; `internal/store`; `internal/api/*`; `cmd/*`. |
| `internal/api` | Stable verb-handler surface used by CLI + MCP + hooks. Typed Go functions only — no SQL, no MCP framing. | stdlib; `internal/api/manifest`; (later) `internal/store` and `internal/config`. | Raw SQL; MCP framing; hook IO. |
| `internal/api/manifest` | Defines and exposes the canonical CLI/MCP verb manifest used to keep the CLI surface, MCP tool surface, and docs in lock-step. | stdlib only — leaf package. | `internal/store`, `internal/config`, `internal/api/*` (other than this package), `cmd/*`, raw `database/sql`, SQL strings. The manifest is the source of truth; consumers depend on *it*, never the other way around. |
| `internal/spawn` | Owns the parameter-resolution → validation → defaults → launch pipeline (SRD §7). Builds env maps, synthesizes `--settings` JSON, and asks `internal/tmux` to start the session. Inserts the `pending` row via `internal/store`. | stdlib; `internal/config`; `internal/store`; `internal/tmux`; `github.com/google/uuid` for UUID4 minting. | Raw `database/sql`; hook-handling code; MCP framing; ad-hoc subprocess management outside `internal/tmux`. |
| `internal/tmux` | Thin client over the tmux binary. Each operation is one `exec.Command` invocation. Provides `NewSession`, `HasSession`, `KillSession`, `ListPanes`. | stdlib (`bytes`, `os/exec`, `strings`, `strconv`, `sort`). | Shell processes (`/bin/sh`), template / config / store packages, anything other than direct `exec.Command`. |
| `internal/hook` | Reads payload JSON from stdin, classifies per SRD §5.2, writes the row UPSERT, exits 0 (state-tracking fail-open). | stdlib; `internal/store`. | `internal/tmux`; `internal/spawn`; `internal/config` (the cmd-side wrapper loads config; the package itself stays narrow). |

### `internal/store`

`internal/store` is the only layer in the binary permitted to speak SQL.
All other packages call typed methods on a `*store.Store` and never see a
`*sql.DB`, a SQL string, or a database driver error. Hard rule, kept here
verbatim so future code review can grep for it:

> No SQL outside `internal/store`; callers use typed query primitives only.

**Schema v1** lives in `internal/store/schema.go` and matches SRD §4.2 byte
for byte. Two tables:

- `spawns` — one row per Claude Code instance under direction, with
  parent/child link (`parent_id`), lifecycle (`state`, `started_at`,
  `last_seen_at`, `ended_at`), tmux + relay metadata, and a JSON-encoded
  `labels` blob. Indexed on `state`, `last_seen_at`, and `parent_id`.
- `permission_requests` — one open permission request per spawn (UNIQUE on
  `claude_instance_id`, FK-cascaded on spawn delete), with `tool_name`,
  `tool_input`, and an optional `decision` / `decision_reason`.

**Schema versioning convention.** SQLite's `PRAGMA user_version` is the
source of truth for which schema this binary expects. On `Open`:

- `user_version == 0` → fresh DB: create the v1 tables and indexes inside a
  single transaction, then stamp `PRAGMA user_version = 1`.
- `user_version == 1` → no-op; the schema already matches.
- Any other value → return the sentinel `store.ErrSchemaMismatch` (an
  exported `errors.New` value, so callers use `errors.Is`). No DDL runs in
  this case. Bumping schema versions in the future will mean adding a
  migration step into `ensureSchema`, not editing v1 DDL.

**Concurrency.** `Open` calls `db.SetMaxOpenConns(1)` on the underlying
`*sql.DB`. Combined with `journal_mode=WAL` and `foreign_keys=ON` (both
applied via DSN PRAGMAs so every connection the pool dials in starts in the
right state), this gives us a single in-process writer with cheap concurrent
readers — exactly the model SRD §13.3 requires. WAL plus one writer
sidesteps `database is locked` retry loops; readers see committed snapshots
without blocking writes. PRAGMAs are verified after open: if SQLite ever
silently downgrades them, `Open` fails loudly instead of yielding a
half-broken Store.

**File-system contract.** The parent directory (`~/.claude-director/` by
default) is created with mode 0700, and the database file is chmodded to
0600 on every `Open`. Repeated opens never widen permissions. A leading
`~/` in the path is expanded against `os/user.Current().HomeDir`.

Cross-reference: SRD §4.2 (canonical DDL), §4.5 (layer boundaries), §13.3
(single-writer + WAL rationale).

### `internal/api/manifest` — Verb Registry

**What it is.** A single Go source file at
`internal/api/manifest/manifest.go` driven by a `//go:generate` directive.
Each `VerbDef` entry records:

- the verb name,
- a one-line description,
- its parameters (name, type, description, required flag),
- its result fields (name, type, description), and
- the set of error names it may emit.

A package-level `var Verbs []VerbDef` holds the ordered registry, and
`Lookup(name)` returns a single entry by name.

**Why a single source of truth.** Three downstream consumers derive from
`Verbs`:

1. The CLI dispatch table in `cmd/claude-director/main.go` (which verbs the
   binary accepts and how `help` enumerates them).
2. The MCP tool schema served in `mcp` mode (Epic 11).
3. The generated reference docs `docs/cli-reference.md` and
   `docs/mcp-reference.md`, written by `tools/gen-docs` (Task 6 of Epic 1).

Adding or modifying a verb anywhere other than `internal/api/manifest`
drifts the surface from the manifest and is caught by the CI doc-drift gate
(re-runs `go generate` and fails if any tracked file changes).

**How to add a verb.**

1. Add a `VerbDef` literal to `Verbs` in
   `internal/api/manifest/manifest.go`. Populate `Name`, `Description`,
   `Params`, `ResultFields`, and `ErrorNames` (empty slice, not nil, when
   the verb has no error conditions).
2. Implement the handler in `internal/api` (typed parameter struct in,
   typed result struct out, returning a Go `error`). Keep SQL inside
   `internal/store`; the handler calls store primitives.
3. Wire the verb into the CLI dispatch map in
   `cmd/claude-director/main.go` so argv routes to the new handler.
4. Run `make generate` to regenerate `docs/cli-reference.md` and
   `docs/mcp-reference.md` from the manifest.
5. Verify idempotency: re-run `make generate` and confirm `git status`
   shows no diff. A second run that produces a diff means the generator is
   non-deterministic — fix it before merging.

**Prohibitions.**

- Do not hand-edit `docs/cli-reference.md` or `docs/mcp-reference.md`.
  They are auto-generated; the CI drift gate will fail.
- Do not define CLI flags outside the manifest. New params go in the
  matching `VerbDef.Params` literal.
- Do not hand-write MCP tool schemas. The MCP server reads from `Verbs`.
- Do not have `internal/api/manifest` import `internal/store`,
  `internal/config`, or anything under `cmd/`. The package is stdlib-only
  by design so the generator (which imports it) stays trivially buildable.

**CLI JSON-output discipline.**

> Every CLI verb emits exactly one JSON object on stdout; errors emit JSON
> `{err_name, err_description}` on stderr; no banners, no progress, no
> prose preamble. Enforced by code review against SRD §12.3 and §16.3.

See `docs/cli-reference.md` and `docs/mcp-reference.md` — auto-generated; do not edit.

### Layer boundary diagram

```
                +-------------------------+
                |   cmd/claude-director   |
                |   (CLI dispatch, exit   |
                |    codes, JSON errors)  |
                +-----------+-------------+
                            |
                            v
                +-------------------------+
                |   internal/api/*        |
                |   manifest, (future)    |
                |   spawn, hook, mcp,     |
                |   tmux, probe           |
                +-----------+-------------+
                            |
                            v
                +-------------------------+
                |   internal/store        |
                |   (sole SQL owner;      |
                |    schema v1 / SRD §4.2)|
                +-------------------------+

   internal/config -----> consumed by cmd and internal/api/*
                          (never imports internal/store)

Future Epics fill in sibling packages under internal/ at the api layer:
    internal/spawn   - lifecycle of Claude Code child processes
    internal/hook    - hook-event entrypoint logic
    internal/tmux    - tmux session orchestration
    internal/probe   - liveness / health probes
    internal/mcp     - stdio MCP server implementation

The arrow direction is a hard rule. internal/store knows nothing about its
callers; cmd/ knows nothing about SQL. Any PR that introduces a back-edge
(e.g. internal/store importing internal/api) should be rejected at review.
```

Cite SRD §2 for the overall component decomposition this diagram realizes.

## State Machine

A Spawn's lifecycle is tracked in the `state` column of `spawns`. Every
state value comes from the SRD §5.1 enum; transitions are driven either
by hook events (SRD §5.2) or by direct verb action (`pause`, `resume`,
`expire`, `delete`).

```
pending  ──spawn() launches tmux session
  │
  ▼   SessionStart hook fires
waiting  ◄─── Stop, Notification (and SessionEnd reason=clear|compact
  │           soft refresh: bumps last_seen_at, no state change)
  │
  ▼   UserPromptSubmit / PreToolUse(non-AUQ) / PostToolUse
working  ─────────────────────────────────────┐
  │                                            │
  │  PreToolUse(AskUserQuestion)               │ PermissionRequest
  ▼                                            ▼
ask_user                                  check_permission
  │                                            │
  │   send-keys, etc.                          │   decide() writes
  └────────────►  working / waiting   ◄────────┘   permission_requests.decision
                                                   (Epic 10)

waiting / working / ask_user / check_permission
  │
  ▼   SessionEnd hook (real end)
ended

waiting / working / ask_user / check_permission
  │
  ▼   find-missing (Epic 8): DB live row, no live tmux/Claude
missing
  │
  ▼   resume() relaunches with --resume (Epic 9)
waiting (after SessionStart fires)
```

### Event → state mapping (SRD §5.2)

| Event | Tool / reason carve-out | Resulting state |
| --- | --- | --- |
| `SessionStart` | — | `waiting` (also writes `claude_session_id` from `transcript_path`) |
| `UserPromptSubmit` | — | `working` |
| `PreToolUse` | `tool_name = AskUserQuestion` | `ask_user` |
| `PreToolUse` | any other tool | `working` |
| `PostToolUse` | — | `working` |
| `Stop` | — | `waiting` |
| `Notification` | — | `waiting` |
| `PermissionRequest` | — | `check_permission` (relay-mode envelope is Epic 10) |
| `SessionEnd` | `reason ∈ {clear, compact}` | soft refresh — `last_seen_at` only |
| `SessionEnd` | any other reason | `ended` (also sets `ended_at`) |
| unknown event | — | soft refresh + info-level log entry |

`missing` is only written by `find-missing` (Epic 8). `pending` is only
written by `spawn()`; the first SessionStart hook flips it to `waiting`.

State-tracking hook writes are fail-open: any internal failure logs and
exits 0 (SRD §3.2). A missed UPSERT never blocks Claude.

## Spawn Parameter Resolution

`spawn` is implemented as a four-stage pipeline. The boundaries exist
so each stage can be tested in isolation against synthesized input.

```
  caller params         (CLI flags / MCP tool input)
       │
       ▼
   ┌─────────┐   template merge (Epic 7 — stub today)
   │ Resolve │
   └────┬────┘
        ▼
   ┌──────────┐   SRD §7.2: cwd shape/existence/type;
   │ Validate │   relay_mode; denied flags; reserved env keys.
   └────┬─────┘   No side effects on failure.
        ▼
   ┌────────────┐   SRD §7.3: UUID4 if no claude_instance_id;
   │ ApplyDefaults│  <basename(cwd)>-<id[:8]> session name;
   └────┬───────┘   relay_mode from config. Collision check via store.
        ▼
   ┌────────┐   SRD §7.4: pending row insert; env compose;
   │ Launch │   --settings JSON synthesis; tmux new-session via direct argv.
   └────┬───┘   Fire-and-forget — returns claude_instance_id.
        ▼
   claude_instance_id (state stays `pending` until SessionStart fires)
```

Layer boundaries (load-bearing):

- `internal/spawn` calls `internal/store` (one `InsertPending` UPSERT
  and one `LiveSpawnExists` collision read) and `internal/tmux` (one
  `NewSession` argv). Nothing else.
- `internal/hook` calls `internal/store` (state UPSERT + session-id
  write). Never `internal/tmux`, never `internal/spawn`.
- `internal/api` is the thin verb-handler surface: it composes
  `internal/spawn` calls for the `spawn` verb and direct
  `internal/store` reads for `status` / `get`. No SQL strings, no tmux
  argv at this layer.

The hook handler is invoked via the per-Spawn `--settings` JSON
synthesized in stage 4. The handler's binary path is resolved via
`os.Executable()` (`/proc/self/exe` on Linux, `_NSGetExecutablePath` on
macOS) so it is always the same binary version that ran the `spawn`
call.

## Interact: `send-keys` + `read-pane`

After Epic 4 a tracked Spawn becomes externally drivable: an orchestrator
can deliver text into its tmux pane and read the rendered TUI back out
without attaching to the session. The two verbs are typed Go functions
in `internal/api`, each calling exactly one method on the shared
`*tmux.Client` (`SendKeys` / `CapturePane`). Cross-reference SRD §4.3
(send-keys multiline semantics), SRD §12 (verb shapes),
`reference/send-keys-research.md` (empirical LF/CR behavior),
`reference/pane-output-research.md` (capture-pane sanitization).

### `send-keys`

Submits text into a live Spawn's first pane. Three behaviors are
load-bearing:

- **`\r` (CR, 0x0D) stripped before tmux receives the argv.** Per
  `reference/send-keys-research.md` "CR caveat", a literal CR in the
  payload would submit the buffer at the position the CR appears —
  splitting one logical message into multiple submissions. The verb
  removes CR bytes pre-send so only the trailing Enter submits.

- **`\n` (LF, 0x0A) passed through verbatim.** Claude Code's input
  handler treats LF as "insert newline in input box", not as a submit.
  A multi-line payload composes as one message. The argv to tmux is
  *one* element containing the literal LFs; tmux's own quoting handles
  them.

- **`press_enter` (default true) appends a single Enter.** Implemented
  as a *separate* `tmux send-keys -t <name>:0.0 Enter` call after the
  text. Mixing the submit byte into the text argv would re-introduce
  the same "premature submission" failure mode the CR strip prevents.

State precondition: the row's `state` must be one of `waiting`,
`working`, `ask_user`, `check_permission`. `pending`, `ended`, and
`missing` reject with `ErrSpawnNotInteractive`. `pending` is excluded
because the TUI is not yet up — the first SessionStart hook flips
`pending` to `waiting`, after which the Spawn is reachable.

Relay-mode guard: when `relay_mode=on` AND `state=check_permission`,
the permission relay (Epic 10) owns the modal answer. SendKeys refuses
with `ErrSendKeysWhileRelayed` so the relay's `decide()` write isn't
racing a pane-side keystroke. The full relay path lands in Epic 10;
Epic 4 stubs the guard so the precondition surface is correct from
day one.

### `read-pane`

Captures the last N lines of a Spawn's first pane via
`tmux capture-pane -p -t <name>:0.0 -S -<n>`. Default `n=25`, no upper
cap (SRD §12 explicitly leaves the bound to the caller).

ANSI handling:

- **Default (`ansi=false`) — strip ANSI escape sequences, preserve
  unicode glyphs.** `tmux capture-pane -p` (without `-e`) already
  removes most SGR / cursor escapes server-side; `internal/tmux.StripANSI`
  scrubs any residuals with a byte-oriented regex (`\x1b\[[0-9;]*[a-zA-Z]`)
  that never touches non-ASCII bytes. The TUI glyphs Claude uses
  (`❯` U+276F, `⎿` U+23BF, `🐝` U+1F41D, box-drawing chars) survive —
  the orchestrator reads them as state signal per
  `reference/pane-output-research.md` "State-signal value".

- **`ansi=true` — return raw bytes from tmux exactly as captured.**
  Useful for a future TUI viewer mirroring the pane to a browser, or
  for debugging color-coded diffs. No transformation between tmux
  stdout and the JSON `pane` field.

Errors: `ErrSpawnNotFound` (unknown id), `ErrTmuxCaptureFailed`
(transport-layer tmux failure — e.g. the session vanished between the
row lookup and the capture call). Unlike `send-keys`, `read-pane` has
*no* state precondition: a caller can inspect an `ended` Spawn's final
pane bytes as a post-mortem.

### Layer boundaries

- `internal/api/sendkeys.go` and `internal/api/readpane.go` are the
  verb surfaces — typed params in, typed result + error out, errors
  matched via `errors.Is`.
- They call `internal/store` for the row lookup and `internal/tmux` for
  the wire op. Each verb is one row read plus one or two tmux calls.
- No SQL strings, no shell, no `&&`/`|`/`$VAR` at any layer (SRD §4.3,
  §14.3). tmux invocations go through `*tmux.Client.SendKeys` /
  `CapturePane`, both of which use direct `exec.Command` with the text
  as a single argv element.

## Test Harness

Every functional Epic (Epics 3-13) is gated by a Docker-based integration
harness rather than `go test ./...`. The harness was built in Epic 2; this
section captures what it is, how to extend it, and the rules the gate
depends on. Cross-reference SRD §15 (testing strategy), §17 (audit
standard), §18 (CI environment).

### Container

`test/Dockerfile` builds `claude-director-test`:

- Base: `debian:bookworm-slim`.
- Installs `tmux`, `nodejs` 20, `jq`, `sqlite3`, `git`.
- Installs `@anthropic-ai/claude-code@<pinned>` (see "Pinned Claude Code
  version" below).
- Copies in the pre-built `claude-director` binary from `./bin/`.
- Runs as a non-root `tester` user with `HOME=/home/tester`.
- Default command is `/opt/driver/run-testplan.sh`.

The image is built via `make test-image`. `make test-image-smoke` exercises
it standalone: confirms `claude --version` reports the pinned version,
`claude-director help` exits 0, and the driver rejects an unknown EPIC.

### Driver

`test/driver/run-testplan.sh` is the container entrypoint. Contract:

1. `EPIC` env var names the testplan slug. The driver resolves it to a
   `t1.*.md` collector under `/work/tickets/testplans/` (mounted read-only
   by `make test-docker`), either by literal subdirectory or by
   `title:.*<EPIC>` frontmatter match.
2. Case order comes from the t1's `children:` YAML list. Alphabetical
   basename sort would scramble paired cases (e.g. the smoke-2 / smoke-3
   DB-isolation pair) — `children:` preserves authoring order.
3. Before each t2 case, the driver invokes `test/driver/db-reset.sh`: it
   removes `~/.claude-director/state.db` + WAL/SHM, kills tmux sessions
   matching the `cd-` prefix, then calls `claude-director help` to
   rebuild schema v1.
4. For each case, the driver runs in one of two modes:
   - `DRIVER_MODE=shell` (default) — extracts the t2 body's fenced
     ```bash``` block and executes it directly. No API calls. Used by
     `harness-smoke` and by any other testplan whose cases are observable
     shell-level checks.
   - `DRIVER_MODE=claude` — concatenates `test/driver/prompt.md` + the t2
     body and runs `claude --print --output-format json` against it. The
     driver-Claude reads the t2's "Pass criteria" section and emits a
     single JSON verdict (`{"verdict":"pass|fail","details":"..."}`) as
     its stop output.
5. Output is one JSON object per case on stdout (`{"case","status","details"}`)
   followed by a summary line (`{"summary":{"total","pass","fail"}}`). The
   driver exits 0 iff every case passes.

### Canonical command

```
make test-docker EPIC=<slug>
```

Fixed signature. Every functional Epic's Progression Contract references
this verbatim; changing the form would require updating every gated Epic
ticket. Required env: `EPIC`. Optional: `DRIVER_MODE`, `ANTHROPIC_API_KEY`,
`CLAUDE_CODE_OAUTH_TOKEN`.

### Auth

The harness is env-var auth only (no file mounts). Per the empirical
research notes under `reference/`:

- *API-key accounts:* pass `ANTHROPIC_API_KEY=sk-ant-...` via `-e`. See
  `reference/anthropic-api-key-auth-research.md`.
- *Max / OAuth accounts:* pass `CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-...`
  via `-e`. The token must be a long-lived one minted by
  `claude setup-token`, *not* the short-lived access token from
  `~/.claude-maxauth/.credentials.json` (which expires in ~9h). See
  `reference/max-account-auth-research.md`.

`make test-docker` inherits both env vars from the calling process — never
hard-coded in the Makefile. CI sources them from secrets (see
`.github/workflows/integration.yml`).

Multi-container parallelism is clean because there is no shared
`~/.claude.json` to race on. Test credentials should be CI-secret-scoped,
distinct from the operator's primary account.

The shell `DRIVER_MODE` runs without any credential — Epic 2's
`harness-smoke` gate runs that way so PR CI can stay credentialless. Real
driver-Claude runs (`DRIVER_MODE=claude`) require one of the env vars.

### testplans hive convention

`tickets/testplans/` is a bees hive (registered in Epic 1's bootstrap
commit). One t1 collector per Epic; t2 cases are plain-English bodies. The
driver does not read tier labels — it reads `t1.*.md` and `t2.*.md` files
directly — so the hive's tier-label naming (`Collector` / `Test case`) is
purely cosmetic. Each t2 body has a fenced ```bash``` block that the shell
driver mode executes; the same prose is also what the `claude` driver mode
hands to the driver-Claude as its spec.

### Pinned Claude Code version

The harness installs `@anthropic-ai/claude-code@2.1.120`. The pin is
load-bearing: every empirical behavior the harness relies on (env-var
auth, settings merge, hook surface) was validated against this version in
the notes under `reference/*-research.md`.

**Bump policy.** A pin bump (`CLAUDE_CODE_VERSION` in `Makefile` and the
`ARG` in `test/Dockerfile`) requires re-running each empirical research
note under `reference/` against the new version *before* merging:

- `reference/docker-auth-research.md`
- `reference/anthropic-api-key-auth-research.md`
- `reference/max-account-auth-research.md`
- `reference/claude-settings-research.md`

Any note that no longer reproduces is a blocker. Update the note + bump
the pin in the same PR; never bump silently.

### CI lane

`.github/workflows/integration.yml` defines two jobs:

- `linux-integration` — runs `make test-docker EPIC=harness-smoke` on
  `ubuntu-latest` for every PR and push to `main`. Auth env vars come
  from `${{ secrets.ANTHROPIC_API_KEY }}` and
  `${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}`. Neither is required for the
  default shell mode, but the secrets pipeline is wired so opt-in
  credentialed runs work without repo edits.
- `macos-stub` — runs on `macos-latest`, exits 0 with a stub message.
  Epic 8 (sysctl-based liveness probe) will swap this for a real macOS
  test that exercises the sysctl path. SRD §19 Q7.

### Audit standard

The harness's `make test-docker` output is the *input* to the orchestrator
audit, not the gate itself. SRD §17 requires first-hand audit: the
orchestrator runs `make test-docker` themselves, reads the JSON stream
case-by-case, confirms each case actually executed and passed, and signals
"continue" to advance the Epic. No blind approval, no auto-retry.

Cross-reference: SRD §15 (testing strategy), §17 (orchestrator-in-the-loop
gate), §18 Q10 (Docker test credential injection).
