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
| `internal/api/manifest` | Defines and exposes the canonical CLI/MCP verb manifest used to keep the CLI surface, MCP tool surface, and docs in lock-step. | stdlib; `internal/store` (read-only handle types only); `internal/config`. | `cmd/*`; raw `database/sql`; SQL strings. |

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
