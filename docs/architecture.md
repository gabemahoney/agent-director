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

## Supported platforms (v1)

The v1 supported-platform set is **`linux/amd64`**, **`linux/arm64`**,
and **`darwin/arm64`**. `darwin/amd64` (Intel Mac) was **dropped on
2026-05-24** (no Intel Mac users to serve). The CLI is released for
all three; the TS Client's npm sub-packages ship the CLI binary for
linux-x64 and darwin-arm64.

All three binaries are pure `CGO_ENABLED=0` cross-compiles (pure-Go
SQLite via `modernc.org/sqlite`). There is no FFI / shared-library
build path; the b.eiv refactor (b.19d) replaced the previous `pkg/cabi`
FFI surface with a subprocess-CLI architecture where the TS Client
spawns the CLI binary per verb call.

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
| `cmd/agent-director` | Thin CLI shim: argv parser and JSON envelope marshaller. Constructs one `pkg/api.Client` at startup via `setupClient()`; every store-backed verb calls a method on that Client (`client.Spawn(params)`, `client.Status(id)`, etc.) — no business logic lives in `cmd/`. **DB-free exceptions:** `help`, `--help`, `version`, no-args (routes to help), and `trail-emit` are dispatched BEFORE `setupClient` so they never open or create `~/.agent-director` (SR-4.1/4.2); help/version run against a zero-value `Client` and consult no store. **`runHook` exception:** retains independent `config.Load` + `store.Open` calls per SRD §3.2 fail-open; hook fires must never be blocked by Client-startup failures. | stdlib; `pkg/api`; `pkg/api/errnames`; `internal/hook`; `internal/config` and `internal/store` (error sentinels only) in `setupClient`; `internal/config` in `runHook` and `newHookLogger`. | Direct `database/sql` use; raw SQL strings; ad-hoc subprocess management; `store.Open` / `config.Load` / `tmux.New` outside `runHook`, `newHookLogger`, and `setupClient`'s logger bootstrap. |
| `pkg/api` | **Canonical verb-handler home and public surface.** Opaque `Client` facade — no exported fields, construction via `New` only. Owns all verb implementations, seam interfaces (`ListStore`, `PauseStore`, `KillTmux`, `KillLogger`, etc.), params/result types, and error sentinels. Owns store, tmux, and config internally; exposes one method per CLI verb; idempotent `Close`. Consumed by `cmd/agent-director` and `internal/mcp`. **tmux:** `TmuxClient` (the `Options.TmuxClient` injection point, SRD Appendix F.3) carries the eight socket-taking methods (`Lookup`, `ListPanes`, `KillPane`, `KillSessionID`, `SendKeysPane`, `CapturePaneID`, `NewSession`, `SetLabel`) beside the five name-based ones (`NewSessionByName`, `HasSession`, `KillSession`, `SendKeys`, `CapturePane`); `*tmux.Client` and `tmuxfix.Recorder` implement it. `tmux_aliases.go` re-exports the typed tmux API as `Tmux*` aliases (`TmuxLookupAnswer`, `TmuxSession`, `TmuxPane`, `TmuxLabel`, `TmuxCreateReply`, `TmuxCall`, `TmuxFailure`, `TmuxCallError`) and constants (`TmuxCall*`, `TmuxFail*`, `TmuxLabelNone` / `TmuxLabelValid`), identical to the originals, so an external implementer never imports `internal/tmux`. `api.New` builds the production client as `tmux.New(opts.TmuxCommand, tmuxTimeouts(cfg.Tmux))`, taking the timeouts and pipe-close wait from `EffectiveQueryTimeout`, `EffectiveActionTimeout`, `EffectiveCreateTimeout` and `EffectivePipeCloseWait`; an injected `Options.TmuxClient` is used as given and gets no timeouts. | stdlib; `internal/store`; `internal/config`; `internal/tmux`; `internal/probe`; `internal/spawn`. | Direct `database/sql`; raw SQL strings; MCP framing. |
| `internal/store` | Sole owner of the SQLite database file. Opens the DB, enforces file/dir permissions, manages schema (v5; see "Schema v5" below), exposes typed CRUD primitives (added in later Tasks). | stdlib (`database/sql`, `os`, `os/user`, `path/filepath`, `errors`, etc.); `modernc.org/sqlite` for the driver side-effect import. | `pkg/api`; `internal/config`; `cmd/*`; any package outside this one. The dependency arrow points *into* `store`, never out. |
| `internal/config` | Loads, validates, and serves the TOML config at `~/.agent-director/config.toml`. Read-only after load. Owns the `[tmux]` timing settings (`config.Tmux`, nine keys: `starting_session_seconds`, `stopping_window_seconds`, `pending_grace_seconds`, `query_timeout_ms`, `action_timeout_ms`, `create_timeout_ms`, `pipe_close_wait_ms`, `sweep_budget_seconds`, `kill_exit_wait_ms`), one named constant per default and per safe minimum, and the pending grace period's minimum rule (`PendingGraceMinimumSeconds`). Safe minimums: bound 60 s, stopping window 30 s, grace period 30 s or ⌈(create timeout + pipe-close wait) / 1000⌉ + 20 s when larger; the other six keys have none (a value too low fails closed). A missing key or 0 gives the default; a negative value, a positive value below a minimum and a non-integer are refused at load (`*config.ConfigError`, surfaced by the CLI as `ErrConfigMalformed`), never clamped. See [`[tmux]` timing settings](#tmux-timing-settings). | stdlib; `github.com/BurntSushi/toml`. | `database/sql`; `internal/store`; `pkg/api`; `cmd/*`. |
| `pkg/api/apitest` | Test seed helpers extracted from `pkg/api/*_test.go` for cross-package importing. Provides `Seed*` functions (`SeedListFixture`, `SeedDeleteFixture`, `SeedDecideFixture`, `SeedPermissionRow`, `SeedExpireFixture`, `SeedJsonl`, `SeedStore`, `OpenStoreWithRow`) that set up fixture DB rows and filesystem state for `test/envelope-diff` and future Epic 4/5 smoke tests. Also provides the config writer `WriteTmuxConfig` (settings built with `TmuxInt`, or `TmuxFloat` / `TmuxString` / `TmuxBool` for malformed values, keyed by `config.TmuxKey`): `pkg/api`, CLI and MCP tests write `[tmux]` settings only through it, so no test outside `internal/config` spells a `[tmux]` key (rules: Test Harness, "apitest `[tmux]` config writer"). Provides `SeedSpawn`'s trailing `SpawnOption`s for the v5 columns, timestamps and raw text (`WithTmuxSessionName`, `WithStartedAt` / `WithEndedAt`, `WithLaunchStartedAt`, `WithRawLaunchStartedAt`, `WithNoLaunchStartedAt`, `WithLifeNumber`, `WithNoPreTrust`, `WithRawNoPreTrust`, `WithLaunchIdentity`, `WithNoLaunchToken`, `WithRawLabels`, `WithRawClaudeArgs`, `WithRawExtraEnv`) and archived session history (`WithSessionHistory`), the default socket `TestSocket`, the default pane `TestPaneID` / `TestPanePID` that `SeedSpawn` gives a live row (both re-exported from `internal/testsupport/launchfix`; a terminal row gets no pane), the store-read helper `ReadSpawnColumns`, the every-life history-read helper `ReadSessionHistoryAllLives`, and the store-id helpers `ReadStoreID`, `SeedStoreID` and `OtherStoreID` (with `ErrNoStoreID`): new tests seed rows and read columns no verb shows only through these (rules: Test Harness, "apitest Seed* factory contract"). To place a seeded row's own labelled session in the Recorder, tests use `tmuxfix.Recorder.SeedRowSession` (in `internal/testsupport/tmuxfix`, not this package). Non-test package (regular `.go` files) so it can be imported by harnesses outside `pkg/api`. | stdlib; `internal/store`; `internal/spawn`; `internal/config` (the `[tmux]` key definitions); `github.com/BurntSushi/toml` (to encode the config file); `internal/testsupport/storefix`; `internal/testsupport/procstarttimefix` and `internal/testsupport/launchfix` (leaf fixture-value packages); `github.com/google/uuid`; `modernc.org/sqlite` (driver side-effect import). | `pkg/api` (cycle constraint); `cmd/*`; `internal/mcp`; `test/*`. |
| `pkg/api/errnames` | **Single source of truth for err_name strings.** Declares `Catalog []Entry` (each Entry pairs a sentinel `error` with its canonical name string), `Classify(err) (name, description)` with `ErrInternal` fallback, and `TrimNamePrefix` for envelope-text normalisation. The `Catalog` is consumed by `cmd/agent-director`'s envelope writer and `internal/mcp`'s `classifyDispatchError`. `catalog.json` is generated deterministically from `Catalog`; the doc-drift CI gate enforces coherence. | stdlib; `pkg/api`; `internal/config`; `internal/probe`; `internal/spawn`; `internal/store`; `internal/tmux` (sentinel types only). | `cmd/*`; `internal/mcp`. |
| `internal/mcp` | Stdio MCP server. `server.go` handles JSON-RPC framing (initialize, tools/list, tools/call). `dispatch.go::LiveDispatcher` holds a single `*pkg/api.Client` and routes each tool call to the corresponding `Client` method — no business logic of its own. `classifyDispatchError` delegates to `errnames.Classify`. | stdlib; `pkg/api`; `pkg/api/manifest`; `pkg/api/errnames`. | `internal/store`; `internal/config`; `internal/tmux`; `internal/spawn`; `cmd/*`. |
| `pkg/api/manifest` | Defines and exposes the canonical CLI/MCP verb manifest used to keep the CLI surface, MCP tool surface, and docs in lock-step. | stdlib only — leaf package. | `internal/store`, `internal/config`, `cmd/*`, raw `database/sql`, SQL strings. The manifest is the source of truth; consumers depend on *it*, never the other way around. |
| `internal/spawn` | Owns the parameter-resolution → validation → defaults → launch pipeline (SRD §7). Builds env maps, synthesizes `--settings` JSON, and asks `internal/tmux` to start the session. Inserts the `pending` row via `internal/store`. | stdlib; `internal/config`; `internal/store`; `internal/tmux`; `github.com/google/uuid` for UUID4 minting. | Raw `database/sql`; hook-handling code; MCP framing; ad-hoc subprocess management outside `internal/tmux`. |
| `internal/tmux` | Thin client over the tmux binary, built only by `New(binary, Timeouts)` (`""` = tmux on `PATH`). **Phase 1 call set (SR-2.1, Appendix F.1)**, every call taking the socket: `Lookup` (the one-invocation lookup: session listing with labels plus the three `@ad_owner` scope reads), `ListPanes` (`list-panes -a`), `KillPane` (by pane id), `KillSessionID` (by session id), `SendKeysPane` (by pane id: the text call `send-keys -t <pane id> -l -- <text>`, then an optional separate `send-keys -t <pane id> Enter`; the `--` makes a text starting with `-` literal, never read as a send-keys flag; a text ending in `;` is sent with that `;` escaped as `\;`, because tmux reads an argument-final `;` as a command separator even after `--` — the escape is `escapeFinalSemicolon`, used only by the text call), `CapturePaneID` (by pane id), `SetLabel` (label by id: the session label by session id and the pane label by pane id) and `NewSession` (the create with its chained `@ad_owner` and `@ad_pane` labels). **Label form (SR-3.4, SR-3.5):** `ad1 <token> <$N> <instance id> <store id>`, five fields. The store id is the writing store's `store_meta.store_id`, which callers pass from `(*store.Store).StoreID()`; it is the last field, so the instance id is everything between the third and the last space and may contain spaces. `NewSession` and `SetLabel` both take the token, the instance id and the store id; the chain doubles `#` only inside the instance id. **Pane label (SR-2.1, SR-3.5):** every created pane carries the per-pane user option `@ad_pane` = `<token> <pane id>`, so a launch whose create reply was lost can later find its own pane by token, whatever the base-index or window layout. The create sets it with a second chained step, `; set-option -p -F -t =<name>: @ad_pane '<token> #{pane_id}'`, after the `@ad_owner` step; each `;` is its own argv element, and a name for which `NeedsLabelByID` holds gets neither chained step. A failure of either chained step is the create's `FailLabel` (tmux stops the chain at the first failing step). `SetLabel(socket, sessionID, paneID, token, instanceID, storeID)` sets both labels in one invocation, `set-option -t <$N> @ad_owner '<label>' ; set-option -p -t <%N> @ad_pane '<token> <%N>'`, with the session and pane ids from the create reply; a failure may leave the session labelled and its pane not. Neither label value ends in `;`. Only the new session's one pane is labelled: a pane split from it later has no value. **Pane listing:** `ListPanes` reads `#{@ad_pane}` as the sixth and last field, the value being everything after the fifth tab, so a tab inside it cannot shift the other fields. `Pane.AdPane` is the token only when the value is exactly `<16 lowercase hex token> <pane id>` and that pane id equals the line's own `%N` (`classifyPaneLabel`); anything else gives `""`, so a window, session, global or server value borrowed through the format, which names another pane or none, never counts (the scope guard of SR-3.6). Caveat: on tmux 3.3a a server-scope `@ad_pane` (`set-option -s`) is listed on every pane in place of its own value, so while one exists only the pane that value names can report a token and every other pane reads `""`; no other pane is matched, but a pane reading `""` then does not show that its label is gone. The raw value never leaves the client, and a malformed listing's `CallError.FirstLine` is its first line cut before the pane label field (`paneListingFirstLine`). The lookup does not read `@ad_pane`. No verb calls the pane label yet: it exists for adoption of a lost create reply (SR-3.6), the leftover-pane check (SR-3.7) and the no-pane row check (SR-11.3). A value in any other form, a four-field one included, parses as no label (`LabelNone`), except that a four-field value whose instance id ends in a space and 16 lowercase hex reads as a shorter id plus that word as its store id; and `Label.StoreID` is set only on a valid label. Typed results and failures: `Call`, `Failure`, `CallError`, `LookupAnswer`, `Session`, `Label` / `LabelKind`, `CreateReply`, `Pane`, `Timeouts`. Mechanics: every call runs `-u -S <socket>` first; targets are ids only (never a name or pattern); each call class (query, action, create) has its own timeout, plus the pipe-close wait (`Timeouts.WaitDelay`); data is parsed only from standard output of an exit-0 call; replies are recognised only from the first line of standard error; the client's environment has every `AGENT_DIRECTOR_*` variable removed. Socket-taking calls fail only with `*CallError`. Labels reach callers only classified (the raw value never leaves the client) and recognised replies only as a `Failure`; the one exception is an unrecognised reply, whose first line (trimmed, at most 200 bytes) is carried in `CallError.FirstLine`. **Socket resolution (RN-5):** `ResolveSocket(create)` resolves the socket as tmux does (`TMUX`, then `TMUX_TMPDIR`, then `/tmp`, with tmux's per-user directory checks) and `EnsureSocketDir(socket)` creates only a missing per-user directory; refusals are `*SocketDirError` (with `SocketDirReason`), matching `ErrTmuxNotAvailable`. **Must use** `tmux.NeedsLabelByID(name)` to decide whether a session name (one containing `$` or `\`) must be labelled by id rather than by the chain; never re-implement that test. The client receives its timeouts and pipe-close wait from `pkg/api` at construction, never from `internal/config` (see [`[tmux]` timing settings](#tmux-timing-settings)); the package defines no defaults. The runner seam types (`Invocation`, `RunStatus`, `RunResult`, `Runner`) are exported for replay tests; tests install a runner only through the test-only `NewWithRunner` in `export_test.go`. The name-based methods (`NewSessionByName`, `HasSession`, `KillSession`, `SendKeys`, `CapturePane`) keep their contracts until their last verb moves to the socket-taking calls. `HasSession` matches by prefix: `resume` still calls it until it moves to the lookup, and no verb may newly adopt it. `StripANSI` post-processes captures. | stdlib (`bytes`, `context`, `errors`, `fmt`, `io/fs`, `os`, `os/exec`, `path/filepath`, `regexp`, `sort`, `strconv`, `strings`, `syscall`, `time`, `unicode`, `unicode/utf8`). | `internal/config` (see [`[tmux]` timing settings](#tmux-timing-settings)); template and store packages; shell processes (`/bin/sh`); anything other than direct `exec.Command`. |
| `internal/hook` | Reads payload JSON from stdin, classifies per SRD §5.2, writes the row UPSERT, exits 0 (state-tracking fail-open). | stdlib; `internal/store`. | `internal/tmux`; `internal/spawn`; `internal/config` (the cmd-side wrapper loads config; the package itself stays narrow). |

### `[tmux]` timing settings

