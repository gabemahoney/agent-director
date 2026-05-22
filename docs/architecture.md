# agent-director — Architecture

## What it is

A single Go binary that:

- Spawns Claude Code instances inside tmux sessions.
- Hooks into those Claude sessions (via Claude Code's hooks mechanism) to track state, capture transcripts, and relay events.
- Exposes a CLI for humans and a stdio MCP server for LLM callers — both implemented by the same binary in different modes.

## Surfaces

- **CLI** — `agent-director <verb> ...` for every verb in
  `pkg/api/manifest`. See `docs/cli-reference.md` for the canonical
  list.
- **Hook entrypoint** — the same binary invoked by Claude Code on hook events (SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, Stop, Notification, SessionEnd, PermissionRequest).
- **Stdio MCP server** — same binary invoked as `agent-director serve --stdio`. Stdio transport, lifetime scoped to a single Claude Code session.

## Data

- SQLite at `~/.agent-director/state.db` for spawn state, parent/child links, permission requests, and labels.
- TOML config at `~/.agent-director/config.toml`.
- Templates as plain files in `~/.agent-director/templates/`.

See the SRD (Apiary Ideas hive: `t1.jus.x5`) for the full design.

## Package Layout & Layer Boundaries

The binary is layered so each package owns exactly one concern. Imports flow
in one direction only: `cmd/` depends on `pkg/api`, which depends on
`internal/store` (and on `internal/config` for read-only configuration).
Nothing flows back upward and nothing skips a layer.

`pkg/api` is the canonical verb-handler home sitting above `internal/store`,
`internal/config`, `internal/tmux`, and `internal/probe`; it is consumed by
`cmd/agent-director` and `internal/mcp`. The downward-only dependency rule
still holds: nothing in `internal/` imports `pkg/api`.

### Package inventory

| Path | Responsibility | Allowed imports | Prohibited imports |
| --- | --- | --- | --- |
| `cmd/agent-director` | Thin CLI shim: argv parser and JSON envelope marshaller. Constructs one `pkg/api.Client` at startup via `setupClient()`; every non-hook verb calls a method on that Client (`client.Spawn(params)`, `client.Status(id)`, etc.) — no business logic lives in `cmd/`. **`runHook` exception:** retains independent `config.Load` + `store.Open` calls per SRD §3.2 fail-open; hook fires must never be blocked by Client-startup failures. | stdlib; `pkg/api`; `pkg/api/errnames`; `internal/hook`; `internal/config` and `internal/store` (error sentinels only) in `setupClient`; `internal/config` in `runHook` and `newHookLogger`. | Direct `database/sql` use; raw SQL strings; ad-hoc subprocess management; `store.Open` / `config.Load` / `tmux.New` outside `runHook`, `newHookLogger`, and `setupClient`'s logger bootstrap. |
| `pkg/api` | **Canonical verb-handler home and public surface.** Opaque `Client` facade — no exported fields, construction via `New` only. Owns all verb implementations, seam interfaces (`ListStore`, `PauseStore`, `KillTmux`, `KillLogger`, etc.), params/result types, and error sentinels. Owns store, tmux, and config internally; exposes one method per CLI verb; idempotent `Close`. Consumed by `cmd/agent-director` and `internal/mcp`. | stdlib; `internal/store`; `internal/config`; `internal/tmux`; `internal/probe`; `internal/spawn`. | Direct `database/sql`; raw SQL strings; MCP framing. |
| `internal/store` | Sole owner of the SQLite database file. Opens the DB, enforces file/dir permissions, manages schema (v1 per SRD §4.2), exposes typed CRUD primitives (added in later Tasks). | stdlib (`database/sql`, `os`, `os/user`, `path/filepath`, `errors`, etc.); `modernc.org/sqlite` for the driver side-effect import. | `pkg/api`; `internal/config`; `cmd/*`; any package outside this one. The dependency arrow points *into* `store`, never out. |
| `internal/config` | Loads, validates, and serves the TOML config at `~/.agent-director/config.toml`. Read-only after load. | stdlib; `github.com/BurntSushi/toml`. | `database/sql`; `internal/store`; `pkg/api`; `cmd/*`. |
| `pkg/api/errnames` | **Single source of truth for err_name strings.** Declares `Catalog []Entry` (each Entry pairs a sentinel `error` with its canonical name string), `Classify(err) (name, description)` with `ErrInternal` fallback, and `TrimNamePrefix` for envelope-text normalisation. The `Catalog` is consumed by `cmd/agent-director`'s envelope writer, `internal/mcp`'s `classifyDispatchError`, and (future) `pkg/cabi`'s JSON encoder. `catalog.json` is generated deterministically from `Catalog`; the doc-drift CI gate enforces coherence. | stdlib; `pkg/api`; `internal/config`; `internal/probe`; `internal/spawn`; `internal/store`; `internal/tmux` (sentinel types only). | `cmd/*`; `internal/mcp`. |
| `internal/mcp` | Stdio MCP server. `server.go` handles JSON-RPC framing (initialize, tools/list, tools/call). `dispatch.go::LiveDispatcher` holds a single `*pkg/api.Client` and routes each tool call to the corresponding `Client` method — no business logic of its own. `classifyDispatchError` delegates to `errnames.Classify`. | stdlib; `pkg/api`; `pkg/api/manifest`; `pkg/api/errnames`. | `internal/store`; `internal/config`; `internal/tmux`; `internal/spawn`; `cmd/*`. |
| `pkg/api/manifest` | Defines and exposes the canonical CLI/MCP verb manifest used to keep the CLI surface, MCP tool surface, and docs in lock-step. | stdlib only — leaf package. | `internal/store`, `internal/config`, `cmd/*`, raw `database/sql`, SQL strings. The manifest is the source of truth; consumers depend on *it*, never the other way around. |
| `internal/spawn` | Owns the parameter-resolution → validation → defaults → launch pipeline (SRD §7). Builds env maps, synthesizes `--settings` JSON, and asks `internal/tmux` to start the session. Inserts the `pending` row via `internal/store`. | stdlib; `internal/config`; `internal/store`; `internal/tmux`; `github.com/google/uuid` for UUID4 minting. | Raw `database/sql`; hook-handling code; MCP framing; ad-hoc subprocess management outside `internal/tmux`. |
| `internal/tmux` | Thin client over the tmux binary. Each operation is one `exec.Command` invocation. Provides `NewSession`, `HasSession`, `KillSession`, `ListPanes`. | stdlib (`bytes`, `os/exec`, `strings`, `strconv`, `sort`). | Shell processes (`/bin/sh`), template / config / store packages, anything other than direct `exec.Command`. |
| `internal/hook` | Reads payload JSON from stdin, classifies per SRD §5.2, writes the row UPSERT, exits 0 (state-tracking fail-open). | stdlib; `internal/store`. | `internal/tmux`; `internal/spawn`; `internal/config` (the cmd-side wrapper loads config; the package itself stays narrow). |

### No-business-logic-in-cmd contract

The thin-shim rule is now grep-enforceable. In `cmd/agent-director/`
source files (excluding `*_test.go`), the following symbols must appear
**only** in the named exemption sites:

| Symbol | Permitted in |
| --- | --- |
| `store.Open` / `store.OpenOrInit` | `runHook` only |
| `config.Load` | `runHook`, `newHookLogger`, and `setupClient`'s logger bootstrap (Pin 3) only |
| `tmux.New` | none — `cmd/` must not construct a tmux client directly; `pkg/api.New` owns it |

Any occurrence outside those sites is a layer-boundary violation and should
be rejected at review. The enforcement command:

```sh
grep -rn "store\.Open\|config\.Load\|tmux\.New" cmd/agent-director/ \
  | grep -v '_test\.go'
```

Expected output after this refactor: only lines inside `runHook`,
`newHookLogger`, and `setupClient`.

### `pkg/api` Client lifecycle

`pkg/api.Client` is the opaque handle through which all callers interact with
agent-director. No exported fields; obtain a client via `New(opts Options)`,
release with `Close()`.

**`Options` fields.**

| Field | Default | Notes |
| --- | --- | --- |
| `StorePath` | three-tier resolution (see below) | Tilde-expanded by `New`. |
| `ConfigPath` | `~/.agent-director/config.toml` | Tilde-expanded by `New`. |
| `TmuxCommand` | binary on `PATH` | Override for testing or unusual installs. |
| `Logger` | `log.New(io.Discard, …)` | CLI passes a real logger; MCP callers pass nil (intentional silence). |
| `CreateIfMissing` | `false` | CLI sets `true` to preserve first-run UX (see below). |

**StorePath three-tier precedence.** `New` resolves the store path in order:

1. `Options.StorePath` if non-empty (tilde-expanded).
2. `cfg.Store.DbPath` loaded from the resolved `ConfigPath`, if non-empty.
3. Hardcoded fallback `~/.agent-director/state.db` (tilde-expanded).

This preserves existing CLI behavior: a user with a custom `[store] db_path`
in `config.toml` continues to hit that path without any extra flags or env
vars.

**No schema-init side effects — the key invariant** (kept here verbatim for
code review):

> When `CreateIfMissing` is `false` (the library default), `New` will NOT
> create the database file or its parent directory, and will NOT run any DDL.
> A missing store returns a typed error wrapping `store.ErrStoreNotInitialized`
> detectable via `errors.Is`.

CLI callers set `Options.CreateIfMissing = true` to preserve the pre-refactor
first-run UX: the store is created automatically on first invocation, matching
what the binary did before the facade was introduced.

**`Close`.** Releases store and tmux resources. Idempotent: a second call
returns `nil` without double-closing the underlying `*store.Store`. After
`Close` returns, any subsequent verb method call on the client returns the
sentinel `ErrClientClosed` (detectable via `errors.Is`).

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

**Concurrency.** `Open` calls `db.SetMaxOpenConns(1)`. `journal_mode=WAL`
and `foreign_keys=ON` are applied via DSN PRAGMAs and verified after open;
a silent downgrade fails `Open` rather than yielding a half-broken Store.

**File-system contract.** The parent directory (`~/.agent-director/` by
default) is created with mode 0700, and the database file is chmodded to
0600 on every `Open`. Repeated opens never widen permissions. A leading
`~/` in the path is expanded against `os/user.Current().HomeDir`.

Cross-reference: SRD §4.2 (canonical DDL), §4.5 (layer boundaries), §13.3
(single-writer + WAL rationale).

### `pkg/api/manifest` — Verb Registry

**What it is.** A single Go source file at
`pkg/api/manifest/manifest.go` driven by a `//go:generate` directive.
Each `VerbDef` entry records:

- the verb name,
- a one-line description,
- its parameters (name, type, description, required flag),
- its result fields (name, type, description), and
- the set of error names it may emit.

A package-level `var Verbs []VerbDef` holds the ordered registry, and
`Lookup(name)` returns a single entry by name.

**Consumers of `Verbs`.**

1. CLI dispatch table in `cmd/agent-director/main.go`.
2. MCP tool schema served in `mcp` mode (Epic 11).
3. Generated reference docs `docs/cli-reference.md` and
   `docs/mcp-reference.md`, written by `tools/gen-docs`.

Verb additions/edits go in `pkg/api/manifest` only; the CI doc-drift
gate re-runs `go generate` and fails if any tracked file changes.

**How to add a verb.**

1. Add a `VerbDef` literal to `Verbs` in
   `pkg/api/manifest/manifest.go`. Populate `Name`, `Description`,
   `Params`, `ResultFields`, and `ErrorNames` (empty slice, not nil, when
   the verb has no error conditions).
2. Implement the handler in `pkg/api` (typed parameter struct in,
   typed result struct out, returning a Go `error`). Keep SQL inside
   `internal/store`; the handler calls store primitives.
3. Add a method on `pkg/api.Client` that calls the new handler. Then
   add a closure in the `handlers(client)` map in
   `cmd/agent-director/main.go`: parse flags into a params struct,
   call `client.VerbName(params)`, and marshal the result as JSON via
   `writeJSON`. The `cmd/` file must contain no implementation logic —
   only flag parsing, the `client.X(params)` call, and JSON output.
4. If the verb emits new error sentinels, follow the checklist in
   [Err-name five-way coherence](#err-name-five-way-coherence) before
   proceeding — the CI drift gate will fail if any of the five sources
   are out of sync.
5. Run `make generate` to regenerate `docs/cli-reference.md` and
   `docs/mcp-reference.md` from the manifest.
6. Verify idempotency: re-run `make generate` and confirm `git status`
   shows no diff. A second run that produces a diff means the generator is
   non-deterministic — fix it before merging.

**Prohibitions.**

- Do not hand-edit `docs/cli-reference.md` or `docs/mcp-reference.md`.
  They are auto-generated; the CI drift gate will fail.
- Do not define CLI flags outside the manifest. New params go in the
  matching `VerbDef.Params` literal.
- Do not hand-write MCP tool schemas. The MCP server reads from `Verbs`.
- Do not have `pkg/api/manifest` import `internal/store`,
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
                |   cmd/agent-director   |
                |   (CLI dispatch, exit   |
                |    codes, JSON errors)  |
                +-----------+-------------+
                            |
                            v
                +-------------------------+
                |   pkg/api               |
                |   (verb-handler home;   |
                |    public Client facade;|
                |    owns store/tmux/cfg) |
                +-----------+-------------+
                            |
                            v
                +-------------------------+
                |   internal/store        |
                |   (sole SQL owner;      |
                |    schema v1 / SRD §4.2)|
                +-------------------------+

   internal/config -----> consumed by pkg/api and cmd/
                          (never imports internal/store)

Sibling packages under internal/ consumed by pkg/api:
    internal/spawn   - lifecycle of Claude Code child processes
    internal/hook    - hook-event entrypoint logic
    internal/tmux    - tmux session orchestration
    internal/probe   - liveness / health probes
    internal/mcp     - stdio MCP server implementation

The arrow direction is a hard rule. internal/store knows nothing about its
callers; cmd/ knows nothing about SQL. Any PR that introduces a back-edge
(e.g. internal/store importing pkg/api) should be rejected at review.
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
   ┌─────────┐   template merge: caller fields overlay template
   │ Resolve │   defaults; nil caller field → template value kept.
   └────┬────┘   ClaudeArgs: nil means caller supplied nothing
        │        (template wins); non-nil replaces wholesale.
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
   │ Launch │   --settings JSON synthesis; pre-trust cwd in ~/.claude.json;
   └────┬───┘   tmux new-session via direct argv. Fire-and-forget.
        ▼
   claude_instance_id (state stays `pending` until SessionStart fires)
```

### Workspace-trust pre-write

Claude Code shows a one-time "Quick safety check: Is this a project you
created or one you trust?" modal the first time it sees a new cwd. The
modal blocks before `SessionStart` fires, so a Spawn into a fresh cwd
sits in `pending` forever and `send-keys` refuses to drive it (the
precondition is a live state). Before exec'ing tmux, `internal/spawn`
reads `~/.claude.json`, sets
`projects.<canonical cwd>.hasTrustDialogAccepted = true`, and writes
the file back atomically (temp + rename) so a torn write against the
operator's own Claude Code session is impossible. The same file is
written by the operator's Claude Code itself; concurrent updates use
last-writer-wins — the window is small and the outcome (both writers
end up with the same key set to `true`) is safe.

`--no-pre-trust` (`SpawnParams.NoPreTrust`) opts out for callers that
explicitly want the human-in-the-loop trust dialog — e.g. spawning into
a directory handed in by an untrusted caller. The flag defaults off, so
pre-trust is the default behavior. When the file does not exist (truly
fresh Claude Code install), the write is skipped and a soft warning
lands on stderr; the spawn proceeds and the trust dialog is unavoidable
in that case.

Only `hasTrustDialogAccepted` is touched. Sibling keys
(`hasCompletedProjectOnboarding`, `hasClaudeMdExternalIncludesApproved`,
etc.) have semantics beyond trust and are left alone. Unknown top-level
and per-project keys round-trip verbatim via a `map[string]json.RawMessage`
shape, so future Claude Code releases that add keys are forward-compatible.

Layer boundaries (load-bearing):

- `internal/spawn` calls `internal/store` (one `InsertPending` UPSERT
  and one `LiveSpawnExists` collision read) and `internal/tmux` (one
  `NewSession` argv). Nothing else.
- `internal/hook` calls `internal/store` (state UPSERT + session-id
  write). Never `internal/tmux`, never `internal/spawn`.
- `pkg/api` is the verb-handler surface: it composes `internal/spawn`
  calls for the `spawn` verb and direct `internal/store` reads for
  `status` / `get`. No SQL strings, no tmux argv at this layer.

The hook handler is invoked via the per-Spawn `--settings` JSON
synthesized in stage 4. The handler's binary path is resolved via
`os.Executable()` (`/proc/self/exe` on Linux, `_NSGetExecutablePath` on
macOS) so it is always the same binary version that ran the `spawn`
call.

### Opt-in dynamic help-hook injection

When `defaults.inject_help_hook = true` is set in `config.toml`,
stage 4 also appends a second `SessionStart` entry to the synthesized
`--settings`: a single `command` of
`~/.agent-director/bin/agent-director help` (post `~` expansion).
This mirrors the static hook `install.sh` writes into
`~/.claude/settings.json`, but routes through `--settings` instead of
disk so a Spawn whose `CLAUDE_CONFIG_DIR` is fresh (or otherwise
missing the static entry) still receives the help manifest on
SessionStart.

The flag is off by default; the install dialog's Q4 toggles both halves
together — `Q4=yes` writes the static `settings.json` hook *and* sets
the config flag; `Q4=no` (`install.sh --no-hooks`) leaves both
unchanged. The hook command is the absolute install path rather than
a bare `agent-director` because the spawned Claude's PATH may not
include `~/.local/bin` and the hook fires before any shell-rc
manipulation can run.

## Interact: `send-keys` + `read-pane`

A tracked Spawn is externally drivable: an orchestrator can deliver text
into its tmux pane and read the rendered TUI back out without attaching
to the session. The two verbs are typed Go functions
in `pkg/api`, each calling exactly one method on the shared
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

- **A single Enter is always appended after the text.** Implemented
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

- **`ansi=true` — `tmux capture-pane -p -e` is invoked.** The `-e` flag
  tells tmux to emit SGR / cursor escapes in its output; the bytes are
  returned verbatim with no verb-layer strip.

Errors: `ErrSpawnNotFound` (unknown id), `ErrTmuxCaptureFailed`
(transport-layer tmux failure — e.g. the session vanished between the
row lookup and the capture call). Unlike `send-keys`, `read-pane` has
*no* state precondition: a caller can inspect an `ended` Spawn's final
pane bytes as a post-mortem.

### Layer boundaries

- `pkg/api/sendkeys.go` and `pkg/api/readpane.go` are the
  verb surfaces — typed params in, typed result + error out, errors
  matched via `errors.Is`.
- They call `internal/store` for the row lookup and `internal/tmux` for
  the wire op. Each verb is one row read plus one or two tmux calls.
- No SQL strings, no shell, no `&&`/`|`/`$VAR` at any layer (SRD §4.3,
  §14.3). tmux invocations go through `*tmux.Client.SendKeys` /
  `CapturePane`, both of which use direct `exec.Command` with the text
  as a single argv element.

## Label model

Labels are caller-owned tags on a Spawn, surfaced two ways and never
re-read after spawn time.

### Sources of truth

The **DB is canonical** (SRD §11). The `spawns.labels` column carries
a JSON object with the verbatim caller-supplied keys and values. The
`list` verb's label filter consults this column via
`json_extract(labels, '$.<key>') = '<value>'`.

The **env-var emission** is a derived view, set on the tmux session at
creation time so the Spawn's own shell can introspect its labels and
child processes can inherit them. Each entry becomes:

```
AGENT_DIRECTOR_LABEL_<NORMALIZED_KEY> = <value>
```

where `<NORMALIZED_KEY>` is the caller key uppercased with every
non-alphanumeric rune replaced by `_` (SRD §7.2 step 5). The
transformation is **unidirectional** — the env-var name does not
need to round-trip back to the DB key. A key like `my-key` produces
env `AGENT_DIRECTOR_LABEL_MY_KEY=val` while the DB row keeps
`"my-key":"val"` verbatim.

### Hooks do NOT mutate labels

State-tracking hooks (SRD §3.2) do not read or write the labels
column. Labels live in their own data plane:

- Set at `spawn` time only.
- Never changed by SessionStart / UserPromptSubmit / PreToolUse /
  SessionEnd / etc.
- The env-var view is similarly frozen at session creation; tmux
  does not re-evaluate `-e` flags after the session starts.

### parent_id

`parent_id` is auto-derived alongside labels at spawn time, but lives
on its own column:

- `internal/spawn/launch.go` reads `os.Getenv("AGENT_DIRECTOR_INSTANCE_ID")`
  in the spawning process.
- If set, that value is written to the new row's `parent_id`.
- If unset (operator running from a plain shell), `parent_id` is NULL.
- The schema's `ON DELETE SET NULL` on the FK keeps orphans clean
  when find-missing (Epic 8) later removes a parent row.

The `list --parent <id>` filter walks this column directly; the
MCP server (Epic 11) exposes the same filter so an LLM client can
map a tree by recursive listings.

## Install layout

The `skills/install-agent-director/` skill (Epic 12) lays out the
on-disk install so an operator can run, upgrade, and uninstall
agent-director with one script.

### On-disk shape

```
~/.agent-director/
├── bin/
│   ├── agent-director            (the binary; regular file, mode 0755)
│   └── agent-director.prior      (optional rollback snapshot; --keep-prior)
├── state.db                       (mode 0600)
├── state.db-wal                   (when WAL is active)
├── state.db-shm
├── templates/                     (mode 0700; created lazily)
│   └── <name>.toml                (mode 0600)
├── config.toml                    (operator-owned; not created here)
└── errors.log                     (touched on first hook-fire failure)

~/.local/bin/agent-director       → ~/.agent-director/bin/agent-director   (optional)

~/.claude/settings.json
└── hooks
    ├── SessionStart  → [{hooks: [{type: command, command: "<bin> help"}]}]
    └── SessionEnd    → [{matcher: "compact", hooks: [{type: command, command: "<bin> help"}]}]
```

### Upgrade-safety pattern

The install script uses the standard single-binary CLI install
pattern (gh, kubectl, terraform): write to a sibling temp path,
then `mv` over the target. `mv` within one filesystem is atomic at
the inode level — concurrent readers see either the old binary or
the new, never half. A running process holds the old inode, so an
in-flight exec is unaffected by the swap.

1. Write the new binary at `agent-director.tmp.$$` next to the
   target.
2. `chmod 0755` the temp file.
3. `mv` it onto `agent-director`.

Optional `--keep-prior` snapshots the existing binary to
`agent-director.prior` before step 3, giving a one-step rollback
(`mv .prior canonical`). Without it, rollback is a re-install of the
previous tag via `install.sh --from-release v<old>`. The
version-manager pattern (canonical symlink → versioned files) was
considered and rejected for b.43y: it only earns its complexity when
multiple concurrent versions are actually being managed.

### Uninstall semantics

`uninstall.sh` removes ONLY what `install.sh` wrote: the canonical
binary, the optional `.prior` rollback snapshot (and any
legacy versioned-binary siblings left over from pre-b.43y installs),
the optional PATH symlink, and the two hook entries it injected
(matched by the install root prefix in their command string). Other
user hooks in `SessionStart` / `SessionEnd` survive verbatim.
`~/.agent-director/` itself is preserved by default — operators
frequently want to keep templates and state.db across reinstalls.

`--purge` is the explicit nuke path: a full `rm -rf
~/.agent-director` with an interactive confirmation
(`--force` skips the prompt). State, templates, and any local
edits to `config.toml` are lost.

### ErrSchemaMismatch recovery

v1 has no migration story (SRD §19 Q11). If `agent-director help`
reports `ErrSchemaMismatch` after an upgrade, the recovery is
`rm ~/.agent-director/state.db*` followed by a re-run. Spawn
history in the DB is lost; JSONL transcripts under
`~/.claude/projects/` survive independently and can be re-resumed
by id (the operator's notes) via `agent-director resume`.

## Stdio MCP server

The MCP server is the JSON-RPC-over-stdio surface
(`internal/mcp`). It exposes every CLI verb as an MCP tool so an
MCP-capable LLM client can drive Spawns without going through the
shell. The server is **long-lived** per SRD §3.3: config is loaded
once at startup; in-flight edits to `~/.agent-director/config.toml`
don't take effect until the next `serve --stdio` invocation.

### Drift-free schema generation

The tool list is generated from `pkg/api/manifest.Verbs` — the
same single source of truth that drives the CLI flag definitions
and the reference docs (`cli-reference.md`, `mcp-reference.md`).
Adding a verb to the manifest exposes it via MCP on the next server
start with NO source changes in `internal/mcp`. The
drift-by-construction invariant is pinned by
`TestToolsListMatchesManifest` in `internal/mcp/server_test.go`:
the test enumerates `manifest.Verbs` at run time and compares the
result against `tools/list`'s output, so a new manifest verb that
isn't filtered automatically extends the test.

### Layer map

Both the CLI and the MCP server dispatch through the same `pkg/api.Client`
facade. `LiveDispatcher` holds a single `*pkg/api.Client`; each tool `case`
in `LiveDispatcher.Call` decodes the MCP JSON args and calls `client.X(…)`.
There is no longer a parallel dispatch path mirroring the CLI's — both
surfaces share the same facade. Every verb call from CLI or MCP routes
through the same Client method, so behavioral divergence between CLI and MCP
outputs is structurally prevented.

```
  cmd/agent-director                 internal/mcp/server.go
  (CLI: flag parse → client.X)       (MCP: JSON-RPC 2.0 on stdin/stdout)
          │                                      │
          │                          Dispatcher.Call(ctx, name, args)
          │                                      │
          │                          internal/mcp/dispatch.go::LiveDispatcher
          │                          (switch on verb name; decode JSON args)
          │                                      │
          └──────────────┬────────────────────────┘
                         │  client.X(params)
                         ▼
               pkg/api.Client
               (verb-handler home; holds store, tmux, config)
```

### Per-Client logger (Pin H4)

`serveHandlerWith` constructs a **separate** `*pkg/api.Client` for the MCP
dispatcher, with `Options.Logger: nil`. This preserves the pre-refactor
behavior where Kill/FindMissing/Expire swallow their tmux WARN logs on the
MCP path — those warnings are most useful to the interactive CLI operator,
not a long-lived MCP client. The CLI's main Client (constructed in
`setupClient`) and the MCP's Client have distinct logger ownership; neither
shares the other's `log.Logger`.

### Filtered verbs

`tools/list` omits two verbs:

- `hook` — internal entrypoint invoked by Claude Code's hook machinery.
- `serve` — the MCP server itself.

The filter lives in `internal/mcp/server.go::ExposedVerb`.

### Name mapping

MCP tool names use underscores (`send_keys`); manifest verb names
use hyphens (`send-keys`). `ToolName` + `VerbNameFromTool` are
symmetric inverses, pinned by `TestToolNameMapping`. The mapping is
faithful because no current verb name carries an underscore — if
that changes, the round-trip test will catch it.

### Error envelope

Tool-call failures return a JSON-RPC error response with the
canonical SRD §13.1 err_name in the `data` field:

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "error": {
    "code": -32000,
    "message": "spawn id-x: not found",
    "data": {
      "err_name": "ErrSpawnNotFound",
      "err_description": "ErrSpawnNotFound: spawn id-x: not found"
    }
  }
}
```

The err_name string is resolved at call time by `errnames.Classify`
(from `pkg/api/errnames`) — the single source of truth for the
err_name mapping. Both the CLI's JSON envelope writer and the MCP
server's `classifyDispatchError` delegate to `errnames.Classify`,
so the same canonical names appear in both surfaces for the same
wrapped errors. See the [err_name catalog](#err_name-catalog) subsection below.

### Success envelope

Tool-call success returns the verb's typed result, JSON-encoded,
wrapped in MCP's content shape:

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {"type": "text", "text": "{\"claude_instance_id\":\"id-x\"}"}
    ]
  }
}
```

This mirrors the CLI's stdout shape — a script reading the CLI and
an MCP client see the same JSON. The content array is single-text-
part for v1; richer types (resource URIs, images) are out of scope.

### err_name catalog

`pkg/api/errnames` is the single source of truth for all err_name
strings in agent-director. No other package declares or owns these
strings; callers read them through this package only.

**`Catalog []Entry`** — the canonical lookup table. Each `Entry` has:

| Field | Type | Description |
| --- | --- | --- |
| `Name` | `string` | The canonical err_name string (e.g. `"ErrSpawnNotFound"`). |
| `Err` | `error` | The sentinel error the name maps to. Matching uses `errors.Is`, so `%w`-wrapped errors resolve correctly. |

**`Classify(err error) (name, description string)`** — walks `Catalog`
via `errors.Is` and returns the first matching entry's `Name` plus
`err.Error()` as the description. Errors not present in the catalog
collapse to `"ErrInternal"` — production paths should never reach
this; tests pin canonical names directly.

**`TrimNamePrefix(name, description string) string`** — strips the
redundant `"ErrName: "` prefix from a description string when present.
Used by the CLI's `writeApiError` so the envelope reads cleanly instead
of carrying the err_name in both the `err_name` field and the
`err_description` text.

**`ErrUnknownTool`** — declared in `internal/mcp/server.go` because it is
a dispatch-level error (the MCP transport layer detected an unknown tool
name), not a verb-surface error. As a result it is NOT in `errnames.Catalog`
(which is verb-surface only). `internal/mcp/server.go::classifyDispatchError`
special-cases `errors.Is(err, ErrUnknownTool)` before delegating to
`errnames.Classify` for verb-surface errors.

**`catalog.json`** — a machine-readable snapshot of `Catalog`,
generated deterministically by `go generate ./pkg/api/errnames/...`.
The doc-drift CI gate (`make doc-drift`) enforces that the checked-in
`catalog.json` stays in sync with the Go source.

**Consumers:** `cmd/agent-director`'s envelope writer and
`internal/mcp`'s `classifyDispatchError` both call `errnames.Classify`
directly — there is no register-then-classify two-step; the `Catalog`
slice itself is the declaration. See [Err-name five-way coherence](#err-name-five-way-coherence)
for the invariant that keeps all five sources in sync.

### Err-name five-way coherence

The err_name system enforces a five-way invariant: five separate sources of truth must stay
mutually consistent whenever a sentinel is added, renamed, or removed.

**The five sources**

| # | Source | Location |
| --- | --- | --- |
| (a) | Sentinels referenced by handler code via `fmt.Errorf("%w: ...", X)` | `pkg/api/*.go` |
| (b) | Entries in `pkg/api/errnames.Catalog` | `pkg/api/errnames/catalog.go` |
| (c) | Per-verb `ErrorNames` slices in callable verbs from `manifest.CallableVerbs()` | `pkg/api/manifest/manifest.go` |
| (d) | Exported `var Err*` declarations in `pkg/api` | `pkg/api/errors.go` |
| (e) | Committed JSON snapshots regenerated from Go source | `pkg/api/errnames/catalog.json`, `pkg/api/manifest/surface.json` |

Non-callable verbs (`help`, `serve`, `hook`) are **intentionally excluded** from source (c):
they have no handler code in `pkg/api/*.go`, so including their `ErrorNames` would produce
false-positive coherence failures.

**Enforced check directions**

Adding or removing a sentinel requires updating all relevant sources; editing the manifest
or catalog Go source requires regenerating the corresponding JSON file.

- **(a) → (b)**: Every sentinel referenced in handler code must have a Catalog entry.
  Enforced by `TestFiveWayCoherence` check 1 (runtime).
- **(c) → (b)**: Every `ErrorNames` entry in a callable verb must have a Catalog entry.
  Enforced by `TestFiveWayCoherence` check 3 (runtime).
- **(b) → (c)**: Every Catalog entry must appear in at least one callable verb's `ErrorNames`.
  Enforced by `TestFiveWayCoherence` check 4 (runtime).
- **(b) → (d)** *(compile-time)*: `catalog.go` imports `pkg/api` and references `api.ErrX`
  directly — a missing `var` declaration is a build failure, not a test failure. This is
  why `TestFiveWayCoherence` check 2 has no `t.Errorf`; the compiler already enforces it.
- **(e) freshness**: `TestCatalogJSONUpToDate` and `TestSurfaceJSONUpToDate` re-run the
  generators and diff the output against the committed files. A stale JSON file fails the
  test with a clear "run `make errnames-json`" / "run `make surface-json`" message.

**Documented exclusions**

- `ErrInternal` is the `Classify` fallback for unrecognized errors. It is not in the Catalog
  and is not enforced by the coherence check.
- Catalog entries whose sentinels are declared in `internal/*` packages (e.g.
  `tmux.ErrTmuxNotAvailable`, `store.ErrSpawnNotFound`) do not appear in `exportedSentinels`
  and are therefore excluded from check 2. Their coherence with the Catalog is enforced at
  compile time — `catalog.go` imports and references them directly.
- Non-callable verbs (`help`, `serve`, `hook`) are excluded from the manifest-side coherence
  checks (source (c)).

**CI enforcement**

`.github/workflows/doc-drift.yml` runs on every PR and push to `main`:

1. `make err-coherence` runs `TestFiveWayCoherence`, `TestCatalogJSONUpToDate`, and
   `TestSurfaceJSONUpToDate` in-process, covering all five sources.
2. Explicit drift gates re-run `make errnames-json` and `make surface-json` and fail if
   the committed JSON files differ from the regenerated output.

**Adding a new sentinel**

1. Add `var ErrFoo = errors.New("ErrFoo: ...")` to `pkg/api/errors.go`.
2. Reference it from handler code: `return fmt.Errorf("%w: ...", ErrFoo)`.
3. Add a Catalog row in `pkg/api/errnames/catalog.go`: `{Name: "ErrFoo", Err: api.ErrFoo}`.
4. Add it to the relevant verb's `ErrorNames` slice in `pkg/api/manifest/manifest.go`.
5. Add a `packageOf` map entry in `pkg/api/errnames/generate.go`. **This map is
   hand-maintained** — `errors.New` returns a plain `*errorString` with no package
   attribution that reflection can recover, so the generator cannot infer the origin
   package automatically. A missing entry causes the generator to exit 1 with an explicit
   error message.
6. Run `make errnames-json && make surface-json` to refresh the committed JSON outputs.
7. Run `make err-coherence` locally to confirm all five checks pass before pushing.

### Registration

```sh
claude mcp add agent-director /path/to/agent-director serve --stdio
```

Claude Code stores this in its MCP config and launches the binary
on session start. The binary's `~/.agent-director/config.toml` is
the same one the CLI uses; SRD §3.3 says edits take effect on the
next session.

## Permission relay

Orchestrators can intercept tool permission requests from Spawns and
decide allow/deny out-of-band. Conceptually:

```
  Claude Code ─PermissionRequest hook→ agent-director hook (polling)
                                            ↑
                                            │ writes decision
                                            │
                        orchestrator → agent-director decide
```

### Components

- **`internal/hook/envelope.go`** — `EncodeDecision(behavior, reason)`
  serializes the SRD §6.3 `hookSpecificOutput` envelope. The deny-
  default-message ("Denied by orchestrator") and the allow-message-
  omission rule both live here.

- **`internal/hook/polling.go`** — `Poll` is the loop. Pure function
  taking `(ctx, store, clock, cfg.Relay, id, *rand.Rand)`. The
  clock seam lets tests inject a fast variant; the rng is per-call
  so the jitter is deterministic in tests.

- **`internal/hook/permission.go`** — `runRelay` orchestrates the
  PermissionRequest relay path: UPSERT the open row, call `Poll`,
  write the envelope. Always emits an envelope before returning.

- **`internal/hook/handler.go`** — branches into `runRelay` when the
  event is `PermissionRequest` AND `AGENT_DIRECTOR_RELAY_MODE=on`.
  Every pre-relay failure path emits a deny envelope when relay is
  active (SRD §6.4).

- **`internal/store/permission.go`** — three primitives:
  - `UpsertOpenPermissionRequest`: DELETE-INSERT in one tx.
  - `GetPermissionRequest`: the polling-loop read.
  - `DecidePermissionRequest`: the race-free decide UPDATE.

- **`pkg/api/decide.go`** — verb wrapper. State guards
  (`ErrRelayModeOff`, `ErrSpawnNotFound`, `ErrInvalidDecision`)
  before the UPDATE, plus the RowsAffected==0 disambiguation
  (`ErrAlreadyDecided` vs `ErrNoOpenPermissionRequest`).

### Polling cadence + the 50ms floor

The per-iteration sleep is
`max(50ms, cfg.PollBaseMs + uniform(0, cfg.PollJitterMs))`. SRD §6.2
specifies the floor explicitly so a misconfigured 0+0 config cannot
pin CPU. Default: `100ms + 0..100ms`.

### Fail-closed boundary

SRD §6.4 enumerates the failure modes. They split into two scopes:

**Pre-relay (handler-level):** instance-id missing/invalid, payload
read failure, classify failure, UPSERT failure, session-id write
failure. The handler's `failClosed` helper writes a deny envelope
when `relayActive` is true.

**Inside the polling loop:** timeout expiry, `ctx.Done()`, row
preempted via `sql.ErrNoRows`, read-retry budget exhausted.
`runRelay` checks `PollResult.Decision` and writes a deny envelope
when it's empty.

The cmd/-side `runHook` ALSO has a pre-Handle fail-closed: if the
config can't be loaded or the store can't be opened, runHook itself
writes the deny envelope before returning. This is the SRD §6.5
"env-var, not DB" guarantee — even a store-open failure on a
relay-on Spawn still surfaces deny.

### Send-keys interaction

`pkg/api/sendkeys.go`'s precondition: when `relay_mode=on` AND
`state=check_permission`, return `ErrSendKeysWhileRelayed`. The
relay path owns the modal answer; a pane-side keystroke would race
the relay's decide write. The guard was added in Epic 4 (stubbed);
Epic 10 activates it end-to-end.

## Resume

Bringing a terminated Spawn back to life via `claude --resume`. Same
`claude_instance_id`, fresh tmux session, same JSONL transcript.

### Verb (`pkg/api/resume.go`)

Guards run in order; every error path is side-effect-free (no DB
mutation, no half-created tmux session):

1. `GetSpawn` → `ErrSpawnNotFound` on unknown id.
2. State must be `ended` or `missing` → otherwise
   `ErrSpawnNotResumable`. The verb refuses to touch a live Spawn.
3. `claude_session_id` populated → otherwise `ErrNoSessionId`. A
   Spawn killed before its first SessionStart hook fired has no
   rotated session id to point `--resume` at.
4. JSONL transcript file exists on disk → otherwise
   `ErrJsonlMissing`. Pure `os.Stat` pre-flight; no read.
5. Canonical tmux session name is free → otherwise the wrapped
   `tmux.ErrTmuxSessionCreate` sentinel. Resume does NOT auto-kill
   a stale session; the operator cleans up manually.
6. Re-derive `parent_id` from caller's `AGENT_DIRECTOR_INSTANCE_ID`
   env (NULL when unset). The DB write happens **before** the tmux
   launch — if launch fails, the parent stamp is a harmless stale
   value the next retry will overwrite.
7. `spawn.Relaunch` composes env + synthesized settings + the resume
   argv, fires `tmux new-session -d -s <name> -c <cwd> -e KEY=VAL ...
   claude --resume <session_id> --settings <json> [user claude_args]`.
   Fire-and-forget — no readiness wait.

The verb does NOT change the row's `state` or `ended_at`. Those
transitions happen when the resurrected Claude's first SessionStart
hook fires (see below).

### parent_id re-derivation (SRD §7.5)

`parent_id` records **who currently owns this Spawn**, not who
originally created it:

- Resume from a bare shell → `parent_id = NULL`.
- Resume from inside another Spawn → `parent_id = caller's id`.
- A Spawn originally parented to A and later resumed by B → `parent_id
  = B`.

The FK `ON DELETE SET NULL` cascade (Epic 3 schema) handles the
"former parent gets deleted" case orthogonally.

### SessionStart hook side of the contract

`ApplyHookTransition` treats every transition to a non-terminal state
as a chance to clear `ended_at`:

- Fresh spawn `pending → waiting`: `ended_at` was already NULL; the
  `ended_at = NULL` write is a no-op.
- Resurrection `ended/missing → waiting`: `ended_at` is cleared so
  the row's metadata reflects the active life rather than the dead
  past.

`claude_session_id` is overwritten by the same hook payload's
`transcript_path` basename. Claude Code rotates the UUID on every
`--resume`, so the column carries the freshly-rotated value after
the hook fires — the next resume from THIS resurrected lifetime uses
the new id, pointing at the new JSONL.

### JSONL path resolver (`internal/spawn/jsonl.go`)

```
~/.claude/projects/<slug(cwd)>/<session_id>.jsonl
```

`slug(cwd)` replaces every rune outside `[A-Za-z0-9-]` with `-`. Each
rune (single-byte or multi-byte UTF-8) collapses to **one** dash.

`slug(cwd)` differs from `SanitizeSessionName` (Epic 3): `_` is
replaced with `-` here, preserved there. The JSONL layout is owned
by Claude Code, so the two slug rules are not symmetric. Pinned by
`TestSlugDivergenceFromTmuxSanitizer`.

### What's not carried over

Two pieces of state are NOT stored on the row and are not
reconstructed on resume:

- **`Permissions`** — the synthesized `--settings` JSON carries fresh
  hook entries on resume but no `permissions` block. Resume relies
  on Claude Code's tier-stack permissions.
- **`ExtraEnv`** — the original spawn's extra env vars (e.g.
  `ANTHROPIC_API_KEY`) are NOT replayed. Auth on resume comes from
  the caller's shell env, which tmux propagates to the new session
  by default.

## Crash recovery and DB hygiene

Three verbs cooperate to keep the DB honest in the face of crashes,
manual kills, and accumulated history: `find-missing` (reconcile),
`expire` (age-out terminal rows), `delete` (admin force-removal).
All three are designed to run on cron at different cadences.

### Probe layer (`internal/probe`)

The prober answers one question: *which `AGENT_DIRECTOR_INSTANCE_ID`
values are currently observable in live process env blocks?* It is
the ground truth `find-missing` diffs against the DB.

Per-OS implementations are selected by build tags:

- **Linux (`probe_linux.go`)** — walks `/proc/<pid>/environ` for every
  numeric PID entry. The `environ` pseudo-file is the NUL-separated
  `KEY=VAL` block the kernel exposes. Default permissions make it
  owner-readable only — that's load-bearing: a `find-missing` run as
  the wrong user simply can't see the env vars and falls into the
  degraded-mode guard rather than corrupting state.

- **macOS (`probe_darwin.go`)** — `sysctl("kern.proc.all")` returns
  the kinfo_proc array; per PID, `sysctl("kern.procargs2", pid)`
  returns the argv+env blob. The KERN_PROCARGS2 layout is
  `uint32 argc` + null-terminated `exec_path` + `argv[0..argc-1]` +
  `envp[0..]`. `envFromProcArgs2` skips past the argv section to
  reach the env, then scans for the prefix.

  The kinfo_proc walker (`parse_kinfo.go`) carries two XNU-version-
  sensitive constants: `kinfoProcSize` (sizeof struct kinfo_proc) and
  `kinfoProcPIDOffset` (byte offset of extern_proc.p_pid). Both are
  pinned to XNU 11.x (macOS 14 / 15) and are NOT a kernel ABI
  guarantee — a future macOS major bump that resizes the struct will
  silently drift the stride-based walker. The parser's plausibility
  guard catches that: if more than 10% of decoded PIDs fall outside
  `[1, 4_194_304]`, `parsePIDsFromSysctlBuf` returns
  `ErrProbeUnsupported` and `find-missing` fails closed rather than
  emitting a garbage probe set.

  **macOS-major bump policy.** When supporting a new macOS major:
  compile the matching XNU sources (Apple publishes them at
  `apple-oss-distributions/xnu`), re-derive `kinfoProcSize` +
  `kinfoProcPIDOffset` from `<bsd/sys/proc.h>` + `<bsd/sys/sysctl.h>`,
  refresh the constant comments in `parse_kinfo.go`, and re-run
  `GOOS=darwin GOARCH=arm64 go build ./...` plus the prober's
  integration test under that macOS version. The plausibility guard
  is a safety net, not a substitute for the bump.

- **Other** — the fallback returns `ErrProbeUnsupported` so
  `find-missing` fails closed rather than silently treating "no
  per-OS impl" as "no live processes".

Permission-denied / process-gone errors mid-walk are skipped silently;
a single foreign-owned process can't poison the whole probe.

### Degraded-mode guard (SRD §14.6)

When the probe returns zero IDs AND the DB has ≥1 live-state row,
`find-missing` writes nothing and logs a warning to
`cfg.log.error_log_path`. The legitimate 0-live-rows + 0-probe-IDs
case is distinguished and treated as a fast no-op success.

### `find-missing`

`pkg/api/find_missing.go`:

1. `ListLiveSpawnIDs` returns every row where `state NOT IN (ended,
   missing)`. This includes `pending` — SRD §5.2 explicitly scans
   pending rows so a Spawn whose tmux died before SessionStart fired
   still reconciles correctly.
2. `probe.Probe()` collects live IDs.
3. Degraded-mode guard fires when warranted (see above).
4. Per row in the set-difference: `MarkSpawnMissing` sets
   `state='missing'` and `ended_at = now`. Per-row failures (e.g.
   transient SQLite I/O error) are logged and the sweep continues —
   one bad row does not abort the others.

The verb does NOT touch tmux. A row marked `missing` may still have
an orphaned tmux session if (somehow) the env-var check misfired
without the tmux process exiting. The operator clears those manually;
`agent-director kill` is the supported path.

### `expire`

`pkg/api/expire.go` calls
`store.DeleteTerminalOlderThan(duration)`, which executes a
`DELETE ... RETURNING claude_instance_id` against rows whose
`state IN (ended, missing)` AND `ended_at IS NOT NULL` AND
`ended_at < now - duration`.

- Default duration: `cfg.Defaults.ExpireRetentionDays * 24h` (config
  default is 31 days).
- `--older-than 0d` reaps every terminal row.
- NULL `ended_at` is preserved (conservative — would only happen for
  hand-edited or legacy rows).
- Live-state rows are never touched.
- The verb does NOT touch tmux or JSONL transcripts.

### `delete`

`pkg/api/delete.go` is the admin force-removal verb. It
processes ids one at a time, returning a per-row map of
`{id: "ok" | "<err_name>"}`. The batch never aborts on a partial
failure — every id in the input is attempted; the map records the
outcome.

`delete` bypasses every state-precondition guard. A live-state row
is removed by id exactly the same way a terminal row is. The verb
does NOT touch tmux or JSONL transcripts; the
`permission_requests` row(s) FK-referencing the spawn are removed
by the schema's `ON DELETE CASCADE`.

### Cron user invariant

All three verbs assume they run as the same user that owns the
Spawns (or as root). `find-missing` exposes mismatches via the
degraded-mode guard. `expire` and `delete` are pure DB operations
and don't depend on probe permissions; running them as the wrong
user is harmless (they just operate on whatever rows the DB happens
to hold).

The recommended operator setup is a systemd user-timer or a personal
crontab — not a system-level cron — so the userland identity matches
the Spawn-launching identity automatically.

## Stop semantics

Two verbs terminate a Spawn — `kill` (immediate, forceful) and `pause`
(graceful, bounded). They make different promises about the Spawn's
final state and need to be reached for in different situations.

### `kill`

1. Row lookup. Unknown id → `ErrSpawnNotFound`.
2. Terminal state (`ended` / `missing`) → success, no tmux call.
3. Otherwise → invoke `tmux.KillSession`. Any tmux error is swallowed.

Kill does not mutate the row's `state` column. The row stays in its
pre-kill state until find-missing (Epic 8) reconciles it.

### `pause`

`pause` is the only verb with a polling loop:

1. Row lookup. Unknown id → `ErrSpawnNotFound`.
2. Terminal state (`ended` / `missing`) → no-op success.
3. State == `waiting` → send `/exit` then `Enter` via two
   `tmux.SendKeys` calls, then poll the row's state column at a
   fixed interval until either `state == ended` (success) or
   `pause.timeout_seconds` elapses (`ErrPauseTimeout`). `ctx.Done()`
   short-circuits the loop with `ctx.Err()`.
4. State ∈ {`pending`, `working`, `ask_user`, `check_permission`}
   → `ErrSpawnNotPausable`.

Pause is one-shot; no incremental progress callback.

### kill vs find-missing

kill leaves the row in its pre-kill live state; find-missing is the
reconciliation path. The flow:

1. Operator runs `kill <id>`.
2. tmux session goes away; row still says `waiting`.
3. find-missing scans rows in live states, asks tmux about their
   sessions, and flips any whose session is gone to `missing`.
4. Subsequent `status <id>` reports `missing`.

## Release engineering

### Supported platforms

agent-director ships as four pre-built static binaries, one per
target tuple:

| OS | Arch | Format | Static |
|---|---|---|---|
| linux | amd64 | ELF 64 LE | yes (no libc dep) |
| linux | arm64 | ELF 64 LE | yes (no libc dep) |
| darwin | amd64 | Mach-O 64 LE | n/a (no system linker) |
| darwin | arm64 | Mach-O 64 LE | n/a (no system linker) |

Windows is not supported (SRD §16.1).

Linux binaries are statically linked via `CGO_ENABLED=0` plus
`modernc.org/sqlite` (pure-Go SQLite driver, no libsqlite3
dependency). The `release-binaries-smoke` target verifies static
linkage on every release via `ldd` ("not a dynamic executable").

### Semver policy

agent-director uses strict `vMAJOR.MINOR.PATCH` semantic
versioning. For v1:

- **MAJOR**: bumped on any wire-shape change to the CLI JSON
  envelope, the MCP tool schemas, or `~/.agent-director/config.toml`.
  Operators script against these surfaces; we don't break them
  without a major.
- **MINOR**: new verbs, new manifest entries, new config knobs.
  Strictly additive — existing scripts continue to work.
- **PATCH**: bug fixes, doc updates, internal refactors.

Pre-release tags (e.g. `v0.1.0-rc1`) are **not supported in v1**.
The release skill rejects them at the semver gate. Iterating
toward a release happens on a branch; the tag lands once.

### The release skill

`skills/release-agent-director/release.sh` automates the workflow
documented in `skills/release-agent-director/SKILL.md`. Behavior:

1. Validate the semver tag.
2. Verify `gh` (GitHub CLI) is authenticated.
3. Confirm the working tree is clean (no uncommitted edits).
4. Confirm the tag doesn't already exist.
5. Confirm the current branch matches `--branch` (default `main`).
6. `git tag -a $VERSION -m "$VERSION" && git push origin $VERSION`.
7. `make release-binaries` — cross-compiles the four targets.
8. Template release notes from `git log <prev-tag>..HEAD`,
   grouped by Epic ID where commit messages reference one.
9. `gh release create $VERSION dist/* --notes-file <generated>`.

`--dry-run` skips steps 6-9 and prints the templated notes. Used
for CI smoke tests and pre-tag reviews. The dry-run path also
relaxes the `gh` requirement since it never actually calls `gh`.

### ErrSchemaMismatch on upgrade

v1 has no migration story (SRD §19 Q11). Bumping `schemaVersion`
in `internal/store/store.go` requires:

1. Document the schema change in the release notes.
2. Tell operators to `rm ~/.agent-director/state.db*` post-upgrade.
3. Active Spawns whose JSONL transcripts under
   `~/.claude/projects/` are still on disk can be re-resumed by id
   via `agent-director resume`.

## Test Harness

Every functional Epic is gated by a Docker-based integration harness rather
than `go test ./...`.

### Container

`test/Dockerfile` builds `agent-director-test`:

- Base: `debian:bookworm-slim`.
- Installs `tmux`, `nodejs` 20, `jq`, `sqlite3`, `git`.
- Installs `@anthropic-ai/claude-code@<pinned>` (see "Pinned Claude Code
  version" below).
- Copies in the pre-built `agent-director` binary from `./bin/`.
- Runs as a non-root `tester` user with `HOME=/home/tester`.
- Default command is `/opt/driver/run-testplan.sh`.

The image is built via `make test-image`. `make test-image-smoke` exercises
it standalone: confirms `claude --version` reports the pinned version,
`agent-director help` exits 0, and the driver rejects an unknown EPIC.

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
   removes `~/.agent-director/state.db` + WAL/SHM, kills tmux sessions
   matching the `cd-` prefix, then calls `agent-director help` to
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

Test credentials should be CI-secret-scoped, distinct from the operator's
primary account.

`DRIVER_MODE=shell` runs without any credential. `DRIVER_MODE=claude`
requires one of the env vars above.

### testplans hive convention

`tickets/testplans/` is a bees hive. One t1 collector per Epic; t2 cases
are plain-English bodies. The driver reads `t1.*.md` and `t2.*.md` files
directly. Each t2 body has a fenced ```bash``` block executed by
`DRIVER_MODE=shell`; the same prose is the spec the `DRIVER_MODE=claude`
path hands to the driver-Claude.

### Pinned Claude Code version

The harness installs `@anthropic-ai/claude-code@2.1.120`. Pin sites:
`CLAUDE_CODE_VERSION` in `Makefile`, the `ARG` in `test/Dockerfile`.
Empirical behaviors the harness relies on (env-var auth, settings merge,
hook surface) are recorded in `reference/*-research.md`.

### CI lane

`.github/workflows/integration.yml` defines two jobs:

- `linux-integration` — runs `make test-docker EPIC=harness-smoke` on
  `ubuntu-latest` for every PR and push to `main`. Auth env vars come
  from `${{ secrets.ANTHROPIC_API_KEY }}` and
  `${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}`.
- `macos-stub` — runs on `macos-latest`, exits 0 with a stub message.
  Epic 8 (sysctl-based liveness probe) will swap this for a real macOS
  test that exercises the sysctl path. SRD §19 Q7.

### Audit standard

Per SRD §17, the orchestrator runs `make test-docker` first-hand, reads
the per-case JSON stream, and signals "continue" only after confirming
each case executed and passed.