Every tmux timing value (the starting-session bound, the stopping window,
`find-missing`'s pending grace period, the per-call timeouts, the pipe-close
wait, the sweep tmux budget and `kill`'s exit wait) is a key of the `[tmux]`
table, held in `config.Config.Tmux`. `internal/config` is the single source
of truth for them, following the `Relay.EffectiveTimeoutSeconds` pattern:

- **Read a value only through its `config.Tmux` accessor**
  (`EffectiveStartingSession`, `EffectiveStoppingWindow`,
  `EffectivePendingGrace`, `EffectiveQueryTimeout`, `EffectiveActionTimeout`,
  `EffectiveCreateTimeout`, `EffectivePipeCloseWait`, `EffectiveSweepBudget`,
  `EffectiveKillExitWait`, or the generic `Effective(k)` with a
  `config.TmuxKey`). Each returns a `time.Duration`: the configured value when
  positive, else the key's `Default*` constant, saturating at the largest
  duration.
- **No other package defines one of these defaults or minimums or applies
  the fallback.** Defaults are the `Default*` constants, minimums
  `MinStartingSessionSeconds`, `MinStoppingWindowSeconds` and the grace rule
  `PendingGraceMinimumSeconds` (built from `PendingGraceFloorSeconds` and
  `PendingGraceMarginSeconds`); `Tmux.Minimum(k)` returns a key's minimum.
  Key names, units and defaults come from `config.TmuxKey` (`TmuxKeys()`,
  `Name`, `Unit`, `DefaultValue`, `MinimumKind`); outside `internal/config`
  and its own tests, no code or test spells a `[tmux]` key name (SR-20.2).
- **Refuse, never clamp.** `config.Load` validates the table after
  decoding; every refused key, in table order, is described in one
  `*config.ConfigError`. No verb, server or hook runs with a minimum or
  default in place of a refused value.
- **`internal/tmux` never imports `internal/config`** and reads no
  configuration (its row's prohibited imports).

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

**Schema v5** lives in `internal/store/schema.go`. Four tables:

- `spawns` — one row per Claude Code instance under direction, with
  parent/child link (`parent_id`), lifecycle (`state`, `started_at`,
  `last_seen_at`, `ended_at`), tmux + relay metadata, and a JSON-encoded
  `labels` blob. Indexed on `state`, `last_seen_at`, and `parent_id`.
- `permission_requests` — one row per `(claude_instance_id, request_token)`
  pair, FK-cascaded on spawn delete; `request_token TEXT NOT NULL`, composite
  `UNIQUE(claude_instance_id, request_token)`, with `tool_name`, `tool_input`,
  `decision`, `decision_reason`, and `decided_at`. Indexed on
  `(claude_instance_id, decision)` and `(decision, decided_at)`.
- `session_history` (v4, b.v2c) — one row per `(claude_instance_id,
  claude_session_id)` pair the row has archived, FK-cascaded on spawn
  delete; `claude_session_id TEXT NOT NULL`, nullable `jsonl_path`,
  `recorded_at`, composite `UNIQUE(claude_instance_id, claude_session_id)`,
  indexed on `claude_instance_id`. When a session rotates (the agent's row
  is reported with a new session id), the prior `(session id, jsonl_path)`
  is archived here before the `spawns` row is overwritten, so the earlier
  session's transcript is never orphaned.
  - **History belongs to a life.** Each entry carries the life it belongs to
    (`life_number`, see "v5 columns" below): the life the row was in when that
    session ran. The rotation archive takes that life from the same read as
    the outgoing session id and jsonl path.
  - **One entry per instance and session id.** Re-archiving a session id
    within the same life keeps an already-recorded path when the new path is
    NULL and refreshes `recorded_at`. Re-archiving it in a later life moves
    the entry to that life, with that life's path exactly (NULL included),
    and refreshes `recorded_at`; no path from the earlier life survives.
  - **Retention and reads.** The store keeps every entry until the row is
    deleted or expired. Its history read, `ListSessionHistory(id, life)`,
    returns one life's entries, newest first, and keeps an entry equal to the
    row's current session id. `resume` and `get` read the life of the row
    they read and use only its *visible history*: that life's entries minus
    any entry whose session id equals the row's current, non-empty session
    id (`visibleHistory` in `pkg/api/visible_history.go`). The visible
    history is the queryable link from a row back to the earlier sessions of
    its current life, surfaced through `get`'s `prior_sessions` and consulted
    by `resume` as a fallback candidate source. **Must use:** every
    `pkg/api` reader of session history applies the current-session rule
    through `visibleHistory` (`pkg/api/visible_history.go`), never its own
    filter.
  - **Must use:** every product-code archive into `session_history` goes
    through
    `upsertSessionHistoryEntry(q querier, instanceID, sessionID, jsonlPath,
    life)` in `internal/store/session_history.go`. It runs one statement on
    the pool or on a transaction the caller already holds, reads nothing
    (the caller passes values it has already read), and applies the rules
    above: within the same life a known path is kept, and in another life
    the entry moves with that life's path exactly. It emits no trail event
    and advances no `row_version`; callers own their events and failure
    policy. The rotation archive calls it today. Every new code path that
    archives a session must call it too, never a second upsert.
- `store_meta` (v5, b.fmk; SR-5.1) — `key TEXT PRIMARY KEY, value TEXT NOT
  NULL`, with one row in Phase 1: `store_id`, the store's identity. Every
  `@ad_owner` label carries it as its last field (see `internal/tmux` in the
  package inventory).
  - **Value.** 64 random bits from `crypto/rand`, written as 16 lowercase hex
    characters. It is random, not secret, but no error message ever contains
    it (SR-15).
  - **Created once, never changed.** `createSchema` (a new store) and
    `migrateV4toV5` (the hop) insert it inside their own transaction, before
    the stamp, only when no `store_id` row exists (`insertStoreIDOnceSQL` in
    `internal/store/storeid.go`, the only statement that writes
    `store_meta`). No verb changes it, and a restore keeps it because it is in
    the file.
  - **Read once at open.** `openDB` reads it after `ensureSchema` succeeds
    (`readStoreID`), and `(*Store).StoreID()` returns it. A current-version
    store with no `store_meta` table, no `store_id` row, or a value that is
    not 16 lowercase hex refuses to open with an error wrapping
    `ErrSchemaMismatch`; the DB is closed and nothing is written. Any other
    read error (busy, I/O) is returned as it is. So every open `*Store` holds
    a well-formed id; treating a malformed value like a missing row keeps a
    made-up id out of labels.
  - **Must use:** production code reads the id through `(*Store).StoreID()`,
    never by querying `store_meta`, and writes it only through
    `insertStoreIDOnce`. Tests read and pin it through the `apitest` store-id
    helpers (`ReadStoreID`, `SeedStoreID`).

**v5 columns (b.fmk).** Schema v5 adds twelve `spawns` columns, after the
v4 columns, and one `session_history` column. They are the storage later work
builds on. The store gives each column its default on insert and on
migration. Beyond that, the v5 writes are the ones under "Versioned writes"
below (every `spawns` update advances `row_version`, and some clear
`launch_started_at`) and the rotation archive, which writes
`session_history.life_number` through `upsertSessionHistoryEntry`. No verb
reports any of the columns yet.

- **Row version** — `spawns.row_version INTEGER NOT NULL DEFAULT 0`. A
  per-row change counter, so a conditional write can tell that the row changed
  since it was read.
- **Launch start** — `spawns.launch_started_at INTEGER` (nullable). The time,
  in epoch milliseconds, that the row's current launch began; NULL when no
  launch is in progress.
- **Life numbers** — `spawns.life_number` and `session_history.life_number`,
  both `INTEGER NOT NULL DEFAULT 0`. The row's current life and the life each
  history entry belongs to, so the history of a reused id can be split by
  life.
- **Pre-trust choice** — `spawns.no_pre_trust INTEGER NOT NULL DEFAULT 0`. The
  pre-trust choice of the spawn or reuse that began the row's current life:
  0 means pre-trust allowed, anything else means the caller opted out.
- **Launch identity** — eight nullable `spawns` columns: `launch_token` (TEXT,
  the launch's token), `tmux_socket` (TEXT, the tmux socket the launch uses),
  the tmux server's identity `tmux_server_pid` (INTEGER),
  `tmux_server_started` (INTEGER) and `tmux_server_starttime` (TEXT), and the
  agent's pane `pane_id` (TEXT), `pane_pid` (INTEGER) and `pane_starttime`
  (TEXT). Together they say which tmux server, session label and pane belong
  to the row's current launch.

None of the thirteen has a CHECK constraint or an index. `schemaDDL` and
`migrateV4toV5` declare them with the same text, in the same order, so a
fresh store and a migrated store have identical column lists on both tables.

**v5 row types and narrow reads (b.fmk).** `store.Spawn` (and its alias
`api.Spawn`) carries the v5 columns on every read that returns a row
(`GetSpawn` and `ListSpawns` share one column list, `spawnColumns`, and one
scanner, `scanSpawn`, so both fill them identically): `RowVersion`,
`LaunchStartedAtMillis` (0 = absent), `LifeNumber`, `NoPreTrust` (the
recorded pre-trust choice), `EndedAtText` (`ended_at` exactly as stored, ""
for NULL), `Snapshot` and `Identity`. The fields are read-only. No write takes
them from a `Spawn`, and no verb reports them.

- **`store.RowSnapshot`** (alias `api.RowSnapshot`) is the SR-5.3
  change-detection key: `row_version`, `started_at`, `claude_session_id`,
  `pid`, `proc_starttime` and `tmux_session_name`, held as the stored values
  (the timestamps as stored text, selected with `CAST(col AS TEXT)`). They are
  never parsed and re-formatted. Two snapshots compare with `==`.
- **`store.LaunchIdentity`** (alias `api.LaunchIdentity`) holds the eight
  launch-identity columns: token, socket, tmux server pid/start/starttime,
  pane id/pid/starttime. A zero field means NULL. It holds handles and
  liveness evidence only. The session label, not this value, proves ownership
  of a launch.
- **SR-5.5 narrow reads.** A `launch_started_at` that is not an integer reads
  as absent (0). A `launch_token` that is not exactly 16 lowercase hex
  characters reads as absent (""). Any `no_pre_trust` other than the integer
  0 reads as the opt-out. None of these fails a read. Every other column keeps
  its existing failure behaviour; for example, malformed `labels` or a text
  `pid` still fail the read with the same error text. A wrong storage class in
  any other v5 column (for example, a text `tmux_server_pid`) also fails the
  read, as a text `pid` does. These are new columns, so there is no earlier
  error text to keep.
- **Must use:** every read of `launch_started_at`, `launch_token` or
  `no_pre_trust` goes through the shared decoders in
  `internal/store/rowsnapshot.go` (`decodeLaunchStartedAt`,
  `decodeLaunchToken`, `decodeNoPreTrust`; each takes the column scanned into
  an `any` and never fails). A new read returning a `Spawn` selects
  `spawnColumns` and scans with `scanSpawn`. Never re-derive the rules in a
  new read.

**Versioned writes (b.fmk, SR-5.2).** The row version exists so that a later
compare-and-set write can guard on it: the write checks that the version is
still the one it read, so it knows the row has not changed since. As built:

- Every statement that updates a `spawns` row advances `row_version` by
  exactly one in that same statement, with no transaction added. The shared
  SET fragment is `rowVersionAdvance` in `internal/store/spawns.go`. This
  covers every branch of `ApplyHookTransition` / `ApplyHookTransitionResult`
  (state transitions, the `ended` transition, soft refreshes), both variants
  of `RecordSessionStartIdentity`, `SetParentID`, `MarkSpawnMissing`,
  `SetLivenessUnverified`, `ClearLivenessUnverified` (on every matched row,
  whether or not a note was set) and `HealJsonlPath`.
- `InsertPending` starts a row at 0 (the column default). A path that writes
  nothing advances nothing. Examples are the `working`-transition hold path
  (open permission requests), a write whose `WHERE` matches no row, and
  `SetLivenessUnverified` / `HealJsonlPath` / `MarkSpawnMissing` when their
  guard misses. `DeleteSpawn` and `DeleteTerminalOlderThan` remove the row.
- Archiving the prior session into `session_history` on a rotation does not
  advance the version by itself. The `RecordSessionStartIdentity` update it
  belongs to advances it once.
- The foreign-key action that clears a child's `parent_id` when its parent
  is deleted (`ON DELETE SET NULL`) does not advance the child's version.
  `parent_id` is not in `RowSnapshot`.
- Launch-start rule: every write that sets `state` to a value other than
  `pending` also sets `launch_started_at` to NULL in the same statement
  (shared fragment `launchStartClear`). These writes are the `ended`
  transition, every other hook transition whose target is not `pending`, and
  `MarkSpawnMissing`. Every other write leaves `launch_started_at` unchanged.
  Nothing sets a launch start yet, so this changes no behaviour today.
- No store update touches `life_number`, `no_pre_trust`, `launch_token`,
  `tmux_socket` or the six tmux server and pane identity columns.
- **Must use:** any new or changed store statement that updates a `spawns`
  row must advance `row_version` in that same statement (concatenate
  `rowVersionAdvance`), and must follow the launch-start rule (concatenate
  `launchStartClear` when it sets a non-`pending` state). It must also get a
  case in the SR-5.2 versioning test, `internal/store/row_version_test.go`,
  which every later exported `spawns` write extends. A write that skips the
  advance silently defeats every version guard.

**Schema versioning convention.** SQLite's `PRAGMA user_version` is the
source of truth for which schema this binary expects. On `Open`:

- `user_version == 0` → fresh DB: create the v5 tables and indexes and insert
  the store id inside a single transaction, then stamp `PRAGMA user_version =
  5`.
- `0 < user_version < 5` (older-than-binary, i.e. v1, v2, v3, or v4) → **gated**: the
  store does **not** auto-migrate on `Open`. The open is refused with
  `store.ErrSchemaMigrationRequired` (an exported `errors.New` value; callers
  use `errors.Is`) and zero DDL runs, *unless* an administrator has placed a
  valid authorization sentinel next to the DB file. The sentinel (`migrate-authorized`,
  sibling to the resolved DB path) is a strict JSON object naming exactly one
  transition (e.g. `{"from": 4, "to": 5}`) and authorizes the migration only
  when its `from` exact-matches the DB's actual `user_version` and its `to`
  exact-matches this binary's `schemaVersion`. When authorized, the upgrade
  runs as a chain of `migrationSteps` (the ordered step registry in
  `schema.go`), each step individually transactional; today the chain holds four
  steps, `{from: 1, apply: migrateV1toV2}` (DROP+CREATE `permission_requests`,
  V1 rows discarded), `{from: 2, apply: migrateV2toV3}` (five ADD COLUMN on
  `spawns`), `{from: 3, apply: migrateV3toV4}` (CREATE `session_history`), and
  `{from: 4, apply: migrateV4toV5}` (thirteen ADD COLUMN across `spawns` and
  `session_history`, then CREATE `store_meta` and its store id) — see "Schema v1 → v2 Migration", "Schema v2 → v3
  Migration", "Schema v3 → v4 Migration", and "Schema v4 → v5 Migration" below.
  A v1 DB opened against this binary chains v1→v2→v3→v4→v5 in one pass. The
  sentinel is consumed after the chain commits.
- `user_version == 5` → nothing to do; the schema already matches.
- `user_version > 5` (newer-than-binary) → return the sentinel
  `store.ErrSchemaMismatch` (an exported `errors.New` value, so callers use
  `errors.Is`). No DDL runs in this case.
- After any of the successful arms, `openDB` reads the store id; a store
  without a valid one also fails with `ErrSchemaMismatch` (see `store_meta`
  above).

**Schema v1 → v2 Migration.** The first real migration:

1. **Migration shape**: `DROP TABLE permission_requests` followed immediately
   by `CREATE TABLE permission_requests` at the v2 DDL, plus two new indexes,
   all inside a single transaction.
2. **No v1 row preservation**: v1 `permission_requests` rows are deliberately
   discarded. Any open row from v1 was abandoned during the downtime that
   triggered the upgrade.
3. **Single-version DB invariant** (SR-2.6): a binary always expects exactly
   one schema version; mixed-version operation against a single store file is
   not supported. `ErrSchemaMismatch` is the signal to stop rather than
   silently corrupt rows.
4. **`user_version` stamp**: `PRAGMA user_version = 2` is the final
   in-transaction step before `COMMIT`. A crash mid-migration leaves
   `user_version = 1` and — because the sentinel is only consumed *after* the
   chain commits — the `migrate-authorized` sentinel still in place, so the
   next authorized `Open` retries the migration cleanly.

**Schema v2 → v3 Migration.** This migration adds process-liveness
identity to `spawns` (`migrateV2toV3`):

1. **Migration shape**: five `ALTER TABLE spawns ADD COLUMN` statements inside a
   single transaction — `pid INTEGER`, `proc_starttime TEXT`,
   `liveness_unverified_since TEXT`, `liveness_note TEXT` (all nullable), and
   `extra_env TEXT NOT NULL DEFAULT '{}'`.
2. **No backfill**: `ADD COLUMN` populates existing rows from the column
   defaults — NULL for the four nullable columns, `'{}'` for `extra_env` — so
   there is no phase-3 data transform.
3. **`user_version` stamp**: `PRAGMA user_version = 3` is the final
   in-transaction step before `COMMIT`; a rollback on any error leaves
   `user_version = 2` intact.

**Schema v3 → v4 Migration.** This migration adds the `session_history`
table so session rotations no longer orphan transcript history (`migrateV3toV4`,
b.v2c):

1. **Migration shape**: a single `CREATE TABLE IF NOT EXISTS session_history`
   plus `CREATE INDEX IF NOT EXISTS idx_session_history_instance`, inside a
   single transaction. A new-table hop — no existing row is touched.
2. **No backfill**: `session_history` starts empty. Pre-v4 rows have no recorded
   session history; the current session pair is archived lazily on the next
   rotation, so there is no phase-3 data transform.
3. **`user_version` stamp**: `PRAGMA user_version = 4` is the final
   in-transaction step before `COMMIT`; a rollback on any error leaves
   `user_version = 3` intact.

**Schema v4 → v5 Migration.** The current migration adds the v5 columns and
the `store_meta` table (`migrateV4toV5`, b.fmk):

1. **Migration shape**: thirteen `ALTER TABLE … ADD COLUMN` statements across
   two tables inside a single transaction — the twelve `spawns` columns first,
   then `session_history.life_number`, in the order listed under "v5 columns"
   above. SQLite has no `ADD COLUMN IF NOT EXISTS`, so each one is guarded by a
   `pragma_table_info` probe of its own table and skipped when the column is
   already present; re-entering the hop is safe. Then, in the same
   transaction, a new-table step: `CREATE TABLE IF NOT EXISTS store_meta`
   (the same text as `schemaDDL`) and the guarded insert of one new random
   `store_id`, which writes only when no `store_id` row exists. A second run,
   or a run on a store whose `store_meta` already holds an id, keeps that id.
2. **No backfill**: there is no phase 3. `ADD COLUMN` gives every existing row
   and history entry the column's ordinary default — row version 0, life 0,
   `no_pre_trust` 0, and NULL for the launch start, launch token, socket and
   server/pane identity. No row is a special case (a `pending` row included),
   and no existing value is rewritten. The store id is the one row of a new
   table, not a backfill.
3. **`user_version` stamp**: `PRAGMA user_version = 5` is the final
   in-transaction step before `COMMIT`; a rollback on any error (a column,
   the table or the insert) leaves `user_version = 4`, neither table with any
   of the new columns, and no `store_id`. `user_version > 5` surfaces
   `ErrSchemaMismatch`.
4. **Install-only**: like every hop, it runs only when the install flow's
   `migrate-authorized` sentinel authorizes `{"from": 4, "to": 5}`; a v4 store
   opened without it is refused with `ErrSchemaMigrationRequired`. `install.sh`
   learns the target version from that refusal, so it needed no change.
5. **Downgrade**: to return a migrated store to v4, use the v5 → v4 emergency
   downgrade recipe in docs/migration-guide.md §5 (drop the thirteen columns
   and the `store_meta` table, then `PRAGMA user_version = 4`), or restore a
   copy of `state.db` taken before the install. Either way the store id is
   gone: a later re-migration creates a new one, so every label written
   before the rollback reads as another store's. Every agent is stopped
   before the rollback and started again after a re-migration.

**Concurrency.** `Open` calls `db.SetMaxOpenConns(1)`. `journal_mode=WAL`
and `foreign_keys=ON` are applied via DSN PRAGMAs and verified after open;
a silent downgrade fails `Open` rather than yielding a half-broken Store.

**File-system contract.** The parent directory (`~/.agent-director/` by
default) is created with mode 0700, and the database file is chmodded to
0600 on every `Open`. Repeated opens never widen permissions. A leading
`~/` in the path is expanded via `os.UserHomeDir()`, which honours the `$HOME` environment variable.

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
                |    schema v5 / SRD §4.2)|
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

### Build paths

agent-director ships a single Go build path:

| Path | Command | CGO | Output |
| --- | --- | --- | --- |
| Static CLI (host) | `make build` | `CGO_ENABLED=0` | `bin/agent-director` |
| Release cross-compile (3 platforms) | `make release-binaries` | `CGO_ENABLED=0` | `dist/agent-director-{linux-amd64,linux-arm64,darwin-arm64}` |

The CLI is statically linked everywhere (pure-Go SQLite via
`modernc.org/sqlite`). Linux binaries pass an `ldd → "not a dynamic
executable"` check in `make release-binaries-smoke`.

### TS Client → CLI subprocess

There is no FFI / shared-library path. The TS Client (`pkg/ts-bun-client`)
spawns the bundled CLI binary as a subprocess per verb call; see
[TS/Bun client library — Subprocess Client](#tsbun-client-library-pkgts-bun-client)
below.

## Public API

`pkg/api` is the stable public Go module surface for agent-director. Go library
callers, the stdio MCP server (`internal/mcp`), and the CLI binary
(`cmd/agent-director`) all dispatch through the same `*pkg/api.Client`; no
business logic is duplicated across surfaces.

**Canonical module path:** `github.com/gabemahoney/agent-director/pkg/api`

**API reference:** https://pkg.go.dev/github.com/gabemahoney/agent-director/pkg/api

**Consumer quick start:** See `pkg/api/README.md` for installation,
construction, and a first-call example.

**Enforcement:** `tools/check-doccomments` is an AST walker that requires every
exported identifier in `pkg/api` to carry a doc comment. It runs in the
doc-drift CI gate (`.github/workflows/doc-drift.yml`) on every PR and push to
main. Undocumented symbols fail the build rather than accumulating silently.

## Caller surfaces and shared API

agent-director exposes two caller surfaces. The CLI subprocess
(`cmd/agent-director`), reached by shelling out to the binary, serves
both shell operators and the TS Client (`pkg/ts-bun-client`), which
spawns one CLI subprocess per verb call. The in-process Go consumer
imports `pkg/api` directly and calls `*pkg/api.Client` methods, no
process boundary.

No business logic is duplicated between the CLI and the TS Client;
both terminate at `pkg/api.Client`.

The CLI marshals errors via `pkg/api/errnames.Catalog`:
`cmd/agent-director`'s envelope writer calls `errnames.Classify` to
map every Go error sentinel to its canonical `err_name` string. The
TS Client lifts the same `err_name` strings out of the CLI's stderr
envelope and rethrows them as the matching `Err*` subclass.

```
  CLI / TS Client                in-process Go consumer
  (cmd/agent-director,           (import pkg/api)
   pkg/ts-bun-client via
   subprocess spawn)
         │                              │
         │   stdin/stdout JSON          │   client.X(params)
         │   envelopes                  │
         └──────────────┬───────────────┘
                        ▼
           +--------------------------+
           |     pkg/api.Client       |
           | (verb-handler home;      |
           |  owns store/tmux/cfg)    |
           +-----------+--------------+
            │      │      │      │    │
            ▼      ▼      ▼      ▼    ▼
       internal/ internal/ internal/ internal/ internal/
       store     tmux      spawn     config    probe
```

Every arrow from a caller surface terminates at `pkg/api.Client`; no surface short-circuits to a lower layer.

## TS/Bun client library (`pkg/ts-bun-client`)

`pkg/ts-bun-client/` is the TypeScript client library for agent-director.
It ships as a Bun-native ESM package (`type: "module"`, `target: "bun"`)
and spawns the bundled CLI binary as a subprocess per verb call.

### Subprocess Client (post-b.eiv architecture)

The public `Client` is `src/internal/subprocessClient.ts` (re-exported
from `src/client.ts`). For every verb call the Client spawns the
bundled `agent-director` CLI binary with a JSON params envelope on
argv, reads the stdout JSON envelope back, and returns it (or throws
the matching `Err*` subclass for stderr error envelopes).

```
  agent-director (TS/Bun)
       │
       │  src/client.ts            (public Client = SubprocessClient)
       │        │
       │  src/internal/
       │     subprocessClient.ts   (per-call spawn + parse)
       │     spawner.ts            (Bun.spawn wrapper, stderr/stdout pumps)
       │     platformResolve.ts    (binary path resolver; called per-spawn in production)
       │     argv.ts               (verb name + JSON params → argv)
       │     errorMap.ts           (stderr envelope → typed Err* class)
       │        │
       ▼        ▼
  agent-director CLI subprocess  (bin/agent-director)
       │
       ▼
  pkg/api.Client
```

There is no FFI, no shared library, no worker thread. The replaced
`bun:ffi` path lived briefly in `pkg/cabi` + `src/ffi.ts` + a dedicated
worker; the b.eiv refactor (b.19d) replaced it with the per-call
subprocess model.

### System-install discovery pipeline (b.w3q)

After the b.ue3 cutover, the TS library no longer ships a bundled CLI
binary. `Client.create()` and `resolveSystemBinary()` discover the
host's system-installed agent-director CLI at construction time via
the SR-1 pipeline:

1. **Standard install path:** `$HOME/.agent-director/bin/agent-director`,
   after `~` expansion. Skipped silently when HOME is unset, empty, or
   non-absolute.
2. **PATH lookup:** first match of `agent-director` against the colon-
   separated PATH.

The candidate is validated (regular file, exec bit honoring owner/group/
other precedence) and probed with `<bin> version --json` under a bounded
invocation policy (5000 ms timeout, SIGTERM → 2000 ms grace → SIGKILL,
allowlist-only env scrub, stdin closed, cwd=/, stdout/stderr capped at
64 KiB each). The probed version is compared against `MIN_BINARY_VERSION`
(sourced from `dist/version-floor.json` at build time). Each failure
mode surfaces as one of:

- `ErrSystemInstallNotFound` (no candidate found anywhere)
- `ErrSystemInstallTooOld` (candidate is below floor)
- `ErrSystemInstallUnreachable` with `reason: UnreachableReason` (one of
  eight enumerated string-literal values; see SR-3.3)
- `ErrCallerCwdUnreachable` (b.cot — `process.cwd()` is gone or not a
  directory after the binary probe and floor comparison, before Client
  allocation)

All four error classes extend `AgentDirectorError` directly. No
shared parent class is introduced. The discovery is one-shot per
`Client.create()` / `resolveSystemBinary()` call; subsequent verb calls
on a successfully-constructed Client reuse the resolved absolute path.

The library returns the binary's reported version verbatim — no leading
`v` stripping, no normalization. Discovery preserves the byte-exact
sentinel `0.0.0-dev` end-to-end.

#### Spawn-side hook stability (b.ue3 / SR-1.8)

When the CLI's `spawn` verb writes hook commands into the spawned Claude
session's `settings.json`, every command's first argv token is an
absolute, symlink-resolved path computed via `os.Executable()` followed
by `filepath.EvalSymlinks()`. The CLI never writes the bare token
`agent-director`, never writes a PATH-relative path, and never writes
`$0` / `${0}` / `$(command -v agent-director)`.

Stability assumption: `install.sh` writes the AD binary to
`~/.agent-director/bin/agent-director` and re-runs of `install.sh`
overwrite the file at the same absolute path. The directory entry's
identity is stable across re-installs even though the file's inode may
change. Hook commands captured into a spawned Claude session therefore
continue to invoke the same path after the operator runs `install.sh`
again to upgrade.

The library-side absolute-path invariant (SR-1.5) and the spawn-side
hook-writing invariant (SR-1.8) together form a single contract: any
hook command pointing at AD's CLI binary is an absolute path to a
binary that remains callable for the lifetime of the configuration
that referenced it.

### Per-platform optional-dependency packaging

**Distribution model.** `pkg/ts-bun-client/` follows the [esbuild distribution
model](https://esbuild.github.io/getting-started/#download-a-build) for native
binaries: the top-level package ships zero binaries; each supported
platform gets its own optional sub-package that carries exactly one CLI
binary at `bin/agent-director`. `npm install` (and `bun install`)
resolve only the sub-package that matches `os` + `cpu` on the installing
host, leaving the others absent.

**Sub-packages (v1 — two platforms):**

| npm package | Platform | Binary file |
| --- | --- | --- |
| `@agent-director/linux-x64` | Linux x86-64 | `bin/agent-director` |
| `@agent-director/darwin-arm64` | macOS Apple Silicon | `bin/agent-director` |

> **v1 scope note (2026-05-24).** `@agent-director/darwin-x64` (macOS
> Intel) was **dropped** from the v1 set — no Intel Mac users to serve
> and the GH-hosted `macos-13` 10x billing multiplier was not worth the
> spend. Linux ARM64 (`linux-arm64`) remains deferred to v2; a
> sub-package `@agent-director/linux-arm64` will be added then.

Each sub-package lives under `pkg/ts-bun-client/platforms/<tuple>/` and
contains only `package.json`, `README-binary-source.md`, and
(release-injected) the CLI binary under `bin/agent-director`. The
binary is gitignored; the `/release` skill stages it after `make
release-binaries` cross-compiles.

For local development, `bun run prepare-platforms` copies the matching
`dist/agent-director-<os>-<arch>` into `platforms/<tuple>/bin/agent-director`.
The TS test preload (`test/setup.ts`) does the same at test time so
`resolveCliPath()` succeeds against the in-repo binary.

**Resolver flow — `src/internal/platformResolve.ts`.**

`platformResolve.ts` (internal, not re-exported from `src/index.ts`)
implements a five-step resolution sequence. In production it is called
on every verb spawn — not cached — so the client recovers transparently
from a binary replacement between construction and spawn (b.i5y). It is
also called once eagerly at construction to surface install errors
immediately (see Construction step 2 below):

1. **Bun version check.** Compare `Bun.version` against `MIN_BUN_VERSION`
   (`"1.0.21"`). Fail fast with `ErrBunVersionTooOld` before attempting any
   module resolution.
2. **Tuple lookup.** Build `<process.platform>-<process.arch>` and look it up
   in a static map. An unsupported tuple throws `ErrUnsupportedPlatform`.
3. **Sub-package resolution.** Call
   `import.meta.resolve("<subpkg>/package.json")` (Bun-synchronous,
   returns a `file://` URL). On failure (package not installed), throw
   `ErrPlatformPackageMissing`. Construct
   `<pkgDir>/bin/agent-director`.
4. **Stat the binary.** `statSync` on the resolved path; if absent,
   throw `ErrPlatformPackageMissing` (the message differentiates the
   two missing cases).
5. **Execute-bit check.** Check `S_IXUSR | S_IXGRP | S_IXOTH` against
   the current uid/gid; if not executable throw `ErrCliNotExecutable`.

**Four platform error subclasses** (all TS-only):

| Class | Thrown when | Key field in message |
| --- | --- | --- |
| `ErrBunVersionTooOld` | `Bun.version` < `1.0.21` | actual version + minimum |
| `ErrUnsupportedPlatform` | tuple not in supported set | tuple string (e.g. `linux-arm64`) |
| `ErrPlatformPackageMissing` | sub-package not installed or binary absent | sub-package name |
| `ErrCliNotExecutable` | binary exists but lacks execute permission | binary path |

**version-bump script.** `scripts/version-bump.ts` is the canonical tool for
all version-stamp mutations at release time. Only `verify_phase` calls it,
in a two-pass sequence, against the stage directory (never the live working
tree). A bare invocation with no `--target` stamps all five version-bearing
sites in one pass:

```sh
bun run version-bump-publish --version X.Y.Z
```

The five sites and their four `--target` selectors:

| `--target` selector | File(s) | Field |
| --- | --- | --- |
| `platform-version` | `platforms/linux-x64/package.json`, `platforms/darwin-arm64/package.json` | `version` |
| `umbrella-version` | `package.json` | `version` |
| `opt-deps` | `package.json` | `optionalDependencies` (`file:` → `^X.Y.Z` pins) |
| `skill-frontmatter` | `skills/install-agent-director/SKILL.md` | frontmatter `version:` |

Omitting `--target` runs all four selectors in canonical order:
`platform-version` → `umbrella-version` → `opt-deps` → `skill-frontmatter`.
The flag is repeatable for targeted single-site runs.

**Idempotence.** Every target skips the write if the file already carries the
target version, logging `version-bump [<target>]: already at <version> —
skipped`. Running the script twice with the same version produces zero file
writes on the second pass.

**OTQ-2 frontmatter robustness.** The `skill-frontmatter` target is strictly
frontmatter-scoped: the file must open with `---` and the frontmatter block
extends to the next `---`. Exactly one `version:` line must appear inside that
block — zero or multiple `version:` lines → non-zero exit with a descriptive
error. Lines after the closing `---` (the document body) are never scanned or
modified, even if they contain the word `version:`.

**check-version-coherence script.** `scripts/check-version-coherence.ts`
is the version-coherence gate run by `verify_phase` and `publish_phase` to
assert that all version-stamp sites agree with the release version before any
irreversible step. The gate accepts `--scope verify` or `--scope publish` and
checks five sites:

| Site ID | File | Field |
| --- | --- | --- |
| `site-1` | `platforms/linux-x64/bin/agent-director`, `platforms/darwin-arm64/bin/agent-director` | `version --json` output (`"v${ver}"`) |
| `site-3a` | `package.json` | `version` |
| `site-3b` | `platforms/linux-x64/package.json`, `platforms/darwin-arm64/package.json` | `version` |
| `site-4` | `package.json` | `optionalDependencies` (`^X.Y.Z` pins) |
| `site-5` | `skills/install-agent-director/SKILL.md` | frontmatter `version:` |

**Site-4 verify-scope skip.** Under `--scope verify`, site-4 is skipped when
all `optionalDependencies` entries still carry `file:` paths — this is normal
because `verify_phase` stamps opt-deps only after `bun install`, running the
first gate before the opt-deps pass. After the opt-deps pass, a second
`--scope verify` gate runs and site-4 must pass (opt-deps are now `^X.Y.Z`).

**Publish-scope additions.** `--scope publish` additionally performs a
SHA-256 round-trip check (SR-1.3 / SR-1.5): it reads
`AGENT_DIRECTOR_RELEASE_SHASUMS` (the manifest written by `verify_phase`) and
re-hashes every listed tarball. A hash mismatch means bytes were mutated after
`verify_phase` packed them; the gate aborts before `npm publish` fires.
`--scope publish` also runs the `dist/index.js` negative-grep
(SR-2.3), making publish ⊇ verify per SR-2.2.

### Release blockers

The npm-name blocker (H3) was resolved on 2026-05-24: the umbrella package
publishes as `agent-director` (unscoped) and the three per-platform sub-packages
publish under the `@agent-director` scope. The H3 entry in
[docs/release-blockers.md](release-blockers.md) records the resolution and is
kept as the template for any future release blockers.

### Package layout

```
pkg/ts-bun-client/
├── package.json          name: agent-director, version 0.0.0
├── tsconfig.json         strict, ES2022 + ESNext.Disposable, declaration-only to dist/
├── .eslintrc.cjs         @typescript-eslint strict rules
├── build.ts              Bun.build (ESM, single entry) → tsc (declarations)
├── src/
│   ├── index.ts          public re-exports (client, errors, types)
│   ├── client.ts         thin re-export: Client = SubprocessClient
│   ├── errors.ts         typed error subclasses
│   ├── types.ts          param/result types
│   └── internal/
│       ├── subprocessClient.ts  per-call CLI spawn + envelope parse
│       ├── spawner.ts           Bun.spawn wrapper, stderr/stdout pumps
│       ├── platformResolve.ts   CLI binary path resolver (called per-spawn in production)
│       ├── argv.ts              verb name + JSON params → argv
│       ├── errorMap.ts          stderr envelope → typed Err* class
│       ├── verbs.ts             callable-verb list (mirrors manifest.CallableVerbs)
│       ├── tilde.ts             `~` → home expansion
│       └── tsOnlyErrors.ts      TS-only error allow-list (catalog-drift exemption)
├── platforms/            per-platform npm sub-packages
│   ├── linux-x64/        @agent-director/linux-x64 → bin/agent-director
│   └── darwin-arm64/     @agent-director/darwin-arm64 → bin/agent-director
├── test/                 bun:test suite
└── dist/                 build output (gitignored)
```

### Client lifecycle

A `Client` (aliased from `SubprocessClient`) owns no Go-side resources;
each verb call is a one-shot subprocess.

**Construction.** `new Client(opts)` is synchronous. All `ClientOptions` fields are optional; omitted fields fall through to the CLI's own default-resolution (the CLI is the single source of truth — the TS Client provides no fallback values, b.32k). The constructor:

1. Applies tilde expansion (TS-side, via `src/internal/tilde.ts`) to `storePath`, `home`, and `tmuxCommand` so the CLI subprocess always receives absolute paths.
2. Calls `resolveCliPath()` eagerly to surface platform/install errors at construction time (ErrUnsupportedPlatform, ErrPlatformPackageMissing, ErrCliNotExecutable, ErrBunVersionTooOld), but does **not** cache the result. Each verb call re-resolves the binary path fresh so that a binary replacement between construction and spawn (e.g. a background `bun install` upgrading the global package) does not cause ENOENT failures on long-lived clients.
3. Stores the caller-supplied options for forwarding on each verb call. No subprocess is spawned at construction time; `client.version({})` is the canonical "is the binary functional" smoke.
4. Initializes `#npmPkgVersion` to `undefined`. The npm package version is loaded lazily on the first `version()` call and cached for the lifetime of the instance (see `loadNpmPackageVersion()` below).

**`close()`.**

- Sets `_open = false` so subsequent verb calls throw `ErrClientClosed`.
- Has no subprocess to terminate (every verb call is its own short-lived subprocess) — so it cannot fail mid-call.
- **Idempotent**: a second `close()` call is a no-op.

**`[Symbol.dispose]()`** delegates to `close()`, enabling `using` blocks (Explicit Resource Management):

```ts
{
  using client = new Client({});
  // use client …
} // client.close() called automatically here
```

**`_assertOpen()`** is called at the top of every verb method. It throws `ErrClientClosed` (a TS-only error subclass, not in the shared Go catalog) if the client has already been closed.

**Tilde expansion** is handled entirely on the TS side, in
`src/internal/tilde.ts`, before any path value is forwarded to the CLI
subprocess. The subprocess never receives a leading `~`.

**`loadNpmPackageVersion()`** — runtime npm package version resolver (b.6o1). `version()` calls this on its first invocation and caches the result in `#npmPkgVersion` — one disk read per `Client` instance. Not called at construction time. Internal to `subprocessClient.ts`; not re-exported. Code that needs the npm package version must go through `client.version()`.

Resolution order:
1. **Production path.** `import.meta.resolve("agent-director/package.json")` resolves the installed package's `package.json` via Bun's synchronous module resolver. Reads the file and returns `version`.
2. **Dev-tree fallback.** On failure (e.g. `bun test` against source without an npm-installed copy), falls back to `new URL("../../package.json", import.meta.url)` to locate the package root relative to `subprocessClient.ts`. Used in development and CI; not expected in a production install.

### Subprocess call recipe

Every verb call from `Client` follows this four-step recipe inside
`src/internal/subprocessClient.ts`:

1. **Build argv.** `src/internal/argv.ts` maps the verb name + params
   object to the canonical CLI argv (e.g. `["send-keys",
   "--claude-instance-id", "<id>", "--text", "..."]`). Per-Client
   global options (`storePath`, `home`, `tmuxCommand`) are prepended
   BEFORE the verb token as `--store-path`, `--home`,
   `--tmux-command` so the CLI's global-flag pre-scan
   (`cmd/agent-director/global_flags.go`) strips them prior to
   per-verb dispatch. Each is emitted only when the corresponding
   `ClientOptions` field was set by the caller (b.32k). JSON-only
   fields go through `--params-json` for verbs that accept it.
2. **Spawn the CLI.** `resolveCliPath()` is called fresh to obtain the
   binary path (production) or the `_cliPath` DI override is used verbatim
   (tests). `src/internal/spawner.ts` then calls `Bun.spawn` with that
   path, pipes stdin (closed), captures stdout and stderr, and respects
   `callTimeoutMs` (default 30 s).
3. **Parse the envelope.** On exit code 0, parse stdout as a JSON
   object → return it. On non-zero exit, parse stderr as a JSON
   error envelope (`{ "err_name": "...", "err_description": "..." }`)
   via `src/internal/errorMap.ts` and throw the matching typed
   `AgentDirectorError` subclass.
4. **Timeout / signal handling.** A per-call timer drives a SIGTERM →
   SIGKILL escalation when the budget is exceeded; the resulting
   rejection is `ErrCallTimeout`. Externally-killed subprocesses
   reject with `ErrConsumerSignal`.

```
Client.sendKeys(params)
  │
  ▼  src/internal/subprocessClient.ts → callVerb("send-keys", params)
  │
  ▼  argv.ts → [verb, ...flags]
  │
  ▼  resolveCliPath() → cliPath (fresh per-call)
  │
  ▼  spawner.ts → Bun.spawn(cliPath, argv, { stdin: "ignore", stdout: "pipe", stderr: "pipe" })
  │
  ▼  read stdout to EOF; await exit
  │
  ▼  exit 0  → JSON.parse(stdout) → resolve
     exit !=0 → JSON.parse(stderr) → errorMap.ts → throw Err* subclass
```

Spawn-per-call is the same model the shell CLI uses. There is no
shared Go-runtime state to preserve across calls.

### Error mapping

Every agent-director error envelope carries two string fields: `err_name` (the canonical error name, e.g. `"ErrSpawnNotFound"`) and `err_description` (a human-readable detail string). The TS client translates these into a typed class hierarchy so callers can catch specific errors with `instanceof`.

**Catalog source.** `pkg/api/errnames/catalog.json` is the single source of truth for every named error the Go binary can emit. It contains 40 entries at time of writing. Each entry has a `name` field (the `err_name` string) and a `package` field naming the origin Go package.

**Base class.** `src/errors.ts::AgentDirectorError extends Error`. Constructor: `(verb: string, err_name: string, err_description: string)`. Sets `this.name = this.constructor.name` so subclass names propagate correctly through the prototype chain. Readonly fields: `verb`, `errName`, `errDescription`. Message format: `"${err_name}: ${err_description}"`.

**Subclasses.** One `Err<Name> extends AgentDirectorError {}` per catalog entry. Bodies are empty — class identity is the sole value-add. Example:

```ts
export class ErrSpawnNotFound extends AgentDirectorError {}
```

**TS-only errors.** Ten subclasses have no counterpart in the Go catalog:
`ErrClientClosed`, `ErrBunVersionTooOld`, `ErrConsumerSignal`,
`ErrCallTimeout`, `ErrUnknownErrorName`, `ErrSystemInstallNotFound`,
`ErrSystemInstallTooOld`, `ErrSystemInstallUnreachable`,
`ErrCallerCwdUnreachable` (construction-time cwd validity guard), and
`ErrSystemInstallDisappeared` (binary disappeared since construction). Their
names are centralised in a single `as const` array exported from
`pkg/ts-bun-client/src/internal/tsOnlyErrors.ts::TS_ONLY_ERROR_NAMES`.
The catalog-drift test imports this constant and removes those names from both
sides before comparing, so CI never flags them as unexpected classes. Each
subclass in `src/errors.ts` carries a comment cross-referencing this module.

**Factory.** `errorFromEnvelope(verb, err_name, err_description): AgentDirectorError` in `src/errors.ts`. Maintains an internal `ERROR_TABLE` literal that maps every `err_name` string to its constructor. Unknown `err_name` values produce a plain `AgentDirectorError` with a `console.warn` so callers are not silently swallowed.

**Wiring.** `src/internal/errorMap.ts` calls `errorFromEnvelope(verb, parsed.err_name, parsed.err_description)` from `subprocessClient.ts` when the CLI exits non-zero and stderr parses as an error envelope.

**Catalog drift enforcement gate.** `pkg/ts-bun-client/test/errors-catalog-drift.test.ts`
reads `pkg/api/errnames/catalog.json` at test time (the single source of truth,
produced by Epic 1's `go generate ./pkg/api/errnames/...` mechanism), imports
`src/errors.ts`, and asserts that every catalog entry has a corresponding
`AgentDirectorError` subclass and that every exported `Err*` subclass (after
removing the TS-only allow-list from `src/internal/tsOnlyErrors.ts`) appears in
the catalog. On mismatch the test reports a two-sided diff: names present in the
catalog but absent from TS, and names present in TS but absent from the catalog.
This keeps the TS error surface from silently drifting from the Go one.

**Runtime ENOENT detection.** At verb dispatch in the subprocess transport, an `ENOENT` from spawning is classified into three outcome classes: `ErrSystemInstallDisappeared` if the binary is gone, `ErrCallerCwdUnreachable` if the caller's working directory is unreachable, or fall-through to the existing `ErrSubprocessCrash` path if neither condition applies.

## State Machine

A Spawn's lifecycle is tracked in the `state` column of `spawns`. Every
state value comes from the SRD §5.1 enum; transitions are driven either
by hook events (SRD §5.2) or by direct verb action (`pause`, `resume`,
`expire`, `delete`).

```
pending  ──spawn() launches tmux session
  │
  ▼   SessionStart hook fires
waiting  ◄─── Stop
  │           (soft refreshes only, no transition: SessionEnd reason=clear|compact, Notification)
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
| `SessionStart` | — | `waiting` (also writes `claude_session_id` from `transcript_path`; `jsonl_path` only when the file exists on disk, else NULL/provisional — b.v2c; a differing session id archives the prior pair to `session_history`, tagged with the row's current life) |
| `UserPromptSubmit` | — | `working` |
| `PreToolUse` | `tool_name = AskUserQuestion` | `ask_user` |
| `PreToolUse` | any other tool | `working` |
| `PostToolUse` | — | `working` |
| `Stop` | — | `waiting` |
| `Notification` | — | soft refresh — no state change; bumps `last_seen_at` (display-only signal; `Stop` owns the idle→`waiting` transition) |
| `PermissionRequest` | — | `check_permission` (relay-mode envelope is Epic 10) |
| `SessionEnd` | `reason ∈ {clear, compact}` | soft refresh — no state change; bumps `last_seen_at` |
| `SessionEnd` | any other reason | `ended` (also sets `ended_at`) |
| unknown event | — | soft refresh + info-level log entry |

`missing` is only written by `find-missing` (Epic 8). `pending` is only
written by `spawn()`; the first SessionStart hook flips it to `waiting`.

State-tracking hook writes are fail-open: any internal failure logs and
exits 0 (SRD §3.2). A missed UPSERT never blocks Claude.

## Spawn Parameter Resolution

`spawn` is implemented as a four-stage pipeline, preceded by one id
check in the shared verb layer. The boundaries exist so each stage can
be tested in isolation against synthesized input.

```
  caller params         (CLI flags / MCP tool input / Go / TS client)
       │
       ▼
   ┌──────────────┐   pkg/api runSpawn, SRD SR-9.1: explicit
   │ Explicit-id  │   claude_instance_id with a byte 0x00-0x1f or
   │ check        │   0x7f → ErrInvalidFlags. Runs before any stage.
   └────┬─────────┘   Empty id passes (ApplyDefaults mints one).
        ▼
   ┌─────────┐   template merge: caller fields overlay template
   │ Resolve │   defaults; nil caller field → template value kept.
   └────┬────┘   ClaudeArgs: nil means caller supplied nothing
        │        (template wins); non-nil replaces wholesale.
        ▼
   ┌──────────┐   SRD §7.2: cwd shape/existence/type;
   │ Validate │   relay_mode; denied flags; reserved env keys;
   └────┬─────┘   explicit tmux session name (see below).
        │         No side effects on failure.
        ▼
   ┌────────────┐   SRD §7.3: UUID4 if no claude_instance_id;
   │ ApplyDefaults│  <basename(cwd)>-<id[:8]> session name;
   └────┬───────┘   relay_mode from config. Explicit id: collision
        │           pre-check via store (live row → ErrInstanceIdCollision;
        │           read failure → ErrInternal). Nothing created on error.
        ▼
   ┌────────┐   SRD §7.4: pending row insert; env compose;
   │ Launch │   --settings JSON synthesis; pre-trust cwd in .claude.json;
   └────┬───┘   tmux new-session via direct argv. Fire-and-forget.
        ▼
   claude_instance_id (state stays `pending` until SessionStart fires)
```

### Explicit-id check

`runSpawn` in `pkg/api/spawn.go` calls `validateExplicitInstanceID`
before `spawn.Resolve`. An explicit `claude_instance_id` that contains
any byte 0x00-0x1f or 0x7f returns `ErrInvalidFlags` with the
description "the instance id contains a control character". The id is
never included in the text. Such an id could never carry a valid
`@ad_owner` label: the label parser rejects control characters, and
lookup lines are split on tabs and newlines. The check runs before
template resolution, validation, the collision check and launch. A
rejected id therefore loads no template, reads and writes no store row,
and creates no tmux session, pre-trust write or trail entry. The CLI,
MCP, the Go client and the TypeScript client all reach `spawn` through
`runSpawn`, so every surface returns the same error. An empty or absent
id passes the check, and `ApplyDefaults` mints a fresh UUID4 for it.
Existing rows are not re-checked.

The check lives in `pkg/api` rather than `internal/spawn` because
`ErrInvalidFlags` is declared in `pkg/api`, and `internal/spawn` cannot
import it. Any future code path that accepts a caller-supplied instance
id for `spawn` must go through `runSpawn`, or call
`validateExplicitInstanceID` itself. Do not write a second byte loop.

### Collision pre-check

For an explicit `claude_instance_id`, `spawn.ApplyDefaults`
(`internal/spawn/defaults.go`) asks its `CollisionChecker` whether a live
row already holds the id (`LiveSpawnExists`). There are two error
outcomes:

- A live row, `pending` included, returns `ErrInstanceIdCollision`.
- A failed store read returns `ErrInternal`, with the description "the
  collision pre-check could not read the store: <store error>". A store
  fault says nothing about whether the id is in use, so it must never
  reach a caller as a collision, which callers read as "resume instead".

Either way nothing is created. The pre-check runs before `Launch`, so no
row, pre-trust write or tmux session follows. An empty id is never
checked; `ApplyDefaults` mints a fresh UUID4 for it. SQLite's PRIMARY KEY
still catches a race at INSERT, and `Launch` reports that as
`ErrInstanceIdCollision`.

The read-failure mapping lives in one place:
`spawn.PreCheckReadError(err)`. It formats the store error with `%v`,
not `%w`, so the result wraps no sentinel and `errnames.Classify`
returns `ErrInternal` on every surface. Every collision pre-check read
must map its failure through `spawn.PreCheckReadError`. That includes
the finished-id reuse path. Do not build a second pre-check error,
and do not wrap the store error with `%w`.

`runSpawn` in `pkg/api/spawn.go` takes the pre-check reader
(`spawn.CollisionChecker`) separately from the insert store.
`Client.Spawn` passes the same `*store.Store` for both. Tests inject a
failing reader through `api.SpawnWithCollisionReader` in
`pkg/api/export_test.go`.

### Explicit session-name validation

When the caller supplies `tmux-session-name`
(`SpawnParams.TmuxSessionNameSupplied`), `spawn.Validate` runs
`validateTmuxSessionName` (`internal/spawn/validate.go`). It checks in
this order:

- An empty value returns `ErrTmuxSessionNameEmpty`.
- A value longer than 64 bytes (`MaxTmuxSessionNameBytes`) returns
  `ErrTmuxSessionNameTooLong`.
- A value that is not valid UTF-8 returns `ErrTmuxSessionNameInvalid`.
- A value containing `#`, `:`, `.`, `$`, `\` or an ASCII control byte
  (0x00-0x1f, 0x7f) returns `ErrTmuxSessionNameInvalid` ("contains
  reserved character ..." or "contains ASCII control byte 0x..").

`$` and `\` are rejected because tmux cannot match such a name exactly.
A target such as `=$7:` reads as the session id `$7`, and a backslash is
processed by tmux's escaping (SR-9.2). All other characters are allowed.
The name is never rewritten: a caller-supplied name is used byte for
byte or rejected. Defaulted names go through `SanitizeSessionName`
instead.

### Workspace-trust pre-write

Claude Code shows a one-time "Quick safety check: Is this a project you
created or one you trust?" modal the first time it sees a new cwd. The
modal blocks before `SessionStart` fires, so a Spawn into a fresh cwd
sits in `pending` forever and `send-keys` refuses to drive it (the
precondition is a live state). Before exec'ing tmux, `internal/spawn`
resolves the target `.claude.json`: if the spawn's `extra_env` supplies
`CLAUDE_CONFIG_DIR`, that directory is used (`<CLAUDE_CONFIG_DIR>/.claude.json`);
otherwise the operator's `~/.claude.json` is used. It then sets
`projects.<canonical cwd>.hasTrustDialogAccepted = true` and writes
the file back atomically (temp + rename) so a torn write against the
operator's own Claude Code session is impossible. The same file is
written by the operator's Claude Code itself; concurrent updates use
last-writer-wins — the window is small and the outcome (both writers
end up with the same key set to `true`) is safe.

`--no-pre-trust` (`SpawnParams.NoPreTrust`) opts out for callers that
explicitly want the human-in-the-loop trust dialog — e.g. spawning into
a directory handed in by an untrusted caller. The flag defaults off, so
pre-trust is the default behavior. When the resolved file does not exist (truly
fresh Claude Code install, or a fresh `CLAUDE_CONFIG_DIR`), the write is
skipped and a soft warning lands on stderr; the spawn proceeds and the
trust dialog is unavoidable in that case.

Only `hasTrustDialogAccepted` is touched. Sibling keys
(`hasCompletedProjectOnboarding`, `hasClaudeMdExternalIncludesApproved`,
etc.) have semantics beyond trust and are left alone. Unknown top-level
and per-project keys round-trip verbatim via a `map[string]json.RawMessage`
shape, so future Claude Code releases that add keys are forward-compatible.

Layer boundaries (load-bearing):

- `internal/spawn` calls `internal/store` (one `InsertPending` UPSERT
  and one `LiveSpawnExists` collision read) and `internal/tmux` (one
  `NewSessionByName` argv). Nothing else.
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

**Emitted per-hook relay timeout.** `synthesizeSettings` emits an explicit
per-hook `timeout` field on exactly the `PermissionRequest` and `PreToolUse`
hook entries — placed on the inner command object (sibling of
`type`/`command`), not on the outer entry that carries `matcher`. Its value is
`config.Relay.EffectiveTimeoutSeconds()` (the configured `relay.timeout_seconds`
when positive, else the `DefaultRelayTimeoutSeconds` fallback of 86400). This is
the same accessor the relay poll loop's deadline derives from
(`internal/hook/polling.go`), so Claude Code's per-hook kill boundary and the
poll deadline are always the identical value. Without the field Claude Code
would kill the polling hook at its own 600-second default per-hook timeout —
discarding the hook's output with no envelope, so a late decision falls open
into the native permission flow. The other six hook events and the
`inject_help_hook` `SessionStart` entry carry no `timeout` and are unchanged.
Any future author touching either the emitted timeout or the poll deadline must
route through `EffectiveTimeoutSeconds()` — it is the single source of truth for
the "non-positive falls back to 86400" rule, and splitting it would let the two
boundaries drift.

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
`working`, `ask_user`, `check_permission`. `ended` and `missing` always
reject with `ErrSpawnNotInteractive` — there is no pane to write to.
`pending` also rejects by default; pass `allow_pending: true` to bypass
the check for the pre-SessionStart window (see below). The first
SessionStart hook flips `pending` to `waiting`, after which the Spawn is
reachable without the flag.

`allow_pending` opt-in: when `allow_pending=true` AND `state=pending`, the
state check is skipped and text is delivered directly to the tmux pane. The
canonical use case is dismissing interactive prompts that Claude Code renders
*before* SessionStart — e.g. the `--dangerously-load-development-channels`
warning. Without the flag a caller in this situation deadlocks: the prompt
blocks SessionStart, so the state never advances, and `send-keys` keeps
rejecting with `ErrSpawnNotInteractive`. `ended`/`missing` are still rejected
even when `allow_pending=true`.

Relay-mode guard (time-bounded): when `relay_mode=on` AND
`state=check_permission`, the permission relay normally owns the modal
answer, so `SendKeys` refuses with `ErrSendKeysWhileRelayed` to keep a
pane-side keystroke from racing the relay's `decide()` write. The refusal
is **not unconditional**: it consults the guard-release sibling of the
same single time-based authority `decide` uses
(`RelayRequestGuardReleasable`, SR-4.4 — same file, same margin constant
in `pkg/api/deliverability.go` as `RelayRequestUndeliverable`) across
*every* one of the spawn's `permission_requests` rows, each row's window
measured from its own `created_at` regardless of decision status. The one
deliberate difference is the **sign of the safety margin**: `decide` fails
early (refuses at `elapsed ≥ window − margin`), the guard fails late
(releases at `elapsed ≥ window + margin`), so the guard never frees while
a live poller — provably alive until ~`window` — could still emit a
decision. It refuses while any row might still be delivered (a zero-row
spawn also refuses — no signal, no authority to release) and **releases
only once every row's window plus the safety margin has elapsed**, at
which point the delivering hook is dead and send-keys becomes the
sanctioned recovery of a fallen-back relay (see "Send-keys interaction"
and "Invariant — relay-listener pairing" in the relay chapter). There is
no second independent check — no dialog-visibility probe, no re-derived
timeout arithmetic.

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

## Install flows

There are two complementary install surfaces, separated by what they
touch on disk. **Pattern A** (the npm postinstall) ships
`/install-agent-director` into Claude Code's skill registry so the
operator can discover the install skill in one step; **Pattern B**
(the install skill itself) is the only path that touches the CLI
binary, state DB, and Claude Code hooks. Pattern A is silent; Pattern
B is explicit and operator-confirmed.

### Pattern A — Postinstall skill copy

When the umbrella package is installed:

```
bun add agent-director
  → bun resolves umbrella + platform sub-package
  → bun runs pkg/ts-bun-client/scripts/postinstall.ts
      → host-pair gate (linux/x64 or darwin/arm64, else exit 1)
      → ${HOME}/.claude/skills/install-agent-director/ atomic copy
        of the bundled skill body
  → claude /install-agent-director is now invokable in any Claude
    Code session run by that operator
```

The postinstall **only** writes under `${HOME}/.claude/skills/`
(plus a sibling tmp dir and an optional timestamped backup). It does
NOT touch `~/.local/bin/agent-director`, `~/.agent-director/`,
`~/.claude/settings.json`, or `~/.claude/config.toml`. Those side
effects are reserved for Pattern B's `install.sh`. Keeping
postinstall narrow protects operators who install the library purely
to import it from TypeScript code and never want the CLI / state DB
/ hooks materialized.

The three-way decision (identical / older-or-absent / newer) is
governed by the YAML frontmatter `version:` field on
`SKILL.md`. Authoritative spec lives in SRD `t1.fg3.7i` SR-1.4.

### Pattern B — `install.sh` (the install skill)

Invoked from inside Claude Code via `/install-agent-director` (which
runs the skill body Pattern A copied), or directly via
`bash skills/install-agent-director/install.sh`:

```
claude /install-agent-director (or `bash install.sh`)
  → install.sh preflight gates (OS/CPU, --binary arch probe,
    required tools on PATH incl. sqlite3, whitespace-free install path)
  → write CLI binary to ~/.agent-director/bin/agent-director  (atomic mv)
  → schema migration at install-time (see below): open/migrate state.db
    under a one-shot migrate-authorized sentinel (full six-step flow in
    install-agent-director/SKILL.md)
  → merge SessionStart + SessionEnd hooks into ~/.claude/settings.json
  → optional ~/.local/bin/agent-director PATH symlink
```

Pattern B is where the CLI / state / hooks side effects happen.

#### Schema migration at install-time

Because the store refuses to auto-migrate under an agent (see the
schema-versioning section above), a bare warm-up would fail every
upgrade whose binary is newer than an existing `state.db`. `install.sh`
runs on the end-user's machine as an *administrator* action, so it is
the one legitimate place to authorize that migration — which it does
with a one-shot `migrate-authorized` sentinel: it reads the DB's ACTUAL
`user_version` (via `sqlite3 … "PRAGMA user_version"`, through the WAL —
hence `sqlite3` is a preflight requirement), writes a
`{"from":<actual>,"to":<target>}` sentinel beside `state.db` (skipped
when already current or on a fresh install), opens the store once with a
store-opening verb (`agent-director list`, deliberately not the DB-free
`help`/`version`) to run the migration and consume the sentinel, then
verifies the post-open `user_version` and aborts loudly (exit 5) on any
mismatch. A brief hook-failure window between the binary swap and that
open is accepted, not worked around. That same open gives the store its
store id: the v4→v5 hop creates it on an upgrade, and `createSchema` on a
fresh install. `install.sh` never reads or writes `store_meta`.

The full ordered six-step flow — including how `<target>` is learned
from the binary's own refusal message and why `list` rather than
`help`/`version` is the warm-up verb — is documented in
`skills/install-agent-director/SKILL.md` ("Schema migration: the
six-step sentinel flow"), the canonical admin-facing home for the
install flow. The store-side gate contract (sentinel path/shape, strict
parsing, one-shot consume-on-success, audit trail) lives in
docs/migration-guide.md §1a.

**Sentinel semantics (in brief; canonical in migration-guide.md §1a).**
The sentinel co-locates with `state.db`, names exactly one `from → to`
transition, and is honored only when *both* ends match. It is consumed
on the successful open and only then; a refused open runs zero DDL and
leaves both `state.db` and the sentinel byte-identical, never partially
honored.

**Who writes it.** The sentinel is written *only* by a human operator or
by this install skill — never by any agent. It is deliberately **absent
from every agent-facing surface** (help text, MCP tool descriptions, the
npm/customer README, the migration error message); those route the
operator to *this install flow* and nowhere else. `architecture.md`,
`install-agent-director/SKILL.md`, and docs/migration-guide.md are
internal/admin-facing, which is why they may name it.

### Pattern B fallback (postinstall skipped)

When `bun add --ignore-scripts agent-director` (or any client that
suppresses lifecycle scripts) is used, the postinstall does not run.
Pattern B is still reachable two ways:

1. Manual: `cp -r node_modules/agent-director/skills/install-agent-director ~/.claude/skills/` then invoke the skill.
2. Direct: invoke `claude /install-agent-director` — the skill body
   knows how to copy itself into `~/.claude/skills/` as a side
   effect of running install.sh.

Same end state in both cases.

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
├── migrate-authorized             (mode 0600; transient — present only
│                                    between an install's sentinel write
│                                    and the store open that consumes it)
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

Two concerns compose here: swapping the binary safely, and migrating
`state.db` safely across that swap.

**Binary swap.** The install script uses the standard single-binary CLI
install pattern (gh, kubectl, terraform): write to a sibling temp path,
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

**Schema migration across the swap.** Once the newer binary is in
place, an older `state.db` cannot be opened until the install authorizes
the migration with a one-shot `migrate-authorized` sentinel — the flow
is described under **Pattern B** above ("Schema migration at
install-time") and, in full, in
`skills/install-agent-director/SKILL.md`. The upgrade-safety caveat: because
migrations are forward-only, rolling the *binary* back after the DB has
migrated forward makes the older binary newer-than-DB in reverse and
surfaces `ErrSchemaMismatch`. Roll back only before letting the new
binary migrate the DB. After it has migrated, first return the store to
the older version with the emergency downgrade recipe in
docs/migration-guide.md §5 (for v5 → v4: drop the thirteen v5 columns
and the `store_meta` table, then stamp `user_version = 4`), or restore a
copy of `state.db` taken before the install. Either way the store id is
gone, so a later re-migration creates a new one and every label written
before the rollback reads as another store's; stop every agent before the
rollback and start them again after a re-migration.

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

`ErrSchemaMismatch` fires when the store's `user_version` is not recognized by
this binary — typically meaning the store was written by a newer binary
(`user_version > 5`). Note: an older-than-binary store (v1, v2, v3, or v4) does
**not** trigger `ErrSchemaMismatch` — it surfaces the distinct
`ErrSchemaMigrationRequired` instead. The store does not silently upgrade an
older DB on `Open`: the open is refused with `ErrSchemaMigrationRequired`
unless an administrator has placed a valid `migrate-authorized` sentinel next
to the DB file, in which case the gated migration chain runs (v1→v2, DROP+CREATE
`permission_requests` with v1 rows discarded; v2→v3, five `spawns` ADD COLUMN
preserving every row; v3→v4, CREATE `session_history` preserving every row;
v4→v5, thirteen ADD COLUMN across `spawns` and `session_history` preserving
every row and entry, then CREATE `store_meta` with one new store id).
Rolling the binary back after such a migration needs the
emergency downgrade recipe in docs/migration-guide.md §5 or a copy of
`state.db` taken before the install.

`ErrSchemaMismatch` also fires for a store at the current version that has no
valid store id: no `store_meta` table, no `store_id` row, or a value that is
not 16 lowercase hex (see `internal/store`, `store_meta`). The store and the
migration never produce that state; only a hand edit of `state.db` does (the
v5 → v4 recipe leaves the store at v4, which a v5 binary refuses with
`ErrSchemaMigrationRequired` instead). The error never contains the stored
value.

If a store-opening verb (e.g. `agent-director list`) reports `ErrSchemaMismatch`,
the recovery depends on the cause, and it is never to delete `state.db`:

- **Store newer than the binary.** Install the agent-director release that
  matches the store's schema. This loses nothing; it is the fix `install.sh`'s
  exit-5 message and the install skill give.
- **Hand-edited or missing store id, or a binary rolled back over a migrated
  store.** Restore the copy of `state.db` (with its `-wal` and `-shm` files)
  taken before the install. That copy is v4, so re-run the install afterwards;
  a v5 binary's install migrates it again. Writes made since the install are
  lost.

Deleting the store loses every row and the store id. JSONL transcripts under
`~/.claude/projects/` survive independently either way.

## Stdio MCP server

The MCP server is the JSON-RPC-over-stdio surface
(`internal/mcp`). It exposes every CLI verb as an MCP tool — except
the three filtered by `ExposedVerb` (see [Filtered verbs](#filtered-verbs))
— so an MCP-capable LLM client can drive Spawns without going through the
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

`tools/list` omits three verbs:

- `hook` — internal entrypoint invoked by Claude Code's hook machinery.
- `serve` — the MCP server itself.
- `trail-emit` — a CLI-only write verb that runs without opening
  `state.db` and has no MCP dispatch handler.

The filter lives in `internal/mcp/server.go::ExposedVerb`. The same
filter now also gates `docs/mcp-reference.md`: `tools/gen-docs`'s
`renderMCP` calls `internal/mcp.ExposedVerb` so the generated MCP
reference documents exactly the tools the live server registers —
these three verbs appear in `docs/cli-reference.md` but not
`docs/mcp-reference.md`.

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
`err.Error()` as the description. Errors that match no catalogued
sentinel return `"ErrInternal"`. This is the deliberate name for such
failures, and production paths do reach it: for example, the `spawn`
collision pre-check's store read failure (see
[Collision pre-check](#collision-pre-check)). `ErrInternal` is listed in
no verb's `ErrorNames`; a verb states its `ErrInternal` triggers in its
manifest description text instead.

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

- `ErrInternal` is the `Classify` fallback for errors that match no catalogued sentinel,
  such as `spawn.PreCheckReadError`'s result. It is not in the Catalog, is listed in no
  verb's `ErrorNames`, and is not enforced by the coherence check.
- `ErrInvalidFlags` has two sources. First, CLI flag parsing emits it for every verb: the
  `cmd/agent-director` flag handlers write it as a string literal in the error envelope.
  Second, the shared verb layer returns it for `spawn` only, from the explicit-id check in
  `runSpawn` (see [Explicit-id check](#explicit-id-check)). It is in the Catalog. It is
  listed in `spawn`'s manifest `ErrorNames` and its Go "Errors:" list, because spawn is the
  only verb whose shared verb layer emits it. No other callable verb lists it, because the
  CLI flag-parse emission is not specific to any verb. It stays in `check3Exceptions` in
  `pkg/api/errnames/coherence_diff_test.go`, next to `ErrInternal`; that list feeds the
  (b) ⊆ (c) check ("Check 3" in that file). Because `spawn` lists it, that check passes
  without the exception; the exception stays (SR-1.7). `TestDiffExclusionErrInvalidFlags` proves that the exception alone keeps that check quiet.
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
3. `make nondet-coverage` runs `tools/check-nondet` to enforce bidirectional alignment
   between `manifest.CallableVerbs()` and `test/envelope-diff/nondeterministic.json`: a
   **missing verb** (callable verb present in the manifest but absent as a JSON key) or an
   **extraneous key** (JSON key that names a non-callable verb) both fail the step. See
   [envelope-diff harness](#envelope-diff-harness) for the full non-determinism model.
4. `make check-sandbox-bypass` asserts the sandbox-guard bypass
   (`BYPASS_CONTAINER_FOR_AGENT_DIRECTOR_TESTS`) does not appear in
   `.github/workflows/pre-release-verify-mac.yml`, whose `[self-hosted, macOS, ARM64]`
   runner may hold a real `~/.agent-director`. See the sandbox-guard notes below and
   docs/engineering-guide.md §10 and
   [Sandbox guard and the CI bypass](#sandbox-guard-and-the-ci-bypass).

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

- **`internal/hook/envelope.go`** — `EncodeDecision(eventName, behavior, reason)`
  serializes the SRD §6.3 `hookSpecificOutput` envelope. The caller's
  `eventName` is written verbatim into `hookEventName`; production
  callers pass `EventNamePermissionRequest` (b.45p — the hardcoded
  literal was removed because Claude Code routes hook output by file
  descriptor rather than by envelope contents, so a mislabeled deny
  emitted from a non-PermissionRequest process was applied to the
  in-flight tool). The deny-default-message ("Denied by orchestrator")
  and the allow-message-omission rule both live here.

- **`internal/hook/polling.go`** — `Poll` is the loop. Pure function
  taking `(ctx, store, clock, cfg.Relay, id, *rand.Rand)`. The
  clock seam lets tests inject a fast variant; the rng is per-call
  so the jitter is deterministic in tests.

- **`internal/hook/permission.go`** — `runRelay` orchestrates the
  PermissionRequest relay path: UPSERT the open row, call `Poll`,
  write the envelope. Always emits an envelope before returning.

- **`internal/hook/handler.go`** — branches into `runRelay` when the
  event is `PermissionRequest` AND `AGENT_DIRECTOR_RELAY_MODE=on`.
  Pre-relay failure paths emit a deny envelope ONLY when the event is
  `PermissionRequest` AND relay is active (SRD §6.4 + b.45p). The
  handler peeks the event name from the raw payload via
  `PeekEventName` before resolving the instance id so the gate has
  honest information from the first failure point. Non-permission
  events (PreToolUse, etc.) stay fail-open even on internal failures.

- **`internal/store/permission.go`** — store primitives:
  - `UpsertOpenPermissionRequest`: INSERT-only per `(instanceID, requestToken)`.
    A second call with the same pair returns `ErrRequestTokenCollision`; the
    first row is unmodified.
  - `GetPermissionRequest`: pair-keyed read on `(claude_instance_id, request_token)`.
  - `GetPermissionRequestByToken`: token-only read (no `claude_instance_id`
    filter; SR-3.5 — the UUIDv4 is globally selective). Returns
    `ErrPermissionRequestNotFound` when no row matches; `sql.ErrNoRows` is
    translated here and MUST NOT leak across the store boundary (SR-7.4).
  - `DecidePermissionRequest`: the race-free first-call-wins UPDATE.
  - `DecidePermissionRequestIfDeliverable`: the deliverability-guarded
    variant of the above. Same `decision IS NULL AND request_token = ?`
    first-call-wins guard PLUS a `created_at > ?` predicate, so the
    deliverability check and the decision write are one atomic statement
    — there is no interval in which a success is returned but the relay
    window has already closed. The cutoff instant is computed by the
    `pkg/api` single authority (see `pkg/api/deliverability.go` below)
    and passed in; the boundary + safety-margin logic is never restated
    in SQL (SR-4.4).

- **`pkg/api/deliverability.go`** — the single authority (SR-4.4) for
  the relay delivery-window boundary, holding **both** sides of a
  deliberate asymmetry so each caller fails toward safety.
  `RelayDeliverabilityCutoff(now, effectiveWindow)` /
  `RelayRequestUndeliverable(createdAt, effectiveWindow, now)` are the
  fail-early pair used by `decide`: the cutoff is `now` less the
  effective window **plus** the named `RelayKillSafetyMargin` (a 1s
  epsilon at the kill boundary), so `decide` refuses at `elapsed ≥
  window − margin` and never records a success a dying hook might not
  deliver. `RelayGuardReleaseCutoff(now, effectiveWindow)` /
  `RelayRequestGuardReleasable(createdAt, effectiveWindow, now)` are the
  fail-late mirror used by the send-keys guard: the cutoff subtracts
  `window + margin`, so the guard releases only at `elapsed ≥ window +
  margin` and never frees while a live poller (provably alive until
  ~`window`) could still emit. Both pairs live in this one file and
  share the one `RelayKillSafetyMargin` constant — the "single time-based
  authority" is one file, one margin, applied with the sign that makes
  each caller safe. All four are pure, time-only functions of stored row
  state, the resolved window, and an injected clock — never dialog- or
  state-derived. Any code needing either boundary MUST consult these
  functions rather than re-derive it.

- **`pkg/api/decide.go`** — verb wrapper. State guards
  (`ErrRelayModeOff`, `ErrSpawnNotFound`, `ErrInvalidDecision`)
  before the UPDATE, then the atomic deliverability-guarded write via
  `DecidePermissionRequestIfDeliverable` (cutoff obtained from the
  shared single-authority function; the effective window is resolved
  once at `Client.Decide` via `config.Relay.EffectiveTimeoutSeconds()`
  and the clock is injected as `time.Now()`). A successful write means
  the decision is deliverable — never a recorded success against a dead
  relay hook. The RowsAffected==0 case is three-way disambiguated via a
  follow-up SELECT with pinned precedence: `ErrAlreadyDecided` wins for
  decided rows; `ErrRelayFallenBack` (the "too late — answer at the
  pane" sentinel) applies ONLY to open rows whose window has elapsed;
  otherwise `ErrNoOpenPermissionRequest`. A fallen-back refusal leaves
  `decision` NULL.

- **`pkg/api/get_permission.go`** — verb wrapper. Read-only: delegates to
  `GetPermissionRequestByToken` and projects the row onto the SR-7.4 wire
  shape (`GetPermissionResult`). Nullable DB columns (`decision`,
  `decision_reason`, `decided_at`) surface as pointer fields so the JSON
  encoding renders `null` for NULL. The store-layer
  `ErrPermissionRequestNotFound` sentinel is re-exported via `aliases.go` and
  Catalog-registered; callers detect it with `errors.Is` across both names.

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

`pkg/api/sendkeys.go`'s guard: when `relay_mode=on` AND
`state=check_permission`, `SendKeys` refuses with
`ErrSendKeysWhileRelayed` *while the relay can still act* — the relay
owns the modal answer, and a pane-side keystroke would race the relay's
`decide` write. The guard is **time-bounded, not unconditional**:
`evaluateRelayGuard` loads all of the spawn's `permission_requests` rows
via `PermissionRequestsForSpawn` and calls the shared
`RelayRequestGuardReleasable` signal — the guard-release mirror of the
same single time-based authority `decide` uses (SR-4.4, same file and
margin constant in `pkg/api/deliverability.go`; no independent second
check and no dialog probe) — on each, measuring each row's window from its
own `created_at` regardless of decision status. The margin sign is the one
deliberate difference: `decide` fails early (`window − margin`), the guard
fails late (`window + margin`), so the guard never frees while a live
poller could still emit. It refuses while any row might still be delivered
(and refuses on the zero-row transient — no signal, no authority to
release), and **releases only when every row's window plus the safety
margin has elapsed**. Once released, the delivering hook is provably dead,
so send-keys is the sanctioned recovery of a fallen-back relay — see the
invariant below and the `ad.send_keys.called` audit event. (If the store
read fails, the guard records `guard_evaluation="error"` — distinct from
the ordinary-send `"not-applicable"` — and the send fails with the store
error.)

### Invariant — relay-listener pairing

**Aggregate invariant (per Spawn).** If `spawns.state = check_permission`,
then either a `runRelay` polling loop is alive consuming `decide()` writes,
OR `permission_requests.decision` is non-NULL for the corresponding row. A
bot must never be sitting in "waiting for permission" with no live listener AND
no decision *and no sanctioned way out*: if a relay listener is gone and every
row is undeliverable, an external surface (e.g. a Slack approval message from
CSCB) would be a lying ghost — buttons that go nowhere.

**Sanctioned handling of the all-rows-undeliverable state.** The
listener-gone/decision-NULL state is not a stranded dead end. Once every
`permission_requests` row's window plus the safety margin has elapsed — the
guard-release mirror (`RelayRequestGuardReleasable`) of the same time-based
authority whose fail-early form (`RelayRequestUndeliverable`) makes `decide`
return `ErrRelayFallenBack` — the send-keys relay guard *releases* (see
"Send-keys interaction" above). The guard holds a margin longer than
`decide` refuses (`window + margin` vs `window − margin`), so by the time it
releases `decide` has long since returned `ErrRelayFallenBack`. The operator answers Claude Code's still-displayed native
permission dialog through `send-keys` (no dedicated verb, never raw tmux), and
the recovery is audited as `ad.send_keys.called` with
`guard_evaluation=released`. So the terminal state of a fallen-back relay is a
sanctioned, audited in-band recovery, not a lying ghost.

**What makes the invariant hold, and the window it holds within.** The
listener half of the invariant is guaranteed only for the configured relay
window, and only because `synthesizeSettings` emits the per-hook `timeout`
(equal to `relay.timeout_seconds`) on the `PermissionRequest`/`PreToolUse`
entries — see "Emitted per-hook relay timeout" in the spawn pipeline section.
Without that field Claude Code would kill the polling hook at its 600-second
default with no envelope — silently violating the invariant by removing the
listener while the row stays open. With the field, the poll deadline and Claude
Code's kill boundary are the same value, so the hook is never killed out from
under the loop; instead the timeout path (`decision='deny'`,
`decision_reason='timeout'`) closes the invariant in-band by writing a decision.
The native permission dialog Claude Code shows during a relayed request is
concurrent racing UI alongside the live `PermissionRequest` hook — not a
fallback state and not a hook-death signal; a decision envelope arriving within
the window dismisses it.

**Per-row refinement (SRD §6.2, v2).** The v2 schema allows multiple
concurrent `permission_requests` rows for the same Spawn, one per
`(claude_instance_id, request_token)` pair. The composite
`UNIQUE(claude_instance_id, request_token)` constraint enforces: at most one
outstanding row per `(Spawn, request_token)`. Each `runRelay` invocation mints
its own UUIDv4 `request_token` via `mintRequestToken()` so distinct concurrent
PermissionRequest events cannot overwrite each other's rows. The original
"at most one outstanding request per Spawn" is still a valid aggregate-level
safety property, qualified now by the per-row identity.

The AD code paths that satisfy this invariant:

- **Normal flow**: hook fires → `runRelay` mints a `request_token` →
  `UpsertOpenPermissionRequest(instanceID, requestToken, …)` →
  state=`check_permission` → `Poll(…, instanceID, requestToken, …)` runs →
  either `decide()` is called targeting the same `(instanceID, requestToken)`
  pair (decision set) or the loop times out and writes
  `decision='deny'`, `decision_reason='timeout'` to that specific row, then
  transitions state back to `working`.
- **Process death**: the `find-missing` reconciler iterates all open rows
  (`decision IS NULL`) for the dead Spawn via `OpenPermissionRequestsForSpawn`,
  writes `decision='deny'`, `decision_reason='find_missing'` to each row, then
  transitions state to `missing`.

Edge case: a panic inside `runRelay` AFTER
`UpsertOpenPermissionRequest` but BEFORE the loop iterates can
leave state=`check_permission` with decision=NULL if the
surrounding Claude Code instance is still alive (so `find-missing`
doesn't catch it). If this becomes observable in production, file a
follow-up.

### Get-permission verb (closed-row audit)

`get-permission` is the closed-row audit counterpart to `agent-director get`.
Both verbs read `permission_requests` rows; they partition the surface by
liveness, not by overlap:

- `get` projects only the **open** rows for a live Spawn as
  `SpawnRow.PermissionRequests` (the plural projection added in E1). It is
  Spawn-scoped and never surfaces closed rows.
- `get-permission` returns **one row in any state** — open, closed-allow, or
  closed-deny — selected by `request_token` alone. The token UUIDv4 is
  globally selective per SR-3.5, so no `claude_instance_id` is supplied; a
  caller who holds the token (typically captured at decide time from `get`'s
  plural projection) can resolve the row without prior knowledge of the
  owning Spawn.

The live-row contract that the "Invariant — relay-listener pairing"
subsection pins is untouched: `get-permission` is a read on rows the
relay-listener pair has already minted or closed, never a write or a state
transition.

**Layer delegation.** The CLI verb dispatches into
`pkg/api/get_permission.go::GetPermission`, which in turn calls
`internal/store/permission.go::GetPermissionRequestByToken`. Pure read path
— no INSERT, no UPDATE, no DELETE at any layer.

**`ErrPermissionRequestNotFound` semantics.** A single sentinel covers two
operationally indistinguishable cases:

- The token never named a row (typo, fabricated UUID, wrong session).
- The row existed but was reaped by the cap-based GC introduced in E3.

E2 leaves the `permission_requests` table unbounded, so in E2-only
deployments the sentinel always means "never existed". The dual meaning is
load-bearing for E3 — by design there is no separate eviction signal, and
callers MUST handle the sentinel uniformly across both cases. The store
boundary translates `sql.ErrNoRows` into `ErrPermissionRequestNotFound`
(SR-7.4); `sql.ErrNoRows` must not leak to verb-layer callers.

**`decision_reason` cross-reference.** The verdict annotation surfaces in
`GetPermissionResult.DecisionReason` as the closed enum defined by the
[`decision_reason` canonical enum](#decision_reason-canonical-enum)
subsection below — `operator`, `timeout`, or `find_missing`. The field is
`null` in exactly two cases (SR-1.3): on open rows and on closed-allow rows.
All closed-deny rows carry a non-null `decision_reason`.

**Concurrency contract.** Read-only and lock-free at the application layer:
`get-permission` is safe under arbitrary concurrent `runRelay` polling,
`decide` UPDATEs, and other `get-permission` calls against the same or
different rows. It relies on SQLite's read concurrency under the WAL journal
mode used by the store; no extra mutex, no transaction, no busy-retry loop
is required. The contract is pinned by
`TestGetPermissionRequestByTokenConcurrentReads` in
`internal/store/permission_test.go`.

### Cap-based GC

To bound `permission_requests` table growth, `UpsertOpenPermissionRequest`
runs an optional DELETE step **inside the same transaction as the INSERT**.
After inserting the new open row, if the total row count exceeds the effective
cap, the oldest closed rows are deleted (by `decided_at ASC`) until the count
equals the cap. Open rows (`decision IS NULL`) are never eviction candidates,
regardless of cap value.

**Trigger.** Eviction runs at every call to `UpsertOpenPermissionRequest` that
pushes the total row count past the cap. The check is synchronous and
transactional — there is no background sweep.

**Selector.** Only closed rows (`decision IS NOT NULL`) are eligible, ordered
`decided_at ASC`. The oldest decided rows are removed first.

**Cap = 0.** Eviction is disabled entirely; the DELETE step is skipped.
This is an operator opt-in to unbounded growth — useful for audit-heavy
deployments where no history may be discarded.

**Cap < 0.** Silently falls back to the default (1000), mirroring the
`TimeoutSeconds <= 0` guard (see
[Polling cadence + the 50ms floor](#polling-cadence--the-50ms-floor)).
The fallback prevents silent unbounded growth without surfacing a runtime
error.

**No time-floor guarantee.** Under a hot upsert burst a closed row can be
evicted within milliseconds of its `decided_at` timestamp. Agent-director
does not promise any minimum readability window for closed rows.

**Wire indistinguishability.** `get-permission` returns
`ErrPermissionRequestNotFound` for both a token that never existed and a
token whose row was evicted by cap-based GC (see
[Get-permission verb (closed-row audit)](#get-permission-verb-closed-row-audit)).
Callers must handle both cases identically — there is no eviction signal on
the wire.

**Config source.** `config.Relay.PermissionRequestCap` (TOML key
`permission_request_cap`), default 1000.

### `decision_reason` canonical enum

`decision_reason` is a nullable column; its valid non-null values form a
closed set. The three `DecisionReason*` constants in
`internal/store/permission.go` are the canonical definitions — all write sites
must use them, never free-form strings:

| Value | Written by |
| --- | --- |
| `operator` | `Decide` verb (orchestrator allow or deny) |
| `timeout` | relay polling-loop timeout path in `runRelay` |
| `find_missing` | `find-missing` reconciler per-row deny path |

**Null semantics.** `decision_reason` is `NULL` in exactly two cases:

- Open rows (`decision IS NULL`) — no verdict has been written yet.
- Closed-allow rows (`decision = 'allow'`) — a successful allow carries no
  reason annotation.

All closed-deny rows carry a non-null `decision_reason`.

**Stability policy.** New values are release-note-only changes; there is no
`protocol_version` field on the wire. CSCB (and any other consumer of
`decision_reason` values) must fail closed on unknown values — treat any
unrecognized `decision_reason` as a deny with an opaque reason. This gives
agent-director room to add new deny sources without a coordinated
breaking-change rollout across consumers.

## Trail file

agent-director writes an append-only JSONL audit trail as events occur.
This section is the canonical operator reference for the file's location,
line shape, and access patterns.

### Location

Fixed path: `~/.agent-director/ad-trail.jsonl`

agent-director state lives at `~/.agent-director`, resolved from the
invocation's effective home. The trail file is always
`~/.agent-director/ad-trail.jsonl` — both the `~/.agent-director` directory
name and the `ad-trail.jsonl` filename are fixed. The **persistent
environment-variable relocation switch is gone**: no env var and no config
key relocates agent-director state. The store and trail always resolve from
the invocation's effective home (via `$HOME` / `os.UserHomeDir`) or an
explicit `--store-path`. The documented per-invocation global flags `--home`
and `--store-path` (see the argv recipe above) are request-scoped
*targeting* — they point one invocation at a different home or store DB, not
a persistent relocation mechanism — and the trail follows the effective
home. Isolation — for tests or otherwise — is therefore achieved by running
inside the sandbox, where the resolved `~/.agent-director` does not exist,
never by relying on redirection.

**This is a plain on-disk JSONL file.** It is NOT a SQLite table, NOT a
column on `state.db`, and NOT any other database. Every line is one
self-contained JSON object, `\n`-terminated, with no file header or
trailer (SR-A-7.1, SR-A-7.7). The writer is append-only: no rotation, no
retention, no truncation — log rotation is the operator's responsibility
(SR-A-7.4, SR-A-7.5). One file descriptor is held open per AD process,
lazy-opened on the first `Emit` call (SR-A-7.6).

### Discoverability

The trail file's fixed path is documented here — this section is the operator
entrypoint (SR-A-6.1). There is no CLI read verb; the file is accessed directly
with standard shell tools (`tail`, `grep`, `jq`).

Relative to the invocation's effective home, the path is always:

```
~/.agent-director/ad-trail.jsonl
```

`~/.agent-director` is resolved from the effective home (via `$HOME` /
`os.UserHomeDir`); there is no env var or config key that persistently
relocates it. The per-invocation `--home` / `--store-path` flags only
retarget a single invocation, and the trail follows the effective home.
Operators find the trail at this one path, under whatever home the
invocation resolves, on every installation.

### Per-line envelope

Every line is a flat JSON object. Keys are **always top-level** — event
fields are never nested under `data`, `payload`, `body`, or any other
wrapper (SR-A-7.13).

Required fields on every event:

| Field | Type | Notes |
|-------|------|-------|
| `ts` | string | RFC 3339 UTC, millisecond precision. Regex: `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3,}Z$` (SR-A-7.9) |
| `event` | string | Dotted name in the `ad.*` namespace (required, non-empty) |

Conditional fields present whenever the value is known:

| Field | Type | Notes |
|-------|------|-------|
| `claude_instance_id` | string | Included whenever the emitter has an instance id in scope |
| `request_token` | string | Included whenever AD has minted a request token for the event |
| `source` | string | Identifies the emitter — see **Sources** below (SR-A-1.4) |

Event-class-specific fields are merged at the top level alongside the
fields above. There is no schema-version field and no migration path
(SR-A-7.16, SR-A-7.18).

**`tool_input` is never persisted.** The writer silently drops any
`tool_input` key before serialization. Callers must not attempt to log
tool_input (PRD §9, SR-A-2.1).

### `ad.*` event namespace

Nine event strings are emitted today. The first eight are the primary
event families; the last is a self-reporting meta event.

| Event | Source | Description |
|-------|--------|-------------|
| `ad.hook.fired` | `ad_hook` | One per `agent-director hook` invocation — records the hook payload and caller identity (SR-A-2.1, Epic 1) |
| `ad.spawn.state_transition` | `ad_spawn_store` | One per write to `spawns.state`, including no-ops and soft-refresh ticks (SR-A-2.2, Epic 2) |
| `ad.row_mutation.committed` | `ad_store` | One per successful write to `permission_requests` (SR-A-2.6, Epic 3) |
| `ad.decide.called` | `ad_decide` | One per `agent-director decide` invocation on every return path, carrying an `outcome` field set to the canonical err_name (or `ok`). Recognized failure outcomes include the no-op refusals `ErrAlreadyDecided` and `ErrRelayFallenBack` (a fallen-back refusal is a recognized outcome, not `ErrInternal`) (SR-A-2.4, Epic 4) |
| `ad.find_missing.tick` | `ad_find_missing` | One per row find-missing reconciles or flags: `reconciliation_reason=proc_absent` per row marked missing, `permission_orphan_closeout` per orphaned permission_requests row closed on that mark, and `probe_eacces` per live row that first transitions into the unverified (permission-walled) state — one tick per NULL→set transition only, never on a repeat sweep. There is no global-refusal tick (the old degraded-mode refusal is gone; unreadable rows are skipped and surfaced per-row). (SR-A-2.5, Epic 5; SR-7/SR-8) |
| `ad.relay_attempt.completed` | `relay_hook` | One per worker permission-relay attempt (SR-A-2.3, Epic 6) |
| `ad.resume.observed` | `ad_polling` | One per hook-resume back to Claude Code (SR-A-2.7, Epic 7) |
| `ad.send_keys.called` | `ad_send_keys` | One per `agent-director send-keys` invocation on every return path (fail-open, mirroring `ad.decide.called`). Carries `outcome` (canonical err_name or `ok`), AD-collected `caller_*` identity, and a `guard_evaluation` field — `not-applicable` (relay guard did not apply), `held` (refused, relay could still act), or `released` (guard released, the audited recovery of a fallen-back relay) — so recovery sends are distinguishable from ordinary sends and refusals (SR-5.2) |
| `ad.trail_meta.emit_failed` | `ad_trail_meta` | Self-reporting envelope written when a primary emit fails — carries `original_event` and `error_class` (SR-A-3.2) |

### Sources

The `source` field identifies which emitter wrote the line:

| Value | Emitter |
|-------|---------|
| `ad_hook` | `internal/hook/handler.go` — the hook ingestion handler |
| `ad_spawn_store` | `internal/store/spawns.go` — spawn state-machine writes |
| `ad_store` | `internal/store/permission.go` — permission-request row mutations |
| `ad_decide` | `pkg/api/decide.go` and `cmd/agent-director/spawn_cmd.go` — decide verb |
| `ad_find_missing` | `pkg/api/find_missing.go` and `internal/store/recovery.go` — reconciliation |
| `relay_hook` | `internal/hook/permission.go` and `cmd/agent-director/trail_emit_cmd.go` — relay-attempt completion |
| `ad_polling` | `internal/hook/permission.go` — resume observed on hook return |
| `ad_send_keys` | `pkg/api/sendkeys.go` — send-keys verb (per-invocation, carries the relay-guard evaluation) |
| `ad_trail_meta` | `internal/trail/trail.go` — the trail writer itself (meta-events only) |

### Operator access

The trail is designed for direct `grep`/`jq`/`tail` access against the
raw file. File + shell tools are the operator entrypoint (SR-A-6.1, SR-A-7.14,
SR-A-7.15). There is no CLI read verb.

**Live stream** — follow new events as they arrive:

```sh
tail -f ~/.agent-director/ad-trail.jsonl
```

**One interaction** — all events for a specific `request_token`:

```sh
jq -c 'select(.request_token=="<TOKEN>")' ~/.agent-director/ad-trail.jsonl
```

**Channel + time window** — filter by `channel` and timestamp range:

```sh
jq -c 'select(.channel=="C..." and .ts >= "T1" and .ts <= "T2")' \
  ~/.agent-director/ad-trail.jsonl
```

### Stitching with CSCB

AD's trail and CSCB's `permission-trail.jsonl` share `request_token` as a
join key. To get a single chronologically-ordered view across both surfaces
for one interaction:

```sh
cat ~/.agent-director/ad-trail.jsonl ~/.claude/channels/slack/permission-trail.jsonl \
  | jq -sc 'sort_by(.ts) | map(select(.request_token=="<TOKEN>"))[]'
```

### Fail-soft semantics

Trail writes never block verb execution. On any write or sync failure the
writer attempts one `ad.trail_meta.emit_failed` envelope (carrying
`original_event` and `error_class`). If that also fails, one line is
written to the operational logger. The original error is always returned
to the caller so verbs can fail-open (SR-A-3.2).

### Reusable writer

`internal/trail/` is the single write path for all `ad.*` events. All
event-emitting code calls `trail.Emit(ctx, event, fields)` — the
process-singleton `Writer` handles serialization, `tool_input` stripping,
fd management, and fail-soft meta events. Future event types add a new
`event` string and call `Emit`; do NOT introduce a parallel writer or
write directly to the trail file from application code.

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
4. JSONL transcript file exists on disk → otherwise `ErrJsonlMissing` or
   `ErrJsonlNeverWritten` (see below). Pure `os.Stat` pre-flight; no read.
   Candidate resolution follows a strict precedence (decision of record, bug
   b.1ba, extended by b.v2c):
   1. The persisted `jsonl_path` is tried first, if non-empty. If its
      `os.Stat` succeeds it **wins outright** — no fallback is computed.
   2. If `jsonl_path` is NULL/empty **or** its `os.Stat` fails for ANY
      reason (not just ENOENT — a permission-broken persisted path must
      not block an otherwise-resumable row), a fallback path is
      recomputed from the row's `ExtraEnv["CLAUDE_CONFIG_DIR"]` (or
      `~/.claude` when that key is absent/empty) `+ slug(cwd) + session
      id`, and that is `os.Stat`'d.
   3. If neither the current session's persisted path nor its fallback
      exists, `resume` walks the row's *visible history* (newest first,
      b.v2c AC6): the `session_history` entries of the row's current life,
      minus any entry whose session id equals the row's current session id
      (see `internal/store`, `session_history`). That entry is never tried;
      its session's own two candidates were already tried above, and a path
      recorded only on it is never stat'd. Each visible entry gets the **same
      persisted→fallback two-step the current session gets** (bug b.5jm):
      the recorded `jsonl_path` is stat'd first, and on ANY stat failure of
      a non-empty path — the b.1ba rot mode — the `CLAUDE_CONFIG_DIR`-aware
      path is recomputed and stat'd for that same session id before
      advancing to the next, older entry. (A NULL/empty recorded path skips
      straight to the recomputed path.) Without this two-step, a newer entry
      whose recorded path had rotted would be skipped and an older entry
      could silently win. The first visible
      transcript that exists **wins**: `resume` relaunches `claude
      --resume` against the archived id so it points at the recovered
      transcript (the mutation is in-memory only — the DB row itself is
      re-stamped by the relaunched Claude's subsequent SessionStart). This
      is what stops a rotation (the agent's row reported with a new session
      id, for example after the caller restarts its agents) from stranding
      intact history.

   This heals legacy rows written before the SessionStart hook persisted
   `jsonl_path`, rows whose recorded path has rotted, and rows whose history
   moved to an earlier session id on rotation — a successful fallback resume
   re-fires SessionStart, which re-persists the correct path. When **every**
   candidate (persisted, fallback, and every visible-history transcript)
   fails, the verb distinguishes two cases (b.v2c AC2), decided on the
   visible history:
   - `ErrJsonlNeverWritten` when the persisted `jsonl_path` was NULL/empty
     **and** the visible history is empty (a history holding only the
     current session id counts as empty) — nothing was ever written in the
     row's current life (a freshly restarted agent the caller has not yet
     messaged; a fresh Claude session writes no `.jsonl` until its first
     user turn). Recourse: message it, or delete + re-spawn.
   - `ErrJsonlMissing` otherwise — the persisted path was set and has
     rotted, or the visible history is non-empty and none of its
     transcripts exists. Recourse: `delete` + fresh `spawn`.

   Both messages report each path tried with its source (`persisted`,
   `fallback`, or `history`) and its stat error; every path named comes
   from the current session's own two candidates or the current life's
   visible history (see
   [JSONL path resolver](#jsonl-path-resolver-internalspawnjsonlgo)).
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
  the row's metadata reflects the running agent rather than the ended
  launch. A resume does not start a new life: the row keeps its
  `life_number`.

`claude_session_id` is overwritten by the same hook payload's
`transcript_path` basename. Claude Code rotates the UUID on every
`--resume`, so the column carries the freshly-rotated value after
the hook fires — the next resume of this resumed launch uses
the new id, pointing at the new JSONL. The rotation stays within the
row's current life.

**Conditional `jsonl_path` write (b.v2c AC1).** A fresh Claude session writes
no `.jsonl` transcript until its first user turn, so the `transcript_path` the
SessionStart payload reports may not exist yet. The hook `os.Stat`s that path and
tells the store whether the file is present; the store records `jsonl_path`
**only when the file actually exists on disk**, and leaves it NULL (provisional)
otherwise. This is what stops a row from asserting a dead pointer: an
un-messaged, freshly-restarted bot has a NULL `jsonl_path`, not a path to a file
that was never written. A stat error other than not-exist (e.g. a permission
wall) is treated conservatively as "not present". The provisional NULL is not
lossy — the resume fallback recomputes the path, and `find-missing` heals the
row once the transcript appears (below).

**Session-history archival on rotation (b.v2c AC6).** When the SessionStart
payload carries a **different** `claude_session_id` than the row currently holds
(a rotation — for example the agent restarted with a new session), the
store archives the prior `(claude_session_id, jsonl_path)` pair into
`session_history` **before** overwriting the `spawns` row with the new session.
The archived pair is tagged with the row's life, read in the same statement as
the outgoing session id and path, and written through
`upsertSessionHistoryEntry`. The earlier session's transcript is therefore never
orphaned: while the row stays in that life and the entry is not the row's
current session id, it is reachable through `get`'s `prior_sessions` and is a
resume fallback candidate.

**Lazy transcript healing in `find-missing` (b.v2c AC3).** `find-missing`
already sweeps every live row; on each sweep it also lists rows with a NULL
`jsonl_path` (provisional — a session that started before its transcript was
written), recomposes each path the same way the resume fallback does, `os.Stat`s
it, and records it when present. So the normal case — bot starts, operator
messages it an hour later — heals with no operator intervention. Per-row errors
are logged and skipped; healing never aborts the sweep.

### JSONL path resolver (`internal/spawn/jsonl.go`)

The resume pre-flight prefers the transcript path **persisted on the
row** (`jsonl_path`, stamped by the SessionStart hook once the file exists on
disk — b.v2c; a provisional NULL means the transcript was not yet written). That
path is authoritative — it records where Claude Code actually wrote the
transcript, including under a custom `CLAUDE_CONFIG_DIR`. It wins as
long as its `os.Stat` succeeds.

When `jsonl_path` is empty (rows written before the hook persisted it)
**or** its `os.Stat` fails for any reason (path rot, permission
break), the pre-flight recomputes a **`CLAUDE_CONFIG_DIR`-aware
fallback** and stats that (bug b.1ba). Two resolvers back this:

- **`spawn.JsonlPathIn(configDir, cwd, sessionID)`** — composes
  `<configDir>/projects/<slug(cwd)>/<session_id>.jsonl`. This is the
  config-dir-aware resolver the fallback uses when the row's
  `ExtraEnv["CLAUDE_CONFIG_DIR"]` is set, so a Spawn that ran under a
  custom config dir finds its transcript under
  `<CLAUDE_CONFIG_DIR>/projects/...` rather than `~/.claude/projects/...`.
  Reuse it whenever you need a transcript path under an explicit config
  dir — do not re-derive the layout by hand. This follows the
  established `internal/spawn/pretrust.go` pattern of reading
  `ExtraEnv["CLAUDE_CONFIG_DIR"]` to locate a per-spawn config dir.
- **`spawn.JsonlPath(cwd, sessionID)`** — a thin wrapper over
  `JsonlPathIn` that resolves the config dir to `$HOME/.claude`. Used
  for the default-config fallback (no `CLAUDE_CONFIG_DIR` key on the
  row). It reconstructs the default layout:

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

`Permissions` is the sole piece of spawn state NOT reconstructed on
resume:

- **`Permissions`** — the synthesized `--settings` JSON carries fresh
  hook entries on resume but no `permissions` block. Resume relies
  on Claude Code's tier-stack permissions.

`ExtraEnv` **is** carried over. The original spawn's extra env vars
(e.g. `ANTHROPIC_API_KEY`, `CLAUDE_CONFIG_DIR`) are persisted on the
row at launch and restored by `spawn.Relaunch` (`in.Row.ExtraEnv`,
decoded by `GetSpawn`), so a resurrected Spawn re-enters the same
auth/config context as the original — no dependence on whatever the
resuming caller's shell env happens to hold. Persistence adds no new
exposure tier: the values live only in the owner-only state DB
(0600 file / 0700 dir) alongside the rest of the row, and `ExtraEnv`
is not surfaced as an API-visible field.

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
  the wrong user simply can't read those env vars. Rows carrying a
  concrete pid + starttime no longer depend on this probe set at all —
  they get an individual, evidence-based verdict through the
  `LivenessChecker` seam (see
  [Degraded-mode reconciliation + cron user](#degraded-mode-reconciliation--cron-user)),
  whose Linux impl resolves an `environ` permission wall to an UNKNOWN
  verdict and skips just that row rather than misreporting it. Only the
  partial-identity rows (NULL pid or NULL starttime) still fall back to
  this probe set, and an unreadable `environ` there means the row is
  left in place, never marked dead.

- **macOS (`probe_darwin.go`)** — `sysctl("kern.proc.all")` returns
  the kinfo_proc array; per PID, `sysctl("kern.procargs2", pid)`
  returns the argv+env blob. The KERN_PROCARGS2 layout is
  `uint32 argc` + null-terminated `exec_path` + `argv[0..argc-1]` +
  `envp[0..]`. `envFromProcArgs2` skips past the argv section to
  reach the env, then scans for the prefix.

  The kinfo_proc walker (`parse_kinfo.go`) carries a family of XNU-
  version-sensitive constants. Two anchor the stride-based PID walk:
  `kinfoProcSize` (sizeof struct kinfo_proc = 648) and
  `kinfoProcPIDOffset` (byte offset of extern_proc.p_pid). Three more
  pin the per-entry identity fields the probe extracts: `kinfoEprocPPIDOffset`
  (byte offset of kp_eproc.e_ppid = `sizeof(extern_proc)=296` +
  `offsetof(eproc, e_ppid)=264` = 560) and the start-time pair
  `kinfoProcStartSecOffset`/`kinfoProcStartUsecOffset` (0 and 8 — the
  `kp_proc.p_starttime` timeval aliases the head of extern_proc's leading
  `p_un` union, so `tv_sec` sits at the very start of the entry and `tv_usec`
  8 bytes in). A sixth, `kinfoProcStatOffset` (36), pins the process state
  `extern_proc.p_stat` (a char) that the start-time reader uses to recognise a
  zombie: `p_un` (16) + `p_vmspace` (8) + `p_sigacts` (8) + `p_flag` (4) = 36,
  from `<bsd/sys/proc.h>`, just before `p_pid` at 40 (3 bytes of padding align
  the pid). Its known values are `kinfoStatSIDL` (1) through `kinfoStatSZOMB`
  (5, a zombie); `parseKinfoStat` treats any value outside that range as
  drift. All six are pinned to XNU 11.x (macOS 14 / 15) off the same
  LP64 header basis and are NOT a kernel ABI guarantee — a future macOS major
  bump that resizes the struct will silently drift both the stride-based
  walker and the identity offsets.

  Two independent guards catch that drift, and they carry *opposite*
  fail-semantics on purpose. The whole-buffer PID walker fails **closed**:
  if more than 10% of decoded PIDs fall outside `[1, 4_194_304]`,
  `parsePIDsFromSysctlBuf` returns `ErrProbeUnsupported` and `find-missing`
  refuses to emit a garbage probe set. The per-entry identity extractors
  (`parseKinfoPPID`, `parseKinfoStartTime`, `parseKinfoStat`) fail **open**: when an entry's
  bytes fail their field plausibility guards they return the distinct
  `ErrKinfoLayoutDrift` sentinel, which deliberately does NOT wrap
  `ErrProbeUnsupported` — drift here maps to unknown identity (SessionStart
  records NULL pid+starttime and proceeds) rather than a hard failure.
  Keeping the sentinels separate stops `errors.Is(err, ErrProbeUnsupported)`
  from also matching identity drift and re-importing fail-closed semantics.

  **macOS-major bump policy.** When supporting a new macOS major:
  compile the matching XNU sources (Apple publishes them at
  `apple-oss-distributions/xnu`), re-derive `kinfoProcSize` +
  `kinfoProcPIDOffset` **and** the identity offsets
  `kinfoEprocPPIDOffset` / `kinfoProcStartSecOffset` /
  `kinfoProcStartUsecOffset` / `kinfoProcStatOffset` (and the
  `kinfoStatSIDL`..`kinfoStatSZOMB` values) from `<bsd/sys/proc.h>` +
  `<bsd/sys/sysctl.h>`, refresh the constant comments in `parse_kinfo.go`,
  and re-run `GOOS=darwin GOARCH=arm64 go build ./...` plus the prober's
  integration test under that macOS version. The plausibility guards are
  a safety net, not a substitute for the bump. The same
  `kinfoProcStartSecOffset`/`kinfoProcStartUsecOffset` pair now also feeds
  the per-row liveness checker's starttime comparison (`checker_darwin_core.go`
  parses the LIVE pid's `p_starttime` via `parseKinfoStartTime` and compares
  it to the stored `proc_starttime`), so a bump that drifts those offsets
  affects the checker as well as the probe. That path is fail-open by the
  same rule: a `parseKinfoStartTime` layout-drift (`ErrKinfoLayoutDrift`)
  or any unpinned sysctl errno classifies to UNKNOWN, never provably-dead.
  The start-time reader (below) depends on the same pair plus
  `kinfoProcStatOffset`: it reads the start time and the process state from
  the single per-pid KERN_PROC_PID entry, and drift in either
  (`ErrKinfoLayoutDrift`, a short entry included) gives unreadable, never
  gone. A bump that drifts any of those three offsets therefore affects the
  start-time reader too.

- **Other** — the fallback returns `ErrProbeUnsupported` so
  `find-missing` fails closed rather than silently treating "no
  per-OS impl" as "no live processes".

Permission-denied / process-gone errors mid-walk are skipped silently;
a single foreign-owned process can't poison the whole probe.

#### Start-time reader (`ProcChecker`, `starttime.go`)

`ProcChecker` is the one start-time-only process reader (SR-3.8, SRD
Appendix F.2). New code that needs to know whether a pid is alive, and with
what start time, MUST use it (via `NewProcChecker()`) rather than reading
`/proc` or calling sysctl itself. Its intended users (SR-3.8) are the tmux
server check (SR-3.3); the start times of the identity write and of
adoption (SR-3.6); the SR-22.9 hook gate, which compares the hook parent's
start time with the row's recorded `pane_starttime`; SessionStart-identity
and pane liveness in `find-missing` (SR-11.1); kill's wait (SR-6.1);
expire's `process_alive` (SR-12.2); and resume's and reuse's check of a
running agent process (SR-4.2). No verb, command or hook calls it yet:
`find-missing` still uses `LivenessChecker` / `NewChecker()` unchanged.

```go
type ProcChecker interface {
    StartTime(pid int) (start string, alive bool, known bool)
}
func NewProcChecker() ProcChecker
```

`StartTime(pid)` answers exactly one of:

| Answer | Return | Meaning |
|---|---|---|
| alive | `(start, true, true)` | The process runs with this start time. |
| gone | `("", false, true)` | No such process, or a zombie (Linux state `Z` or `X`; darwin `p_stat == SZOMB`). |
| unreadable | `("", false, false)` | EACCES/EPERM, no or unreadable `/proc` root, a malformed entry, kinfo layout drift, a pid ≤ 0, or an unsupported OS. |

`start` is empty unless alive. Its form is the `proc_starttime` the store
already records: on Linux field 22 of `/proc/<pid>/stat` verbatim; on darwin
the KERN_PROC_PID entry's `p_starttime` as `<tv_sec>.<tv_usec>`
(`formatDarwinProcStartTime`). Callers compare it byte-for-byte with their
recorded value; a different start time means the pid was reused. The reader
never reads a process environment and never reads the clock.

`NewProcChecker()` picks the implementation by build tag:

- **Linux** — `linuxStartTimeReader` reads only `<procRoot>/<pid>/stat`,
  parsed by `parseLinuxStatWithState`. A missing entry (ENOENT/ESRCH) is
  gone only when `<procRoot>/self` resolves; otherwise the answer is
  unreadable, so an unmounted procfs (an empty `/proc`) reads unreadable,
  never gone. Under `hidepid=noaccess` the kernel returns EPERM, and the
  reader answers unreadable. Under `hidepid=invisible` or `ptraceable`,
  another user's pid returns ENOENT and reads gone; those two modes also
  hide a non-dumpable process of the same user. The agent, its pane and the
  tmux server run as the caller's own user, so they stay visible.
- **darwin** — `darwinStartTimeReader` fetches the single KERN_PROC_PID
  entry (the fetch today's checker uses; no KERN_PROCARGS2) and parses both
  `parseKinfoStat` and `parseKinfoStartTime` before the zombie check. ESRCH
  or an empty buffer is gone; any other errno or layout drift is unreadable.
- **Other** — `unsupportedProcChecker` answers unreadable for every pid.

The Linux and darwin cores and `unsupportedProcChecker` are build-tag-free,
so tests drive them on any OS through their seams: `procRoot` (a temp
directory) and `fetchKinfo` (a synthetic 648-byte entry).

### Degraded-mode reconciliation + cron user

**There is no global degraded-mode refusal.** The old guard — "probe
returned zero IDs while the DB holds ≥1 live row → write nothing, log a
warning" — has been removed (SR-7/SR-8). It over-refused: after a full
reboot the probe set is legitimately empty even though every recorded row
is genuinely dead, and a single unreadable process used to block the whole
sweep. `find-missing` now reconciles **per row on evidence**, and an
unreadable row is skipped and surfaced as unverified rather than blocking
anything.

**Per-row evidence model.** `find-missing` lists every live-state identity
(`ListLiveSpawnIdentities` — each row's `claude_instance_id` plus its
recorded `pid` + `proc_starttime`) and partitions on identity completeness:

- **Full identity (pid AND starttime recorded)** — the row gets an
  evidence-based verdict from the `LivenessChecker` seam
  (`internal/probe`, `NewChecker()`), which returns one of three verdicts:
  - *provably-dead* → mark missing (see the pinned marking order below).
  - *verified-alive* → skip and clear any stale liveness fields.
  - *unknown* → skip THIS ROW ONLY, set the liveness metadata, and record
    the id as unverified.
- **Partial/absent identity (NULL pid OR NULL starttime)** — the row falls
  back to the environ **probe-set diff**: a live id absent from
  `probe.Probe()`'s set is marked missing. This is the sole remaining
  consumer of the environ prober. There is **no per-row skip here** — a
  partial-identity row carries no pid/starttime evidence to fall back on, so
  absence from the probe set is the *only* signal, and any such row not in the
  set IS marked missing. That includes the case where the invoking user can't
  read the target processes' environ (a wrong-user or empty-probe run): every
  partial/NULL-identity live row is then absent from the set and gets marked.
  This is the intended post-reboot behavior (the probe set is legitimately
  empty and those rows are genuinely dead) — and precisely why same-user
  scheduling matters (see the cron user story).

**Pinned per-OS errno mapping (SR-7.4) — the cardinal rule is
UNKNOWN-never-dead.** Only positive, pinned evidence yields provably-dead;
ANY unexpected errno or parse/layout-drift failure resolves to UNKNOWN.
The tables live inside the per-OS checker impls
(`checker_linux_core.go`, `checker_darwin_core.go`), expressed as pure
`classify{Linux,Darwin}Errno` functions so the tables are unit-testable
off any OS:

| Evidence | Linux (`/proc`) | macOS (sysctl) | Verdict |
| --- | --- | --- | --- |
| Process-table entry absent | `stat` ENOENT/ESRCH | KERN_PROC_PID ESRCH / empty | provably-dead |
| Recorded starttime ≠ live starttime (pid reuse) | stat field 22 mismatch | `p_starttime` mismatch | provably-dead |
| Can't read the process-table entry | `stat` EACCES/EPERM | KERN_PROC_PID EACCES/EPERM | unknown |
| starttime matches, env readable but LACKING the instance id | environ scan | PROCARGS2 env scan | provably-dead (tiebreaker) |
| starttime matches, env unreadable (permission wall) | environ EACCES/EPERM | PROCARGS2 EACCES/EPERM | **verified-alive** |
| starttime matches, env readable and HAS the instance id | environ scan | PROCARGS2 env scan | verified-alive |
| Any other errno, malformed stat, or `ErrKinfoLayoutDrift` | — | — | **unknown (never dead)** |

The env-permission-wall row is the load-bearing case: pid + starttime
already proved the tracked process is running, so an unreadable env is
*verified-alive*, not unknown — we simply couldn't run the id tiebreaker.
The checker never returns an error; every failure mode folds into UNKNOWN
per the fail-open contract.

**Liveness metadata fields + unverified surfacing.** Unknown liveness is
row metadata, never a lifecycle state — the `state` enum is untouched. Two
nullable columns carry it: `liveness_unverified_since` (set on the FIRST
EACCES sweep, preserved verbatim on repeats) and `liveness_note` (e.g.
`probe_eacces`). `SetLivenessUnverified` writes both in one guarded UPDATE
that fires only when `liveness_unverified_since` is currently NULL; its
returned bool is the sole NULL→set signal. The fields are cleared when the
row is later verified-alive, immediately after it is marked missing, and on
every hook row-UPDATE path. `FindMissingResult` carries `unverified`
(count) and `unverified_ids` (sorted, `[]` never null) alongside
`count`/`ids`, and the `list`/`get` verbs expose the two liveness fields as
additive nullable fields, so an operator sees exactly which rows were
skipped for lack of evidence.

**Marking order.** A provably-dead (or fallback-absent) row is reconciled
in a pinned sequence: `MarkSpawnMissing` (the unchanged primitive; returns
the prior state so a no-op on an absent/terminal row writes nothing
downstream) → `ClearLivenessUnverified` → one `ad.find_missing.tick`
(`reconciliation_reason=proc_absent`) → `CloseOrphanedPermissionRequests`
(which fail-closes any relay polling loop and emits its own
`permission_orphan_closeout` tick per closed row). The unverified path
emits exactly one `ad.find_missing.tick` (`reconciliation_reason=probe_eacces`)
per NULL→set transition and nothing on repeats. Per-row store/checker
errors are logged and skipped; the sweep never aborts on one bad row, and a
trail-emit failure never changes the sweep's return.

**Cron user story.** `find-missing` still assumes it runs as the user that
owns the Spawns (or as root), and a user mismatch no longer *corrupts or
refuses* — but the two identity classes react to a mismatch very
differently, which is exactly why the run-as-owner rule still matters.
Full-identity rows (pid + starttime recorded) the invoking user can't read
hit the env permission wall and are surfaced as **unverified**
(verified-alive if pid + starttime matched, unknown otherwise) — never
silently marked missing; the per-row skip protects them. Partial/NULL-identity
rows have **no per-row evidence** to skip on, so their sole signal is the
environ probe-set diff — and a wrong-user (or otherwise empty) probe set
leaves every one of them absent from the set, so they **are marked missing**.
That is correct after a real reboot (the probe set is legitimately empty and
those rows are dead), but under a mere user mismatch it would wrongly mark
still-live partial-identity Spawns. The recommended operator setup is
therefore load-bearing, not cosmetic: a **systemd user-timer or a personal
crontab** — not a system-level cron — so the userland identity matches the
Spawn-launching identity, full-identity rows get real verdicts instead of a
wall of unverified metadata, and partial-identity rows are diffed against a
probe set that can actually see them.

### `find-missing`

`pkg/api/find_missing.go`:

1. `ListLiveSpawnIdentities` returns every row where `state NOT IN (ended,
   missing)`, each carrying its recorded `pid` + `proc_starttime`. This
   includes `pending` — SRD §5.2 explicitly scans pending rows so a Spawn
   whose tmux died before SessionStart fired still reconciles correctly.
2. Rows are partitioned by identity completeness (see
   [Degraded-mode reconciliation + cron user](#degraded-mode-reconciliation--cron-user)).
3. Full-identity rows get a per-row `LivenessChecker` verdict:
   provably-dead → mark missing; verified-alive → clear stale liveness
   fields; unknown → skip that row only, set `liveness_unverified_since` +
   `liveness_note`, and record the id in `unverified_ids`. There is no
   global refusal — an unreadable row is surfaced, not a blocker.
4. Partial-identity rows fall back to `probe.Probe()`'s set: a live id
   absent from the set is marked missing.
5. Each marked row runs the pinned marking order (`MarkSpawnMissing` →
   `ClearLivenessUnverified` → `proc_absent` tick →
   `CloseOrphanedPermissionRequests`). Per-row failures (e.g. transient
   SQLite I/O error) are logged and the sweep continues — one bad row does
   not abort the others.

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
Spawns (or as root). `find-missing` no longer refuses on a user
mismatch: rows it can't read are surfaced per-row as **unverified**
(or verified-alive when pid + starttime already matched) rather than
corrupted or globally skipped — see
[Degraded-mode reconciliation + cron user](#degraded-mode-reconciliation--cron-user)
for the full story. `expire` and `delete` are pure DB operations and
don't depend on probe permissions; running them as the wrong user is
harmless (they just operate on whatever rows the DB happens to hold).

The recommended operator setup is a systemd user-timer or a personal
crontab — not a system-level cron — so the userland identity matches
the Spawn-launching identity automatically and rows get real liveness
verdicts instead of a wall of unverified metadata.

### Reboot-recovery runbook

After a machine reboot, every Spawn's process and tmux server are gone,
but the rows persist in `state.db` frozen at their pre-reboot
live state (`waiting`/`working`). Recovering a Spawn's conversation is a
**two-verb contract, in order**:

1. **`find-missing`** — reconciles the frozen rows against reality. On a
   reboot the probe set is legitimately empty and the rows are genuinely
   dead, so this marks them `missing` (a terminal state). This step is
   **required first**: `resume` refuses any non-terminal row with
   `ErrSpawnNotResumable`, so a row still frozen at `waiting`/`working`
   cannot be resumed until `find-missing` moves it to `missing`.
2. **`resume`** — relaunches each missing row via `claude --resume` in a
   fresh tmux session under the same `claude_instance_id`, restoring the
   persisted `ExtraEnv` (auth/config, incl. `CLAUDE_CONFIG_DIR`) and
   replaying the JSONL transcript. The transcript is located by the
   [pre-flight precedence](#verb-pkgapiresumego) (persisted `jsonl_path`,
   then the `CLAUDE_CONFIG_DIR`-aware fallback, then any transcript of the
   row's visible history: the current life's archived `session_history`
   entries, minus the entry for the row's current session id), which is
   what lets a Spawn that ran under a custom config dir — or whose session
   rotated when its agent restarted — recover across a reboot.

```
agent-director find-missing        # reconcile frozen rows → missing
agent-director resume --claude-instance-id <id>   # relaunch each
```

**`ErrJsonlNeverWritten` vs `ErrJsonlMissing` (b.v2c).** A resume can fail two
ways, and the errors mean different things:

- `ErrJsonlNeverWritten` — the row has a session id but no transcript was ever
  written in its current life (persisted `jsonl_path` NULL and an empty
  visible history: no archived session of the current life other than the
  row's current session id). This is a freshly restarted agent the caller has
  not yet messaged: a fresh Claude session writes no `.jsonl` until its first
  user turn. There is genuinely nothing to resume; the caller messages the
  agent (its transcript then appears and `find-missing` heals the row)
  or `delete` + re-spawn.
- `ErrJsonlMissing` — the persisted path was recorded and has since rotted, or
  the visible history is non-empty and none of its transcripts exists. The
  paths it names all come from the current life. Recovery is `delete` + fresh
  `spawn`.

`get`'s `transcript_status` field surfaces this distinction **before** a resume
is attempted: `never_written`, `rotated` (the current life's visible history
is non-empty, i.e. history exists under a different session id — see
`prior_sessions`, which lists that visible history), `present`, or
`no_session`.

**Recovering a pre-v4 orphaned transcript (manual).** When a session rotated and
intact history was stranded under an earlier session id that the store does not
currently point at **and was never archived** — only possible for a pre-v4
rotation, since `session_history` archives every rotation from schema v4 onward —
recovery is a manual operator task, not a product verb. The repair tooling was
deliberately not shipped (a permanent CLI/MCP surface to fix a class of incident
that can no longer occur is a bad trade). Instead, the
`repair-orphaned-transcript` skill
(`.claude/skills/repair-orphaned-transcript/`) walks an operator (or an LLM)
through locating the orphaned transcript across both config dirs and issuing the
single-transaction SQL that archives the row's current
`(claude_session_id, jsonl_path)` pair into `session_history`, as an entry of
the row's current life, and re-points the row. Post-v4 rotations self-archive
and need no repair.

**`delete` is NOT a recovery step.** It is destructive: it removes the
row along with its `claude_session_id`, labels, and `extra_env`, making
any later recovery impossible. Reaching for `delete` + fresh `spawn`
after a failed resume throws away the conversation. The only legitimate
recovery path is `find-missing` → `resume`; `delete` is an admin
force-removal verb, not part of the recovery contract. (A resume that
fails with `ErrJsonlMissing` names every transcript path it tried and
its source, so the operator can diagnose *why* before deciding anything.)

**Autostart is the CALLER's responsibility.** agent-director does not
watch for reboots and does not schedule anything itself. Its recovery
contract **starts at** "the caller invokes `find-missing` then
`resume`". Whatever triggers that sequence after a boot — a systemd
unit, a startup script, a `find-missing` cron loop — is owned and
operated by the caller, not by agent-director. Before reaching for `delete`
after a failed resume, check `get`'s `transcript_status` and `prior_sessions`:
`rotated` means the current life's visible history is non-empty and may be
recoverable — a plain `resume` walks the current life's archived sessions
(minus the row's current session id) and reattaches automatically — and
`never_written` means the caller simply hasn't messaged the agent yet; neither
warrants a destructive delete.

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

Pre-release operator checklist: see bee `b.dc1` in the `Release` hive.

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

The `/release` skill (`skills/release-agent-director/SKILL.md`) is the
operator runbook and top-level release driver. Orchestration is split: the
skill-running Claude instance drives phase sequencing and gate evaluation —
every phase decision and the decision to proceed is LLM-driven — while the
publish phase is executed by a shell-script orchestrator,
`skills/release-agent-director/gates/publish/publish-orchestrator.sh`. That
script owns the six publish substeps (push-branch, create-tag, gh-release,
npm-publish, fast-forward-main, delete-remote-branch), including their
dry-run gating, halt-on-first-failure sequencing, substep recording, and
SR-numbered diagnostics. Before the first (irreversible) substep it resolves
every file-input argument (`--tarball`, `--notes`, `--binaries`) to an
absolute path at parse time and runs an artifact preflight
(`publish.preflight-publish-artifacts`) that halts the run — in both live and
dry-run mode — if any of those paths is not a readable file, so no public tag
or Release is created for a run that would fail at `npm-publish` on a bad
input (b.mjd).

**Worktree-isolation contract.** The skill creates a separate git worktree
at `.release-work/release-v<target>/`. The operator's primary checkout is
never written during a release run; the only mutation to the primary
worktree is the final fast-forward-main substep (the last action of a
successful live run).

**Source-of-truth invariant.** `pkg/ts-bun-client/package.json` is the
sole authoritative version string in the repo (SR-16). Every other site —
binary ldflags, npm sub-packages, release notes — derives from it. The
SR-16 gate (`pkg/ts-bun-client/scripts/check-source-of-truth.ts`) enforces
this invariant and fires on any independent version site detected outside
the derivation chain.

**Phase model.** Phases run in strict order, most-reversible to
least-reversible:

```
preflight → branch-and-bump → coverage → compile → smoke
→ coherence(binary) → pack → install → coherence(tarball+bump)
→ notes → publish → finalize
```

Each phase is a self-contained gate; any failure halts the run immediately.
The publish and finalize phases are gated behind explicit operator
confirmation after all earlier phases pass. Defaults to dry-run; live runs
require explicit opt-in.

**Subprocess directory layout.** Each gate phase delegates to a shell
helper in `skills/release-agent-director/gates/<phase>/`:

```
gates/preflight/    gates/branch/       gates/coverage/
gates/compile/      gates/smoke/        gates/coherence/
gates/pack/         gates/notes/        gates/publish/
gates/finalize/
```

**Run report.** `dist/release-report.json` is written on every run (dry
and live). It captures every phase, every sub-check, every publish substep,
every diagnostic message, and elapsed time per phase.

**SR-13 outcome dispositions.** On a successful live run the release
worktree is removed. On failure or dry-run the worktree and all staged
artifacts are preserved for operator inspection.

#### Go module tagging convention

The `pkg/api` Go module is in-repo and shares the root `go.mod` —
no separate `pkg/api/go.mod` exists. Consumers resolve
`github.com/gabemahoney/agent-director/pkg/api@$VERSION` via the
single root tag. If the package is ever split into its own module
the release skill must additionally push `pkg/api/$VERSION` (the
sub-path tag Go's module protocol requires); the publish phase already
detects this case via the presence of `pkg/api/go.mod`.

### ErrSchemaMismatch on upgrade

Schema upgrades are **gated**, not automatic: an older-than-binary database
(v1, v2, v3, or v4) is refused on `Open` with `ErrSchemaMigrationRequired` unless an
administrator has placed a valid `migrate-authorized` sentinel next to the DB
file. Only then does the migration chain run: the v1→v2 hop (DROP+CREATE
`permission_requests`, no row preservation), the v2→v3 hop (five
`spawns` ADD COLUMN, no backfill), the v3→v4 hop (CREATE
`session_history`, no backfill), and/or the v4→v5 hop (thirteen ADD COLUMN
across `spawns` and `session_history`, no backfill, then CREATE `store_meta`
with one new store id), walking from the DB's `user_version` up to
`schemaVersion` in one pass. `ErrSchemaMismatch` fires when `user_version >
5` — meaning the store was written by a binary newer than the current one —
or when a v5 store has no valid store id, which only a hand edit causes (see
"ErrSchemaMismatch recovery"). Rolling a release back after its install
migrated the store needs the emergency downgrade recipe in
docs/migration-guide.md §5 (the thirteen columns and `store_meta`) or a copy
of `state.db` taken before the install; a later re-migration then creates a
new store id.

Bumping `schemaVersion` beyond 5 requires:

1. Add a `migrateVNtoVN1` hop in `internal/store/schema.go` and append a
   `migrationStep{from: N, apply: migrateVNtoVN1}` entry to the `migrationSteps`
   registry (following the `migrateV1toV2` pattern). The chain engine walks the
   registry automatically — do not add per-version switch arms.
2. Document the schema change in the release notes.
3. Operators upgrading from a version older than the migration path's base must
   `rm ~/.agent-director/state.db*` post-upgrade.
4. Active Spawns whose JSONL transcripts under `~/.claude/projects/` are still
   on disk can be re-resumed by id via `agent-director resume`.

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
   matching the `cd-` prefix, then calls `agent-director list` to
   rebuild the store at the binary's current schema version (a fresh DB
   is created directly at that version, not migrated up from v1). Cases must therefore derive the expected `user_version` from the
   shipped binary rather than hard-code a literal; hard-coding a stale
   version is what turned this lane red in b.m9q.
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

### Envelope-diff regression harness

`test/envelope-diff/` (Go test package `envelope_diff`) is the SR-7.4 regression gate: the canonical proof that `cmd/agent-director` and `*pkg/api.Client` return structurally-equivalent envelopes for every callable verb.

For each verb in `manifest.CallableVerbs()`, the harness copies a fixture store to a temp directory, runs the freshly-built `agent-director` binary as a subprocess (capturing stdout, stderr, and exit code), then invokes the same verb in-process via `*pkg/api.Client`. Each side is reduced to a single envelope per the shape contract — stdout when exit is 0, stderr when exit is non-zero. Both envelopes are normalized via sorted-key JSON re-encode, filtered through the per-verb non-determinism manifest at `test/envelope-diff/nondeterministic.json`, and structurally diffed. Mismatches are reported as path-style failures (e.g. `.spawns[0].claude_instance_id`).

**File roles** (one per concern, no cross-layer logic):

- `harness.go` — fixture-copy helper and one-time CLI/fake-tmux binary build (`sync.Once`).
- `runners.go` — `runCLI` (subprocess) and `runClient` (in-process) plus per-verb dispatch.
- `selectors.go` — path matching with `[*]` wildcard support.
- `diff.go` — JSON normalization and structural diff.
- `manifest_loader.go` — loads and validates `nondeterministic.json`.
- `nondeterministic.json` — per-verb selector manifest; every callable verb in `manifest.CallableVerbs()` is a key, deterministic verbs carry `[]`.
- `nondeterministic.md` — selector grammar and decision tree for classifying fields as deterministic or non-deterministic; consult before adding or removing entries.

`test/envelope-diff/nondeterministic.json` is verb-keyed: every callable verb in `manifest.CallableVerbs()` maps to the field-path selectors excluded from the diff (generated IDs, build stamps, call-time timestamps, and any other documented non-deterministic fields); deterministic verbs carry `[]`. See `test/envelope-diff/nondeterministic.md` for the selector grammar and the decision tree for classifying fields. A missing entry for a non-deterministic field causes the diff to fail. The Task 6 coverage gate, wired into the doc-drift CI check (SR-8.3), enforces that the manifest lists every callable verb in `manifest.CallableVerbs()` — no more, no fewer; adding a callable verb without a corresponding manifest entry fails CI.

**Epic scope.** Task 1 builds the scaffold and unit tests. Per-verb success cases (Task 3) and documented-error cases (Task 4) follow. CI wiring (Task 5) and the nondeterministic.json completeness check (Task 6) close the Epic. `serve` and `hook` are excluded — they are non-callable and carry no envelope contract.

**Error-coverage contract.** Every callable verb with non-empty `ErrorNames` has at least one error-path subtest in `test/envelope-diff/error_cases.go` asserting that CLI and Client envelopes carry an identical `err_name` and a matching `err_description` (prefix-match policy documented in `test/envelope-diff/nondeterministic.md`). `TestErrorTableCoverage` is the CI gate enforcing this: it iterates `manifest.CallableVerbs()` and fails if any verb with non-empty `ErrorNames` lacks a corresponding `error_cases.go` row, so new error sentinels cannot land without coverage — analogous to the `nondeterministic.json` completeness gate that enforces every callable verb is represented in the non-determinism manifest. Two entries are explicitly exempted: `ErrTemplateExists` for `make-template` (its `err_description` embeds an absolute temp-dir path that the prefix-match policy cannot normalize across the two fixture copies on Linux; `ErrTemplateNameUnsafe` provides alternative make-template coverage) and `ErrProbeUnsupported` for `find-missing` (only compiled on non-linux/non-darwin targets via build tags; the empty-store success path covers find-missing on CI).

**CI integration.** The harness runs in CI via the `envelope-diff` job in `.github/workflows/integration.yml` on every PR and push to main. The job builds the CLI binary (`tmpbin/agent-director`) and the fake-tmux helper (`tmpbin/faketmux/tmux`) from the commit under test, then runs `go test ./test/envelope-diff/...` with `AGENT_DIRECTOR_TEST_BINARY` and `AGENT_DIRECTOR_FAKE_TMUX_DIR` set to absolute workspace paths so the test process does not pay the build cost a second time. The same step sets `BYPASS_CONTAINER_FOR_AGENT_DIRECTOR_TESTS` — the package carries `sandboxguard.Require()`, and the hosted runner is ephemeral with no real store to protect; see [Sandbox guard and the CI bypass](#sandbox-guard-and-the-ci-bypass) for the placement rule.

### Go smoke test

`test/smoke/go/` is the canonical home for the Go-side smoke test. Its purpose is to exercise every callable verb through `pkg/api.Client` exactly as an external consumer would — no subprocess invocations, no access to `internal/` implementation details.

**Verb coverage.** `manifest.CallableVerbs()` drives the verb list (16 verbs). `serve` and `hook` have `Callable=false` and are excluded.

**Import constraint.** The smoke target imports only `pkg/api`, `pkg/api/manifest`, and `internal/testsupport/*`. Imports of `internal/api`, `internal/store`, or any other `internal/` package are prohibited and enforced at test time by `test/smoke/go/import_graph_test.go` (Task c8). This keeps the smoke test honest as a consumer: if `pkg/api` does not expose something, the smoke test cannot reach around it.

**No verb chaining.** Each subtest receives a fresh store and a fresh tmux recorder. Preconditions (e.g., a live Spawn row required by `status` or `send-keys`) are injected by the `internal/testsupport` seeders — never produced by calling another verb first. This makes subtests independent and order-invariant; a subtest failure cannot cascade into later subtests through shared state.

**Race and repeatability.** The smoke target must pass under:

```sh
go test ./test/smoke/go/... -race -count=2
```

`-race` shakes out goroutine-level data races in `pkg/api` and its dependencies. `-count=2` runs each subtest twice in the same process, exposing inter-test state leakage (e.g., package-level singletons or temp files not cleaned up between runs).

### storefix seeders (reusable test fixtures)

`internal/testsupport/storefix/` is the canonical home for store-layer
test fixtures. Future test authors MUST reuse these `Seed*` helpers
rather than reinvent raw-connection row setup or backdating; each helper
uses `t.TempDir()` and never touches `~/.agent-director`.

**`SeedUndeliverablePermissionRequest(t, s, dbPath, instanceID, requestToken, age)`**
is the reusable helper for undeliverability tests: it backdates the
`created_at` of a single open `permission_requests` row (identified by
token) to `now-age` so the shared time-based signal
(`api.RelayRequestUndeliverable`) reads it as undeliverable, while
leaving other open rows for the same spawn deliverable. Because the
store API deliberately exposes no `created_at` mutation, the helper uses
a second raw `sql.Open` connection with a UTC-formatted UPDATE (mirroring
`SeedExpiredCandidate` and `SeedClosedPermissionRequests`) — do NOT
hand-roll raw-connection backdating in new tests; call this instead. It
deliberately does not hardcode the 86400s default or restate the
non-positive→default fallback (that lives solely in
`config.Relay.EffectiveTimeoutSeconds`); callers choose `age` relative to
the effective window. Seed the open row first (e.g. via
`SeedCheckPermission`, which uses `TestRequestTokenA`).

**`InjectWriteFailure(t, dbPath, kind, instanceID)`** makes one kind of
write to one id's rows fail on the concrete `*store.Store` (SR-20.3). The
four kinds (`WriteFailureKind`, an alias of `writefailfix.Kind`) are:

- `WriteFailReuseArchive`: an insert or update of the id's
  `session_history` entry.
- `WriteFailReuseReset`: an update moving the id's finished row (`ended` or
  `missing`) to `pending`.
- `WriteFailReusePermissionDelete`: a delete of one of the id's
  `permission_requests`, including one done by the cascade when its `spawns`
  row is deleted.
- `WriteFailReuseRestore`: an update moving the id's `pending` row to
  `ended` or `missing`.

How it works:

- It installs BEFORE triggers through a second raw connection to the temp
  store file. Each trigger runs `RAISE(ABORT, 'injected write failure: <kind>')`,
  so the store method returns an error; the write never silently does
  nothing.
- The trigger SQL is fixed text. The id is recorded, with bound parameters,
  in a bookkeeping table (`ad_test_write_failure`) that each trigger's WHEN
  clause checks. Other ids and other rows are unaffected.
- The test's cleanup removes the triggers and the bookkeeping table.
- Several kinds also match other existing writes, including seeding writes
  (for example, `SeedSpawn` of a finished row matches `WriteFailReuseRestore`).
  So install after seeding. Each `writefailfix.Kind` constant's doc comment
  lists what it also matches.

Single trigger source: the trigger SQL lives only in the leaf package
`internal/testsupport/writefailfix` (`Kind`, `Kinds()`, `Install`,
`Handle.Remove`; imports only `database/sql` and `fmt`). Its white-box
counterpart for tests inside `internal/store`, which cannot import
`storefix`, is `injectWriteFailure(t, s, kind, instanceID)` in
`internal/store/migration_fixtures_test.go`. It calls the same
`writefailfix.Install` on the store's own connection.

**Must use:** tests make a concrete-store write fail only through
`storefix.InjectWriteFailure` or, inside `internal/store`, its white-box
counterpart. Never write trigger SQL or any other failure SQL in a test.
Writes behind a store interface fail through a failing wrapper of that
interface.

### Which tmux test double to use

- In-process verb tests: `tmuxfix.Recorder`
  ([tmux test doubles](#tmux-test-doubles-replay-catalogue-recorder-and-clock-reusable-test-fixtures)),
  paired with `apitest.SeedSpawn` rows ([Seed* contract](#apitest-seed-factory-contract-reusable-test-fixtures)).
- Subprocess tests (CLI, MCP, envelope-diff, TypeScript): `test/fake-tmux`, which Go tests get and drive through `faketmuxfix` ([fake-tmux](#testfake-tmux-the-subprocess-tmux-double-reusable-test-fixture)).
- Real-tmux proof, sandbox only: `test/realtmux` ([realtmux](#testrealtmux-real-tmux-proof-of-the-client-reusable-test-fixtures)).
- Reply wording always comes from the replay catalogue, and timing tests use `tmuxfix.Clock`.

**Must use (server isolation, SR-20.3):** a test that reaches a tmux
server, real or fake, uses a per-test `TMUX_TMPDIR` with `TMUX` unset, or
puts the fake first on `PATH`, so no two tests share a server.

### tmux test doubles: replay catalogue, Recorder and Clock (reusable test fixtures)

`internal/testsupport/tmuxfix` holds the in-process tmux doubles (SRD
SR-20.3, SR-20.4, Appendix F.5). The package doc comment and each
exported name's doc comment carry the detail; this subsection says what
exists and when to use it. The subprocess double is `test/fake-tmux` (next
subsection).

**Replay catalogue** (`replay.go`, `replay_answers.go`, `replay_names.go`).
The tmux evidence of SRD Appendix E as data, recorded on tmux 3.2a;
identical on 3.3a (composed entries say so in their `Source`). An `Entry`
holds the exact standard-output and standard-error bytes, the exit status
and, per call kind, the typed `tmux.Failure` the production client must
return (`Want`; 0 is success), with the expected `Socket`, `FirstLine` and
parsed answer. It contains:

- `Replies(socket)`: every reply wording with the socket path as a
  parameter (`NoServer`, `NoSocket`, `SocketDenied`, `Duplicate(stored)`,
  `Silent()` and the rest), each on its recorded stream and exit status.
- `LookupAnswers()`: the F1, F2 and F9 lookup answers plus malformed ones;
  `PaneListings()`; `CreateReplies()`; `Captures()`.
- `StoredNames()`: the stored forms of `$` and `\` names, of a name spelled
  as a pane id (`%9`, from SRD Appendix E.9 T2a) and of names with `.`, `:`
  and invalid UTF-8; `StoredName.Entry()` is a one-session lookup listing
  that stored form (the non-UTF-8 listings). `LocaleForms()`: names
  and ids as a `-u` client lists them and as a client without `-u` does
  (`ü-x` / `_-x`). `LabelShapes()`: the AC-LKP-05 label shapes, each with
  the label class it reads as. `PaneLabelShapes()`: 16 `@ad_pane` value
  shapes on pane `PaneLabelLineID` (`%1`) of `$1`, each with the
  `Pane.AdPane` it reads as, as entries named `panes/label:<name>`. Only
  `valid` and `valid-other-token` give the token; the rest (another pane's
  id, a neighbour id `%12`, a session id, no pane id, a trailing space, an
  extra field, a double space, a tab separator, a token in uppercase, of 15
  or 17 hex or a placeholder, an `@ad_owner`-shaped value, empty) give `""`.
- The pane listings are six-field, the pane label last. `PaneListings()`
  includes labelled and unlabelled panes, a split pane with no label, a
  server value borrowed onto every pane (only its own pane counts), a label
  holding a tab (parses, `AdPane` `""`), the old five-field form (now
  malformed) and malformed listings whose `FirstLine` stops before the
  label field. `reply/no-such-pane` (`no such pane: %99`, the label by id's
  pane step, recorded on tmux 3.3a only) is the one pane-label reply.
- Builders for composed data: `SessionLine`, `LabelValue`,
  `ChainLabelValue`, `PaneLine` (its last argument the raw `@ad_pane`
  value, `""` for none), `PaneLabelValue(token, paneID)` (`<token> <%N>`),
  `ChainPaneLabelValue(token)` (`<token> #{pane_id}`), `Listing` with
  `PaneListing` (a pane and its raw value), `CreateReplyLine`, `Valid`,
  `Answer`; the
  tokens `Token` and `OtherToken`; the store ids `StoreID` and
  `OtherStoreID` (fixed 16-hex values for catalogue entries).
- Every label is in the five-field form `ad1 <token> <$N> <instance id>
  <store id>`. `LabelValue(token, sessionID, instanceID, storeID)`,
  `ChainLabelValue(token, instanceID, storeID)` (`#` doubled only in the
  instance id) and `Valid(token, instanceID, storeID)` all take the store
  id last. `LookupAnswers` and `StoredNames` use `StoreID`. `LabelShapes`
  includes the AC-LKP-05 store-id shapes (`four-fields`, a store id in
  uppercase, of 15 or 17 hex characters, empty or a placeholder) and valid
  shapes whose instance id has spaces or `#` or ends in a 16-hex word.

Consumers: the `internal/tmux` replay tests, which feed `Entry.Result()`
to the client through the test-only runner seam `tmux.NewWithRunner`
(`internal/tmux/export_test.go`; the fixture is
`internal/tmux/replay_helpers_test.go`, in package `tmux_test` because
`tmuxfix` imports `internal/tmux`); the fake's `Reply` injections; and the
`test/realtmux` assertions. The Recorder's typed failures map one-to-one to
the catalogue's `Want` kinds.

**Must use:** tests never spell a tmux reply wording, stored name or lookup
line inline (SR-20.2). They take it from the catalogue or build it with its
builders. The catalogue and the Recorder hold the only tmux reply wording
in tests.

**Recorder** (`recorder*.go`). `tmuxfix.Recorder` implements
`api.TmuxClient` and is injected through `Options.TmuxClient`. It models
tmux at the level of the typed API (Appendix F.1): it never produces or
parses reply text.

- **Per-socket tables.** Each socket has at most one bound server
  (`Server`: pid, `#{start_time}`, process start time) holding sessions
  (`SeedSession`: id, stored name, creation time, typed `tmux.Label`,
  `LabelSet`, panes; each `SeedPane` has window, index, id, pid and
  `AdPane`, the pane label's token as the listing classifies it, typed and
  never raw) and scope values (`SetScope` / `ClearScope` with
  `ScopeLevel` and a typed `ScopeValue`). A listed label follows tmux's
  precedence: server value, else global-window value, else the session's
  own, else global. Servers: `StartServer`, `RestartServer` (the old server
  stops; ids restart), `RebindServer` (the old server keeps running
  unbound), `StopServer`, `Servers` / `Server` for a process-checker fake.
  Seeding: `SeedSessions`, `SetCapture`; `Sessions` reads the table back.
- **Default answers from the table.** Lookup, pane listing, both kills,
  sends, capture and label by id act on the table. The create starts a
  server if none is bound, adds `$N` with one pane, labels it
  `Valid(token, id, storeID)` with the caller's store id only when `!tmux.NeedsLabelByID(name)` (the chained
  label rule), and in that case also gives its pane `AdPane` = token (the
  pane label); a scripted `FailLabel` leaves both unset. It gives
  `FailDuplicate` for a stored name already held. The pane listing returns
  each pane's `AdPane`. Label by id follows tmux's step order: an unknown
  session id changes nothing; otherwise the session is labelled, then the
  pane `paneID`, found anywhere on the server, gets `AdPane` = token, and an
  unknown pane id fails with the session left labelled. Panes added by
  `ReplaceSessionAfter` have no pane label.
  An unknown pane or session id gives `FailUnrecognized`. A server stays
  bound after its last session; `StopServer` models its exit. A call on a
  socket with no server fails with `FailNoSocket`, or the failure set by
  `SetNoServerFailure`.
- **Scripted typed results.** `Script(socket, Script{...}, calls...)`
  replaces the table's answer per call kind and per socket (`AnySocket`
  for any): once or N times (`Times`), or always (`Times` 0). Any failure
  kind, `FailSocketDenied` included, can be scripted on every call kind.
  `Applied` applies the table effect before the failure.
- **Hooks.** Session hooks `ReplaceSessionAfter` (same stored name, new id
  and panes, a given label) and `RemoveSessionAfter` fire once after the
  next matching call. `AfterCall` hooks run on every matching call.
- **Call order.** Each socket-taking call runs its table effect or scripted
  result, then the virtual-time charge, then the session hooks, then the
  after-call hooks (outside the Recorder's lock, so they may call back into
  the Recorder or the store), then returns.
- **Recorded calls.** `SocketCalls` / `SocketCallsOf(call)` return
  `SocketCall` records. A create or label-by-id record carries the label's
  `Token`, `InstanceID` and `StoreID`; a label-by-id record also carries
  `PaneID`, the pane its pane label is set on.
- **Store ids.** Sessions the Recorder creates, labels by id or seeds from
  a row carry the caller's store id: `SetLabel` stores
  `Valid(token, id, storeID)`, and `SeedRowSession` labels with the id of
  the store it reads the row from. Tests take this store's id from
  `apitest.ReadStoreID` or the open store's `StoreID()`, and another
  store's from `apitest.OtherStoreID`. A test may pass the catalogue's
  `tmuxfix.StoreID` as a caller's store id (the argv, replay, Recorder,
  fake-tmux and realtmux tests do). A test that seeds rows from a store
  labels them with that store's id (`apitest.ReadStoreID` / `StoreID()`),
  never with the catalogue constant.
- **Name-based methods.** `NewSessionByName`, `HasSession`, `KillSession`,
  `SendKeys` and `CapturePane` keep their old behaviour (`Calls`,
  `CallsOfKind`, `WithPaneOutput`, `WithHasSession`) until their last user
  moves. They are never charged and run no hooks. `Reset` clears recorded
  calls and scripts; tables, hooks and virtual time stay.

**Must use:** in-process verb tests use the Recorder, never a hand-written
`TmuxClient` fake.

**Clock and virtual time** (`clock.go`, `WithVirtualTime`).
`tmuxfix.Clock` (`NewClock(start)`, `Now`, `Advance`) is the shared test
clock that verbs under test read. It is safe for concurrent use, and
`Advance` with a negative duration steps it back.
`Recorder.WithVirtualTime(c, t)` binds the Recorder to `c`: every
socket-taking call, whatever its result, advances `c` by its class's
timeout from `t` (query: lookup and pane listing; action: kills, text and
Enter sends, capture, label by id; create: the create). A zero field of `t`
takes the `internal/config` default through the `config.Tmux` accessors;
`tmuxfix` spells no duration. A scripted `FailTimeout` carries the same
value. `t.WaitDelay` is never charged. Outside virtual time the Recorder
never touches a clock. Seeded sessions and creates take their default
creation time from the bound clock's current second, else the wall clock.

**Must use:** timing tests (ceilings, sweep budgets, grace periods) use
this clock with virtual time. They never sleep and never define their own
clock.

**Seeding bridge.** `SeedSpawn` gives a live row the default pane, and
`Recorder.SeedRowSession` places a seeded row's own labelled session in
the Recorder. See [apitest Seed* factory
contract](#apitest-seed-factory-contract-reusable-test-fixtures).

`tmuxfix` imports `internal/tmux`, `internal/config` and `internal/store`.

### test/fake-tmux: the subprocess tmux double (reusable test fixture)

`test/fake-tmux` is the subprocess path that exercises the production
`internal/tmux` client end to end (SRD SR-20.3). CLI, MCP, envelope-diff
and TypeScript smoke tests put it first on `PATH` or pass it as the tmux
command (`Options.TmuxCommand`, `tmuxCommand`). Its package doc comment
(`test/fake-tmux/main.go`) is the full reference. Its shared, non-test
half is `internal/testsupport/faketmuxfix`.

**Socket form** (argv starting `-u -S <socket>`). The fake splits the argv
into commands like real tmux: an argument ending in an unescaped `;` ends
the command, its text before the `;` (if any) staying as that command's
last argument, so a standalone `;` is a plain separator; an argument ending
in `\;` loses that backslash and does not split; a `;` elsewhere in an
argument is kept. An empty command (for example two `;` in a row) exits 2
on purpose: real tmux silently drops it, but the fake rejects it so a
client bug fails loudly.

It answers the Phase 1 call set: the lookup (`list-sessions -F` with `#{@ad_owner}` resolved by tmux's scope precedence, plus the three
`show-options` scope reads), `list-panes -a`, `kill-pane` and
`kill-session` by id, both `send-keys` forms, `capture-pane`, `new-session`
with its `-P -F` reply and chained `set-option`s, and `set-option` by id.
Commands run in order; the first failure ends the invocation, as in tmux.
A name already held gets the catalogue's `duplicate session: <stored name>`
reply. An unknown target gets the catalogue's reply for it. Argv the fake
does not understand exits 2, and a table it cannot read or write exits 3,
both with no output.

**Pane label.** Each pane keeps its own raw `@ad_pane` value
(`faketmuxfix` `Pane.AdPane`, JSON `ad_pane`; `""` is unset). `set-option
-p [-F] -t <%N | =<name>:> @ad_pane <value>` sets the target pane only;
`=<name>:` means that session's first pane, and `-F` expands `#{pane_id}`
for it. The create chain and the label by id each end with this step, so a
created pane is labelled and a pane seeded without a value (a split pane)
reads empty. `@ad_owner` with `-p`, or `@ad_pane` without it, exits 2. An
unknown pane id, or any other non-chain target, gets the catalogue's
`reply/no-such-pane`; a chain-form target that matches nothing gets the
same line as for `@ad_owner`. `#{@ad_pane}` is the pane's own value only:
the fake models no window, session, global or server `@ad_pane`, so a test
writes a borrowed value into the pane's entry as the text tmux would list.
`ResolveEntry` also resolves the `PaneLabelShapes` entries.

**Per-socket tables.** Each socket has one table (`faketmuxfix.Table`:
`Server`, `Scope`, `Sessions` with their `Panes` (each with its `AdPane`) and capture text, id
counters, `NewPanePID`, `Injections`). By default it is the file
`<socket>.fake-tmux.json` beside the socket path, so in-process tests on
different sockets need no process-wide variable. With `FAKE_TMUX_TABLES`
set to a directory, every socket's table is a file there, so a subprocess
test keeps its own tables even on the shared test socket. A socket with no
table answers as a server with no sessions. Every write takes a lock and
replaces the file atomically.

**Must use:** tests read and write tables only through
`faketmuxfix.Tables{Dir}` (`Path`, `Env`, `Write`, `Read`, `Update`,
`Inject`). They never write a table file by hand.

**Injections** (per socket, per call kind, stored in the table):

- `Reply(call, entry)`: a catalogue entry's exact bytes on its recorded
  streams, with its recorded exit status. An entry name that does not
  resolve fails the test at write time.
- `ExitCode(call, n)`: a bare exit status with no output.
- `Hang(call)`: no answer until the client's timeout kills it. On its own
  the fake exits after a bound (`DefaultHangBound`, 30 s; `Bound(d)` sets
  it).
- `HoldPipes(call, d)`: a normal answer (or `.WithReply(entry)`) and exit,
  while a child keeps the output pipes open for `d`. The pipe-close-wait
  tests use it.
- `ChainFails()`: the create succeeds and prints its reply, then the
  chained label step fails with the catalogue's line.
- Modifiers: `.WithEffect()` applies the call's normal table effect first;
  `.FirstN(n)` limits the injection to the next `n` calls (default: every
  call). Every hang and hold is capped at `MaxBound` (2 min).

**Control variables.** Tests use the names through the `faketmuxfix`
constants and never spell them: `EnvLog` (`FAKE_TMUX_LOG`), `EnvPaneOutput`
(`FAKE_TMUX_PANE_OUTPUT`), `EnvFailNewSessionName`
(`FAKE_TMUX_FAIL_NEWSESSION_NAME`) and `EnvTables` (`FAKE_TMUX_TABLES`). None
starts with `AGENT_DIRECTOR_`, which the production client strips.
`FAKE_TMUX_FAIL_NEWSESSION_NAME` now prints the catalogue's
`duplicate session: <stored name>` wording.

**No wording of its own.** Every reply the fake prints comes from the
replay catalogue. Its only literal output is the capture stub, which is
pane content, not a reply.

**Legacy form.** Argv without a leading `-u` is what the name-based client
methods send, and behaves as before: `new-session`, `send-keys`,
`kill-session` and `capture-pane` log their argv to `FAKE_TMUX_LOG` in the
same format (one element per line, then `---`) and exit 0; `capture-pane`
prints `FAKE_TMUX_PANE_OUTPUT` or the stub; `has-session` exits 1; anything
else exits 0. The socket form logs every invocation.

**Build helper.** `faketmuxfix.Binary(t)` builds the fake once per test
binary and returns the path of a file named `tmux`; `faketmuxfix.Dir(t)`
returns its directory for a `PATH` prepend. **Must use:** new Go tests get
the fake only through `faketmuxfix.Binary` / `Dir`. The existing builders
(`buildFakeTmux` in `test/envelope-diff/harness.go` and in
`cmd/agent-director/spawn_cli_test.go`) stay as they are. The Makefile's
`test/fake-tmux/tmux` rule (`make fake-tmux`) lists as prerequisites every
non-test source of the packages the fake links (`FAKE_TMUX_SRCS`,
including `faketmuxfix`, `tmuxfix` and `internal/tmux`), so a change to the
shared packages rebuilds it.

### test/realtmux: real-tmux proof of the client (reusable test fixtures)

`test/realtmux` is the real-tmux half of the production `internal/tmux`
client's contract (SRD SR-20.4, SR-20.7). The replay tests prove the client
against recorded replies; this package proves those replies against a real
tmux 3.3a and runs the client against it. Every observed reply is asserted
against the same [replay catalogue](#tmux-test-doubles-replay-catalogue-recorder-and-clock-reusable-test-fixtures)
entry the replay tests use. The package is `realtmux_test`, all `_test.go`
files. The package doc comment is in `main_test.go`. The shared helpers are in
`harness_test.go` (isolation, raw runner, cleanup), `harness_client_test.go`
(production client, recording, creates, starters, generators, assertions)
and `harness_proc_test.go` (polling, `/proc` readers). tmux 3.2a is covered
only by the replay catalogue; the real-tmux tests run on the sandbox image's
3.3a (SR-20.8, AC-TEST-03).

**Running it.** Only in the sandbox:
`make sandbox CMD="go test ./test/realtmux/... -count=1 -v"`, never on the
host. `make test-sandbox` also runs it as part of `go test ./...`. No GitHub
workflow runs it. In the sandbox one case skips because it needs root (see
Skips below).

**Isolation.**

- `TestMain` calls `sandboxguard.Require()` (the
  [sandbox guard](#sandbox-guard-and-the-ci-bypass), same pattern as
  `test/reboot-recovery/main_test.go`). It records tmux's absolute path, and
  when tmux is not on `PATH` every test skips.
- `newRealTmux(t)` gives each test (or subtest) a fresh private world. It
  makes a short directory under the temp base (`rt.Dir`, a real path, short
  because Unix socket paths are limited). It sets `TMUX_TMPDIR` to that
  directory and unsets `TMUX` and `TMUX_PANE`, restoring them afterwards.
  It makes `rt.UserDir` (`tmux-<uid>`, 0700) and sets `rt.Socket`
  (`UserDir/default`). It uses `t.Setenv`, so these tests never call
  `t.Parallel`.
- Every helper passes `-S` with a socket under `rt.Dir`. No test starts,
  kills or depends on a server at the default socket path, because
  `test/reboot-recovery` owns the default server and may run at the same
  time. `TestSocketFallbackToTmpMatchesTmux` runs only a read-only
  `list-sessions` with no `-S` at the default path. That call may make tmux
  create `/tmp/tmux-<uid>`.
- Cleanup is automatic through `t.Cleanup` and also runs when a test fails.
  It restores owner modes under `rt.Dir` without following symlinks. It
  sends `kill-server` to every tracked socket and every socket file found
  under `rt.Dir`. It waits for the tracked server and pane processes, which
  are identified by pid and start time so a reused pid is never signalled.
  It SIGKILLs any survivors and reports any still alive. Last, it removes
  `rt.Dir`. It never touches anything outside `rt.Dir`.

**Skips.** Two cases skip on purpose. `TestPermissionDeniedEveryCall` skips
as root, because root's access ignores the socket's mode 000. The sandbox
runs as a non-root user, so it runs there. The foreign-owned per-user
directory case of `TestSocketUnsafeUserDirRefused` needs root to `chown`,
so it skips in the sandbox. That is the run's one skip.

**Case groups** (one file each):

- `replies_test.go`: the lookup's and pane listing's typed failure and reply
  for no server, no socket, a regular file at the socket path, an ended or
  SIGKILLed server and a missing directory; `duplicate session`; the create
  reply's five fields; the streams of successful calls (the pane listing's
  six fields, the created pane carrying its label); the capture answer.
- `permission_test.go`: a socket at mode 000 gives `FailSocketDenied` with
  the socket path on every call kind.
- `labels_test.go`: a created session carries `ad1 <token> <its own $N>
  <id> <store id>` for every valid id shape (`#` included), both detached and from
  inside another labelled session's pane, whose label stays unchanged; a
  `$`-bearing name gets no chain and is labelled only by id. The
  `TestLabelStoreIDLast` group: an instance id with spaces, or ending in a
  16-hex word, round-trips through the production lookup with its store
  id, both chained and by id.
- `pane_label_test.go` (SR-2.1, SR-20.7): the pane label `@ad_pane`. The
  chained create sets `<token> <its own %N>` on the new pane only, and
  `ListPanes` gives that pane `AdPane` = token; a pane split from it reads
  empty. A `$`-bearing name gets no chain, and the label by id with the
  reply's ids sets both labels, leaving other sessions' panes unchanged.
  The label by id with an unknown pane id fails with `no such pane: …`
  (`reply/no-such-pane`) but has already relabelled the session; with an
  unknown session id the pane step never runs. A value set at window,
  session, global or global-window scope never counts on a pane without its
  own. With `base-index` and `pane-base-index` 1 the created pane is still
  found by its token. Server scope is the exception to "own value wins":
  a server-scope `@ad_pane` (`set-option -s`) replaces every pane's own
  value in the format, so every pane lists that value and only the pane it
  names could count; the guard then finds no other pane (it fails safe,
  never matching a wrong pane).
- `socket_resolution_test.go`: RN-5. `tmux.ResolveSocket` equals tmux's own
  `#{socket_path}`; a `TMUX_TMPDIR` that is unset or names a missing path
  (a value containing `:` included) falls back to `/tmp` as tmux does
  (RN-5 R1 and R3b), checked with read-only calls only; unsafe per-user
  directories are refused; and a `-S` server in a directory agent-director
  made is reached later without `-S`.
- `clean_client_test.go`: a server started by the production create holds
  no `AGENT_DIRECTOR_*` variable in its global environment.
- `locale_test.go`: `ü-x` and `agent-ü1` compare exactly under `LC_ALL=C`
  and with no locale variables, and the client adds no locale variable to
  the server.
- `id_targets_test.go` (Appendix E.9 T2a, I1): a `$N` or `%N` target reaches
  only its own session or pane. It never reaches a session named like the id
  (the catalogue's `$7` and `%9` stored names), nor a replacement of an ended
  session. Ids only grow within one server and restart at `$0` and `%0` on a
  new server.
- `send_keys_test.go` (SR-2.1 "Text" row, SR-20.7): texts starting with `-`
  (`-x`, `--`, `-l`) and texts holding a `;` (a final `;` included, which
  the client sends escaped) go through the production `SendKeysPane` with
  Enter into a pane running `cat -` and arrive literally.

**Helpers** (package-level identifiers in the `_test.go` files; each doc
comment has the detail):

- World: `newRealTmux`, `rt.Dir` / `rt.UserDir` / `rt.Socket`,
  `rt.at(t, path)` (the same world bound to another socket under `rt.Dir`),
  `rt.fresh(t)` (a new unused socket), `unsetEnv(t, keys...)`,
  `rt.trackServer(pid)` / `rt.trackPane(pid)` (for processes not started
  through a helper).
- Raw tmux runner: `rt.run(t, args...)` returns a `rawResult` (`Stdout`,
  `Stderr`, `Exit`). `rt.must(t, args...)` returns stdout and fails unless
  the exit is 0 with empty stderr. Both run `tmux -u -S <socket>` with the
  process environment minus every `AGENT_DIRECTOR_*` variable, bounded to
  30 s. To change that, start from the builder `rt.raw()` and chain
  `.withoutS()`, `.withoutU()`, `.withEnv("K=V", ...)`, `.withoutEnv(keys...)`,
  `.bareEnv()` (`env -i`) or `.inDir(dir)`, then call `.run`, `.must` or
  `.startSession`. With `.withoutS()`, a command that starts or kills a
  server is refused unless tmux would resolve its socket under `rt.Dir`.
  Read helpers: `rt.format(t, target, "#{...}")`, `rt.formatInt` and
  `rt.label(t, sessionID)` (the raw `@ad_owner` value, which is never
  printed). On tmux 3.3a, a bare session-name target `=<name>` gives empty
  pane fields; use `=<name>:` to get the active pane.
- Production client: `newClient()` (tmux on `PATH`, `clientTimeouts`),
  `newClientWith(binary, timeouts)`, and `newRecordingClient(t)`, which
  returns the client and a `callLog` (`calls`, `last`) of every call's
  argv, streams and exit status. The recorder wraps tmux in a `/bin/sh`
  script, so environment-sensitive tests (clean client, locale) use the
  plain `newClient()`.
- Creates and starters: `rt.create(t, createSpec{...})` and `rt.mustCreate`
  run the production `NewSession` on the bound socket, returning `created`.
  Zero fields default to a unique name, `rt.Dir`, `stubCommand()`, a fresh
  token, a UUID-suffixed id and `newClient()`. `rt.startSession(t, name)`
  starts a raw session with no label and no `-e`.
  `rt.startSessionWithID(t, name, id)` adds
  `-e AGENT_DIRECTOR_INSTANCE_ID=<id>`. Both return `rawSession` (`ID`,
  `PaneID`, `PanePID`, `ServerPID`) and track what they start.
  Generators: `stubCommand`, `newToken`, `newInstanceID`, `uniqueName`.
- Assertions: `assertTriple(t, got, entry)` checks stdout, stderr and exit
  against a catalogue entry. `assertCallError(t, err, call, entry)` checks
  the entry's typed `Want` outcome for that call. `describe(err)` and
  `redact(s)` hide every label value in failure messages.
- Polling and processes: `waitFor(t, msg, cond, observe)` (`pollBudget`
  10 s), `waitForObserved` (own budget), `pidGone`, `waitPidGone`,
  `procState`, `procEnviron`, `procCmdline` and `envValue`. Waits poll
  every `pollInterval` and never sleep a fixed time.

**Must use:** later real-tmux tests (Epic 5 Task 4, then the verb Epics
per SR-20.7) add their cases to `test/realtmux`. They reuse these helpers
and never shell out to tmux themselves, never pass a socket outside
`rt.Dir`, and never spell a reply wording: it comes from the catalogue.
New helpers go in a `harness_*_test.go` file other than `harness_test.go`.

### apitest `[tmux]` config writer (reusable test fixture)

`pkg/api/apitest.WriteTmuxConfig(t, path, settings...)` is the ONLY way
`pkg/api`, CLI and MCP tests write `[tmux]` settings. Build each setting
with `TmuxInt(k, v)`, or `TmuxFloat` / `TmuxString` / `TmuxBool` for
malformed-value cases, keyed by a `config.TmuxKey`; iterate every key
with `config.TmuxKeys()` (table order).

- Outside `internal/config` and its own tests, no test spells a `[tmux]`
  key string or writes `[tmux]` TOML by hand. Key names come from
  `k.Name()`; defaults and minimums come from the `internal/config`
  constants (`Default*` / `Min*`) or `config.PendingGraceMinimumSeconds`,
  never from restated literals.
- The config file goes under a throwaway `HOME` or `t.TempDir()`, never
  the real `~/.agent-director`.

Why: key names and defaults are defined once in `internal/config`, so a
rename or default change breaks the build (or fails the test loudly)
instead of letting a test silently write a key the loader no longer
reads.

### apitest Seed* factory contract (reusable test fixtures)

`pkg/api/apitest` is how tests seed spawn rows and read the v5 columns no
verb shows (SR-20.2, SR-20.3). The package doc comment (`doc.go`, "#
Schema-v5 seeding and store reads") and each option's doc comment say the
same. For writing `[tmux]` settings, see the config writer subsection above.

**Signature.**

```go
func SeedSpawn(dbPath, id, state, cwd, relayMode, sessionID string, createStore bool, opts ...SpawnOption) (string, error)
```

The options are variadic and trailing, so positional call sites compile
unchanged. When two options set the same column, the later one wins.

**Options, by group:**

- Identity/name: `WithTmuxSessionName(name)`: stored exactly as given.
- Timestamps: `WithStartedAt(at)` and `WithEndedAt(at)`. Each takes either
  a `time.Time`, written as UTC `2006-01-02 15:04:05`, or a `string`, stored
  byte for byte (for unparseable text).
- Launch start:
  - `WithLaunchStartedAt(ms int64)`: epoch milliseconds.
  - `WithRawLaunchStartedAt(raw any)`: stored as bound. INTEGER affinity
    still turns integer-looking text into an integer.
  - `WithNoLaunchStartedAt()`: NULL.
- Life and pre-trust:
  - `WithLifeNumber(life int64)`.
  - `WithNoPreTrust()`: stores 1.
  - `WithRawNoPreTrust(raw any)`: stored as bound.
- Launch identity:
  - `WithLaunchIdentity(id store.LaunchIdentity)`: sets all eight
    columns, replacing the live-row pane default. A zero field is stored as
    NULL, so `LaunchIdentity{}` gives no token, socket, pane or identity.
    The token is stored as given, so a malformed token is expressible.
  - `WithNoLaunchToken()`: seeds a row from before the release. The launch
    token, the socket and the whole launch identity, the pane default
    included, are NULL, the same as
    `WithLaunchIdentity(store.LaunchIdentity{})`. For a row with no token but
    a socket, use `WithLaunchIdentity(store.LaunchIdentity{Socket: TestSocket})`.
- Raw JSON columns: `WithRawLabels(text)`, `WithRawClaudeArgs(text)` and
  `WithRawExtraEnv(text)`, each stored byte for byte. `WithRawExtraEnv` and
  `WithExtraEnv` write the same column.
- Session history: `WithSessionHistory(entry SessionHistorySeed)` seeds one
  archived `session_history` entry per use: `SessionID` (it may equal the
  row's current session id), `JSONLPath` (empty stores NULL), the `Life` it
  belongs to (need not be the row's life) and an optional `RecordedAt`
  (stored as UTC whole seconds; zero takes the store's default, the current
  time). Two entries with the same session id for one row are a `SeedSpawn`
  error, never an overwrite. Without the option no history is seeded; history
  is otherwise seeded only through the hook path (a session rotation).
- Existing v3 options: `WithPID`, `WithProcStarttime` (use
  `LinuxProcStarttime` / `DarwinProcStarttime`), `WithJsonlPath`,
  `WithExtraEnv`, `WithLivenessUnverifiedSince`, `WithLivenessNote`.

**SR-20.3 defaults**, for every column no option names:

- A `pending` row's launch start equals its final `started_at` (after
  `WithStartedAt`) in milliseconds, at whole-second precision. So a `pending`
  row seeded with an old `WithStartedAt` is already past the grace period. If
  `started_at` does not parse as a time, there is no launch start. A row in
  any other state has none.
- Every row gets a well-formed launch token (16 lowercase hex, from
  `crypto/rand`, distinct per row) and the test socket `apitest.TestSocket`
  (defined once in the leaf `internal/testsupport/launchfix`, never derived
  from the environment).
- Pre-trust is allowed (`no_pre_trust` 0) and the life is 0.
- The tmux server identity columns are NULL.
- A live row (a state for which `store.IsLiveState` holds: `pending`,
  `waiting`, `working`, `ask_user`, `check_permission`) gets the default
  pane `apitest.TestPaneID` (`%42`) with pid `apitest.TestPanePID`
  (4194305, above Linux's `PID_MAX_LIMIT`, so no start-time reader finds
  it) and no pane start time. Both constants are defined once in the leaf
  `internal/testsupport/launchfix`. The default is the pane only; the
  server identity stays NULL. A terminal-state row has no pane (NULL).
  `WithLaunchIdentity` and `WithNoLaunchToken` override it. Every live row
  gets the same pane, so two live rows on one socket that both need a
  Recorder session must set distinct panes with `WithLaunchIdentity`.

The options and defaults are applied by apitest-internal SQL in one
transaction, after the store's `InsertPending` / `RecordSessionStartIdentity` /
`ApplyHookTransition` sequence: one UPDATE writes the options and defaults;
for a `pending` row with no launch-start option a second UPDATE sets the
default launch start; then each `WithSessionHistory` entry is INSERTed into
`session_history` in seeding order, so with no stated `RecordedAt` a
later-seeded entry reads as newer. None of these statements advances
`row_version`.

**Seeding and `row_version`.** The store calls in that sequence are versioned
writes (see `internal/store`, "Versioned writes"): `InsertPending` starts at
0, and `RecordSessionStartIdentity` (when given a session id) and
`ApplyHookTransition` (for a state other than `pending`) each add one. The
option/default UPDATEs and the apitest and storefix backdating fixtures add
nothing. So a `pending` row seeded with no session id is at 0, like a fresh
insert, and every other seed starts above 0. Tests assert version deltas
(after minus before, both read through `ReadSpawnColumns`), never absolute
values, except for a row the test inserted itself.

**Read helpers:**

- `ReadSpawnColumns(dbPath, instanceID) (SpawnColumns, error)`
  - Returns every `spawns` column of one row raw, as `any`: nil for NULL,
    otherwise the stored storage class (`int64`, `float64`, `string` or
    `[]byte`). TIMESTAMP columns come back as stored text.
  - Decodes nothing, so it works on rows `GetSpawn` cannot decode.
  - A missing row returns an error wrapping `store.ErrSpawnNotFound`. It
    never creates a store file.
- `ReadSessionHistoryAllLives(dbPath, instanceID) ([]HistoryEntry, error)`
  - Returns the id's `session_history` entries from every life:
    `ClaudeSessionID`, `JSONLPath` (`sql.NullString`), `LifeNumber` and
    `RecordedAt` (stored text).
  - Order is newest `recorded_at` first, ties broken by the most recently
    inserted. No entries gives an empty non-nil slice.
  - It reads the table directly, never through the store's history read.

**Store-id helpers** (`pkg/api/apitest/storeid.go`):

- `ReadStoreID(dbPath) (string, error)`
  - Returns `store_meta`'s `store_id` raw, through a direct connection. It
    does not check the value's form, so it also reads a hand-edited value.
  - A missing `store_meta` table or `store_id` row returns an error wrapping
    `apitest.ErrNoStoreID`. It never creates a store file.
  - A test that holds a `*store.Store` uses its `StoreID()` instead: the
    value read when that store opened.
- `SeedStoreID(dbPath, id) error`
  - Overwrites the store's id with `id`, so a test can build this store's
    labels deterministically. Test-only; no production path changes the id.
  - Rejects an `id` that is not 16 lowercase hex and writes nothing. A store
    with no `store_id` row or no `store_meta` table gives an error wrapping
    `ErrNoStoreID`.
  - A store opened before the call keeps the id it read, so seed before
    opening the client or store under test.
- `OtherStoreID(id) string`
  - Returns a well-formed id that always differs from `id`, for another
    store's labels. It is deterministic: for a well-formed id it changes the
    last hex digit to the next one (`f` wraps to `0`); for any other input it
    returns `0000000000000000`.

**Row-to-Recorder session helper:**

- `(*tmuxfix.Recorder).SeedRowSession(t, dbPath, instanceID, opts...) SeedSession`
  (package `internal/testsupport/tmuxfix`) makes the Recorder hold the
  seeded row's own session, read from the store:
  - on the row's socket, starting a server with the row's recorded server
    identity when the socket has none;
  - with the row's pane (a new pane when the row records none), whose
    `AdPane` is the session label's token when that label is
    `LabelValid`, else `""`, as the create leaves it (so an old
    `OtherToken` label gets a matching pane label);
  - named by the stored form of the row's session name;
  - labelled valid for the row's id, its stored launch token and the
    store's id (`StoreID()` on the store it opens to read the row);
  - created at the bound clock's current second (`WithVirtualTime`), else
    the wall clock's.
- Options (`RowSessionOption`): `WithRowSessionCreated(epoch)` for another
  creation time; `WithRowSessionName(stored)` for another stored name;
  `WithRowSessionLabel(label, set)` for another label: no label
  (`LabelNone`, `set` false), an old one (`Valid(OtherToken, id, storeID)`),
  a foreign one (`Valid(token, other id, storeID)`), another store's
  (`Valid(token, id, apitest.OtherStoreID(storeID))`), or a borrowed or malformed value
  (`LabelNone`, `set` true, or a `LabelShape`'s `Want`).
- The test fails when the row cannot be read, records no socket, records no
  well-formed token and no label option is given, or its pane is already on
  that server.

**Rules (must use):**

- New tests contain no inline SQL. They seed rows through `SeedSpawn` and
  its options, `OpenStoreWithRow`, `SeedExpireFixture` or the storefix
  seeders.
- They read columns that `status`/`get`/`list` do not expose through
  `ReadSpawnColumns`, and history through `ReadSessionHistoryAllLives`.
- They seed history only through `WithSessionHistory` or the hook path (a
  session rotation), never by writing `session_history`.
- A label of this store ends with its id, taken from `ReadStoreID`, the
  store's `StoreID()` or an id pinned with `SeedStoreID`; another store's
  labels use `OtherStoreID`. No test writes `store_meta` any other way.
- A test that needs a seeded row's own session in the Recorder uses
  `tmuxfix.Recorder.SeedRowSession`. It never reads a row's launch token or
  spells one by hand (SR-20.2).
- Concrete-store write failures come only from `storefix.InjectWriteFailure`
  or its white-box counterpart in `internal/store/migration_fixtures_test.go`
  (see "storefix seeders" above). Writes behind a store interface fail
  through a failing wrapper of that interface.

### ts-helper wrapper CLI

`test/smoke/ts-helper/` is a small Go binary compiled exclusively with the
`helper` build tag (`go build -tags helper`).  It bridges Go fixture-seed
helpers into the TypeScript smoke test suite so that Bun-side tests can shell
out to it for store / template setup without reimplementing the seeding logic
in TypeScript.

**Why a subprocess instead of reusing the apitest package directly?**

The existing `pkg/api/apitest` helpers all accept `*testing.T` and are
designed for Go-internal use.  Bun's test runner cannot call Go functions
in-process.  A thin CLI wrapper is the minimal seam that avoids duplicating
store-schema knowledge and state-machine details in a second language.

**Build tag isolation.**

`pkg/api/export_for_helper.go` carries `//go:build helper` and lives in
package `api`.  It exposes `HelperSeedSpawn`, `HelperSeedParentChild`,
`HelperSeedPermissionRequest`, `HelperSeedTemplate`, and `HelperInitStore` —
functions that open the SQLite store directly (via `internal/store`) and write
fixture rows without going through a `pkg/api.Client`.  Because the file is
excluded from all non-helper builds, the production `agent-director` binary and
`go test ./...` (no tag) are completely unaffected.

**Subcommand contract.**

Every subcommand follows the same I/O contract:

| Outcome | stdout | stderr | exit code |
| --- | --- | --- | --- |
| success | exactly one line of JSON | empty | 0 |
| failure | empty | error message | 1 |

The single-line JSON guarantee means Bun tests can parse results with
`JSON.parse(stdout.trim())` without worrying about multi-line output.  stderr
emptiness on success means a non-empty stderr is an unambiguous failure signal.

**Available subcommands.**

| Subcommand | Key flags | Result shape |
| --- | --- | --- |
| `seed-spawn` | `--store`, `--state`, `--id`, `--cwd`, `--create-store` | `{"claude_instance_id": "..."}` |
| `seed-parent-child` | `--store`, `--parent-id`, `--child-id` | `{"parent_id": "...", "child_id": "..."}` |
| `seed-permission-request` | `--store`, `--spawn-id`, `--tool` | `{"request_id": <number>}` |
| `seed-template` | `--templates-dir`, `--name`, `--body` | `{"path": "..."}` |
| `seed-empty-store` | `--store` | `{"path": "..."}` |
| `json-schema` | — | machine-readable result-shape map for all subcommands |

**Makefile target.**

`make ts-helper` builds `bin/ts-helper`.  The target lists every
`.go` source under `test/smoke/ts-helper/` and
`pkg/api/export_for_helper.go` as prerequisites so Make's mtime tracking
makes subsequent runs no-ops when nothing has changed.

**Bun integration — `TS_HELPER_PATH`.**

`pkg/ts-bun-client/bunfig.toml` registers `./test/setup.ts` as a Bun preload:

```toml
[test]
preload = ["./test/setup.ts"]
```

`test/setup.ts` runs `make ts-helper` synchronously (via `Bun.spawnSync`)
before any test starts.  On success it sets `process.env.TS_HELPER_PATH` to
the absolute path of `bin/ts-helper`.  On failure it prints a clear message
and calls `process.exit(1)` so the whole test run aborts immediately.

Because the make target is incremental, a cached build adds only a few
milliseconds of overhead to the suite.  Individual smoke tests can retrieve
the path with `process.env.TS_HELPER_PATH` and shell out to specific
subcommands.

### TS smoke-test harness

`pkg/ts-bun-client/test/` contains the Bun smoke-test suite for every
callable verb. It exercises `pkg/ts-bun-client/src/` end-to-end by
spawning the real CLI binary per verb call.

**Directory layout.**

```
test/
  setup.ts                   # Bun preload: builds ts-helper + fake-tmux
  smoke-invariants.test.ts   # Meta-test enforcing coverage contracts
  smoke/
    spawn.test.ts            # One file per verb (happy + error path)
    status.test.ts
    get.test.ts
    get-permission.test.ts
    send-keys.test.ts
    read-pane.test.ts
    kill.test.ts
    decide.test.ts
    resume.test.ts
    find-missing.test.ts
    expire.test.ts
    delete.test.ts
    make-template.test.ts
    list.test.ts
    pause.test.ts
    version.test.ts
  internal/
    tempHome.ts              # withTempHome() helper
    helper.ts                # runHelper() wrapper for ts-helper subprocess
```

**`withTempHome` helper.**

`test/internal/tempHome.ts` exports `withTempHome(testFn)`.  It creates a fresh
`mkdtemp` directory, sets `process.env.HOME` and `process.env.PATH` in the main
thread, runs `testFn(homeDir)`, then restores env and cleans up the temp dir (on
success) or preserves it (on failure, for inspection).

**Subprocess environment isolation caveat.** Each verb call spawns the
CLI binary in a fresh subprocess. By default the subprocess inherits
the parent's process environment, but `withTempHome` only sets
`process.env` in the main test thread — the CLI subprocess sees the
modified environment at spawn time, but verbs that resolve paths
relative to `HOME` (Go's `os.UserHomeDir()`) read the same value the
parent's `HOME` env var points at. Smoke tests use the explicit
`Client` options where possible:

1. **tmux binary** — verbs that invoke tmux (`spawn`, `send-keys`, `read-pane`,
   `resume`) pass `tmuxCommand: <path-to-fake-tmux>` explicitly to the
   `Client` constructor so the CLI subprocess uses the fake-tmux stub
   rather than the real binary on PATH.

2. **HOME-relative paths** — `make-template` writes to
   `~/.agent-director/templates/` using Go's `os.UserHomeDir()` inside
   the CLI subprocess. Tests that check `result.path` assert the
   filename suffix only and delete the file in a `finally` block.

3. **JSONL paths** — `resume`'s pre-flight `os.Stat` for the JSONL
   transcript resolves under the subprocess's inherited HOME. Resume
   tests create the JSONL placeholder at
   `${HOME}/.claude/projects/${slug(cwd)}/${sessionId}.jsonl` and
   delete it in a `finally` block.

**`AGENT_DIRECTOR_INSTANCE_ID` and the FK constraint.**

When tests run inside a live Claude session the environment variable
`AGENT_DIRECTOR_INSTANCE_ID` is set to the session's UUID. The CLI
subprocess inherits this value and passes it as `parent_id` on every
`InsertPending` and `SetParentID` call. Because the test stores are
fresh SQLite files that do not contain a row for the session UUID, the
FOREIGN KEY constraint fails.

Any smoke test that exercises a verb that writes `parent_id` (`spawn`,
`resume`) pre-seeds a row with `id = process.env.AGENT_DIRECTOR_INSTANCE_ID`
in the same store before calling the verb. This satisfies the FK
constraint without altering the subprocess's inherited environment.

**fake-tmux stub.**

`test/fake-tmux` is the subprocess tmux double (see [test/fake-tmux: the
subprocess tmux double](#testfake-tmux-the-subprocess-tmux-double-reusable-test-fixture)).
The smoke tests' verbs still send the legacy name-based argv, which the fake
accepts and answers with exit 0 (`has-session` exits 1), with no live tmux
session needed.  For `capture-pane` it writes a fixed stub string to stdout so
`read-pane` tests can assert the return value is non-empty.  The binary is built
by `make fake-tmux` (which `test/setup.ts` calls before any test runs).  Both
the Makefile recipe and `setup.ts` explicitly `chmod 755` the output: without the
execute bit, `exec.LookPath` silently skips the stub and falls through to the
real `/usr/bin/tmux`, leaking actual tmux sessions into the test environment.

**`runHelper` wrapper.**

`test/internal/helper.ts` exports `runHelper(subcommand, args)`.  It shells out
to `bin/ts-helper` via `Bun.spawnSync`, throws on non-zero exit, and returns
parsed JSON from stdout.  Smoke tests use it to seed SQLite rows and
filesystem fixtures that would otherwise require duplicating Go's state-machine
logic in TypeScript.

**`smoke-invariants.test.ts` meta-test.**

Three static assertions are enforced by grepping test file contents:

| Assert | Rule |
| --- | --- |
| (a) | Every verb in `src/internal/verbs.ts::VERBS` has a `test/smoke/<verb>.test.ts` file. |
| (b) | Every smoke file imports `withTempHome` and calls it. |
| (c) | Every smoke file outside the allow-list contains `instanceof Err` or `toBeInstanceOf(Err`. |

Allow-list for (c): `version`, `expire`, `delete`, `find-missing` — verbs whose
manifests declare no verb-level ErrorNames (errors surface in result maps or are
untriggerable on Linux).

**Gate — TS client.** The `bun test` suite for `pkg/ts-bun-client/` is gated at release time, not on every PR. It runs locally as part of the `coverage` phase in the `/release` skill, against the in-tree source (`cd "$REPO_ROOT/pkg/ts-bun-client" && bun install --frozen-lockfile && bun test`) — distinct from the packed-tarball smoke that precedes it. GitHub Actions are reserved for narrower checks (go-smoke, integration, mac pre-release verify); release-blocking gates run locally so they execute against exactly the tree being tagged.

**Gate — Go smoke.** The `.github/workflows/go-smoke.yml` workflow runs `go test -race -count=1 -v ./test/smoke/go/...` on `ubuntu-latest` (linux/amd64) on every pull request and push to `main`. Cross-platform extension to macOS and Windows is Epic 6.

### Sandbox guard and the CI bypass

`internal/testsupport/sandboxguard` is the **reusable** fail-fast gate every
state- or exec-touching Go test package calls from its `TestMain`. Do not
hand-roll an equivalent check: call `sandboxguard.Require()`. It is test-only and
absent from the production code graph.

`Require()` proceeds when **either** of two env vars is set, and refuses
otherwise (fail-closed, exit 1 with a message naming both):

| Const | Variable | Meaning |
| --- | --- | --- |
| `sandboxguard.EnvVar` | `AGENT_DIRECTOR_TEST_SANDBOX` | The process is inside the sandbox container. Exported by the `make sandbox*` targets; also checked by the bun preload `pkg/ts-bun-client/test/setup.ts`. |
| `sandboxguard.BypassEnvVar` | `BYPASS_CONTAINER_FOR_AGENT_DIRECTOR_TESTS` | The caller asserts there is **no real `~/.agent-director` to damage** — true only on an ephemeral GitHub-hosted runner. Go-only. |

The guard defends against b.8dr: the store resolves `~` via `user.Current()`
(`internal/store.expandTilde`), not `$HOME`, so a host-side `go test` can rewrite
the real store no matter how `HOME` is set. The container is the only isolation
boundary; the bypass is not isolation, it is an assertion that there is nothing
to isolate from.

The bypass is set **only at the `job:`/`step:` level** of the two
GitHub-hosted-runner workflows (`go-smoke.yml`, `integration.yml`), each with an
in-file comment recording why it is safe there. It must never be a
repository/organization-level Actions variable or secret, and never appear in
`.github/workflows/pre-release-verify-mac.yml` — that job runs on
`[self-hosted, macOS, ARM64]`, a persistent machine that plausibly holds a real
store. `make check-sandbox-bypass` (run by the doc-drift workflow) enforces the
latter by grep; it also fails if the mac workflow file is missing, so the check
cannot pass vacuously. Deliberately **not** implemented: a store-presence probe
or any `CI` / `GITHUB_ACTIONS` / `$HOME`-derived discriminator — ambient signals
would also disable the guard on the self-hosted runner (b.175).

### TS envelope-diff regression

`pkg/ts-bun-client/test/envelope-diff.test.ts` is the TypeScript counterpart to
Epic 3's Go-side envelope-diff harness. Both suites run the same 16 callable
verbs against identical SQLite fixtures and assert that the CLI subprocess
output and the Bun Client wrapper output are structurally identical —
catching any divergence introduced by argv construction, JSON parameter
marshalling, or stdout-envelope deserialization in the TS Client.

**How it works.**

For each verb the test:

1. Seeds a single SQLite store using `bin/ts-helper` (the same fixture seeders
   used by the smoke tests).
2. Copies the seeded store byte-for-byte to two temp-home directories (`homeA`
   and `homeB`) so that both sides start from an identical on-disk state —
   including timestamps that would otherwise diverge if seeded independently.
3. Runs the CLI subprocess via `runCli(args, cliEnv(homeA))` and captures
   stdout/stderr.
4. Calls the Bun Client wrapper with equivalent parameters, pointing its
   `storePath` at `homeB/.agent-director/state.db`.
5. Calls `assertEnvelopesEqual(cliResult, tsResult, { ignorePaths })` where
   `ignorePaths` comes from `loadIgnorePathsForVerb(verb)`.

Each describe block contains a **success path** test and (for verbs that expose
verb-level errors) an **error path** test.

**Reuse of `nondeterministic.json`.**

`test/internal/loadIgnorePaths.ts` reads Epic 3's
`test/envelope-diff/nondeterministic.json` at module-load time and caches it.
`loadIgnorePathsForVerb(verb)` returns the selector array for that verb; callers
pass it directly as `ignorePaths` to `assertEnvelopesEqual`.  This keeps both
suites synchronized: adding a selector to the JSON file silences the field in
both the Go and TS diffs simultaneously.

**`assertEnvelopesEqual` (structuralDiff.ts).**

`test/internal/structuralDiff.ts` implements a recursive structural diff using
the same dot-bracket path notation as Epic 3's `diff.go`:

- Field access: `.foo`, `.foo.bar`
- Array index: `.arr[0]`
- Wildcard selector (in ignore list only): `.arr[*]` matches any `.arr[N]`

On any mismatch all divergences are collected and thrown together so a single
test run surfaces every problem at once.

**`runCli` (cliRunner.ts).**

`test/internal/cliRunner.ts` exports `runCli(args, env)`.  It spawns
`bin/agent-director` via `Bun.spawnSync`, captures stdout and stderr as strings,
and returns `{ stdout, stderr, exitCode }`.  The CLI binary path defaults to
`process.env.CLI_PATH` (set by `test/setup.ts`) or is resolved relative to the
repo root.

**fake-tmux and PATH.**

Both the side-by-side CLI subprocess (`runCli`) and the Client's
per-verb CLI subprocesses must resolve `tmux` to the fake stub. For
`runCli`, the `cliEnv` object prepends `FAKE_TMUX_DIR` to `PATH`. For
the Client side, tests pass `tmuxCommand: FAKE_TMUX_BIN` explicitly to
the `Client` constructor (the same pattern used in smoke tests) so the
spawned CLI uses the stub regardless of its inherited PATH.

**Meta-test (`envelope-diff-invariants.test.ts`).**

Five static assertions are enforced by grepping file contents — no test
execution required, completes in under 1 second:

| Assert | Rule |
| --- | --- |
| (a) | Every verb in `VERBS` has a `describe("verb", ...)` block in `envelope-diff.test.ts`. |
| (b) | Every verb has a `"success path"` test. |
| (c) | Every verb outside the allow-list has an `"error path"` test inside its describe block. |
| (d) | `nondeterministic.json` contains an entry for every verb in `VERBS`. |
| (e) | `assertEnvelopesEqual` call count equals `loadIgnorePathsForVerb` call count. |

Allow-list for (c): `version`, `expire`, `delete`, `find-missing`.

**`make envelope-diff-ts`.**

The Makefile target builds all three required binaries incrementally, then
runs the two test files:

```
envelope-diff-ts: agent-director ts-helper fake-tmux
    cd pkg/ts-bun-client && bun test test/envelope-diff.test.ts test/envelope-diff-invariants.test.ts
```

It is wired into `make test` so `go test ./...` will not run unless the TS
envelope-diff suite passes first.
