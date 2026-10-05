# agent-director — Architecture

## What it is

A Go binary (plus an off-PATH operator tool, `agent-director-admin`) that:

- Spawns Claude Code instances inside tmux sessions.
- Hooks into those Claude sessions (via Claude Code's hooks mechanism) to track state, capture transcripts, and relay events.
- Exposes a CLI for humans and a stdio MCP server for LLM callers — both implemented by the same binary in different modes.

## Surfaces

- **CLI** — `agent-director <verb> ...` for every verb in
  `pkg/api/manifest`. See `docs/cli-reference.md` for the canonical
  list.
- **Hook entrypoint** — the same binary invoked by Claude Code on hook events (SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, Stop, Notification, SessionEnd, PermissionRequest).
- **Stdio MCP server** — same binary invoked as `agent-director serve --stdio`. Stdio transport, lifetime scoped to a single Claude Code session.
- **Operator tool** — a second binary, `agent-director-admin`, for the two
  operator-only actions (the finished-row kill, `kill-finished`, and
  `delete`). It is not an agent surface: install.sh puts it at
  `~/.agent-director/admin/agent-director-admin`, never on PATH, and no
  manifest text, `help`, MCP tool or client library names it. See
  [Operator tool `agent-director-admin`](#operator-tool-agent-director-admin)
  and `docs/admin-reference.md`.

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

Each target gets two binaries, `agent-director` and `agent-director-admin`
(six in all). All are pure `CGO_ENABLED=0` cross-compiles (pure-Go
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
`cmd/agent-director`, `cmd/agent-director-admin`, `internal/mcp` and
`internal/clisetup` (the client setup both binaries share). No other
package in `internal/` imports `pkg/api`. `internal/adminapi` imports
nothing from it: `pkg/api` imports `internal/adminapi` and sets its hooks
in `init`. The verb registry
`pkg/api/manifest` is a separate stdlib-only leaf, not `pkg/api`:
`internal/mcp`, `internal/spawn` and `pkg/api` import it.

### Package inventory

| Path | Responsibility | Allowed imports | Prohibited imports |
| --- | --- | --- | --- |
| `cmd/agent-director` | Thin CLI shim: argv parser and JSON envelope marshaller. Constructs one `pkg/api.Client` at startup via `setupClient()`, a thin wrapper over `internal/clisetup.Open`, which `cmd/agent-director-admin` shares; every store-backed verb calls a method on that Client (`client.Spawn(params)`, `client.Status(id)`, etc.) — no business logic lives in `cmd/`. **DB-free exceptions:** `help`, `--help`, `version`, the no-verb run (no verb after the global flags, so a run with only global flags counts), and `trail-emit` are dispatched BEFORE `setupClient` so they never open or create a store (SR-4.1/4.2, b.8dr); help/version run against a zero-value `Client` and consult no store. The no-verb run prints help, except that when stdin is not a terminal and carries a hook payload (a Claude Code that does not run exec-form hooks, SR-22.9) it prints nothing, exits 0 and writes one `ad.hook.ignored` `no_exec_form`, still with no store and no config load; the trail file is the only thing it may create (`noVerbHookIgnored` in `noverb.go`, which reads stdin with a 1 MiB cap and a 1 s deadline and hands the bytes to `hook.HandleNoExecForm`; see [Hooks move a row only for its own agent](#hooks-move-a-row-only-for-its-own-agent)). `help`, `--help` and `version` never read stdin. **`runHook` exception:** retains independent `config.Load` + `store.Open` calls per SRD §3.2 fail-open; hook fires must never be blocked by Client-startup failures. `runHook` builds the `hook.HandleConfig`, wiring `Now: time.Now` and `PendingGrace: cfg.Tmux.EffectivePendingGrace()` (the grace bound of SessionStart's wait for its launch's identity write, SR-22.9, SR-13.4) beside the parent-process readers and the production `PollClock`. | stdlib; `pkg/api`; `pkg/api/errnames`; `internal/hook`; `internal/probe` (the hook's parent-process readers, `hookParentProc`, shared by `runHook` and the no-verb run); `golang.org/x/sys/unix` (the no-verb run's terminal check, `isTerminal`, with the per-OS `ioctlReadTermios` in `noverb_linux.go` / `noverb_darwin.go`); `internal/clisetup` (`setupClient`'s `Open`, and the global-flag parser `ParseGlobalFlags` / `GlobalFlags.Apply`, which `run()` calls directly; `cmd/agent-director` has no global-flag code of its own); `internal/config` in `runHook`, `newHookLogger` and `setupClient` (the `config.Config` it returns); `internal/store` in `runHook`. | Direct `database/sql` use; raw SQL strings; ad-hoc subprocess management; `store.Open` / `config.Load` / `tmux.New` outside `runHook` and `newHookLogger` (the Client's config load and logger bootstrap are `internal/clisetup.Open`'s). |
| `cmd/agent-director-admin` | The operator tool (b.vqr), a thin shim like `cmd/agent-director` with no business logic: verbs `kill-finished` (kill's finished-row opt-in), `delete`, `help` (also `--help`, `-h` and the no-verb run) and `version`, taken from `internal/adminapi.Verbs`, never from `pkg/api/manifest`. It parses and applies the main CLI's global flags (`--store-path`, `--home`, `--tmux-command`, before or after the verb) with `internal/clisetup`, opens the Client with `clisetup.Open` (the same store, config, logger and schema checks as `agent-director`), calls `adminapi.KillFinished` / `adminapi.Delete`, and prints JSON on stdout, or one `{err_name, err_description}` envelope on stderr with exit 1 (`errnames.Classify`). `help`, every verb's `--help` / `-h` and `version` open no store and load no config, and every help opens with `adminapi.ApprovalStatement`. See [Operator tool `agent-director-admin`](#operator-tool-agent-director-admin). | stdlib; `internal/adminapi`; `internal/clisetup`; `pkg/api` (`Client`, `Version`); `pkg/api/errnames`. | `pkg/api/manifest` (its verbs are not manifest verbs); direct `database/sql`; `store.Open` / `config.Load` / `tmux.New`; business logic. |
| `internal/adminapi` | The admin binary's door into `pkg/api` (b.vqr). Declares the hooks `KillFinished(c any, id) (KillResult, error)` and `Delete(c any, ids) (DeleteResult, error)` as function variables, which `pkg/api`'s `init` (`pkg/api/admin.go`) sets to the unexported `Client.killFinished` (`kill_optin.go`) and `Client.deleteRows` (`delete.go`); a `c` that is not a non-nil `*api.Client` is an error and nothing runs. Also holds the admin binary's own verb list (`Verbs`, `Lookup`), global-flag list (`GlobalFlags`, `GlobalFlagsText`) and `ApprovalStatement`, from which its help and the generated `docs/admin-reference.md` are built. Being under `internal/`, no other module can import it, so neither action has a public Go entry point. | stdlib only (it imports nothing). | `pkg/api` (`pkg/api` imports it: a cycle); `pkg/api/manifest`. |
| `internal/clisetup` | Client setup shared by both command binaries (b.vqr). `Open(Overrides)` builds the `pkg/api.Client` every store-backed CLI verb and admin verb uses (the design pins: `CreateIfMissing`, the store-path precedence, the recovery logger `NewRecoveryLogger`, the returned `config.Config`) and returns an `*OpenError` naming `ErrConfigMalformed`, `ErrSchemaMismatch`, `ErrSchemaMigrationRequired` or `ErrStoreOpen`. `globalflags.go` holds the only global-flag parser, the pre-scan `ParseGlobalFlags`, with `GlobalFlags.Apply` (`--home` sets HOME before any config load; `--store-path` and `--tmux-command` become `Overrides`) and `ExpandTilde`; `globalflags_test.go` tests them. **Must use:** a command binary opens its Client through `Open` and parses its global flags through `ParseGlobalFlags` / `Apply`; never a second setup or flag parser. | stdlib; `pkg/api`; `internal/config`; `internal/store` (error sentinels only). | `internal/mcp`; `cmd/*`; direct `database/sql`. |
| `pkg/api` | **Canonical verb-handler home and public surface.** Opaque `Client` facade — no exported fields, construction via `New` only. Owns all verb implementations, seam interfaces (`ListStore`, `PauseStore`, etc.), params/result types, and error sentinels (the seven tmux sentinels of SR-1.1 are all re-exported in `aliases.go`). Owns store, tmux, and config internally; exposes one method per CLI verb; idempotent `Close`. Consumed by `cmd/agent-director`, `internal/mcp`, `internal/clisetup` and `cmd/agent-director-admin`. **Operator-only actions (b.vqr):** the finished-row kill and delete are unexported (`Client.killFinished` in `kill_optin.go`, `Client.deleteRows` in `delete.go`) and reached only through the `internal/adminapi` hooks that `admin.go`'s `init` sets, so no exported method, type or field offers them. **`kill` seams** (`kill.go`): `KillStore` (`GetSpawn`, the adoption write `AdoptIdentityIfUnchanged`, `StoreID`; `*store.Store` satisfies it), `KillTmux` (`TmuxLookup`'s `Lookup` plus `ListPanes`, `KillPane`, `KillSessionID`; `TmuxClient` satisfies it) and the start-time reader `ProcChecker`; `Kill` also takes the three `[tmux]` durations, the clock and the sleep, and has no logger. **`find-missing` seams** (`find_missing.go`): `FindMissingStore` (the live-row read, the four same-life guarded writes, `CloseOrphanedPermissionRequests`, `ListProvisionalTranscripts`, `HealJsonlPath`, `StoreID`; `*store.Store` satisfies it), `FindMissingTmux` (`TmuxLookup`'s `Lookup` plus `ListPanes`) and `ProcChecker`; the exported `FindMissing` also takes the pending grace period, the sweep budget, the clock and a `FindMissingLogger` (see [`find-missing`](#find-missing)). **Pane-verb seams** (`readpane.go`, `sendkeys.go`, `pause.go`; see [Interact](#interact-send-keys--read-pane) and [`pause`](#pause)): `ReadPaneStore` (`GetSpawn`, `StoreID`; no write) and `ReadPaneTmux` (`TmuxLookup`'s `Lookup` plus `ListPanes`, `CapturePaneID`); `SendKeysStore` (`GetSpawn`, `PermissionRequestsForSpawn`, the adoption write `AdoptIdentityIfUnchanged`, `StoreID`) and `SendKeysTmux` (`Lookup`, `ListPanes`, `SendKeysPane`); `PauseStore` (`GetSpawn`, `GetSpawnState`, `AdoptIdentityIfUnchanged`, `StoreID`) and `PauseTmux` (`Lookup`, `ListPanes`, `SendKeyPane` for `pause`'s line clear, `C-u`, `SendKeysPane`). `*store.Store` and `TmuxClient` satisfy them. `SendKeys` and `Pause` take the start-time reader `ProcChecker`; the exported `ReadPane` uses `probe.NewProcChecker()` and `Client.ReadPane` the Client's reader. **tmux:** `TmuxClient` (the `Options.TmuxClient` injection point, SRD Appendix F.3) carries the nine socket-taking methods (`Lookup`, `ListPanes`, `KillPane`, `KillSessionID`, `SendKeysPane`, `SendKeyPane`, `CapturePaneID`, `NewSession`, `SetLabel`) beside the one name-based method left, `HasSession`, which is kept but no verb uses, and none may; the name-based send and capture are gone. `*tmux.Client` and `tmuxfix.Recorder` implement it. `tmux_aliases.go` re-exports the typed tmux API as `Tmux*` aliases (`TmuxLookupAnswer`, `TmuxSession`, `TmuxPane`, `TmuxLabel`, `TmuxCreateReply`, `TmuxCall`, `TmuxFailure`, `TmuxCallError`) and constants (`TmuxCall*`, `TmuxCallSendKey`, "key send", included; `TmuxFail*`, `TmuxLabelNone` / `TmuxLabelValid`), identical to the originals, so an external implementer never imports `internal/tmux`; it also re-exports the start-time reader interface as `ProcChecker` (`= tmux.ProcChecker`). The Client holds its clock (`time.Now`), its sleep (`time.Sleep`, the pause of `kill`'s process wait) and its start-time reader (`probe.NewProcChecker()`), all set in `New`; plain spawn uses the clock and reader for the launch start and the identity write, and `kill` uses all three for its lookup, adoption and process wait; `find-missing` measures the pending grace period on the same clock, with the value from `EffectivePendingGrace`. The label scan of a plain spawn lives in `spawn_scan.go`, its held-name path after "duplicate session" (the end write, one re-lookup, the classified error) in `spawn_held.go`, the shared held-name error builder in `held_name.go` and the one `ad.launch.name_held` emitter in `name_held_trail.go` (see [Launch identity](#launch-identity)). **`resume` seams** (`resume.go`): `ResumeStore` and `ResumeTmux` (`TmuxLookup`'s `Lookup` plus `NewSession`, `SetLabel` and `KillSessionID`; no pane listing, since `resume` adopts nothing, and no name-based method; `TmuxClient` satisfies it), with the start-time reader `ProcChecker`, the configuration, the store id, the clock and the logger. Its pre-launch lookup's decision lives in `resume_lookup.go` (`decidePreLaunch`), the launch outcome, restore and path after "duplicate session" it shares with reuse in `finished_launch.go` (`finishedLaunch`) and the shared starting-session refusal in `starting_session.go` (see [Resume](#resume) and [Starting-session rule](#starting-session-rule-starting_sessiongo)). **Reuse** (`spawn` with `ReuseFinished` and an explicit id whose row is finished; `spawn_reuse.go`): the unexported `reuseStore` (`ReadForReuse`, `ResetForReuse`, `RestoreAfterFailedReuse`, `RecordLaunchIdentity`; `*store.Store` satisfies it), injected through `runSpawnWithReuseStore` (`runSpawn` passes its store), and its own descriptions in `spawn_reuse_errors.go` (see [Reuse of a finished id](#reuse-of-a-finished-id)). **`expire`'s window parser** (`older_than.go`, b.hxn): `ParseOlderThan(s) (time.Duration, bool)` takes a Go duration or decimal digits followed by `d` for days, and rejects a value in neither form, a negative Go duration and a day count above `config.MaxExpireRetentionDays` (106751); `OlderThanForm` words the accepted form for the refusals and for MCP's `tools/list` (see [`expire`](#expire)). `api.New` builds the production client as `tmux.New(opts.TmuxCommand, tmuxTimeouts(cfg.Tmux))`, taking the timeouts and pipe-close wait from `EffectiveQueryTimeout`, `EffectiveActionTimeout`, `EffectiveCreateTimeout` and `EffectivePipeCloseWait`; an injected `Options.TmuxClient` is used as given and gets no timeouts. | stdlib; `internal/store`; `internal/config`; `internal/tmux`; `internal/probe`; `internal/spawn`; `internal/adminapi` (to set its hooks); `pkg/api/manifest` (the verb list for `help`, and `TmuxSessionNameSpelling` for the list hint). | Direct `database/sql`; raw SQL strings; MCP framing. |
| `internal/store` | Sole owner of the SQLite database file. Opens the DB, enforces file/dir permissions, manages schema (v5; see "Schema v5" below), exposes typed CRUD primitives (added in later Tasks). | stdlib (`database/sql`, `os`, `path/filepath`, `errors`, etc.); `modernc.org/sqlite` for the driver side-effect import. | `pkg/api`; `internal/config`; `cmd/*`; any package outside this one. The dependency arrow points *into* `store`, never out. |
| `internal/config` | Loads, validates, and serves the TOML config at `~/.agent-director/config.toml`. Read-only after load. Owns the `[tmux]` timing settings (`config.Tmux`, nine keys: `starting_session_seconds`, `stopping_window_seconds`, `pending_grace_seconds`, `query_timeout_ms`, `action_timeout_ms`, `create_timeout_ms`, `pipe_close_wait_ms`, `sweep_budget_seconds`, `kill_exit_wait_ms`), one named constant per default and per safe minimum, and the pending grace period's minimum rule (`PendingGraceMinimumSeconds`). The pending grace period bounds both `find-missing`'s hands-off window for a `pending` row and a SessionStart hook's wait for its launch's identity write, each measured from the launch start (SR-13.4, SR-22.9); it has no maximum. The hook's wait is also capped at 540 s after it began (`sessionStartWaitCap` in `internal/hook`; WD 2026-09-30c), so a grace above 540 s lengthens only `find-missing`'s window, which is unchanged. Safe minimums: bound 60 s, stopping window 30 s, grace period 30 s or ⌈(create timeout + pipe-close wait) / 1000⌉ + 20 s when larger; the other six keys have none (a value too low fails closed). A missing key or 0 gives the default; a negative value, a positive value below a minimum and a non-integer are refused at load (`*config.ConfigError`, surfaced by the CLI as `ErrConfigMalformed`), never clamped. See [`[tmux]` timing settings](#tmux-timing-settings). Also owns `[defaults] expire_retention_days`, `expire`'s default window in whole days: `DefaultExpireRetentionDays` (31), `MaxExpireRetentionDays` (106751, the largest whole number of days a `time.Duration` holds, which is also `older_than`'s day limit in `pkg/api`'s `ParseOlderThan`) and `Defaults.EffectiveExpireRetentionDays()` (the configured value when positive, else 31). A missing key or 0 gives the default; a negative value and one above the maximum are refused at load in the same `*config.ConfigError` as the `[tmux]` refusals, never replaced by the default or capped. **Must use:** read the setting only through `EffectiveExpireRetentionDays` and the day limit only from `MaxExpireRetentionDays` (see [`expire`](#expire)). | stdlib; `github.com/BurntSushi/toml`. | `database/sql`; `internal/store`; `pkg/api`; `cmd/*`. |
| `pkg/api/apitest` | Test helpers shared across packages (non-test `.go` files, so harnesses outside `pkg/api` import them). Families: the `Seed*` fixtures (`SeedSpawn`, `SeedListFixture`, `SeedDeleteFixture`, `SeedDecideFixture`, `SeedPermissionRow`, `SeedExpireFixture`, `SeedJsonl`, `SeedStore`, `OpenStoreWithRow`), `SeedSpawn`'s `With*` options, the store-read and store-id helpers (see [apitest Seed* factory contract](#apitest-seed-factory-contract-reusable-test-fixtures)); the config writers `WriteTmuxConfig` and `WriteRetentionConfig` (see [apitest `[tmux]` config writer](#apitest-tmux-config-writer-reusable-test-fixture)); and the description helper, `AssertDescription` with the `Desc*` cases in `descriptions*.go` (see [apitest description helper](#apitest-description-helper-reusable-test-fixture)). Each section states the must-use rule. | stdlib; `internal/store`; `internal/spawn`; `internal/config` (the `[tmux]` key definitions); `internal/tmux` (the `tmux.Call` names the description cases use); `github.com/BurntSushi/toml` (to encode the config file); `internal/testsupport/storefix`; `internal/testsupport/procstarttimefix` and `internal/testsupport/launchfix` (leaf fixture-value packages); `github.com/google/uuid`; `modernc.org/sqlite` (driver side-effect import). | `pkg/api` (cycle constraint); `cmd/*`; `internal/mcp`; `test/*`. |
| `pkg/api/errnames` | **Single source of truth for err_name strings.** Declares `Catalog []Entry` (each Entry pairs a sentinel `error` with its canonical name string), `Classify(err) (name, description)` with `ErrInternal` fallback, and `TrimNamePrefix` for envelope-text normalisation. The `Catalog` is consumed by `cmd/agent-director`'s envelope writer and `internal/mcp`'s `classifyDispatchError`. `catalog.json` is generated deterministically from `Catalog`; the doc-drift CI gate enforces coherence. | stdlib; `pkg/api`; `internal/config`; `internal/probe`; `internal/spawn`; `internal/store`; `internal/tmux` (sentinel types only). | `cmd/*`; `internal/mcp`. |
| `internal/mcp` | Stdio MCP server. `server.go` handles JSON-RPC framing (initialize, tools/list, tools/call). `dispatch.go::LiveDispatcher` holds a single `*pkg/api.Client` and routes each tool call to the corresponding `Client` method — no business logic of its own. Before routing, `checkParamNames` refuses an argument that is not one of the verb's manifest params with `ErrInvalidFlags`; every manifest param of every exposed verb is decoded through `decodeParams`, which refuses a wrongly typed value with `ErrInvalidFlags` too (see [Parameter names and unknown arguments](#parameter-names-and-unknown-arguments)). `expire`'s `older_than` is parsed with `pkg/api.ParseOlderThan`, the parser the CLI shares, and a value it rejects is `ErrInvalidFlags`. `classifyDispatchError` delegates to `errnames.Classify`. | stdlib; `pkg/api`; `pkg/api/manifest`; `pkg/api/errnames`. | `internal/store`; `internal/config`; `internal/tmux`; `internal/spawn`; `cmd/*`. |
| `pkg/api/manifest` | Defines and exposes the canonical CLI/MCP verb manifest used to keep the CLI surface, MCP tool surface, and docs in lock-step. Also holds `ReuseOptInSpelling`, the reuse opt-in's one spelling in shared advice, which `internal/spawn` builds its retry sentences on, and `TmuxSessionNameSpelling`, the session-name param's, which `pkg/api`'s list hint and `internal/spawn`'s `ErrTmuxSessionNameEmpty` description build on (see [`pkg/api/manifest` — Verb Registry](#pkgapimanifest--verb-registry)). | stdlib only — leaf package. | `internal/store`, `internal/config`, `cmd/*`, raw `database/sql`, SQL strings. The manifest is the source of truth; consumers depend on *it*, never the other way around. |
| `internal/spawn` | Owns the parameter-resolution → validation → defaults → launch pipeline (SRD §7). `ApplyDefaults` makes the collision pre-check's one `SpawnState` read and returns an `IDCheck`. Builds env maps and synthesizes `--settings` JSON. Plain spawn's `Launch` resolves the launch socket and mints the launch token (`launchid.go`: `ResolveLaunchSocket`, `ResolveScanSocket`, `NewLaunchToken`, and `ResolveQuerySocket` for a query on a row that records no socket), inserts the `pending` row with launch start, token and socket, creates and labels the session through the shared create-and-label step (`createlabel.go`: `LaunchTmux`, `CreateRequest`, `CreateAndLabel`, `CreateOutcome` / `CreateKind`), maps its failures in one place (`launch_errors.go`: `plainSpawnCreateError`, built from the exported description builders shared by every launch verb, `TmuxUnavailableError` (also the label scan's), `LaunchTimeoutError`, `UnlabelledSessionError`, `CreateFailedError` (with `InstanceCreateFailedError`, its form led by the instance id, for plain spawn's held name whose holder vanished) and the row sentence `RowStaysPending`, with the retry sentences `LaunchRetryRule`, `ReuseRetry` and `PlainSpawnCollides` and `ReuseOptIn`, the reuse opt-in in its one spelling, built on `manifest.ReuseOptInSpelling`; "duplicate session" is no verb error there but a `*HeldNameError` handed to `pkg/api`'s held-name path) and makes the conditional identity write (`RecordLaunchIdentity`, shared with resume and reuse). The one composition step for a resolved request is `ComposeLaunch` (`compose.go`): the `CreateRequest` and the row's request fields (`ComposedLaunch{Create, Row}`), with no write, tmux call or I/O, shared by plain spawn's insert and reuse's reset; the parent id comes from `ParentIDFromEnv()` (the caller's `AGENT_DIRECTOR_INSTANCE_ID`), the one derivation used by the insert, the reset and resume's move. Every launch pre-trusts its folder through the one shared step `PreTrust` (`pretrust.go`), which plain spawn runs before its insert, reuse before its reset and resume before its move to `pending`; its read-modify-write of `.claude.json` runs under Claude Code's own lock on that file, taken by `lockConfig` (`configlock.go`); see [Workspace-trust pre-write](#workspace-trust-pre-write). Resume's launch uses the same pieces: `ResolveRowLaunchSocket` (the row's recorded socket), `ComposeRelaunch` (the `CreateRequest`, with no tmux call or write) and `Relaunch` (`CreateAndLabel` on that request). Reuse's launch uses `ResolveRowLaunchSocket`, `ComposeLaunch` and `CreateAndLabel`. The clock and the start-time reader are passed in. See [Launch identity](#launch-identity) and [Reuse of a finished id](#reuse-of-a-finished-id). | stdlib; `internal/config`; `internal/store`; `internal/tmux`; `pkg/api/manifest` (`ReuseOptInSpelling` and `TmuxSessionNameSpelling` only); `github.com/google/uuid` for UUID4 minting. | Raw `database/sql`; hook-handling code; MCP framing; ad-hoc subprocess management outside `internal/tmux`. |
| `internal/tmux` | Thin client over the tmux binary, built only by `New(binary, Timeouts)` (`""` = tmux on `PATH`). **Phase 1 call set (SR-2.1, Appendix F.1)**, every call taking the socket: `Lookup` (the one-invocation lookup: the server identity read `display-message -p 'ad-server<TAB>#{pid}<TAB>#{start_time}'`, then the session listing with labels, then the three `@ad_owner` scope reads; LFR H5; b.47f. The identity read answers on a server with no sessions too (tmux's `exit-empty` off), so every answer names the server that gave it. `parseLookup` takes the first line as the identity line, exactly `ad-server`, the decimal pid and the decimal start time, tab separated; then the session lines, whose pid and start time must equal the identity line's; then the scope section (LFR H6). A first line that is not an identity line, empty output included, or a session line that disagrees with it makes the answer malformed, `FailUnrecognized`), `ListPanes` (`list-panes -a`), `KillPane` (by pane id), `KillSessionID` (by session id), `SendKeysPane` (by pane id: the text call `send-keys -t <pane id> -l -- <text>`, then an optional separate `send-keys -t <pane id> Enter`; the `--` makes a text starting with `-` literal, never read as a send-keys flag; a text ending in `;` is typed whole, by the argv escape below), `CapturePaneID` (by pane id), `SetLabel` (label by id: the session label by session id and the pane label by pane id) and `NewSession` (the create with its chained `@ad_owner` and `@ad_pane` labels). **Key send (b.9o4)**, an addition beside the SR-2.1 calls, not one of them, also taking the socket: `SendKeyPane` (by pane id: `send-keys -t <pane id> <key>`, one key by its tmux key name, never typed literally, call kind `CallSendKey`, "key send"; `pause`'s `C-u`; the key is a fixed name the caller chooses, never caller text). **Label form (SR-3.4, SR-3.5):** `ad1 <token> <$N> <instance id> <store id>`, five fields. The store id is the writing store's `store_meta.store_id`, which callers pass from `(*store.Store).StoreID()`; it is the last field, so the instance id is everything between the third and the last space and may contain spaces. `NewSession` and `SetLabel` both take the token, the instance id and the store id; the chain passes only the instance id through the format escape below. **Pane label (SR-2.1, SR-3.5):** every created pane carries the per-pane user option `@ad_pane` = `<token> <pane id>`, so a launch whose create reply was lost can later find its own pane by token, whatever the base-index or window layout. The create sets it with a second chained step, `; set-option -p -F -t =<name>: @ad_pane '<token> #{pane_id}'`, after the `@ad_owner` step; each `;` is its own argv element, and a name for which `NeedsLabelByID` holds gets neither chained step. A failure of either chained step is the create's `FailLabel` (tmux stops the chain at the first failing step). `SetLabel(socket, sessionID, paneID, token, instanceID, storeID)` sets both labels in one invocation, `set-option -t <$N> @ad_owner '<label>' ; set-option -p -t <%N> @ad_pane '<token> <%N>'`, with the session and pane ids from the create reply; a failure may leave the session labelled and its pane not. Only the new session's one pane is labelled: a pane split from it later has no value. **Pane listing:** `ListPanes` reads `#{@ad_pane}` as the sixth and last field, the value being everything after the fifth tab, so a tab inside it cannot shift the other fields. `Pane.AdPane` is the token only when the value is exactly `<16 lowercase hex token> <pane id>` and that pane id equals the line's own `%N` (`classifyPaneLabel`); anything else gives `""`, so a window, session, global or server value borrowed through the format, which names another pane or none, never counts (the scope guard of SR-3.6). Caveat: on tmux 3.3a a server-scope `@ad_pane` (`set-option -s`) is listed on every pane in place of its own value, so while one exists only the pane that value names can report a token and every other pane reads `""`; no other pane is matched, but a pane reading `""` then does not show that its label is gone. The raw value never leaves the client, and a malformed listing's `CallError.FirstLine` is its first line cut before the pane label field (`paneListingFirstLine`). The lookup does not read `@ad_pane`. `kill`'s adoption of a lost create reply (SR-3.6) is its first reader; it also exists for the leftover-pane check (SR-3.7) and the no-pane row check (SR-11.3). A value in any other form, a four-field one included, parses as no label (`LabelNone`), except that a four-field value whose instance id ends in a space and 16 lowercase hex reads as a shorter id plus that word as its store id; and `Label.StoreID` is set only on a valid label. Typed results and failures: `Call`, `Failure`, `CallError`, `LookupAnswer`, `Session`, `Label` / `LabelKind`, `CreateReply`, `Pane`, `Timeouts`. **Argv escape (`invoke.go`):** tmux splits its argv into commands at every element ending in `;`, before any option parsing and so even after `--`: the `;` is dropped and the element ends its command, so a caller value ending in `;` followed by more elements would run those as a tmux command of the caller's choosing (`kill-server`, `run-shell <shell command>`). An element ending in `\;` is instead one argument with that backslash removed. `commandArgv(cmds ...[]string)` builds every call's argv after `-u -S <socket>` from a list of commands: a standalone `;` only between commands, and every element of every command passed through `escapeFinalSemicolon` (one backslash before a final `;`). So every value (session name, cwd, `-e` entry, the agent's command with its claude arguments at spawn and at resume, send-keys text, label values, targets) reaches its tmux command as one argument, a final `;` included, and never ends that command. The escape covers only this split: what the command then does with the argument is unchanged (format expansion is the format escape's job, below). The chain target `=<name>:` ends in `:`, so it is never escaped, and tmux matches it against the stored, unescaped name. The socket is not escaped: tmux's own option parsing consumes `-S <socket>` before the split. `HasSession` builds its argv the same way. **Must use:** every tmux call composes its argv through `commandArgv`, socket-taking calls by passing one `[]string` per command to `invoke` (`runAction`, `runData`); never put a `;` separator or a value into a tmux argv by hand. **Format escape (`create.go`):** tmux expands formats in some arguments before using them: `new-session`'s `-c` cwd (twice, by the command and again at the pane spawn, both times from the raw argument), its `-s` name, and every `set-option -F` value. In a format, `#(<cmd>)` runs `<cmd>` through the shell on the tmux server, and `#{…}` and aliases such as `#S` are replaced, so a raw cwd holding them would run a command, or start the agent in another directory, at spawn and on every resume. `escapeFormat(text)` returns text that expands back to exactly itself: each run of `#` is doubled (`##` expands to `#`), except a run directly before `[`, which tmux copies through unchanged as a style, so doubling it would add `#`. `createCommands` sends `-c` as `escapeFormat(cwd)` and the `@ad_owner` value's instance id through it, so spawn and resume start the agent in the cwd exactly as given and run nothing in it; the cwd stored on the row and reported by agent-director is the unescaped one. The two escapes compose: `escapeFormat` adds or removes no `;`, and tmux drops the argv escape's backslash before it expands the format, so a cwd `/a#;` is sent as `/a##\;` and expands to `/a#;`. Not escaped: the `-s` name, which never holds `#` (spawn refuses one, and a default name keeps only `[A-Za-z0-9_-]`); the `-e` entries and the agent's command, which tmux does not expand; and the client's own fixed formats (`#{session_id}`, `#{pane_id}`, the identity read's, the reply and listing formats). **Must use:** every caller-controlled value in a tmux argument that tmux format-expands goes through `escapeFormat`; never double `#` by hand. Mechanics: every call runs `-u -S <socket>` first; targets are ids only (never a name or pattern); each call class (query, action, create) has its own timeout, plus the pipe-close wait (`Timeouts.WaitDelay`); data is parsed only from standard output of an exit-0 call; replies are recognised only from the first line of standard error; the client's environment has every `AGENT_DIRECTOR_*` variable removed. Socket-taking calls fail only with `*CallError`. Labels reach callers only classified (the raw value never leaves the client) and recognised replies only as a `Failure`; the one exception is an unrecognised reply, whose first line (trimmed, at most 200 bytes) is carried in `CallError.FirstLine`. **Socket resolution (RN-5):** `ResolveSocket(create)` resolves the socket as tmux does (`TMUX`, then `TMUX_TMPDIR`, then `/tmp`, with tmux's per-user directory checks) and `EnsureSocketDir(socket)` creates only a missing per-user directory; refusals are `*SocketDirError` (with `SocketDirReason`), matching `ErrTmuxNotAvailable`. **Must use** `tmux.NeedsLabelByID(name)` to decide whether a session name (one containing `$` or `\`) must be labelled by id rather than by the chain; never re-implement that test. The client receives its timeouts and pipe-close wait from `pkg/api` at construction, never from `internal/config` (see [`[tmux]` timing settings](#tmux-timing-settings)); the package defines no defaults. The runner seam types (`Invocation`, `RunStatus`, `RunResult`, `Runner`) are exported for replay tests; tests install a runner only through the test-only `NewWithRunner` in `export_test.go`. The one name-based method left is `HasSession`; the name-based kill, send and capture are removed (every verb targets ids only). `HasSession` is kept on the client and on `api.TmuxClient` and matches by prefix; no verb uses it, and none may (`resume` judges its row with the lookup). `StripANSI` post-processes captures. **Shared lookup (SR-3.3, SR-3.4, SR-3.10, Appendix F.2):** `Lookup` / `Classify` in `lookup.go`, `lookup_class.go`, `lookup_holder.go` and `lookup_server.go` turn one lookup answer and a row's `Launch` into a verdict; see [Shared tmux lookup](#shared-tmux-lookup). Beside it: `unusable.go` (the unusable-name guard `Unusable`, and `RewrittenIn`), `agent_process.go` (agent-process selection `SelectAgentProcess`, judgement `JudgeProcess` and `KnownStartTime`), `pane_token.go` (`PaneByToken`, a pane found by its `@ad_pane` token), `sweep.go` (the multi-socket sweep `Sweep`, built by `NewSweep`, under one tmux budget) and `starting_session.go` (the session-age helper `SessionAge` and the starting-session rule `StartingSession`; see [Starting-session rule](#starting-session-rule-starting_sessiongo)). | stdlib (`bytes`, `context`, `errors`, `fmt`, `io/fs`, `os`, `os/exec`, `path/filepath`, `regexp`, `slices`, `sort`, `strconv`, `strings`, `syscall`, `time`, `unicode`, `unicode/utf8`). | `internal/config` (see [`[tmux]` timing settings](#tmux-timing-settings)); `internal/probe` (the lookup's `ProcChecker` is satisfied structurally); template and store packages; shell processes (`/bin/sh`); anything other than direct `exec.Command`. |
| `internal/hook` | Reads payload JSON from stdin, classifies per SRD §5.2, and writes the row only through the gated store writes: a hook applies only when its parent process (`getppid()` and that pid's start time, captured once at entry) is the row's recorded pane process; otherwise it changes nothing and writes one `ad.hook.ignored` (SR-22.9; see [Hooks move a row only for its own agent](#hooks-move-a-row-only-for-its-own-agent)). A subagent's or in-process teammate's SessionStart or SessionEnd (non-empty `agent_id`) is decided from the payload before any write and ignored as `subagent_event`. The main agent's idle-prompt Notification (`notification_type` `idle_prompt`, no `agent_id`) is written through `ApplyHookWaitingIfWorking`, which returns a `working` row to `waiting` and soft-refreshes any other (b.svb; see "The idle-prompt Notification" in [Event → state mapping](#event--state-mapping-srd-52)). A SessionStart that arrives before its launch's identity write waits for it until the launch start plus the pending grace period or 540 s after it began waiting (`sessionStartWaitCap`), whichever comes first, re-reading the row every 250 ms on the injected clock, before its final gated write (`recordSessionStart`, `waitForLaunchIdentity` in `handler.go`; see [Hooks move a row only for its own agent](#hooks-move-a-row-only-for-its-own-agent)). `HandleNoExecForm` (`noexec.go`) is the no-verb run's side: it takes the raw stdin bytes, writes `ad.hook.ignored` `no_exec_form` when they are a hook payload, and opens no store. On the hook path (`emitIgnored`), after a `pid_mismatch` refusal, and only then, the hook reads the parent's own parent pid once (`ParentProc.PPID`) for the launcher warning (`launcher_pid`, `ad.hook.launcher_detected`; b.9n6), plus, only when it writes `ad.hook.launcher_detected`, the launcher's command name (`ParentProc.CommandName`, `launcher_command`); neither read feeds the gate's decision (see "Launcher warning" in [Hooks move a row only for its own agent](#hooks-move-a-row-only-for-its-own-agent)). Exits 0 (state-tracking fail-open). | stdlib; `internal/store`; `internal/trail`; `internal/config` (the `config.Relay` settings type only; the cmd-side wrapper loads config); `github.com/google/uuid`. The parent-process readers arrive as `HandleConfig.ParentPID` / `ParentProc`, and the wait's clock and grace period as `HandleConfig.Now` and `HandleConfig.PendingGrace` (a `time.Duration`, so the package reads no `[tmux]` setting), all wired by `cmd/agent-director`; the wait sleeps on `HandleConfig.Clock` (the relay poll's `PollClock`). | `internal/tmux`; `internal/spawn`; `internal/probe` (no tmux call and no ancestry walk on the hook path; the reads past the parent are the launcher warning's parent-pid read, plus the launcher's command-name read only when `ad.hook.launcher_detected` is written, both through `ParentProc`). |

### `[tmux]` timing settings

Every tmux timing value (the starting-session bound, the stopping window,
the pending grace period, the per-call timeouts, the pipe-close wait, the
sweep tmux budget and `kill`'s exit wait) is a key of the `[tmux]` table,
held in `config.Config.Tmux`. The pending grace period has two users
(SR-13.4): `find-missing` leaves a `pending` row inside it unjudged, and a
SessionStart hook that arrives before its launch's identity write waits for
that write until the launch start plus the grace period or 540 s after it
began waiting, whichever comes first (SR-22.9; WD 2026-09-30c; `runHook`
passes `EffectivePendingGrace` to the hook as `HandleConfig.PendingGrace`).
The 540 s cap is `sessionStartWaitCap` in `internal/hook`, not a `[tmux]`
setting; `pending_grace_seconds` keeps no maximum, and a grace above 540 s
lengthens only `find-missing`'s window. `internal/config` is the single source of
truth for them, following the `Relay.EffectiveTimeoutSeconds` pattern:

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
  decoding, together with `[defaults] expire_retention_days` (see
  [`expire`](#expire)); every refused key is described in one
  `*config.ConfigError`, `[defaults]` first, then `[tmux]` in table
  order. No verb, server or hook runs with a minimum or default in place
  of a refused value.
- **`internal/tmux` never imports `internal/config`** and reads no
  configuration (its row's prohibited imports).

### Shared tmux lookup

`internal/tmux` holds the one shared lookup (SRD SR-3.3, SR-3.4, SR-3.10,
Appendix F.2): given a row's launch identity and this store's id, one lookup
answer gives exactly one verdict for the row, plus the holder of a given
name. Beside it sit the shared helpers the verbs build on: the
unusable-name guard, agent-process selection and the recorded-process
judgement, the pane-by-token selector, the sweep and the starting-session
rule. The doc comments in the
files carry the detail.

| File | Holds |
| --- | --- |
| `lookup.go` | `Launch` (the row: `InstanceID`, `Token`, `StoreID`, `Socket`, `ServerPID`, `ServerStart`, `ServerStarttime`; zero means none), `ProcChecker`, `LookupClient`, `Verdict`, `CantTellKind`, `Result` with `Token()`, `Lookup`, `Classify`, the `Server*` and `Reason*` constants, `TokenNotRun`, and the one outcome mapping (unexported `resultForCall`, and `resultForFailure` for a failed call, shared with the sweep's pane listing and, through `ListingFailure`, a single-row verb's pane listing). |
| `lookup_class.go` | `LabelClass`, `(LabelClass) CaseWords()`, `(Launch) ClassOf(Label)`. |
| `lookup_holder.go` | `StoredForms(name)` and the name-holder match. |
| `lookup_server.go` | The clock-free server check (unexported `checkServer`), which judges the recorded server process through `JudgeProcess`. |
| `unusable.go` | `UnusableKind` and `Unusable(name)`, the unusable recorded-name guard. |
| `agent_process.go` | `ProcIdentity`, `AgentSource`, `AgentProcess`, `SelectAgentProcess`, `ProcState` and `JudgeProcess`. |
| `pane_token.go` | `PaneMatch` and `PaneByToken(panes, token)`. |
| `sweep.go` | `Sweep`, `NewSweep`, `(*Sweep) Lookup`, `PaneLister`, `PaneListing`, `(*Sweep) ListPanes`. |
| `starting_session.go` | `Elapsed` with `Under`, `SessionAge`, `StartingSessionOutcome` (`StillStopping`, `StillStarting`, `PastBoth`), `StartingSessionInput`, `StartingSessionClass` and `StartingSession`. |

**Entry points.**

- `Lookup(c LookupClient, pc ProcChecker, row Launch, holderName string) Result`
  makes exactly one `c.Lookup(row.Socket)` call, for single-row verbs,
  follow-ups and re-lookups. It never resolves or creates a socket.
- `Classify(ans LookupAnswer, pc ProcChecker, row Launch, holderName string) Result`
  classifies an answer the caller already holds (sweeps) and makes no call.
- `LookupClient` is `Lookup(socket) (LookupAnswer, error)`; `*tmux.Client`
  and `tmuxfix.Recorder` satisfy it.
- `ProcChecker` is `StartTime(pid) (start string, alive, known bool)`,
  declared here with the same signature as
  [`probe.ProcChecker`](#start-time-reader-procchecker-starttimego);
  `probe.NewProcChecker()` satisfies it structurally.

**Verdicts and tokens.** `Verdict` is `Ours`, `Leftover`, `Gone` or
`CantTell`; a Can't tell carries a `CantTellKind`: `CantTellUnreadable`,
`CantTellDifferentServer`, `CantTellProvenanceConflict` or
`CantTellUnavailable`. `Result.Token()` gives the outcome token: `ours`,
`leftover`, `gone`, `cant_tell` (unreadable), `different_server`,
`provenance_conflict` or `tmux_unavailable` (`""` for a zero value), and
`not_run` for a `Skipped` result (set only by the sweep; see
[Sweep](#sweep-sweepgo)).
`Result` also carries `Session` (Ours), `Leftovers` (old-labelled sessions,
in listing order), `Conflicting` (the sessions carrying the current label
when two or more do, so a description can name them; nil otherwise, a
scope value included), `Holder`, `HolderClass`, `HolderAmbiguous`, `Server`,
`ServerPID` / `ServerStart` (the answering server), `Adopt` (Ours on a row
with no recorded server identity), `Disagree`, `Cause` and `Skipped`.

**Verdict order.** A call error comes first:
`FailUnavailable` / `FailSocketDenied` give `tmux_unavailable`; `FailNoServer`
/ `FailNoSocket` go to the server check as a no-server reply (Gone or a
different server); any other error is `cant_tell` (`Cause` set only for a
`*CallError`). On an answer, in order:

1. The server check says a different server: `different_server`
   (`server_mismatch`).
2. A scope value (`ScopeValue`), on any listing, empty or restarted
   included: `provenance_conflict` (`scope_value`).
3. Two or more sessions of class current: `provenance_conflict`
   (`duplicate_label`), `Leftovers` cleared.
4. Exactly one current: `Ours`, whatever the session's name.
5. No current, one or more old: `Leftover`.
6. Otherwise `Gone` (foreign, other-store and none labels, or no sessions).

`Disagree` holds each reason (`ReasonServerRestarted`,
`ReasonServerMismatch`, `ReasonDuplicateLabel`, `ReasonScopeValue`) at most
once and never a label value. `pid_mismatch` is retired; `adopted` and
`name_changed` (`ReasonAdopted`, `ReasonNameChanged`) are decided by the
verbs, never by the lookup, so all six reasons are declared here.

**`TokenNotRun`** (`not_run`) is the token of a step that did not run: a
Skipped sweep result, and `kill`'s `lookup_outcome`, `followup_outcome`
and `process_check` trail fields. Use it; never spell `not_run`.

**`ListingFailure(err, pc, row) Result`** maps a single-row verb's failed
pane listing through the same failure mapping as a failed lookup: tmux
unavailable, Gone or a different server for a no-server reply, or
unreadable. It makes no call. **Must use** it for a verb's own pane
listing failure (the sweep's listing already goes through
`Sweep.ListPanes`); never classify a listing error by hand.

**Label classes** (`Launch.ClassOf`, byte-for-byte comparisons, checked in
this order):

| Class | When | `CaseWords()` |
| --- | --- | --- |
| `ClassNone` | No valid label (`LabelNone`). | "no valid instance id" |
| `ClassOtherStore` | A valid label whose `StoreID` is not `Launch.StoreID`, whatever its instance id and token. An empty `Launch.StoreID` matches no label (fail closed). | "another agent-director store" |
| `ClassForeign` | This store, another instance id; also every label when the row's id is empty or holds a control character (0x00-0x1f, 0x7f), so such a row is never Ours or Leftover. | "a different instance id" |
| `ClassCurrent` | This store, the row's id, and the row's non-empty token. | "" |
| `ClassOld` | This store, the row's id, another token (or any token when the row has none, so a row with no token is never Ours). | "left over from an earlier life" |

An other-store session is never Ours or Leftover, is never listed in
`Leftovers`, and counts toward Gone for the row.

**Server check (SR-3.3, clock-free).** `ProcChecker.StartTime` is called at
most once, on `Launch.ServerPID`:

- No identity recorded (`ServerPID == 0`): `Server` = `unknown`; the reader
  is not called. Whichever server answers counts; an empty listing or a
  no-server reply is Gone.
- A listing, with or without sessions, whose `ServerPID` and `ServerStart`
  equal the recorded ones: `match`; the reader is not called. So the
  recorded server left with no sessions (tmux's `exit-empty` off) matches,
  never a different server, and with no scope value the row is Gone with no
  reason (LFR H5; b.47f).
- Otherwise the recorded process decides, judged by `JudgeProcess` on
  (`ServerPID`, `ServerStarttime`); anything but `ProcGone` counts as a
  different server. Gone (absent or zombie, or alive
  with a start time other than `ServerStarttime`) gives `restarted`: a
  listing, with or without sessions, is judged on the answering server
  (so an empty one is Gone) and logs `server_restarted` (AC-LKP-19; LFR H5;
  b.47f); a no-server or no-socket reply is Gone with no reason, since
  nothing answered. Running (alive
  with the recorded start time) or unchecked (unreadable, or alive while no
  `ServerStarttime` is recorded) gives `differs`, `different_server` and
  `server_mismatch`. So with no recorded start time only an absent or
  zombie process reads as gone.
- Every answer carries the answering server's identity from its identity
  line, an empty listing included, and `Result.ServerPID` /
  `ServerStart` report it; only a no-server or no-socket reply names no
  server. So the check (`checkServer`) tells two reply shapes apart: a
  listing (`replyListing`, any lookup answer, empty or not) and a
  no-server or no-socket reply (`replyNoServer`). `Result.Server` is `""`
  when no server check ran (unreadable, unavailable).

**Name holder (SR-3.10).** `StoredForms(name)` gives the raw name alone for
a name with neither `$` nor `\`, else the raw name and tmux 3.2a/3.3a's
escaped form (`\` as `\\`, `$` before an ASCII letter, `_` or `{` as `\$`),
never the same form twice; it works on bytes. The holder is the session
whose stored name equals one of the forms exactly: no match means not held;
more than one gives `Holder` nil and `HolderAmbiguous` true (Can't tell for
the holder check). `HolderClass` is `ClassOf` the holder's label, and its
`CaseWords()` give the description's words. The holder is reported on every
path that read an answer, different server and conflict included.

**What the lookup never reads.** No clock, no environment (a tmux server
agent-director started carries no `AGENT_DIRECTOR_*` variable and must read
as running), no store and no `@ad_pane`. It consumes only `LookupAnswer`,
`*CallError` and the `ProcChecker`. `internal/tmux` does not import
`internal/probe` or `internal/config`.

**Must use:** every verb that judges a row against tmux goes through
`tmux.Lookup` (one row, one call) or `tmux.Classify` (an answer already
held). Never re-implement label classes, the server check, the verdict
order or stored-name matching. Its verb users so far are plain spawn's
label scan (see [Launch identity](#launch-identity)), `kill` (see
[Stop semantics](#kill)), `resume` (see [Resume](#resume)) and, through
the [Sweep](#sweep-sweepgo), `find-missing` and `expire`; the single-row verbs map its Can't tell
results through `pkg/api`'s shared mapping (see
[Single-row verb helpers](#single-row-verb-helpers-pkgapi)).

Tests: `internal/tmux/lookup_test.go`, `lookup_server_test.go`,
`lookup_holder_test.go`, `lookup_unusable_test.go` (the unusable-name
guard), `lookup_agent_test.go` (agent-process selection and judgement),
`lookup_adopt_test.go` (`PaneByToken`), `lookup_sweep_test.go` (the sweep's
lookups, stop rule and budget) and `lookup_sweep_panes_test.go` (the sweep's
pane listings) share the external-test-package fixture
`internal/tmux/lookup_helpers_test.go` (package `tmux_test`): a default row
(`newLookupRow` with its `row*` options), label kinds (`lbl*`), a Recorder
table plus a [`procfix`](#procfix-the-process-checker-fake-reusable-test-fixture)
checker (`newLookupFixture`), and a runner (`run` / `runOn`) that makes one
lookup call and holds `Lookup` and `Classify` equal. **Reusable sweep
fixture:** `newSweepRun(t, query, sockets...)` returns a `*sweepRun`, one
sweep case: a Recorder on virtual time (each lookup or pane listing takes
`query`) with the recorded server alive on every socket, acting as the
sweep's `LookupClient` and keeping each socket's lookup outcome. Its
`check(got, row, holder)` asserts a sweep result equals the row's judged
result (`Classify` on the held answer, or `Lookup` for a held failure),
`checkCalls(call, want)` asserts the per-socket count of a call kind, and
the free function `checkSkipped(t, got)` asserts a bare Skipped result
(token `not_run`). New lookup and sweep tests in the package use these
rather than building their own.

#### Unusable-name guard (`unusable.go`)

`Unusable(name string) UnusableKind` classifies a recorded session name
(SR-3.2). When a name has more than one fault, the first in this order
decides:

1. `UnusableEmpty`: `""` (tmux reads it as "the most recent session").
2. `UnusableControl`: any byte 0x00-0x1f or 0x7f, judged byte by byte on the
   raw bytes, so an invalid-UTF-8 name that also carries a control byte is
   Control. C1 code points and other Unicode characters do not count.
3. `UnusableRewritten`: `.` or `:` (tmux stores them as `_`), or bytes that
   are not valid UTF-8 (tmux stores them as backslash-octal escapes).
4. Otherwise `UnusableNone`, the zero value. `$`, `\`, `#` and valid
   non-ASCII UTF-8 are usable.

The guard only classifies (no error sentinel); the caller decides what each
kind means. It is separate from `internal/spawn`'s validation of a new
explicit name, which also refuses `#`, `$`, `\` and long names; the guard
allows `$`, `\` and `#` in a recorded name.

**Must use** `tmux.Unusable` to decide whether a recorded name may be passed
to tmux; never re-implement the test or its order. `RewrittenIn(name)`
(a `Rewritten` of `Dot`, `Colon`, `InvalidUTF8`, with `Any()`) says which
rewritten characters a name holds; `Unusable` uses it, so the rules live in
one place, and a description that names the character uses it too. Verbs
reach the guard through `pkg/api`'s `unusableNameError` (see
[Single-row verb helpers](#single-row-verb-helpers-pkgapi)), which lists
every verb that calls it. The two sweeps refuse nothing and call
`tmux.Unusable` directly, each through its one kind-to-token mapping:
`find-missing`'s `unusableNameNote` (a liveness note; see
[`find-missing`](#find-missing)) and `expire`'s `unusableKeptReason` (a
kept reason; see [`expire`](#expire)).

#### Agent-process selection and judgement (`agent_process.go`)

A `ProcIdentity` (`PID`, `Starttime`) is recorded when `PID > 0`; a pid with
no start time is a recorded pid-only identity.

`SelectAgentProcess(sessionStart, pane ProcIdentity) AgentProcess` picks a
row's agent process (SR-3.8):

- neither recorded: `AgentNone`, zero identity;
- only one recorded: that one (`AgentSessionStart` or `AgentPane`);
- both recorded and they disagree (the pids differ, or both start times are
  recorded and differ): `AgentPane`, the pane identity wins;
- both recorded and they agree: `AgentSessionStart`, as recorded.

The two can differ only on a hand-edited row. A disagreement signals
nothing: there is no disagree reason for it, and `pid_mismatch` is retired
as an `ad.provenance.disagree` reason.

`JudgeProcess(pc ProcChecker, id ProcIdentity) ProcState` judges a recorded
process with the start-time reader, calling `pc.StartTime` at most once and
never when `id` is not recorded:

| Case | `ProcState` |
| --- | --- |
| Not recorded | `ProcNone` (no reader call) |
| Unreadable (`known` false) | `ProcUnknown` |
| Gone (absent or zombie) | `ProcGone` |
| Alive, pid-only identity | `ProcUnknown` (never alive) |
| Alive, start time equals the recorded one | `ProcAlive` |
| Alive, another start time (pid reused) | `ProcGone` |

Both read no clock and no environment and make no tmux call. The lookup's
server check already judges the recorded server through `JudgeProcess`.

**Must use** `tmux.SelectAgentProcess` to choose a row's agent process and
`tmux.JudgeProcess` to judge any recorded process (liveness, `kill`'s wait,
`expire`'s process check, `find-missing`'s process path, `resume`'s and
reuse's running-process check); never re-implement either. `kill` is their
first verb user.

`KnownStartTime(pc, pid)` returns a process's start time when the reader
answers alive and known, else `""` (pid not positive, unreadable or gone),
calling the reader at most once. **Must use** it for any start time that is
recorded or compared later (the identity write, adoption, `kill`'s listed
pane processes); it moved here from `internal/spawn`, and no package keeps
its own copy.

#### Pane-by-token selector (`pane_token.go`)

`PaneByToken(panes []Pane, token string) (Pane, PaneMatch)` picks, from one
pane listing, the pane whose `Pane.AdPane` equals `token` (the client has
already applied the `@ad_pane` scope guard). Panes are counted by distinct
pane id, because `list-panes -a` lists a shared pane once per session
showing its window:

- `PaneOne`: exactly one distinct pane id carries the token; the `Pane` is
  its first entry in listing order.
- `PaneNone`: none carries it, or `token` is `""`; the `Pane` is zero.
- `PaneMany`: more than one distinct pane id carries it; the `Pane` is zero.

Its callers are adoption of a lost create reply (SR-3.6, the row's token), a
leftover's pane (SR-3.7, the leftover label's token) and the no-pane row
check (SR-11.3). What each outcome means is the caller's. Adoption
(SR-3.6) and a leftover's pane (SR-3.7) take a pane only on `PaneOne`; for
adoption, none and more than one alike adopt no pane (the pane verbs refuse
with "the agent's pane was not found"). In the SR-11.3 check of a row that
records no pane, `PaneNone` counts as Gone and `PaneMany` is unverified,
never Gone. It makes no tmux call.

**Must use** `tmux.PaneByToken` to find a pane by its `@ad_pane` token;
never match `AdPane` by hand. Its user is `pkg/api`'s adoption rules
(`findAdoption`, which `kill`, `send-keys` and `pause` reach through
`adoptIdentity`, `find-missing` through `adoptInSweep`, and `read-pane`
directly, for the call only, with no write).

#### Sweep (`sweep.go`)

A `Sweep` serves one `find-missing` or `expire` run (SR-3.15, SR-13.3,
SR-13.5) and is not for concurrent use.

- `NewSweep(c LookupClient, pc ProcChecker, now func() time.Time, budget time.Duration) *Sweep`
  makes no call and does not read `now`. Callers pass the effective sweep
  budget (`config.Tmux.EffectiveSweepBudget`); the Sweep reads no
  configuration and no environment.
- `(*Sweep) Lookup(row Launch, holderName string) Result`: **one lookup per
  socket**, keyed by `row.Socket` exactly as passed (never resolved or
  created). The first row to reach a socket makes its one
  `c.Lookup(row.Socket)` call and the outcome (answer or error) is held;
  every row of that socket is then mapped from the held outcome through the
  same outcome mapping and `Classify` as `tmux.Lookup`, with its own
  `Launch`, so each row is judged against its own recorded server.
- `(*Sweep) ListPanes(pl PaneLister, row Launch) PaneListing`: the adoption
  pane listing, at most one `pl.ListPanes` call per socket, under the same
  gate, stop rule and budget. `PaneLister` is `ListPanes(socket) ([]Pane,
  error)`; `*Client` and `tmuxfix.Recorder` satisfy it. A held listing is
  returned as a copy (`Listed`, `Panes`); a failure is mapped for the
  requesting row exactly as a lookup failure (`PaneListing.Result`).
  `expire` never lists panes.

**Stop rule (per socket).** A result on a socket that is Can't tell
unreadable or tmux unavailable, from the lookup or the pane listing, stops
that socket: that row gets its real result, and every later row of the
socket is Skipped with no call, even when its answer is held. Other sockets
go on. A different server, a `provenance_conflict` and a no-server or
no-socket reply stop nothing.

**Budget (on the injected clock).** Each call's time is `now()` at its
return minus `now()` at its start, added only when positive (a clock stepped
back refunds nothing). The budget is spent once the total is at least
`budget`; a non-positive budget is spent from the start, so no call is ever
made. Before every call, and before handing out anything held, a spent
budget or a stopped socket makes the row Skipped. A call that leaves the
total at or over the budget has its outcome discarded, nothing is held and
its row is Skipped; every later row on every socket is then Skipped. A run
spends at most the budget plus one call.

**Skipped rows.** A Skipped result is `Result{Skipped: true}` with nothing
else set; `Result.Token()` gives `not_run`, which is none of the seven
outcome tokens. Callers check `Skipped` first.

**Must use:** a sweep verb (`find-missing`, `expire`) judges its rows only
through one `tmux.NewSweep` per run and its `Lookup` and `ListPanes`; never
call `tmux.Lookup` per row in a sweep, and never re-implement the grouping,
the stop rule or the budget. `find-missing` and `expire` use it (see
[`find-missing`](#find-missing) and [`expire`](#expire)).

#### Starting-session rule (`starting_session.go`)

Two files hold the one rule that decides whether a finished row's own
session, or its still-running agent process, is still stopping, still
starting or past both (SRD SR-3.9, SR-4.2, SR-4.3). The rule's order and
what `resume` returns for each outcome are in
[Resume](#the-starting-session-rule-as-resume-applies-it); the defaults and
settings are in the [caller contract](#reading-a-refusal).

**The session-age helper and the pure rule**
(`internal/tmux/starting_session.go`). It reads no clock, no environment
and no configuration, and makes no tmux call, write or log:

- `SessionAge(now, created) Elapsed`: the age of a session from the
  listing's creation time (`Session.Created`, epoch seconds) as of `now`,
  the verb's injected instant. `Elapsed{Since, Future}` reports a time in
  the future as `Future`, never as a negative or clamped duration;
  `Under(limit)` is a strict less-than and is true for any `Future`
  value. Age never decides ownership; the label does.
- `StartingSession(StartingSessionInput) StartingSessionClass`. Inputs:
  `EndedAt` (the `ended_at` the verb examined before its move or reset;
  nil skips the window), `RecordsPID` and `RecordsSessionID` (neither
  skips the window), `Session` (the Ours session, or nil for "no session,
  agent process running", which skips the age step), `Now`, and the
  effective `Bound` and `Window`, used as given. Output: the outcome
  (`StillStopping`, `StillStarting` or `PastBoth`), whether the window was
  checked and the session present, the limits, and the measured
  `SinceEnd` and `Age`. The stopping window is checked first, then the
  bound.

**The shared refusal** (`pkg/api/starting_session.go`, unexported):

- `startingSessionLimitsOf(config.Tmux)` gives the bound and window
  (`startingSessionLimits`) through `EffectiveStartingSession` and
  `EffectiveStoppingWindow`, the only source of their defaults.
- `checkStartingSession(lim, now, row, session) startingSessionCheck`
  classifies a `startingSessionRow` (instance id, the name to quote,
  examined `ended_at`, `pid` and session-id presence, and `Consequence`,
  the sentence saying what the caller's state is: `""` means "nothing was
  done"; a refusal after writes passes its own, as `resume` passes its
  restore's sentence after "duplicate session").
- Its errors: `refusal()` for all three outcomes (still stopping or
  starting → `ErrTmuxUnresponsive`, retry later; past both → the own-id
  `ErrTmuxSessionConflict` "this row's own id", with the "Operator
  actions" pointer); `unavailableError()` for the first two only, nil
  past both; `ownIDConflictError()` for the third. No text names a
  session-ending command, another row's id or a label value, and none
  says "dead" or "gone".

Users: the pre-launch lookup of `resume` and of reuse (`decidePreLaunch`)
and their re-lookup after "duplicate session" (`heldNameOutcome` with a
`heldExaminedRow`, from `finishedLaunch.heldName`); and `kill`'s
finished-row opt-in (`finishedOurs` in `pkg/api/kill_optin.go`), which
uses `unavailableError` only, with the bound and window passed to `Kill`.

**Must use:** every verb that applies the starting-session rule must use
these components: the age through `tmux.SessionAge`, the classification
through `tmux.StartingSession`, the limits through
`startingSessionLimitsOf` (or the verb's effective values passed straight
through), and the refusals through `checkStartingSession`. No verb
re-implements the stopping window, the age calculation, the order of the
two checks or their refusal texts.

### Single-row verb helpers (`pkg/api`)

`kill` is the first live-row verb on the shared lookup, and the pane verbs
(`read-pane`, `send-keys`, `pause`) the next. The pieces they built are
shared, unexported helpers in `pkg/api`; `resume`, reuse, `expire`, the
sweeps and the finished-row opt-in reuse them. Their doc comments carry
the detail.

| File | Holds | Must use |
| --- | --- | --- |
| `lookup_outcome.go` | `cantTellError(res, cantTellRefusal{InstanceID, Context, Socket, Call, Consequence, Retry})`: the one mapping from a Can't tell lookup or pane-listing `Result` to its verb error (different server and tmux unavailable → `ErrTmuxNotAvailable`, conflicting labels → `ErrTmuxSessionConflict` naming the sessions or the scope, unreadable → `ErrTmuxUnresponsive` ending with the retry sentence); nil for any other verdict. `Consequence` `""` means "nothing was done"; a verb that already acted passes its own sentence. `Retry` `""` means the default `retryLater` ("retry later"); only a caller whose retry of the same call cannot work passes its own (plain spawn's held-name path, and `send-keys` after a keys failure that may have left its text typed, set by `keysReached` from `sendKeysNext`). It is verb-agnostic: kill, the label scan and resume's pre-launch lookup keep the default. `rowSocket(recorded)`: the socket every call for a row uses, the recorded one as is, else `spawn.ResolveQuerySocket("nothing was done")`. `rowLaunch(instanceID, id, storeID, socket) tmux.Launch`: the lookup's view of a row (instance id, the token and server identity of `id`, this store's id, the socket); `kill`, `read-pane`, `send-keys`, `pause`, `resume`, `find-missing` and `expire` build every lookup's `Launch` with it. The constants `nothingWasDone`, `operatorActionsPointer` and `listSessionNameHint` ("list tmux_session_name (--tmux-session-name on the CLI) shows whether a row uses a session name", built on `manifest.TmuxSessionNameSpelling`). | Every single-row verb maps a Can't tell lookup or pane listing through `cantTellError`, takes a row's socket from `rowSocket` and builds its `tmux.Launch` with `rowLaunch`; never write another Can't tell description, socket rule or launch builder. Plain spawn's label scan uses `cantTellError` too. |
| `unusable_name.go` | `unusableNameError(name)`: nil for a usable recorded name, else the verb-agnostic `ErrInternal` refusal (empty, control character, or which rewritten character via `tmux.RewrittenIn`), pointing to "Operator actions" and saying no tmux call was made. The verb prefixes the instance id. | Every verb that would look a row up refuses an unusable recorded name through it, before the socket and any tmux call: `kill` of a live row and, with the finished-row opt-in, of a finished row (`killRun.withOptIn`); `read-pane` (any state); `send-keys` after its state and relay guards; `pause` on a `waiting` row; `resume` first in `resumeAfterJsonl`, wrapped `resume: ` in place of the instance id; reuse in `reuseExamine`. Never write another unusable-name description. `find-missing` and `expire` refuse nothing and do not use it: they note or keep such a row through `tmux.Unusable` (`unusableNameNote`, `unusableKeptReason`). |
| `agent_pane.go` | `agentPane(panes, paneID, panePID)`: the agent's pane in one listing, by recorded pane id and pid, wherever it now is (SR-3.7). `findAdoption(recorded, res, panes, pc) adoption`: the one home of the adoption rules (SR-3.6), with no write: due when the lookup is Ours and the row records no server identity or no pane; the server identity from the answering server, the pane by `@ad_pane` token (`tmux.PaneByToken`), start times through `tmux.KnownStartTime`. `adoptIdentity(s, row, res, panes, pc) adoption`: the single-row verbs' step, `findAdoption` over the verb's own listing plus one `AdoptIdentityIfUnchanged` write; the found identity (`adoption.Identity`) is used for this call whatever the write's outcome, and `Applied` is set only when the write applied. `identityAdopter` is the store capability it needs. `adoptInSweep(s, sw, pl, it, launch, res, pc, lg) sweepAdoption`: `find-missing`'s step, `findAdoption` over the Sweep's pane listing (`sw.ListPanes`, at most one per socket per run) plus one `AdoptIdentityIfSameLife` write guarded on the snapshot the sweep read; `sweepAdoption` reports `Unlisted` (the listing did not answer: nothing adopted, no write) with the listing's `Listing` result, `Guard` (the snapshot the row's verdict write is guarded on: the adoption write's new one when it applied), `Write` and `Left` (the write found the row changed or absent, or failed: the row gets no verdict write). `sameLifeAdopter` is its store capability. | `findAdoption` holds the adoption rules; never decide or build an adopted identity elsewhere. `kill`, `send-keys` and `pause` find the agent's pane with `agentPane` and adopt with `adoptIdentity`, using the verb's own one pane listing; `read-pane` calls `findAdoption` directly and uses what it found for that call only, with no write (SR-7.5), as `resume`, reuse and `expire` must when they need an identity without writing it; a sweep adopts only with `adoptInSweep`. Never match panes or write an adoption by hand. |
| `pane_run.go` | `paneRun`: one pane-verb call's run, with `paneTmux` (`TmuxLookup` + `ListPanes`). It holds the row, the socket, the store id, the verb's gone sentinel (`ErrTmuxCaptureFailed` or `ErrTmuxSendKeys`), what its refusals say was not done (`paneNothing`) and an optional `adopter`. Methods: `launchFor` (through `rowLaunch`), `ours` (the one pane listing, adoption — `adoptIdentity` with an adopter, else `findAdoption` for the call only — then `agentPane`, or `paneNotFoundError`), `listPanes` (a failed listing classified by `tmux.ListingFailure`: Gone → the gone error, otherwise `cantTellError`), `goneError`, `refusal` and `cantTellRefusal`. `readPaneRun` embeds it. | Every pane verb runs its lookup's Ours step, pane listing, gone error and Can't tell mapping through `paneRun`; a new pane verb embeds it and keeps only its own Leftover handling and action. |
| `pane_keys.go` | `keysRun` (embeds `paneRun`), `newKeysRun(t, pc, row, storeID, socket, adopter)` and `keysFacts`: the tmux phase shared by the verbs that type keys (`send-keys`' text, `pause`'s `/exit`). `deliver(text)`: one lookup (`target`, holder name the recorded name), on Ours one listing and the adoption write, then, when `keysRun.clear` is set, the line clear (`SendKeyPane` of `lineClearKey`), then `SendKeysPane` by pane id; a failure of either is mapped by `sendFailed` through `paneActionFailureError` in Keys mode with the verb's next step; nothing sent before Ours and the agent's pane, and nothing after a failed line clear. `keysRun.leftover` is the verb's Leftover refusal (nil gives `paneLeftoverError`; `send-keys` sets `pendingLeftoverError` on a `pending` row). `keysRun.next` (a `keysNext`) is the verb's next step after a keys failure that may have left its text typed: `send-keys` sets `sendKeysNext` (`pane_action_failure.go`); the zero value, `pause`'s, keeps "retry later". `keysRun.clear` (a `lineClearTmux`; `pause` sets it to its `PauseTmux`, `send-keys` leaves it nil) sends `lineClearKey`, `C-u`, before the text (why, and what it does not clear: "Why `C-u` first" under [`pause`](#pause)). `keysFacts` keeps the socket, the first lookup, a failed listing, `Adopted`, the Ours session, `Sent` / `SendErr` (both unset after a failed line clear, which types no text) and the follow-up. `emitDisagree(verb, instanceID, who)`: the keys verbs' one `ad.provenance.disagree` emit, source `ad_send_keys`, reasons collected as `kill` collects them, action from `sendKeysAction` (`sendkeys_trail.go`: `keys_sent`, `text_sent`, `nothing_sent`). | A verb that types keys into the agent's pane runs `keysRun.deliver` and writes its disagree records with `keysRun.emitDisagree`; never a second keys phase, action mapping or disagree collection. A verb that must send the line clear first sets `keysRun.clear` rather than sending its own key call, and a verb whose literal retry would type its text twice sets `keysRun.next` rather than its own retry sentence. |
| `pane_refusals.go` | The pane verbs' refusal descriptions, verb-neutral through `paneRefusal{InstanceID, Name, Nothing}` and `paneNothing` (`nothingRead`, `nothingSent`, `textNotSubmitted`): `paneNotFoundError(r, notAdopted)` ("the agent's pane was not found", plus "the agent's pane was not adopted (no pane carries this launch's pane label)" for a lost create reply), `paneLeftoverError(r, leftovers, moreThanOne)` ("not this launch's session"; "more than one leftover session exists" for `read-pane`), `paneGoneError(gone, r, detail)` ("the row's session is not there", matching only the verb's gone sentinel). `leftoverSessions(leftovers)`: the one sentence naming leftover sessions, shared with `kill`'s `leftoverError`; `namedSessions(sessions, limit)`: quoted names and `$N`, then a count. `send-keys`' two `pending`-row `ErrSpawnNotInteractive` builders: `pendingNoLaunchError(instanceID)` (no launch start or token; no tmux call made) and `pendingLeftoverError(instanceID, leftovers)`. | No verb builds its own pane-verb refusal text: pane not found, Leftover and the gone error go through these builders, a Leftover sentence through `leftoverSessions`. Tests check them with `apitest`'s `DescPane*` and `DescSendKeysPending*` cases. |
| `pane_action_failure.go` | `paneActionFailureError(err, paneActionFailure{Call, Gone, Pane, Refusal, Keys, Next}, t, pc, launch)`: the SR-7.3 mapping of a failed action call (capture, line clear, text, Enter): a timeout → `ErrTmuxUnresponsive` with no follow-up; any other failure → exactly one follow-up lookup (Gone or Leftover → the verb's gone error; a different server or tmux unavailable → `ErrTmuxNotAvailable`; otherwise `ErrTmuxUnresponsive`); returns the follow-up (`paneFollowUp`) for the trail. `Keys` selects the "keys may have reached the pane" mode (`keysReached`): "the keys may have been delivered" after a timeout and "the text may be typed but not submitted" once the text went through (`enterFailedAfterText`). In that mode `Next` (a `keysNext{TextTimeout, NotSubmitted}`) is the verb's next step, which `keysReached` sets as `Refusal.Retry`, so an `ErrTmuxUnresponsive` that may have left the text in the pane ends with it in place of "retry later": `TextTimeout` after a timed-out text call, `NotSubmitted` after a failed or timed-out Enter; a field left `""` keeps "retry later". `sendKeysNext` holds `send-keys`' two: "read-pane; if the text is typed, send-keys with empty text, otherwise the same send-keys" and "send-keys with empty text submits it" (b.9o4). A failed line clear (`tmux.CallSendKey`, `pause`'s `C-u`) types no text: a timeout says "the keys may have been delivered", any other failure is described as a text call's that typed nothing, and both end "retry later". The reply text never classifies. | Every pane action failure goes through it (`read-pane` with `Keys` false; `send-keys` and `pause` with `Keys` true); later verbs whose action is a pane action (resume, reuse, expire, the sweeps) use it rather than their own timeout or follow-up rule. A keys verb's next step after a keys failure goes in `Next`, never in a sentence of its own. |
| `provenance_disagree.go` | `emitProvenanceDisagree(provenanceDisagree{...}, reasons...)` (callers: `kill`, `find-missing`, `expire` (`expireRun.emitRow`), plain spawn's held-name path, `resume` and reuse through `emitFinishedRowDisagree` (`finished_launch.go`, over `emitLookupDisagree`), and the keys verbs through `keysRun.emitDisagree`): writes one `ad.provenance.disagree` per distinct reason among the six (`disagreeReasons`), in a fixed order, nothing for none, dropping unknown reasons; fail-open; never a label's content. `nameChanged(res, recordedName)`: the `name_changed` condition (Ours under a name that is not a stored form of the recorded one, `tmux.StoredForms`). | A verb collects every reason of its call (lookups, a pane-listing failure, `adopted` when the adoption applied, `name_changed`) and calls the emitter once per call; a sweep calls it once per row. Never write the event directly. |
| `error_name.go` | `errorName(err)`: the err_name the CLI would print, for trail fields (`ad.kill.called`'s and `ad.send_keys.called`'s `outcome`, `ad.resume.restored`'s and `ad.spawn.reuse_restored`'s `launch_error`, `ad.launch.name_held`'s `outcome`); `ErrInternal` for anything else. `pkg/api` cannot import `errnames`, so this is its one mapping. | A trail field that carries an err_name uses it, and a verb that gains a name extends it here. |
| `caller_identity.go` | `callerIdentity()`: the invoking process's identity (`caller`: process name, pid, hostname, user), collected inside agent-director, never from caller-asserted params, for the trail's `caller_*` fields. `lazyCaller` (`get()`): a sweep run's caller identity, collected on first use and shared by every record of the run, so a run collects it at most once and a run that writes no record carrying it collects none; the zero value is ready, and it serves one run, not concurrently. `find-missing` and `expire` each hold one per run. | A verb's trail `caller_*` fields come from `callerIdentity`; a sweep holds one `lazyCaller` per run and calls `get()` only when a record needs it. Never collect the caller identity another way or once per row. |
| `starting_session.go` | `startingSessionLimitsOf`, `startingSessionRow` (with its `Consequence`), `checkStartingSession` and the `startingSessionCheck` errors `refusal`, `unavailableError` and `ownIDConflictError`: the shared starting-session refusal over `tmux.StartingSession` (see [Starting-session rule](#starting-session-rule-starting_sessiongo)). | Every verb that applies the starting-session rule must use them, with the age from `tmux.SessionAge`; never re-implement the stopping window, the age calculation or their refusal texts. |
| `resume_lookup.go` | `decidePreLaunch(res, preLaunchRow, pc, lim, now) preLaunchDecision`: a finished row's pre-launch decision from one lookup, "proceed" or the one refusal (see [The pre-launch lookup](#the-pre-launch-lookup)); `preLaunchRowOf(row, name, socket)`, whose `Name` is the recorded name every refusal quotes, and `preLaunchRow.HolderName`, the name the lookup was given as its holder name, quoted by the name-holder check on Gone (`""` means `Name`: resume leaves it empty, reuse sets the requested name); `lookupTrailFacts(res, name)`: the `ad.provenance.disagree` facts of a lookup of the recorded name (its reasons plus `name_changed`); `emitLookupDisagree(call, action, pre)`: one lookup's records through `emitProvenanceDisagree`; the action values `preLaunchActionRefused` / `preLaunchActionProceeded` (`preLaunchActionOf(err)`); `leftoverLostRace(examined, again, found, err)`: the decision of the one re-read after a Leftover refusal (`CondAbsent`, `CondChanged`, or 0 when the refusal stands), which each verb maps to its own lost-race error (`resumeLostRace`, `reuseLostRace`); `preLaunchHolderError` and `preLaunchLeftoverError(instanceID, leftovers, consequence)`, the Leftover refusal worded as plain spawn's label scan. | `resume`'s pre-launch lookup and reuse's old-row lookup (with its new-name pre-check) use them, and so do their re-lookups; a verb that examines a finished row before a launch routes its lookup through `decidePreLaunch`'s builders, its records through `emitLookupDisagree` and its lost-race re-read through `leftoverLostRace`, rather than its own holder, Leftover, trail-fact or race code. |
| `finished_launch.go` | What resume and reuse share once the write that begins their launch has applied (resume's move, reuse's reset): `finishedLaunchVerb` (the verb word, trail source, restore event, `ad.launch.name_held` launch, the restore sentences' write, the timeout's row sentence; the two values `resumeLaunchVerb` and `reuseLaunchVerb`, declared only here); `finishedLaunch`, built per call by `resumeDeps.launchOnto` / `reuseDeps.launchOnto`, with `outcome(out, req)` (the one mapping of the create's outcome: identity write, timeout, "duplicate session", restore then launch error), `restore(launchErr)` (the one restore attempt, its WARN line and the verb's restore event) and `heldName(req)` (the path after "duplicate session": one re-lookup, the restore, `heldNameOutcome`, the re-lookup's disagree records, one `ad.launch.name_held`); `restoreResultOf(res, rerr, priorState, launchWrite)` (the one mapping of a restore's result to its row sentence, `row_result` and store error); `emitFinishedRowDisagree` (one lookup's records for a launch onto a finished row, always naming the recorded name). | A launch onto a row examined as finished maps its create outcome, restores and handles "duplicate session" through `finishedLaunch`, adding a `finishedLaunchVerb` value for a new verb; never a copy of the outcome mapping, the restore sentences or the held-name path. |
| `spawn_reuse_errors.go` | The descriptions only reuse returns: `reuseLostRaceError(id)` (`ErrInstanceIdCollision`: "the row changed or was removed after this spawn examined it and nothing was changed"), `reuseChangeError(id, err)` (the one mapping from a failed reset to its two `ErrInternal` descriptions, "archiving the previous session failed" when `errors.Is(err, store.ErrReuseArchive)`, else "the reuse could not be applied", each "and nothing was changed", the store error added with `%v`), `reuseNothingChanged` and `reuseRowResetStaysPending` (the launch timeout's row sentence, "the row was reset; the row stays pending", passed to `spawn.LaunchTimeoutError`). | Reuse returns its lost race, change failures and timeout row sentence only through these; a later change to the reuse path never re-spells them or tells an archive failure apart by its text. |
| `kill.go` | `killPollInterval` (100 ms): the pause between two readings of `kill`'s process wait, a named constant, not a setting. `Client.sleep` (`time.Sleep` in production) is the pause, set in tests through `SetSleepForTest`. | A wait that polls a process uses the injected clock and sleep, never `time.Sleep` directly. |

`internal/spawn.ResolveQuerySocket(consequence)` resolves the caller's
socket as tmux does, creating nothing, for a row that records no socket;
its refusal ends with `consequence`. **Must use** it (through `rowSocket`)
for a query on such a row, never `ResolveLaunchSocket`.

### No-business-logic-in-cmd contract

The thin-shim rule is now grep-enforceable. In `cmd/agent-director/`
and `cmd/agent-director-admin/` source files (excluding `*_test.go`), the
following symbols must appear **only** in the named exemption sites:

| Symbol | Permitted in |
| --- | --- |
| `store.Open` / `store.OpenOrInit` | `runHook` only |
| `config.Load` | `runHook` and `newHookLogger` only; the Client's config load and logger bootstrap (Pin 3) are `internal/clisetup.Open`'s, which `setupClient` and `agent-director-admin` call |
| `tmux.New` | none — `cmd/` must not construct a tmux client directly; `pkg/api.New` owns it |

`cmd/agent-director-admin/` has no exemption site: none of the three may
appear there. Any occurrence outside those sites is a layer-boundary
violation and should be rejected at review. The enforcement command:

```sh
grep -rn "store\.Open\|config\.Load\|tmux\.New" cmd/agent-director/ cmd/agent-director-admin/ \
  | grep -v '_test\.go'
```

Expected output: only lines inside `runHook` and `newHookLogger` (and
comments that name them).

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

**No usable home.** With `HOME` unset or empty, a `~/` path from any tier
(or a `~/` `ConfigPath`) is never resolved against another home: `New`
fails instead (the store's rule is "No usable home" under
[internal/store](#internalstore)). A `~/` `ConfigPath` (the default, and the
one the CLI and admin binaries pass) is refused first
(`api: expand config path: …`), so those binaries fail with `ErrStoreOpen`.
A library caller that passes an absolute `ConfigPath` and no `StorePath`
gets past that, but a `~/` `db_path` (the default) stays unexpanded,
reaches the store and is refused: `New` fails with `api: open store: …`
wrapping the store's `errNoHome`.

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
    session ran. A life runs from the spawn that created the row, or from a
    reuse's reset, to the next reuse. The rotation archive, inside SessionStart's gated write,
    takes the outgoing session id from the snapshot the hook examined and
    that session's jsonl path and life from a read pinned to that same
    snapshot, so all three belong to one row version. The reuse archive,
    inside `ResetForReuse`'s transaction, takes the current session id,
    path and life from its own in-transaction read and tags the entry with
    the life the reuse ends; it moves an entry archived in an earlier life
    to that life. The reset is the only write that starts a new life
    (`life_number` + 1), so a reused row's visible history starts empty and
    no earlier life's entry is ever shown or tried again. An applied restore
    after a failed reuse writes the pre-reuse life number back, so that
    life's visible history is exactly as before the attempt (the entry the
    reuse archived is the row's current session id, which the visible
    history drops). A restored number is safe to hand out again: the next
    reset starts the same life number afresh, and no entry belongs to the
    abandoned attempt's life, because the reset row records no pane, so no
    hook (and so no rotation archive) applies to it before the restore.
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
    policy. Two archives call it: the rotation archive, on SessionStart's
    transaction (fail-open: a history error still commits the
    SessionStart write and emits `ad.session.archive_failed`), and the
    reuse archive, on `ResetForReuse`'s transaction through
    `connQuerier` (fail-closed: a history error rolls the whole change
    back and returns an error holding `ErrReuseArchive`). Every new code
    path that archives a session must call it too, never a second upsert.
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
`launch_started_at`) and the rotation and reuse archives, which write
`session_history.life_number` through `upsertSessionHistoryEntry`. Verbs
report two of the columns, and only for display: `status`, `get` and `list`
report the launch start as `launch_started_at`, and `get` alone reports the
recorded socket as `tmux_socket` (see "Where callers see the launch start"
and "Where the socket is shown" under [Launch identity](#launch-identity)).
No verb reports any other v5 column.

- **Row version** — `spawns.row_version INTEGER NOT NULL DEFAULT 0`. A
  per-row change counter, so a conditional write can tell that the row changed
  since it was read.
- **Launch start** — `spawns.launch_started_at INTEGER` (nullable). The time,
  in epoch milliseconds, that the row's current launch began; NULL when no
  launch is in progress.
- **Life numbers** — `spawns.life_number` and `session_history.life_number`,
  both `INTEGER NOT NULL DEFAULT 0`. The row's current life and the life each
  history entry belongs to, so the history of a reused id can be split by
  life. `spawns.life_number` changes only through reuse: the reset
  (`ResetForReuse`) advances it by one, and an applied restore after a
  failed reuse (`RestoreAfterFailedReuse`) sets it back to the pre-reuse
  value. A `resume` keeps it.
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
for NULL), `Snapshot` and `Identity`. Two writes take a `Spawn`:
`InsertPending` and `ResetForReuse` (its `fresh` row), each taking
`LaunchStartedAtMillis`, `NoPreTrust`, `Identity.Token` and
`Identity.Socket` from it; no write takes the other v5 fields from one.
Reuse reads a finished row through its own narrow read, `ReadForReuse`
(`store.ReuseRow`, with the raw `store.RawLife`; see "Versioned writes"). A caller passes `Snapshot` to `MoveToPending`, and builds the `store.ResumePrior` that
`RestoreAfterFailedResume` takes from `EndedAtText`, `Identity` and the other
fields the move clears. No verb reports these fields, with two exceptions.
The launch start: `get` and `list` report `LaunchStartedAtMillis` as
`launch_started_at` on a `pending` row, and `status` reports it through
`SpawnStatus` (see "Where callers see the launch start"). The socket: `get`
reports `Identity.Socket` as `tmux_socket` on a row in any state (see "Where
the socket is shown").

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
- **SR-5.5 narrow reads.** A `launch_started_at` that is NULL, is not an
  integer, or is an integer outside the years 0 to 9999 UTC reads as absent
  (0). The range is inclusive, from `minLaunchStartedAtMillis` (the first
  millisecond of year 0) to `maxLaunchStartedAtMillis` (the last millisecond
  of year 9999), both in `internal/store/rowsnapshot.go`. A time outside it
  has no RFC3339 form, so it would fail JSON encoding. Only a hand edit can
  store such a value, and it never fails a read or a `status`, `get` or
  `list` result. A `launch_token` that is not exactly 16 lowercase hex
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
  an `any` and never fails). The year 0 to 9999 range rule lives only in
  `decodeLaunchStartedAt`, so every consumer inherits it: `SpawnStatus`,
  `GetSpawn`/`ListSpawns` (through `scanSpawn`), `ListLiveSpawnIdentities`
  (`find-missing`'s live-row read, in `internal/store/recovery.go`) and
  `resume`'s refusal, which reads the row with `GetSpawn`. A new read
  returning a `Spawn` selects `spawnColumns` and scans with `scanSpawn`. Never
  re-derive the rules in a new read.
- **Must use:** `store.InsidePendingGrace` (in
  `internal/store/rowsnapshot.go`) is the only statement of the SR-11.2 rule:
  a `pending` row is inside its grace period while its age, measured from the
  launch start, is below the grace period. Any check of whether a `pending`
  row is inside its grace period calls it, passing the decoded launch start,
  the grace period and the caller's clock. Never re-derive the age
  arithmetic. It has two callers, both with the effective pending grace
  period (SR-13.4): `find-missing`'s step 2 (see
  [`find-missing`](#find-missing)) and the hook's SessionStart wait for its
  launch's identity write (`waitingForIdentity` in
  `internal/hook/handler.go`, which adds the no-pane condition; see [Hooks
  move a row only for its own
  agent](#hooks-move-a-row-only-for-its-own-agent)).
- **The live-row read** (SR-11.7; `internal/store/recovery.go`).
  `ListLiveSpawnIdentities` returns one `store.LiveSpawnIdentity` per row
  in a live state, `pending` included, in no set order: the instance id,
  `State`, the SessionStart identity (`PID`, `ProcStarttime`),
  `LaunchStartedAtMillis` (through `decodeLaunchStartedAt`, 0 = absent),
  `TmuxSessionName`, `LivenessNote` (`""` = NULL), `Snapshot` (the snapshot
  every guarded write of the sweep compares against) and `Identity` (the
  launch identity, token through `decodeLaunchToken`). Snapshot and
  identity are selected through `lifeColumns`, as every read returning a
  `Spawn` selects them, so both equal `GetSpawn`'s for the same row. No
  stored launch start, token or identity value fails the read, and the
  structured columns (`labels`, `claude_args`, `extra_env`) are never read.
  The struct grows additively.

**Versioned writes (b.fmk, SR-5.2).** The row version exists so that a later
compare-and-set write can guard on it: the write checks that the version is
still the one it read, so it knows the row has not changed since. As built:

- Every statement that updates a `spawns` row advances `row_version` by
  exactly one in that same statement, with no transaction added. The shared
  SET fragment is `rowVersionAdvance` in `internal/store/spawns.go`. This
  covers every branch of `ApplyHookTransition` / `ApplyHookTransitionResult`
  (state transitions, the `ended` transition, soft refreshes),
  `ApplyHookWaitingIfWorking`, `RecordSessionStartIdentity`, `SetParentID`,
  `HealJsonlPath`, and
  `RecordLaunchIdentity`, `MoveToPending`, `RestoreAfterFailedResume`,
  `ResetForReuse`'s reset, `RestoreAfterFailedReuse`,
  `EndHeldLaunch`, `AdoptIdentityIfUnchanged` and `find-missing`'s four
  guarded writes `MarkMissingIfSameLife`, `SetLivenessNoteIfSameLife`,
  `ClearLivenessIfSameLife` and `AdoptIdentityIfSameLife` (each when it
  applies; the clear advances the version whether or not a note was set).
- `InsertPending` starts a row at 0 (the column default). A path that writes
  nothing advances nothing. Examples are the `working`-transition hold path
  (open permission requests), a write whose `WHERE` matches no row (a
  hook the gate did not apply included), and `HealJsonlPath` or any
  guarded write (`find-missing`'s four, resume's, the end write, the
  adoption) when its guard misses. `DeleteSpawn` removes the row, and so
  does `expire`'s `DeleteFinishedIfSameLife` when it applies; when its
  guard misses it removes nothing and answers `CondChanged` (the row is
  live or its snapshot differs) or `CondAbsent` (no row).
- Archiving the prior session into `session_history` on a rotation does not
  advance the version by itself. The `RecordSessionStartIdentity` update it
  belongs to advances it once. The same holds for the reuse archive and the
  permission-request deletion: `ResetForReuse`'s reset UPDATE advances the
  version once for the whole change.
- The foreign-key action that clears a child's `parent_id` when its parent
  is deleted (`ON DELETE SET NULL`) does not advance the child's version.
  `parent_id` is not in `RowSnapshot`.
- Launch-start rule: every write that sets `state` to a value other than
  `pending` also sets `launch_started_at` to NULL in the same statement
  (shared fragment `launchStartClear`). These writes are the `ended`
  transition, every other hook transition whose target is not `pending`,
  `MarkMissingIfSameLife`, `RestoreAfterFailedResume`,
  `RestoreAfterFailedReuse` and `EndHeldLaunch`. Every other write leaves
  `launch_started_at` unchanged, except the three that set a launch start:
  `InsertPending` (plain spawn), `ResetForReuse` (reuse's reset) and
  `MoveToPending` (resume's move).
- `InsertPending` writes `launch_started_at`, `launch_token` and
  `tmux_socket` in the INSERT (zero values as NULL) and never the six
  identity columns. The same INSERT records the caller's pre-trust choice:
  `no_pre_trust` is 1 when `Spawn.NoPreTrust` is true, else 0. `RecordLaunchIdentity(instanceID, launchVersion, token,
  identity)` is the one update that records a launch's six tmux server and
  pane identity columns: one UPDATE, guarded on `state = 'pending'`, the given
  `row_version` and the given `launch_token`, that advances `row_version`
  (zero values written as NULL). When it updates nothing, one follow-up
  read tells `CondChanged` from `CondAbsent` (`store.CondResult`,
  `rowsnapshot.go`; every conditional write shares the helper
  `condNotApplied`). Only reuse's two writes touch `life_number` and
  `no_pre_trust` after the insert: the reset writes life + 1 and its own
  call's choice, and an applied restore writes the pre-reuse values back.
  So the choice of the spawn or reuse that began a life holds for that
  life.
- Resume's writes (SR-8.3, SR-8.5; `internal/store/resume_writes.go`;
  called only by `resume`, see [Resume](#resume)). `MoveToPending` moves
  an `ended` or `missing` row whose `RowSnapshot` still equals the
  examined one to `pending`: it sets the launch start, `launch_token`, `tmux_socket` and `parent_id`, and clears `pid`,
  `proc_starttime`, `ended_at`, the liveness columns and the six identity
  columns. It returns the version it produced. `RestoreAfterFailedResume`,
  guarded on `pending` at that version, writes a `ResumePrior` back (state,
  cleared columns, token, socket, identity; `parent_id` keeps the move's value)
  and clears the launch start. Neither emits a trail event.
- Reuse's read, change and restore (SR-10.2 to SR-10.4, SR-5.3, SR-5.6,
  SR-5.8, SR-5.9; `internal/store/reuse.go`; called only by `spawn` with
  the reuse opt-in, see [Reuse of a finished id](#reuse-of-a-finished-id)).
  None emits a trail event.
  - `ReadForReuse(id) (ReuseRow, found, error)`: the one pre-check read,
    one SELECT by primary key. `ReuseRow` holds `State`, `EndedAtText`
    (`ended_at` as stored text, `""` for NULL), `EndedAt` (as the driver
    parses it, the way `scanSpawn` does; nil for NULL or a value that does
    not parse), `Snapshot` and `Identity` (through `lifeColumns`, so equal
    to `GetSpawn`'s), and `Life`, a `RawLife`: every column the restore
    writes back, each selected as `typeof(col), +col` so the stored value
    and storage class come back unparsed (NULL distinct from empty, a
    zero-length blob kept a blob). It never decodes `labels`,
    `claude_args` or `extra_env`, and no stored timestamp, token or
    pre-trust value fails it, so a hand-edited malformed row can be reused
    and restored byte for byte. No row: `found` false, no error.
  - `ResetForReuse(id, examined, fresh) (CondResult, archivedSessionID,
    resetVersion, error)`: the change, one transaction. It encodes the
    request fields first (a failure begins nothing), then takes one
    connection from the pool and runs `BEGIN IMMEDIATE`, so it holds the
    write lock before it reads (see the write-lock-before-read rule
    below). Its in-transaction read takes the session id, path and
    `life_number` and decides, with `finishedStateGuardSQL` and
    `snapshotMatchSQL`, whether the row is still finished with the
    examined snapshot: no row is `CondAbsent`, a live or changed row
    `CondChanged`, both writing nothing. Otherwise, in order: the keyed
    archive of a non-empty session id in the ending life through
    `upsertSessionHistoryEntry` (fail-closed; its error holds
    `ErrReuseArchive`); the reset UPDATE, still guarded and required to
    change exactly one row: state `pending`; `claude_session_id`,
    `jsonl_path`, `pid`, `proc_starttime`, both liveness columns,
    `ended_at` and the six server and pane identity columns NULL;
    `started_at` and `last_seen_at` from `fresh.StartedAt` in the store's
    layout; `launch_started_at` from `fresh.LaunchStartedAtMillis`; the
    request fields and `parent_id` from `fresh`; `no_pre_trust` from this
    call; `launch_token` and `tmux_socket` from `fresh.Identity`;
    `life_number` + 1; `row_version` + 1; then the deletion of every
    permission request of the id, decided or not; then COMMIT. Applied, it
    returns the archived session id (`""` when none) and `resetVersion`,
    the examined version + 1. Any failure rolls everything back and
    returns a wrapped error with a zero `CondResult`; only an archive
    failure holds `ErrReuseArchive`. Children keep their `parent_id`; the
    store reads no clock.
  - `RestoreAfterFailedReuse(id, resetVersion, prior, failedAt)`: one
    UPDATE, guarded on `state = 'pending'` and `row_version =
    resetVersion`. It writes every `RawLife` column back exactly as
    stored (state, `life_number`, `no_pre_trust`, the session id and path,
    pid and start time, `started_at`, `last_seen_at`, the liveness
    columns, the request fields, token, socket and the six identity
    columns); `ended_at` as stored, or `failedAt` when it was NULL;
    `parent_id` only while that row still exists (else NULL, decided in
    the same statement, so the foreign key never fails it); clears
    `launch_started_at`; advances `row_version`. It never touches
    `session_history` (the archived entry stays) or `permission_requests`
    (the deleted requests stay deleted). A zero `RawLife`, or one whose
    state is not finished, is refused with an error and nothing written.
    Not applied: `CondChanged` or `CondAbsent` through `condNotApplied`; a
    store failure: a wrapped error, the row left as the reset left it.
  - **The fail-closed archive versus the fail-open rotation archive.** Both
    write through `upsertSessionHistoryEntry`. A rotation archive failure
    never blocks the agent's SessionStart; a reuse archive failure stops the
    whole change, because a reset that lost the old session's entry would
    orphan its transcript.
  - **Must use:** `ErrReuseArchive` (`internal/store/spawns.go`) is the one
    marker of a failed reuse archive. It is not a catalogued sentinel and
    is never aliased in `pkg/api`; a caller tells an archive failure apart
    only with `errors.Is` (as `reuseChangeError` does), never by text.
  - **Must use: the write lock before the read.** A store write that must
    read and change a row with no other writer in between, and must never
    fail because another process committed after its read began, takes
    one connection with `s.db.Conn`, runs `BEGIN IMMEDIATE` on it, runs
    every statement on that connection (helpers that take a `querier` get
    the `connQuerier` adapter, `internal/store/session_history.go`), never
    goes through the pool inside it (the pool has one connection and would
    wait for itself), and rolls back with `rollbackConn`, which discards a
    connection whose ROLLBACK failed so a stuck transaction never returns
    to the pool. `ResetForReuse` is the pattern's one user; no other store
    transaction's locking changed.
- The held-launch end write (SR-9.4, SR-5.8, SR-22.3;
  `internal/store/spawns.go`, `endHeldLaunchSQL`). A plain spawn whose
  create answered "duplicate session" calls
  `EndHeldLaunch(instanceID, insertLaunchStartedAtMillis, endedAt)` (see
  "Held name" in [Launch identity](#launch-identity)). It is one UPDATE,
  guarded on `state = 'pending'`, `row_version = 0` and `launch_started_at`
  equal to the insert's launch start (a delete and a fresh insert restart
  the version at 0; the launch start tells the two lives apart). It sets
  `state` to `ended`, `ended_at` to `endedAt` in the store's
  CURRENT_TIMESTAMP layout (`storeTimestamp`: UTC, whole seconds, so
  `expire`'s text comparison selects the row as it selects one the `ended`
  hook transition wrote), clears `launch_started_at` and advances
  `row_version`. It writes no other column; the server and pane identity
  columns stay NULL. It returns `CondApplied`, or, having written nothing,
  `CondChanged` (another versioned write came first, or the row was
  deleted and inserted afresh) or `CondAbsent` (no row), told apart by
  `condNotApplied`; a driver error is returned wrapped with a zero
  `CondResult`. It emits no trail event and makes no tmux call.
- The adoption write (SR-3.6; `internal/store/spawns.go`).
  `AdoptIdentityIfUnchanged(instanceID, examined, identity)` records a
  launch identity a verb found for a lost create reply: one UPDATE, guarded
  by `snapshotMatchSQL` on the `RowSnapshot` the verb examined, writing the
  six server and pane identity columns (zero values as NULL) and advancing
  `row_version`. It never writes the token, the socket or the state, and
  emits no trail event. When nothing applied, `condNotApplied` tells
  `CondChanged` from `CondAbsent`; a driver error returns a zero
  `CondResult`. **Must use:** only `kill`, `send-keys`, `pause` and
  `find-missing` write an adoption (SR-3.6): the single-row verbs through
  `pkg/api`'s `adoptIdentity` and `find-missing` through `adoptInSweep`
  (with `AdoptIdentityIfSameLife`), both on the rules of `findAdoption` (see
  [Single-row verb helpers](#single-row-verb-helpers-pkgapi)); never write
  the identity columns another way.
- `find-missing`'s guarded writes (SR-11.3, SR-11.4, SR-11.6, SR-3.6,
  Appendix F.4; `internal/store/find_missing_writes.go`). Each is one
  UPDATE guarded on a live state (`liveStateGuardSQL`: `state` is one of
  `liveStates`, `pending` included) and on the `RowSnapshot` the sweep
  examined (`snapshotMatchSQL`), and each advances `row_version` by exactly
  one. None emits a trail event or makes a tmux call; the ticks and the
  permission-request denial stay with the caller.
  - `MarkMissingIfSameLife(instanceID, examined) (priorState, CondResult,
    error)`: sets `state` `missing`, `ended_at` and `last_seen_at` to
    `CURRENT_TIMESTAMP`, and in the same statement NULLs both liveness
    columns and `launch_started_at` (`launchStartClear`). It returns the
    state it replaced, read just before the statement; that read never
    decides whether the mark applies, the guard alone does.
  - `SetLivenessNoteIfSameLife(instanceID, examined, note)`: overwrites
    `liveness_note`, keeps `liveness_unverified_since` when set and sets it
    to `CURRENT_TIMESTAMP` when NULL. It writes even when `note` equals the
    stored note; skipping an equal note is the caller's rule.
  - `ClearLivenessIfSameLife(instanceID, examined)`: NULLs both liveness
    columns, whether or not a note was set; not writing a row with no note
    is the caller's rule.
  - `AdoptIdentityIfSameLife(instanceID, examined, identity) (CondResult,
    now RowSnapshot, error)`: writes the six server and pane identity
    columns through `adoptIdentitySet`, the assignment it shares with
    `AdoptIdentityIfUnchanged`, and never the token, socket, state,
    liveness columns or launch start. When it applies it returns `now`, the
    row's snapshot after the write (`examined` with `RowVersion` one
    higher, no read needed), which guards the row's verdict write.

  The note write, the clear and the adoption leave `launch_started_at`
  unchanged, and none of the four writes `life_number`, `no_pre_trust`,
  `launch_token`, `tmux_socket` or a request column. Results: `CondApplied`;
  `CondChanged`, having written nothing, when the row exists but is
  terminal or its snapshot differs; `CondAbsent` when no row has the id
  (both through `condNotApplied`; the mark answers `CondAbsent` from its
  state read). A store failure is returned as a wrapped error with a zero
  `CondResult`, never as a `CondResult` value (SR-5.8); the prior state
  is `""` and `now` the zero snapshot on anything but `CondApplied`.
  **Must use:** `find-missing` writes a live row only through these four,
  reached through `pkg/api`'s per-row outcome writers and `adoptInSweep`
  (see [`find-missing`](#find-missing)); `CloseOrphanedPermissionRequests`
  runs only after a mark that applied. A new sweep write guarded on the
  life it read is a new statement here built from `liveStateGuardSQL` and
  `snapshotMatchSQL`, never an unguarded update.
- `expire`'s candidate read and conditional delete (SR-12.1, SR-12.3,
  SR-5.3, Appendix F.4; `internal/store/expire.go`; see
  [`expire`](#expire)). `ListExpireCandidates(cutoff)` reads every finished
  row (`finishedStateGuardSQL`: `ended` or `missing`) whose `ended_at` is
  set and older than `cutoff`, in instance-id order, through `lifeColumns`
  (the fragment `GetSpawn` selects), so each `ExpireCandidate`'s snapshot
  and identity equal `GetSpawn`'s; it decodes no label, `claude_args` or
  `extra_env`, and the store never reads the clock (to select every
  finished row the caller passes a far-future cutoff).
  `DeleteFinishedIfSameLife(instanceID, examined)` is one `DELETE` guarded
  on `finishedStateGuardSQL` and `snapshotMatchSQL`: `CondApplied` when it
  deleted the row; `CondChanged` (live or `pending` now, or its snapshot
  differs) or `CondAbsent` through `condNotApplied`, having deleted
  nothing; a store failure as a wrapped error with a zero `CondResult`.
  Neither emits a trail event or makes a tmux call. **Must use:** a delete
  of a finished row decided on a judgement goes through
  `DeleteFinishedIfSameLife`; its version cases are in
  `internal/store/row_version_expire_test.go`. `finishedStateGuardSQL`
  (with `finishedStateGuardArgs`) is the one finished-state guard:
  `expire`'s read and delete, `resume`'s move (`MoveToPending`) and
  reuse's reset (`ResetForReuse`) use it; a new write guarded on a
  finished row uses it too, never an inline `state IN (...)`.
- **Must use:** every write guarded on a full `RowSnapshot` uses
  `snapshotMatchSQL` with `snapshotMatchArgs` (`rowsnapshot.go`), and any read
  that fills a `RowSnapshot` selects its columns with the `spawnColumns`
  expressions. The guard compares through those same expressions (`CAST` for
  `started_at`, `COALESCE` to the zero value for nullable columns); a snapshot
  read any other way never matches.
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
`~/` in the path given to `Open` or `OpenOrInit` is expanded against `$HOME`
(`os.UserHomeDir()`). This is the same rule `pkg/api` and `internal/config`
use, so a caller that redirects `HOME` reaches the store under that home, not
the real one. Any other path is used as given. With `HOME` set, the
production callers (`pkg/api`, and `runHook` via `internal/config`) already
expand `~/` before calling the store.

**No usable home.** With `HOME` unset or empty, `os.UserHomeDir()` fails and
none of the three resolves `~/`: `pkg/api` returns an error,
`internal/config` leaves the path unexpanded, and the store's `expandTilde`
refuses it. `Open` and `OpenOrInit` then return an error wrapping the
unexported `errNoHome` (`store: resolve path: no home directory for
"~/…": $HOME is not defined`) and open or create nothing. There is no
fallback to another home source such as the passwd entry,
`user.Current().HomeDir` (b.8dr incident class, b.4uz). What `runHook`
does then is under [Fail-closed boundary](#fail-closed-boundary), and what
`pkg/api.New` returns under
[`pkg/api` Client lifecycle](#pkgapi-client-lifecycle).

The audit trail (`internal/trail`) also writes nothing when there is no
absolute home (see [Location](#location)).

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

**Help size guard.** `TestHelpSizeGuard` in
`cmd/agent-director/help_size_test.go` measures `agent-director help`'s
stdout and runs two checks. Both must pass:

1. **Hard cap.** The test fails when help stdout is larger than
   `helpHardCap`, the user's cap of 15160 B. There is no slack: 15160 B
   passes and 15161 B fails. The failure message says to trim help.
2. **Baseline.** The test fails when help stdout grows more than 10% past
   the byte count recorded in `helpStdoutBytes` (SR-20.6). This check
   catches growth relative to the last recorded size.

The hard cap is the binding limit: while the baseline sits near the cap,
baseline + 10% is well above it, so a growth that the baseline check
allows still fails on the cap. The test logs the measured size, the
baseline, the baseline limit and the cap on every run.

**Approval rule for the cap.** `helpHardCap` moves only with the user's
approval. The change is recorded in the constant's doc comment in the
test, with the date and the reason. A Task whose description text grows
help must trim other description text to fit at or under the cap, with no
change in meaning. If it cannot fit, the Task stops and reports the
measured size; it never raises the cap on its own.

**Re-recording the baseline.** `helpStdoutBytes` is re-recorded to the
newly measured count in the same commit that changes the size (SR-20.6,
decision-0930e):

- Downward, after a trim: free, no approval needed.
- Upward: only with the user's approval given before the commit,
  and a reason recorded in the commit message (what grew, by how many
  bytes, and why it cannot be shorter). An upward re-record never lifts
  the hard cap.
- Never to make a failing guard pass.

The baseline check exists to stop silent creep: several upward
re-records, none of them large, can add up to undo a trim. The hard cap
puts a fixed upper limit on that. To keep repeated text out of every
description, a statement several verbs need goes into one constant in
`pkg/api/manifest/manifest.go` with a short form (for example
`liveRowSequence` and `missingNotProofShort`), and the full text lives in
one description, the README and this document (see
[Live-row sequence](#live-row-sequence) and
"`missing` is a judgement, not proof" in
[Degraded-mode reconciliation + cron user](#degraded-mode-reconciliation--cron-user)).
A statement several texts carry word for word is one constant too:
`sameEnvConsequences` holds SR-18.7's two consequences of the same-user,
same-tmux-environment requirement (see
[Same environment](#same-environment)), and the `kill` and `find-missing`
descriptions and spawn's `reuse_finished` parameter text append it.
**Must use** `sameEnvConsequences` for any further text that states those
two consequences; never a second copy of the sentence. Go checks of it go
through the `apitest` constants `finishedRowNotVerification` and
`wrongServerSecondAgent`.

**Param names.** A param's `Name` is its MCP argument name, used exactly,
and has no dash: underscores only (`claude_instance_id`,
`reuse_finished`), pinned by `TestManifestParamNamesHaveNoDash`. The CLI
flag is the same name with dashes (`--reuse-finished`), and the
TypeScript client's field is the manifest name. Spawn's `relay_mode`,
`extra_env`, `no_pre_trust`, `tmux_session_name` and `reuse_finished`, and
list's `tmux_session_name`, were dashed before 0.11.0; each of their texts
ends with an older-serve sentence, built from `olderServeRenamed` or
`olderServeUndecoded` over `olderServeReleases`, saying what a `serve`
process from before the change does with the param (see
[Parameter names and unknown arguments](#parameter-names-and-unknown-arguments)).

**The reuse opt-in's one spelling.** `ReuseOptInSpelling`
("reuse_finished (--reuse-finished on the CLI)") is how every text that
every surface shows names the reuse opt-in: the param name, which MCP
takes and the TypeScript client's field shares, then the CLI flag. The
spawn and kill (live-row step 6) Descriptions use it, and
`internal/spawn.ReuseOptIn` builds the runtime retry sentences on it.
**Must use** `ReuseOptInSpelling` (or `spawn.ReuseOptIn` in an error
description) for any further text that names the opt-in; never one
surface's spelling alone, which another surface refuses with
`ErrInvalidFlags`. `TestManifestParamReuseOptInOneSpelling` checks every
Description.

**The session-name param's one spelling.** `TmuxSessionNameSpelling`
("tmux_session_name (--tmux-session-name on the CLI)") is how every runtime
text that every surface shows names the session-name param, in the same
form as the reuse opt-in (b.ro3). `pkg/api`'s `listSessionNameHint`, the
list hint that ends every refusal left for a human to look at
(conflicting labels, a leftover session, a pane not found, a starting or
never-reported-in session, a held name), and `internal/spawn`'s
`ErrTmuxSessionNameEmpty` description are built on it.
**Must use** `TmuxSessionNameSpelling` (or `listSessionNameHint` for the
list hint) for any further text that names the param; never one surface's
spelling alone.

**Params named in Descriptions.** A verb or param Description that names a
param gives its manifest name, either bare (make-template's "Per-call cwd
overrides.", expire's "older_than overrides") or with the CLI flag as an
aside, `<param> (--<flag> on the CLI)` (make-template's per-call
`claude_args`); never the CLI flag alone. `TestManifestTextsNameParamsNotFlags` checks
every verb and param text, on the manifest and in `surface.json`. Another
program's flag, such as claude's `--settings`, is not a param and is not
checked.

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
  matching `VerbDef.Params` literal, named with underscores, never a dash.
- Do not add an MCP-exposed param without decoding it in
  `LiveDispatcher.Call` through `decodeParams`: `TestMCPParamParity` fails
  for a param the dispatcher drops or whose wrongly typed value is not
  refused with `ErrInvalidFlags`.
- Do not give an MCP-exposed param a manifest `Type` that
  `goTypeToJSONSchema` does not map, such as an annotated
  `[]string (k=v)`; state an entry's form in the Description instead.
  Such a param gets no type in `tools/list`, and `TestMCPParamTypesAgree`
  fails for it (see
  [Parameter names and unknown arguments](#parameter-names-and-unknown-arguments)).
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
    internal/probe   - single-process readers: the start-time reader,
                       the command-name reader and the parent-pid reader
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
| Static CLI and operator tool (host) | `make build` | `CGO_ENABLED=0` | `bin/agent-director`, `bin/agent-director-admin` |
| Release cross-compile (3 platforms, 6 binaries) | `make release-binaries` | `CGO_ENABLED=0` | `dist/agent-director-{linux-amd64,linux-arm64,darwin-arm64}`, `dist/agent-director-admin-{linux-amd64,linux-arm64,darwin-arm64}` |

Both binaries of a build carry the same version stamp. The CLI and the
operator tool are statically linked everywhere (pure-Go SQLite via
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

### Operator tool `agent-director-admin`

`agent-director-admin` (`cmd/agent-director-admin`, b.vqr) is a second
binary for the two operator-only actions: `kill-finished
--claude-instance-id <id>` (kill's finished-row opt-in; see step 2 of
[`kill`](#kill)) and `delete --claude-instance-id <id>...` (see
[`delete`](#delete)), plus `help` and `version`. Its generated reference is
`docs/admin-reference.md`. It exists so that the agent surfaces keep full
CLI/MCP parity: the `agent-director` CLI, MCP, the Go client and the
TypeScript client expose exactly the same verbs and parameters, with no
hidden or CLI-only flag, and none of them can run either action.

- **Not an agent surface.** Its verbs are not manifest verbs. Its verb
  list, global-flag list and opening statement live in `internal/adminapi`
  (`Verbs`, `GlobalFlags`, `ApprovalStatement`), and `tools/gen-docs`
  renders `docs/admin-reference.md` from them. Nothing in the manifest,
  `surface.json`, `help`, `docs/cli-reference.md` or
  `docs/mcp-reference.md` names the binary, its verbs or the opt-in. The
  checks of whole agent-facing outputs share one pattern,
  `apitest.OperatorActionNames`: `pkg/api/manifest`'s
  `manifest_optin_absent_test.go` (the manifest and `surface.json`: no
  `delete` verb and no such name), `cmd/agent-director`'s `help_test.go`
  (`TestHelpOmitsKillOptIn`: `help` and `--help` list no `delete` and name
  none) and `tools/gen-docs`' `TestGenerate_OperatorActionsAbsent`. The
  surfaces are pinned by `pkg/api`'s `operator_surface_absent_test.go`
  (`KillParams` has only `ClaudeInstanceID`; no exported `Delete`),
  `cmd/agent-director`'s `operator_actions_absent_cli_test.go` (`delete` is
  `ErrUnknownVerb`) and `kill_optin_flag_cli_test.go` (every spelling of
  the opt-in is `ErrInvalidFlags`, with no tmux call), `internal/mcp`'s
  `operator_actions_absent_test.go` (no `delete` tool; a `delete` call is
  an unknown tool) and the TS client's `operator-actions-absent.test.ts`.
- **The structural parity guard.** `cmd/agent-director`'s
  `cli_manifest_parity_test.go` keeps the main CLI at parity with the
  manifest, so a hidden or CLI-only flag cannot come back.
  `TestCLIFlagsAreManifestParams` type-checks every non-test file of
  `cmd/agent-director` (a file this platform's build excludes is checked by
  syntax: it must not import `flag` or call a method shaped like a flag
  registration) and fails on: a main-CLI flag that is not its verb's
  manifest param (dashed); a manifest param the CLI does not register as a
  flag, bar the three it takes otherwise (`spawn`'s `claude_args` after
  `--`, `trail-emit`'s positional `sub_verb`, `hook`'s `stdin`); a flag
  with empty usage text; a main-CLI verb that is not in the manifest, or a
  manifest verb that is not a main-CLI verb; and any flag set it cannot
  tie to a verb. A flag set it can tie is a local variable defined from
  `flag.NewFlagSet` with a constant name that names a verb, used only as a
  method receiver; anything else fails, including a `FlagSet` from another
  package or a helper, one passed, returned, stored or reassigned, a
  `FlagSet` method value, `flag.CommandLine` and the `flag` package's own
  registration functions, and registration through an interface method
  with a `FlagSet` registration method's signature. Arguments parsed by
  hand, without the `flag` package (such as the global flags
  `clisetup.ParseGlobalFlags` pre-scans), are outside its reach.
  `TestMCPExposedVerbExceptions` pins `mcp.ExposedVerb`'s exception list:
  the only manifest verbs MCP does not expose are exactly `hook`, `serve`
  and `trail-emit`.
- **How it reaches `pkg/api`.** `pkg/api` keeps both implementations
  unexported (`Client.killFinished`, `Client.deleteRows`).
  `internal/adminapi` declares the hooks `KillFinished` and `Delete` as
  function variables, and `pkg/api/admin.go`'s `init` sets them.
  `internal/adminapi` imports nothing from `pkg/api`, so there is no
  cycle, and being under `internal/` it cannot be imported from another
  module. **Must use:** a new operator-only action gets a hook in
  `internal/adminapi` and a verb in its `Verbs`, never an exported
  `pkg/api` symbol, a manifest param or a CLI flag.
- **Same store, same conventions.** It parses and applies the main CLI's
  global flags (`--store-path`, `--home`, `--tmux-command`, before or after
  the verb) and opens its Client through `internal/clisetup`
  (`ParseGlobalFlags`, `GlobalFlags.Apply`, `Open`), exactly as
  `agent-director` does. It prints JSON on stdout, or one
  `{err_name, err_description}` envelope on stderr with exit 1, the names
  from `errnames.Classify` (`ErrUnknownVerb` and `ErrInvalidFlags` for its
  own dispatch errors).
- **Every help opens with the human-approval statement.** The no-verb
  run, `help`, `--help`, `-h` and each verb's `--help` / `-h` print
  `adminapi.ApprovalStatement` as their first line: "agent-director-admin
  is an operator tool. Do not run any of its commands without explicit
  approval from a human for this specific run. Agents and automated
  callers must not run it." Help and `version` open no store and load no
  config (b.8dr).
- **Install.** `install.sh` installs it from the same build as
  `agent-director`, at `~/.agent-director/admin/agent-director-admin`
  (directory 0700, binary 0755), never on PATH and never symlinked, and
  refuses (exit 3) when the two binaries' `version` stamps differ or carry
  no commit stamp (see [Install flows](#install-flows)). The release builds it for every target
  (see [Release engineering](#release-engineering)).
- **Off PATH, not locked.** Agents run as the same user as the operator,
  so an agent can find it and run it by full path, or open `state.db` with
  `sqlite3`. Keeping it off PATH and out of every agent-facing text makes
  running it a deliberate act that its first line forbids; it is not
  access control (see [Known limitations](#known-limitations)).

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

When the CLI's `spawn` verb writes hook entries into the spawned Claude
session's `--settings`, every state-tracking entry is in exec form
(SR-22.9): `{"type":"command","command":"<bin>","args":["hook"]}`, plus an
inner `timeout` on the `SessionStart` entry and the two relay entries (see
"Emitted per-hook relay timeout" below). `command` is the program itself, an absolute, symlink-resolved path
computed via `os.Executable()` followed by `filepath.EvalSymlinks()`
(`executablePath` in `internal/spawn/settings.go`), written verbatim and
never quoted, because it is a path and not shell text. `args` is
exactly `["hook"]`. The CLI never writes the bare token
`agent-director`, never writes a PATH-relative path, and never writes
`$0` / `${0}` / `$(command -v agent-director)`. The optional `help`
SessionStart entry (`inject_help_hook`) stays in shell form (`"<install
path> help"`): it writes nothing, so its parent process does not matter.

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
├── go.mod                module-boundary stub, no Go code (see below)
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

`go.mod` makes `pkg/ts-bun-client/` a separate Go module, so every `./...`
walk from the repo root (`go build`, `go test`, `go vet`, `go list`) skips this
tree. bun rewrites `dist/` and `node_modules/` here while those walks can run,
and `node_modules/` can hold stray Go packages (`flatted` ships one). Its module
path, `agent-director.invalid/ts-bun-client`, lies outside the root module's
path, so a root finder that matches the root module line
(`test/envelope-diff/harness.go`) skips it. Most `repoRoot` helpers stop at the
nearest `go.mod` and would stop here, so Go code that walks up that way must
not run with its cwd inside `pkg/ts-bun-client/`; today nothing does (the only
`go` command run there is the bun gates' `go env`, and the Go binaries bun tests
run look for no `go.mod`). Keep the stub free of Go code; it is not in the npm
package.

### Client lifecycle

A `Client` (aliased from `SubprocessClient`) owns no Go-side resources;
each verb call is a one-shot subprocess.

**Construction.** `new Client(opts)` is synchronous. All `ClientOptions` fields are optional; omitted fields fall through to the CLI's own default-resolution (the CLI is the single source of truth — the TS Client provides no fallback values, b.32k). The constructor:

1. Applies tilde expansion (TS-side, via `src/internal/tilde.ts`) to `storePath`, `home`, and `tmuxCommand` so the CLI subprocess never receives a leading `~`.
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

**Tilde expansion** of `storePath`, `home` and `tmuxCommand` is done on the
TS side, in `src/internal/tilde.ts`, before the value is forwarded to the CLI
subprocess. A bare `~` or a leading `~/` is expanded against `HOME`, or
against `os.homedir()` when `HOME` is unset or empty (b.vqj). Every other
value, relative paths included, is forwarded unchanged and the CLI resolves
it. Verb parameters such as spawn's `cwd` are not expanded here; they are
forwarded as given.

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
   (`clisetup.ParseGlobalFlags` in `internal/clisetup/globalflags.go`,
   which `run()` in `cmd/agent-director/main.go` calls) strips them prior to
   per-verb dispatch. Each is emitted only when the corresponding
   `ClientOptions` field was set by the caller (b.32k). An empty
   string counts as set and is forwarded as given (e.g.
   `--store-path ""`); the pre-scan rejects it, so every verb call
   rejects with `ErrInvalidFlags` (`<flag> requires a value`) and the
   CLI never falls back to its default (b.pu2). JSON-only
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

**Catalog source.** `pkg/api/errnames/catalog.json` is the single source of truth for every named error the Go binary can emit. It contains 42 entries at time of writing. Each entry has a `name` field (the `err_name` string) and a `package` field naming the origin Go package.

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
`spawn` with the reuse opt-in, `expire`, and the operator tool's
`delete`). Every hook transition below is one that the hook
gate applied: a hook moves a row only when it comes from the row's own
agent (see [Hooks move a row only for its own
agent](#hooks-move-a-row-only-for-its-own-agent)).

`pending` has one meaning (SRD SR-22.1): a launch (spawn, reuse or
resume) is in progress and the agent has not reported in yet (Claude
Code's SessionStart). It may be loading or waiting at a startup prompt.
A resumed row in `pending` keeps its session id and history; a caller
tells it from a fresh one by its non-empty `claude_session_id`. While a
row is `pending` it carries its launch start, `launch_started_at`.

```
pending  ──spawn() inserts the row, then launches its tmux session
  │
  ▼   SessionStart hook fires (one that arrives before the launch's
  │   identity write first waits for it, at most until the launch start
  │   plus the pending grace period or 540 s after it began waiting,
  │   whichever comes first)
waiting  ◄─── Stop; from working only, the main agent's idle-prompt Notification
  │           (soft refreshes only, no transition: SessionEnd reason=clear|compact,
  │           every other Notification)
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

pending   (a plain spawn's insert, row_version 0)
  │
  ▼   the create answered "duplicate session": the spawn's own end write
  │   (store.EndHeldLaunch, applied only at version 0 with the insert's
  │   launch start), never a hook; see "Held name" in Launch identity
ended     (records no pane, so no later hook moves it)

pending / waiting / working / ask_user / check_permission
  │
  ▼   find-missing: the row's agent process is gone, or it cannot be
  │   checked and the lookup finds no session of the row's current
  │   launch (Gone or Leftover); a pending row only once past its
  │   pending grace period
missing   (the sweep's judgement, not proof that the agent exited)

ended / missing
  │
  ▼   resume(): the move to pending (one conditional write), then the
  │   launch with --resume
pending   (keeps its session id and history)
  │
  ├──► SessionStart hook fires (the resumed agent reports in) ──► waiting
  │
  ├──► the launch fails other than by timing out: the restore, applied
  │    only at the move's row_version ──► the prior ended / missing
  │
  └──► the create times out ──► stays pending

ended / missing
  │
  ▼   spawn() with the reuse opt-in: the reset (one transaction: the
  │   archive, the reset to a new life, the permission requests
  │   deleted), then the launch under the requested name
pending   (life + 1, no session id; the new life's history starts empty)
  │
  ├──► SessionStart hook fires (the new life's agent reports in, only
  │    under the hook gate, from the launch's own pane) ──► waiting
  │
  ├──► the launch fails other than by timing out: the restore, applied
  │    only at the reset's row_version ──► the prior ended / missing,
  │    in its pre-reuse life
  │
  └──► the create times out ──► stays pending (in the new life)

any state
  │
  ▼   a hook the gate does not apply (another process's, or any hook
  │   before the row records its pane; a SessionStart only after its
  │   bounded wait for the identity write), or a subagent's or in-process
  │   teammate's SessionStart / SessionEnd (agent_id in the payload)
unchanged   (nothing written; one ad.hook.ignored)
```

### Event → state mapping (SRD §5.2)

| Event | Tool / reason carve-out | Resulting state |
| --- | --- | --- |
| `SessionStart` | — | `waiting`, whatever the prior state, in one statement (`RecordSessionStartIdentity`): also writes `claude_session_id` from `transcript_path`; `jsonl_path` only when the file exists on disk, else NULL/provisional — b.v2c; `pid` and `proc_starttime` = the hook's parent, the pane process; a differing session id archives the prior pair to `session_history`, tagged with the row's current life, in the same transaction |
| `UserPromptSubmit` | — | `working` |
| `PreToolUse` | `tool_name = AskUserQuestion` | `ask_user` |
| `PreToolUse` | any other tool | `working` |
| `PostToolUse` | — | `working` |
| `Stop` | — | `waiting` |
| `Notification` | `notification_type = idle_prompt` and no `agent_id` (the main agent idle at the prompt) | `waiting` when the row is `working` as the write lands (`ApplyHookWaitingIfWorking`); any other state: soft refresh — no state change; bumps `last_seen_at` (see "The idle-prompt Notification" below) |
| `Notification` | any other `notification_type` (`permission_prompt`, `auth_success`, `elicitation_dialog`, missing or unknown), or an idle prompt with `agent_id` | soft refresh — no state change; bumps `last_seen_at` |
| `PermissionRequest` | — | `check_permission` (relay-mode envelope is Epic 10) |
| `SessionEnd` | `reason ∈ {clear, compact}` | soft refresh — no state change; bumps `last_seen_at` |
| `SessionEnd` | any other reason | `ended` (also sets `ended_at`) |
| unknown event | — | soft refresh + info-level log entry |
| any event | the gate does not apply (the hook's parent is not the row's recorded pane process, or the row records no pane) | unchanged; one `ad.hook.ignored` (for a `pending` row's SessionStart whose parent is the pane process's child, also one `ad.hook.launcher_detected`; see "Launcher warning" in [Hooks move a row only for its own agent](#hooks-move-a-row-only-for-its-own-agent)) |
| `SessionStart`, `SessionEnd` | the payload carries a non-empty `agent_id` (a subagent or in-process teammate), any reason or source | unchanged, decided before the gate; one `ad.hook.ignored` `subagent_event` (none when no row has the id) |

Every ordinary (non-SessionStart) hook that applies to a row with no
recorded `claude_session_id` also records the payload's session id and
transcript path (presence rule) in the same write; a row that already
records one keeps it. An ordinary hook whose payload carries `agent_id`
records neither.

**The idle-prompt Notification (b.svb).** `Stop` owns the end-of-turn move
to `waiting`, so a hook that fires after `Stop` with no `Stop` after it
leaves the row `working` while the agent sits idle. Claude Code's
end-of-turn background forks (the prompt-suggestion fork, for one) run the
session's `PreToolUse` hooks from the agent's own process, so they pass the
gate; the fork's tool call never runs, so no `PostToolUse` follows, and the
payload does not tell a fork from the main agent. Claude Code's idle-prompt
`Notification` (`notification_type` `idle_prompt`, sent only while its main
agent is idle at the prompt, once `messageIdleNotifThresholdMs`, 60 s by
default, has passed since the turn ended) is the evidence that the main
agent's turn is over. It is not evidence that background subagents have
finished (see the residual windows below):

- **Classification.** `ClassifyEvent` (`internal/hook/classify.go`) sets
  `ClassifyResult.WaitingIfWorking` for a Notification whose
  `notification_type` is `idle_prompt` (`NotificationTypeIdlePrompt`) and
  whose payload carries no `agent_id`. `SoftRefresh` stays true alongside
  it, so a writer that does not act on `WaitingIfWorking` makes the soft
  refresh. Every other Notification, and an idle prompt with `agent_id`, is
  the plain soft refresh.
- **The write.** `hook.Handle` sends it to `store.ApplyHookWaitingIfWorking`
  (`applyOrdinaryHook`, `internal/hook/handler.go`; a store without that
  method, such as a test double, gets the ordinary soft-refresh write). The
  write is one `UPDATE` whose `WHERE` carries `state = 'working'` with the
  gate (`hookGateSQL`), so no read decides it. When it applies it writes
  what a non-terminal `ApplyHookTransitionResult` transition writes (state
  `waiting`; `last_seen_at` bumped; `ended_at`, `launch_started_at` and the
  liveness markers NULLed; the session record; the pane start time when
  NULL; `row_version` + 1) and emits `ad.spawn.state_transition` from
  `working` to `waiting` with `soft_refresh` false and
  `triggering_event_name` `Notification`; `ad.hook.fired` carries
  `upsert_outcome` `updated`.
- **Not working.** When that statement matches no row (the row is not
  `working`, the gate does not hold, or no row has the id), the write is
  `ApplyHookTransitionResult`'s soft refresh, which tells those cases apart
  and reports exactly as for any soft-refresh hook (a hook the gate does
  not apply writes its one `ad.hook.ignored`). A row that becomes `working`
  between the two statements
  gets the soft refresh: the Notification is then ordered before the hook
  that moved the row.
- The `working`-transition hold for open permission requests does not
  apply (it holds transitions to `working` only), and the relay is not
  involved.

Residual windows: the row reads `working` until the Notification arrives,
about 60 s after the turn ended. A stray hook whose write lands after the
Notification's leaves the row `working` until the agent's next hook. And the
Notification can race a new turn: if its write lands after the new turn's
first hook moved the row to `working`, the row reads `waiting` until the
turn's next hook. On Claude Code 2.1.280 through 2.1.287 the Notification
can also fire while a background subagent is still running (fixed in
2.1.288, anthropics/claude-code#93672): if that subagent's hook had moved
the row to `working`, the row reads `waiting`, its tool possibly still
running, until the subagent's next tool hook (`PreToolUse`, `PostToolUse`
or `PermissionRequest`). `CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false` in the
agent's environment turns off the prompt-suggestion fork at its source.

`missing` is set by `find-missing`'s mark, and written back by a failed
resume's or reuse's restore when the row was `missing` before the move or
reset. It is the
sweep's judgement on the evidence available to it, not proof that the
agent has exited; neither `ended` nor `missing` means the agent is dead
or its row is safe to delete (see [`find-missing`](#find-missing)).

`pending` is set only by the three writes that begin a launch (SR-22.1):
a spawn's insert (`InsertPending`), reuse's reset (`ResetForReuse`, see
[Reuse of a finished id](#reuse-of-a-finished-id)) and `resume`'s move
(`MoveToPending`, see [Resume](#verb-pkgapiresumego)). It is ended by
every write that sets another state: the agent's report-in (the first
SessionStart hook, to `waiting`), another of the agent's hooks' state
transitions (`ApplyHookTransition`), `find-missing`'s mark (to
`missing`), a plain spawn's end write after "duplicate session"
(`EndHeldLaunch`, to `ended`), a failed resume's restore
(`RestoreAfterFailedResume`) and a failed reuse's restore
(`RestoreAfterFailedReuse`), each restore to the prior `ended` or
`missing`. Each of these clears `launch_started_at` in the same
statement. A timed-out
create writes nothing and leaves the row `pending`, because the session
may exist; so does a restore that does not apply or fails.

State-tracking hook writes are fail-open: any internal failure logs and
exits 0 (SRD §3.2). A missed UPSERT never blocks Claude. The one
deliberate delay is SessionStart's bounded wait for its launch's identity
write: it can hold the hook, and so the agent's first response, until the
launch start plus the pending grace period or 540 s after the wait began,
whichever comes first; the hook still exits 0 with empty stdout.

### Hooks move a row only for its own agent

(SRD SR-22.9, SR-14 `ad.hook.ignored`, Appendix F.4; decision-0929c.)
The row's agent is the pane agent-director created: the `pane_pid` from
the create reply, with that pid's start time, which the identity write
records as `pane_pid` and `pane_starttime` (see [Launch
identity](#launch-identity)). With Claude Code the pane process is the
Claude process itself. Only hooks whose parent is that process move the
row.

**Exec form and `getppid()`.** Every state-tracking hook is registered in
exec form (`{"type":"command","command":"<bin>","args":["hook"]}`,
`internal/spawn/settings.go`), so Claude Code starts `agent-director
hook` directly, with no `sh` between them. The hook's parent
(`getppid()`) is then the Claude process that fired it. A shell-form
entry would put an `sh` there (dash does not exec its last command), and
the parent would be that shell.

A Claude Code that does not run exec-form hooks ignores `args` and runs
`command`, the bare binary, through `/bin/sh`, so each of its hooks runs
`agent-director` with no verb and the payload on stdin; no hook applies.
The no-verb run detects this (`noVerbHookIgnored`,
`cmd/agent-director/noverb.go`): when stdin is not a terminal it reads at
most `hook.MaxPayloadBytes` (1 MiB), bounded by `noVerbStdinDeadline`
(1 s, a timer around a goroutine read, since an inherited blocking stdin
has no read deadline), and passes the bytes to `hook.HandleNoExecForm`
(`internal/hook/noexec.go`). Input counts as a hook payload only when it
is a JSON object whose `hook_event_name` is a non-empty string
(`noExecFormPayload`; the `event_name` alias that `PeekEventName`
accepts does not count). Then the run prints nothing, exits 0, runs no
hook logic and writes one `ad.hook.ignored` `no_exec_form`
(`emitNoExecForm`), with no store, no config load, no `ad.hook.fired`
and no logger, so nothing reaches stderr. Anything else (a terminal, a
read error, empty input, input over 1 MiB, input still open at the
deadline, or input that is not a hook payload) prints help as before.
The run happens after the global flags and `--home` are applied, so the
record lands under that home. `help`, `--help` and `version` never read
stdin. The installed persistent hooks and the per-Spawn
`inject_help_hook` entry are shell form with an explicit `help` verb, so
this check never applies to them.

**Subagents and in-process teammates.** Claude Code runs every subagent,
and an in-process agent-team teammate's turns, inside the agent's own
process, so their hooks pass the gate; the payload marks them with a
non-empty `agent_id` (`ClassifyResult.AgentID`, `internal/hook/classify.go`).
`agent_type` alone (a session started with `--agent`) does not mark one
and is not read.

- **SessionStart and SessionEnd** with `agent_id`
  (`ClassifyResult.SubagentLifecycle`) are decided from the payload in
  `hook.Handle`, before the gate and before any write: nothing is written
  to the row, stdout stays empty, `ad.hook.fired` carries
  `upsert_outcome` `no_change`, and `emitIgnored` writes one
  `ad.hook.ignored` with reason `subagent_event`
  (`store.HookReasonSubagentEvent`). `emitIgnored`'s one row read carries
  the no-row rule (`ignoredHook.silentWithoutRow`): when no row has the
  id, nothing is written, as for the other hook-path reasons. So a
  subagent can neither replace the row's session id (which `resume` uses)
  nor end a live row.
- **Every other hook** with `agent_id` goes through the gate as any hook
  does and applies its state transition, but the handler passes an empty
  session id (in the `store.HookGate`) and an empty transcript path, so
  it records neither. A relayed PermissionRequest from a subagent uses the
  row's relay. An idle-prompt Notification with `agent_id` is a soft
  refresh, never the return to `waiting` (see "The idle-prompt
  Notification" in [Event → state mapping](#event--state-mapping-srd-52)).

**Capture.** `hook.Handle` captures the parent once, at entry, before any
store call (`captureParent`, `internal/hook/gate.go`): the pid from
`HandleConfig.ParentPID` (`os.Getppid` in `cmd/agent-director`) and that
pid's start time from `HandleConfig.ParentProc.StartTime` (the per-OS
`probe.ProcChecker`). An unreadable start time is `""`. The pair, with
the event name and the payload's session id, is the hook's
`store.HookGate`, and every store write for the hook carries it.

**The condition, in each statement.** Every hook write puts the gate in
its own `WHERE` (`hookGateSQL`, `internal/store/hook_gate.go`):

```sql
pane_pid = ? AND ? <> '' AND (pane_starttime = ? OR pane_starttime IS NULL)
```

So no write lands between a check and the write, and a hook racing a
launch write never applies to the new launch. The writes are:
`ApplyHookTransition` / `ApplyHookTransitionResult` (every event but
SessionStart), `ApplyHookWaitingIfWorking` (the main agent's idle-prompt
Notification, whose write is `ApplyHookTransitionResult`'s soft refresh
when the row is not `working`), `RecordSessionStartIdentity` (SessionStart, which also
carries the snapshot condition on the row it examined, SR-5.3), and the
relay's `UpsertOpenPermissionRequest` / `UpsertOpenPermissionRequestResult`
(`INSERT … SELECT … WHERE EXISTS (… AND <gate>)`). The rules:

- The parent pid and start time must equal the row's `pane_pid` and
  `pane_starttime`.
- **NULL start time.** When the row's `pane_starttime` is NULL
  (agent-director could not read it at the create, for example from
  another pid namespace), the pid alone decides, and the applied write
  records the parent's start time in the same statement
  (`hookPaneStartSet`: `pane_starttime = COALESCE(pane_starttime, ?)`).
- An unreadable parent start time never matches, whatever the row holds.
  A non-positive parent pid is bound as NULL and matches nothing.
- **No pane recorded.** A row with no `pane_pid` matches no hook: a
  `pending` row before its identity write, a row whose create reply was
  lost (until its pane is adopted), and a finished row that `resume`
  has moved to `pending` (the move clears the identity) until the new
  launch's identity write. A SessionStart that meets such a `pending`
  row inside its pending grace period does not give up at once: it
  waits for the identity write (see "SessionStart waits for its launch's
  identity write" below).
- **The session id is recorded, not gated.** SessionStart records the
  payload's session id; an applied ordinary hook records it only when the
  row has none. A payload's session id never decides whether a hook
  applies.

**Not applied.** When the statement matches no row, one read after it
(`readHookGateRow`) decides the result (`store.HookApplied`):

| Row | Result |
|---|---|
| no row with the id | `HookApplied{}`, no reason: today's silent no-op, no `ad.hook.ignored` |
| a subagent's SessionStart / SessionEnd (never reaches the store write) | reason `subagent_event` (`store.HookReasonSubagentEvent`), set by the handler from the payload before any write; none when no row has the id |
| `pane_pid` NULL | reason `no_pane_recorded` (`store.HookReasonNoPaneRecorded`), even with an unreadable parent start time; for SessionStart the handler first runs its bounded wait when the row qualifies, and only the final gated write's reason is logged |
| SessionStart, gate holds (only the snapshot changed) | `snapshotChanged`: the handler re-reads and retries once; a second change logs one line and writes no `ad.hook.ignored` |
| anything else | reason `pid_mismatch` (`store.HookReasonPIDMismatch`) |

A hook that did not apply changes nothing: no state, no `last_seen_at`,
no liveness-note clear, no permission request. It exits 0 with empty
stdout (a relayed PermissionRequest returns no decision, so the process's
own Claude Code asks as it would with no relay), and, when there is a
reason, writes exactly one `ad.hook.ignored` (`emitIgnored`,
`internal/hook/gate.go`; see [`ad.*` event
namespace](#ad-event-namespace)). A store write reports only
`pid_mismatch` or `no_pane_recorded`; the other two of SR-14's four
reasons are decided before any store write: `subagent_event` by the
handler, `no_exec_form` by the no-verb run (with no store at all). Its `ad.hook.fired` carries
`upsert_outcome` `no_change`.

**SessionStart waits for its launch's identity write.** (SR-22.9 "SessionStart
before the identity write", SR-13.4; AC-HOOK-05.) The agent starts inside the
create call, and the identity write lands only after the reply is parsed, the
labels are placed and one store write is made, so a quick agent's SessionStart
can reach a row that records no pane yet; an idle agent fires no further hook.
So `recordSessionStart` (`internal/hook/handler.go`) runs in three steps:

1. `writeSessionStart`: the ordinary gated write (one `GetSpawn` for the
   snapshot, one `RecordSessionStartIdentity`, one retry on a snapshot
   change). It also returns the row it examined.
2. Only when that write reported `no_pane_recorded`,
   `waitForLaunchIdentity`. It waits only when the examined row qualifies
   (`waitingForIdentity`): `pending`, `pane_pid` not recorded, a launch start
   set, and `HandleConfig.Now` before the launch start plus
   `HandleConfig.PendingGrace` (`store.InsidePendingGrace`). A nil `Now` or a
   grace of zero or less never waits. While it waits it sleeps on
   `HandleConfig.Clock` (`DefaultPollClock` when nil) for at most
   `sessionStartWaitInterval` (250 ms), never past either bound, and
   re-reads the row with `GetSpawn` only, a read that never blocks the
   identity write. It ends when a pane is recorded, the state or
   `row_version` changes, the row is no longer inside its grace period (the
   grace bound), `sessionStartWaitCap` (540 s) has passed since the wait
   began (the cap; WD 2026-09-30c), the row is gone, `ctx` is cancelled, or
   a read fails (logged; fail-open). The wait thus ends at the earlier of
   the grace bound and the cap, whatever the configured grace. It writes
   nothing.
3. After a wait, `writeSessionStart` once more with the snapshot it reads
   then. That write's result is the hook's: applied when the pane now
   recorded is the hook's parent (`waiting`, `ad.hook.fired`
   `upsert_outcome` `updated`); `pid_mismatch` for a leftover or stray that
   waited; `no_pane_recorded` when the row still records no pane at either
   bound; no record when the row is gone. Because this last gated write, not
   the wait, decides, an identity written between the last re-read and the
   bound (grace bound or cap) still applies.

So SessionStart writes `no_pane_recorded` only after its bounded wait, and
nothing is logged while it waits. Only SessionStart waits: Claude Code sends
no prompt until its SessionStart hooks finish, so every later hook of the
agent fires after the wait, and no other event needs one. A subagent's
SessionStart (`subagent_event`) returns before this path and never waits. The
wait makes no tmux call and adds no column. The grace period is the one
`find-missing` uses, from `config.Tmux.EffectivePendingGrace` through
`runHook`; `internal/hook` never reads `[tmux]` itself.

**The cap and the hook timeout (WD 2026-09-30c).** `sessionStartWaitCap`
(540 s, `internal/hook/handler.go`) always stays below the `"timeout": 600`
that the synthesised settings state on the SessionStart `agent-director
hook` entry (`sessionStartHookTimeoutSeconds`,
`internal/spawn/settings.go`; see "Emitted per-hook relay timeout" in the
spawn pipeline section). So the hook always ends its
own wait, and writes its `no_pane_recorded`, before Claude Code could
kill it, whatever the configured grace and whatever a clock step or a
launch start in the future does. Cap < timeout is stated in the two
constants' comments and here only: the packages do not import each
other, and each value is pinned once in its own package's tests
(`hook.SessionStartWaitCap` through `internal/hook/export_test.go`).
Changing either value means checking the other.

**Must use:** SessionStart's wait for its launch's identity write, and any
new hook wait bounded by the pending grace period, sleeps on
`HandleConfig.Clock` and reads time only through `HandleConfig.Now`, never
`time.Sleep` or `time.Now`, and decides "inside the grace period" only
through `store.InsidePendingGrace`. The relay poll (`Poll` in
`internal/hook/polling.go`) also waits on the store but keeps its own seams:
it sleeps on its `PollClock` and reads its deadline through `nowFunc`
(`time.Now`). Tests drive the SessionStart wait with a virtual clock whose
`Sleep` advances `Now` (`hookConfig` in
`internal/hook/hook_parent_fakes_test.go` wires one, with the default grace,
by default). The wait has one budget, fixed when it begins:
min(grace bound − `Now` at the start, `sessionStartWaitCap`).

- **Elapsed time comes from `HandleConfig.Now`.** The cap is measured as
  `Now` at each turn minus `Now` at the start (`capEnd` is the start
  reading plus the cap). `runHook` wires `time.Now` as is, whose readings
  carry Go's monotonic clock reading, so that difference is monotonic and
  no wall-clock step moves it; production wiring must keep that reading
  and never pass `Now`'s value through `.UTC`, `.Local`, `.Round(0)`, `.Truncate`
  or `.In`, which strip it. In tests the difference of two virtual-clock
  readings is the virtual elapsed time, whether the clock was seeded with
  `time.Now()` (`hookConfig`) or with `time.UnixMilli` (the wait tests'
  `runSessionStartWait`), since `Sleep` advances both readings alike.
- **The grace bound is wall clock.** It comes from `time.UnixMilli` (the
  launch start plus the grace), which carries no monotonic reading, so
  `bound.Sub(Now())` and `store.InsidePendingGrace` compare wall-clock
  times.
- **The re-read cap** is ⌊budget / 250 ms⌋ + 1 re-reads. With a clock that
  advances steadily it never binds; it is the guard against a `Now` that
  never advances (a frozen test clock), which would otherwise never reach
  either bound.
- **Clock steps.** A launch start in the future, or a wall-clock step back
  before the wait begins, moves the grace bound later but not the cap, so
  the wait ends at the cap at the latest (a launch start 10 s in the future
  with a 60 s grace ends the wait at the grace bound, after 70 s). A step back during the wait makes the grace bound
  report more time left; the wait then ends at whichever comes first, the
  re-read cap (about the time left at its start) or the cap.

**SessionStart is one statement.** An applied SessionStart sets
`waiting` whatever the prior state and, in the same UPDATE, records the
payload's session id and transcript path (presence rule), `pid` and
`proc_starttime` = the hook's parent (the pane process), `pane_starttime`
when NULL, bumps `last_seen_at`, clears `ended_at`, the liveness notes
and `launch_started_at`, and advances `row_version` once. The rotation
archive runs inside the same transaction, only when the write applied
(see [SessionStart hook side of the
contract](#sessionstart-hook-side-of-the-contract)).

**Must use:** every store write a hook makes takes the hook's
`store.HookGate`, puts `hookGateSQL` (with `hookGateArgs`) in its own
statement (an UPDATE also carries `hookPaneStartSet` in its SET), and
reports
`store.HookApplied`, with the not-applied reason from `notAppliedReason`
(`internal/store/hook_gate.go`). No ungated hook write exists in
production code; never add one, and never check the gate in a separate
read before the write. An ordinary hook records the session id only
through `hookSessionRecordSet`; a hook whose payload carries `agent_id`
passes an empty session id in its `store.HookGate` (and an empty
transcript path), so that fragment records nothing. The `ad.hook.ignored`
reason constants (`store.HookReasonPIDMismatch`,
`store.HookReasonNoPaneRecorded`, `store.HookReasonSubagentEvent`,
`store.HookReasonNoExecForm`) live together in
`internal/store/hook_gate.go`; a new reason goes beside them. Every
`ad.hook.ignored` record's SR-14 fields come from the one builder
`ignoredFields` (`internal/hook/gate.go`), which `emitIgnored` (hook
path, then filling `row_session_id` / `row_pane_pid` / `launcher_pid`
from its row read) and `emitNoExecForm` (no-verb path, row fields and
`launcher_pid` null) both use; never build those fields elsewhere.

**Launcher warning (b.9n6).** When the `claude` on PATH is a launcher or
shim that runs Claude Code as a child instead of exec-ing it, the pane
process is the launcher and every hook's parent is its child, so SR-22.9
refuses every hook of the agent with `pid_mismatch` and the row stays
`pending`. A hook run through a shell instead of in exec form has the
same structure (the pane process is Claude Code, the parent that shell);
the two are not told apart. The gate's decision does not change; only
the trail says more. For a hook refused with `pid_mismatch`, and only
then, `emitIgnored` reads the parent's own parent pid once, after the
decision, through `HandleConfig.ParentProc.PPID` (the per-OS
[parent-pid reader](#parent-pid-reader-parentpidreader-ppidgo), wired by
`hookParentProc`), and compares it with the `pane_pid` of its one row
read (`launcherPID`, `internal/hook/gate.go`). When they are equal:

- `ad.hook.ignored` carries `launcher_pid` = that pid, for any event and
  any row state. Otherwise `launcher_pid` is null; it is always null for
  `no_pane_recorded`, `subagent_event` and `no_exec_form`.
- When the hook is also a SessionStart and that row read found the row
  `pending`, `emitLauncherDetected` then reads the launcher's command
  name once (`ParentProc.CommandName`, for `launcher_command`) and writes
  one `ad.hook.launcher_detected` after the `ad.hook.ignored` record (see
  [`ad.*` event namespace](#ad-event-namespace)). It keeps no record of
  having warned: each refused SessionStart (startup, `/clear`,
  compaction, resume) writes one. Callers branch on the event name;
  its `advice` (`launcherAdvice`) names both causes, asserts neither,
  and never advises ending a session by hand.

Both are fail-open: a nil reader, an unreadable parent pid or a
trail-write failure leaves `launcher_pid` null or writes nothing, an
unreadable launcher command name leaves `launcher_command` null, and the
row, stdout and exit stay those of any ignored hook. Neither read is
evidence: no gate, ownership, liveness or launch decision reads them.

**No resolver, no tmux.** The hook path makes no tmux call and walks no
process ancestry: `internal/hook` imports neither `internal/tmux` nor
`internal/probe`. Its reads past the parent are the launcher warning's
parent-pid read above, plus the launcher's command-name read only when
`ad.hook.launcher_detected` is written; both report and never decide. A slow or
wedged tmux never delays a hook. Adopting the
pane of a row whose create reply was lost is never a hook's job (SR-3.6
gives it to `kill`, `send-keys`, `pause` and `find-missing`).

**What it rejects and keeps.** A nested `claude` started in the agent's
shell (its hooks' parent is the nested process), a teammate pane split in
the agent's session (its own main process), a leftover of an earlier
life and any stray process that inherited `AGENT_DIRECTOR_INSTANCE_ID`
are all ignored, for every event, SessionStart included. `/clear`,
`/resume` and compaction inside the agent's own process are applied,
because the process is the same, and SessionStart records the new session
id. An `ended` row stays `ended` against every other process's hooks:
only the row's own agent, or a verb such as `resume`, moves it again. The
ended transition therefore comes only from the row's own agent (or from
agent-director's own writes). A subagent's or in-process teammate's
SessionStart and SessionEnd (`agent_id`) are rejected too, even from the
pane process; its other hooks are kept but record no session id.

**Residuals (SR-18.12).** A nested `claude`'s and a teammate pane's hooks
are ignored, so their work is not reflected in the row. A row whose
create reply was lost takes no hook, and stays `pending`, until a verb
adopts its pane by `@ad_pane` (SR-3.6: `kill`, and `send-keys` with
`allow_pending`, through `adoptIdentity`, and `find-missing`, once the row
is past its pending grace period, through `adoptInSweep`; `read-pane` uses
the pane for its call only and writes nothing). Its SessionStart waits to the bound and, unless the
pane is adopted meanwhile, is ignored as `no_pane_recorded`, so after a
later adoption an idle agent's row stays `pending` until the agent's
next hook (the lost-reply residual). The SessionStart wait bounds the
race with the identity write by the pending grace period and by the
540 s cap, so an identity write slower than that period, or than 540 s,
leaves the row `pending` the same way (residual (ii); TLA+
`ci_H_Lrep_slow`). The wait always ends itself, at the earlier of its
grace bound and 540 s after it began, before the `"timeout": 600` its
hook entry states, so it is never killed silently: it writes its
`no_pane_recorded` whatever the grace or a clock step does (WD
2026-09-30c). With a grace above 540 s, SessionStart stops waiting at
540 s while `find-missing` still counts the row as inside its grace
period; the row stays `pending` until adoption or the agent's next hook,
as in residual (ii), now with its `no_pane_recorded` record. A launch
start later than the hook's clock (a wall-clock step back, or clocks
that disagree) likewise keeps `find-missing` counting the row as inside
the grace period; the hook's wait still ends by the cap at the latest. While a SessionStart waits, the agent's first response
waits too, since Claude Code waits on SessionStart hooks. A Claude Code
version that does not run exec-form hooks (Claude Code 2.1.120, the
Docker pin, is one) never applies a hook, so its rows stay `pending`
until `kill` or `find-missing`; each of its hooks writes
`ad.hook.ignored` `no_exec_form`, so the trail says why (RN-9; the
README states the minimum version). A `claude` on PATH that runs Claude
Code as a child instead of exec-ing it likewise never applies a hook, and
its rows stay `pending`; its hooks' `ad.hook.ignored` records carry
`launcher_pid`, and each refused SessionStart of a `pending` row writes
`ad.hook.launcher_detected` (see "Launcher warning" above; the README
states the prerequisite). Subagents' and in-process teammates'
tool and permission events move the row's state and use its relay, so a
row can read `working` while only they work: the row reflects the
process. Other agent
CLIs are unsupported in this release: agent-director launches only
Claude Code. `respawn-pane` of the agent's pane changes the pane pid, so
the agent's hooks are then ignored, visibly.

## Spawn Parameter Resolution

`spawn` is implemented as a four-stage pipeline, preceded by one id
check in the shared verb layer and, for a caller-supplied id with no
row, followed by the label scan before the launch. The boundaries exist
so each stage can be tested in isolation against synthesized input.

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
   └────┬───────┘   relay_mode from config. Explicit id: one collision
        │           pre-check read (SpawnState; live row →
        │           ErrInstanceIdCollision; read failure → ErrInternal).
        │           Without the reuse opt-in any existing row collides
        │           here, live or finished (the insert's PRIMARY KEY
        │           catches only a row inserted after this read).
        │           Nothing created on error. Returns an IDCheck.
        │           With the opt-in, a finished row leaves the pipeline
        │           here for the reuse path (see "Reuse of a finished id").
        ▼
   ┌────────────┐   pkg/api scanForLeftover, IDNoRow only (explicit id,
   │ Label scan │   no row of any state): one tmux lookup on the socket
   └────┬───────┘   the caller's environment resolves, creating nothing.
        │           Leftover of this store → ErrTmuxSessionConflict;
        │           Can't tell → its error. A refusal writes no row and
        │           no trust entry.
        ▼
   ┌────────┐   resolve + create the socket's per-user dir (refusal →
   │ Launch │   ErrTmuxNotAvailable, nothing written); mint launch token;
   └────┬───┘   env compose; --settings synthesis; pre-trust; pending
        │       insert with launch start, token and socket; one bounded
        │       create that labels the session (@ad_owner, @ad_pane);
        │       one conditional identity write. "duplicate session" →
        │       the held-name path instead: the end write (row → ended),
        │       one re-lookup of the name, the classified error, one
        │       ad.launch.name_held. See "Launch identity".
        ▼
   claude_instance_id, pre_trust (state stays `pending` until SessionStart fires)
```

The verb returns once the create answers; it never waits for the agent
to report in. The create itself is bounded by the create timeout (see
[Launch identity](#launch-identity)).

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
`validateExplicitInstanceID` itself. The byte test is `hasControlChar`
(`pkg/api/spawn.go`), the one predicate behind this check and `resume`'s
refusal of a row whose id has a control character (SR-3.13, see
[Resume](#resume)). **Must use:** any code in `pkg/api` that tests an
instance id for control characters calls `hasControlChar`. Do not write a
second byte loop.

### Collision pre-check

For an explicit `claude_instance_id`, `spawn.ApplyDefaults`
(`internal/spawn/defaults.go`) makes one read through its
`CollisionChecker`: `SpawnState(id)` returns the row's state, or
`exists` false when there is no row. The one read tells no row, a live
row and a finished row apart. `ApplyDefaults` returns an `IDCheck`:
`IDMinted` (no id supplied, nothing read; `runSpawn` derives `Launch`'s
`minted` from it), `IDNoRow` (the label scan runs; for both, see
[Launch identity](#launch-identity)), `IDFinishedRow` (a finished
row, returned only with the reuse opt-in: not scanned, the
[reuse path](#reuse-of-a-finished-id) runs) or `IDNotChecked` (no
checker given). A read error is never "no row", whatever it wraps.
Without the reuse opt-in, any existing row with the explicit id
collides here, finished or live. There are three error outcomes here:

- A live row, `pending` included, returns `ErrInstanceIdCollision`
  ("<id> already live"), with or without the opt-in.
- A finished row (`ended` or `missing`) without the opt-in returns
  `ErrInstanceIdCollision` ("<id>"; b.hjs).
- A failed store read returns `ErrInternal`, with the description "the
  collision pre-check could not read the store: <store error>". A store
  fault says nothing about whether the id is in use, so it must never
  reach a caller as a collision, which callers read as "resume instead".

In every case nothing is created. The pre-check runs before `Launch`, so
no socket directory, launch token, `--settings` synthesis, pre-trust
write, row or tmux session follows. An empty id is never checked;
`ApplyDefaults` mints a fresh UUID4 for it. SQLite's PRIMARY KEY at the
insert (`store.ErrPrimaryKeyCollision`, which `Launch` reports as
`ErrInstanceIdCollision`) is only the backstop for a row inserted by a
race after the pre-check's read. That race is the one refusal of an
existing row that comes after pre-trust, so only it can leave a refused
plain spawn's trust entry written (see
[Workspace-trust pre-write](#workspace-trust-pre-write)).

**Must use:** plain spawn's `ErrInstanceIdCollision` for an existing row
with no "already live" finding (the pre-check's finished row without the
opt-in, and the insert's race collision) is built by
`rowExistsCollision(id)` (`internal/spawn/defaults.go`), so both give
the same text. Do not format that error inline.

The read-failure mapping lives in one place:
`spawn.PreCheckReadError(err)`. It formats the store error with `%v`,
not `%w`, so the result wraps no sentinel and `errnames.Classify`
returns `ErrInternal` on every surface. Every collision pre-check read
must map its failure through `spawn.PreCheckReadError`. The reuse path
does: its one read, `ReadForReuse`, answers `ApplyDefaults` through the
`reusePreCheck` adapter (`pkg/api/spawn_reuse.go`), so its read failure
and its live-row collision go through the same mappings. Do not build a
second pre-check error, and do not wrap the store error with `%w`.

`runSpawn` in `pkg/api/spawn.go` takes the pre-check reader
(`spawn.CollisionChecker`) separately from the insert store, and
`runSpawnWithReuseStore` also takes the reuse store. `Client.Spawn`
passes the same `*store.Store` for all of them. Tests inject a failing
reader through `api.SpawnWithCollisionReader`, and a wrapped reuse store
through `api.SpawnWithReuseStore` (with the alias `api.ReuseStore`),
both in `pkg/api/export_test.go`.

### Explicit session-name validation

When the caller supplies `tmux_session_name`, even empty
(`SpawnParams.TmuxSessionNameSupplied`: the CLI sets it when
`--tmux-session-name` is given, MCP when the key is present and not
`null`), `spawn.Validate` runs
`validateTmuxSessionName` (`internal/spawn/validate.go`). It checks in
this order:

- An empty value returns `ErrTmuxSessionNameEmpty`, whose description
  names the param through `manifest.TmuxSessionNameSpelling`
  ("tmux_session_name (--tmux-session-name on the CLI) was supplied with
  an empty value").
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

### Launch identity

A plain spawn labels its tmux session when it creates it, records which
launch, socket, server and pane the row belongs to, and scans for a
leftover first when the caller chose the id (SRD SR-3.3, SR-3.5, SR-3.6,
SR-9.3, SR-9.4, SR-22.2). Every spawned session can then be proven to be
its row's current launch, and its store's, by its label alone. The lookup
that reads the labels is the [shared tmux lookup](#shared-tmux-lookup);
the tmux calls are the `internal/tmux` call set (package inventory).

**The session label.** `@ad_owner` = `ad1 <token> <$N> <instance id>
<store id>`, five fields:

- `<token>` is the launch token, 16 lowercase hex characters (64 bits
  from `crypto/rand`, `spawn.NewLaunchToken`; a read failure is an
  uncatalogued error before any write, never a weaker source).
- `<$N>` is the new session's own tmux id, expanded by tmux at creation.
- `<store id>` is this store's `store_meta.store_id`, read once when the
  store opens and returned by `(*store.Store).StoreID()`. `Launch` and
  `runSpawn` read it from the store; it is not a parameter. It keeps the
  agents of several agent-director stores on one tmux server apart.

The create also sets the per-pane `@ad_pane` = `<token> <pane id>` on the
session's one pane. `@ad_owner` is the session's provenance label, set by
agent-director. It is unrelated to the caller's key=value spawn labels of
the [Label model](#label-model), which are stored in `spawns.labels` and
emitted as `AGENT_DIRECTOR_LABEL_*` variables.

**The label scan** (`scanForLeftover`, `pkg/api/spawn_scan.go`). It runs
only when `ApplyDefaults` returns `IDNoRow`: a caller-supplied id with no
row of any state. It runs after the collision pre-check and before
`Launch`, so before socket creation, the token, pre-trust and the insert.
It resolves the socket from the caller's environment creating nothing
(`spawn.ResolveScanSocket`) and makes one `tmux.Lookup` for a token-less
`Launch` of the id with this store's id and no recorded server. Outcomes:

| Lookup verdict | Result |
|---|---|
| Leftover: one or more sessions carry a valid label of this store naming the id, whatever the token or name | `ErrTmuxSessionConflict` ("left over from an earlier life"), naming up to three sessions by name and `$N`, then a count; one `ad.launch.name_held` record |
| Can't tell, unreadable | `ErrTmuxUnresponsive` |
| Can't tell, `provenance_conflict` (only a scope value can cause it here) | `ErrTmuxSessionConflict` ("conflicting labels") |
| Can't tell, tmux unavailable | `ErrTmuxNotAvailable` (`spawn.TmuxUnavailableError`) |
| Gone: no such label, only other stores' labels, no server, no socket | the spawn proceeds |

Every refusal writes no row and no trust entry. The scan writes no
`ad.provenance.disagree`: a leftover is an expected condition. The
`ad.launch.name_held` record (source `ad_spawn`, `row_result`
`not_inserted`, `leftover_count`) takes its session fields from the
leftover with the lowest numeric `$N`, and carries `tmux_socket`,
`store_id`, and the by-hand `attach_command` and `end_command` for that
session. Humans read those commands; no error description carries them.
A minted id is never scanned, because a fresh random id cannot have a
leftover. A finished row is not scanned either. The scan does not see a
leftover on another server or socket, an unlabelled session, or a process
that carries the id but has no labelled session. It is one lookup, so a
caller-supplied id's spawn can cost a query timeout more than a minted
one's (SR-13.2).

**The insert.** `Launch` first resolves the launch socket as tmux would
(`spawn.ResolveLaunchSocket`: `TMUX`, then `TMUX_TMPDIR`, then `/tmp`),
creating a missing per-user directory with mode 0700 and checking it as
tmux does. A directory that cannot be created or fails the check
(symlink, not a directory, wrong owner, open to others), or a
`TMUX_TMPDIR` naming a regular file, returns `ErrTmuxNotAvailable`. The
description names the socket, the directory and the reason, and says
nothing was launched. That happens before pre-trust and the insert. The
pending insert (`InsertPending`) then writes, in the INSERT itself:

- `launch_started_at`: the Client's clock (`now`), read once, in
  milliseconds;
- `launch_token`: the token;
- `tmux_socket`: the resolved socket;
- `no_pre_trust`: the caller's pre-trust choice (1 when it passed
  `no_pre_trust`, `--no-pre-trust` on the CLI, else 0).

The six server and pane identity columns stay NULL and `row_version` is
0. A plain spawn's recorded socket therefore comes from the caller's tmux
environment; later calls for the row use the recorded socket.

**The create** (`spawn.CreateAndLabel`, `internal/spawn/createlabel.go`,
shared with later launch kinds). It makes one `NewSession` invocation on
`-S <socket>` that creates the session and, chained in the same
invocation, sets `@ad_owner` and `@ad_pane`. After that:

- A name containing `$` or `\` (`tmux.NeedsLabelByID`; only rows from
  before these names were rejected) gets no chain. It is labelled in a
  second call, `SetLabel`, by the session and pane ids of the create
  reply.
- A chained label step that fails (`FailLabel`) is relabelled once by id
  with `SetLabel`.
- If that label by id fails, the session is killed by its id
  (`KillSessionID`). The spawn returns `ErrTmuxSessionCreate`, saying that
  the session was created but could not be labelled, and whether it was
  ended.

So a create makes at most one create, one label by id and one kill. A
create that timed out or whose reply did not parse is never relabelled,
because its session id is unknown.

**The identity write.** Only after a create whose reply parsed and whose
label is in place (`CreateLabelled`), `Launch` reads the start times of
the reply's server pid and pane pid through the Client's `ProcChecker`
(`StartTime`; a value counts only when alive and known, else none is
recorded). It then makes one conditional write,
`(*store.Store).RecordLaunchIdentity(id, 0, token, identity)`. The write
sets `tmux_server_pid`, `tmux_server_started`, `tmux_server_starttime`,
`pane_id`, `pane_pid` and `pane_starttime` and advances `row_version`,
only while the row is `pending` with `row_version` 0 and the launch's
token. It returns a `store.CondResult`:

- `CondApplied`: the six columns were written.
- `CondChanged` (another write came first, such as `find-missing`'s
  mark) or `CondAbsent` (the row is gone): nothing was written, and the
  spawn still succeeds. No hook can write first: until this write the row
  records no pane, and a row that records no pane matches no hook
  (SR-22.9, [the hook gate](#hooks-move-a-row-only-for-its-own-agent)); a
  SessionStart that arrives first waits for this write instead of giving
  up.

This write is what lets the agent's hooks apply: `pane_pid` and
`pane_starttime` are the process every hook's parent is compared with.

A store error gives one `WARN:` line on the Client's logger and does not
change the spawn's result. A reply lost with exit 0 (`CreateLostReply`) is
a success with no identity written; that row takes no hook until its
pane is adopted by `@ad_pane` (SR-3.6: `kill`, and `send-keys` with
`allow_pending`, adopt through `adoptIdentity`, and `find-missing`, once
the row is past its pending grace period, through `adoptInSweep`). A SessionStart that arrives before the identity write
waits for it until the launch start plus the pending grace period or
540 s after it began waiting, whichever comes first (see [the hook
gate](#hooks-move-a-row-only-for-its-own-agent)), so when this write
lands before that, the agent's first hook still applies. With a grace
above 540 s, a write landing after the cap but inside the grace period
leaves the row `pending` until the agent's next hook.

The write is `spawn.RecordLaunchIdentity(w, pc, lg, id, launchVersion,
token, reply)` (`internal/spawn/launch.go`), over the one-method
`spawn.IdentityWriter` that `*store.Store` satisfies. Plain spawn passes
the insert's version 0; `resume` passes the version its move to `pending`
produced and the move's token (see [Resume](#resume)). The WARN line
names only the instance id ("recording the launch identity of instance
<id> failed"); a nil logger logs nothing. **Must use:** every launch
verb records its launch identity through `spawn.RecordLaunchIdentity`,
never with its own start-time reads or its own write.

**Bounded create and outcomes.** The create is bounded by the create
timeout (`create_timeout_ms`, 5 s by default), which `api.New` gives the
production tmux client. Every failure below happens after the insert
(`plainSpawnCreateError`, `internal/spawn/launch_errors.go`, the one
mapping). After every one but "duplicate session" the row stays
`pending`:

| Create result | Error |
|---|---|
| Timed out, or a reply that does not parse with a non-zero exit | `ErrTmuxUnresponsive` |
| Binary cannot be run, or the socket-permission reply | `ErrTmuxNotAvailable` |
| Created but could not be labelled (ended, or the kill failed too) | `ErrTmuxSessionCreate` |
| No-server or no-socket reply, anything else | `ErrTmuxSessionCreate` |
| "duplicate session" | no verb error here: `*spawn.HeldNameError`, handled by the held-name path below |

The `ErrTmuxUnresponsive` description carries the launch-timeout rule:
the session may have been created, the row stays `pending`, and the
caller must not retry until `get` shows the row `ended` or `missing`. A
retried spawn without an explicit id would start a second agent.

For a caller-supplied id the description then names the retry that
works (b.1qq): once the row is `ended` or `missing`, a plain spawn of the
id collides with it (`ErrInstanceIdCollision`), so it ends "...; do not
retry until get shows the row ended or missing; then a retry with this
id uses the reuse opt-in reuse_finished (--reuse-finished on the CLI),
since a plain spawn of the id now collides". A minted id's description ends
with the launch-timeout rule alone, since its retry mints a new id.
`Launch` takes `minted` after the resolved request and passes it to
`plainSpawnCreateError`, which picks the ending by it and by nothing
else. `runSpawn` passes `idCheck == spawn.IDMinted`, so the `IDCheck`
that `ApplyDefaults` returns is its only source; `minted` changes no
behaviour. Resume's and reuse's timeouts end with the rule alone
(`LaunchTimeoutError`): their retry of the same row works.

**Held name** (SR-9.4, SR-1.4, SR-3.10, SR-5.8, SR-14, SR-22.3; PO
2026-09-27 HELD and REVIEW). "duplicate session" proves the create made
nothing. `Launch` returns a `*spawn.HeldNameError` at once, with no
further write, tmux call or log line; it carries the new row's instance
id, the requested name, the launch socket, the token and the insert's
launch start, and never reaches a caller. `runSpawn` hands it to
`spawnHeldName` (`pkg/api/spawn_held.go`), which in this order:

1. Ends the new row with the end write, `store.EndHeldLaunch` (see
   [internal/store](#internalstore), "Versioned writes"), before anything
   else, so a competing write has the least time to change the row
   first. It applies only while the row is `pending` at `row_version` 0
   with the insert's launch start, and sets `state` `ended`, `ended_at`
   in the store's layout, clears the launch start and advances the
   version. It makes no tmux call.
2. Makes exactly one `tmux.Lookup` on the row's socket, for the new row's
   instance id and token with this store's id and no server identity,
   and the requested name as the holder name.
3. Builds the classified error with `heldNameOutcome`
   (`pkg/api/held_name.go`), carrying exactly one row sentence and, for
   `ErrTmuxUnresponsive` and for the `ErrTmuxSessionCreate` of a holder
   that vanished, the retry sentence the end write's result picks
   (below).
4. Writes the lookup's `ad.provenance.disagree` records (reason
   `scope_value` is the only one this lookup can give; `action` is the
   row result), then exactly one `ad.launch.name_held` (source
   `ad_spawn`, `launch` `spawn`), both fail-open, and returns the error.

The row sentence comes from the end write's result:

| End write | Row sentence | `row_result` |
|---|---|---|
| Applied (`CondApplied`) | "the new row was ended" | `ended` |
| Not applied (`CondChanged`, `CondAbsent`) | "the new row changed after this spawn inserted it and was left as it is" | `left_changed` |
| Store error | "the new row could not be ended and stays pending" | `still_pending` |

On a store error the row stays `pending`, the Client's logger gets one
`WARN:` line naming only the instance id and the store's error, and
`find-missing` marks the row once it is past its pending grace period.

The re-lookup's outcome gives the error (each wraps one sentinel):

| Re-lookup outcome | Error (class) |
|---|---|
| One holder, an old label of this store naming the id | `ErrTmuxSessionConflict` (CONFLICT), "left over from an earlier life"; points to "Operator actions" |
| One holder, a label naming another instance id | `ErrTmuxSessionConflict`, "a different instance id": another row's agent, must not be ended; no pointer |
| One holder, another store's label (WD 2026-09-29 STORE) | `ErrTmuxSessionConflict`, "another agent-director store": that store's agent, must not be ended; no pointer |
| One holder, no valid label | `ErrTmuxSessionConflict`, "no valid instance id"; points to "Operator actions" |
| Can't tell, `provenance_conflict` | `ErrTmuxSessionConflict`, "conflicting labels" (no label claim) |
| No holder: the session vanished before the re-lookup | `ErrTmuxSessionCreate` (LAUNCH FAILURE), "session creation failed: duplicate session", led by "instance <id>: " (the new row's id, a minted one included) and naming the session, with the retry sentence below |
| More than one listing entry matches the name, or Can't tell, unreadable (a timeout or an unrecognised reply) | `ErrTmuxUnresponsive` (UNAVAILABLE), with the retry sentence below in place of "retry later" |
| Can't tell, tmux unavailable (socket permission included) | `ErrTmuxNotAvailable` (ENVIRONMENT) |

`ErrTmuxUnresponsive` here never says "retry later" (WD 2026-09-30d
(a), SR-1.4 row "Every error a plain spawn returns after 'duplicate
session'"): a plain spawn of the same id cannot simply be retried, since
once its row is finished a plain spawn of the id collides with it
(`ErrInstanceIdCollision`). It, and the `ErrTmuxSessionCreate` of a
holder that vanished (b.1qq), carry a retry sentence that the end
write's result picks (`heldRetrySentences{Unanswered, Vanished}`):

| End write | `ErrTmuxUnresponsive` (`Unanswered`) | `ErrTmuxSessionCreate`, holder vanished (`Vanished`) |
|---|---|---|
| Applied (`ended`) | "a retry with this id uses the reuse opt-in reuse_finished (--reuse-finished on the CLI) once the name is free, since a plain spawn of the id now collides" (`heldRetryFree`) | the same (`heldRetryFree`) |
| Not applied or store error (`left_changed`, `still_pending`) | "do not retry until get shows the row ended or missing; then " followed by `heldRetryFree`'s sentence (`heldRetryWait`) | the same (`heldRetryWait`) |

Both sentences name the opt-in through `spawn.ReuseOptIn`, in its one
spelling: `heldRetryFree` is built from `spawn.ReuseRetry` and
`spawn.PlainSpawnCollides`, and `heldRetryWait` from
`spawn.LaunchRetryRule` and `heldRetryFree`.

For a timeout or an unrecognised reply the `Unanswered` sentence ends
the shared Can't tell text in place of "retry later"; for more than one
matching entry it follows the row sentence, before the list hint
(`listSessionNameHint`). The `Vanished` sentence follows the
row sentence, and the description is then built by
`spawn.InstanceCreateFailedError`, so it starts
`tmux: new-session failed: instance <id>: tmux session "<name>":`. It
names the id that the sentence's "this id" means, as the unanswered
re-lookup's description does, so a caller whose id was minted can
follow the retry. The conflict and `ErrTmuxNotAvailable` texts carry no
retry sentence.

Every description names the quoted requested name and, when one session
holds it, its tmux id; a single holder's also says whether its label
names this instance id. The Can't tell outcomes go through the shared
`cantTellError` with the row sentence in place of "nothing was done".
No description carries a label value, another row's id, a store id, a
session-environment value or a session-ending command. A holder with
this launch's own label cannot occur (the token is new); it is handled
as a conflict for a human, never as a success.

The holder is never killed, read or typed into, and the path writes
nothing else to the store. After the insert it adds only the one
re-lookup to the create (the end write makes no tmux call); the bound is
SR-13.2's plain-spawn row. Pre-trust's wait for a config lock held by
another process, at most 5 s (see
[Workspace-trust pre-write](#workspace-trust-pre-write)), is separate
and comes on top. For a caller-supplied id the label scan comes
first and already refuses a labelled leftover of this store before any
row exists, so this path meets a leftover only when it appeared after
the scan.

"ended" sticks (SR-9.4, SR-22.9; WD 2026-09-29 HOOK): the ended row
records no pane, so a hook from any process that is not the row's own
agent (a leftover's included) changes nothing and is logged as
`ad.hook.ignored` (`no_pane_recorded`). Afterwards a plain spawn of the
id returns `ErrInstanceIdCollision`, `resume` returns `ErrNoSessionId`
(the row never had a session id), and a spawn with the reuse opt-in
(`--reuse-finished`) is decided by its lookup like any finished row's
reuse.

**Shared launch-error builders** (`internal/spawn/launch_errors.go`,
SR-1.8). Each takes the verb's row sentence as `consequence` and wraps
exactly one sentinel:

- `LaunchTimeoutError(ce, verb, instanceID, consequence)`:
  `ErrTmuxUnresponsive`, the launch-timeout description above, naming
  the verb and ending with `LaunchRetryRule`. Resume and reuse use it as
  it is; `plainSpawnCreateError` goes through the unexported
  `launchTimeoutError`, which takes the ending, to add the opted-in retry
  for a caller-supplied id (`explicitIDTimeoutRetry`).
- `TmuxUnavailableError(ce, socket, consequence)`: `ErrTmuxNotAvailable`.
- `UnlabelledSessionError(outcome, name, consequence)`:
  `ErrTmuxSessionCreate` for `CreateUnlabelledEnded` /
  `CreateUnlabelledRunning`.
- `CreateFailedError(ce, name, consequence)`: `ErrTmuxSessionCreate`
  for every other failed create, and for a held name (resume's or
  reuse's) whose holder vanished before the re-lookup.
- `InstanceCreateFailedError(ce, instanceID, name, consequence)`:
  `CreateFailedError`'s description led by "instance <id>: ", from the
  same format string (the unexported `createFailedError`). Plain spawn's
  held name whose holder vanished before the re-lookup uses it: its
  `consequence` ends with a retry sentence that refers to "this id", so
  the description names the id, a minted one included (b.1qq).
  `heldNameOutcome` uses it only when the verb gives a `Vanished` retry
  sentence, which only plain spawn does.
- `RowStaysPending` is the sentence "the row stays pending". Plain spawn
  uses it after every create failure but "duplicate session", `resume`
  after a timeout, and reuse after a timeout inside its own sentence
  `reuseRowResetStaysPending` ("the row was reset; the row stays
  pending").
- `LaunchRetryRule` is the launch-timeout rule's sentence, "do not retry
  until get shows the row ended or missing".
- `ReuseOptIn` names the reuse opt-in in its one spelling, "the reuse
  opt-in reuse_finished (--reuse-finished on the CLI)", for a description
  every surface shows. It is built on `manifest.ReuseOptInSpelling`, so the
  error texts and the manifest's spawn and kill Descriptions spell
  it alike: the param name, which MCP takes and the TypeScript client's
  field shares, then the CLI flag. Another spelling does not work on every
  surface: the CLI refuses an undefined flag and MCP an unknown argument,
  both with `ErrInvalidFlags`. `ReuseRetry` ("a retry with this id uses "
  followed by `ReuseOptIn`) and its reason `PlainSpawnCollides` ("since
  a plain spawn of the id now collides") make up the opted-in retry
  sentence. Plain spawn's explicit-id timeout ending and `pkg/api`'s
  held-name retry sentences (`heldRetryFree`, `heldRetryWait`) are built
  from them. `pkg/api`'s `launchInProgressNoSessionStep`, resume's
  launch-in-progress step for a `pending` row with no session id, is built
  on `ReuseOptIn` directly.

`plainSpawnCreateError` and the outcome mapping resume and reuse share
(`finishedLaunch.outcome`, `pkg/api/finished_launch.go`) are both built
from these.
**Must use:** a launch verb builds its create-failure descriptions with
these builders and never re-spells their text. A failed-create
description whose consequence refers to "this id" is built with
`InstanceCreateFailedError`, never by adding the instance id to
`CreateFailedError`'s text or by a second format string. An error
description that names the reuse opt-in builds it from
`spawn.ReuseOptIn` (directly or through `ReuseRetry`), never writing one
surface's spelling alone.

**Shared held-name components** (`pkg/api`):

- `heldNameOutcome(res, instanceID, name, socket, rowSentence, retry,
  examined)` (`held_name.go`) classifies a re-lookup's holder after
  "duplicate session" and builds the SR-1.4 description around a
  caller-supplied row sentence and retry sentences, returning the holder
  facts (`heldNameHolder`) the trail record needs. It is verb-agnostic:
  `retry` is a `heldRetrySentences{Unanswered, Vanished}`. `Unanswered`
  follows the `ErrTmuxUnresponsive` errors, `""` keeping the default
  (unreadable ends with "retry later"; the ambiguous holder gets no
  retry sentence); `Vanished` follows the row sentence of a vanished
  holder's `ErrTmuxSessionCreate`, `""` meaning none. With `Vanished`
  set the description goes through `spawn.InstanceCreateFailedError`
  and so names the instance id; with it empty it goes through
  `spawn.CreateFailedError` and does not. Resume and reuse pass
  `Unanswered: retryLater`, so their ambiguous holder also ends with
  "retry later", and no `Vanished`, so their vanished holder's text is
  unchanged.
  `examined` is nil for plain spawn, whose row did not exist before its
  create. A verb whose row existed before its create (resume and reuse,
  through `finishedLaunch.heldName`) passes a `heldExaminedRow`: the starting-session facts of the
  row as examined before its move or reset (`startingSessionRow`: the
  recorded name, `ended_at`, `pid` and session-id presence), the
  effective bound and window (`startingSessionLimits`) and `Now`, its
  clock read once after the re-lookup. With it the holder is routed by
  class: an old label to the Leftover refusal (`preLaunchLeftoverError`,
  whose `consequence` parameter takes the row sentence; the pre-launch
  lookup passes `preLaunchNothingWritten`), a current label to the
  starting-session refusal (`checkStartingSession`, with the row sentence
  as `startingSessionRow.Consequence`), and every other class to
  `heldHolderError` as for plain spawn. `heldHolderError` and
  `ambiguousHolderError` take a `holderPhrasing`: `heldAfterDuplicate`
  ("already exists (duplicate session)") after a create, or
  `heldBeforeLaunch` ("already exists") at the pre-launch lookup of
  resume and reuse (reuse's new-name pre-check).
  Plain spawn's three row sentences (`heldRowEnded`, `heldRowLeftAsIs`,
  `heldRowStaysPending`) and two retry sentences (`heldRetryFree`,
  `heldRetryWait`) are the unexported constants beside it, the single
  source of that wording; `spawnHeldName` picks the retry sentences from
  the end write's result (applied: `heldRetryFree` for both `Unanswered`
  and `Vanished`; otherwise `heldRetryWait` for both). Resume's and reuse's row sentence is their
  restore's: `restoreResultOf` (`finished_launch.go`) is the one mapping
  from the restore's outcome to the sentence (`restoreSentence*`, and the
  changed and removed sentences built from the verb's launch write:
  "resume moved it to pending", "this spawn reset it"), the `row_result`
  and the store error.
- `emitNameHeld(nameHeld{...})` (`name_held_trail.go`) is the one
  emitter of `ad.launch.name_held`, with its source (`nameHeldSourceSpawn`,
  `nameHeldSourceResume`), launch (`nameHeldLaunchSpawn`,
  `nameHeldLaunchResume`, `nameHeldLaunchReuse`) and row-result constants (`nameHeldRowEnded`,
  `nameHeldRowRestored`, `nameHeldRowLeftChanged`,
  `nameHeldRowStillPending`, `nameHeldRowNotInserted`), the by-hand
  attach and end commands and their quoting (`shellQuote`).
  `nameHeldSourceResume` (`ad_resume`) is the one source constant of
  every record resume writes; `nameHeldSourceSpawn` (`ad_spawn`) is
  plain spawn's and reuse's. The label scan (`emitScanNameHeld`, a thin
  wrapper), plain spawn's held-name path and the one resume and reuse
  share (`finishedLaunch.heldName`) call it. `find-missing`'s sweep
  calls it too (`emitRowNameHeld` in `find_missing_lookup.go`, once per
  mark attempt with tick reason `tmux_name_held`): source
  `nameHeldSourceFindMissing` (`ad_find_missing`), no launch or outcome
  (null), and row result `nameHeldRowMarkedMissing`,
  `nameHeldRowLeftChanged` or `nameHeldRowStillPending` from the mark.
- `heldHolderFacts(res)` (`held_name.go`) is the one derivation of the
  holder facts (`heldNameHolder`) a record carries from a lookup `Result`:
  identified only when exactly one session holds the name, its class only
  when the verdict is not Can't tell. `heldNameOutcome` uses it for a
  launch, and the sweep calls it directly on the row's lookup.

**Must use:** any verb that handles "duplicate session" or a held name
builds its error with `heldNameOutcome` and its record with
`emitNameHeld`, adding its own row sentence and source, launch and
row-result constants beside the existing ones; it never re-spells the
case words, the row sentences, the retry sentences, the field set or
the commands. Every `ad.launch.name_held` record, the sweep's included,
is written by `emitNameHeld` with holder facts from `heldHolderFacts`;
a sweep never builds the record or the holder facts another way.

**Accepted risks of plain spawn.** A failed label step followed by a
failed kill leaves an unlabelled session of agent-director's that may
run; the error says so. A plain spawn's recorded socket comes from the
caller's tmux environment (`TMUX`, `TMUX_TMPDIR`), so a caller with a
different environment launches on a different server.

**A launch onto an existing row** (`resume`, see [Resume](#resume); reuse,
see [Reuse of a finished id](#reuse-of-a-finished-id)). Its socket comes from `spawn.ResolveRowLaunchSocket(recorded)`
(`internal/spawn/launchid.go`, SR-3.3). A recorded socket is returned
exactly as recorded after `tmux.EnsureSocketDir`: an existing directory
is left unchecked, and a vanished per-user directory (`tmux-<uid>` under
an existing parent) is created again with mode 0700 and checked as tmux
checks it. Any other missing directory is refused with
`ErrTmuxNotAvailable`, through the same refusal builder as
`ResolveLaunchSocket`. A row with no recorded socket (one from before
this release) resolves one from the caller's environment exactly as a
plain spawn does, and the launch records it. The function makes no tmux
call and no write. `spawn.ComposeRelaunch` builds the `CreateRequest`
(the recorded name verbatim, cwd, env, the `claude --resume` argv, the
socket, the new token and the store id) without a tmux call or a write.
`spawn.Relaunch(t, req)` is `CreateAndLabel` on that request and returns
its `CreateOutcome` unchanged. So a resumed session is labelled at
creation exactly as a plain spawn's is, including the label by id for a
`$` or `\` name and the relabel-or-kill of a failed label step. Reuse
composes its request with `spawn.ComposeLaunch`, as a plain spawn does
(the requested name and the new request's fields), fills in the socket,
the new token and the store id, and calls `spawn.CreateAndLabel` on it.
**Must use:** a launch onto an existing row resolves its socket with
`ResolveRowLaunchSocket` and creates its session with the shared
create-and-label step, never with a name-based create.

**Where callers see the launch start.** `status`, `get` and `list` carry
`launch_started_at` (RFC3339 UTC, millisecond precision) only on a
`pending` row whose stored launch start is a non-zero integer inside the
years 0 to 9999 UTC; otherwise, on any other state included, the key is
omitted, never `null` (SR-22.2). An out-of-range stored value omits the key
and the call still succeeds (SR-5.5). One helper, `launchStartedAt` (`pkg/api/get.go`), makes that
projection for all three. Any verb that reports the launch start must use
`launchStartedAt`, not its own state check or timestamp format. `status`
reads the launch start through an optional narrow read: if its
`StatusStore` also implements the unexported `statusReader`
(`SpawnStatus`, one read by primary key of `state` and
`launch_started_at`), `Status` uses it. `*store.Store` implements it, so
`StatusStore` and `Status` keep their signatures, and an injected store
without it gives no launch start (SR-16.1).

**Where the socket is shown.** `get` alone shows the row's recorded socket,
as `tmux_socket` on `SpawnRow` (`SpawnRow.TmuxSocket`, filled from
`Identity.Socket`; SR-3.3 "Shown by `get`", SR-16.1). It is the absolute
path of the socket the row's latest launch recorded, on a row in any state:
a finished row keeps its latest launch's socket. A row from before this
release records none, and the key is then omitted, never `null`
(`omitempty`; the TypeScript `GetResult` has `tmux_socket?: string | null`,
SR-16.4). `status` and `list` do not carry it, and no error description
names it. It is display only: no verb reads it back, and a launch onto an
existing row takes its socket from the store through
`ResolveRowLaunchSocket`, never from a caller. It is the `-S` socket of the
README's "Operator actions" commands. A plain spawn refused by the label
scan ("left over from an earlier life") inserts no row, so `get` has
nothing to show; that socket comes from the refusal's
`ad.launch.name_held` trail record instead. After a held name the row
exists (`ended`, unless the end write did not apply), and `get` shows
its `tmux_socket`, the socket the holder is on.

### Reuse of a finished id

`spawn` with the reuse opt-in starts an explicit id again on its own
finished row, in place, without a `delete` (SRD SR-10, SR-18.10). The
path is `runSpawnWithReuseStore` → `spawnReuse` (`reuseExamine`, then
`reuseChangeAndLaunch`) in `pkg/api/spawn_reuse.go`; its store side is
`internal/store/reuse.go` (see [internal/store](#internalstore),
"Versioned writes").

**The parameter.** CLI `--reuse-finished`, MCP `reuse_finished` (the
manifest name), Go `SpawnParams.ReuseFinished`, TypeScript
`reuse_finished` (passed as the flag only when true); advice every
surface shows spells it `manifest.ReuseOptInSpelling`, "reuse_finished
(--reuse-finished on the CLI)". The manifest text is the constant
`reuseFinishedDescription`. What it says, and what holds:

- It applies to finished rows only (`ended` or `missing`). A live row,
  `pending` included, still collides with `ErrInstanceIdCollision`, as
  does a row that changed or was removed after this spawn examined it;
  nothing is changed then.
- It has no effect without an explicit `claude_instance_id` (a minted id
  cannot collide).
- It applies to this one call only. It is not a template parameter:
  `make-template` refuses it with `ErrInvalidFlags` (on the CLI an
  undefined flag, over MCP an unknown parameter), no template records it,
  and a hand-edited template carrying it fails to load
  (`ErrTemplateMalformed`).
- The default (off) is unchanged: any existing row with the id collides.
- A successful spawn does not say whether it created a fresh row or reset
  a finished one; the result is the id and `pre_trust`.
- A reused id starts with no memory of its earlier lives: `resume` and
  `get` never use or show an earlier life's history, so the earlier
  conversation cannot be resumed through agent-director after a reuse
  (SR-8.7; see "History belongs to a life" under
  [internal/store](#internalstore)).
- Retrying a failed plain spawn: after "duplicate session" the row is
  already `ended` (unless the error says the end write did not apply), so
  an opted-in retry is decided by its lookup at once; after any other
  failed launch the row stays `pending`, and an opted-in retry collides
  until `find-missing` marks it `missing`, which happens only after the
  pending grace period (60 s by default).
- Feature detection: a caller reads the version of the binary that serves
  it. On the CLI, the `version` verb; over MCP, the `version` tool, which
  reports the running `serve` process's own version until that process
  restarts; in the TypeScript client, `binaryVersion` (from
  `Client.create()`, or the `version` that `resolveSystemBinary()`
  returns), never the client's `version()` method, whose field is the npm
  package's version; Go links the library, so the field exists at compile
  time. A release candidate `X.Y.Z-rc.N` counts as `X.Y.Z`. A build
  without a release stamp reports `0.0.0-dev` (a `make` build; the
  TypeScript probe accepts it as its development sentinel) or `dev` (a
  plain `go build`; the probe rejects it as unparseable). A caller that
  finds either cannot compare versions and relies on the older-binary
  behaviour. `version` carries no capability list.
- Older-binary behaviour: the CLI and the TypeScript client return
  `ErrInvalidFlags`; MCP silently ignores the parameter, so a finished row
  gives `ErrInstanceIdCollision`. Over MCP this also holds for a `serve`
  process of 0.11.0-rc.1, which counts as 0.11.0 and knows the parameter
  only as `reuse-finished`: it silently ignores `reuse_finished`, and the
  version check cannot tell it from 0.11.0.
- Same environment (SR-18.7): use it as the same user and in the same
  tmux environment as the agents. Two consequences: `kill`'s success on a
  finished row is not verification that the agent exited; and on the
  wrong tmux server, a row wrongly marked `missing`, `kill`'s no-op
  success and a reuse together start a second agent for the same id (see
  [Same environment](#same-environment)).

**Gating.** The explicit-id control-character check runs first, then
`Resolve` and `Validate` (the new cwd is checked before anything
changes). The reuse branch is taken only when `ReuseFinished` is set and
the id is non-empty; every other call takes the plain path unchanged.

**Decision table** (SR-10.2). `ReadForReuse` is the one pre-check read;
its answer feeds `ApplyDefaults` through `reusePreCheck`, so the
collision and read-failure mappings stay single (see
[Collision pre-check](#collision-pre-check)).

| Existing row | Result |
| --- | --- |
| Pre-check read fails | `ErrInternal` (`spawn.PreCheckReadError`); no tmux call, nothing written |
| None | The ordinary fresh spawn: the label scan, then `Launch`; an insert collision in a race is `ErrInstanceIdCollision` |
| Live, `pending` included (a `resume`'s launch too) | `ErrInstanceIdCollision`; no tmux call, nothing changed, whatever name the request names |
| Finished | `reuseExamine`, below |

`reuseExamine`, for a finished row, first applies the
unusable-recorded-name guard (SR-3.2) to the row's **recorded** name, as
read by the pre-check, with no second read: a name that is empty, holds a
control character, or holds a character tmux stores differently (`.`,
`:`, invalid UTF-8) → `ErrInternal` (`unusableNameError`, prefixed with
the instance id), with no tmux call and nothing written or changed;
removing the row is a human's decision (README "Operator actions"). The
requested name is not checked here. Then it:

1. Resolves the launch socket with `spawn.ResolveRowLaunchSocket` (the
   row's recorded socket, or the caller's resolved one when it records
   none). A refusal is `ErrTmuxNotAvailable`, with no tmux call and
   nothing written.
2. Makes exactly one `tmux.Lookup` on that socket for the row's examined
   launch identity (`rowLaunch`: instance id, launch token, recorded
   server identity, this store's id), with the **requested** name as the
   holder name. Any adoption the result offers is ignored (reuse writes
   no adoption, SR-3.6), and no pane listing is made.
3. Decides with the shared `decidePreLaunch` on the row as examined
   (`reuseSpawnOf`, `preLaunchRowOf` with `HolderName` set to the
   requested name), with the configured bound and window and the Client
   clock (table below). There is no second tmux call.
4. Writes the decision's `ad.provenance.disagree` records
   (`emitFinishedRowDisagree` with `reuseLaunchVerb`: verb `spawn`,
   source `ad_spawn`, `tmux_session_name` the **recorded** name, action
   `refused` or `proceeded`), one per distinct reason, right after the
   decision and before any trust or store write, on a refusal as on
   proceed. Fail-open.
5. On a Leftover refusal only, re-reads the row once (`reuseLostRace`:
   one `ReadForReuse`, decided by the shared `leftoverLostRace`): a row
   that was removed or no longer holds the examined snapshot (a competing
   reuse's reset or resume's move came first) gives the lost-race
   `ErrInstanceIdCollision` instead; an unchanged snapshot or a failed
   re-read leaves the Leftover refusal.

| Lookup outcome | Result |
| --- | --- |
| Ours (the row's own session, whatever its name) | The starting-session rule, stopping window first on the examined `ended_at`: `ErrTmuxUnresponsive` "appears to still be stopping" or "appears to still be starting", else `ErrTmuxSessionConflict` "this row's own id"; quoting the recorded name |
| Leftover (an earlier launch's labelled session) | `ErrTmuxSessionConflict` "left over from an earlier life" (`preLaunchLeftoverError`, "nothing was written"), then the one lost-race re-read (step 5) |
| Gone while the recorded agent process runs | The rule with no session: "appears to still be stopping" inside the window, else the own-id conflict |
| Gone otherwise: the new-name pre-check (SR-10.8) on the same listing, quoting the requested name | No holder (or a no-server or no-socket Gone, which lists nothing) proceeds; one holder with another instance id's, another store's or no valid label is `ErrTmuxSessionConflict` by its class ("a different instance id", "another agent-director store", "no valid instance id"); more than one matching entry is `ErrTmuxUnresponsive`, retry later |
| Can't tell | `cantTellError` with the recorded name: a different server and tmux unavailable `ErrTmuxNotAvailable`, `provenance_conflict` `ErrTmuxSessionConflict` "conflicting labels", unreadable `ErrTmuxUnresponsive` |

An `ended_at` that is NULL or does not parse skips the stopping window
(and the own-id text then says nothing about when the row ended), as does
a row that records neither a pid nor a session id. Another store's
session is never Ours or Leftover; it is met only as the requested name's
holder.

A refusal before the change writes nothing (no archive, reset,
permission-request deletion, trust entry or `ad.spawn.*` event, and no
blocking session touched), so re-issuing the spawn later is safe; only
the `ad.provenance.disagree` records may have been written.

**Token, composition, pre-trust.** On proceed, `reuseChangeAndLaunch`
mints a launch token (`spawn.NewLaunchToken`; a failure is `ErrInternal`,
nothing written) and composes the launch with `spawn.ComposeLaunch` (the
create request and the new life's request fields, the parent id from the
caller's `AGENT_DIRECTOR_INSTANCE_ID`; a failure writes nothing). Then
pre-trust (`spawn.PreTrust`) for the new request's cwd and extra env,
following this call's own `no_pre_trust`. It never refuses; its outcome
is the result's `pre_trust`. A reset that then loses its race leaves the
trust entry written, which is harmless.

**The single change** (SR-10.3; `ResetForReuse`). One reading of the
Client clock gives the new life's `started_at`, `last_seen_at` and launch
start. The fresh row is `ComposeLaunch`'s row with that reading, the new
token, the prepared socket and the call's `NoPreTrust`. `ResetForReuse`
applies, in one transaction that holds the write lock before it reads,
and only while the row is still finished with the examined snapshot:

- the keyed archive of the current session (if any) into
  `session_history`, in the life the reuse ends, fail-closed;
- the reset to `pending` in life + 1: session id, transcript path, pid,
  start time, liveness columns, `ended_at` and the six server and pane
  identity columns cleared; the new token and socket; the request fields
  and `parent_id` from the new request; `no_pre_trust` from this call;
  `launch_started_at` set; version advanced. The raw stored `labels`,
  `claude_args` and `extra_env` are never decoded;
- the deletion of every permission request of the id. Children keep
  their parent id.

Outcomes: changed or removed since examined → the lost-race
`ErrInstanceIdCollision` (`reuseLostRaceError`); a store error →
`ErrInternal` through `reuseChangeError` ("archiving the previous session
failed" for an archive failure, else "the reuse could not be applied",
each "and nothing was changed"). Nothing is launched in either case.

**The launch.** The create (`spawn.CreateAndLabel`) follows the commit
with only in-process work between them: the requested name on the
prepared socket, the composed environment and command, the new token,
the instance id and this store's id (the five-field label). Once it
returns, `ad.spawn.reused` is written (see [`ad.*` event namespace](#ad-event-namespace)),
before any restore. The outcome goes through `finishedLaunch.outcome`
(`reuseDeps.launchOnto`), shared with `resume`:

| Create outcome | Result |
| --- | --- |
| Labelled | The identity write (`spawn.RecordLaunchIdentity` with the reset's version and the new token; a failure logs one WARN line and does not fail the call); success: the id and `pre_trust` |
| Reply lost with exit 0 | Success, no identity written |
| Timed out, or a non-zero-exit reply that does not parse | `ErrTmuxUnresponsive` with the launch-timeout description and "the row was reset; the row stays pending"; no restore, the row stays `pending` in its new life |
| "duplicate session" | `finishedLaunch.heldName`: one re-lookup of the requested name against the examined launch identity, the restore, the holder's classified error (as in [After "duplicate session"](#after-duplicate-session), with the examined `ended_at`), the re-lookup's disagree reasons not already written, and one `ad.launch.name_held` (`launch` `reuse`) |
| tmux unavailable (the binary cannot be run, or the socket-permission reply) | The restore, then `ErrTmuxNotAvailable` |
| A session that could not be labelled, or any other failure | The restore, then `ErrTmuxSessionCreate` |

**The restore** (SR-10.4; `RestoreAfterFailedReuse`). Attempted exactly
once on every failed launch but a timeout, with the reset's version, the
examined raw life and a failure time read from the Client clock. Applied
only while the row is `pending` at the reset's version, it writes the
pre-reuse life back exactly, the life number included, so that life's
visible history is exactly as before the attempt; `ended_at` is the old
value or the failure time, and `parent_id` the old value only if that row
still exists. The launch error's last sentence says what it did:

| Restore | Row sentence | `row_result` |
| --- | --- | --- |
| Applied | "the row was restored to its prior state, ended" (or "missing") | `restored` |
| The row changed since the reset | "the row changed after this spawn reset it and was left as it is" | `left_changed` |
| The row was removed | "the row was removed after this spawn reset it, so nothing was restored" | `left_changed` |
| Store error | "the row could not be restored and stays pending", plus one `WARN: spawn: restoring instance <id> to its prior state after a failed launch failed: …` line on the Client's logger | `still_pending` |

Each attempt writes one `ad.spawn.reuse_restored` (`applied` false unless
it applied). The launch error is returned whatever the restore did.

**Concurrency** (SR-10.5).

- Two opted-in spawns of one finished id: the reset's condition lets one
  win; the other gets `ErrInstanceIdCollision` (the row changed, it is
  now live, or its lookup met the winner's session as Leftover and the
  re-read found the row changed) and creates no tmux session. History is
  neither lost nor duplicated.
- Reuse against `resume`: whichever writes first launches. A reuse that
  loses gets `ErrInstanceIdCollision` in every order (the resume's move
  came after the reuse examined the row, so the reset finds it changed,
  or before, so the row is live). A `resume` that loses gets
  `ErrSpawnNotResumable` (or `ErrSpawnNotFound`). If the resume's launch
  failed and its row was restored, a reuse is decided by its lookup like
  any finished row.
- Reuse against `expire`'s delete: a reset that finds the row deleted
  launches nothing and returns `ErrInstanceIdCollision`.

**Ceiling.** SR-13.2's row for `spawn` with reuse: max(Q + C + 2A + 4W,
2Q + C + 3W), **10.9 s** at the defaults (path (ii) 8.3 s). Pre-trust's
wait for a config lock held by another process, at most 5 s (see
[Workspace-trust pre-write](#workspace-trust-pre-write)), is separate
and comes on top.

**Reusable components (must use).** Reuse is built from the shared
pieces, and a later change keeps it that way:

- The composition step `spawn.ComposeLaunch` (`internal/spawn/compose.go`)
  is the only composition of a resolved request, for a plain spawn's
  insert and reuse's reset; never a second composition path. The parent
  id comes only from `spawn.ParentIDFromEnv`.
- Reuse's descriptions come only from `pkg/api/spawn_reuse_errors.go`
  (`reuseLostRaceError`, `reuseChangeError`, `reuseRowResetStaysPending`;
  see [Single-row verb helpers](#single-row-verb-helpers-pkgapi)).
- The decision uses Epic 16's helpers, not copies: `decidePreLaunch` with
  `preLaunchRow.HolderName`, the starting-session rule
  (`checkStartingSession`), `leftoverLostRace` and `emitLookupDisagree`.
- The launch outcome, restore and "duplicate session" path are
  `finishedLaunch` with `reuseLaunchVerb` (`finished_launch.go`); the
  held-name error is `heldNameOutcome` and the record `emitNameHeld` with
  `nameHeldLaunchReuse` (see [Launch identity](#launch-identity), "Shared
  held-name components").

### Workspace-trust pre-write

Claude Code shows a one-time "Quick safety check: Is this a project you
created or one you trust?" modal the first time it sees a new cwd. The
modal blocks before `SessionStart` fires, so a Spawn into a fresh cwd
sits in `pending` until something answers the modal.

Every launch pre-trusts the agent's folder through one shared step,
`spawn.PreTrust(cwd, extraEnv, off)` in `internal/spawn/pretrust.go`: a
plain `spawn` (`spawn.Launch`, with the resolved cwd, extra env and
`NoPreTrust`), a reuse (`reuseChangeAndLaunch` in
`pkg/api/spawn_reuse.go`, with the new request's cwd, extra env and this
call's `NoPreTrust`) and `resume` (`resumeAfterJsonl` in
`pkg/api/resume.go`, with the row's cwd, extra env and recorded
`NoPreTrust`). **Must use:** any launch path, current or future,
pre-trusts by calling `spawn.PreTrust`; do not call `preTrustCwd`
directly or write `.claude.json` any other way. `PreTrust` returns a
`PreTrustOutcome` (`ok`, `skipped`, `failed`) and never returns an
error; every caller carries it into its result as `pre_trust` (see "The
`pre_trust` result field" below).

Placement: pre-trust runs after every check that can refuse the launch
without a write, and immediately before the write that begins the
launch: the pending insert for `spawn` (after the socket, token, env
compose and `--settings` synthesis), the reset for a reuse (after its
pre-check, lookup and new-name pre-check, token and `ComposeLaunch`),
the move to `pending` for `resume` (after its guards, transcript search,
control-character check, socket resolution, pre-launch lookup, token and
`ComposeRelaunch`). A launch refused before pre-trust writes no trust
entry. A launch refused after it (a `spawn` whose insert collides with a
row inserted by a race after its collision pre-check, a reuse
whose reset finds the row changed or gone or fails in the store, a
`resume` whose move finds the row changed or gone) leaves the entry
written, which is harmless: it only marks the folder trusted. Because pre-trust runs before the launch-start
write, it never lengthens the window between the launch start and the
create.

The step resolves the target `.claude.json` (`claudeJSONFor`) from the
launch's extra env `CLAUDE_CONFIG_DIR`:

- Absent or empty: the operator's `~/.claude.json`.
- An absolute path: `<CLAUDE_CONFIG_DIR>/.claude.json`.
- Set but not an absolute path (relative, `~`-prefixed or
  whitespace-only): refused (b.nje). Pre-trust reads, writes and stats
  no file, makes no lock dir, and reports `failed` (below). Claude Code
  resolves a relative value against its pane's cwd, while agent-director
  would resolve it against its own caller's cwd, which differs for each
  caller, so the entry could land in a file the agent never reads.
  Falling back to `~/.claude.json` could also write a file the agent
  never reads, so pre-trust does not fall back.

For `resume` the extra env is the row's, so it targets the same file the
row's spawn did. With a file resolved, the step sets
`projects.<canonical cwd>.hasTrustDialogAccepted = true` and writes
the file back atomically (temp + rename), so no reader ever sees a torn
file.

**One rule for `CLAUDE_CONFIG_DIR` (b.1ba, b.nje).** Whether a
`CLAUDE_CONFIG_DIR` value is usable is `spawn.ConfigDirUsable`
(`internal/spawn/pretrust.go`): only an absolute path is. Every reader
of the value shares it: pre-trust's file resolution above, and the
transcript paths `resume`'s fallback and `find-missing`'s heal compose
(see [JSONL path resolver](#jsonl-path-resolver-internalspawnjsonlgo)).
Only the response to an unusable value differs: those read paths treat
it as absent and look under `~/.claude`, while pre-trust, which writes,
refuses it. **Must use:** code that reads `CLAUDE_CONFIG_DIR` from a
launch's or row's extra env decides whether to use it with
`spawn.ConfigDirUsable`; do not write a second check.

**Config lock (b.zjm).** Claude Code writes the same file, at startup
and while it runs, and saves it under a lock: it takes the lock,
re-reads the file, applies its change and writes the whole file. Pre-trust
takes the same lock in the same way around its own read-modify-write, so
neither side loses the other's update, and concurrent pre-trusts of one
file (parallel launches on one `HOME` or one `CLAUDE_CONFIG_DIR`) run
one at a time, so none drops another's entry. Neither guarantee holds
while two processes break the same stale lock at once: both may then
hold the lock, as they can under proper-lockfile itself. The takeover
check before the write (below) makes the one whose lock was replaced
report `failed` rather than write in most such cases, but not in all.
The lock is Claude Code's proper-lockfile protocol, taken by
`lockConfig` in `internal/spawn/configlock.go`:

- The lock is the directory `<resolved file>.lock` (the resolved path as
  is, plus `.lock`; no symlink is resolved), made with `mkdir`. Pre-trust
  checks that the file exists before it takes the lock, so a missing file
  creates neither the file nor the lock dir.
- An existing lock dir is held by another process (Claude Code or another
  agent-director) unless its mtime is more than 10 s old
  (`configLockStale`, proper-lockfile's stale limit as Claude Code uses
  it). A stale lock dir is removed with `rmdir` and the `mkdir` tried once
  more.
- While the lock is held and not stale, pre-trust waits and tries again
  with backoff (25 ms, doubling up to 500 ms, each wait stretched by a
  random factor in [1, 2)), for at most 5 s in total (`configLockWait`, a
  fixed value, not a setting), then gives up and writes nothing. Any
  other failure to take the lock fails at once. A launch waits only while
  another process holds the lock, and pre-trust adds at most about 5 s to
  it.
- Under the lock pre-trust reads the file, sets the one key and writes
  the file atomically. Just before the write it makes two checks
  (`checkHold`), in this order, and writes nothing if either fails:
  - Hold time: it does not refresh the lock dir's mtime, so it writes
    only if it has held the lock for at most 5 s (`configLockMaxHold`,
    half the stale limit); past that another process may judge the lock
    stale.
  - Takeover: the lock dir must still be the one it made, the same
    directory with the same mtime (`owned`, the check the release
    shares). A lock dir that is gone or replaced means another process
    broke the lock as stale and may hold it now. The check cannot catch
    a process that takes the lock over after it; that process still
    races the write.
- It releases the lock with `rmdir`, but only while the lock dir is still
  the one it made (`owned`): a lock removed as stale and taken by another
  process is left alone. A lock dir can be left behind, for example when
  agent-director's process is killed while it holds the lock
  (agent-director installs no exit handler to remove it). It then blocks
  other takers for up to 10 s, until it goes stale and the next taker
  breaks it. Claude Code's own retries on a held lock last longer than
  10 s, so its save still goes through; another agent-director pre-trust
  of that file in that window may give up after its 5 s wait and report
  `failed`.

Every lock failure is a pre-trust failure, reported as `failed` (below).
Lock failures have no outcome, error name or result value of their own.
**Must use:** `lockConfig` is the one implementation of Claude Code's
config-file lock; code that writes a Claude Code config file under that
lock takes it through `lockConfig`, never a second implementation.

Claude Code has a feature-gated storage path for this file that may use
a different lock. A Claude Code write on that path can still drop the
trust entry, and the agent may then stop at the folder-trust prompt.

`no_pre_trust` (`--no-pre-trust` on the CLI, `SpawnParams.NoPreTrust` in
Go) opts out for callers that
explicitly want the human-in-the-loop trust dialog — e.g. spawning into
a directory handed in by an untrusted caller. The flag defaults off, so
pre-trust is the default behavior. With the opt-out, `PreTrust` attempts
nothing, touches no file and prints nothing.

The choice is recorded on the row at the spawn's pending insert
(`no_pre_trust`, see [internal/store](#internalstore)), and every
`resume` of that life honours it: `resume` passes the recorded
`NoPreTrust` as `off`, so over an opt-out it attempts no pre-trust and
writes no trust entry. The move to `pending` and the restore after a
failed launch keep the column, so a retry after a restored launch
failure honours it too. `resume` has no pre-trust parameter, and no
verb, parameter or setting turns pre-trust back on over a recorded
opt-out. Rows from before this release carry the column's default
(pre-trust allowed), so their `resume` pre-trusts whatever their
original spawn chose.

A reuse is a new spawn call: it pre-trusts through the same shared step
by its own call's `no_pre_trust`, whatever an earlier life chose, and its
reset records that choice on the row, overwriting the previous life's
(SR-18.13, SR-22.6). Every `resume` of the new life then follows it. A
failed reuse's applied restore writes the previous life's choice back
with the rest of that life.

Pre-trust is best effort on both verbs: a failure never fails the
launch. When the write cannot be made (the extra env's
`CLAUDE_CONFIG_DIR` is set but not an absolute path, or the resolved
file does not exist, as on a fresh Claude Code install or a fresh
`CLAUDE_CONFIG_DIR`, or it cannot be read, parsed or written, or its
lock cannot be taken, stays held by another process through the 5 s
wait, was held too long to write under, or was taken over by another
process before the write), `PreTrust` returns `failed` and
prints one line to stderr (`preTrustWarn`): `agent-director: pre-trust
failed for <path> (<reason>); the agent may stop at Claude Code's
folder-trust prompt`, with the reason "file does not exist" for a
missing file, `pre-trust: lock <path>.lock held by another process;
gave up after waiting 5s` for a lock that stayed held, and `pre-trust:
lock <path>.lock was taken over by another process, so wrote nothing`
for a lock taken over before the write. An unusable `CLAUDE_CONFIG_DIR`
resolves no file, so its line names none and quotes the value (Go `%q`)
instead: `agent-director: pre-trust failed (pre-trust: CLAUDE_CONFIG_DIR
"rel" is not an absolute path); the agent may stop at Claude Code's
folder-trust prompt`. The launch proceeds, and the agent may wait at the
trust dialog.

**The `pre_trust` result field.** Every successful `spawn` and `resume`
result carries `pre_trust`, always exactly one of three values:

- `ok`: the folder-trust entry was written.
- `skipped`: pre-trust was off for this launch, so nothing was attempted.
  For `spawn`, a reuse included, the caller passed `no_pre_trust`. For
  `resume`, the row records that the spawn or reuse that began its life
  opted out; this holds on
  every `resume` of that life, including a retry after a launch failure
  whose row was restored.
- `failed`: pre-trust was attempted and the entry was not written (the
  `.claude.json` file is missing, or could not be read, parsed or
  written, its lock held by another process included, or the extra
  env's `CLAUDE_CONFIG_DIR` is set but not an absolute path, so no file
  was touched). The launch still proceeds, and the agent may stop at
  Claude Code's folder-trust prompt.

A `failed` pre-trust never fails the launch; a launch that fails returns
its error, not a result. The value is the `PreTrustOutcome` the shared
step returned, carried unchanged into the result by both verbs
(`string(outcome)` in `pkg/api`'s `runSpawn`, `reuseChangeAndLaunch` and
`resumeAfterJsonl`);
it is never recomputed from the file. The standard-error warning line
above is printed only for `failed`, uses the same word, and is for humans
at the CLI. Callers read the field on every surface (CLI, MCP, Go,
TypeScript); standard error never reaches an MCP caller. The field is
declared in the manifest's `spawn` and `resume` result fields (with its
allowed values), as Go `SpawnResult.PreTrust` and `ResumeResult.PreTrust`
(a plain `string`; `pkg/api` exports no type or constant for it) and as
the TypeScript `pre_trust: "ok" | "skipped" | "failed"` on `SpawnResult`
and `ResumeResult`. A result without `pre_trust` comes from a binary
older than this release.

Every surface honours the opt-out: the CLI's `--no-pre-trust`, MCP's
`no_pre_trust`, Go's `SpawnParams.NoPreTrust` and TypeScript's
`no_pre_trust`. Over MCP, a `serve` process from before 0.11.0, or
0.11.0-rc.1, silently ignores `no_pre_trust` under either spelling, so a
spawn through it still pre-trusts (see
[Parameter names and unknown arguments](#parameter-names-and-unknown-arguments)).

Only `hasTrustDialogAccepted` is touched. Sibling keys
(`hasCompletedProjectOnboarding`, `hasClaudeMdExternalIncludesApproved`,
etc.) have semantics beyond trust and are left alone. Unknown top-level
and per-project keys round-trip verbatim via a `map[string]json.RawMessage`
shape, so future Claude Code releases that add keys are forward-compatible.

Layer boundaries (load-bearing):

- For a plain spawn, `internal/spawn` calls `internal/store` for one
  `SpawnState` pre-check read (caller-supplied id only), one
  `InsertPending` and at most one `RecordLaunchIdentity`, and reads
  `StoreID()`. On "duplicate session" it makes no further call and
  returns `*HeldNameError`. It calls `internal/tmux` for socket resolution
  (`ResolveSocket`) and, through its `LaunchTmux` interface, one
  `NewSession`, at most one `SetLabel` and at most one `KillSessionID`.
  It reads the clock and the start times only through what the caller
  passes in (`now func() time.Time`, a `tmux.ProcChecker`); it does not
  import `internal/probe`.
- For `resume`, `pkg/api` makes the store writes (`MoveToPending`, then
  `RestoreAfterFailedResume` after a failed launch) and calls
  `internal/spawn` for `ResolveRowLaunchSocket`, `NewLaunchToken`,
  `ComposeRelaunch`, `PreTrust`, `Relaunch` (the shared create-and-label
  step through the `LaunchTmux` calls) and `RecordLaunchIdentity`, which writes through
  the `IdentityWriter` it is given. `internal/spawn` opens no store for
  resume.
- For a reuse, `pkg/api` makes the store reads and writes through its
  `reuseStore` (`ReadForReuse`, `ResetForReuse`, then
  `RestoreAfterFailedReuse` after a failed launch) and calls
  `internal/spawn` for `ApplyDefaults` (over the one read),
  `ResolveRowLaunchSocket`, `NewLaunchToken`, `ComposeLaunch`, `PreTrust`,
  `CreateAndLabel` and `RecordLaunchIdentity`. `internal/spawn` opens no
  store for a reuse either.
- The label scan lives in `pkg/api` (`spawn_scan.go`): one `tmux.Lookup`
  through the Client's tmux client and the one `ad.launch.name_held`
  trail record. The held-name path lives there too (`spawn_held.go`):
  the one `EndHeldLaunch` write, one `tmux.Lookup` of the requested name,
  the error from `heldNameOutcome` (`held_name.go`) and the records from
  `emitProvenanceDisagree` and `emitNameHeld` (`name_held_trail.go`).
  `pkg/api` passes the Client's clock (`time.Now`) and
  start-time reader (`probe.NewProcChecker()`) down to `spawn.Launch`.
- `internal/hook` calls `internal/store` (the gated hook writes, and one
  `GetSpawn` read for SessionStart's snapshot and for `ad.hook.ignored`,
  plus SessionStart's read-only `GetSpawn` re-reads while it waits for its
  launch's identity write;
  `HandleNoExecForm`, the no-verb run's `no_exec_form` path, makes no
  store call).
  Never `internal/tmux`, never `internal/spawn`, never `internal/probe`:
  `cmd/agent-director` wires the hook's parent-process readers in
  (`hook.HandleConfig.ParentPID`, `ParentProc`).
- `pkg/api` is the verb-handler surface: it composes `internal/spawn`
  calls for the `spawn` verb and direct `internal/store` reads for
  `status` / `get`. No SQL strings, no tmux argv at this layer.

The hook handler is invoked via the per-Spawn `--settings` JSON
synthesized in stage 4, in exec form (`"command":"<bin>","args":["hook"]`;
see [Spawn-side hook stability](#spawn-side-hook-stability-bue3--sr-18)).
The handler's binary path is resolved via
`os.Executable()` (`/proc/self/exe` on Linux, `_NSGetExecutablePath` on
macOS) so it is always the same binary version that ran the `spawn`
call.

**Emitted per-hook relay timeout.** `synthesizeSettings` emits the relay
window as an explicit per-hook `timeout` field on exactly the `PermissionRequest` and `PreToolUse`
hook entries — placed on the inner command object (sibling of
`type`/`command`/`args`), not on the outer entry that carries `matcher`. Its value is
`config.Relay.EffectiveTimeoutSeconds()` (the configured `relay.timeout_seconds`
when positive, else the `DefaultRelayTimeoutSeconds` fallback of 86400). This is
the same accessor the relay poll loop's deadline derives from
(`internal/hook/polling.go`), so Claude Code's per-hook kill boundary and the
poll deadline are always the identical value. Without the field Claude Code
would kill the polling hook at its own 600-second default per-hook timeout —
discarding the hook's output with no envelope, so a late decision falls open
into the native permission flow. The `SessionStart` `agent-director hook`
entry carries a fixed inner `"timeout": 600`
(`sessionStartHookTimeoutSeconds`, Claude Code's current default, stated so
a change to that default cannot move it), which does not move with the relay
settings: it keeps Claude Code's kill boundary above the SessionStart wait's
540 s cap (`sessionStartWaitCap` in `internal/hook`; SR-22.9, WD
2026-09-30c; see "SessionStart waits for its launch's identity write" in
[Hooks move a row only for its own
agent](#hooks-move-a-row-only-for-its-own-agent)). The other five hook
events and the `inject_help_hook` `SessionStart` entry carry no `timeout`
and are unchanged.
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

A tracked row is externally drivable: a caller can deliver text into the
agent's own pane and read the rendered TUI back out without attaching to
the session. The two verbs (and `pause`, see
[Stop semantics](#stop-semantics)) are the pane verbs: typed Go functions in
`pkg/api` that each make one lookup (`tmux.Lookup`, the session found by
its label on the row's recorded socket) and one pane listing
(`ListPanes`) through the shared tmux client, then act by pane id
(`SendKeysPane`, with `pause`'s `SendKeyPane` before it; `CapturePaneID`).
They share one run, `paneRun`
(`pkg/api/pane_run.go`); see
[Single-row verb helpers](#single-row-verb-helpers-pkgapi). There is no
name-based send or capture. Cross-reference SRD SR-7 (pane verbs), SRD
§4.3 (send-keys multiline semantics), SRD §12 (verb shapes),
`reference/send-keys-research.md` (empirical LF/CR behavior),
`reference/pane-output-research.md` (capture-pane sanitization).

### `send-keys`

`pkg/api/sendkeys.go` holds the flow (`SendKeys`, unexported `sendKeysRun`
and `sendKeysStateGuard`); its tmux phase is the keys verbs' shared
`keysRun` (`pkg/api/pane_keys.go`), which `pause` also uses. Delivery
(SRD SR-7.1, SR-7.2): one lookup (the session found by its `@ad_owner`
label on the row's recorded socket) and, on Ours, one pane listing; then
the text and a separate Enter, both by the agent's pane id
(`SendKeysPane`). Keys are never sent before the lookup returned Ours and
the listing showed the agent's pane. Four behaviors are load-bearing:

- **`\r` (CR, 0x0D) stripped before tmux receives the argv.** Per
  `reference/send-keys-research.md` "CR caveat", a literal CR in the
  payload would submit the buffer at the position the CR appears —
  splitting one logical message into multiple submissions. The verb
  removes CR bytes pre-send so only the trailing Enter submits.

- **`\n` (LF, 0x0A) passed through verbatim.** Claude Code's input
  handler treats LF as "insert newline in input box", not as a submit.
  A multi-line payload composes as one message. The text goes to tmux as
  *one* literal argv element (`send-keys -t %N -l -- <text>`) containing
  the LFs; tmux's own quoting handles them.

- **A single Enter is always appended after the text.** It is a
  *separate* `send-keys -t %N Enter` call to the same pane id, made only
  if the text call succeeded. Mixing the submit byte into the text argv
  would re-introduce the same "premature submission" failure mode the CR
  strip prevents.

- **Empty text is an Enter-only send.** With `text` empty the text call
  types nothing, so the call sends only the Enter, on every surface (CLI
  `--text ""`, MCP, TypeScript, Go; the manifest marks `text`
  `AllowEmpty`), on a `pending` row too with `allow_pending` (dismissing a
  startup prompt). It submits a line already typed, such as a text a
  failed `send-keys` left unsubmitted. Enter on an empty Claude Code input
  submits nothing, so it is safe also when an earlier Enter had in fact
  submitted the text.

State precondition (`sendKeysStateGuard`, before any tmux call): the
row's `state` must be one of `waiting`, `working`, `ask_user`,
`check_permission`. `ended` and `missing` are refused with
`ErrSpawnNotInteractive` and no tmux call, with or without
`allow_pending`. `pending` is refused the same way unless `allow_pending`
is set. The agent's first SessionStart hook moves `pending` to `waiting`,
after which the row is reachable without the flag.

`allow_pending` opt-in (SR-18.14): the flag also allows a `pending` row: a
launch (spawn, reuse or resume) whose agent has not reported in yet. Keys
are delivered only to a session started by the row's current launch.
`ended` and `missing` rows are still rejected. The canonical use case is
dismissing a prompt that Claude Code renders *before* SessionStart, such
as the `--dangerously-load-development-channels` warning or the
workspace-trust modal: the prompt blocks SessionStart, so without the
flag the row never leaves `pending` and `send-keys` keeps refusing. On a
`pending` row with the flag, using the row as `send-keys` read it:

- **No launch start or no launch token** (absent, or a stored value that
  does not parse): `ErrSpawnNotInteractive` (`pendingNoLaunchError`,
  "cannot show that a session belongs to the current launch"), before
  the lookup, with no tmux call and nothing sent (SR-22.8). Without a
  token no label can be current, so the row fails closed.
- **The current-launch check.** Otherwise the keys go only to the
  current launch's session: the one whose `@ad_owner` label carries the
  row's id, its launch token and this store's id (SR-3.4). A fresh
  spawn, a reuse and a resume are reached the same way. A leftover (an
  earlier launch's session holding the name or the id) is Leftover, never
  Ours: `ErrSpawnNotInteractive` (`pendingLeftoverError`, "not this
  launch's session", naming the leftover sessions), nothing sent. A
  different server gives `ErrTmuxNotAvailable`; every other outcome is as
  on a live row (below).

What agent-director does and does not guarantee when a caller types into
a launching session (SR-22.7):

- **Guaranteed.** The keys go only to the current launch's agent pane,
  found by the lookup and the pane listing for the row as `send-keys` read
  it. A SessionStart between that read and the send changes neither the
  session nor the pane, so the keys still land there, whatever the state
  by the time they arrive.
- **Not guaranteed.** That the prompt the caller saw is still showing.
  The agent can report in between the caller's `read-pane` and its
  `send-keys`, or between the text and the Enter of one `send-keys`,
  which are not one atomic step. Text meant for a prompt can then land in
  the agent's input line, and with the Enter it is submitted as a user
  turn. Whether and what to answer is the caller's business: no option
  types only while a row is `pending`, and no verb blocks until the agent
  reports in.

Outcomes on a live row, and on a `pending` row past the checks above (the
full table is SRD SR-7.2; the failure rules SR-7.3):

- **Ours:** one pane listing; the agent's pane is the entry with the row's
  recorded pane id and pid, wherever it now is. A row that records no
  server identity or no pane (a lost create reply) adopts the pane whose
  `@ad_pane` names its launch token, written once through `adoptIdentity`
  (SR-3.6); the write's outcome never changes the result. No agent's pane
  → `ErrTmuxSessionConflict` ("the agent's pane was not found"), nothing
  sent.
- **Leftover** on a live row → `ErrTmuxSessionConflict` ("not this
  launch's session"), nothing sent.
- **Gone** (another store's sessions included) → `ErrTmuxSendKeys` ("the
  row's session is not there"), nothing sent.
- **Can't tell / tmux unavailable** → the single-row verbs' shared mapping
  (`ErrTmuxNotAvailable`, `ErrTmuxSessionConflict` "conflicting labels",
  `ErrTmuxUnresponsive`), nothing sent.
- **A failed text or Enter call.** A timeout → `ErrTmuxUnresponsive`
  saying the keys may have been delivered, with no follow-up call. Any
  other failure makes one follow-up lookup (Gone or Leftover →
  `ErrTmuxSendKeys`; a different server or tmux unavailable →
  `ErrTmuxNotAvailable`; otherwise `ErrTmuxUnresponsive`). Once the text
  call went through and the Enter failed or timed out, every description
  says the text may be typed but not submitted, never that nothing was
  sent.
- **The next step after a keys failure** (b.9o4). A literal retry would
  type the text again after an unsubmitted copy, so where the text may be
  in the pane an `ErrTmuxUnresponsive` ends with the next step in place of
  "retry later" (`sendKeysNext`, through `keysReached`):
  - a timed-out text call: "the keys may have been delivered; read-pane;
    if the text is typed, send-keys with empty text, otherwise the same
    send-keys";
  - a timed-out Enter call: "the keys may have been delivered; the text
    may be typed but not submitted; send-keys with empty text submits it";
  - an Enter call that failed otherwise, whose follow-up lookup found the
    session, could not be read or found conflicting labels: what the
    follow-up found, then "the text may be typed but not submitted;
    send-keys with empty text submits it".

  A text call that failed other than by timing out typed nothing, and its
  `ErrTmuxUnresponsive` still ends "nothing was done; retry later". The gone error and
  `ErrTmuxNotAvailable` give no next step. After a failed Enter the
  Enter-only send needs no read first: on an empty input it submits
  nothing.

`send-keys` never writes the row's state; the adoption is its only store
write. Its tmux calls are at most one lookup, one pane listing, the text
call, the Enter call and one follow-up lookup; its ceiling is
3Q + 2A + 5W, **9 s** at the defaults (SRD SR-13.2, the one source for
this bound). Its trail is `ad.send_keys.called` from `Client.SendKeys` and
`ad.provenance.disagree` with source `ad_send_keys` (see
[`ad.*` event namespace](#ad-event-namespace)).

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

Order of the refusals before any tmux call (`sendKeysRun.run`): the state
guard (which also refuses a `pending` row with no launch start or no
launch token, so that row keeps `ErrSpawnNotInteractive` whatever its
name), then the relay-mode guard, then the unusable-recorded-name check:
a recorded session name that is empty, holds a control character, or
holds a character tmux stores differently (`.`, `:`, invalid UTF-8) →
`ErrInternal` (`unusableNameError`, SR-3.2), no tmux call, nothing sent;
removing the row is a human's decision (README "Operator actions"). Only
then the row's socket and the one lookup.

### `read-pane`

`pkg/api/readpane.go` holds the flow (`ReadPane`, unexported `readPane`
and `readPaneRun`, built on `paneRun`). It captures the last N lines of
the agent's own pane by pane id: `capture-pane -p [-e] -t %N -S -<n>`.
Default `n=25`, no upper cap (SRD §12 explicitly leaves the bound to the
caller). `n` counts lines of history before the visible pane, so
`n_lines: 1` is the smallest capture.

Order (`readPane`): the row read (unknown id → `ErrSpawnNotFound`); then,
for a row in any state, the unusable-recorded-name check: a recorded
session name that is empty, holds a control character, or holds a
character tmux stores differently (`.`, `:`, invalid UTF-8) →
`ErrInternal` (`unusableNameError`, SR-3.2), no tmux call, nothing read;
removing the row is a human's decision (README "Operator actions"). Then
the row's socket and the one lookup.

Outcomes of its one lookup (SRD SR-7.2, SR-7.5):

- **Ours:** one pane listing, then the agent's pane (the entry with the
  row's recorded pane id and pid, wherever it now is) is captured. A row
  that records no pane (a lost create reply) uses the pane whose
  `@ad_pane` names its launch token, found by `findAdoption` for this
  call only, with no write. No agent's pane → `ErrTmuxSessionConflict`
  ("the agent's pane was not found"; for a lost create reply also "the
  agent's pane was not adopted (no pane carries this launch's pane
  label)"), nothing read.
- **Leftover:** with exactly one leftover session of this store, one pane
  listing, then the pane whose `@ad_pane` names that leftover's token
  (`tmux.PaneByToken`) is captured; no such pane → the same "the agent's
  pane was not found" conflict. More than one leftover →
  `ErrTmuxSessionConflict` ("more than one leftover session exists"),
  with no listing and nothing read.
- **Gone** (another store's sessions included) → `ErrTmuxCaptureFailed`,
  meaning only that no session of this agent is there ("the row's session
  is not there"), no listing, nothing read.
- **Can't tell / tmux unavailable** → `ErrTmuxNotAvailable`,
  `ErrTmuxSessionConflict` ("conflicting labels") or
  `ErrTmuxUnresponsive`. `ErrTmuxUnresponsive` and `ErrTmuxNotAvailable`
  give no information about the agent.
- **A failed capture** (SR-7.3): a timeout → `ErrTmuxUnresponsive` with no
  further call; any other failure makes one follow-up lookup (Gone or
  Leftover → `ErrTmuxCaptureFailed`; a different server or tmux
  unavailable → `ErrTmuxNotAvailable`; otherwise `ErrTmuxUnresponsive`).

**`read-pane` changes nothing** (SR-7.5). It sends no keys; creates, kills
and renames no session; writes nothing to the store, so the row's
`row_version` and snapshot are unchanged (`ReadPaneStore` has no write
method); and emits no trail event. It has no state guard: a `pending`,
live or finished row behaves alike, and a finished row whose own session
runs still returns its pane (a post-mortem read). Its results answer only
about the row's own session; it is not a liveness verb, and the row's
state stays the liveness authority (see
[Liveness authority](#liveness-authority)).

**Cost.** At most three tmux calls normally (lookup, pane listing,
capture); only a capture that fails other than by timing out adds the
follow-up lookup. Ceiling 3Q + A + 4W, **6.9 s** at the defaults (SRD
SR-13.2, the one source for this bound).

ANSI handling:

- **Default (`ansi=false`) — strip ANSI escape sequences, preserve
  unicode glyphs.** `tmux capture-pane -p` (without `-e`) already
  removes most SGR / cursor escapes server-side; `internal/tmux.StripANSI`
  scrubs any residuals with a byte-oriented regex (`\x1b\[[0-9;]*[a-zA-Z]`)
  that never touches non-ASCII bytes. The TUI glyphs Claude uses
  (`❯` U+276F, `⎿` U+23BF, `🐝` U+1F41D, box-drawing chars) survive —
  the caller reads them as state signal per
  `reference/pane-output-research.md` "State-signal value".

- **`ansi=true` — `tmux capture-pane -p -e` is invoked.** The `-e` flag
  tells tmux to emit SGR / cursor escapes in its output; the bytes are
  returned verbatim with no verb-layer strip.

### Layer boundaries

- `pkg/api/sendkeys.go`, `pkg/api/readpane.go` and `pkg/api/pause.go`
  are the pane-verb surfaces — typed params in, typed result + error out,
  errors matched via `errors.Is`.
- Each is one row read, one lookup, one pane listing, at most three
  actions (`read-pane`: one capture; `send-keys`: the text and Enter;
  `pause`: the line clear, the text and Enter) and at most one follow-up
  lookup after an action failure. They call `internal/store` for the row
  read (and, for `send-keys` and `pause`, the adoption write) and
  `internal/tmux` through the id-based client methods (`Lookup`'s session
  listing, `ListPanes`, `SendKeysPane`, `SendKeyPane`, `CapturePaneID`),
  each on the row's socket.
- No SQL strings, no shell, no `&&`/`|`/`$VAR` at any layer (SRD §4.3,
  §14.3). tmux invocations use direct `exec.Command` argv, with the text
  as a single argv element after `--`; the one key a key send names
  (`pause`'s `C-u`) is a fixed key name, never caller text. The name-based send and capture no
  longer exist; `HasSession` is the only name-based tmux method left, and
  no verb calls it.

## Label model

Labels are caller-owned tags on a Spawn, surfaced two ways and never
re-read after spawn time. They are not the tmux session's `@ad_owner`
provenance label, which agent-director sets itself (see
[Launch identity](#launch-identity)).

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
  → umask u=rwx, first of all (see "The operator's umask" below)
  → flag checks, before any other check: --sha256 and --admin-sha256 go
    together (exactly one → exit 2)
  → install.sh preflight gates (whitespace-free install path, OS/CPU,
    required tools on PATH incl. sqlite3)
  → with --from-release: download both assets, checking each hash when
    given (a mismatch → exit 3, nothing installed)
  → find both source binaries; --binary and --admin-binary arch probes
  → source-tree version check (a local agent-director in a git checkout
    must be built from HEAD)
  → version-stamp pairing: agent-director and agent-director-admin must
    report the same `version` stamp (version and commit), with a real
    commit, else exit 3
  → --keep-prior: snapshot both existing binaries to their .prior,
    unless this is a re-install of the same pair (then neither; the
    .prior files are kept)
  → stage both binaries: create ~/.agent-director/admin/ (0700) and make
    both sibling temp copies (0755); a failure here replaces neither
  → mv both into place, back to back:
    ~/.agent-director/bin/agent-director and
    ~/.agent-director/admin/agent-director-admin
  → optional ~/.local/bin/agent-director PATH symlink (agent-director
    only; agent-director-admin never gets one, under any option)
  → schema migration at install-time (see below): open/migrate state.db
    under a one-shot migrate-authorized sentinel (full six-step flow in
    install-agent-director/SKILL.md)
  → merge SessionStart + SessionEnd hooks into ~/.claude/settings.json
  → optional MCP registration (--register-mcp)
  → print the admin path once, for the human
```

Pattern B is where the CLI / state / hooks side effects happen.

**The two binaries (b.vqr).** Every install installs both
`agent-director` and the operator tool `agent-director-admin` (see
[Operator tool `agent-director-admin`](#operator-tool-agent-director-admin))
from one build, because both open the same store. The admin source is
`--admin-binary <path>`, else the in-repo `bin/agent-director-admin` beside
the script's checkout (`make build` builds both), or, with
`--from-release`, the release's `agent-director-admin-<os>-<arch>` asset.
It is never looked up on PATH, so an `agent-director` found only on PATH
cannot be paired with it.

- **Hashes go together.** `--sha256 <hex>` verifies the main asset and
  `--admin-sha256 <hex>` the admin asset; both apply only with
  `--from-release` (either without it keeps the "only applies with
  --from-release" error, exit 2). Pass both or neither: exactly one is
  refused before anything is downloaded (exit 2, "--sha256 without
  --admin-sha256 would install agent-director-admin unverified; refusing to
  install.", and the mirror for `--admin-sha256` alone), so asking to
  verify one asset never installs the other unverified. A hash mismatch is
  exit 3 and installs nothing.
- **Stamps must match and carry a commit.** Both binaries' `version` stamps
  (version and commit) must be equal; otherwise, one unreadable stamp
  included, "version stamps differ; refusing to install" (exit 3). Equal
  stamps whose commit is empty or `unknown`, two unreadable stamps
  included, are refused too (exit 3, "carry no commit stamp, so they
  cannot be shown to come from the same build"):
  a plain `go build` reports `{"version":"dev","commit":"unknown"}`, so two
  such builds from different trees would otherwise pass as a pair. `make
  build` in a git checkout stamps both with the checkout's commit, and
  release assets are stamped; the advice for both refusals is `make build`
  or `--from-release`.
- **Both staged before either is replaced.** After any `--keep-prior`
  snapshots, install.sh creates the admin directory and makes both sibling
  temp copies with their modes, and only then runs the two `mv`s back to
  back. A failure while staging (a full disk, an unwritable admin
  directory) leaves both old binaries in place, never a new
  `agent-director` beside an old `agent-director-admin`; the EXIT trap
  removes a staged copy that was never moved.
- **Other refusals, all exit 3:** the admin binary is missing (with neither
  binary beside the script, one combined refusal naming both `--binary` and
  `--admin-binary`); a `--from-release` tag before 0.11.0, refused at once
  without the CDN retry, because such a release has no admin asset
  ("release <tag> has no agent-director-admin binary"; the advice is a
  release of 0.11.0 or later).

**The operator's umask (b.7j2).** install.sh runs `umask u=rwx` right
after `set -euo pipefail`. That clears the owner's bits from the umask
and keeps its group and other bits, so the umask never takes away the
owner's own permission bits on a file or directory install.sh creates,
or that an `agent-director` it runs creates in `~/.agent-director/`
(each run inherits the umask). A umask like 0777 therefore cannot make
mktemp's sqlite3 error file or the `--from-release` downloads
unwritable. The group and other bits follow the operator's umask, and
an ordinary umask (022, 077, 027, 002) is unchanged. The umask
guarantees only the owner's access; the exact modes install.sh gives
(the two binaries, `~/.agent-director/`, `admin/`, `state.db`, the
sentinel; see [On-disk shape](#on-disk-shape)) come from explicit
`chmod`s.

#### Schema migration at install-time

Because the store refuses to auto-migrate under an agent (see the
schema-versioning section above), a bare warm-up would fail every
upgrade whose binary is newer than an existing `state.db`. `install.sh`
runs on the end-user's machine as an *administrator* action, so it is
the one legitimate place to authorize that migration — which it does
with a one-shot `migrate-authorized` sentinel: it reads the DB's ACTUAL
`user_version` (via `sqlite3 -batch -init /dev/null -cmd ".timeout 10000" … "PRAGMA user_version"`,
through the WAL and with the store's own 10 s busy timeout, so a running
agent-director's momentary lock delays the read instead of failing it;
`-init /dev/null` keeps the operator's `~/.sqliterc` from changing the
output, and `-batch` keeps sqlite3 from announcing that init file on a
terminal — hence `sqlite3` is a preflight requirement), writes a
`{"from":<actual>,"to":<target>}` sentinel beside `state.db` (skipped
when already current or on a fresh install), opens the store once with a
store-opening verb (`agent-director list`, deliberately not the DB-free
`help`/`version`) to run the migration and consume the sentinel, then
verifies the post-open `user_version` and aborts loudly (exit 5) on any
mismatch. That check runs only after the open succeeded, which leaves
`state.db` at the binary's version with any authorized migration run
and its sentinel consumed; a readable mismatch therefore means
`state.db` changed after the open or the read is wrong (b.wt9). A
fresh install is one with no `state.db` on disk, never one whose
version read failed. Every version read must print a whole number
(0 or more) before it reaches the sentinel's `printf %d` or the version
compare. A read that fails exits 5 as `<unreadable>`, showing sqlite3's
error, and re-running the install retries the read. That error is kept
in a mktemp file; if mktemp cannot create it (a full TMPDIR, say), both
reads still run and a failed one is reported without it. A read that prints
anything else also exits 5 as `<unreadable>`, showing that output and
naming the sqlite3 on PATH; a re-run gets the same output unless that
sqlite3 or state.db changes. Either kind at the first read stops the
install before any sentinel is written or the store is opened; at the
post-open read it stops the install whether or not a migration was
expected. The sentinel is written to a temp file and moved into place;
the EXIT trap removes a temp file that was never moved. A brief
hook-failure window between the binary swap and that open is accepted,
not worked around. That same open gives the store its store id: the
v4→v5 hop creates it on an upgrade, and `createSchema` on a fresh
install. `install.sh` never reads or writes `store_meta`.

**The probe, and a refused config (b.7b4).** On an existing `state.db`,
the install learns whether a migration is pending from one probe open
(`agent-director list`, before any sentinel is written) and decides by
the `err_name` of the probe's stderr envelope (`ad_err_name`, via jq),
never by its description text:

| Probe outcome | Install |
|---|---|
| opens, or `ErrSchemaMismatch` | no sentinel; `no migration authorization needed` (an `ErrSchemaMismatch` fails step 4's open again, with the store advice) |
| `ErrSchemaMigrationRequired` | sentinel written, `to` read from the refusal's `requires v<N>` (`ad_target_version`) |
| `ErrConfigMalformed` | stops, exit 5, config advice |
| any other error, or no envelope (`<no err_name>`) | no sentinel; `could not tell whether a migration is needed (agent-director list failed: <err_name>)`; step 4's open reports the failure if it persists |

The binary loads its config before it opens the store (`pkg/api`'s
`New`, `internal/clisetup`'s `Open`), so `ErrConfigMalformed`, at the probe
or at step 4's open, says nothing about `state.db`. Both stop the install
with exit 5 and the config advice (`ad_fail_config_refused`: the config
path, the envelope, "fix what the error above names in the config file,
then re-run this install"), never the store advice, having written no
sentinel and, on a fresh install, created no `state.db`. A re-run after
the fix probes again and authorizes any pending migration. **Must use:**
an `install.sh` decision on a verb's failure branches on `ad_err_name`,
never on the description's text.

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
├── admin/                         (mode 0700; never on PATH)
│   ├── agent-director-admin       (the operator tool; mode 0755)
│   └── agent-director-admin.prior (optional rollback snapshot; --keep-prior)
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

1. Write each new binary to a sibling temp file next to its target
   (`agent-director.tmp.$$`, and `agent-director-admin.tmp.$$` in the
   admin directory, which is created first) and `chmod 0755` it.
2. Only once both are staged, `mv` each onto its target, back to back.

A failure while staging replaces neither binary, so an install never
leaves a new `agent-director` beside an old `agent-director-admin`; the
EXIT trap removes a temp copy that was never moved.

Optional `--keep-prior` snapshots each existing binary to its `.prior`
(`agent-director.prior`, `agent-director-admin.prior`) before either is
staged, giving a one-step rollback of the matching pair
(`mv .prior canonical` for each). Whether to snapshot is decided once
for the pair, never per binary. A re-install of the same pair
(agent-director byte-identical to its source by `cmp -s`,
agent-director-admin byte-identical too or not installed; a `cmp` that
cannot compare counts as different) is not snapshotted: the `.prior`
files are kept, and the `prior` and `admin prior` lines each say
`kept <path>` or `none`, followed by `(not snapshotted: ...)`. Any
other install snapshots both, even when only one of them changed. An
upgrade from a release before 0.11.0 has no admin binary to snapshot:
when it replaces `agent-director`, `install.sh` removes any stale
`agent-director-admin.prior` (it would pair wrongly), prints an
"admin prior: none" line ending "to roll back, remove <path>", and
rolling back means removing `agent-director-admin`; a re-run of that
upgrade keeps `agent-director.prior` and prints the same advice.
Without `--keep-prior`, rollback is a re-install of the previous tag
via `install.sh --from-release v<old>`, which refuses a tag before
0.11.0 (no `agent-director-admin` asset). The
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
`agent-director-admin` with its `.prior` and the `admin/` directory (left
in place, with a note, when it holds other files),
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
facade. `LiveDispatcher` holds a single `*pkg/api.Client`; once
`checkParamNames` has accepted the argument names, each tool `case` in
`LiveDispatcher.Call` decodes the MCP JSON args (`decodeParams`) and calls
`client.X(…)`.
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
dispatcher, with `Options.Logger: nil`, so the verbs that still write log
lines (Spawn and Resume with their WARN lines; Expire with one line for a
failed candidate read, which fails the verb, and one per per-row delete
error, the run continuing after each of those; FindMissing with
one line per per-row store error and per transcript-heal error, the sweep
continuing after each) stay silent on the MCP path; those lines are most
useful to the interactive CLI operator, not a
long-lived MCP client. `kill` has no logger path at all (SR-6.3): it
writes no log line on any surface, its errors reach the MCP caller in the
error envelope, and its audit is the `ad.kill.called` trail event. The
CLI's main Client (constructed in
`setupClient`, through `internal/clisetup.Open`) and the MCP's Client have
distinct logger ownership; neither
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

The operator tool's verbs (`kill-finished`, `delete`) are not filtered
here: they are not manifest verbs, so neither the CLI nor `tools/list` has
them (a `delete` call is an unknown tool). They exist only on
`agent-director-admin` (see
[Operator tool `agent-director-admin`](#operator-tool-agent-director-admin)).

### Name mapping

MCP tool names use underscores (`send_keys`); manifest verb names
use hyphens (`send-keys`). `ToolName` + `VerbNameFromTool` are
symmetric inverses, pinned by `TestToolNameMapping`. The mapping is
faithful because no current verb name carries an underscore — if
that changes, the round-trip test will catch it.

### Parameter names and unknown arguments

A tool's arguments are its verb's manifest param names, used exactly:
underscores only (`claude_instance_id`, `relay_mode`, `extra_env`,
`no_pre_trust`, `tmux_session_name`, `reuse_finished`), never the CLI's
dashed flag spellings and with no aliases. `buildInputSchema`
(`internal/mcp/schema.go`) builds each tool's `inputSchema` from the
manifest with `additionalProperties: false`, so `tools/list` states what
the server enforces. `TestMCPParamToolsListNames` checks that no property
name has a dash and that every tool sets `additionalProperties: false`.

**Unknown arguments are refused.** Before any decode,
`LiveDispatcher.Call` runs `checkParamNames` (`internal/mcp/dispatch.go`)
for every exposed tool. It compares each top-level key of `arguments` with
the verb's manifest `Params`, exactly, case included (Go's decoder on its
own would match a key case-insensitively and ignore an unknown one). Any
other key, such as an old dashed name or a param of another surface, fails
the call with `ErrInvalidFlags`, the CLI's error for an undefined flag,
and nothing runs: no store read or write, no tmux call, no trail line. The
description names each unknown key quoted as sent, sorted, and lists the
verb's params in manifest order:

```
ErrInvalidFlags: spawn: unknown parameter "reuse-finished"; valid parameters: cwd, template, claude_instance_id, …, reuse_finished
```

Several unknown keys read "unknown parameters"; a tool with no params
lists "valid parameters: none". `arguments` that is not a JSON object (an
array, a string, a number or a boolean) is refused with `ErrInvalidFlags`
too, on every tool, and nothing runs:

```
ErrInvalidFlags: spawn: arguments must be a JSON object
```

`arguments` that is absent or JSON `null` is no arguments. No error name is
added: `spawn` stays the only MCP tool whose `ErrorNames` lists
`ErrInvalidFlags` (see the `ErrInvalidFlags` exclusion under
[Err-name five-way coherence](#err-name-five-way-coherence)).
`TestMCPParamUnknownRefused` and `TestMCPParamUnknownRefusalText`
(`internal/mcp/param_test.go`) pin both refusals.

**Every param is decoded.** Each tool's `case` decodes its arguments with
`decodeParams` (`internal/mcp/dispatch.go`) into a typed struct with one
field per manifest param, tagged with the manifest name. `spawn` decodes
`no_pre_trust` and `tmux_session_name` as the CLI does:
`tmux_session_name` is supplied when its key is present, even empty
(`ErrTmuxSessionNameEmpty`), and not supplied when absent or `null`.
`list` decodes `tmux_session_name` as its filter. Every exposed verb has a
`case`, `get-permission` included; `ErrUnknownTool` is only for a tool
name that is not exposed.

**A wrongly typed value is refused.** When the decode fails because a
param's value has the wrong JSON type (a wrong element inside an array or
object included), `decodeParams` refuses the call with `ErrInvalidFlags`,
the CLI's error for a bad flag value, and nothing runs. The description
names the verb (its manifest name, such as `make-template`, as the
unknown-parameter refusal does), the param as sent and the value it takes,
in the terms `tools/list` declares, never a Go type:

```
ErrInvalidFlags: list: parameter "limit" must be an integer
```

The value reads "a string", "a boolean", "an integer", "an array of
strings" or "an object with string values"; for a `duration` param
(`expire`'s `older_than`) it reads "a string holding a non-negative Go
duration like "12h" or trailing-d days like "7d" up to "106751d""
(`api.OlderThanForm`).
Two value checks after the decode refuse the same way: a `label` entry
with no `=` or an empty key on `spawn` and `make_template` (`labelMap`),
and an `older_than` on `expire` that `api.ParseOlderThan` rejects, which
is one in neither duration form, a negative Go duration such as `"-2h"`,
or a day count above 106751 such as `"365000d"` (`Expire` would take a
negative window, or the one such a count wraps to, as selecting every
finished row):

```
ErrInvalidFlags: spawn: parameter "label" entry "nokv" must be key=value
ErrInvalidFlags: expire: parameter "older_than" value "soon" must be a non-negative Go duration like "12h" or trailing-d days like "7d" up to "106751d"
ErrInvalidFlags: expire: parameter "older_than" value "-2h" must be a non-negative Go duration like "12h" or trailing-d days like "7d" up to "106751d"
ErrInvalidFlags: expire: parameter "older_than" value "365000d" must be a non-negative Go duration like "12h" or trailing-d days like "7d" up to "106751d"
```

Any other decode failure is agent-director's own fault and stays
`ErrInternal` (`decode <verb> params: …`). **Must use:** every tool `case`
decodes with `decodeParams` and parses `label` entries with `labelMap`,
never a bare `json.Unmarshal` or its own split, so a wrongly typed value
or a malformed label is `ErrInvalidFlags` in one wording on every tool.
`expire` parses `older_than` with `api.ParseOlderThan` (see
[`expire`](#expire)).
`TestMCPParamParity` sends, for every manifest param of every exposed
verb, values of the wrong shape alongside the verb's required params, and
requires exactly that refusal with nothing changed, so a param a `case`
does not decode, or decodes another way, fails the test.
`TestAdviceFollow_I3_InvalidLabelWantKeyValue` and
`TestAdviceFollow_I6_OlderThanDurationForm`
(`internal/mcp/advice_follow_mcp_test.go`) pin the `label` and
`older_than` refusals and follow them; the `older_than` one refuses, among
others, `"-2h"`, `"106752d"` and `"365000d"` with nothing deleted and no
tmux call, and its `"106751d"` follow keeps a row that `"12h"` and `"7d"`
delete. `TestExpireMCPOlderThanSign` (`internal/mcp/expire_test.go`) pins
that a negative `older_than` keeps a row that finished a minute ago, with
no tmux call, while `"0d"` and `"0s"` still delete it. The refusal of a
day count past an `int`'s range, such as `"9223372036854775808d"`, is
pinned only by `TestParseOlderThan` (`pkg/api/older_than_test.go`).

**Declared, listed and decoded shapes agree.** `goTypeToJSONSchema`
(`internal/mcp/schema.go`) turns each param's manifest `Type` into its
`tools/list` property schema: `string` and `duration` a string, `bool` a
boolean, `int` an integer, `[]string` an array of strings, and
`map[string]string` an object whose values are strings. A Type it does not
know gets no `type`, so `tools/list` would neither show nor check the
shape. Each `case` decodes a param into the Go type its manifest Type
names, so a call that sends the declared shape is never refused for its
shape; a value of another shape is refused with `ErrInvalidFlags` (above).
`expectedValue` (`internal/mcp/schema.go`) words each shape for that
refusal and is kept in step with `goTypeToJSONSchema`: a Type mapped in one
is mapped in the other. Spawn's
`label` and `claude_args` are arrays of strings and its `extra_env` is an
object mapping each variable name to its value
(`{"CLAUDE_CONFIG_DIR": "/cfg"}`), the same shapes as `make_template`'s;
the CLI spells the same values as repeated `--label k=v` and
`--extra-env K=V` flags and the arguments after `--`.
`TestMCPParamTypesAgree` (`internal/mcp/param_type_test.go`) checks every
param of every exposed tool: its manifest Type has a JSON Schema shape,
`tools/list` gives it that shape, and the decode accepts a value of it.
`TestMCPParamParity` is the wrong-shape half.

**Older `serve` processes.** A `serve` process from before 0.11.0, or
0.11.0-rc.1 (which reports 0.11.0, so the version check cannot tell it
apart), knows spawn's `relay-mode` and `extra-env` (and, in
0.11.0-rc.1, `reuse-finished`) only by those dashed names and silently
ignores their underscore names, and decodes `no_pre_trust` and
`tmux_session_name` under neither spelling, on spawn or list; it also
ignores every other unknown argument. Each affected param's manifest text
ends with a sentence saying so (see the param names under
[`pkg/api/manifest` — Verb Registry](#pkgapimanifest--verb-registry)).
Such a `serve` also has no `get-permission` case, so every MCP
`get_permission` call to it returns `ErrUnknownTool`.
A long-lived `serve` keeps its version until it restarts.

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
manifest description text instead. The unusable-recorded-name trigger
(SR-1.7) is stated in full only in `kill`'s Description; `read-pane`,
`send-keys` and `pause` carry the shared pointer `unusableNamePointer`
("Unusable recorded name: ErrInternal (see kill).", in
`pkg/api/manifest/manifest.go`), and `resume` and `spawn` merge "recorded
name is unusable (see kill)" into their own `ErrInternal` sentences, so
`help` stays under its size cap. A new verb that adds this trigger uses
the pointer, never a second full statement.

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
- `ErrInvalidFlags` has three sources. First, CLI flag parsing emits it for every verb: the
  `cmd/agent-director` flag handlers write it as a string literal in the error envelope.
  Second, the MCP server returns it for every tool whose `arguments` carry a key that is
  not one of the verb's manifest params or are not a JSON object (`checkParamNames`), or
  carry a param value of the wrong JSON type (`decodeParams`), and for a `label` entry
  that is not key=value (`spawn`, `make_template`) or an `older_than` that is in
  neither duration form, is negative or is a day count above 106751 (`expire`); see
  [Parameter names and unknown arguments](#parameter-names-and-unknown-arguments).
  Third, the shared verb layer returns it for `spawn` only, from the explicit-id check in
  `runSpawn` (see [Explicit-id check](#explicit-id-check)). It is in the Catalog. It is
  listed in `spawn`'s manifest `ErrorNames` and its Go "Errors:" list, because spawn is the
  only verb whose shared verb layer emits it. No other callable verb lists it, because the
  CLI flag-parse and MCP argument emissions are the surfaces' checks of the caller's
  arguments, which every verb gets, not emissions of a verb's shared layer. It stays in `check3Exceptions` in
  `pkg/api/errnames/coherence_diff_test.go`, next to `ErrInternal`; that list feeds the
  (b) ⊆ (c) check ("Check 3" in that file). Because `spawn` lists it, that check passes
  without the exception; the exception stays (SR-1.7). `TestDiffExclusionErrInvalidFlags` proves that the exception alone keeps that check quiet.
- Catalog entries whose sentinels are declared in `internal/*` packages and not
  re-exported from `pkg/api` (e.g. `spawn.ErrCwdMissing`, `config.ErrTemplateNotFound`,
  `store.ErrAmbiguousRequest`) do not appear in `exportedSentinels` and are therefore
  excluded from check 2. Their coherence with the Catalog is enforced at compile time —
  `catalog.go` imports and references them directly. The seven tmux sentinels
  (`tmux.ErrTmuxNotAvailable`, `ErrTmuxSessionCreate`, `ErrTmuxUnresponsive`,
  `ErrTmuxSessionConflict`, `ErrTmuxKillFailed`, `ErrTmuxSendKeys`,
  `ErrTmuxCaptureFailed`) and several `store` sentinels (e.g.
  `store.ErrSpawnNotFound`) are declared in `internal/*` but re-exported from `pkg/api`
  (`aliases.go`), so they do appear in `exportedSentinels`. The Catalog holds 42 names.
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
   [envelope-diff harness](#envelope-diff-regression-harness) for the full non-determinism model.
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

`serve`'s manifest Description does not carry this command, so `help`
and the generated CLI and MCP references do not show it. It is stated
here, in the README's install section (beside the installer's
`--register-mcp` flag, for a human registering by hand), in the install
skill (`skills/install-agent-director/SKILL.md`) and in `serve`'s own
stderr hint (`cmd/agent-director/serve_cmd.go`).

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
  write the envelope. Always emits an envelope before returning, except
  when the gated INSERT does not apply (see "The hook gate on the relay
  path" below).

- **`internal/hook/handler.go`** — branches into `runRelay` when the
  event is `PermissionRequest` AND `AGENT_DIRECTOR_RELAY_MODE=on` AND the
  hook's gated state write applied.
  Pre-relay failure paths emit a deny envelope ONLY when the event is
  `PermissionRequest` AND relay is active (SRD §6.4 + b.45p). The
  handler peeks the event name from the raw payload via
  `PeekEventName` before resolving the instance id so the gate has
  honest information from the first failure point. Non-permission
  events (PreToolUse, etc.) stay fail-open even on internal failures.

- **`internal/store/permission.go`** — store primitives:
  - `UpsertOpenPermissionRequest`: INSERT-only per `(instanceID, requestToken)`,
    gated like every hook write: it takes the hook's `store.HookGate`, the
    INSERT is `INSERT … SELECT … WHERE EXISTS (SELECT 1 FROM spawns WHERE
    … AND <gate>)`, and it reports `store.HookApplied`. A second call with
    the same pair returns `ErrRequestTokenCollision`; the first row is
    unmodified.
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

With `HOME` unset or empty, `store.OpenOrInit` refuses the hook's `~/`
store path (the config's default `~/.agent-director/state.db`, left
unexpanded; see "No usable home" under [internal/store](#internalstore),
b.4uz), so the hook takes this path: it opens and creates no store
anywhere, logs one `hook: open store: …` line, exits 0, and writes the
deny envelope only for a PermissionRequest with relay on; every other
event leaves stdout empty.
The line normally goes to stderr: the default `errors.log` path stays
unexpanded too, so the hook logger cannot open it.

### The hook gate on the relay path

(SR-22.9.) A relayed PermissionRequest is gated
like every hook (see [Hooks move a row only for its own
agent](#hooks-move-a-row-only-for-its-own-agent)), and a gate that does
not hold is not a failure: it gets no deny envelope.

- **Ignored at the transition.** When the hook's `check_permission`
  write does not apply, `Handle` never enters `runRelay`: no permission
  request is recorded, nothing is written to stdout (no decision, so the
  process's own Claude Code asks as it would with no relay), the hook
  exits 0, and it writes one `ad.hook.ignored`.
- **Ignored at the INSERT.** The request INSERT carries the same gate in
  its own statement. If the transition applied but the INSERT does not
  (the row stopped being this process's in between), `runRelay` records no
  request, writes nothing to stdout, and calls `onIgnored` once, so
  `Handle` writes the hook's one `ad.hook.ignored` with the store's
  reason. A row gone in between gives no reason and no `ad.hook.ignored`.
- **The timeout gate.** On a polling timeout, the timeout path's
  `working` write (`ApplyHookTransition` with trigger
  `PermissionRequestTimeout`) reuses the gate `Handle` captured at entry;
  it takes no second reading of the parent. If that write does not apply,
  it is logged only, with no second `ad.hook.ignored`. The timeout's
  decision write (`DecidePermissionRequest`) and the deny envelope are
  not gated and happen as before.

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
OR `permission_requests.decision` is non-NULL for the corresponding row. An
agent must never be sitting in "waiting for permission" with no live listener AND
no decision *and no sanctioned way out*: if a relay listener is gone and every
row is undeliverable, an external surface (for example a caller's approval
prompt) would be a lying ghost — buttons that go nowhere.

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

- **Normal flow**: the agent's hook fires → state=`check_permission`
  (gated) → `runRelay` mints a `request_token` →
  `UpsertOpenPermissionRequest(instanceID, gate, requestToken, …)` (gated)
  → `Poll(…, instanceID, requestToken, …)` runs →
  either `decide()` is called targeting the same `(instanceID, requestToken)`
  pair (decision set) or the loop times out and writes
  `decision='deny'`, `decision_reason='timeout'` to that specific row, then
  transitions state back to `working`.
- **Process death**: the `find-missing` reconciler first marks the Spawn
  `missing` (the guarded `MarkMissingIfSameLife`, then its tick; see the
  pinned marking order in
  [Degraded-mode reconciliation + cron user](#degraded-mode-reconciliation--cron-user)).
  Only when that mark applied does `CloseOrphanedPermissionRequests` read
  the open rows (`decision IS NULL`) through
  `OpenPermissionRequestsForSpawn` and write `decision='deny'`,
  `decision_reason='find_missing'` to each, which fail-closes the relay's
  polling loop.

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
transactional — there is no background sweep. A call whose gated INSERT
did not apply (SR-22.9) rolls back, inserting and evicting nothing.

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
`protocol_version` field on the wire. Every caller that consumes
`decision_reason` values must fail closed on unknown values — treat any
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

**No usable home.** The trail is only ever written at an absolute path under
the home directory. When none can be determined — `os.UserHomeDir` fails
(on Unix, `HOME` unset or empty) or returns a path that is not absolute —
the trail has no path and nothing is written anywhere (`resolvePath` in
`internal/trail/trail.go`). There is no fallback: not to the working
directory, and not to another home source such as the passwd entry. The
process-singleton writer resolves its path once, when the process first uses
it (`Emit` or `SetLogger`), so a writer that found no home stays pathless for
the rest of the process even if `HOME` is set later. `trail.Path()` returns
`""` in this case, never a relative path. What each `Emit` does then is
under [Fail-soft semantics](#fail-soft-semantics). With `HOME` set to an
absolute path the location is unchanged.

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
invocation resolves, on every installation. An invocation that resolves no
usable home writes no trail at all (see [Location](#location)): its events
are not recorded anywhere, and in particular never in a `.agent-director/`
under its working directory.

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

The table lists nineteen event strings. The first eighteen are the primary
event families; the last is a self-reporting meta event. The store's
session-history events (`ad.session.archived`, `ad.session.archive_failed`,
`ad.session.jsonl_healed`) and schema-migration events (`ad.schema.*`)
are also emitted but are not listed here.

| Event | Source | Description |
|-------|--------|-------------|
| `ad.hook.fired` | `ad_hook` | One per `agent-director hook` invocation — records the hook payload and caller identity (SR-A-2.1, Epic 1) |
| `ad.hook.ignored` | `ad_hook` | Exactly one per hook SR-22.9 did not apply (see [Hooks move a row only for its own agent](#hooks-move-a-row-only-for-its-own-agent)), fail-open: a trail-write failure changes nothing and the hook still exits 0. Emitted by `emitIgnored` (`internal/hook/gate.go`) on the hook path and by `emitNoExecForm` (`internal/hook/noexec.go`, called from `cmd/agent-director`'s no-verb run through `hook.HandleNoExecForm`); both build the fields with `ignoredFields`. Carries `claude_instance_id`, `hook_event`, `reason` (one of four: `pid_mismatch`: the hook's parent process, with its start time, is not the row's recorded pane process; `no_pane_recorded`: the row records no pane, for a SessionStart only after its bounded wait for the launch's identity write; `subagent_event`: a SessionStart or SessionEnd whose payload carries a non-empty `agent_id`, decided before any write; `no_exec_form`: a no-verb run given a hook payload on stdin, from a Claude Code that does not run exec-form hooks, written with no store access, so `row_session_id` and `row_pane_pid` are always null and `claude_instance_id` is null when the environment has none or an invalid one), `parent_pid`, `parent_command` (the parent's command name from `probe.CommandNameReader`, read only for this record; null when unreadable), `hook_session_id` (null when the payload gives none), `row_session_id` and `row_pane_pid` (from one read of the row; null when the row records none or the read fails), `launcher_pid` (the row's `pane_pid` when the hook was refused with `pid_mismatch` and the parent's own parent, read once through `probe.ParentPIDReader`, is the row's pane process, for any event and any row state; null otherwise, and always null for the other three reasons; b.9n6). On the hook path, not written for a hook whose id has no row (`subagent_event` included), or for a SessionStart that lost to a changed row twice; `no_exec_form` reads no row and is always written. No `ad.hook.fired` accompanies a `no_exec_form` record. Never another row's id or any session-environment content (SR-14, SR-15) |
| `ad.hook.launcher_detected` | `ad_hook` | The launcher warning (b.9n6; see "Launcher warning" in [Hooks move a row only for its own agent](#hooks-move-a-row-only-for-its-own-agent)): one per SessionStart refused with `pid_mismatch` whose `ad.hook.ignored` carries `launcher_pid` while the row is `pending`, written by `emitLauncherDetected` (`internal/hook/gate.go`) right after that record. Not once-only: each such SessionStart (startup, `/clear`, compaction, resume) writes one. Carries `claude_instance_id`, `launcher_pid` (the row's pane process, the parent's own parent), `launcher_command` (its command name from `probe.CommandNameReader`; null when unreadable), `parent_pid` and `parent_command` (the `ad.hook.ignored` record's values), `advice` and `source`. The event name is the contract; `advice` is supplementary English naming both causes the check cannot tell apart (a `claude` launcher that runs Claude Code as a child instead of exec-ing it, or a hook run through a shell instead of in exec form) and never advises ending a session by hand. It does not change the gate's decision, the row or the hook's exit; fail-open: a trail-write failure changes nothing. Never another row's id or any session-environment content (SR-14, SR-15) |
| `ad.spawn.state_transition` | `ad_spawn_store` | One per applied hook write (`ApplyHookTransition`, `ApplyHookWaitingIfWorking`, `RecordSessionStartIdentity`), including same-state writes, soft-refresh ticks and the gated `working` hold; a hook the gate did not apply emits none. SessionStart on a resumed row records `prior_state` `pending`. Hook-driven writes are the only ones that emit it: a spawn's insert and `find-missing`'s mark never did, `resume`'s move and restore do not (their own `ad.resume.*` events record them), and reuse's reset and restore do not (`ad.spawn.reused` and `ad.spawn.reuse_restored` record them) (SR-A-2.2, SR-14) |
| `ad.row_mutation.committed` | `ad_store` | One per successful write to `permission_requests` (SR-A-2.6, Epic 3) |
| `ad.decide.called` | `ad_decide` | One per `agent-director decide` invocation on every return path, carrying an `outcome` field set to the canonical err_name (or `ok`). Recognized failure outcomes include the no-op refusals `ErrAlreadyDecided` and `ErrRelayFallenBack` (a fallen-back refusal is a recognized outcome, not `ErrInternal`) (SR-A-2.4, Epic 4) |
| `ad.find_missing.tick` | `ad_find_missing` | Written by find-missing only for a guarded write that applied (see [Degraded-mode reconciliation + cron user](#degraded-mode-reconciliation--cron-user)); fail-open. Every tick carries `claude_instance_id`, `prior_state`, `new_state`, `reconciliation_reason` and `source`. **Mark ticks** (`markMissingSameLife`, `pkg/api/find_missing_writes.go`): exactly one per applied mark, `prior_state` the row's state before the mark and `new_state` `missing`, with `reconciliation_reason` one of `proc_absent` (the agent process, from the SessionStart identity or the pane identity, recorded or adopted, is gone; no extra field), `tmux_absent` (the lookup found no session of the row's current launch and no session holds the recorded name, or an Ours row that records no pane has no pane carrying its launch token; plus `lookup_outcome`, the lookup's outcome token: `gone`, `leftover` or `ours`) or `tmux_name_held` (as `tmux_absent`, but a session holds the recorded name; plus `lookup_outcome` and `tmux_session_name`, the recorded name). **Close-out ticks** (`CloseOrphanedPermissionRequests`, `internal/store/recovery.go`): one `permission_orphan_closeout` per open permission request denied after an applied mark, carrying `request_token`, `prior_state` and `new_state` null. **Note ticks** (`writeLivenessNote`): `prior_state` and `new_state` null, `reconciliation_reason` the note (`probe_eacces`, `process_not_seen_session_present`, `process_not_seen_tmux_unchecked`, `tmux_server_changed`, `provenance_conflict`, or one of the unusable-recorded-name notes `tmux_session_name_empty`, `tmux_session_name_control_char` and `tmux_session_name_rewritten`, which carry no extra field), at most one per applied note write, and only when the row goes from no note to a note or enters `provenance_conflict` from no note or another note. No tick for a clear, for a note equal to the one the sweep read, for any other change of note, or for a write that found the row changed or absent or failed in the store (SR-11.4, SR-11.6, SR-14). A mark is the sweep's judgement on the evidence available to it, not proof that the agent has exited (SR-18.2). There is no global-refusal tick. (SR-A-2.5) |
| `ad.relay_attempt.completed` | `relay_hook` | One per worker permission-relay attempt (SR-A-2.3, Epic 6) |
| `ad.resume.observed` | `ad_polling` | One per hook-resume back to Claude Code (SR-A-2.7, Epic 7) |
| `ad.resume.moved_to_pending` | `ad_resume` | Once per applied move to `pending` by the `resume` verb, emitted when the create that directly follows the move returns (no I/O may run between the move and the create, SR-8.3); fail-open. Carries `claude_instance_id`, `prior_state` (`ended` or `missing`) and `claude_session_id` (the session id the row keeps). A `resume` refused before or at its move emits none. Emitted in `pkg/api/resume.go`, so every surface gets it. Unrelated to `ad.resume.observed`, a permission relay's hook resume (SR-8.3, SR-14) |
| `ad.resume.restored` | `ad_resume` | Once per restore attempt after a failed `resume` launch (a failure other than a timeout), applied or not; fail-open. Carries `claude_instance_id`, `applied` (boolean), `launch_error` (the err_name of the launch error `resume` returns: after "duplicate session", the name of the classified error the re-lookup gave, such as `ErrTmuxSessionConflict`, and `ErrTmuxSessionCreate` only when the holder vanished) and `restore_error` (null, or the store error's text when the restore's write failed). After "duplicate session" it precedes the call's `ad.provenance.disagree` and `ad.launch.name_held` records. Emitted by `finishedLaunch.restore` in `pkg/api/finished_launch.go` (also on the path after "duplicate session", `finishedLaunch.heldName`) with resume's values (`resumeLaunchVerb`), so a failed restore behind the MCP server, whose client has no logger, is still recorded. Unrelated to `ad.resume.observed` (SR-8.5, SR-14) |
| `ad.spawn.reused` | `ad_spawn` | Exactly once per applied reuse change (`spawn` with the reuse opt-in whose `ResetForReuse` applied), fail-open: a trail-write failure never changes the spawn's result. Written after the change commits and after the create call that follows it returns, before any restore: on success, on a launch timeout and on every launch failure after the reset. Carries `claude_instance_id`, `prior_state` (the state examined before the reset, `ended` or `missing`), `archived_session_id` (the session id the reset archived, null when the row had none), `lookup_outcome` (the old-row lookup's outcome token as the decision kept it; only a Gone lookup proceeds, so `gone`) and `source`. A reuse refused before or at its change (a lost race, a store error) emits none. Emitted by `emitReused` in `pkg/api/spawn_reuse.go`, on the `pkg/api` spawn path, so every surface gets it. Not an `ad.spawn.state_transition`: the reset emits none (SR-10.6, SR-14) |
| `ad.spawn.reuse_restored` | `ad_spawn` | Exactly once per restore attempt after a failed reuse launch (a failure other than a timeout, "duplicate session" included), applied or not; fail-open. Carries `claude_instance_id`, `applied` (boolean: true only when the restore applied), `launch_error` (the err_name of the error the spawn returns, through `errorName`; after "duplicate session", the classified error the re-lookup gave) and `restore_error` (null, or the store error's text when the restore's write failed), and `source`. Written after `ad.spawn.reused`; after "duplicate session" it precedes the call's `ad.provenance.disagree` records and its `ad.launch.name_held`. Emitted by `finishedLaunch.restore` (`pkg/api/finished_launch.go`) with reuse's values (`reuseLaunchVerb`), on the `pkg/api` spawn path, so every surface gets it. Not an `ad.spawn.state_transition`: the restore emits none (SR-10.4, SR-10.6, SR-14) |
| `ad.send_keys.called` | `ad_send_keys` | One per `Client.SendKeys` call past the closed check (every `agent-director send-keys` invocation), on every return path, fail-open (mirroring `ad.decide.called`); emitted by `emitSendKeysCalled` (`pkg/api/sendkeys_trail.go`). The exported `SendKeys` writes none. Carries `claude_instance_id`; `allow_pending`; `row_state` (the stored state of the row the verb read, `""` when no row was read: it tells keys typed into a launching agent's startup prompt, a `pending` row, from keys sent to a live conversation, and records the state a refusal met); `outcome` (`ok` or the err_name from `errorName`: `ErrTmuxUnresponsive`, `ErrTmuxSessionConflict`, `ErrTmuxNotAvailable`, `ErrTmuxSendKeys`, `ErrSpawnNotInteractive` (its `pending`-row triggers included), `ErrSendKeysWhileRelayed` and `ErrSpawnNotFound` by name; `ErrInternal` only for an error none of those matches, such as a relay-guard store error); the AD-collected `caller_*` identity (collected once per call); and a `guard_evaluation` field — `not-applicable` (relay guard did not apply), `held` (refused, relay could still act), `released` (guard released, the audited recovery of a fallen-back relay) or `error` (the guard's store read failed) — so recovery sends are distinguishable from ordinary sends and refusals (SR-5.2, SR-7.4). Never the typed text. `pause` and `read-pane` write no call event |
| `ad.launch.name_held` | `ad_spawn`; `ad_resume`; `ad_find_missing` | Written by the one emitter `emitNameHeld` (`pkg/api/name_held_trail.go`), fail-open: a trail-write failure never changes the verb's result, error or description. Exactly one per plain-spawn label-scan refusal ("left over from an earlier life"), exactly one per plain spawn whose create answered "duplicate session" (the held-name path; see [Launch identity](#launch-identity)), exactly one per `resume` whose create answered "duplicate session" (see [After "duplicate session"](#after-duplicate-session)), and exactly one per reuse whose create answered "duplicate session" (see [Reuse of a finished id](#reuse-of-a-finished-id)). Carries `source`, `claude_instance_id`, `launch` (`spawn`, `resume` or `reuse`), `tmux_session_name` (the requested name; for the scan, the first leftover's; for `resume`, the recorded name), `tmux_socket`, `tmux_session_id` and `session_created` (the blocking session; for the scan, the leftover with the lowest `$N`), `store_id` (this store's `store_meta.store_id`, never a label's, for comparison with a label's last field), `carries_this_id` and `current_launch` (from the holder's label class only, never the environment: true and false for an old label; true and true for a current label, which only the re-lookup of `resume` or reuse can meet (the row's own session); false and null for another id's, another store's or no valid label, another store's counting as not carrying the id even when it names it; both null when no single holder was identified or its class cannot be trusted), `lookup_outcome` (the lookup's outcome token), `outcome` (the returned error's name), `row_result` (`not_inserted` for the scan; `ended`, `left_changed` or `still_pending` after a plain spawn's "duplicate session"; `restored`, `left_changed` or `still_pending` after a `resume`'s or a reuse's, as its restore went), `store_error` (the end write's or the restore's store error text, else null), the by-hand `attach_command` (`tmux -u -S '<socket>' attach-session -r -t '<$N>'`) and `end_command` (`tmux -u -S '<socket>' kill-session -t '<$N>'`), and the `caller_*` identity; `leftover_count` only on the scan's record. `tmux_session_id`, `session_created` and both commands are present whenever one blocking session was identified, a holder with no valid label included, and null otherwise (vanished, ambiguous, unreadable, tmux unavailable). The trail carries the two commands because humans read it; no error description carries them. Later sources and launch kinds (another launch's `launch` value, the sweep's null `launch` and `outcome`) add their constants beside the existing ones without changing the field set. **resume** (source `ad_resume`, `launch` `resume`, written by `finishedLaunch.heldName` in `pkg/api/finished_launch.go`) writes exactly one per call whose create answered "duplicate session", after its re-lookup, its restore attempt and `ad.resume.restored`, whatever the outcome: the holder fields from the re-lookup, `tmux_socket` the launch socket, `lookup_outcome` the re-lookup's token, `outcome` the returned error's name, `row_result` the restore's (`restored`: the restore applied; `left_changed`: the row changed or was removed after the move; `still_pending`: the restore failed in the store, with `store_error`); no `leftover_count`. A refusal at `resume`'s pre-launch lookup writes none. **reuse** (source `ad_spawn`, `launch` `reuse`, written by the same `finishedLaunch.heldName`) writes exactly one per call whose create answered "duplicate session", after `ad.spawn.reused`, its re-lookup, its restore attempt and `ad.spawn.reuse_restored`, in that order, whatever the outcome, with the same fields as `resume`'s: `tmux_session_name` the requested name, `row_result` the restore's (`left_changed`: the row changed or was removed after the reset). A refusal before the reset (the old-row lookup or the new-name pre-check) writes none. **find-missing** (source `ad_find_missing`, `emitRowNameHeld` in `pkg/api/find_missing_lookup.go`) writes exactly one per row per sweep whose mark attempt had tick reason `tmux_name_held`, whatever the mark's outcome, with the same field set: `launch` and `outcome` null; `tmux_session_name` the row's recorded name; `tmux_socket` the socket the lookup used; `store_id` this store's, as for every source; the holder fields from the row's lookup (`heldHolderFacts`), so `carries_this_id` false and `current_launch` null when the holder is another store's session, and the holder fields null when more than one listing entry matches the name; `lookup_outcome` the lookup's token (`gone` or `leftover`); `row_result` `marked_missing` (the mark applied), `left_changed` (the row was changed or absent) or `still_pending` (the mark failed in the store, with `store_error`); no `leftover_count`. The sweep never touches the holding session (see [`find-missing`](#find-missing)). Never a label value, the id a label names, another row's id or session-environment content (SR-9.3, SR-9.4, SR-14, SR-15) |
| `ad.kill.called` | `ad_kill` | Exactly one per kill call (`runKill` in `pkg/api/kill.go`, behind the exported `Kill` and `agent-director-admin kill-finished`), on every return path, so `Client.Kill`, direct callers and the admin verb all get it; fail-open (a trail-write failure never changes the result). A call on a closed `Client` returns `ErrClientClosed` before `Kill` runs and emits nothing. Emitted by `killRun.emit` (`pkg/api/kill_trail.go`). Carries `claude_instance_id`; `tmux_session_name` (the recorded name, empty when there is no row); `outcome` (`ok` or the err_name the CLI would print, from `errorName`; `ErrSpawnNotResumable` only for the operator-only opt-in on a live row); `lookup_outcome` (the lookup's outcome token, or `not_run` when no lookup ran: unknown id, a finished row without the operator-only opt-in, the operator-only opt-in on a live row, an unusable recorded name, an unusable socket directory); `followup_outcome` (the follow-up lookup's token, `not_run` when none ran); `kill_sent` (as in the result); `pane_killed` (whether a pane kill was sent); `process_check` (`gone`, `alive`, `unreadable` or `not_recorded` whenever a process check ran, on the Gone path or after a kill; after an expired wait `alive` when the agent still counted as running, else `gone` with survivors listed; `not_run` when none ran); `agent_pid` (the pid of the agent process that was checked, null when no check read one); `survivor_pids` (the pane processes of the labelled session still running when the wait ended, `[]` otherwise); `include_finished` (whether the operator-only finished-row opt-in was set: true only for `agent-director-admin kill-finished`, whose `caller_process` is `agent-director-admin`); and the AD-collected `caller_process`, `caller_pid`, `caller_hostname`, `caller_user`. A human uses `agent_pid` and `survivor_pids` in the README's "Operator actions" procedure. Never session-environment content or another row's id (SR-6.4, SR-14, SR-15) |
| `ad.provenance.disagree` | the verb that decided (`ad_kill`; `ad_send_keys` for both keys verbs, `send-keys` and `pause`, told apart by `verb`; `ad_spawn` for plain spawn's re-lookup after "duplicate session", reason `scope_value`, and for reuse's old-row lookup and its re-lookup after "duplicate session"; `ad_resume` for `resume`'s pre-launch lookup and its re-lookup after "duplicate session"; `ad_find_missing`; `ad_expire`) | Written only when a verb call meets a disagreement, never in the normal case: at most once per reason per verb call, or per reason per row per sweep; fail-open. Emitted through the shared `emitProvenanceDisagree` (`pkg/api/provenance_disagree.go`), which drops duplicates and unknown reasons. `reason` is one of six: `server_restarted`, `server_mismatch`, `adopted` (a lost create reply's identity adopted and written), `duplicate_label` (two sessions with the current label), `scope_value` (an `@ad_owner` value at the global, server or global-window scope) or `name_changed` (Ours found under a name other than the recorded one); `pid_mismatch` is retired. Also carries `claude_instance_id`, `verb`, `tmux_socket`, `tmux_session_name` (the recorded name), `tmux_session_id` (the session concerned, null when none), `current_session_name` (on `name_changed` only, null otherwise), `server` (`match`, `restarted`, `differs` or `unknown`), `verdict` (the lookup's outcome token), `action` (what the verb did; `kill` writes `kill_sent` or `nothing_sent`; `send-keys` and `pause` write `keys_sent` (the text and Enter both went through), `text_sent` (the text call timed out, or the text went through and the Enter call failed or timed out) or `nothing_sent` (no text call, `pause`'s failed `C-u` included, or the text call failed other than by timing out); plain spawn its held-name row result `ended`, `left_changed` or `still_pending`; `resume` and reuse `refused` or `proceeded` at their pre-launch lookup, and their restore's row result `restored`, `left_changed` or `still_pending` after "duplicate session") and the `caller_*` identity. **resume** (source `ad_resume`, `verb` `resume`, written by `emitFinishedRowDisagree` in `pkg/api/finished_launch.go`, over `emitLookupDisagree`) writes each distinct reason at most once per call, on a refusal too, never `adopted` (`resume` adopts nothing). Its pre-launch lookup's records (the lookup's reasons and `name_changed`) are written right after the decision, before any write, with `action` `refused` or `proceeded`. After "duplicate session" the re-lookup's records follow `ad.resume.restored`, with every reason the pre-launch lookup already wrote skipped and `action` the restore's row result; a `name_changed` record there names the row's own session, every other reason the name's holder (else the own session). `tmux_socket` is the lookup's socket and `tmux_session_name` the recorded name. **reuse** (source `ad_spawn`, `verb` `spawn`, written by the same `emitFinishedRowDisagree` with reuse's values) follows the same rules: each distinct reason at most once per call, never `adopted`; its old-row lookup's records right after the decision, before pre-trust and the reset, with `action` `refused` or `proceeded`; after "duplicate session" the re-lookup's records after `ad.spawn.reuse_restored`, skipping reasons already written, with `action` the restore's row result. `tmux_session_name` is the row's recorded (old) name, never the requested one, and `name_changed` compares against it. **send-keys and pause** (source `ad_send_keys`, `verb` `send-keys` or `pause`, written by the shared `keysRun.emitDisagree` in `pkg/api/pane_keys.go`) collect their reasons as `kill` does: the first lookup's, `name_changed`, a failed pane listing's, `adopted` only when the adoption write applied, and the follow-up lookup's; they write none when the call made no lookup. `pause` writes its records before its wait, so the wait's outcome neither adds nor removes one. `read-pane` writes none. **find-missing** (`verb` `find-missing`, written by `emitDisagree` in `pkg/api/find_missing.go` once the row's write has settled) writes one record per distinct reason per row per sweep, in the order above, and none for a row judged without a lookup (a row whose recorded session name cannot be used included) or a row whose lookup was not called. Its reasons are the lookup's own, `name_changed`, `adopted` (only when the adoption write applied) and those of an adoption pane listing that did not answer; each record carries the fields of the observation that produced its reason. A lookup reason carries the lookup's `server` and `verdict` and the lookup's session as `tmux_session_id` (the Ours session; null when none), with that session's stored name as `current_session_name` on `name_changed`. A reason only the listing reported (`server_mismatch`, on a no-server reply while the recorded server process is not gone) carries the listing's `server` (`differs`), its `verdict` (`different_server`) and `tmux_session_id` null. A reason both reported is written once, with the lookup's fields. `tmux_socket` is the socket the lookup used; `action` is the row's outcome: `marked_missing`, `left_live`, `left_unverified`, `left_changed` (a guarded write, adoption included, found the row changed or absent) or `store_error`. **expire** (source `ad_expire`, `verb` `expire`, written by `expireRun.emitRow` in `pkg/api/expire_trail.go` once the row's delete attempt has settled) writes one record per distinct reason per row per run, and none for a row with no reason, a row kept for an unusable recorded name, a `process_alive` row or a row whose lookup was Skipped. Its reasons are the lookup's own and `name_changed`; it never writes `adopted`, because `expire` never adopts. `tmux_socket` is the socket the lookup used, `tmux_session_id` and `current_session_name` come from the lookup's session, `server` and `verdict` from the lookup (`not_run` when none ran), and `action` is the row's outcome: `deleted`, the row's kept reason (such as `ours` or `leftover_running`), or `left_changed` (another caller removed the row first). The caller identity is collected at most once per run (`lazyCaller`). Never a label's content or another row's id (SR-14, SR-15) |
| `ad.expire.kept` | `ad_expire` | Exactly one per row `expire` kept, per run, written by `expireRun.emitRow` (`pkg/api/expire_trail.go`) once the row's outcome is final, `changed_since_examined` and `store_error` included; fail-open (a trail-write failure never changes the run's result). A deleted row and a row another caller removed first get none. Carries exactly `claude_instance_id`, `reason` (the kept reason: `empty_session_name`, `control_char_session_name`, `rewritten_session_name` (an unusable recorded name, checked before every other reason, with no tmux call), `process_alive`, `tmux_skipped`, `leftover_running`, `ours`, `tmux_server_changed`, `provenance_conflict`, `cant_tell`, `tmux_unavailable`, `changed_since_examined` or `store_error` as built; the list is not closed), `tmux_session_name` (the recorded name, JSON-encoded like every trail field, so a control character below 0x20 appears escaped (DEL as is) and a byte that is not valid UTF-8 appears as U+FFFD) and `source`. Never session-environment content, a label value or another row's id (SR-12.5, SR-14, SR-15) |
| `ad.trail_meta.emit_failed` | `ad_trail_meta` | Self-reporting envelope written when a primary emit fails — carries `original_event` and `error_class` (SR-A-3.2) |

### Sources

The `source` field identifies which emitter wrote the line:

| Value | Emitter |
|-------|---------|
| `ad_hook` | `internal/hook/handler.go` — the hook ingestion handler (`ad.hook.fired`); `internal/hook/gate.go` — `ad.hook.ignored` on the hook path (`emitIgnored`) and `ad.hook.launcher_detected` (`emitLauncherDetected`); `internal/hook/noexec.go` — `ad.hook.ignored` `no_exec_form` (`emitNoExecForm`), called from the no-verb run in `cmd/agent-director/noverb.go` |
| `ad_spawn_store` | `internal/store/hook_writes.go` — the gated hook writes (`ad.spawn.state_transition`, and SessionStart's `ad.session.archived` / `ad.session.archive_failed`); `internal/store/session_history.go` — `ad.session.jsonl_healed` |
| `ad_store` | `internal/store/permission.go` — permission-request row mutations |
| `ad_decide` | `pkg/api/decide.go` and `cmd/agent-director/spawn_cmd.go` — decide verb |
| `ad_find_missing` | The find-missing sweep: `pkg/api/find_missing_writes.go` — the mark and note ticks (`markMissingSameLife`, `writeLivenessNote`); `internal/store/recovery.go` — the `permission_orphan_closeout` ticks (`CloseOrphanedPermissionRequests`); `pkg/api/find_missing_lookup.go` — the `ad.launch.name_held` record through `emitNameHeld` (`emitRowNameHeld`); `pkg/api/find_missing.go` — the `ad.provenance.disagree` records through `pkg/api/provenance_disagree.go` (`emitDisagree`) |
| `relay_hook` | `internal/hook/permission.go` and `cmd/agent-director/trail_emit_cmd.go` — relay-attempt completion |
| `ad_polling` | `internal/hook/permission.go` — resume observed on hook return |
| `ad_send_keys` | `pkg/api/sendkeys_trail.go` — send-keys' `ad.send_keys.called` (`emitSendKeysCalled`, called from `Client.SendKeys` in `pkg/api/sendkeys.go`; per invocation, carries the relay-guard evaluation and `row_state`); `pkg/api/pane_keys.go` — the keys verbs' shared `ad.provenance.disagree` emit (`keysRun.emitDisagree`, through `pkg/api/provenance_disagree.go`), called by `pkg/api/sendkeys.go` (`verb` `send-keys`) and `pkg/api/pause.go` (`verb` `pause`; pause writes no call event) |
| `ad_spawn` | The spawn verb, one source constant (`nameHeldSourceSpawn`): `pkg/api/name_held_trail.go` — the one `ad.launch.name_held` emitter (`emitNameHeld`), called by the spawn verb's label scan (`pkg/api/spawn_scan.go`), by plain spawn's held-name path after "duplicate session" (`pkg/api/spawn_held.go`, which also writes its `ad.provenance.disagree` records through `pkg/api/provenance_disagree.go`) and by reuse's (`finishedLaunch.heldName`); `pkg/api/spawn_reuse.go` — reuse's `ad.spawn.reused` (`emitReused`) and its old-row lookup's `ad.provenance.disagree` records (`emitFinishedRowDisagree`); `pkg/api/finished_launch.go` — reuse's `ad.spawn.reuse_restored` (`finishedLaunch.restore`) and, after "duplicate session", its re-lookup's `ad.provenance.disagree` records and `ad.launch.name_held` (`finishedLaunch.heldName`), all with `reuseLaunchVerb` |
| `ad_resume` | The resume verb, one source constant (`nameHeldSourceResume`): `pkg/api/resume.go` — the move (`ad.resume.moved_to_pending`) and the `ad.provenance.disagree` records of its pre-launch lookup (`emitFinishedRowDisagree`, through `pkg/api/provenance_disagree.go`); `pkg/api/finished_launch.go` — the restore (`ad.resume.restored`, from `finishedLaunch.restore`) and the path after "duplicate session" (`finishedLaunch.heldName`), which writes its re-lookup's `ad.provenance.disagree` records and `ad.launch.name_held` through `emitNameHeld` (`pkg/api/name_held_trail.go`), all with `resumeLaunchVerb` |
| `ad_kill` | `pkg/api/kill_trail.go` — the kill verb (`ad.kill.called`, and its `ad.provenance.disagree` records through `pkg/api/provenance_disagree.go`) |
| `ad_expire` | `pkg/api/expire_trail.go` — the expire verb's `ad.expire.kept` records and its `ad.provenance.disagree` records through `pkg/api/provenance_disagree.go` (`expireRun.emitRow`) |
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

**Agent + time window** — filter by `claude_instance_id` and timestamp
range:

```sh
jq -c 'select(.claude_instance_id=="<ID>" and .ts >= "T1" and .ts <= "T2")' \
  ~/.agent-director/ad-trail.jsonl
```

### Stitching with a caller's own trail

A caller that keeps its own trail and records each permission request's
`request_token` can join it with AD's trail on that key. To get a single
chronologically-ordered view across both for one interaction (the
caller's file holds JSON lines with a `ts` field):

```sh
cat ~/.agent-director/ad-trail.jsonl <caller-trail>.jsonl \
  | jq -sc 'sort_by(.ts) | map(select(.request_token=="<TOKEN>"))[]'
```

### Fail-soft semantics

Trail writes never block verb execution. On any write or sync failure the
writer attempts one `ad.trail_meta.emit_failed` envelope (carrying
`original_event` and `error_class`). If that also fails, one line is
written to the operational logger. The original error is always returned
to the caller so verbs can fail-open (SR-A-3.2).

**No path.** When the writer has no path (no usable home, see
[Location](#location)), each `Emit` writes no file and attempts no
`ad.trail_meta.emit_failed` envelope, since there is nowhere to write one.
It writes exactly one line to the operational logger, if one is set
(`trail: no trail path; original=<event> error_class=no_home cause=<error>`),
and returns an error wrapping `errNoHome`. Only `runHook` sets a logger;
elsewhere the line is dropped. Callers treat it like any other trail error:
hooks and the no-verb hook record still exit 0, and `trail-emit`, whose
only job is the write, exits 1 with `ErrTrailWrite` and writes no trail
file.

### Reusable writer

`internal/trail/` is the single write path for all `ad.*` events. All
event-emitting code calls `trail.Emit(ctx, event, fields)` — the
process-singleton `Writer` handles serialization, `tool_input` stripping,
fd management, and fail-soft meta events. Future event types add a new
`event` string and call `Emit`; do NOT introduce a parallel writer or
write directly to the trail file from application code.

## Resume

Relaunching a finished (`ended` or `missing`) row via `claude --resume`.
Same `claude_instance_id`, fresh tmux session, same JSONL transcript.

### Verb (`pkg/api/resume.go`)

Steps run in order (SR-8.1). Every refusal before the move to `pending`
(step 10) writes nothing: no row, parent-id, trust, history or
permission-request write, no `ad.resume.*` event and no create. The only
tmux call before the move is the pre-launch lookup (step 7), whose
`ad.provenance.disagree` records are the one thing such a refusal may
still write. A refusal before pre-trust (step 9) also writes no trust
entry; a move that loses its race after pre-trust leaves the entry
written, harmlessly:

1. `GetSpawn` → `ErrSpawnNotFound` on unknown id.
2. State must be `ended` or `missing` → otherwise
   `ErrSpawnNotResumable`. The verb refuses to touch a live Spawn. A
   `pending` row (a launch in progress whose agent has not reported in,
   a resumed row included) gets the launch-in-progress description
   (`launchInProgressError`, SR-8.4). It gives the launch start from the
   decoded `LaunchStartedAtMillis`, formatted as `launch_started_at` is
   (`launchStartedAt`), or "no launch start is recorded". It also says
   that `resume` applies only to an `ended` or `missing` row, and what
   happens next: the row becomes live if the agent reports in, and if
   the launch was abandoned or failed, `find-missing` marks it `missing`
   once the pending grace period has passed since its launch start. A
   row with no session id (typically a spawn's or reuse's launch), whose
   `resume` step 3 refuses once the row is `missing`, also gets the step
   after that (`launchInProgressNoSessionStep`, b.uey): once `find-missing`
   marks it `missing`, if `get` still shows no session id, spawn the id
   again with the reuse opt-in, in its one spelling (`spawn.ReuseOptIn`),
   since there is no conversation to resume. The step is conditional
   because an agent that reports in late records a session id, and once
   that row is `missing` it can be resumed; a reuse would discard its
   conversation. A row with a session id (typically a resume's launch,
   though a spawn's `pending` row can gain one while still `pending`)
   gets no such step; once `missing`, it can be resumed.
3. `claude_session_id` populated → otherwise `ErrNoSessionId`. A
   Spawn killed before its first SessionStart hook fired has no
   rotated session id to point `--resume` at, nor does a spawn's or
   reuse's launch that never reported in and that `find-missing` marked
   `missing` (the step-2 refusal of its `pending` row named this
   recourse). Recourse: spawn again
   with the same id, opting in to reuse (`--reuse-finished`); the new
   life starts with no memory of the old one.
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
      `~/.claude` when that value is absent, empty or not an absolute
      path: `spawn.ConfigDirUsable`, the rule pre-trust shares) `+
      slug(cwd) + session id`, and that is `os.Stat`'d.
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
      transcript (the id is only passed to the launch: resume never
      rewrites the row's `claude_session_id`, the move keeps it, and the
      relaunched Claude's subsequent SessionStart re-stamps it). This
      is what stops a rotation (the agent's row reported with a new session
      id, for example after the caller restarts its agents) from stranding
      intact history. The walk never leaves the current life: after a
      reuse, which starts a new life, no earlier life's transcript is a
      candidate, so the earlier conversation is never reattached; after a
      failed reuse whose restore applied, the row is back in its pre-reuse
      life and the walk sees exactly that life's history, as before the
      attempt.

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
     user turn). `resume` returns it only for a finished row, which
     `send-keys` refuses, so the row cannot be messaged. Recourse: spawn
     again with the same id, opting in to reuse (`--reuse-finished`); the
     new life starts with no memory of the old conversation.
   - `ErrJsonlMissing` otherwise — the persisted path was set and has
     rotted, or the visible history is non-empty and none of its
     transcripts exists. Recourse: spawn again with the same id, opting in
     to reuse (`--reuse-finished`); the new life starts with no memory of
     the old conversation, which cannot be resumed through agent-director
     afterwards.

   Both messages report each path tried with its source (`persisted`,
   `fallback`, or `history`) and its stat error; every path named comes
   from the current session's own two candidates or the current life's
   visible history (see
   [JSONL path resolver](#jsonl-path-resolver-internalspawnjsonlgo)).
   Steps 1 to 4 read the row once (`GetSpawn`). Everything after works
   from that read: the row is never altered in memory, and the winning
   candidate's session id is passed on as the session to resume. The row
   is re-read only once, and only when the pre-launch lookup refuses a
   Leftover (`resumeLostRace`), solely to compare snapshots (the "lost
   race" case below).
5. The first statement of `resumeAfterJsonl` (SR-8.1 step 2), the
   recorded name before the id. First a recorded session name that is
   empty, holds a control character, or holds a character tmux stores
   differently (`.`, `:`, invalid UTF-8) → `ErrInternal`
   (`unusableNameError`, SR-3.2, wrapped with `resume: ` so the instance
   id is not printed as a prefix); removing the row is a human's decision.
   Then an instance id containing a control character (`hasControlChar`,
   SR-3.13) → `ErrInternal` (no catalogued sentinel), because its session
   could never be labelled. Both come before the socket, with no tmux
   call and nothing written, and both descriptions point to "Operator
   actions" in the README. A recorded name that tmux rewrites (such as
   `mix.$b`, whose `.` tmux stores as `_`) is refused here, before the
   pre-launch lookup, which could not match that name's holder.
6. The launch's socket: `spawn.ResolveRowLaunchSocket` on the row's
   recorded `tmux_socket` (see [Launch identity](#launch-identity)). A
   refusal → `ErrTmuxNotAvailable`.
7. The pre-launch lookup (SR-8.1 step 3, SR-8.2; `resumeAfterJsonl`,
   decided by `decidePreLaunch` in `pkg/api/resume_lookup.go`): exactly
   one [`tmux.Lookup`](#shared-tmux-lookup) on the launch's socket for the
   row's launch identity as read (its instance id, launch token and
   recorded server identity, through `rowLaunch`, with this store's id),
   with the recorded name as the holder name. It is the only tmux call
   before the move; `resume` never uses a name-based tmux method. Its
   outcome is "proceed" or one classified refusal, described in
   [The pre-launch lookup](#the-pre-launch-lookup) below. Its
   `ad.provenance.disagree` records are written right after the decision,
   before any write. A refusal makes no further tmux call, touches no
   session and writes nothing else.
8. A new launch token (`spawn.NewLaunchToken`; a failure → `ErrInternal`),
   then `spawn.ComposeRelaunch` composes env, synthesized settings and
   the argv `claude --resume <session_id> --settings <json> [user
   claude_args]` into a `CreateRequest`. Both run before the move, so a
   failure writes nothing.
9. Pre-trust (`spawn.PreTrust`, SR-8.1 step 4, SR-22.6) for the row's
   cwd and extra env, skipped when the row records the spawn's opt-out
   (`NoPreTrust`). Best effort: its outcome never changes resume's control
   flow or error. See
   [Workspace-trust pre-write](#workspace-trust-pre-write).
10. The move to `pending` (`MoveToPending`, SR-8.3): one conditional write
    guarded on the row's `RowSnapshot` as read. It sets the launch start
    (one read of the Client's clock), the new token, the launch's socket
    and the `parent_id` re-derived from the caller's
    `AGENT_DIRECTOR_INSTANCE_ID` (NULL when unset). It clears `pid`,
    `proc_starttime`, `ended_at`, the liveness note and the server and
    pane identity, and keeps the session id, transcript path, life,
    pre-trust choice and history. This is resume's only parent-id write.
    Before the move, resume builds the `store.ResumePrior` (state, `ended_at` text, pid,
    start time, liveness note, launch identity) from the row as read.
    Outcomes of the move: the row changed → `ErrSpawnNotResumable` ("the
    row changed after resume examined it"); the row is gone →
    `ErrSpawnNotFound`; a store error → `ErrInternal`. In each case
    nothing is written to the store and nothing is launched.
11. The create (`spawn.Relaunch`, the shared create-and-label step) on
    the launch's socket, with the recorded name, labelled `ad1 <token>
    <$N> <instance id> <store id>`. It runs directly after the move, with
    no store, file or network I/O between them. When it returns, resume
    emits `ad.resume.moved_to_pending` (`claude_instance_id`,
    `prior_state`, the row's `claude_session_id` as read, source
    `ad_resume`), fail-open.
12. The outcome (`finishedLaunch.outcome` in `pkg/api/finished_launch.go`,
    built by `resumeDeps.launchOnto` with `resumeLaunchVerb` and shared
    with reuse; SR-8.5):
    - A labelled session: the identity write, `spawn.RecordLaunchIdentity`
      with the move's version and token. The store must also implement the
      optional `RecordLaunchIdentity` method; `*store.Store` does, and an
      injected `ResumeStore` without it makes no identity write. Success.
    - A reply lost with exit 0: success, no identity written; the row
      takes no hook until its pane is adopted.
    - A timeout, or a non-zero-exit reply that does not parse:
      `ErrTmuxUnresponsive` with the launch-timeout description
      (`spawn.LaunchTimeoutError`, verb `resume`). Nothing is written and
      the row stays `pending`, because the session may exist. The
      launch-timeout rule applies: do not retry until `get` shows the row
      `ended` or `missing`.
    - "duplicate session" (a session took the recorded name after the
      lookup): the held-name path, `finishedLaunch.heldName`; see
      [After "duplicate session"](#after-duplicate-session) below. It
      ends with the restore too, and never returns `ErrTmuxSessionCreate`
      for a name it finds held.
    - Every other failure (tmux unavailable → `ErrTmuxNotAvailable`; a
      session that could not be labelled, or any other failed create →
      `ErrTmuxSessionCreate`) is followed by the restore,
      `RestoreAfterFailedResume(id, movedVersion, prior)`
      (`finishedLaunch.restore`). The restore is guarded on `pending` at the move's
      version. It writes the `ResumePrior` back and keeps the move's
      parent id. The launch error's last sentence says what the restore
      did (`restoreResultOf`, the one mapping of the restore's
      result): the row was restored to its prior state; the row changed
      after resume moved it to pending and was left as it is, or was
      removed and nothing was restored; or, on a store error, the row
      could not be restored and stays `pending`, with one `WARN:` line on
      the Client's logger. Then resume emits `ad.resume.restored`
      (`claude_instance_id`, `applied`, `launch_error` as the error's
      name, `restore_error` null or the store error's text, source
      `ad_resume`), fail-open.

On success the verb returns without waiting for the agent: the row
stays `pending` until the resumed agent's first SessionStart hook moves
it (see below).

**Worst case** (SRD SR-13.2, the one source for this bound). With Q the
query timeout (`query_timeout_ms`), C the create timeout
(`create_timeout_ms`), A the action timeout (`action_timeout_ms`) and W
the pipe-close wait (`pipe_close_wait_ms`), `resume` takes at most
max(Q + C + 2A + 4W, 2Q + C + 3W): **10.9 s** at the defaults on path
(i) (lookup, create, label by id, kill of the unlabelled session), and
8.3 s on path (ii) (lookup, create, re-lookup after "duplicate
session"). Pre-trust, the move, the restore and the process check make
no tmux call. Pre-trust's wait for a config lock held by another
process, at most 5 s (see
[Workspace-trust pre-write](#workspace-trust-pre-write)), is separate
and comes on top.

### The pre-launch lookup

`decidePreLaunch` maps the one lookup (step 7) to "proceed" or one
refusal. Every refusal wraps exactly one sentinel and says "nothing was
done" (the Leftover refusal says "nothing was written"). What it names
depends on the case: most name the instance id and the quoted recorded
name; the Leftover refusal names the instance id and the leftover
sessions' own quoted names and `$N` (`preLaunchLeftoverError`); "appears
to still be stopping" with no session names only the instance id; "tmux
unavailable" names the failed call or the socket, as
`spawn.TmuxUnavailableError` words it. None names a session-ending
command, a label value, another row's id or a store id.

| Lookup outcome | Result |
| --- | --- |
| Gone, the recorded agent process not running, and no session holds the recorded name | Proceeds to the token, pre-trust, the move and the launch |
| Gone while the recorded agent process runs (`tmux.JudgeProcess` reads it alive), whatever holds the name | The [starting-session rule](#the-starting-session-rule-as-resume-applies-it) with no session (no age step): `ErrTmuxUnresponsive` "appears to still be stopping", else `ErrTmuxSessionConflict` "this row's own id" |
| Ours: this launch's own session, whatever its name | The [starting-session rule](#the-starting-session-rule-as-resume-applies-it) on that session: `ErrTmuxUnresponsive` "appears to still be stopping" or "appears to still be starting", else `ErrTmuxSessionConflict` "this row's own id" |
| Leftover: sessions with an earlier launch's label of this id, whatever their names | `ErrTmuxSessionConflict` "left over from an earlier life" (`preLaunchLeftoverError`, worded as plain spawn's label scan): up to three sessions by quoted name and `$N`, lowest first, then a count; "nothing was written"; ending such a session is a human's decision, with the "Operator actions" pointer |
| Gone, one session holding the name with a label of another instance id | `ErrTmuxSessionConflict` "a different instance id": another row's agent, which must not be ended; no pointer |
| Gone, one session holding the name with another store's label | `ErrTmuxSessionConflict` "another agent-director store": that store's agent, which must not be ended; no pointer (WD 2026-09-29 STORE) |
| Gone, one session holding the name with no valid label | `ErrTmuxSessionConflict` "no valid instance id": agent-director cannot tell whose session it is; a human must look, with the "Operator actions" pointer |
| Gone, more than one listing entry matching the name | `ErrTmuxUnresponsive`: the holder cannot be told; retry later |
| Can't tell, a different server | `ErrTmuxNotAvailable` (ENVIRONMENT) |
| Can't tell, `provenance_conflict` | `ErrTmuxSessionConflict` "conflicting labels" |
| Can't tell, unreadable (a timeout, an unrecognised reply, or a listing line whose creation time does not parse) | `ErrTmuxUnresponsive`; retry later |
| Can't tell, tmux unavailable (socket permission included) | `ErrTmuxNotAvailable` (ENVIRONMENT) |

The Can't tell refusals go through the shared `cantTellError`, and the
holder refusals through `heldHolderError` / `ambiguousHolderError` under
the phrasing `heldBeforeLaunch`, which makes no "duplicate session" claim.
The rules around the table:

- **The process decides first on Gone.** The holder check runs only when
  the recorded agent process is gone, none is recorded, or it cannot be
  checked (unreadable, or a pid-only identity that reads alive); a
  process that cannot be checked is decided by the lookup alone.
- **Holders.** A prefix neighbour (a longer name, or a prefix of the
  name) never holds the name, and a name containing `$` or `\` that
  matches none of its stored forms (`tmux.StoredForms`) counts as not
  held, so the create decides. A holder with an old or current label of
  this id cannot occur here (the lookup reports it as Leftover or Ours);
  it is handled defensively as a conflict for a human.
- **Another store's session** is never Ours or Leftover (it counts
  toward Gone), so it is met only as the name's holder.
- **No adoption.** An Ours on a row that records no server identity is
  not adopted (`Result.Adopt` is ignored), and `ResumeTmux` has no pane
  listing.
- **A refusal is safe to re-issue.** It writes nothing (see the
  [Verb](#verb-pkgapiresumego) intro) and touches no session, so
  re-issuing `resume` later is how a caller learns that the condition has
  cleared.
- **The lost race.** A resume that read the row before a competing
  resume's move meets that resume's session as Leftover (its label carries
  a token this call did not examine). So a Leftover refusal alone re-reads
  the row once (`resumeLostRace`, one `GetSpawn`, no write, decided by the
  `leftoverLostRace` reuse shares): a changed
  snapshot gives the move's lost-race `ErrSpawnNotResumable`, a removed
  row the move's `ErrSpawnNotFound`, and an unchanged snapshot or a failed
  re-read leaves the Leftover refusal (SR-8.6).
- **Disagree records.** The lookup's reasons, plus `name_changed` when
  Ours was found under another name, go to `ad.provenance.disagree`
  (source `ad_resume`, verb `resume`), once per distinct reason, with
  `action` `refused` or `proceeded`; never `adopted` and never
  `pid_mismatch`.

#### The starting-session rule as `resume` applies it

The rule is the shared one (`checkStartingSession` in
`pkg/api/starting_session.go`, over `tmux.StartingSession`; see
[Starting-session rule](#starting-session-rule-starting_sessiongo)).
`resume` reads its clock once, after the lookup returns, and takes the
bound and the window from the `config.Tmux` accessors
(`startingSessionLimitsOf`); their defaults and settings are in the
[caller contract](#reading-a-refusal). In order:

1. **The stopping window**, on the `ended_at` read before the move: a row
   that ended less than the stopping window ago (a future `ended_at`
   counts as inside) gives `ErrTmuxUnresponsive` "appears to still be
   stopping", retry later. The step is skipped for a row with no
   `ended_at`, or one that records neither a `pid` nor a session id.
2. **The starting-session bound**, on the session's age from the
   listing's creation time: a session younger than the bound (a creation
   time in the future counts as younger) gives `ErrTmuxUnresponsive`
   "appears to still be starting", retry later. Gone while the agent
   process runs has no session, so this step does not run.
3. **Otherwise the own-id conflict**: `ErrTmuxSessionConflict` "this
   row's own id". The agent may be hung or running on a row wrongly
   marked finished, so no automated action on it is safe and a human must
   look ("Operator actions"); the description adds "the conversation
   stays resumable" when the row records a session id.

A creation time that cannot be parsed never reaches the rule: the lookup
reads the listing as malformed, Can't tell unreadable,
`ErrTmuxUnresponsive`.

### After "duplicate session"

A create that answers "duplicate session" proves the create made nothing:
a session took the recorded name between the lookup and the create.
`finishedLaunch.heldName` (`pkg/api/finished_launch.go`, shared with reuse;
SR-8.5, SR-13.2 path (ii)), in this order:

1. Makes exactly one re-lookup on the launch socket for the row as
   examined before the move (its instance id, its earlier launch token and
   its recorded server identity, this store's id), with the recorded name
   as the holder name, then reads the clock once. The move cleared the
   row's identity columns, so nothing is re-read from the store; no agent
   process is read and nothing is adopted.
2. Restores the row (`finishedLaunch.restore`, as for any failed launch above):
   the restore write.
3. Builds the classified error with the shared `heldNameOutcome`, from the
   examined row (`heldExaminedRow`) and the restore's sentence in place of
   "nothing was done" (table below).
4. Emits `ad.resume.restored` with `launch_error` set to that error's
   name.
5. Writes the re-lookup's `ad.provenance.disagree` records, skipping every
   reason the pre-launch lookup already wrote (`finishedLaunch.disagreeWritten`), with
   `action` the restore's row result; then exactly one
   `ad.launch.name_held` (source `ad_resume`, `launch` `resume`,
   `row_result` the restore's). All three records are fail-open.
6. Returns the classified error, last.

| Re-lookup outcome | Error |
| --- | --- |
| One holder with an old label of this id | `ErrTmuxSessionConflict` "left over from an earlier life" (the Leftover wording, quoting the recorded name), with the "Operator actions" pointer |
| One holder with the current label (the row's own session) | The [starting-session rule](#the-starting-session-rule-as-resume-applies-it) with the `ended_at` examined before the move: `ErrTmuxUnresponsive` "appears to still be stopping" or "appears to still be starting", retry later; else `ErrTmuxSessionConflict` "this row's own id" |
| One holder with another instance id's, another store's or no valid label | `ErrTmuxSessionConflict` "a different instance id", "another agent-director store" or "no valid instance id", as at the pre-launch lookup, saying "already exists (duplicate session)" |
| More than one listing entry matching the name | `ErrTmuxUnresponsive`: the holder cannot be told; retry later |
| No holder: it vanished before the re-lookup | `ErrTmuxSessionCreate` (LAUNCH FAILURE): "session creation failed: duplicate session", no session held the name when it was looked up again (`spawn.CreateFailedError`: no "instance <id>" lead and no retry sentence, unlike plain spawn's) |
| Can't tell, a different server | `ErrTmuxNotAvailable` |
| Can't tell, `provenance_conflict` | `ErrTmuxSessionConflict` "conflicting labels" |
| Can't tell, unreadable | `ErrTmuxUnresponsive`; retry later |
| Can't tell, tmux unavailable | `ErrTmuxNotAvailable` |

Each description carries the restore's sentence exactly once (restored to
its prior state; changed or removed and left as it is; or could not be
restored and stays `pending`), never "nothing was done" or "nothing was
written". The holding session is never labelled, killed, read or typed
into. `ErrTmuxSessionCreate` remains only for launch failures (a created
session that could not be labelled included) and for a holder that
vanished before the re-lookup.

A reuse whose create answers "duplicate session" runs this same path
with its own values (`reuseLaunchVerb`): the re-lookup is of the
requested name, against the launch identity examined before the reset;
the restore is `RestoreAfterFailedReuse`; the events are
`ad.spawn.reuse_restored` and an `ad.launch.name_held` with source
`ad_spawn` and `launch` `reuse` (see
[Reuse of a finished id](#reuse-of-a-finished-id)).

### Blocked resume

When `resume` is refused because a session is in the way, the recourse
depends on the case (SR-18.5, AC-DOC-04). The steps that end a session are
a human's and are described only in the README's "Operator actions"
section; this section names no command for them.

- **This row's own id, still stopping or still starting**
  (`ErrTmuxUnresponsive` "appears to still be stopping" or "appears to
  still be starting"): wait and retry `resume`.
- **This row's own id, and old** (`ErrTmuxSessionConflict` "this row's own
  id"): a human investigates as "Operator actions" describes; the
  conversation stays resumable; then retry `resume`. If the session never
  reported in to this row, ending it is a human's decision, made by hand
  after checking its ownership.
- **Left over from an earlier life** (`ErrTmuxSessionConflict`): a human
  ends the leftover as "Operator actions" describes, then retries
  `resume`, as for a plain spawn's leftover.
- **No valid instance id** (`ErrTmuxSessionConflict`): a human checks the
  named session's ownership and may end it by hand as "Operator actions"
  describes, then retries `resume`.
- **A different instance id** (`ErrTmuxSessionConflict`): never end that
  session, which is another row's agent. Wait for its row to finish, or
  respawn with the reuse opt-in (`--reuse-finished`) and a different
  explicit session name.
- **Another agent-director store** (`ErrTmuxSessionConflict`; the holder's
  label carries another store's id): never end that session from this
  store. It is another store's agent, found and stopped only through that
  store's own agent-director ("Operator actions"); or respawn this agent
  with the reuse opt-in (`--reuse-finished`) and a different explicit
  session name. (SR-18.5 lists no such case; WD 2026-09-29 STORE.)

A `resume` or reuse right after `pause` returns, or right after an agent's
natural exit, can get "appears to still be stopping" while the agent's
process exits; a retry after a short wait proceeds. A `resume` right after the last
session on its tmux server ended can also get `ErrTmuxNotAvailable` ("not
the tmux server the agent was launched on") while that server exits; wait
and retry. A refusal after "duplicate session" is followed by the
restore, and its error's last sentence says what the restore did
(`restoreResultOf`). Only when the row was restored to its prior state is
re-issuing `resume` equally safe. When the restore found the row changed
after the move (left as it is) or removed, or hit a store error (the row
stays `pending`), the row is not restored; read it with `get` before
acting on it.

### parent_id re-derivation (SRD §7.5)

`parent_id` records **who currently owns this Spawn**, not who
originally created it:

- Resume from a bare shell → `parent_id = NULL`.
- Resume from inside another Spawn → `parent_id = caller's id`.
- A Spawn originally parented to A and later resumed by B → `parent_id
  = B`.

Resume writes the parent id only in its move to `pending` (step 10
above). A refusal before the move writes none, and a restore after a
failed launch keeps the value the move wrote. The store's `SetParentID`
is called by no verb.

The FK `ON DELETE SET NULL` cascade (Epic 3 schema) handles the
"former parent gets deleted" case orthogonally.

### SessionStart hook side of the contract

SessionStart is one gated statement, `RecordSessionStartIdentity`
(`internal/store/hook_writes.go`; see [Hooks move a row only for its own
agent](#hooks-move-a-row-only-for-its-own-agent)). It applies only when
the hook's parent is the row's recorded pane process, and for a resumed
row that pane is the one the resume's identity write recorded: the move
to `pending` cleared the old pane, so until that write no hook, the old
agent's included, matches the row. The handler reads the row first
(`GetSpawn`) and the write is also conditioned on that snapshot (SR-5.3);
when only the snapshot changed, the handler re-reads and retries once.
When the write does not apply only because the row records no pane yet
(the resume's identity write has not landed), the hook waits for that
write, until the pending grace period measured from the resume's launch
start ends or 540 s after it began waiting, whichever comes first, then
makes the gated write again (see [Hooks move a row
only for its own agent](#hooks-move-a-row-only-for-its-own-agent)); a
fresh spawn's and a reuse's SessionStart wait the same way.

In the one UPDATE it sets `waiting` (whatever the prior state), clears
`ended_at` and the launch start, records the payload's session id and
transcript path, and sets `pid` and `proc_starttime` to the hook's parent
(`getppid()` of the exec-form hook, the pane process; no ancestry walk).

- Fresh spawn `pending → waiting`: `ended_at` was already NULL; the
  `ended_at = NULL` write is a no-op.
- Resumed row `pending → waiting`: resume's move already cleared
  `ended_at`, and the report-in clears the launch start, as on every
  transition to a state other than `pending`. The trail's
  `ad.spawn.state_transition` records `prior_state` `pending`. A resume
  does not start a new life: the row keeps its `life_number`.

`claude_session_id` is overwritten by the same hook payload's
`transcript_path` basename (an empty one keeps the column). Claude Code
rotates the UUID on every `--resume`, so the column carries the
freshly-rotated value after the hook fires — the next resume of this
resumed launch uses the new id, pointing at the new JSONL. An in-session
`/clear`, `/resume` or compaction comes from the same process, so it
applies too and records the new id. The rotation stays within the row's
current life.

**Conditional `jsonl_path` write (b.v2c AC1).** A fresh Claude session writes
no `.jsonl` transcript until its first user turn, so the `transcript_path` the
SessionStart payload reports may not exist yet. The hook `os.Stat`s that path and
tells the store whether the file is present; the store records `jsonl_path`
**only when the file actually exists on disk**, and leaves it NULL (provisional)
otherwise. This is what stops a row from asserting a dead pointer: an
un-messaged, freshly-restarted agent has a NULL `jsonl_path`, not a path to a file
that was never written. A stat error other than not-exist (e.g. a permission
wall) is treated conservatively as "not present". The provisional NULL is not
lossy — the resume fallback recomputes the path, and `find-missing` heals the
row once the transcript appears (below).

**Session-history archival on rotation (b.v2c AC6).** When the SessionStart
payload carries a **different** `claude_session_id` than the row currently holds
(a rotation — for example the agent restarted with a new session), the
store archives the prior `(claude_session_id, jsonl_path)` pair into
`session_history` inside SessionStart's gated write: the same transaction,
right after the UPDATE, and only when the UPDATE applied (an ignored
SessionStart archives nothing). The outgoing session id is the examined
snapshot's; its path and the row's life come from a read pinned to that
snapshot, taken just before the transaction. The entry is written through
`upsertSessionHistoryEntry` on the transaction. The archive is fail-open: a
history error still commits the SessionStart write and emits
`ad.session.archive_failed`. The earlier session's transcript is therefore never
orphaned: while the row stays in that life and the entry is not the row's
current session id, it is reachable through `get`'s `prior_sessions` and is a
resume fallback candidate. A life runs from the spawn that created the row,
or from a reuse's reset, to the next reuse. A reuse archives the current
session itself (fail-closed, in the life it ends) and starts a new life, so
`resume` and `get` of the reused row see only the new life's history and
never reattach the earlier conversation; a failed reuse whose restore
applied returns the row to its pre-reuse life, whose visible history is
exactly as before.

**Lazy transcript healing in `find-missing` (b.v2c AC3).** `find-missing`
already sweeps every live row; on each sweep it also lists rows with a NULL
`jsonl_path` (provisional — a session that started before its transcript was
written), recomposes each path the same way the resume fallback does, `os.Stat`s
it, and records it when present. So the normal case — an agent starts, and a caller
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
  `ExtraEnv["CLAUDE_CONFIG_DIR"]` is usable (`spawn.ConfigDirUsable`: an
  absolute path), so a Spawn that ran under a
  custom config dir finds its transcript under
  `<CLAUDE_CONFIG_DIR>/projects/...` rather than `~/.claude/projects/...`.
  Reuse it whenever you need a transcript path under an explicit config
  dir — do not re-derive the layout by hand. The fallback decides
  whether to use the value by the rule pre-trust's file resolution
  shares (see "One rule for `CLAUDE_CONFIG_DIR`" under
  [Workspace-trust pre-write](#workspace-trust-pre-write)); a relative
  value would resolve against the resuming process's cwd, which differs
  for each caller.
- **`spawn.JsonlPath(cwd, sessionID)`** — a thin wrapper over
  `JsonlPathIn` that resolves the config dir to `$HOME/.claude`. Used
  for the default-config fallback (the row's `CLAUDE_CONFIG_DIR` absent,
  empty or not an absolute path). It reconstructs the default layout:

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
row at launch and restored by `spawn.ComposeRelaunch` (`in.Row.ExtraEnv`,
decoded by `GetSpawn`), so a resumed row's agent re-enters the same
auth/config context as the original — no dependence on whatever the
resuming caller's shell env happens to hold. Persistence adds no new
exposure tier: the values live only in the owner-only state DB
(0600 file / 0700 dir) alongside the rest of the row, and `ExtraEnv`
is not surfaced as an API-visible field.

## Crash recovery and DB hygiene

Three verbs cooperate to keep the DB honest in the face of crashes,
manual kills, and accumulated history: `find-missing` (reconcile),
`expire` (finished-row cleanup), and `agent-director-admin`'s `delete`
(a human's force-removal on the operator tool, not on any agent surface,
and not a cleanup or recovery step; see [`delete`](#delete)).
`find-missing` judges each live row by its agent process and consults
tmux only for a row whose process cannot be checked. A row it marks
`missing` is the sweep's judgement on the evidence available to it, not
proof that the agent has exited. `expire` removes finished rows older than
the retention window whose agent is gone (its process not seen running and
no tmux session of the agent), and keeps and reports the others. The verbs
can be run by an operator's own scheduler at different cadences;
agent-director installs no schedule (see
[Cron user invariant](#cron-user-invariant)).

### Probe layer (`internal/probe`)

`internal/probe` reads facts about one process, by pid. It holds three
readers, each picked by build tag at compile time:

- the [start-time reader](#start-time-reader-procchecker-starttimego)
  (`ProcChecker`, `NewProcChecker()`): the one reader every process
  judgement uses, `find-missing`'s liveness included. It answers alive
  (with the start time), gone or unreadable.
- the [command-name reader](#command-name-reader-commandnamereader-commnamego)
  (`CommandNameReader`, `NewCommandNameReader()`): the hook's
  `parent_command` and `launcher_command` trail fields only, never
  evidence.
- the [parent-pid reader](#parent-pid-reader-parentpidreader-ppidgo)
  (`ParentPIDReader`, `NewParentPIDReader()`): the hook's launcher
  warning only (`launcher_pid`, `ad.hook.launcher_detected`), never
  evidence.

No reader reads a process environment or the clock. The environment
scan (which listed every process's `AGENT_DIRECTOR_INSTANCE_ID`) and the
liveness checker that used an environment read as a tiebreaker have been
removed. No verb reads a process environment, and an agent's
`AGENT_DIRECTOR_INSTANCE_ID` (`probe.EnvKey`) is never liveness or
ownership evidence: it only lets a caller running inside an agent name
itself. `find-missing` judges a row by its agent process's start time
through the start-time reader (see [`find-missing`](#find-missing)).

Files:

| File | Holds |
|---|---|
| `probe.go` | Package doc; `ErrProbeUnsupported`; `EnvKey`. |
| `starttime.go`, `starttime_{linux,darwin,unsupported}.go`, `starttime_{linux,darwin}_core.go` | The start-time reader: the interface, `NewProcChecker()`, the per-OS wiring (`defaultProcRoot`; `fetchKinfoPID`) and the build-tag-free cores (`linuxStartTimeReader`, `darwinStartTimeReader`, `unsupportedProcChecker`). |
| `commname.go`, `commname_{linux,darwin,unsupported}.go`, `commname_{linux,darwin}_core.go` | The command-name reader, laid out the same way. |
| `ppid.go`, `ppid_{linux,darwin,unsupported}.go`, `ppid_{linux,darwin}_core.go` | The parent-pid reader, laid out the same way (`linuxParentPIDReader`, `darwinParentPIDReader`, `unsupportedParentPIDReader`). |
| `parse_linux_stat.go` | `parseLinuxStatWithState` (field 3 state, field 4 ppid, field 22 start time, anchored on the last `)`) and `ErrLinuxStatMalformed`. |
| `parse_kinfo.go` | The kinfo_proc constants and the per-entry extractors `parseKinfoStartTime`, `parseKinfoStat`, `parseKinfoComm` and `parseKinfoPPID`; `formatDarwinProcStartTime`; `ErrKinfoLayoutDrift`. |
| `errno.go` | The errno classifiers `classifyLinuxErrno` / `classifyDarwinErrno` and their `procDisposition` values (see the table below). |

**`ErrProbeUnsupported`.** No verb returns it: the removed environment
scan was the only path that did. It stays in the error catalogue
(`pkg/api/errnames`) and in `find-missing`'s `ErrorNames` so the
catalogue and the client error classes stay unchanged (SR-1.7). An OS with
no per-OS reader does not return it either: its readers answer unreadable
for every pid.

**macOS kinfo_proc layout.** On darwin all three readers read the pid's single
KERN_PROC_PID `kinfo_proc` entry (never KERN_PROCARGS2). `parse_kinfo.go`
carries a family of XNU-version-sensitive constants:

- `kinfoProcSize` (sizeof struct kinfo_proc = 648): every extractor
  refuses an entry that does not fit (`entryOffset`), a short entry
  included.
- `kinfoProcPIDOffset` (40, extern_proc.p_pid): read by no extractor; it
  anchors the `p_stat` and `p_comm` derivations.
- `kinfoProcStartSecOffset` / `kinfoProcStartUsecOffset` (0 and 8): the
  `kp_proc.p_starttime` timeval aliases the head of extern_proc's leading
  `p_un` union, so `tv_sec` sits at the very start of the entry and
  `tv_usec` 8 bytes in. They feed the start-time reader
  (`parseKinfoStartTime`).
- `kinfoProcStatOffset` (36, `extern_proc.p_stat`, a char): `p_un` (16) +
  `p_vmspace` (8) + `p_sigacts` (8) + `p_flag` (4) = 36, from
  `<bsd/sys/proc.h>`, just before `p_pid` at 40 (3 bytes of padding align
  the pid). The start-time reader uses it to recognise a zombie. Its known
  values are `kinfoStatSIDL` (1) through `kinfoStatSZOMB` (5, a zombie);
  `parseKinfoStat` treats any value outside that range as drift.
- `kinfoProcCommOffset` (243) and `kinfoProcCommLen` (17,
  `extern_proc.p_comm`): feed the command-name reader (`parseKinfoComm`).
- `kinfoEprocPPIDOffset` (kp_eproc.e_ppid = `sizeof(extern_proc)=296` +
  `offsetof(eproc, e_ppid)=264` = 560): feeds only `parseKinfoPPID`, whose
  plausibility bound is `maxPlausiblePID` (4 194 304) and which also
  refuses 0. Its one production caller is the parent-pid reader
  (`darwinParentPIDReader`).

All are pinned to XNU 11.x (macOS 14 / 15) off the same LP64 header basis
and are NOT a kernel ABI guarantee: a future macOS major that resizes the
struct silently drifts them. Each extractor checks its fields against a
plausibility guard and, when an entry fails, returns `ErrKinfoLayoutDrift`,
a distinct sentinel that wraps nothing. Drift fails **open**: the
start-time reader answers unreadable (never gone) and the command-name
and parent-pid readers answer none, so a hook whose parent start time is
unreadable is ignored, never applied.

**macOS-major bump policy.** When supporting a new macOS major: compile
the matching XNU sources (Apple publishes them at
`apple-oss-distributions/xnu`), re-derive `kinfoProcSize` +
`kinfoProcPIDOffset` **and** the entry offsets
`kinfoEprocPPIDOffset` / `kinfoProcStartSecOffset` /
`kinfoProcStartUsecOffset` / `kinfoProcStatOffset` /
`kinfoProcCommOffset` + `kinfoProcCommLen` (and the
`kinfoStatSIDL`..`kinfoStatSZOMB` values) from `<bsd/sys/proc.h>` +
`<bsd/sys/sysctl.h>`, refresh the constant comments in `parse_kinfo.go`,
and re-run `GOOS=darwin GOARCH=arm64 go build ./...` plus
`go test ./internal/probe/...` under that macOS version (the CI
`macos-probe` job; `starttime_darwin_test.go` drives the real
KERN_PROC_PID sysctl). A drift in the start-time or state offsets changes
every process judgement's answers on darwin (to unreadable); a drift in
the command-name offsets empties `parent_command` and `launcher_command`;
a drift in `kinfoEprocPPIDOffset` leaves `launcher_pid` null and writes no
`ad.hook.launcher_detected`. The plausibility guards
are a safety net, not a substitute for the bump.

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
running agent process (SR-4.2). Its callers so far: the `pkg/api` Client
builds one in `New` and passes it to `spawn.Launch` and resume's
`spawn.RecordLaunchIdentity` for the identity write's start times, to
the label scan's lookup, to `Kill` (its lookup, adoption and process
wait), to `FindMissing` (each row's process judgement, its lookups and
adoption) and to `Expire` (each row's `process_alive` check and its
lookups); and `cmd/agent-director` wires one into the hook
handler (`hook.HandleConfig.ParentProc`, beside the command-name and
parent-pid readers)
to read the hook parent's start time for the gate.
The [shared tmux lookup](#shared-tmux-lookup) declares its own
`tmux.ProcChecker` with the same method, which `NewProcChecker()` satisfies
structurally, so `internal/tmux` never imports this package. In-process
tests fake it with
[`procfix`](#procfix-the-process-checker-fake-reusable-test-fixture);
real-process proof (this package's tests, `test/realtmux`) uses
`NewProcChecker()`.

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
  entry (`fetchKinfoPID`; no KERN_PROCARGS2) and parses both
  `parseKinfoStat` and `parseKinfoStartTime` before the zombie check. ESRCH
  or an empty buffer is gone; any other errno or layout drift is unreadable.
- **Other** — `unsupportedProcChecker` answers unreadable for every pid.

**Errno classification (SR-7.4): unreadable, never gone.** Only positive,
pinned evidence yields gone; any unexpected errno, malformed entry or
layout drift reads unreadable. A failed read is classified by the pure
functions `classifyLinuxErrno` / `classifyDarwinErrno` (`errno.go`), so
the tables are unit-testable off any OS (`errno_test.go`):

| Evidence | Linux (`<procRoot>/<pid>/stat`) | darwin (KERN_PROC_PID) | Answer |
| --- | --- | --- | --- |
| Entry absent | ENOENT/ESRCH, with `<procRoot>/self` resolving | ESRCH, or an empty result | gone |
| Entry absent, procfs not mounted or root unreadable | ENOENT/ESRCH, `<procRoot>/self` not resolving | — | unreadable |
| Zombie | state `Z` or `X` | `p_stat == SZOMB` | gone |
| Entry read, not a zombie | stat parses | `p_stat` and `p_starttime` pass their guards | alive, with the start time |
| Permission wall | EACCES/EPERM | EACCES/EPERM | unreadable |
| Anything else | any other errno; `ErrLinuxStatMalformed` | any other errno; `ErrKinfoLayoutDrift` (a short entry included) | unreadable |

A different start time is not an answer of the reader: the caller compares
the alive answer's start time with its recorded one, and a mismatch means
the pid was reused. The reader never returns an error; every failure folds
into unreadable.

The Linux and darwin cores and `unsupportedProcChecker` are build-tag-free,
so tests drive them on any OS through their seams: `procRoot` (a temp
directory) and `fetchKinfo` (a synthetic 648-byte entry).

#### Command-name reader (`CommandNameReader`, `commname.go`)

`CommandNameReader.CommandName(pid) (name, ok)` reads a process's command
name: on Linux `<procRoot>/<pid>/comm` without its trailing newline
(`linuxCommandNameReader`); on darwin the KERN_PROC_PID entry's `p_comm`
(`darwinCommandNameReader`, `parseKinfoComm` at `kinfoProcCommOffset`
243, length 17); elsewhere `unsupportedCommandNameReader`. Any failure
(pid ≤ 0, no such process, a permission wall, an empty or malformed
entry, layout drift) is `("", false)`; it never errors, logs or reads an
environment. `NewCommandNameReader()` picks the implementation by build
tag, and the cores are build-tag-free like the start-time reader's. Its
uses are `ad.hook.ignored`'s `parent_command` and
`ad.hook.launcher_detected`'s `launcher_command`, each read only when its
record is written. It is **never evidence**: no ownership, liveness,
launch or hook-gate decision reads it. The hook gate compares only the
parent's pid and start time.

#### Parent-pid reader (`ParentPIDReader`, `ppid.go`)

`ParentPIDReader.PPID(pid) (ppid, ok)` reads a process's parent pid: on
Linux field 4 of `<procRoot>/<pid>/stat`, parsed by
`parseLinuxStatWithState` (`linuxParentPIDReader`); on darwin the
KERN_PROC_PID entry's `e_ppid` (`darwinParentPIDReader`,
`parseKinfoPPID` at `kinfoEprocPPIDOffset` 560); elsewhere
`unsupportedParentPIDReader`. Any failure (pid ≤ 0, no such process, a
permission wall, an unreadable or malformed entry, a parent pid of 0,
layout drift) is `(0, false)`; it never errors, logs or reads an
environment. `NewParentPIDReader()` picks the implementation by build
tag, and the cores are build-tag-free like the start-time reader's.
`cmd/agent-director`'s `hookParentProc` wires it into the hook handler
as `HandleConfig.ParentProc`'s `PPID`. Its one use is the hook's launcher
warning: after the gate refuses a hook with `pid_mismatch`, one read of
the parent's own parent pid, compared with the row's `pane_pid` (see
"Launcher warning" in [Hooks move a row only for its own
agent](#hooks-move-a-row-only-for-its-own-agent)). It is **never
evidence**: no ownership, liveness, launch or hook-gate decision reads
it, and no code may use it to walk ancestry or to identify a hook's
agent.

#### The ancestry resolver is retired

The hook no longer walks process ancestry; its reads past the parent
are the launcher warning's parent-pid read, plus the launcher's
command-name read only when `ad.hook.launcher_detected` is written; both
report and never identify an agent. `probe.Resolver` (the
topmost-ancestor walk that chose SessionStart's recorded pid) and its
per-OS files are deleted: it let a nested `claude` pass as the pane's
agent. SessionStart's `pid` and `proc_starttime` are now the exec-form
hook's parent (`getppid()`, SR-22.9). No code may reintroduce an
ancestry walk to identify a hook's agent.

### Degraded-mode reconciliation + cron user

**There is no global degraded-mode refusal.** The old guard ("the probe
returned no ids while the store holds a live row: write nothing") has been
removed (SR-7, SR-8). It over-refused, and a single unreadable process
blocked the whole sweep. `find-missing` now reconciles **each row on its
own evidence**: a row it cannot decide is left live and surfaced as
unverified, and never blocks another row.

**Per-row evidence model (SR-11.1, SR-11.3).** A live row's liveness comes
only from its agent process, read by the
[start-time reader](#start-time-reader-procchecker-starttimego). The agent
process is picked by `tmux.SelectAgentProcess` from the row's SessionStart
identity (`pid`, `proc_starttime`) and its recorded pane identity
(`pane_pid`, `pane_starttime`): the one recorded, or the pane identity when
both are recorded and disagree (see
[Agent-process selection and judgement](#agent-process-selection-and-judgement-agent_processgo);
a disagreement writes no record). `tmux.JudgeProcess` then gives one of four
answers:

| Process evidence (`ProcState`) | Outcome |
| --- | --- |
| Alive with its recorded start time (`ProcAlive`) | The row stays live whatever tmux shows. A liveness note it carries is cleared; a row with no note is not written. No tmux call. |
| Gone: absent, a zombie, or alive with another start time (`ProcGone`) | Marked `missing` with tick reason `proc_absent`, whatever tmux shows, with no tmux call. A pane kept by `remain-on-exit` or a respawned pane never keeps the row live. |
| Unknown: unreadable, or a pid-only identity that reads alive (`ProcUnknown`) | Decided by the tmux lookup of the row's socket (see [`find-missing`](#find-missing), "The tmux path"), unless its recorded session name cannot be used: then it is left unverified with a note of its own and no tmux call (see "Unusable recorded name" under [`find-missing`](#find-missing)). A pid-only identity that reads gone is marked like any gone process. |
| Absent: no process identity recorded (`ProcNone`) | Decided by the tmux lookup, as unknown, with the same unusable-name exception. |

No process environment is read. A child process of the agent, or any other
process that carries the row's `AGENT_DIRECTOR_INSTANCE_ID`, never keeps a
row live, and a live agent whose environment lacks the id is not marked.

**Errno mapping (SR-7.4).** A process read that fails for any reason other
than a proven-absent entry reads unreadable, never gone; the per-OS table
is in the [start-time reader](#start-time-reader-procchecker-starttimego)
section.

**Liveness notes and ticks (SR-11.4).** Unknown liveness is row metadata,
never a lifecycle state: the `state` enum is untouched. Two nullable columns
carry it and `list` and `get` show them: `liveness_note` (why the row is
unverified) and `liveness_unverified_since` (when it first became
unverified). The notes in use are:

| Note | Written when |
| --- | --- |
| `probe_eacces` | The process evidence is unknown and the lookup is Ours, unreadable, tmux unavailable or not called; also a row judged unknown by the pane its adoption found. |
| `process_not_seen_session_present` | No process evidence is recorded and the lookup is Ours, unless the adoption listing decided otherwise (see "Adoption of a lost reply" under [`find-missing`](#find-missing)). |
| `process_not_seen_tmux_unchecked` | No process evidence is recorded and the lookup is unreadable, tmux unavailable or not called. |
| `tmux_server_changed` | The lookup is Can't tell, a different server. |
| `provenance_conflict` | The lookup is Can't tell: two sessions carry the current label, or a scope value is set. |
| `tmux_session_name_empty` | The process evidence is unknown or absent and the recorded session name is empty; no lookup is made. |
| `tmux_session_name_control_char` | As above, and the recorded name holds a control character. |
| `tmux_session_name_rewritten` | As above, and the recorded name holds a character tmux stores differently (`.`, `:` or bytes that are not valid UTF-8). |

When a row's recorded name cannot be used, its unusable-name note is the
one written, ahead of every tmux-path note above, the "not called" notes
included: no tmux call is made, so no lookup result exists to note. A name
with several faults takes the first in the order empty, control character,
rewritten (SR-11.4).

The rules, applied by the note writer `writeLivenessNote`
(`pkg/api/find_missing_writes.go`):

- The latest reason overwrites the note; `liveness_unverified_since` keeps
  its first time.
- A note equal to the one the sweep read writes nothing (no version change,
  no tick); the row is still reported unverified.
- A note write that applied ticks (`ad.find_missing.tick`, `prior_state`
  and `new_state` null) only when the row goes from no note to a note (the
  tick's `reconciliation_reason` is that note), or enters
  `provenance_conflict` from no note or from another note. Any other change
  of note ticks nothing.
- A row whose process is alive has its note cleared (`clearLivenessNote`),
  with no tick; a row with no note is not written, so an alive row's
  version does not advance on every sweep.
- A mark clears both columns in the same write (below), and every hook
  row-update path clears them too.

**The same-life guard (SR-11.6).** Every write the sweep makes to a row is
one statement guarded on a live state and the row snapshot the sweep read
(or the one its adoption write produced): the mark
(`MarkMissingIfSameLife`), the note write (`SetLivenessNoteIfSameLife`), the
clear (`ClearLivenessIfSameLife`) and the adoption
(`AdoptIdentityIfSameLife`). A write that finds the row changed (a hook, a
relaunch, a reuse or a verb moved it after the read) or absent (deleted), or
that fails in the store, applies nothing: no tick, no permission-request
denial, and the row is in neither result list. A store error is logged on
the Client's logger and the sweep goes on (SR-5.8). A row gets at most one
guarded verdict write per sweep (mark, note or clear), plus the adoption
write before it.

**Marking order (pinned).** A row is marked in this order, by
`markMissingSameLife`:

1. The guarded mark `MarkMissingIfSameLife`: `state` `missing`, `ended_at`
   and `last_seen_at` set, the liveness clear and the launch-start clear
   (`launch_started_at` NULL) folded into the same statement, `row_version`
   advanced by one.
2. Only when it applied, exactly one `ad.find_missing.tick` with
   `prior_state`, `new_state` `missing`, the mark's `reconciliation_reason`
   and source `ad_find_missing` (plus `lookup_outcome`, and
   `tmux_session_name` for `tmux_name_held`, on a mark the lookup decided).
3. Then `CloseOrphanedPermissionRequests`, which fail-closes any relay
   polling loop for the row and writes its own `permission_orphan_closeout`
   tick per closed request. Its error is logged and the row still counts as
   marked.

Trail writes are fail-open: a failed one never changes the sweep's result.

**`missing` is a judgement, not proof.** A row marked `missing` is the
sweep's judgement on the evidence available to it, not proof that the agent
has exited. Neither `ended` nor `missing` means that the agent is dead or
that its row is safe to delete (SR-18.2).

Where agent-visible text states it (decision-0930e):

- Full sentence ("`missing` is the sweep's judgement on the evidence
  available to it, not proof that the agent has exited"): `find-missing`'s
  description, where `missing` is produced; the `state` result field of
  `status` and `get`; `list`'s `spawns`; and `find-missing`'s `ids`. The
  result fields reach only the generated references and `surface.json`,
  never `help` or MCP `tools/list`, so they cost no agent tokens.
- Short form: the `kill`, `resume`, `pause` and `expire`
  descriptions carry the one constant `missingNotProofShort` in
  `pkg/api/manifest/manifest.go`: "`missing` is not proof the agent exited
  (see find-missing)." Every verb description reaches `help` and MCP, so
  the full sentence at each would cost every agent tokens on every session
  and turn (see the [help size guard](#pkgapimanifest--verb-registry)).
- The README, this document, the Go docs and the error descriptions
  (SR-1.4) keep the full statement; none of them is in `help`.

**Cron user story (SR-18.7).** `find-missing` must run as the agents' user,
in their tmux environment (the same `TMUX` / `TMUX_TMPDIR`, so a row that
records no socket is looked up on the agents' server). A run as another
user, as root or against another tmux server can mark live rows `missing`:
their processes may read unreadable, and the lookup then asks the wrong
server, which answers that the row's session is Gone. The recommended
operator setup is a **systemd user timer or a personal crontab** of the
agents' user, not a system-level cron; agent-director installs no schedule
itself.

**Finished-row cleanup (SR-18.7).** Finished rows are removed by an
operator-scheduled `expire` at the default retention, run as the same user
and in the same tmux environment as the agents; it keeps and reports rows
whose session runs or cannot be checked (`kept_ids`, with each reason in
`ad.expire.kept`). Agents never run `expire`, least of all with a zero
window. A run as another user, as root or against another tmux server can
wrongly delete rows (see [Cron user invariant](#cron-user-invariant)).
Agent-visible text carries this as `expire`'s Description sentence; `kill`
and `find-missing` carry only the short pointer `expireCleanupPointer`
("Agents never run expire (operator-scheduled finished-row cleanup).") in
`pkg/api/manifest/manifest.go`, which keeps `help` under its size guard.

### `find-missing`

`pkg/api/find_missing.go` (the verb), `find_missing_lookup.go` (the tmux
path), `find_missing_writes.go` (the per-row writers), `sweep_socket.go`
(the no-socket rule) and `agent_pane.go` (`adoptInSweep`, the adoption
step). The evidence model, notes, guard and marking order are in
[Degraded-mode reconciliation + cron user](#degraded-mode-reconciliation--cron-user).

1. `ListLiveSpawnIdentities` returns every row where `state NOT IN (ended,
   missing)`, `pending` included, each carrying its state, its launch start
   (`launch_started_at` in milliseconds, read through
   `decodeLaunchStartedAt`), its recorded session name, liveness note, row
   snapshot, recorded `pid` + `proc_starttime` and launch identity (token,
   socket, server and pane identity). No stored value fails the read; a list
   error is the sweep's only error. The rows are judged in instance-id
   order, so the per-socket stop and the budget's cut-off fall on the same
   rows on every run. The sweep reads its injected clock once, after the
   list.
2. A `pending` row inside the pending grace period is not judged at all
   (SR-11.2; `store.InsidePendingGrace`). Its age is the sweep's clock minus
   its launch start, and it is inside while that age is below the setting;
   a launch start in the future counts as inside. The row gets no liveness
   check, no start-time read, no mark, no liveness-note write or clear, no
   `ad.find_missing.tick`, no permission close-out and no tmux call, and it
   is in neither `ids` nor `unverified_ids`. The rule is the same for a
   fresh spawn's, a reuse's and a `resume`'s launch: `started_at` (for a
   resumed row, the original spawn's time) plays no part. A `pending` row
   whose launch start is absent or cannot be read (NULL, not an integer,
   or an integer outside years 0 to 9999 UTC, all of which decode to 0)
   counts as past the grace period and is judged at once. A row past the
   grace period, and every row in another live state, goes on to the steps
   below. The setting is `pending_grace_seconds` in the `[tmux]` table
   (60 s by default), read through `config.Tmux.EffectivePendingGrace` and
   handed to the sweep by `Client.FindMissing` together with the client's
   clock; its minimum rule is in
   [`[tmux]` timing settings](#tmux-timing-settings). The hook's
   SessionStart wait for its launch's identity write uses the same
   predicate and the same grace period (SR-13.4; see [Hooks move a row
   only for its own agent](#hooks-move-a-row-only-for-its-own-agent)). The
   hook's wait also ends 540 s after it began (`sessionStartWaitCap`), so
   a SessionStart stops waiting at the same bound after which this step
   judges the row only while the grace is 540 s or less; with a grace
   above 540 s it stops at 540 s while this step still counts the row as
   inside its grace period (the hook gate's residuals). Residual: the
   launch start comes from the launching process's wall clock and the
   age from the sweep's, so a clock step back (or a launch start written
   by a host whose clock runs ahead) keeps a row inside the grace period
   longer than configured; there is no cap on this step. The same step
   does not lengthen a SessionStart's wait past 540 s: that wait ends at
   its cap, on the monotonic clock, or earlier (the hook gate's
   residuals).
3. Every other row is judged by its agent process (`judgeLiveRow`; the
   evidence model in
   [Degraded-mode reconciliation + cron user](#degraded-mode-reconciliation--cron-user)):
   alive clears any note, gone marks it `proc_absent`, and neither makes a
   tmux call. This holds whatever the recorded session name is.
4. A row whose process evidence is unknown or absent and whose recorded
   session name cannot be used is left unverified with a note of its own
   (see "Unusable recorded name" below).
5. Any other row whose process evidence is unknown or absent is decided by
   its socket's lookup (`lookupRow`, below).
6. Each outcome is written through the guarded per-row writers
   (`markMissingSameLife`, `writeLivenessNote`, `clearLivenessNote`) in the
   pinned marking order. Once the row's write has settled, its
   `ad.provenance.disagree` records are written (`emitDisagree`): one per
   distinct reason per row per sweep, from the lookup's reasons,
   `name_changed`, `adopted` (only when the adoption write applied) and a
   failed adoption listing's reasons, each carrying the server and verdict
   of the observation that produced it.
7. Provisional transcripts are healed (`healProvisionalTranscripts`, b.v2c
   AC3): each live row with a session id and no `jsonl_path` has its path
   recomposed and recorded once the file exists. Per-row errors are logged
   and skipped.

**Unusable recorded name (SR-3.2, SR-11.3, SR-11.4).** In
`judgeLiveRow`'s branch for unknown or absent process evidence, before
`lookupRow`, the row's recorded session name is classified by
`tmux.Unusable`, the one unusable-name classifier (see
[Unusable-name guard](#unusable-name-guard-unusablego)), and
`unusableNameNote` maps its kind to the note: `UnusableEmpty` to
`tmux_session_name_empty`, `UnusableControl` to
`tmux_session_name_control_char`, `UnusableRewritten` to
`tmux_session_name_rewritten`, `UnusableNone` to none. A noted row is
written through `writeLivenessNote`, guarded on the snapshot the sweep
read, with the SR-11.4 rules of
[Degraded-mode reconciliation + cron user](#degraded-mode-reconciliation--cron-user)
(an equal note writes nothing; an applied write ticks only on entry from
no note), and is reported in `unverified_ids`. Such a row gets no tmux
call, no socket resolution (`sweepSockets.forRow` is not called), no
lookup and no pane listing, so it neither triggers nor meets the
per-socket stop and spends none of the budget, and it writes no
`ad.provenance.disagree`. The order is: the pending grace period (a
`pending` row is checked only once past it), then the process (alive or
gone still decides the row), then the unusable name, then the lookup. The
row is never marked from tmux evidence; removing it is a human's decision
(the README's "Operator actions").

**The tmux path (SR-11.3, SR-3.15).** Only rows whose process evidence is
unknown or absent, and whose recorded session name can be used, reach
tmux. The run holds one `tmux.Sweep` (see
[Sweep](#sweep-sweepgo)): one lookup per socket per run, taken by the first
row that needs it and shared by every later row of that socket, each row
judged against its own launch (the shared `rowLaunch`: instance id, token,
recorded server identity, this store's `store_id`, socket) and holding its
recorded session name as the holder name.

- **Which socket.** A row's recorded `tmux_socket`, exactly as recorded; the
  caller's environment is not consulted. A row that records none (from
  before the release) uses the caller's socket through the shared no-socket
  helper `sweepSockets.forRow` (`pkg/api/sweep_socket.go`), which resolves
  it through `rowSocket` / `spawn.ResolveQuerySocket` at most once per run,
  only when such a row reaches the lookup, and creates nothing. A missing
  per-user directory is no refusal: the lookup at its would-be socket reads
  tmux's no-socket reply (Gone). An unusable one means no tmux call for
  those rows: the first gets tmux unavailable, every later one not called.
- **Outcomes** (the verdicts and tokens are the
  [shared tmux lookup](#shared-tmux-lookup)'s):

| Lookup outcome | Row outcome |
| --- | --- |
| Ours (the row's own labelled session) | Adoption first (below). Then unverified: `probe_eacces` for unknown evidence, `process_not_seen_session_present` for none, except that a row that records no pane is decided by the adoption listing. |
| Leftover (only sessions of earlier lives) or Gone (no session of this row's current launch) | Marked `missing`, whatever holds the recorded name: tick reason `tmux_name_held` with `lookup_outcome` and `tmux_session_name` when a session holds that name (one holder, or more than one listing entry matching it), else `tmux_absent` with `lookup_outcome`. |
| Can't tell, a different server | Unverified, `tmux_server_changed`. The socket is not stopped. |
| Can't tell, `provenance_conflict` (two sessions with the current label, or a scope value) | Unverified, `provenance_conflict`. The socket is not stopped. |
| Can't tell, unreadable, or tmux unavailable | Unverified, `probe_eacces` (unknown evidence) or `process_not_seen_tmux_unchecked` (none); no more calls on that socket this run. |
| Not called (`not_run`: the socket stopped, the budget is spent, or the no-socket resolution refused earlier) | The same notes as unreadable. |

**The Gone rule.** A row is marked on Gone or Leftover whatever holds its
name, and never while its own labelled session (the current label) exists.
A session whose valid label carries another store's id (the label's last
field is not this store's `store_id`; see the session label under
[Launch identity](#launch-identity) and the label classes under
[Shared tmux lookup](#shared-tmux-lookup)) counts toward Gone for the row,
even when it names this row's id and token, and the sweep never touches it
(WD 2026-09-29 STORE).

**Adoption of a lost reply (SR-3.6, SR-11.6; LFR H2).** On an Ours lookup
for a row that records no server identity or no pane, the adoption step
(`adoptInSweep`, built on the shared rules in `findAdoption`) runs before
the row is judged. A row that records no pane takes its socket's pane
listing through the Sweep (at most one per socket per run, under the stop
rule and the budget) and looks for the one pane whose `@ad_pane` carries
its launch token (`tmux.PaneByToken`). When the identity found adds
something, one `AdoptIdentityIfSameLife` write, guarded on the snapshot the
sweep read, records it; the snapshot it returns guards the row's verdict
write. An adoption write that finds the row changed or absent, or fails in
the store, ends the row: no verdict write, no tick, neither list. A listing
that did not answer (skipped, failed, or over the budget) adopts nothing,
not even the server identity. For a row with no process evidence that
records no pane (a lost create reply), the listing decides:

- exactly one pane carries the token: that pane is adopted and the row is
  judged by its process (alive clears, gone marks `proc_absent`, unknown
  notes `probe_eacces`);
- no pane carries it: the row counts as Gone and is marked `tmux_absent`,
  with `lookup_outcome` the lookup's own token (`ours`) and no held-name
  record, although its own session holds the name;
- the listing did not answer, or two panes carry the token: unverified,
  `process_not_seen_session_present`.

**The held-name record (SR-11.3, SR-14).** After every mark attempt with
`tmux_name_held`, whatever the mark's outcome, the sweep writes exactly one
`ad.launch.name_held` naming the holding session, through the shared
emitter `emitNameHeld`: source `ad_find_missing`, `launch` and `outcome`
null, this store's `store_id`, and `row_result` `marked_missing`,
`left_changed`, or `still_pending` with the store error's text. The sweep
never kills, reads or types into the holding session; it keeps running
until a human ends it (the README's "Operator actions").

**Wedged tmux and the budget (SR-3.15, SR-13.3, SR-13.5).** After an
unreadable or tmux-unavailable result on a socket (from its lookup or its
pane listing), the Sweep makes no more calls there. `sweep_budget_seconds`
(`[tmux]`, 15 s by default; a setting with no safe minimum) caps the run's
cumulative tmux wait on the injected clock; once it is spent every
remaining row is not called and gets the notes above, and is looked at
again on the next run. A run spends at most the budget plus one call on
tmux. `Client.FindMissing` passes the value from
`config.Tmux.EffectiveSweepBudget`. tmux problems never fail the sweep.

**The result.** `ids` / `count` are the rows marked `missing` this sweep,
on process or tmux evidence. `unverified` / `unverified_ids` are the rows
left live with a note, whether the note was written or already current,
an unusable-name note included; never a `pending` row inside the grace
period. A row whose guarded write
missed or failed in the store, and a row found alive (its note cleared or
none to clear), is in neither list. Both lists are sorted and never null. The verb adds no
verb-level error; `ErrProbeUnsupported` stays in its error list (SR-1.7)
but is never returned.

**Go API (Appendix F.4).**

- `FindMissing(ctx, s FindMissingStore, t FindMissingTmux, pc ProcChecker,
  pendingGrace, sweepBudget time.Duration, now func() time.Time,
  lg FindMissingLogger) (FindMissingResult, error)` is exported. It uses
  the values it is given, with no fallback and no minimum check; it never
  reads `time.Now` itself.
- `FindMissingStore`: `ListLiveSpawnIdentities`, the guarded
  `MarkMissingIfSameLife`, `SetLivenessNoteIfSameLife`,
  `ClearLivenessIfSameLife` and `AdoptIdentityIfSameLife`,
  `CloseOrphanedPermissionRequests`, `ListProvisionalTranscripts`,
  `HealJsonlPath` and `StoreID`; `*store.Store` satisfies it.
- `FindMissingTmux`: `TmuxLookup` plus `ListPanes(socket)`; `TmuxClient`,
  `*tmux.Client` and `tmuxfix.Recorder` satisfy it.
- `Client.FindMissing` passes the Client's store, tmux client, start-time
  reader, `EffectivePendingGrace`, `EffectiveSweepBudget`, clock and logger.

| File | Holds | Must use |
| --- | --- | --- |
| `find_missing_lookup.go` | `lookupRow` (SR-11.3's table), `oursRow`, `adoptedPaneRow`, `lookupMarkRow`, `emitRowNameHeld`, `notCalledNote` (each row's `tmux.Launch` comes from the shared `rowLaunch`, `lookup_outcome.go`). | The one mapping from a sweep row's lookup to its mark or note; a sweep never decides a row from a `tmux.Result` another way. |
| `find_missing_writes.go` | The per-row outcome writers, shared by the process path and the tmux path: `markMissingSameLife` (the pinned marking order: guarded `MarkMissingIfSameLife`, then one `ad.find_missing.tick` with any extra fields the tmux path passes, then `CloseOrphanedPermissionRequests`), `writeLivenessNote` (skips a note equal to the one read; the guarded `SetLivenessNoteIfSameLife`; the SR-11.4 tick rule in `noteTickReason`) and `clearLivenessNote` (no write for a row with no note; the guarded `ClearLivenessIfSameLife`, no tick). Each takes the snapshot to guard on (the read one, or the adoption's) and returns a `findMissingWrite`; a changed, absent or failed write ticks nothing and lists the row nowhere, and a store error is logged through `logFindMissing`. | The only way a sweep marks, notes or clears a row: every mark, note write and clear (process or tmux evidence, adopted or not) goes through these three writers; never call the guarded store writes, write a tick or deny permission requests for a sweep row another way. |
| `sweep_socket.go` | `sweepSockets.forRow(recorded)`: the socket a sweep row's calls use, the caller's resolved at most once per run for rows that record none, creating nothing; a refused resolution gives tmux unavailable, then not called. It implements SR-3.3's socket rule for a sweep (a recorded socket exactly as recorded; none recorded: the caller's socket, resolved as tmux would through `rowSocket` / `spawn.ResolveQuerySocket`, creating nothing, LFR H1), with SR-3.15's stop after a refusal. | The shared no-socket helper: every sweep (`find-missing`, and `expire` for each row that reaches its lookup) takes a row's socket from one `sweepSockets` per run; never resolve a sweep row's socket another way. |

`missing` is the sweep's judgement on the evidence available to it, not
proof that the agent has exited.

### `expire`

`pkg/api/expire.go` (the verb), `expire_trail.go` (its trail records) and
`internal/store/expire.go` (the candidate read and the conditional delete).
`expire` deletes a finished row only when its agent's recorded process is
not seen running and tmux shows no session of the agent; every other
selected row is kept and reported (SR-12). A `missing` row is
`find-missing`'s judgement on the evidence available to it, not proof that
the agent has exited, and neither `ended` nor `missing` makes a row safe to
delete by itself (SR-18.2).

**Selection (SR-12.1).** The window is `older_than` (`--older-than` on the
CLI) when given, else `defaults.expire_retention_days` days of 24 h; the
cutoff is the injected clock's `now` minus the window. A zero or negative
window selects every finished row with an `ended_at`: the cutoff is then
the fixed `farFutureCutoff` (9999-12-31 23:59:59 UTC) and the clock is not
read.

- **The retention setting** is a whole number of days from 1 to 106751
  (`config.MaxExpireRetentionDays`, the largest whole number of days a
  `time.Duration` holds). A missing key, or 0, gives the default, 31 days
  (`config.DefaultExpireRetentionDays`); `Client.Expire` passes
  `config.Defaults.EffectiveExpireRetentionDays()`, so a default run never
  has a zero or negative window. `config.Load` refuses a negative value
  and one above 106751 exactly as it refuses a `[tmux]` value (see
  [`[tmux]` timing settings](#tmux-timing-settings)), so store-backed verbs
  fail with `ErrConfigMalformed`, and no verb, server or hook runs on it,
  until the file is fixed. `Expire` turns the day count into the window with
  `retentionWindow`, which never wraps: a count above 106751 gives the
  largest duration and one at or below zero gives zero (every finished
  row), cases only an in-process caller of `Expire` reaches.
- **`older_than`.** The CLI and MCP parse it with `ParseOlderThan`
  (`pkg/api/older_than.go`): a Go duration, or decimal digits followed by
  `d` for days. It rejects a value in neither form, a negative Go duration
  and a day count above 106751 (checked digit by digit, so no count wraps,
  however long), and each surface refuses such a value with
  `ErrInvalidFlags` stating `OlderThanForm` before `Expire` runs, so a
  sign slip such as `-2h` or a count such as `365000d` deletes nothing. A
  caller that means every finished row passes `0d` or `0s`.

`ListExpireCandidates(cutoff)` is the one read: rows in `ended` or
`missing` whose `ended_at` is set and older than the cutoff (compared as
stored text in the `storeTimestamp` layout), in instance-id order, each an
`ExpireCandidate` with its recorded session name, SessionStart `pid` +
`proc_starttime`, launch identity and `RowSnapshot`. It decodes no label,
`claude_args` or `extra_env` and parses no timestamp, so a malformed row is
judged like any other. Live rows, `pending` included, and rows with a NULL
`ended_at` are never selected and get no tmux call. A failed candidate read
is logged and fails the verb, with no tmux call; it is the verb's only
failure.

**Per-row order (SR-12.2).** Rows are judged in instance-id order, so the
per-socket stop and the budget's cut-off fall on the same rows on every
run. For each row:

0. The recorded session name, first, in `judgeRow`: `tmux.Unusable` (see
   [Unusable-name guard](#unusable-name-guard-unusablego)) classifies it
   and `unusableKeptReason` maps the kind to the kept reason:
   `empty_session_name`, `control_char_session_name` or
   `rewritten_session_name` (a character tmux stores differently: `.`, `:`
   or bytes that are not valid UTF-8), the first fault in that order. Such
   a row is kept with no process check, no start-time read, no socket
   resolution and no tmux call, so it takes no lookup, stop or budget from
   its socket or the run. It is kept on every
   run, listed in `kept_ids` with one `ad.expire.kept` carrying its reason,
   and gets no `ad.provenance.disagree`. Removing it is a human's decision
   (the README's "Operator actions").
1. The agent process (`tmux.SelectAgentProcess` over the SessionStart
   identity and the recorded pane's, judged once by `tmux.JudgeProcess`).
   Alive with its recorded start time keeps the row `process_alive`, with
   no tmux call. Gone, unreadable or not recorded goes on to the lookup.
2. The lookup. The socket comes from the run's `sweepSockets.forRow` (the
   recorded one, else the caller's, resolved at most once per run and
   creating nothing; a refused resolution keeps its first row
   `tmux_unavailable` and later rows that record none `tmux_skipped`). The
   row's `tmux.Launch` comes from `rowLaunch` and the lookup from the run's
   one [Sweep](#sweep-sweepgo), with the recorded name as the holder name.
   A Skipped result keeps the row `tmux_skipped`.
3. The verdict. Gone goes to the conditional delete. Every other verdict
   keeps the row through `expireKeptReason`, the one mapping: Leftover
   `leftover_running`; Ours `ours` (however recently the row ended); Can't
   tell `tmux_server_changed` (a different server), `provenance_conflict`,
   `tmux_unavailable`, or `cant_tell` (unreadable, and any verdict or kind
   it does not know).

These are the kept reasons as built; the list is not closed. Step 0 keeps
a row before its process check without changing steps 1 to 3.

**The conditional delete (SR-12.3).** `DeleteFinishedIfSameLife(id,
examined)` is one `DELETE` guarded on a finished state
(`finishedStateGuardSQL`) and on the snapshot the candidate read returned
(`snapshotMatchSQL`), so a resume, restore, reuse or hook write after the
read makes it apply nothing. Applied puts the row in `ids`;
`CondChanged` keeps it `changed_since_examined`; `CondAbsent` (another
caller removed it first) puts it in neither list; a store error is logged
with the instance id, keeps the row `store_error`, and the run goes on and
succeeds. Dependent rows go by the schema's foreign-key actions.

**The result (SR-12.4).** `count` / `ids` are the rows deleted; `kept` /
`kept_ids` are the selected rows kept. Both lists are sorted and never null
(`[]` when empty); `count` and `kept` are their lengths. The verb has no
verb-level error: tmux problems and a failed delete of one row never fail
the run.

**Trail (SR-12.5, SR-14).** Once a row's outcome is final (`emitRow`), a
kept row gets exactly one `ad.expire.kept`, and a row whose lookup reported
disagree reasons gets one `ad.provenance.disagree` per distinct reason
(source `ad_expire`, `verb` `expire`); a deleted row and a row another
caller removed get no `ad.expire.kept`. Both are fail-open. See the
[`ad.*` event namespace](#ad-event-namespace) for their fields.

**Budget and skipped rows (SR-13.5, SR-3.15).** The run's tmux time is
capped by `sweep_budget_seconds` (`config.Tmux.EffectiveSweepBudget`),
measured on the injected clock by the Sweep. After an unreadable or
unavailable answer on a socket, no more calls are made there; once the
budget is spent, none at all. Every row that then needs a lookup is kept
`tmux_skipped` and is judged again at the next run. The budget has no safe
minimum: a short one keeps more rows, and a non-positive one makes no tmux
call.

**Cost.** At most one lookup per socket per run, shared by
every row of that socket, and none when no row reaches the lookup. It lists
no panes, writes no adoption, never kills a session, never touches JSONL
transcripts, and reads no process environment.

**Go API (Appendix F.4).**

- `Expire(s ExpireStore, t ExpireTmux, pc ProcChecker, retentionDays int,
  olderThan *time.Duration, sweepBudget time.Duration, now func()
  time.Time, lg ExpireLogger) (ExpireResult, error)` is exported. It never
  reads `time.Now`. It turns `retentionDays` into the window with
  `retentionWindow` (see Selection above).
- `ExpireStore`: `ListExpireCandidates`, `DeleteFinishedIfSameLife` and
  `StoreID`; `*store.Store` satisfies it. `ExpireCandidate` is an alias of
  `store.ExpireCandidate`.
- `ExpireTmux`: `TmuxLookup` only; `TmuxClient`, `*tmux.Client` and
  `tmuxfix.Recorder` satisfy it.
- `Client.Expire(olderThan)` passes the Client's store, tmux client,
  start-time reader, retention setting (`EffectiveExpireRetentionDays`),
  `EffectiveSweepBudget`, clock and logger.
- `ParseOlderThan(s string) (time.Duration, bool)` and `OlderThanForm`
  (`older_than.go`) are exported: the `older_than` parser and the words
  for the form it accepts (see Selection above).

**Must use:** `ListExpireCandidates` is the one read of finished rows for
cleanup and `DeleteFinishedIfSameLife` the one way to remove a finished
row a judgement was made on; never delete finished rows by age alone or
without the examined snapshot. A kept reason is mapped only in
`expireKeptReason` (and the steps before it), never per caller. Every
surface parses `older_than` with `ParseOlderThan` and states
`OlderThanForm` in its refusal, never with a duration parser of its own.
The default window is read only through `EffectiveExpireRetentionDays` and
turned into a duration only by `retentionWindow`; the day limit of both
inputs is `config.MaxExpireRetentionDays`. `OlderThanForm` spells that
limit as the literal `"106751d"`: `TestParseOlderThan` pins the parser's
limit at 106751, and the exact-wording refusal tests
(`TestAdviceFollow_H6_OlderThanDurationForm` on the CLI,
`TestAdviceFollow_I6_OlderThanDurationForm` with `olderThanRefusal` over
MCP) pin the text.

### `delete`

`delete` is a verb of the operator tool only (b.vqr):
`agent-director-admin delete --claude-instance-id <id>...`. No agent
surface has it: the `agent-director` CLI answers `delete` with
`ErrUnknownVerb`, MCP lists no `delete` tool, and the Go and TypeScript
clients have no `delete`. The admin verb calls
`internal/adminapi.Delete`, which `pkg/api`'s `init` (`admin.go`) sets to
the unexported `Client.deleteRows` (`pkg/api/delete.go`). It processes ids
one at a time, returning a per-row map of `{id: "ok" | "<err_name>"}`
(`adminapi.DeleteResult`). The batch never aborts on a partial
failure — every id in the input is attempted; the map records the
outcome.

`delete` bypasses every state-precondition guard. A live-state row
is removed by id exactly the same way a terminal row is. The verb
does NOT touch tmux or JSONL transcripts; the
`permission_requests` row(s) FK-referencing the spawn are removed
by the schema's `ON DELETE CASCADE`.

It is a human's repair tool for a row nothing else removes (a row whose
recorded tmux session name cannot be used, which `resume` and reuse
refuse and `expire` keeps; the README's "Operator actions" gives the
procedure), not a cleanup or recovery step: `expire` removes finished
rows, and a finished id is spawned again with the reuse opt-in
(`--reuse-finished`).

### Cron user invariant

`find-missing` must run as the same user as the agents and in the same
tmux environment (the same `TMUX` / `TMUX_TMPDIR`). A run as another
user, as root or against another tmux server can mark live rows
`missing`: their agent processes may read unreadable, and the lookup
then asks a server that holds none of their sessions and answers Gone.
Such a mark is the sweep's judgement on the evidence available to it,
not proof that the agent has exited. The consequences for a caller
(`kill`'s success on a finished row is not verification; on the wrong
tmux server, a wrongly marked row, `kill`'s no-op success and a reuse
together start a second agent for the same id) are in
[Same environment](#same-environment). The full story is in
[Degraded-mode reconciliation + cron user](#degraded-mode-reconciliation--cron-user).

`expire` has the same requirement: it must run as the agents' user and in
their tmux environment. A run as another user, as root or against another
tmux server can wrongly delete rows whose agent still runs: the agent
processes may read unreadable, and a row recorded without a socket is
looked up in the caller's tmux environment, whose server holds none of the
agent's sessions and answers Gone. A deleted row cannot be resumed.

The operator tool's `delete` is a pure DB operation and doesn't depend on
probe permissions; running it as the wrong user is harmless (it just
operates on whatever rows the DB happens to hold).

The recommended operator setup is a systemd user timer or a personal
crontab of the agents' user, not a system-level cron, so the sweep's
identity and tmux environment match the ones that launched the agents.
agent-director installs no schedule itself.

### Reboot-recovery runbook

After a machine reboot, every Spawn's process and tmux server are gone,
but the rows persist in `state.db` frozen at their pre-reboot
live state (`waiting`/`working`). Recovering a Spawn's conversation is a
**two-verb contract, in order**:

1. **`find-missing`** — reconciles the frozen rows against reality. After
   a reboot every recorded agent process is gone, so the sweep marks those
   rows `missing` (a terminal state) on process evidence (`proc_absent`);
   a row with no process evidence is marked when its socket's lookup finds
   no session of its current launch (`tmux_absent`, or `tmux_name_held`
   when another session now holds its name). `missing` is the
   sweep's judgement on the evidence available to it, not proof that the
   agent has exited. This step is
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
   rotated when its agent restarted — recover across a reboot. Each
   resumed row shows `pending`, keeping its session id and history,
   until its agent reports in (SessionStart), then `waiting`.

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
  user turn. There is genuinely nothing to resume, and the finished row
  cannot be messaged (`send-keys` refuses it). Recovery: spawn again with
  the same id, opting in to reuse (`--reuse-finished`); the new life starts
  with no memory of the old one.
- `ErrJsonlMissing` — the persisted path was recorded and has since rotted, or
  the visible history is non-empty and none of its transcripts exists. The
  paths it names all come from the current life. Recovery: spawn again with
  the same id, opting in to reuse (`--reuse-finished`); the new life starts
  with no memory of the old conversation, which cannot be resumed through
  agent-director afterwards.

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
and need no repair. The repair applies only to a row that has never been
reused (its current life is its first, `life_number` 0): re-pointing a
reused row at a pre-v4 orphan would bring an earlier life's conversation
into the current life, so the skill stops for such a row, and its SQL is
guarded on `life_number = 0`.

**Recovery is `find-missing` → `resume`, or a reuse.** `find-missing`
then `resume` brings the conversation back. When `resume` cannot
(`ErrNoSessionId`, `ErrJsonlNeverWritten`, `ErrJsonlMissing`), the
recovery is to spawn the id again, opting in to reuse
(`--reuse-finished`): the agent starts a new life with no memory of the
old conversation. Callers cannot delete a row (`delete` is on the
operator tool only), and a human's `delete` is not a recovery step: it
removes the row along with its `claude_session_id`, labels and
`extra_env`. (A resume that fails with
`ErrJsonlMissing` names every transcript path it tried and its source,
so the operator can diagnose *why* before deciding anything.)

**Autostart is the CALLER's responsibility.** agent-director does not
watch for reboots and does not schedule anything itself. Its recovery
contract **starts at** "the caller invokes `find-missing` then
`resume`". Whatever triggers that sequence after a boot — a systemd
unit, a startup script, a `find-missing` cron loop — is owned and
operated by the caller, not by agent-director. Before giving up on a
conversation after a failed resume, check `get`'s `transcript_status` and
`prior_sessions`: `rotated` means the current life's visible history is
non-empty, and a plain `resume` already walks the current life's archived
sessions (minus the row's current session id); nothing from an earlier
life is ever reattached. `never_written` means no transcript was written
in the current life, so there is nothing to resume, and the recourse is a
reuse.

## Caller contract: tmux refusal classes

This is the one written contract for the programs and agents that call
agent-director's tmux-backed verbs (SRD SR-18.1). The README summarises it
and links here. Each verb's manifest description states the class of every
tmux error it can return; this section says what each class means and what
a caller does about it. Each subsection is a table or a list, so a rule
added later goes in as a row or a bullet under the subsection it belongs
to. Where a human is
needed, the contract points to the README's "Operator actions" section and
names no command for a caller to run. Its limits are listed under
[Known limitations](#known-limitations), and what a caller must now
assume differently from earlier releases under
[Meaning changes](#meaning-changes).

### Classes

The seven tmux error names and their classes are final (SR-1.1). The
"Returned by" column lists the verbs that return each name today; each
verb's manifest `ErrorNames` is the authority, and a verb that gains a name
adds it here.

| Name | Class | Returned by | What the caller does |
| --- | --- | --- | --- |
| `ErrTmuxSendKeys` | GONE | `send-keys`, `pause` | Only that the row's session is not there: no session of this agent was found (nothing sent, or, when the Enter failed after the text, the text may be typed but not submitted). The row stays live until `find-missing` marks it; to relaunch the id, follow the [live-row sequence](#live-row-sequence). |
| `ErrTmuxCaptureFailed` | GONE | `read-pane` | As `ErrTmuxSendKeys`: only that the row's session is not there; nothing was read. |
| `ErrTmuxUnresponsive` | UNAVAILABLE (transient) | `kill`, `read-pane`, `send-keys`, `pause`, `resume`, `spawn` | tmux did not answer usably. Retry later with backoff and a retry cap, and alert when the cap is reached. The description says whether anything was sent first (for `kill`: "the kill was sent and may or may not have taken effect"; for `send-keys` and `pause` after a timed-out text or Enter call, or `pause`'s `C-u`: "the keys may have been delivered"). Where `send-keys`' text may be in the pane, its description ends with the next step in place of "retry later", and the caller follows that step instead of sending the text again: after a timed-out text call, `read-pane`, then `send-keys` with empty text (Enter only) if the text is typed, otherwise the same `send-keys`; after a failed or timed-out Enter, `send-keys` with empty text, which submits it. `pause` keeps "retry later": a retried `pause` sends `C-u` before typing `/exit` (see [`pause`](#pause)). For `resume` it also means the row's own session or agent "appears to still be stopping" or "appears to still be starting" (the starting-session bullet under [Reading a refusal](#reading-a-refusal)), or that more than one session's name matches the recorded name. For `spawn` with the reuse opt-in it means the same at the old-row lookup (the row's own session or agent still stopping or starting, an unreadable answer, or more than one session's name matching the requested name; nothing was changed, retry later), the same cases at the re-lookup after "duplicate session" (then the row is restored), or a launch timeout after the reset (the row was reset and stays `pending`; the launch-timeout bullet under [Retrying](#retrying)). |
| `ErrTmuxKillFailed` | UNAVAILABLE | `kill` only | The agent process still runs after `kill`, cannot be checked while its labelled session is still there, or runs while no session or pane of the launch was found (no kill sent). The description says which. Retry `kill` later with backoff and a cap; alert when the cap is reached; never delete the row. |
| `ErrTmuxSessionConflict` | CONFLICT (permanent until a human looks) | `kill`, `read-pane`, `send-keys`, `pause`, `resume`, `spawn` | The session found is not this launch's session, or tmux holds conflicting labels. For the pane verbs it also means the agent's pane was not found in the session carrying this row's id, or (for `read-pane`) more than one leftover session exists. For a plain `spawn` it also means the requested name is held by another session (see the held-name bullet under [Reading a refusal](#reading-a-refusal)). For `resume` it means a session is in the way of the relaunch: one left over from an earlier life, one holding the recorded name, or the row's own session (the own-id conflict bullet under [Reading a refusal](#reading-a-refusal)). For `spawn` with the reuse opt-in it means the same before anything is changed: a session left over from an earlier life of the id, the row's own old session ("this row's own id"), conflicting labels, or the requested name held by another row's session, another agent-director store's session or one with no valid instance id; after "duplicate session", the same cases for the holder of the requested name, then the row is restored. Another row's or another store's session is another agent and is never ended. Stop, surface the named session to a human (README "Operator actions") and never end it yourself; retrying changes nothing until a human has acted. |
| `ErrTmuxNotAvailable` | ENVIRONMENT | `kill`, `read-pane`, `send-keys`, `pause`, `resume`, `spawn` (with or without the reuse opt-in); never `find-missing` or `expire` | tmux could not be run, its socket is not accessible to this user, or this is not the tmux server the agent was launched on. For `spawn` with the reuse opt-in: at the old-row lookup, before anything is changed (a different server, tmux unavailable, an unusable socket directory), or, after the reset, at session creation or the re-lookup after "duplicate session", then the row is restored. An environment problem for an operator to fix; alert, and never read it as gone. |
| `ErrTmuxSessionCreate` | LAUNCH FAILURE | `spawn` (with or without the reuse opt-in), `resume` | The launch's session could not be created or labelled. The description says what became of the row (a plain spawn's new row stays `pending`, or, when the create found the name held but its holder was gone by the re-lookup, is ended; a `resume` and a reuse restore their rows: after the reset, a failed create, a session that could not be labelled, or a holder gone by the re-lookup after "duplicate session"); follow it. A name found held by a session that is still there is never this class: `resume` and reuse return it only for a launch failure or for a holder their re-lookup after "duplicate session" did not find. That is usually a holder that vanished first, but a holder still there can also go unmatched (a name with `$` or `\`, which stays usable), so this class does not prove the name is free. |

### Reading a refusal

- Only GONE means the row's session is not there. UNAVAILABLE, CONFLICT
  and ENVIRONMENT never mean "the agent is dead", and neither does any
  other agent-director error: an error means "don't know".
- For `kill`, GONE is success: when the lookup finds no session of the
  launch, the agent process decides (see [`kill`'s promises](#kills-promises)),
  and `kill` returns no GONE error.
- A caller cannot delete a row: no agent surface has `delete`. A human
  never removes a row after a `kill` that did not succeed either.
- A plain `spawn` whose session-creating call reports the requested name
  already held ends its new row at once (`ended`) and returns the
  classified error naming the blocking session: its tmux id and whether
  its label names this instance id. No caller waits for `find-missing`.
  When the holder is a session left over from an earlier life of this id,
  a human ends it (README "Operator actions"), and the id is then spawned
  again with the reuse opt-in (`--reuse-finished`). Another row's
  session, and another agent-director store's, is never ended. The
  description says what became of the new row: ended, left as it is
  because it changed after the spawn inserted it, or still `pending`
  after a store error. When the re-lookup could not answer
  (`ErrTmuxUnresponsive`) or the holder had vanished
  (`ErrTmuxSessionCreate`), it also names the instance id ("instance
  <id>", a minted one included) and the retry of that id: the reuse
  opt-in once the name is free, and, when the row was not ended, only
  after `get` shows it `ended` or `missing`; a plain spawn of the id
  collides with the finished row.
- The own-id conflict: `ErrTmuxSessionConflict` "this row's own id" means
  the row's own session, at least the starting-session bound old, on a
  row that ended at least the stopping window ago (or the row's agent
  process still running with no session of its launch, past the stopping
  window). The agent may be hung or running on a row wrongly marked
  finished. An automated caller stops, surfaces the named session to a
  human and never ends it itself; how a human proceeds is only in the
  README's "Operator actions" section.
- The starting-session rule decides when a finished row's own session, or
  its still-running agent process, blocks a relaunch (SR-4.2). The
  stopping window is checked first: a row that ended less than the
  stopping window ago gives `ErrTmuxUnresponsive` "appears to still be
  stopping". Then the starting-session bound: a session younger than it
  gives `ErrTmuxUnresponsive` "appears to still be starting". Both are
  UNAVAILABLE: retry later. Past both, the own-id conflict above
  (CONFLICT). The stopping window is 90 s by default
  (`stopping_window_seconds`, safe minimum 30 s) and the starting-session
  bound 300 s by default (`starting_session_seconds`, safe minimum 60 s);
  each is a default an operator may change, but not below its minimum
  (see [`[tmux]` timing settings](#tmux-timing-settings)). `resume` and
  `spawn` with the reuse opt-in apply the rule, each checking the stopping
  window first on the `ended_at` it examined before its move or reset. How
  `resume` applies it is in [Resume](#the-starting-session-rule-as-resume-applies-it),
  and reuse in [Reuse of a finished id](#reuse-of-a-finished-id).
- The rule reads the host clock, which agent-director and the tmux server
  share. A forward step of the host clock can bring the own-id conflict
  early or end the stopping window early; a backward step delays both.
- A `resume` or reuse right after `pause` returns or after an agent's
  natural exit can get "appears to still be stopping" while the agent's
  process exits, and a `resume` right after the last session on its tmux server ended can
  get `ErrTmuxNotAvailable` ("not the tmux server the agent was launched
  on") while that server exits. In both cases wait and retry. The
  per-case recourse for a blocked `resume` is in
  [Blocked resume](#blocked-resume).
- Feature detection: a caller learns whether the reuse opt-in exists from
  the version of the binary that serves it, a release candidate
  `X.Y.Z-rc.N` counting as `X.Y.Z`; `version` carries no capability list.
  The source per surface, and what development builds report, are in
  [Reuse of a finished id](#reuse-of-a-finished-id). Against an older
  binary the opt-in gives `ErrInvalidFlags` on the CLI and in the
  TypeScript client, and over MCP it is silently ignored, so a finished
  row gives `ErrInstanceIdCollision`; so does an MCP `serve` process of
  0.11.0-rc.1, which counts as 0.11.0 but knows the opt-in only as
  `reuse-finished`.
- `ErrConfigMalformed` from a store-backed verb means agent-director cannot
  use its config file: it does not parse, a `[tmux]` timing setting is
  negative or below its safe minimum (see
  [`[tmux]` timing settings](#tmux-timing-settings)), or
  `[defaults] expire_retention_days` is negative or above 106751 (see
  [`expire`](#expire)). Every store-backed
  call fails until an operator fixes the file. A caller takes no action,
  alerts once and never reads it as "dead".

### Retrying

- Re-issuing a refused operation later is how a caller learns that a
  condition has cleared.
- A refusal returned before the verb's first write or tmux action changes
  nothing, so re-issuing it is always safe. A `kill` refused before its
  kill call sends nothing: a leftover, a lookup or pane listing that cannot
  decide, an unusable recorded name or an unusable socket directory. A
  pane verb refused before its action sends and reads nothing.
- A refusal returned after a write or an action says what may have
  happened, and its own rule applies: `ErrTmuxKillFailed` says whether a
  kill was sent, and `ErrTmuxUnresponsive` or `ErrTmuxNotAvailable` from
  the follow-up lookup after a sent kill says the kill may or may not have
  taken effect.
- A `send-keys` whose text may be typed in the pane is never re-sent as
  it was: that would type the text again after the unsubmitted copy, and
  the agent would get it twice. Its `ErrTmuxUnresponsive` names the step
  instead (`read-pane`, and `send-keys` with empty text, which sends Enter
  only; see [`send-keys`](#send-keys)). A `pause` whose keys calls ended in
  `ErrTmuxUnresponsive` is retried later as it is: it sends `C-u` before
  typing `/exit` (see [`pause`](#pause)).
- The launch-timeout rule: after a timed-out spawn, reuse or `resume`
  (`ErrTmuxUnresponsive`, "the session may have been created") the row
  stays `pending` (after a reuse, in its new life: "the row was reset");
  do not retry until `get` shows the row `ended` or `missing`. A retried
  plain spawn without an explicit id would start a second agent, and a
  retried `resume` or reuse of the `pending` row is refused and changes
  nothing. Once the row is `ended` or `missing`, a plain spawn of an
  explicit id is retried with the reuse opt-in, since a plain spawn of
  the id collides with the finished row (`ErrInstanceIdCollision`); its
  timeout description says so.
- A caller retries no more often than it needs to; for UNAVAILABLE, with
  backoff and a cap, and it alerts when the cap is reached.

### Liveness authority

- The row state, kept honest by `find-missing`, is the liveness authority.
  There is no liveness verb, and `get`, `status` and `list` read no tmux.
- `read-pane` changes nothing: no keys, no session created, killed or
  renamed, no store write (`row_version` and the snapshot are unchanged)
  and no trail event. Its results are the lookup classes of the row's own
  session only: the pane (the current launch's agent pane, or the one
  leftover's), GONE (`ErrTmuxCaptureFailed`: no session of this agent is
  there), CONFLICT (the agent's pane not found, or more than one
  leftover), and UNAVAILABLE or ENVIRONMENT, which give no information.
  It is not a liveness verb: the row state stays the authority (see
  [`read-pane`](#read-pane)).
- `ended` and `missing` are not proof that the agent exited: `missing` is
  the sweep's judgement on the evidence available to it.
- Only the row's own agent moves the row: a hook applies only when its
  parent process is the row's recorded pane process (see
  [Hooks move a row only for its own agent](#hooks-move-a-row-only-for-its-own-agent)).
  A process that merely carries the row's id, such as a leftover of an
  earlier life or a nested `claude`, never ends, revives or takes over the
  row.
- The pending grace period is 60 s by default and configurable
  (`pending_grace_seconds`), measured from the launch start that `status`,
  `get` and `list` show. `find-missing` never judges a `pending` row inside
  it: the row is left as it is and is in neither result list, so a caller
  waiting on a launch reads a `pending` row inside its grace period as
  "wait and check again later", never as a reason to escalate. A `pending`
  row with no launch start counts as past the grace period. The sweeps'
  per-run tmux time budget is 15 s by default and configurable
  (`sweep_budget_seconds`): once a `find-missing` run has spent it waiting
  on tmux, the run makes no more tmux calls, and every row still needing a
  lookup is left unverified and looked at again by the next run. The run
  still succeeds. A run spends at most the budget plus one tmux call on
  tmux (16.6 s at the defaults), and the stated worst-case times hold at
  the defaults (see [`[tmux]` timing settings](#tmux-timing-settings)).
- A caller that finds no row for an id spawns it and lets agent-director
  classify a held name; it does not look for a holding session itself.
- `find-missing` judges a live row only by its agent process. A dead
  process is marked `missing` whatever tmux shows, so a session kept by
  `remain-on-exit` or a respawned pane never keeps the row live; a session
  existing never does. A process that reads alive with its recorded start
  time keeps the row live whatever tmux shows. tmux is asked only when the
  process cannot be checked (unreadable, a pid-only identity that reads
  alive, or no process recorded), on the row's recorded socket.
- Past the pending grace period, `find-missing` marks a `pending` row
  `missing` when its agent process is dead or unrecorded and no session
  carries its current label, whatever holds its name, and never while one
  does. A session with a valid label of another agent-director store never
  carries the row's label: it counts as no session for the row, and the
  sweep never touches it. One exception marks a row beside its own
  session: a launch whose create reply was lost (no pane recorded) whose
  session has no pane carrying the launch's pane label counts as gone.
  When a session holds the row's recorded name, the sweep writes one
  `ad.launch.name_held` record naming it; that session keeps running until
  a human ends it (README "Operator actions").
- So `find-missing` heals a launch that never reports in and has no
  session: past the grace period its row is marked `missing`.
- `find-missing` returns none of the seven tmux errors, and no tmux answer
  fails the sweep; its only error is a failure to read the store. When it
  must ask tmux, the answer decides per row: the row's session is not there
  (GONE: gone, or only an earlier launch's session) marks the row
  `missing`; conflicting labels or a scope value (CONFLICT) leave it
  unverified with `liveness_note` `provenance_conflict`; a different tmux
  server leaves it unverified with `tmux_server_changed`; tmux not
  answering usably, unavailable or refusing the socket (UNAVAILABLE,
  ENVIRONMENT) leaves it unverified with `probe_eacces` or
  `process_not_seen_tmux_unchecked`, and the run makes no more calls on
  that socket. An unverified row stays live and is judged again on the
  next run.
- A plain `spawn` that created a new row and is refused with
  `ErrTmuxNotAvailable` at session creation leaves that row `pending` (a
  reuse and a `resume` restore their rows instead). While tmux stays
  unavailable, `find-missing` cannot mark the row; it stays unverified. So
  a retry with the reuse opt-in returns `ErrInstanceIdCollision` until
  tmux is back and a `find-missing` run past the pending grace period
  marks the row `missing`.

### Launch pending

- `pending` means a launch (spawn, reuse or resume) is in progress and the
  agent has not reported in; a resumed `pending` row keeps its session id
  and history. The launch start, shown by `status`, `get` and `list`, lets
  a caller bound how long it waits. A caller that polls a launch polls `status`,
  and calls `read-pane` only while the row is `pending`, at most about
  once a second, backing off once the launch start is old and stopping as
  soon as the row leaves `pending`: each `read-pane` makes up to three
  tmux calls.
- `send-keys` with `allow_pending` reaches only the current launch's
  session, found by its label with the row's launch token. A session left
  over from an earlier launch is refused with `ErrSpawnNotInteractive`
  ("not this launch's session") and nothing is sent.
- A `pending` row with no launch start or no launch token has no current
  label and fails closed for actions: `send-keys` refuses it with
  `ErrSpawnNotInteractive` before any tmux call.
- When a caller types into a launching session, agent-director
  guarantees only that the keys reach the current launch's agent pane, not
  that the prompt the caller saw is still showing: the agent can report in
  between the caller's `read-pane` and its `send-keys`, or between the text
  and the Enter, and the text is then submitted as a user turn. Whether
  and what to answer is the caller's business (see
  [`send-keys`](#send-keys)).
- Every successful launch reports `pre_trust` (`ok`, `skipped` or
  `failed`). agent-director never pre-trusts over an opt-out: a spawn,
  reuse included, follows its own call's `no_pre_trust`, and `resume`
  follows the choice recorded by the spawn or reuse that began the row's
  life; a row from before this release has pre-trust allowed, the
  column's default (see [Workspace-trust pre-write](#workspace-trust-pre-write)).
  A result without `pre_trust` comes from a binary older than this
  release.
- A reused id starts with no memory of its earlier lives: `resume` and
  `get` see only the current life's history, so after a reuse the earlier
  conversation cannot be resumed through agent-director (SR-8.7).

### The lookup rule

- A row's session is the one carrying its `@ad_owner` label with the row's
  launch token and this store's id, found on the row's recorded socket
  wherever it is and whatever its name. The lookup checks the scope values,
  reads tmux as UTF-8 and targets sessions and panes by id (see
  [Shared tmux lookup](#shared-tmux-lookup)).
- The lookup gives one of four verdicts: Ours (the current launch's
  session), Leftover (only sessions labelled by an earlier launch of this
  id), Gone (no session carries a valid label of this store for this id)
  or Can't tell (unreadable,
  a different server, conflicting labels, or tmux unavailable). Each
  verb's table maps them to its result.
- `AGENT_DIRECTOR_INSTANCE_ID` in an agent's environment only tells the
  agent who it is. agent-director never reads it, or any other
  `AGENT_DIRECTOR_*` value, as evidence of whose a session or process is.
- Every call for a row goes to its recorded socket. A row from before this
  release, which records none, uses the caller's socket resolved as tmux
  resolves it.
- A different tmux server at that socket gives `ErrTmuxNotAvailable`.
  Conflicting labels (two sessions with the launch's label, or an
  `@ad_owner` value at the global, server or global-window scope) give
  `ErrTmuxSessionConflict`.
- "No tmux server running" at the row's socket counts as gone only when the
  recorded server process is gone (or the row records no server);
  otherwise it is a different server.
- The lookup reads the answering server's identity even when the server
  has no sessions (tmux's `exit-empty` off), so the recorded server with
  no sessions counts as gone, never as a different server (LFR H5; b.47f).
- A permission refusal on the socket is `ErrTmuxNotAvailable`
  (ENVIRONMENT), never gone.
- Liveness comes only from the agent process, never from a session
  existing.
- A session carrying a valid label of another agent-director store (its
  last field is not this store's id) is never this row's session, never
  Ours or Leftover, and counts toward Gone for the row. No verb acts on it,
  so several stores may share one tmux server.
- The threat model is accidents only: a same-user process that sets,
  copies or removes a label on purpose is out of scope.

### `kill`'s promises

- `kill` succeeds only once the agent process is gone, and returns
  `ErrTmuxKillFailed` otherwise. `kill_sent` says whether a kill was sent.
- It needs the agent process, and every pane process of the labelled
  session that its one pane listing showed and whose start time it could
  read (a teammate's split pane, for example), to be gone.
  `ErrTmuxKillFailed` names each survivor's pid, as does the
  `survivor_pids` field of the call's `ad.kill.called` event. A listed pane
  process whose start time could not be read is not waited for.
- The contract is per call. A survivor named by an earlier failure is not
  the row's agent and is not tracked: a retried `kill` finds the lookup
  Gone and checks only the agent process, so once the agent is gone it
  succeeds with `kill_sent` false whether or not the survivor still runs.
  A retry's success means only that the agent is gone. Ending a survivor is
  the README's "Operator actions" procedure.
- The promise covers the Claude process, not processes it detached.
  `kill` never signals a process itself and never changes the row's state.
- If the row finished while `kill` waited, a retried `kill` is a
  finished-row no-op, and the row then follows the finished-row rules:
  `expire` keeps it while its agent process runs.
- A repeated `kill` on a row whose session and agent process are already
  gone succeeds once the tmux server has finished exiting (SR-6.1,
  SR-18.12; decision-0930b Q5). Right after a `kill` ends the last
  session on its tmux server, a repeated `kill` or any lookup on that
  socket can get `ErrTmuxUnresponsive` or `ErrTmuxNotAvailable` while the
  server exits (`exit-empty`, tmux's default). An exiting server that no
  longer answers is never read as Gone while its process runs: its
  no-server reply does not tell it from a live server or a re-bound socket,
  so Gone on that reply still needs the recorded server process gone
  (SR-3.3). One that still answers the lookup names itself in the lookup's
  identity line, so the recorded server's empty answer is Gone (LFR H5;
  b.47f). The caller waits and checks
  again; the live-row sequence's pacing already does. `kill`'s manifest
  description and `Client.Kill`'s Go doc state this limitation.
- On a `pending` row, `kill` ends only the current launch's session and
  refuses a session left over from an earlier launch with
  `ErrTmuxSessionConflict`, so a leftover is ended only by a human (README
  "Operator actions").
- `kill` on a `pending` row is the way to abort a stuck launch. A `kill`
  made before the launch created its session returns `kill_sent` false and
  does not stop the launch; the live-row sequence's re-check covers it.
- `kill` never ends another agent-director store's session.

### Same environment

- A caller must run as the same user and in the same tmux environment as
  the agents. Two consequences:
  - `kill`'s success on a finished row is not verification that the agent
    exited;
  - on the wrong tmux server, a row wrongly marked `missing`, `kill`'s
    no-op success and a reuse together start a second agent for the same
    id.
- The agent-visible texts state the two consequences through one
  constant, `sameEnvConsequences` in `pkg/api/manifest/manifest.go`: the
  `kill` and `find-missing` descriptions and spawn's `reuse_finished`
  parameter text carry it, so the three sites stay identical. `expire`
  states its own wrong-server sentence instead. A new site uses the
  constant (see [the verb registry](#pkgapimanifest--verb-registry)).
- This covers `find-missing`: it must run as the agents' user in their
  tmux environment. A run as another user, as root or against another tmux
  server can mark live rows `missing`, because their processes may read
  unreadable and the other server answers that their sessions are gone
  (see [Cron user invariant](#cron-user-invariant)).

### Live-row sequence

To end a live row (`pending` included) and relaunch its id, a caller
follows this bounded, paced sequence (SR-18.6). Where it is stated
(decision-0930b Q6):

- `kill`'s manifest description states it in a short form: the same six
  steps and limits, without the rationale. The one constant
  `liveRowSequence` in `pkg/api/manifest/manifest.go` holds it.
- `find-missing`'s and `spawn`'s descriptions end with one sentence that
  points to it (constant `liveRowSequencePointer`): "To end a live row
  (pending included) and relaunch its id, follow the live-row sequence in
  kill's description." Every surface that shows one description shows all
  three (`help`, MCP `tools/list`, the generated references), so the
  pointer always resolves.
- The README's "Caller contract" and this section state it in full, with
  the rationale of steps 2 and 6.

The short form is kept out of the other descriptions because `help` goes
into every agent's context and MCP sends every description each turn; the
[help size guard](#pkgapimanifest--verb-registry) keeps it from growing
back unnoticed.

1. `kill`, and check the result; on any error follow its class.
2. If the row is `pending`, wait until its launch start (shown by
   `status`) plus the pending grace period (60 s unless the operator
   configured another value) has passed. A `pending` row inside its grace
   period means wait and check again later, never escalate.
3. Run `find-missing`, then confirm with `status` or `get` that the row is
   `ended` or `missing`; if not, wait about 5 s and repeat, up to three
   `find-missing` runs in all.
4. If still live, `kill` once more, wait about 5 s, run `find-missing` once
   more and check.
5. If still live, stop and escalate to a human.
6. Once the row is `ended` or `missing`, `resume` it if it has a session id
   and the caller wants the conversation back; otherwise spawn with
   `reuse_finished` (`--reuse-finished` on the CLI; the short form names
   it through `ReuseOptInSpelling`). A caller whose ids agent-director mints spawns fresh
   instead of reusing (OFR 6). A `pending` row, a resumed one included,
   enters this sequence, so a stuck `resume` handled this way can still get
   its conversation back; a reuse makes that conversation unreachable for
   good, because a row's session history belongs to one life and a reuse
   starts a new one (SR-8.7).

## Known limitations

The limits and accepted risks of this release that callers and operators
should know (SRD SR-18.12); each is one bullet in this list, with a link to
the section that describes it in detail.

- **Accidents only** (SR-3.1). A same-user process that deliberately sets,
  copies or removes `@ad_owner`, or renames, re-links or respawns an
  agent's session to fool agent-director, is not defended against. A
  session renamed by accident is still found by its label (see
  [The lookup rule](#the-lookup-rule)).
- **Inherited `AGENT_DIRECTOR_*` values** (SR-3.12). An agent launched on a
  tmux server that something other than agent-director started from inside
  another agent's pane can inherit that agent's other `AGENT_DIRECTOR_*`
  values (its labels, for example) through the server's global
  environment. This affects only the agent's self-identification, never
  provenance: agent-director reads no such variable as evidence.
- **Ids with control characters** (SR-3.13). A row whose instance id
  contains a control character can never carry a valid label, so its
  session is never Ours (see the label classes under
  [Shared tmux lookup](#shared-tmux-lookup)).
- **Rows whose recorded tmux session names cannot be used** (SR-3.2,
  SR-18.12). A recorded name that is empty, contains a control character,
  or contains a character tmux stores differently (`.`, `:` or bytes that
  are not valid UTF-8) can never be matched to its session; the
  [unusable-name guard](#unusable-name-guard-unusablego) classifies it.
  Every verb that would look such a row up returns `ErrInternal` with no
  tmux call, [`find-missing`](#find-missing) gives the row a note of its
  own when its process cannot decide it, and [`expire`](#expire) keeps it
  on every run with a reason of its own. Besides hand-edited rows, this
  covers rows created between 2026-05-18 and the b.gqe fix of 2026-05-27
  (commit `dab3a80`) whose default names took a `.` from their id.
  agent-director never touches such a row's session; a human removes the
  row by the procedure in the README's "Operator actions" section, whose
  last step is `agent-director-admin`'s `delete`.
- **Names with `$` or `\`** (SR-3.5, SR-3.10). New explicit names may not
  contain them, but existing rows' names may. Such a session is labelled
  by its session id when relaunched, never by a name target (see the create
  under [Launch identity](#launch-identity)). A name that matches none of
  its stored forms counts as not held (see the name holder under
  [Shared tmux lookup](#shared-tmux-lookup)).
- **Other tmux versions.** tmux versions other than 3.2a and 3.3a may
  store `$` differently (observed on 3.4 and 3.5a). The name then matches
  none of its stored forms and counts as not held, which fails safe: the
  create decides.
- **The sweeps' tmux time budget** (SR-13.5). `find-missing` and `expire`
  stop calling tmux once the per-run budget (`sweep_budget_seconds`, 15 s
  by default) is spent. While tmux stays that slow, rows beyond the budget
  stay unverified or kept (see [`find-missing`](#find-missing) and
  [`expire`](#expire)).
- **The plain spawn's label scan** sees only the caller's socket. It
  misses a leftover on another server or socket, an unlabelled one (from
  before this release) and a process that carries the id but has no
  labelled session; the hook gate keeps their hooks off the row (see the
  label scan under [Launch identity](#launch-identity)).
- **`pause` clears one input line.** Before typing `/exit`, `pause` deletes
  only from the agent's cursor back to the start of its line (`C-u`). The
  earlier lines of a multi-line text a failed `send-keys` left unsubmitted
  stay, so `/exit` is submitted with them as a prompt and the agent keeps
  running (see [`pause`](#pause)).
- **Accepted risks.**
  - A re-bound or different tmux server gives Can't tell
    (`ErrTmuxNotAvailable`; `find-missing` note `tmux_server_changed`).
    Every call for a row goes to its recorded socket, so only a plain
    spawn and a row from before this release depend on the caller's tmux
    environment (see [The lookup rule](#the-lookup-rule)).
  - Behind a permission wall, an agent whose window lives on in another
    session after its own labelled session ended reads as Gone and can be
    marked `missing`; `kill` of such a live row succeeds with nothing sent
    (see [`kill`](#kill)).
  - A label step and the kill of the unlabelled session that both fail
    leave an unlabelled session of agent-director's that may run, and so
    does a create for a name containing `$` or `\` whose reply exits 0 but
    does not parse.
  - A window- or pane-scope `@ad_owner` value, which only a deliberate
    `set-option` sets, can mask a real label, so the row reads Gone.
  - `kill` promises that the Claude process has exited, not processes it
    detached (see [`kill`'s promises](#kills-promises)).
  - After a `kill` whose wait timed out while SessionEnd finished the row,
    a retried `kill` is a no-op on the finished row.
  - Orphans that already carry a row's id (from before this release, from
    a wrong-server mistake, or a leftover beside a row a plain spawn ended)
    no longer drive that row through their hooks (see
    [Hooks move a row only for its own agent](#hooks-move-a-row-only-for-its-own-agent)).
    Finished rows whose own session keeps running are kept by `expire`
    indefinitely and reported.
  - `agent-director-admin`'s `delete` racing a spawn, reuse or `resume`.
  - The operator tool is off PATH, not locked: agents run as the same
    user, so one can find `~/.agent-director/admin/agent-director-admin`
    and run it by full path (or open `state.db` with `sqlite3`). Its
    human-approval statement forbids that; nothing enforces it (see
    [Operator tool `agent-director-admin`](#operator-tool-agent-director-admin)).
  - Identifier reuse across a tmux server restart.
  - A launch suspended for longer than the pending grace period between
    recording its launch start and its session-creating call, or during
    which the host clock steps forward by at least that period.
  - An agent slower to exit than the stopping window: a `resume` or reuse
    after the window, whose session is at least the starting-session bound
    old, gets `ErrTmuxSessionConflict`. One example is an agent whose
    SessionEnd hook budget is raised past half the stopping window by
    `CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS`, which has no stated cap and
    can come from the host environment, a settings `env` block or a
    spawn's `extra_env` (agent-director reserves only
    `AGENT_DIRECTOR_`-prefixed keys there). An operator covers it by
    keeping the window at least twice the budget, as the README's
    [Timing settings](../README.md#timing-settings-tmux) say (see also
    [`[tmux]` timing settings](#tmux-timing-settings)).
  - A server-scope `@ad_pane` value, which only a deliberate
    `set-option -s` sets, hides every pane's own value, so a lost reply's
    pane is not adopted and its row can be marked `missing` past its grace
    period while its agent runs.
  - A run as another user on a host whose `/proc` is mounted with
    `hidepid=invisible` reads every agent process as gone, which the
    same-user rule already rules out (see [Same environment](#same-environment)).
  - A process whose main thread has exited while its other threads run
    shows state `Z` and reads as gone (neither tmux nor Claude Code does
    this).
  - In the moments after a `kill` ends the last session on its tmux
    server, a repeated `kill` or any lookup on that socket can get
    `ErrTmuxNotAvailable` or `ErrTmuxUnresponsive` while the server exits;
    the caller waits and checks again.
  - tmux replies on macOS are not verified.
- **The hook gate's residuals** (SR-22.9; see the residuals under
  [Hooks move a row only for its own agent](#hooks-move-a-row-only-for-its-own-agent)).
  A hook applies only when its parent process is the row's recorded pane
  process. So:
  - a nested `claude`'s and a teammate pane's hooks are ignored, and their
    work is not reflected in the row;
  - a row whose create reply was lost takes no hook, and stays `pending`,
    until `kill`, `send-keys`, `pause` or `find-missing` adopts its pane by
    `@ad_pane`; after the adoption an idle agent's row stays `pending`
    until the agent's next hook;
  - a SessionStart that arrives before its launch's identity write waits
    for it, bounded by the pending grace period, so only an identity write
    slower than that period leaves a row `pending` the same way. The wait
    also ends 540 s after it began, so with a grace period above 540 s the
    hook stops waiting while `find-missing` still counts the row as inside
    its grace period; the row stays `pending` until adoption or the
    agent's next hook, with its `no_pane_recorded` record;
  - a Claude Code version that does not run exec-form hooks applies no
    hook: each hook writes `ad.hook.ignored` `no_exec_form`, and rows stay
    `pending` until `kill` or `find-missing` (the README states the
    minimum version);
  - subagents' and in-process agent-team teammates' tool and permission
    events move the row's state and use its relay, so a row can read
    `working` while only teammates work; their SessionStart and SessionEnd
    are ignored (`subagent_event`);
  - other agent CLIs, Codex included, are unsupported in this release;
  - `kill` does not wait for a pane process whose start time it could not
    read;
  - a `kill` retried after `ErrTmuxKillFailed` named a non-agent survivor
    succeeds (`kill_sent` false) without checking that survivor again; only
    the first failure reports it (see [`kill`'s promises](#kills-promises)).
- **MCP `serve` processes from before the param rename.** A `serve`
  process of 0.10.x or earlier, or of 0.11.0-rc.1, silently ignores the
  underscore names of spawn's `relay_mode`, `extra_env`, `no_pre_trust`,
  `tmux_session_name` and `reuse_finished` and of list's
  `tmux_session_name`, and returns `ErrUnknownTool` for every MCP
  `get_permission` call. 0.11.0-rc.1 reports 0.11.0, so the version check
  cannot tell it from the fixed release. A `serve` process started before
  an upgrade keeps that behaviour until it restarts (see
  [Parameter names and unknown arguments](#parameter-names-and-unknown-arguments)).
- **Several stores on one tmux server** are supported (WD 2026-09-29
  STORE). Each store's labels carry its own id, and another store's agents
  are never acted on (see [The lookup rule](#the-lookup-rule)).

## Meaning changes

What a caller must now assume, where this release changes the meaning of
a result, a field or an error (SRD SR-18.3). Each item states the new
meaning and links to the section that describes it in detail.

- **`kill` success.** On a live row: the agent process of the row's
  current launch is gone, whether `kill` ended its pane and session or it
  was gone already. On a finished row: still a no-op, not verification
  that the agent exited (see [`kill`'s promises](#kills-promises)).
- **`kill_sent`** says whether a kill was sent to a pane or session.
- **`kill` errors.** `kill` ends the agent's pane and reports
  `ErrTmuxKillFailed` while the agent process runs. `ErrTmuxKillFailed`
  and `ErrTmuxNotAvailable` are now reachable from `kill` (see
  [Classes](#classes)).
- **`kill` on a `pending` row** ends only the current launch's session and
  returns `ErrTmuxSessionConflict` for a leftover of an earlier launch.
- **`find-missing`'s `ids`** add rows whose process could not be checked
  and that tmux shows with no session of their current launch, and rows
  whose recorded pane process is dead; `pending` rows are marked only past
  the pending grace period. **`unverified_ids`** add rows whose own session
  is there (Ours), rows tmux could not tell about, and rows tmux was
  unavailable for; never a `pending` row inside its grace period (see
  [`find-missing`](#find-missing)).
- **`find-missing` and squatted names.** Past its grace period, a
  `pending` row is marked `missing` when no process or session of its
  current launch exists, even while another session holds its name.
- **`find-missing` no longer scans process environments.**
- **`expire`** deletes only on Gone and keeps a row whose agent process
  runs. Its `ids` are the rows deleted, and its new `kept` and `kept_ids`
  fields report the rows kept. A store error on one row no longer fails
  the run (see [`expire`](#expire)).
- **Opted-in `spawn` success**, with `reuse_finished` (`--reuse-finished`
  on the CLI): a fresh row or a
  reset finished row. Afterwards the earlier life's conversation is
  unreachable through `resume` and `get` (see
  [Reuse of a finished id](#reuse-of-a-finished-id)).
- **`resume` success:** no session of the agent's current or earlier
  launches, and no holder of its name, was on its server; the row is then
  `pending` until the agent reports in (see
  [The pre-launch lookup](#the-pre-launch-lookup)).
- **`resume` and reuse on a finished row whose own session runs** get
  `ErrTmuxUnresponsive` while the row ended less than the stopping window
  ago or the session is younger than the starting-session bound, and
  `ErrTmuxSessionConflict` after both (see
  [Reading a refusal](#reading-a-refusal)).
- **Pane-verb success** (`read-pane`, `send-keys`, `pause`) means the row's
  own session.
- **Error names.** The narrowings, new triggers and new names of SRD
  SR-1.3: `ErrInstanceIdCollision`, `ErrSpawnNotResumable`,
  `ErrSpawnNotFound`, `ErrTmuxSessionNameInvalid`, `ErrInvalidFlags`,
  `ErrInternal`, `ErrJsonlNeverWritten`, `ErrJsonlMissing`,
  `ErrPermissionRequestNotFound`, `ErrNoOpenPermissionRequest`,
  `ErrSpawnNotInteractive`, `ErrTmuxKillFailed` and
  `ErrTmuxSessionCreate`, with the new `ErrTmuxUnresponsive` and
  `ErrTmuxSessionConflict` (see [Classes](#classes) and the
  [err_name catalog](#err_name-catalog)).
- **MCP parameter names (breaking).** Spawn's `relay-mode`, `extra-env`,
  `no-pre-trust`, `tmux-session-name` and `reuse-finished`, and list's
  `tmux-session-name`, are now `relay_mode`, `extra_env`, `no_pre_trust`,
  `tmux_session_name` and `reuse_finished`, with no aliases; no manifest
  param name has a dash. CLI flags stay dashed and TypeScript field names
  are unchanged. MCP `spawn` now honours `no_pre_trust` and
  `tmux_session_name`, and MCP `list` honours `tmux_session_name`, as the
  CLI does (see
  [Parameter names and unknown arguments](#parameter-names-and-unknown-arguments)).
- **`ErrInvalidFlags` over MCP (breaking).** Every MCP tool refuses an
  argument that is not one of its params, an old dashed name included,
  with `ErrInvalidFlags` naming it and listing the valid params, and runs
  nothing; before, MCP ignored such an argument. `tools/list` schemas set
  `additionalProperties: false`. A param value of the wrong JSON type,
  `arguments` that are not a JSON object, a `label` entry that is not
  key=value (`spawn`, `make_template`) and an `older_than` that is in
  neither duration form or is negative (`expire`) are now `ErrInvalidFlags` too, with
  a description saying what was expected, and nothing runs; before, they
  were `ErrInternal`, except a negative `older_than` such as `"-2h"`,
  which MCP took as a window selecting every finished row and the CLI
  already refused (see
  [Parameter names and unknown arguments](#parameter-names-and-unknown-arguments)).
- **The reuse opt-in in shared advice** is spelled `reuse_finished
  (--reuse-finished on the CLI)` on every surface: in the spawn and kill
  (live-row step 6) descriptions and in plain spawn's retry
  sentences (see [Reuse of a finished id](#reuse-of-a-finished-id)).
- **The session-name param in shared advice** is spelled
  `tmux_session_name (--tmux-session-name on the CLI)` on every surface:
  in the list hint that ends each refusal left for a human to look at, and
  in `ErrTmuxSessionNameEmpty`'s description. Error names are unchanged
  (see [`pkg/api/manifest` — Verb Registry](#pkgapimanifest--verb-registry)).
- **`delete` leaves every agent surface (breaking).** The `agent-director`
  CLI answers `delete` with `ErrUnknownVerb`, MCP lists no `delete` tool (a
  call gets the unknown-tool error), and the Go client (`Client.Delete`,
  `api.Delete`, `DeleteResult`, `DeleteStore`) and the TypeScript client
  (`delete`, `DeleteParams`, `DeleteResult`) no longer have it. No
  `agent-director` verb removes a row a caller names (only a scheduled
  `expire` removes finished rows); the removal a human needs is
  `agent-director-admin`'s `delete` (see [`delete`](#delete)).
- **Older TypeScript clients** surface each new error name as
  `ErrUnknownErrorName`; `ErrCallTimeout` stays their symptom when a call
  outlasts 30 s (see [Error mapping](#error-mapping)).
- **Plain `spawn`'s held name.** `ErrTmuxSessionCreate` is narrowed to
  launch failures; a held name now gives `ErrTmuxSessionConflict`,
  `ErrTmuxUnresponsive` or `ErrTmuxNotAvailable`, unless the holder
  vanished before the re-lookup. A plain spawn whose create reports
  "duplicate session" ends its new row (`ended`, not `pending`) (see
  [Reading a refusal](#reading-a-refusal)).
- **`pending` covers every launch.** After a successful `resume` the row
  is `pending`, with its process identity and `ended_at` cleared and its
  launch start set, until the agent reports in; a failed launch restores
  the finished row. `status`, `get` and `list` show the launch start
  (`launch_started_at`) (see [Launch pending](#launch-pending)).
- **`ErrSpawnNotResumable` on a `pending` row** says that a launch of the
  row began and its agent has not reported in.
- **Pre-trust.** `spawn` and `resume` report `pre_trust`, and `resume`
  pre-trusts only a row whose spawn or latest reuse did not turn it off. A
  row from before this release has pre-trust allowed, the column's
  default, so its `resume` now pre-trusts whatever its spawn chose (see
  [Workspace-trust pre-write](#workspace-trust-pre-write)).
- **`resume`'s trail.** `resume` records its move
  (`ad.resume.moved_to_pending`) and each restore attempt
  (`ad.resume.restored`), and SessionStart's `ad.spawn.state_transition`
  on a resumed row gives `pending` as its prior state (see
  [`ad.*` event namespace](#ad-event-namespace)).
- **`send-keys` with `allow_pending`** on a `pending` row reaches only the
  current launch's session, found by the `@ad_owner` label with its launch
  token that every created session now carries; otherwise
  `ErrSpawnNotInteractive` (see [Launch pending](#launch-pending)).
- **Hooks move only their own agent's row.** A hook moves a row only when
  its parent process is the row's recorded pane process. A nested
  `claude`'s and a teammate pane's hooks no longer move the row, and rows
  of agents on a Claude Code that does not run exec-form hooks stay
  `pending` (see
  [Hooks move a row only for its own agent](#hooks-move-a-row-only-for-its-own-agent)).
- **A refused `[tmux]` value fails store verbs.** A negative value, a
  positive value below a safe minimum or a non-integer in `[tmux]` makes
  store-backed verbs return `ErrConfigMalformed` instead of running on a
  clamped value (see [`[tmux]` timing settings](#tmux-timing-settings)).
- **`expire_retention_days` of 0 keeps 31 days.** A default `expire` run
  (no `older_than`) under `[defaults] expire_retention_days = 0` keeps
  finished rows for the default 31 days; before, it deleted every finished
  row on each run. A negative value, which did the same, and one above
  106751, which wrapped to another window that could select every
  finished row, are now refused like a refused `[tmux]` value:
  store-backed verbs return `ErrConfigMalformed` until the file is fixed
  (see [`expire`](#expire)).
- **An `older_than` day count above 106751** (`--older-than` on the CLI)
  is now `ErrInvalidFlags` on the CLI and over MCP, and nothing runs;
  before, the count wrapped to another window, which could select every
  finished row (see [`expire`](#expire)).
- **A different tmux server.** A row whose recorded server differs gets
  `ErrTmuxNotAvailable` instead of a false "gone", and every call for a row
  goes to its recorded socket (see [The lookup rule](#the-lookup-rule)).
- **History by life.** `prior_sessions`, `transcript_status`,
  `ErrJsonlNeverWritten` and `ErrJsonlMissing` cover the current life
  only; rows never reused see no change beyond the current-session rule
  (the history entry for the row's current session id is not part of its
  visible history; see [Resume](#resume)).

## Stop semantics

Two verbs terminate a Spawn — `kill` (immediate, forceful) and `pause`
(graceful, bounded). They make different promises about the Spawn's
final state and need to be reached for in different situations.

### `kill`

`pkg/api/kill.go` holds the flow (`Kill`, and the unexported `runKill` and
`killRun` that run every call), `kill_errors.go` its own descriptions and
`kill_trail.go` its trail events
(SRD SR-6.1 to SR-6.4). What a caller may rely on is in
[Caller contract: tmux refusal classes](#caller-contract-tmux-refusal-classes);
this is how the code gets there.

1. **Row read.** Unknown id → `ErrSpawnNotFound`. No tmux call.
2. **Finished row** (`ended` / `missing`) → success, `kill_sent` false, no
   tmux call. That is not verification that the agent exited. With the
   operator-only finished-row opt-in, `killRun.withOptIn`
   (`pkg/api/kill_optin.go`) runs instead. Only `agent-director-admin
   kill-finished` sets the opt-in: it calls `internal/adminapi.KillFinished`,
   which `pkg/api`'s `init` sets to the unexported `Client.killFinished`
   (`runKill` with the opt-in on); `Kill` and `Client.Kill` never set it, and
   `KillParams` has no field for it (b.vqr). With it, a live row is refused first; a
   finished row whose recorded name is unusable (as in step 3) →
   `ErrInternal` (`unusableNameError`) before the socket, no tmux call;
   any other finished row goes on to the socket and the one lookup.
3. **Unusable recorded name.** A live row (`pending` included) whose
   recorded session name is empty, holds a control character, or holds a
   character tmux stores differently (`.`, `:`, invalid UTF-8) →
   `ErrInternal` with no tmux call (`unusableNameError`); removing the row
   is a human's decision (README "Operator actions").
4. **Socket.** The row's recorded socket, used as recorded; a row that
   records none uses the caller's socket resolved as tmux would, creating
   nothing. An unusable socket directory → `ErrTmuxNotAvailable`, "nothing
   was done", no tmux call.
5. **One label lookup** (`tmux.Lookup`) on that socket, for the row's
   launch token, its recorded server identity and this store's id
   (`KillStore.StoreID()`). Its verdict decides:
   - **Ours: the kill sequence.**
     1. One pane listing of the socket. If the listing finds no server any
        more (the recorded server gone), the Gone rules below apply, with
        no second listing; if it cannot decide, its error, nothing sent.
     2. Adoption when due (SR-3.6): the row records no server identity or
        no pane. The pane adopted is the one whose `@ad_pane` carries the
        row's launch token (`tmux.PaneByToken`), never a pane index. On a
        live row one conditional write records it; the identity found is
        used for this call whatever the write's outcome. A finished row's
        identity is used for the call only, with no write.
     3. The agent process is chosen as SR-3.8 says (`tmux.SelectAgentProcess`).
        The listed processes are the other pane processes of the labelled
        session in the same listing (linked windows included), each pid
        once, each with its start time read now; one whose start time
        cannot be read, or that is already gone, is not waited for.
     4. The pane kill by pane id, when the listing shows the agent's pane
        (the recorded pane id with the recorded pane pid, wherever it now
        is); then, always, the session kill by the labelled session's id.
        A kill call that fails or times out does not stop the sequence.
     5. The check (below).
   - **Leftover** → `ErrTmuxSessionConflict` ("not this launch's
     session"), naming the leftover session(s) and their tmux ids; no kill
     sent.
   - **Gone.** The agent process, judged once from the row's recorded
     identity, decides:
     - dead, none recorded, or unreadable → success, `kill_sent` false,
       nothing sent. The unreadable case is a documented residual: behind a
       permission wall the labelled session's absence decides, so an agent
       whose window lives on in another session reads as gone;
     - still running → one pane listing. The agent's pane found in another
       session (a grouped or viewing session, or a linked window) → the pane
       kill by pane id, no session kill, then the check. No pane of it found
       → `ErrTmuxKillFailed` ("no session or pane of this launch was found
       while its agent process still runs"), no kill sent. A listing that
       cannot decide → its error.

     A session carrying another agent-director store's label, whatever id
     it names, counts toward Gone and is never touched.
   - **Can't tell** → nothing sent. A different server →
     `ErrTmuxNotAvailable` ("this is not the tmux server the agent was
     launched on"); tmux unavailable, the socket-permission reply included
     → `ErrTmuxNotAvailable`; conflicting labels →
     `ErrTmuxSessionConflict`; an answer it cannot read (timeout,
     unrecognised reply) → `ErrTmuxUnresponsive`.

**The check** runs after a kill was sent, and is either the wait or the
follow-up, never both. The first reading of the agent process decides:

- **Checkable** (alive or gone): the process wait. The agent process and
  the listed processes are read every `killPollInterval` (100 ms), through
  the injected clock and sleep, for up to the kill exit wait
  (`kill_exit_wait_ms`, 5 s by default). A zombie counts as gone; a later
  reading that cannot tell counts as still running. All gone → success,
  `kill_sent` true. Anything still running when the wait ends →
  `ErrTmuxKillFailed`, naming the agent's pid if it still runs and each
  survivor's pid; the row stays live.
- **Not checkable** (no process recorded, or unreadable): exactly one
  follow-up lookup, with no wait. The current label gone (Gone or
  Leftover) → success, `kill_sent` true. Still there (Ours) →
  `ErrTmuxKillFailed` ("the agent process cannot be checked and its
  labelled session is still there"). Can't tell → `ErrTmuxUnresponsive` or
  `ErrTmuxNotAvailable`, saying the kill was sent and may or may not have
  taken effect. If the killed session was the server's last, the recorded
  server answering with no sessions is Gone, so the follow-up succeeds
  (LFR H5; b.47f); while that server exits (tmux's `exit-empty`) and no
  longer answers, its process still running, the follow-up gives
  `ErrTmuxNotAvailable` or `ErrTmuxUnresponsive` (SR-3.3). Another server
  re-bound on the socket while the recorded one runs gives
  `ErrTmuxNotAvailable`, with or without sessions.

**Survivors, per call.** Success needs every process the call listed and
the agent process gone. `ErrTmuxKillFailed` names each survivor's pid,
and `ad.kill.called` carries them in `survivor_pids`. A later call does not
track them: a retried `kill` finds the lookup Gone and checks only the
agent process, so once the agent is gone it succeeds with `kill_sent`
false whether or not a survivor still runs. Ending a survivor is the
README's "Operator actions" procedure.

**Rules.**

- `kill_sent` is true exactly when a pane kill or a session kill was sent,
  including one whose call failed.
- `kill` never signals a process: tmux ends the pane, and success means
  the Claude process exited, not every process it started.
- `kill` never changes the row's state. Its only row write is the adoption
  on a live row (`AdoptIdentityIfUnchanged`, which advances `row_version`). A `pending`
  row takes the same path and stays `pending`.
- If the row finishes while `kill` waits and the agent outlives the wait,
  `kill` returns `ErrTmuxKillFailed`, and a retried `kill` is a
  finished-row no-op.
- `kill` writes no log line and has no logger (SR-6.3): the returned error
  is the report. Every call of `runKill` (the exported `Kill` and
  `kill-finished` alike) writes exactly one
  `ad.kill.called` and at most one `ad.provenance.disagree` per reason,
  both fail-open (see [`ad.*` event namespace](#ad-event-namespace)).
- A caller must run as the same user and in the same tmux environment as
  the agents. `kill`'s success on a finished row is therefore not
  verification, and on the wrong tmux server a row wrongly marked
  `missing`, `kill`'s no-op success and a reuse together start a second
  agent for the same id.

**Worst case** (SRD SR-13.2, the one source for this bound). With Q the
query timeout (`query_timeout_ms`), A the action timeout
(`action_timeout_ms`), E the kill exit wait (`kill_exit_wait_ms`) and W the
pipe-close wait (`pipe_close_wait_ms`), `kill` takes at most
max(2Q + 2A + E + 4W, 3Q + 2A + 5W): **12.4 s** at the defaults on path
(i) (lookup, pane listing, pane kill, session kill, then the exit wait),
and 9 s on path (ii) (the same four calls, then the follow-up lookup).
Its time waiting on tmux alone is at most 3Q + 2A + 5W (9 s); E counts in
full.

### `pause`

`pkg/api/pause.go` holds the flow (`Pause`, unexported `pauseRun` and
`waitEnded`); its tmux phase is the keys verbs' shared `keysRun`
(`pkg/api/pane_keys.go`), the same one `send-keys` uses (SRD SR-7.1 to
SR-7.3). `pause` is the only verb with a polling loop:

1. **Row read.** Unknown id → `ErrSpawnNotFound`.
2. **Finished row** (`ended` / `missing`) → no-op success, no tmux call,
   no wait.
3. **Every other live state but `waiting`** (`pending`, `working`,
   `ask_user`, `check_permission`) → `ErrSpawnNotPausable`, no tmux call.
   `/exit` is never typed in these states, where the slash would be read
   as input text.
4. **`waiting` row: unusable recorded name.** A recorded session name that
   is empty, holds a control character, or holds a character tmux stores
   differently (`.`, `:`, invalid UTF-8) → `ErrInternal`
   (`unusableNameError`, SR-3.2), no tmux call, `/exit` not sent and no
   wait; removing the row is a human's decision (README "Operator
   actions"). A finished row never reaches this check (step 2).
5. **`waiting` row: the tmux phase.** The row's socket (an unusable socket
   directory → `ErrTmuxNotAvailable`), one lookup by the row's current
   label and, on Ours, one pane listing; then `C-u`, a key send
   (`SendKeyPane`; see "Why `C-u` first" below), and `/exit` and a separate
   Enter (`SendKeysPane`), all to the agent's pane by pane id. A row that records no
   server identity or no pane (a lost create reply) adopts the pane whose
   `@ad_pane` names its launch token, written once through
   `adoptIdentity` (SR-3.6); the write's outcome never changes the
   result. Outcomes, with `/exit` not sent on every refusal:
   - the agent's pane not found, Leftover, or conflicting labels →
     `ErrTmuxSessionConflict`;
   - Gone (another store's sessions included) → `ErrTmuxSendKeys` ("the
     row's session is not there");
   - a different server or tmux unavailable → `ErrTmuxNotAvailable`;
   - unreadable → `ErrTmuxUnresponsive`;
   - a failed `C-u`, `/exit` or Enter call, as for `send-keys` (SR-7.3): a
     timeout → `ErrTmuxUnresponsive` saying the keys may have been
     delivered, with no follow-up; any other failure makes one follow-up
     lookup (Gone or Leftover → `ErrTmuxSendKeys`, a different server or
     tmux unavailable → `ErrTmuxNotAvailable`, otherwise
     `ErrTmuxUnresponsive`); once `/exit` went through and the Enter
     failed or timed out, the description says the text may be typed but
     not submitted. A failed `C-u` types no text and ends the call before
     `/exit`; it is described as a failed text call is, naming the key
     send ("tmux key send failed"). Every `ErrTmuxUnresponsive` ends
     "retry later".
6. **The wait** (unchanged), only once `C-u`, `/exit` and Enter went through:
   poll the row's state column every `pausePollInterval` (200 ms) until
   `state == ended` (success) or `pause.timeout_seconds` elapses on the
   real clock (`ErrPauseTimeout`). `ctx.Done()` short-circuits the loop
   with `ctx.Err()`. On any error before it, the wait does not start.

`pause` never writes the row's state; the adoption is its only store
write. **Trail:** no call event of its own. It writes at most one
`ad.provenance.disagree` per distinct reason per call, with verb `pause`
and source `ad_send_keys` (the action values as for `send-keys`; a failed
`C-u` is `nothing_sent`), only for those reasons, none in the normal case,
and before the wait, so the wait's outcome neither adds nor removes one
(see [`ad.*` event namespace](#ad-event-namespace)).

**Why `C-u` first** (b.9o4). A failed `pause` can leave `/exit` typed and
not submitted, and a waiting agent's input can hold a draft. Typing
`/exit` after either would submit, for example, `/exit/exit`, which
Claude Code reads as an ordinary prompt, not the `/exit` command: the
agent would keep running and the retried `pause` would end in
`ErrPauseTimeout`. Claude Code's prompt input binds `C-u` to deleting from
the cursor to the start of the line, so with `C-u` first a retried
`pause` submits `/exit` alone, and "retry later" after a keys failure is
safe to follow. `C-u` clears only the cursor's line: the earlier lines of
a multi-line text left unsubmitted stay, and `/exit` is then submitted
with them as a prompt. `pause` sends `C-u` as a key, never as typed text,
through the keys verbs' line clear (`keysRun.clear`); `send-keys` sends
none.

**Worst case** (SRD SR-13.2, the one source for this bound): the tmux
phase is at most one lookup, one pane listing, the `C-u` call, the `/exit`
call, the Enter call and one follow-up lookup, 3Q + 3A + 6W, **11.1 s** at
the defaults. The configured pause wait (`pause.timeout_seconds`, default
30 s) is separate and comes on top.

Pause is one-shot; no incremental progress callback.

### kill vs find-missing

`kill` never changes the row's state; `find-missing` is what marks it.
After a successful `kill`:

1. The row stays in its live state (`waiting`, `pending` and so on).
2. `find-missing` marks it `missing` once it judges the agent process
   gone, from the row's recorded process identity. Only when that process
   cannot be checked (unreadable, a pid-only identity that reads alive, or
   no identity recorded) does it consult
   tmux, and then it marks the row when the lookup finds no session of the
   row's current launch (see [`find-missing`](#find-missing)). For a
   `pending` row, a caller first
   waits out the pending grace period (step 2 of the
   [live-row sequence](#live-row-sequence)).
3. `status` or `get` then shows `missing`, or `ended` if the agent's own
   SessionEnd hook moved the row first. Neither is proof that the agent
   exited: `missing` is the sweep's judgement on the evidence available to
   it.

A caller that ends a live row to relaunch its id follows the
[live-row sequence](#live-row-sequence), which bounds and paces these
steps and says when to escalate to a human.

## Release engineering

Pre-release operator checklist: see bee `b.dc1` in the `Release` hive.

### Supported platforms

agent-director ships as six pre-built static binaries, two per target
tuple: `agent-director-<os>-<arch>` and the operator tool
`agent-director-admin-<os>-<arch>` (see
[Operator tool `agent-director-admin`](#operator-tool-agent-director-admin)),
both built by `make release-binaries` with the same version stamp
(`darwin/amd64` was dropped on 2026-05-24; see
[Supported platforms (v1)](#supported-platforms-v1)):

| OS | Arch | Format | Static |
|---|---|---|---|
| linux | amd64 | ELF 64 LE | yes (no libc dep) |
| linux | arm64 | ELF 64 LE | yes (no libc dep) |
| darwin | arm64 | Mach-O 64 LE | n/a (no system linker) |

Windows is not supported (SRD §16.1).

Linux binaries are statically linked via `CGO_ENABLED=0` plus
`modernc.org/sqlite` (pure-Go SQLite driver, no libsqlite3
dependency). The `release-binaries-smoke` target verifies static
linkage of both binaries on every release via `ldd` ("not a dynamic
executable"), and that the host-arch `agent-director-admin`'s `help`
opens with the human-approval statement. The npm tarball ships neither
binary.

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
toward a release happens on a branch; the tag lands once. A release
candidate is a stamped build, never a tag: `make build` (or
`make release-binaries`) with `AGENT_DIRECTOR_BUILD_VERSION=X.Y.Z-rc.N`
stamps that version verbatim, and feature detection counts it as `X.Y.Z`
(see [Reuse of a finished id](#reuse-of-a-finished-id)).

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
input (b.mjd). The same preflight refuses an unpaired release binary: every
`agent-director-<os>-<arch>` in `--binaries` needs its
`agent-director-admin-<os>-<arch>`, and the other way round
(`unpaired_release_binary`, b.vqr).

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
- Copies in the pre-built `agent-director` binary from `./bin/` (to
  `/usr/local/bin`), and `agent-director-admin` from the same `make build`
  to `/opt/bin/agent-director-admin`, off PATH as install.sh keeps it;
  test plans run it by that full path, and install.sh's in-repo fallback
  finds it there for the bundled `/opt/skills` copy.
- Copies all of `test/driver/` to `/opt/driver/`: the driver, its prompt,
  `db-reset.sh`, and the helpers cases call (`sql.sh`, `pane-hook.sh` and
  the Docker hook pattern's stand-ins).
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
     shell-level checks. When the block fails, the driver runs
     `db-reset.sh` again and re-runs the block under `bash -x`. The case's
     `details` hold the first run's exit status and output, then the last
     six lines of the re-run's trace. The re-run starts from a fresh reset
     so it does not meet the first run's leftovers. If that reset fails,
     `details` say `xtrace rerun skipped: db-reset failed before it`. If
     the re-run passes, they say the trace is from that passing re-run,
     not the failure.
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
`CLAUDE_CODE_OAUTH_TOKEN`, `SQL_BUSY_TIMEOUT_MS` (see
[`sql.sh`](#store-access-from-a-case-sqlsh-reusable-driver-helper)).

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

### Store access from a case: `sql.sh` (reusable driver helper)

`test/driver/sql.sh` (installed at `/opt/driver/sql.sh`) is the `sqlite3`
shell with a busy timeout. It runs `exec sqlite3 -cmd ".timeout <ms>"
"$@"`, so its arguments and stdin pass through unchanged and its output and
exit status are sqlite3's own. The wait is `SQL_BUSY_TIMEOUT_MS` whole
milliseconds (default 5000); any other value exits 2 before sqlite3 runs.
`make test-docker` forwards `SQL_BUSY_TIMEOUT_MS` into the container only
when it is set, so set it on the command line to change the wait
(`make test-docker EPIC=<slug> SQL_BUSY_TIMEOUT_MS=10000`).

```bash
token="$(/opt/driver/sql.sh -readonly "$HOME/.agent-director/state.db" "SELECT ...")"
```

**Why:** a bare `sqlite3` waits 0 ms and fails at once with `database is
locked` (exit 5) while an agent-director process briefly holds the
database's exclusive locks; agent-director's own connections wait 10 s
(details in `docs/test-writing-guide.md`, "Reading and writing a store").

**Must use:** every `sqlite3` command in a testplan shell block or a driver
script runs through `sql.sh`, for reads and writes, on `state.db` or any
other database. Cases call `/opt/driver/sql.sh`; a driver script calls the
`sql.sh` beside it, as `pane-hook.sh` does. `test/driver/prompt.md` gives
the driver-Claude the same rule and tells it to run `sql.sh` where a case's
prose says `sqlite3`. Never write another `sqlite3` wrapper or call
`sqlite3` with its own `.timeout`. `TestNoBareSqlite3InCasesOrDriver`
(below) enforces the rule.

**Driver-script tests (`test/driver-scripts/`).** Package
`driverscripts_test` calls `sandboxguard.Require()` from `TestMain` and
runs in `make test-sandbox` (`go test ./...`); no workflow runs it. The
`sql.sh` tests use temp-dir stores and a real `sqlite3` (with none on PATH
they fail inside the sandbox and skip elsewhere); the re-run test uses a
temp-dir plan and a fake `db-reset.sh`, with no store or `sqlite3`. None
touches `~/.agent-director`, tmux or Docker.

- **`sqlite_guard_test.go`**:
  - `TestNoBareSqlite3InCasesOrDriver` scans every `bash`, `sh` and
    `shell` fence in the markdown under `tickets/testplans/` and
    `test/driver/`, and every `#!` script in `test/driver/` except
    `sql.sh`. It fails on each line that names `sqlite3` anywhere but a
    full-line comment: as a command, by path (`/usr/bin/sqlite3`), inside a
    quoted string or in a trailing comment. Longer names
    (`sqlite3_analyzer`, `libsqlite3`, `state.sqlite3`) and `sqlite3` in
    prose or another fence do not count. It also fails if it scanned no
    shell blocks or no scripts, so a moved layout cannot pass it
    vacuously.
  - `TestBareSqlite3Detection` pins which forms the guard flags and which
    it lets through.
- **`sql_sh_test.go`** runs the scripts against a real held lock
  (`holdLock`: a `sqlite3` shell in exclusive locking mode inside `BEGIN
  EXCLUSIVE`):
  - `TestHeldLockFailsReadsWithoutAWait`: a bare `sqlite3` read and
    `sql.sh` with `SQL_BUSY_TIMEOUT_MS=0` fail with `database is locked`,
    and `sql.sh` with a 300 ms wait gives up only after it. Every one gives
    up in under 3 s, short of the 5000 ms default, which proves `sql.sh`
    applies `SQL_BUSY_TIMEOUT_MS`;
  - `TestDriverReadsWaitOutAHeldLock`: `sql.sh` with its default wait and
    `pane-hook.sh`'s row lookup are still waiting while the lock is held
    and finish normally once it is released;
  - `TestSQLShPassesArgumentsThrough` and
    `TestSQLShRejectsANonIntegerTimeout` cover the pass-through and the
    exit 2.
- **`run_testplan_test.go`**: `TestShellModeRerunStartsFromAFreshReset`
  copies `run-testplan.sh` beside a fake `db-reset.sh` (it counts its runs
  and removes the case's marker file) and runs a one-case plan in
  `DRIVER_MODE=shell` whose case fails, and fails differently if the
  first run's marker survived. It checks that `db-reset.sh` runs before the
  case and again before the re-run, that the re-run's trace shows the
  case's own failure, and that `details` say `xtrace rerun skipped:
  db-reset failed before it` when the second reset fails.

**Must use:** a test of a `test/driver/` script goes in
`test/driver-scripts/` and reuses its `newStore`, `holdLock`,
`start`/`run` and `driverScript` helpers. Never put a Go file in
`test/driver/`: the image copies that whole directory.

### Pinned Claude Code version

The harness installs `@anthropic-ai/claude-code@2.1.120`. Pin sites:
`CLAUDE_CODE_VERSION` in `Makefile`, the `ARG` in `test/Dockerfile`.
Empirical behaviors the harness relies on (env-var auth, settings merge,
hook surface) are recorded in `reference/*-research.md`.

Claude Code 2.1.120 starts with exec-form hook settings but ignores the
exec form's `args` and runs `command` through `/bin/sh`, so a real 2.1.120
agent's hooks would run `agent-director` with no verb and never apply;
each would print nothing and write `ad.hook.ignored` `no_exec_form`. No
Docker case relies on a real Claude Code hook (the pinned agent stops at
first-run onboarding and sends none): every case that needs a hook uses
the stand-in pattern below. A future case that needs a real agent's hook
to apply must bump the pin to the README's minimum Claude Code version,
and harness-smoke's `claude --version` check with it.

### Docker hook pattern: the stand-in `claude` and `pane-hook.sh`

(SR-20.9, SR-22.9.) A hook applies only when the `agent-director hook`
process is a direct child of the row's recorded pane process. So a Docker
case never pipes a hook into `agent-director hook` from its own shell,
except to assert that such a hook is ignored. Instead:

- **The stand-in** `test/driver/stand-in-claude/claude` (installed at
  `/opt/driver/stand-in-claude/claude`) runs `exec bash --noprofile
  --norc -i` and ignores its arguments, so the pane process tmux reports
  at the create is an interactive shell with the same pid. A case selects
  it with a PATH prefix on the agent-director command, on the spawn and on
  every resume that should relaunch it:

  ```bash
  id="$(PATH="/opt/driver/stand-in-claude:$PATH" agent-director spawn --cwd /tmp | jq -r '.claude_instance_id')"
  PATH="/opt/driver/stand-in-claude:$PATH" agent-director resume --claude-instance-id "$id"
  ```

  Not `--extra-env PATH=…`: tmux (3.3a in the image) gives a new pane the
  PATH of the tmux client that runs the create, which is agent-director's
  own, so the session environment's PATH never decides where `claude` is
  found, and the real Claude Code would start.
- **`test/driver/pane-hook.sh <id> '<payload>'`** types the hook into the
  row's recorded pane (`tmux send-keys -l --`, then Enter) as one line,
  `[K=V …] agent-director hook < <dir>/payload.json > <dir>/out 2>
  <dir>/err; echo $? > <dir>/rc`, so the pane's bash forks the hook
  directly and the hook's parent is the pane process. It waits (bounded,
  `--timeout`, default 20 s), prints the hook's stdout, copies its stderr
  and exits with the hook's status. `--no-wait` returns a job directory
  for a hook that blocks (a relayed PermissionRequest waiting on
  `decide`), joined later with `--wait <job-dir>`. `--env KEY=VALUE`
  prefixes the typed command (for example
  `AGENT_DIRECTOR_RELAY_MODE=on`), and `--pane <target>` types into
  another pane on the row's socket (a split pane, for the teammate case).
  It reads the row's `pane_id` and `tmux_socket` with one read-only query
  through the `sql.sh` beside it, so the lookup waits out a briefly held
  lock (`PANE_HOOK_DB` overrides the store path). Exit 2 is bad
  usage, 3 a row with no pane or no row, 124 a timeout (the pane's last
  lines go to stderr).
- A hook typed from a shell inside the pane must be a simple command or a
  pipeline, never `sh -c`, a subshell or a script, whose extra process
  would become the hook's parent.
- A negative case (a hook from outside the pane) pipes the hook from the
  driver's shell with `AGENT_DIRECTOR_INSTANCE_ID="$id"` and asserts the
  row is unchanged and `~/.agent-director/ad-trail.jsonl` holds an
  `ad.hook.ignored` with `reason` `pid_mismatch`.
- The no-verb case (`spawn-21-no-verb-hook-payload`, `t2.75s.qo.7v` in
  `epic-03-spawn`) needs no stand-in and no row: it pipes a SessionStart
  payload into `agent-director` with no verb from the driver's shell and
  asserts empty stdout and stderr, exit 0, no `state.db`, and exactly one
  `ad.hook.ignored` `no_exec_form` (with `parent_pid` the case shell's
  pid and null row fields) selected by its own instance and session ids,
  and that `</dev/null` and a non-hook JSON object still print help
  byte-identical to `agent-director help`.
- **A SessionStart before the identity write** (SR-22.9's wait;
  `spawn-22-session-start-before-identity`, `t2.75s.qo.4h` in
  `epic-03-spawn`) needs two more assets, both selected by the same PATH
  prefix on the spawn
  (`PATH=/opt/driver/delay-create-tmux:/opt/driver/early-hook-claude:$PATH`):
  - `test/driver/delay-create-tmux/tmux` passes every call through to
    `/usr/bin/tmux` (`exec` for anything but a create). For
    `new-session` (found before `--`) it holds the reply back for
    `DELAY_CREATE_SECONDS` (default 2, inside the 5 s create timeout),
    then writes it and exits with tmux's status, never holding stderr
    back; each create appends `<created ns> <released ns> <exit status>`
    to `DELAY_CREATE_LOG` (default `/tmp/delay-create-tmux.log`). Its
    variables avoid the `AGENT_DIRECTOR_` prefix, which is stripped from
    every tmux client's environment.
  - `test/driver/early-hook-claude/claude` is a stand-in that, before
    `exec bash --noprofile --norc -i` (same pid), runs one SessionStart
    `agent-director hook` itself as a simple command, so it is the hook's
    direct parent and the hook starts while the create reply is held
    back. It writes `pid`, `session_id`, `payload.json`, `started`,
    `out`, `err`, `ended` and `rc` (last) under
    `${EARLY_HOOK_DIR:-/tmp/early-hook-claude}/<AGENT_DIRECTOR_INSTANCE_ID>/`,
    and runs no hook when the instance id is missing or not
    `[A-Za-z0-9._-]+`. The case passes `EARLY_HOOK_DIR` with
    `--extra-env`, since an already-running tmux server does not give the
    session the client's other variables.

  The case proves the race happened (`started` < released ≤ spawn
  returned) and that the hook waited (released < `ended`), then asserts
  no `ad.hook.ignored`, exactly one `ad.hook.fired` SessionStart
  `updated`, state `waiting` with the stand-in's session id, and `pid` =
  `pane_pid` = the stand-in's pid.

**Must use:** every Docker case that needs a hook to apply spawns with the
stand-in and fires the hook through `pane-hook.sh`; never hand-roll the
`send-keys` line. The one exception is a SessionStart that must start
before the identity write, which uses the delayed-create `tmux` and the
early-hook stand-in above; never add another way to hold the create
back.

### Exit-time measurement harness (`tools/measure-exit/`)

An operator tool, not a test plan. It is not in `test/docker-epics.txt`
and no CI job runs it. It measures real Claude Code exits for SRD Open
Questions RN-6 (pane kill to process gone; sets `kill_exit_wait_ms`) and
RN-2 (stored `ended_at` to the session no longer listing; informs
`stopping_window_seconds` and the stopping window's minimum). It also runs
RN-9: an in-session `/resume`, agent teams in the in-process and split-pane
modes, the per-hook parent-pid outcome, and the exec-form version probe.
RN-7's payload keys are recorded inside RN-9's run, for the record only.
Operating it, including approval, credentials, `decide` and reading the
results, is in [`tools/measure-exit/README.md`](../tools/measure-exit/README.md).
This section does not repeat it.

**Parts.**

- **`run.sh`**, the host runner. It is shell only and never runs tmux,
  agent-director or a Go artifact on the host. It is print-only unless
  `--run`. It stages copies of the settings layers, starts one container per
  run (one per probed version for the probe), and wraps it in the guard.
  - For `measure` and `rn9` it always passes `-cases`: the mode's ids by
    default, and it refuses (exit 2) a `--cases` id from another mode.
  - It refuses (exit 2) a staged layer that is or links to `.claude.json`
    or `.credentials.json`, is not JSON, or holds a credential-like key, or
    whose `env` objects break the layer env rule (`LAYER_REFUSED_ENV`; see
    the isolation contract below). The refusal applies in print-only too,
    and names the key, never a value.
  - The probe takes its candidate versions from `npm view` in a
    credential-free base-image container on the host network, or from
    `--versions` / `--versions-file`. A failed or empty listing stops the
    bisection.
  - A deployed-version STOP ends the probe right after the guard verify,
    with no bisection and no further build (exit 3).
- **The measurement image** (`tools/measure-exit/Dockerfile`), built FROM
  the test image. Claude Code is reinstalled at `CLAUDE_CODE_VERSION` under
  its own tag (`agent-director-measure:cc-<version>`), on Node 22. The test
  image's own pin is unchanged. The driver compiles in the image's build
  stage, and the image holds no stub. The `REQUIRE_NATIVE_CLAUDE` build
  argument checks that `claude` resolves to the package's native ELF binary,
  not a JS launcher, which would make every hook a `pid_mismatch`. The image
  records the result in `/opt/measure-exit/claude-launcher.txt`.
  `make measure-image` requires the native binary. The probe's per-version
  builds only record the result.
- **The driver** (`measure-exit`, Go `package main`). Its subcommands are
  `run -mode real|dry|probe`, `decide` and `record`.
  - `run` spawns agents through the v-next binary on a private tmux server
    and takes one measured action per sample: a pane kill, `pause`'s
    `/exit`, or a natural-exit keystroke. It polls every 100 ms. RN-6 uses
    the same start-time reader as `kill`'s wait, and RN-2 polls the
    session's presence.
  - Cases register through `registerCase` (`cases.go`). The ids are
    `rn6.{idle,midturn,mcp}`, each also with `.raised-hook` and
    `.raised-env`; `rn2.{natural,pause,mcp}`; `rn9.{drive,resume,team-inprocess,team-splitpane}`;
    and `probe.exec-form`. Real mode refuses to run without `-cases`
    (`selectCases`). Dry and probe modes default to every case.
  - Generated layers (the raised SessionEnd budget, the RN-9 recorder) are
    `.claude/settings.local.json` in a harness-owned working directory per
    agent, because `--settings` is a denied caller arg. MCP servers come
    through `--mcp-config`.
  - `record` is the RN-9 recorder hook, in exec form on 11 events. It keeps
    key names and id-shaped values only. `rn9eval.go` joins each recorded
    hook to the container trail's `ad.hook.fired` / `ad.hook.ignored`
    records and gives the verdicts and STOP flags.
  - The RN-9 drive answers permission requests through `decide`, scoped per
    scenario (`rn9ExpectedTools`). It reads only the tool name from `get`,
    allows the scenario's expected tools and denies everything else.
  - **Team-scenario evidence** (`capture.go`, used by `runTeam` in
    `rn9.go`). Harness-only aids, with no production change:
    - `waitInputReady` polls the lead's visible screen every 500 ms until
      `inputReady` sees a prompt line or the `? for shortcuts` footer and
      no selection dialog. It then waits `inputReadyGrace` (2 s) and sends
      the team prompt. The bound is `-input-ready-timeout`. Past it, the
      prompt is never sent and the error is `errInputNeverReady`, quoting
      a scrubbed excerpt of the pane (`paneExcerpt`).
    - After the send, `runTeam` waits up to `-prompt-accept-wait` for the
      lead's own `UserPromptSubmit`.
    - When a team step fails, `capturePanes` writes every pane of the
      lead's session, and of any `claude-swarm-*` server under the private
      socket directory, to `<case>-pane-<label>.txt`, before `pause`. It
      captures again with `-after-pause` if `pause` fails.
    - Each lead runs with `--debug-file` under the run's HOME.
      `keepDebugLogs` copies it, and every file new or changed in
      `$HOME/.claude/debug/` during the scenario, to
      `<case>-claude-debug-*` (`copyScrubbed`; over `maxDebugCopyBytes`,
      32 MiB, the tail from a whole line).
    - The file names go to the scenario's `Artifacts` (`artifacts` in
      `results.json`).
  - **Scrubbing** (`env.go`, `scrubber`). `newScrubber` builds its parts,
    lower-cased, from every set `credentialEnv` value (6 characters or
    more, `minScrubLen`). The parts are the exact values; for
    `ANTHROPIC_BASE_URL`, the URL, its host with and without the port, and
    its path or query parts of 12 or more characters (`urlParts`); for
    every other value, each 12-character piece (`minPartLen`).
    - `scrub` replaces the exact values only. The input-ready check reads
      text scrubbed this way, so a prompt line is still seen.
    - `scrubParts` also replaces every part, without regard to case,
      merging overlapping matches into one `<redacted>`. It covers all of
      `results.json` (`writeJSON`), the table (`writeTable`) and run-log
      argv (`runLog.record`). It replaces substrings only, so the JSON
      stays valid.
    - `scrubEvidence` is `scrubParts` plus line withholding: a line matching
      `credentialWordRE` (auth, token, bearer, api key, cookie, secret,
      passw) becomes `[line withheld: it names a credential]`. It covers
      Claude's own text only: pane captures, debug-log copies and the pane
      excerpt in an error, reason or note. It is never applied to
      `results.json` or the table.
    - Scrubbing removes what the driver knows to look for. It cannot prove
      that nothing else secret is left, so a live run's command file also
      runs a leak gate over every result file.
  - `decide` is pure. It reads results directories and prints the RN-6,
    RN-2 and RN-9 decision record. It exits 0 when decided, 2 on invalid
    input, and 3 for a STOP. It reads the current timing values from
    `internal/config`, and the stated Claude Code minimum from the tool's
    own `minClaudeCodeVersion` (2.1.280, in `tools/measure-exit/config.go`).
    - Each sampled case's rules read every usable sample, in any position,
      once at least 20 are usable (`useSamples`). The largest time is the
      largest over all completed samples. The record shows the recorded,
      used and dropped counts.
    - A default-budget "did not exit" is invalid in any position. Under a
      raised budget, a "did not exit" counts as a measured sample.
    - A `no_ended_at` sample makes its case invalid under any budget.
    - A case whose stored counts disagree with its samples is invalid.
    - `killCeilingAt` derives kill's SR-13.2 ceiling from the
      `internal/config` constants, never from a literal. The record shows
      both paths and their max.
    - `-supersede ID` (repeatable, RN-9 scenario ids only; `decideOptions`,
      `decideWith`, `mergeScenarios`) lets a later run's result of one
      scenario replace earlier inconclusive ones, ordered by `finished_at`.
      An earlier pass or fail is refused, and equal or missing finish times
      cannot be ordered; both stay invalid. The record gains a
      `## Superseded` section. Without the flag, a scenario in two inputs
      is invalid.
- **The dry-run stubs** (`stub/claude`; the probe's `probe-args-kept` and
  `probe-args-dropped`; the team leads `stub/lead-dialog`, whose dialog
  never yields its input, and `stub/lead-late`, whose prompt shows late)
  and `dryrun.sh`, the sandbox dry run.
- **`guard.sh`**, the host-state guard (below).
- **Makefile targets:**
  - `make measure-exit-dryrun`: the dry run, inside the sandbox. It needs
    no credentials, no network and no operator.
  - `make measure-exit-print`: prints the runner's container command lines
    and session counts. It builds and runs nothing.
  - `make measure-image`: builds the measurement image.
  - `make measure-exit`: the gated live run (`MEASURE_MODE` `probe`,
    `measure` or `rn9`). It needs approval and credentials.

  `measure-image` and `measure-exit` chain through `test-image` to the host
  `make build`, the same as `make test-image`. Who launches the live runs
  L0, L1 and L2, the approval they need, their order and their launch
  gates are in the tool's README
  ([Before any live run](../tools/measure-exit/README.md#before-any-live-run)
  and [Live runs](../tools/measure-exit/README.md#live-runs)). The gates
  live in the operator command files, not in the repo; `run.sh` and the
  Makefile do not enforce them.

**Isolation contract.**

- The container has its own HOME with no `.agent-director`, its own store,
  and a private tmux server (a private `TMUX_TMPDIR` at mode 0700, with
  `TMUX` unset). Every spawned row's socket must lie under that directory,
  or the run aborts.
- The driver's preflight refuses to start without the container marker
  (`AGENT_DIRECTOR_MEASURE_CONTAINER`, or the sandbox marker in dry mode).
  It also refuses with `TMUX` set, with a store under `$HOME` or the
  passwd-entry home, below the sample floor, or below the version floor.
- Child processes get an allowlisted environment. `TMUX`, the host's Claude
  Code session variables and `AGENT_DIRECTOR_*` never reach a child.
- Credentials travel by environment only, forwarded by name. The runner's
  list holds exactly the gateway pair and the two pinned models. The probe
  forwards none and runs with `--network none`.
- Real mode is gateway-only. `checkRealModeEnv` refuses with rule
  `real-gateway-only` when any variable in `realModeRefusedEnv` (an API
  key, an OAuth token, the Bedrock and AWS variables) is set, even empty.
  `realModeEnv` also drops them from every child.
- The same rule covers the layer files the agents load, in real mode only
  and before anything is written. `checkLayerFiles` (`layerenv.go`), called
  from `checkEnvironment` right after `checkRealModeEnv`, runs
  `checkLayerFile` on each file `realModeLayerFiles` lists (the managed and
  user layers, plus the project, local and MCP layers when given). The
  first refusal stops the run. The refusal names the layer kind, path and
  key, never the value. A file is refused with `real-gateway-only` when,
  checked in this order:
  - its path as given is named after a `layerRefusedFileNames` file
    (`.claude.json`, `.credentials.json`). This is checked even when
    nothing is there yet, because the driver writes its own `.claude.json`
    after the preflight (`seedClaudeState`). After this check, a path with
    nothing at it (`os.Lstat` finds nothing) is no layer, and the checks
    below are skipped;
  - something is at its path but does not resolve: a dangling symlink, or
    a chain ending in one. It is refused whatever its target is named,
    because the target cannot be checked and may be created later in the
    run. `run.sh`'s `report_layers` reports a dangling source as `MISSING`
    and never stages it, so this refuses nothing a run started by `run.sh`
    would load;
  - it does not resolve for any other reason (a symlink loop, a directory
    it cannot search);
  - the path it resolves to through every symlink is named after a
    `layerRefusedFileNames` file;
  - it cannot be read, or is not one JSON document. An empty file and
    concatenated documents are refused too, which is stricter than
    `run.sh`'s `jq`;
  - a key at any depth (arrays included) is credential-like: its
    upper-cased name contains a `layerCredentialKeyParts` part (`KEY`,
    `TOKEN`, `SECRET`, `PASSWORD`, `PASSWD`, `CREDENTIAL`, `OAUTH`,
    `AUTHORIZATION`, `COOKIE`). That catches `apiKeyHelper`, an env name
    such as `MY_API_TOKEN` and an `Authorization` header in an MCP
    server's `headers`. The first such key in sorted order is named
    (`findCredentialKey`);
  - an `env` object at any depth sets a name `layerEnvNameRefused`
    refuses (any `CLAUDE_CODE_USE_*`, a `realModeRefusedEnv` or
    `credentialEnv` name, `ANTHROPIC_CUSTOM_HEADERS`; case ignored) or a
    value `layerEnvValueRE` matches (a URL, a bearer token, an
    authorization header) (`findLayerEnv`).
- `run.sh`'s `refuse_credential_layer` makes the same layer refusals, in
  the same order, bar the dangling-link one above, when it stages a layer,
  in print-only too (its key and value tests run inside `jq`). The driver
  repeats them so a container started by hand is held to the same rule.
  Two tests keep the runner and the driver in step:
  `TestRunnerLayerRefusedEnvInStep` (`LAYER_REFUSED_ENV`, the driver's
  refused names and `realModeRefusedEnv`) and
  `TestRunnerCredentialLayerInStep` (`refuse_credential_layer`'s file-name
  and key-pattern case arms against `layerRefusedFileNames` and
  `layerCredentialKeyParts`, both ways). The shared `credentialLayerCases`
  table runs the same refused layers through both `run.sh` and the driver.
  `TestRunnerLayerReport` checks that `run.sh` reports a dangling layer
  link as `MISSING` and does not stage it.
- Mounts are staged settings copies (read-only) and one results directory.
  The host home, `~/.agent-director`, `~/.claude*`, a tmux socket
  directory, `/tmp` and the engine socket are never mounted.
- The driver never opens the store. Besides spawning its own agents, it
  makes only the measured exits and pane kills, the RN-9 drive's sends to
  its own agents, and read-only calls (`get`, tmux `list-*` and
  `capture-pane`). Every call goes to `run-log.jsonl`, as argv only with
  credentials scrubbed.

**A second isolation boundary.** The real run's measurement container is a
second isolation boundary beside the sandbox. It holds the same invariant:
its HOME has no `.agent-director`, its tmux server is private, and nothing
built runs on the host. The host-state guard backs it. It is not a
substitute for the sandbox for tests: tests still run only through
`make sandbox*`.

**The guard.** `guard.sh` reads only `state.db` and `ad-trail.jsonl` under
the passwd-entry home, not `$HOME`. The real host store lives under the home
the operator's `HOME` normally points to, which is the passwd-entry home, and
reading the passwd entry means a redirected `HOME` in the guard's own
environment cannot point it at another directory (b.8dr). It takes a
snapshot before the run and verifies after it, even when the container
fails. Quiet-host mode compares SHA-256 sums and fails on
any change. Busy-host mode, the runner's default, scans only the trail bytes
appended since the snapshot for the run's identifiers (from
`harness-ids.txt` and `--id`). It reports `state.db` as not checked, and it
never opens a live database.

**Reuse and scope.**

- **Must use:** any future tool that runs agents on a host to measure them
  wraps the run in `tools/measure-exit/guard.sh` (snapshot, then verify).
  Never write another checksum or trail guard.
- **Scoped to this harness:** the dry-run stubs. The driver's dry mode
  refuses any `claude` whose `--version` lacks "measure-exit dry-run
  stub". They follow the stand-in pattern above: the stub is the pane
  process and fires each hook as its direct child. Docker cases keep using
  `test/driver/stand-in-claude` and `early-hook-claude`.
- **Test helpers (must use within the package):** the package follows the
  shared rule: timing tests use `tmuxfix.Clock`
  ([tmux test doubles](#tmux-test-doubles-replay-catalogue-recorder-and-clock-reusable-test-fixtures))
  and code that takes a `ProcChecker` gets a `procfix.Checker`
  ([procfix](#procfix-the-process-checker-fake-reusable-test-fixture)).
  `tools/measure-exit/helpers_test.go` holds the package's helpers:
  - `sleepClock` (`newSleepClock()`, at `clockStart`), the harness clock
    over the shared clock. It embeds a `*tmuxfix.Clock` and adds `Sleep`,
    which calls the clock's `Advance` unless a test wraps it, so every
    wait and poll runs on virtual time and never sleeps;
  - `agentStart` and `otherStart`, the agent's start time and a reused
    pid's, from `procstarttimefix`;
  - `fakeEnv`, the `environment` seam, and `fakeExec`, which records
    agent-director and tmux argv. No shared fixture covers these seams;
  - `newRig`, a harness on a temp tree with a `fakeExec`, a `sleepClock`
    and an empty `procfix.Checker` (`r.pc`, every pid gone). A test scripts
    the agent process with `r.pc.Set`; `setAfterSleep(d, pid, p)` sets
    `pid` to `p` once `d` of virtual sleep has passed, as the kill fixture's
    `setAfterWaiting` does
    ([kill fixture](#pkgapi-kill-fixture-and-shared-verb-tables-reusable-test-fixtures));
  - `credentialSentinels` with `assertAbsent`, for no-leak checks.

  New tests in `tools/measure-exit` must use these. Do not add another
  environment, clock, exec or process double there.
- **The dry-run E2E:** `tools/measure-exit/dryrun` runs `dryrun.sh` with a
  private `TMUX_TMPDIR`. Run it with `-count=1`, because it builds in a
  subprocess. It is the one end-to-end proof of the harness; extend it
  rather than adding a second one.
- **Sandbox guard:** both `tools/measure-exit` and
  `tools/measure-exit/dryrun` call `sandboxguard.Require()` from `TestMain`
  (see "Sandbox guard and the CI bypass").

### Model check (`make tla`)

An on-demand design check, taken before engineering when the design
changes. It is not CI: no GitHub workflow or CI hook runs it, and it is not
part of `all`, `test`, `test-sandbox` or any release target. No other target
depends on it.

`spec/tla/` holds bee b.zuj's TLA+ model of the Phase 1 LABEL design, its
model-check suite and the runner. b.zuj owns the model and its results. The
repo copy is vendored byte for byte under SHA-256 pins, with two exceptions,
`Phase4.tla` and `ci/mkci.py`. `Phase4.tla` carries bee b.66h's
launch-scoped-actions change. Its knobs default to off. With them off, the
model's steps are identical to b.zuj CI run 4, but distinct-state counts
under `VIEW CIView` can vary between runs. Bee b.y88 then changed only
comments in both files, which now name `agent-director-admin kill-finished`.
Neither the model's steps nor `mkci.py`'s output changed.
[`spec/tla/PROVENANCE.txt`](../spec/tla/PROVENANCE.txt) records the
source, the pin list, the delivering run, the b.66h and b.y88 changes, the
layout and the TLC build: `tla2tools.jar` rev `4260e47`, committed because
that nightly build has no immutable upstream URL.

[`spec/tla/launch/`](../spec/tla/launch/README.md) holds the b.66h runs. It
is written here and not pinned. Its runs are not in `ci/suite.tsv`, so a
plain `make tla` does not run them; `make tla
TLA_SUITE=spec/tla/launch/suite.tsv` does.

**What the suite checks.** The threat model is accidents only. Respawn and
`remain-on-exit` are out of scope. The model's three actors are named here
as follows:

- the *task caller* launches agents under fixed ids and kills them;
- the *supervising caller* keeps one agent id running: it spawns, gets,
  resumes and reuses the id, runs `find-missing`, kills a launch that
  never reports in (the model's `B_NrKill`), and answers the agent's
  startup prompts with `send-keys` on its `pending` row;
- the *operator* resumes and kills by hand.

The model has three specs:

- `Phase4Split.tla` (65 runs) and `Phase4.tla` (5 runs: the unsplit
  cross-check `ci_smoke_*`, plus `ci_C7` and `ci_L1`). Together they model:
  - the label as proof of a session's launch;
  - the lookup's verdicts;
  - `kill`'s pane kill plus process check;
  - process-only liveness;
  - the recorded socket and adoption after a lost create reply;
  - `find-missing` and `expire` under those rules;
  - the plain-spawn scan;
  - the store id in the label.

  The in-scope accidents are:
  - a rename;
  - a grouped viewing session;
  - a failed label step;
  - a lost create reply;
  - a server restart;
  - a different server at the recorded socket;
  - a leftover session of an earlier life;
  - strays and crashes;
  - the /proc wall;
  - a `$` name;
  - another store's agent under the same id.

  Their hook gate (`HookGate`) is the earlier session-based gate of WD
  2026-09-29 HOOK, which decision-0929c replaced.
- `Phase5Hook.tla` (22 runs), decision-0929c's hook identity for one
  instance id. It models:
  - the built gate (SR-22.9, `internal/store/hook_gate.go`), where a hook
    applies only when its parent process (pid and start time) is the row's
    recorded pane process;
  - adoption by `@ad_pane`;
  - `kill` waiting for every pane process of the session;
  - SessionStart's bounded wait for the launch's identity write (0930b Q1).

  Its actors include a nested `claude`, teammates, leftovers, strays, a
  `/clear`, a lost reply, processes that survive the hangup, and pid reuse.

`mkci.py` sets the design values. The runs that turn one off are:

- the controls, each of which removes one design amendment;
- `ci_G3b_ren_noscan`, a safety run with the spawn scan off (the AC-SPN-11
  row relies on it);
- the probes `ci_V_Leftover` and `ci_V_P1Conflict`, with the spawn scan
  off;
- the residual `ci_Cact`, with the action-time process check off.

The properties checked are:

- **Phase4 / Phase4Split safety:**
  - `VerdictSound`: the lookup's verdict is true to the ground truth;
  - `HandsOff`: kills and keys reach only the row's current launch;
  - `KillHonest`: a kill that reports success leaves no process of the
    current launch;
  - `KillFinishedOnlyLeftover`: the operator's finished-row kill never ends
    a working agent;
  - the supervising caller never kills a finished row;
  - `SendOnlyCurrentLaunch`, `NonGoneInert`, `MarksRightLife`,
    `ResumeNoEarlierLife`, `GetNoEarlierLife`, `NoOrphan`, `NoOrphanLife`,
    `OneAgentPerId`, `NoWrongMemory` and `SidBound`;
  - the action properties `NoFalseMissing` and `KillPendingOnlyCurrent`.
- **Phase4 / Phase4Split liveness:** `PendingResolves` and `StuckHeals`.
- **Phase5Hook:**
  - `RowOnlyByAgent`: the row's state and session id move only by the
    current launch's agent;
  - `KillAllGone`: `kill` never succeeds while a pane process of the
    session survives;
  - `PendingResolves` and `ReportInResolves`.

The run kinds and their expected results:

| Kind | Expected result |
|---|---|
| Safety run | pass |
| Eventual-resolution run (a launch that never reports in) | pass |
| Control that removes one design amendment | a violation |
| Reachability probe | a violation, meaning the state is reached |
| Accepted residual, such as `ci_Cact` (the listing and the act as separate steps) and `ci_H_Rns` (an unreadable recorded pane start time plus pid reuse) | a violation |

The model's limits:

- **Small bounds:** 2 or 3 sessions, 1 to 3 lives, and one fault per
  behaviour.
- **One accident group per safety run**, with a state-reducing view on the
  safety runs only.
- **Phase4 and Phase4Split's liveness runs have no stray.** Phase5Hook's
  liveness runs (`ci_H_L`, `ci_H_Lrep` and their controls) do allow
  strays (`AllowStray = TRUE`).
- **The listing and the act are one step**, except in the residual run.
- **`ci_C7` uses `remain-on-exit`**, a setting that is out of scope.
- **Phase5Hook covers one instance id** and no label lookup.

**The manifest and tiers.** [`spec/tla/ci/suite.tsv`](../spec/tla/ci/suite.tsv)
has 92 rows. Its columns are group, spec, cfg, cap_s, expect, tier and
what. Its groups are g1, g2, g3, g4a, g4b, g5a, g5b, g5c and g6.

| Tier | Runs | Contents | Scheduler time |
|---|---|---|---|
| `fast` (the default) | 71 | every must-fail run, plus `mkci.py`'s fast-pass rows | about 1.5 h |
| `full` | 92 | every row | about 6–7 h |

**Criteria to runs.** Each row below lists the PRD criteria, the runs that
check them with each run's tier, and the expected results. Every listed run
met its expectation in b.zuj CI run 4 (see Evidence). The rows follow the
current SRD (0929c, 0930b and 0930c are final) and change if the design
does. In that case b.zuj revises the model. A probe is expected to be
reached, and a residual to violate its property.

| PRD criteria | Runs (tier) and expected result |
|---|---|
| AC-HOOK-01 to AC-HOOK-04 (the built parent-process gate) | <ul><li>`ci_H_Sns` (fast) and `ci_H_S` (full), `RowOnlyByAgent`: pass.</li><li>Controls `ci_H_Ctop` (gate by the topmost ancestor) and `ci_H_Cpane` (gate by the hook's pane) (fast): a violation.</li><li>Probes `ci_H_V_NestedIgnored`, `_TeammateLive`, `_RotationApplied`, `_PidReused` and `_NullStart` (fast).</li><li>Residual `ci_H_Rns` (fast).</li></ul> |
| AC-KILL-19 (every pane process gone) | <ul><li>`ci_H_Sns` (fast) and `ci_H_S` (full), `KillAllGone`: pass.</li><li>Control `ci_H_Ckill` (waits for the agent process only) (fast): a violation.</li><li>Probes `ci_H_V_KillFailed` and `_ChildOutlives` (fast).</li></ul> |
| AC-HOOK-05 (SessionStart's wait) | <ul><li>`ci_H_Lrep` (fast), `ReportInResolves`: pass.</li><li>Control `ci_H_Lrep_nowait` and residual (ii) `ci_H_Lrep_slow` (fast): a violation, as designed.</li><li>Probes `ci_H_V_WaitedApplied` and `_WaitedIgnored` (fast).</li></ul> |
| AC-LKP-21 (adoption by `@ad_pane`; a no-pane row is Gone) | <ul><li>`ci_H_L` (full), `PendingResolves`: pass.</li><li>Control `ci_H_Lsrd` (fast): a violation.</li><li>Control `ci_H_Cadopt` (adopts any pane) (fast): a violation.</li><li>Probe `ci_H_V_Adopted` (fast).</li></ul> |
| AC-HOOK-03's "never kills a working agent" | <ul><li>The earlier gate's runs: `ci_G3b_ren`, `ci_G3b_ren_u` (an unlabelled leftover) and `ci_G3b_ren_noscan` (full), `KillFinishedOnlyLeftover`: pass.</li><li>Control `ci_Chook` (fast): a violation.</li><li>Probe `ci_V_HookIgnored` (fast).</li><li>These check the earlier session-based gate. The property carries over to the narrower built gate by argument, not by TLC.</li></ul> |
| AC-SPN-11 (the plain-spawn scan) | <ul><li>Probe `ci_V_ScanBlocks` (fast).</li><li>`ci_G3b_ren` (full) runs with the scan on.</li><li>`ci_G3b_ren_noscan` (full) shows that safety does not rest on the scan.</li></ul> |
| AC-LKP-20 (the store id in the label) | <ul><li>`ci_S_store` (fast), `VerdictSound` and `HandsOff` with another store's agent under the same id: pass.</li><li>Control `ci_Cstore` (fast): a violation.</li><li>Probe `ci_V_OtherStore` (fast).</li><li>The store id's creation, migration and restore are not modelled.</li></ul> |
| AC-KILL-17 and AC-KILL-18 (the strict `kill`; PRD Traceability row "PO 2026-09-27 REVIEW") | <ul><li>`HandsOff`, `KillHonest` and `KillPendingOnlyCurrent` in the safety runs `ci_S1o_*` (fast), `ci_S1b_*` and `ci_S1h_*` (`_lab` fast, the rest full), and `ci_smoke_*` (fast): pass.</li><li>Control `ci_C5` (session kill only, with a grouped viewer) (fast): a violation.</li><li>Probes `ci_V_KillFailed` and `ci_V_KillWithViewer` (fast).</li></ul> |
| The LABEL lookup's mechanisms from the PRD Traceability row "PO 2026-09-27 LABEL": the label as proof of launch and the four verdicts, the recorded socket, and process-only liveness. The row's other criteria (for example AC-CFG-01's timing defaults, AC-TEST-02 and AC-EXP-08) are not covered. | <ul><li>`VerdictSound` and the safety properties in `ci_S1o_*`, `ci_S3o` and `ci_smoke_*` (fast), `ci_S1b_*` and `ci_S1h_*` (`_lab` fast, the rest full), `ci_S3b` (full) and `ci_G3b_*` (`ci_G3b_lab` fast, the rest full): pass.</li><li>`PendingResolves` and `StuckHeals` in `ci_L5o`, `ci_L5b_*` and `ci_L1` (`ci_L5o`, `ci_L5b_lab` and `ci_L1` fast, the rest full): pass.</li><li>Controls `ci_C1` (token cleared at report-in), `ci_C2` (lookup by name), `ci_C3` (`$` name chained), `ci_C4` (failed label step), `ci_C6` (the caller's server, no adoption) and `ci_C7` (session presence as liveness) (fast): a violation.</li><li>The verdict probes `ci_V_*` (fast).</li></ul> |

**Not covered, and model-vs-built gaps (for b.zuj, which owns the model;
nothing here changes it).**

1. Phase4's hook gate is the superseded one. The built gate is narrower,
   so Phase4's safety results carry over only by argument. Phase4's
   liveness runs assume the wider gate. No run composes the built gate
   with the label lookup.
2. Not modelled:
   - subagents and in-process teammates (no `agent_id`, no in-process
     actors), so neither the `agent_id` guard (`subagent_event`) nor their
     other events moving the row; the SRD says they need no model;
   - `no_exec_form`; the SRD says it needs no model;
   - an unreadable parent start time. The built rule, under which the hook
     never applies, is safety-conservative. For liveness the row stays
     `pending`, like a lost reply;
   - the SessionStart wait's 540 s cap (`sessionStartWaitCap` in
     `internal/hook/handler.go`, AC-HOOK-05). It binds when the grace
     period is over 540 s, and also when the recorded launch start is in
     the future or the clock steps. `ci_H_Lrep_slow` covers it in spirit;
   - kill's exit wait, its skip of a pane process whose start time is
     unreadable, and the retry after survivors: the model's kill is one
     atomic step;
   - the store id's creation, migration and restore;
   - the `@ad_owner` scope-value check;
   - locale, unusable names, timing settings, pre-trust and schema
     migration.
3. The model's spawn scan covers "a plain spawn". The built scan
   (`scanForLeftover` in `pkg/api/spawn_scan.go`) covers a caller-supplied id
   with no row. Its Can't-tell refusal on an unreadable lookup is not in
   the model.
4. Environment assumptions:
   - the jobs need a scheduler timeout of at least the group budget plus
     margin, and at least 24 GiB;
   - `tlcjob.sh`'s comment about the scheduler timeout is stale;
   - the job image's base is not pinned by digest.

**How to run it.** Only an operator runs `make tla`. Agents never
submit. They may run `make tla-print` and the runner tests.

```
make tla-print                          # the plan and the pin check; submits nothing
make tla TLA_CHANNEL=<channel id> TLA_JOBSCHED=<absolute path to the scheduler CLI>
```

The variables:

- `TLA_CHANNEL` is required and has no default. It is the job scheduler's
  notice channel ID, which the operator supplies and which must be on the
  scheduler's allow-list. Each job sends one notice to it.
- `TLA_JOBSCHED` names the job scheduler CLI. Its default, `jobsched` on
  PATH, fails closed by design, because the CLI is not installed on PATH.
  Pass the CLI's absolute path.
- `TLA_TIER` is `fast` (the default) or `full`.
- `TLA_GROUPS` lists the groups to run, space-separated, in the order
  given. The default is every manifest group.
- `TLA_SPEC_DIR` and `TLA_SUITE` point at another copy of the three specs
  and another manifest. An override is not pinned. The cfgs, the jar and
  the job image files always come from the pinned tree.

The host needs `bash`, `sha256sum` and `jq`. The runner reads the
scheduler's `--json` output with `jq`. The scheduler's dispatcher must
already be running: `make tla` never starts or stops it.

TLC and java never run on this host, nor in a local container. The runner
builds job contexts, submits them, polls them and reads their logs. The
checker runs only inside each job's container on the scheduler's remote
host. Nothing local builds the job image. `make tla` is a host target only
because the scheduler CLI is a host tool. It runs nothing built from the
repo and never opens `~/.agent-director`, so it does not breach the rule
that nothing built runs on the host.

The runner, [`spec/tla/ci/run_ci.sh`](../spec/tla/ci/run_ci.sh), runs in
this order:

1. **Preflight, before any submit.** It checks:
   - the tier;
   - the tools;
   - every pin in `PROVENANCE.txt`, and the jar's size;
   - the manifest's rows and groups;
   - that `TLA_CHANNEL` is set;
   - that the CLI resolves;
   - that `$TMPDIR` lies outside the repo;
   - that `list --json` reports a running dispatcher.

   The dispatcher check comes first among the scheduler calls because the
   scheduler has no cancel operation: a job submitted while the dispatcher
   is down would run whenever it next starts. Any failure prints
   `FAIL <step> -- <reason>` and a `CI-VERDICT FAIL (<reason>, tier <t>)`
   line, submits nothing, and exits 1.
2. **A parse-only job** over every spec module gates the rest. Its submit
   reply carries the scheduler's limits. The run timeout must cover the
   largest selected group budget plus 300 s (10500 s for either tier with
   every group), and the
   memory must be at least 24 GiB. Otherwise the runner waits for that
   job to end, then fails with nothing more submitted.
3. **One job per non-empty group,** strictly one at a time, in manifest
   order or `TLA_GROUPS`'s order. Each job runs with `--network off`. A job's budget is the sum of
   each run's cap plus 20 s, plus 120 s, at most 10200 s.

Job contexts, logs and the per-run directory:

- Contexts and logs live in a per-run directory under `$TMPDIR`, which the
  runner prints.
- A context is kept until its job ends, then removed.
- Logs are fetched through the scheduler's `results` and always kept.
- The output is one line per run, then the `CI-VERDICT` line with ok/total,
  minutes and tier.
- A run is ok only when its result matches its expectation. INCOMPLETE,
  ERROR, SKIPPED and NO-VERDICT are never ok.
- The exit status is 0 only on PASS. Nothing is retried.

**Interrupts.** On SIGINT, SIGTERM or SIGHUP the runner exits 130 with
`CI-VERDICT FAIL (interrupted, …)`. It then acts on the current job:

| The current job | What the runner does |
|---|---|
| Still queued | Cancels it in effect: removes the job's context, so the scheduler rejects the job when it reaches the head of the queue. |
| Already building or running | Keeps the context. The job finishes on the scheduler, because the scheduler has no cancel operation. |
| State unreadable | Keeps the context (fail safe). |

The logs are always kept.

**Evidence.** b.zuj CI run 4 (full tier, 92/92 ok, controls violating as
expected), whose job ids and SHA-256s
[`PROVENANCE.txt`](../spec/tla/PROVENANCE.txt) records.

**Runner tests (`test/tla/`, reusable test fixtures).** Package `tla_test`
calls `sandboxguard.Require()` from `TestMain`. It runs the real runner
against a fake scheduler CLI and never runs TLC, java, docker or a real
scheduler. The suite's files:

- **`rig_test.go`** has three parts:
  - the fake `jobsched`. Its scripted answers for `list`, `submit`,
    `status` and `results` come from per-call plan files. Holds make a
    call block. It serves canned run logs built from a verdict table;
  - never-call shims for `java`, `docker`, `podman`, `tlc` and `tlc2`;
  - the `rig` fixture: `newRig`, `private` (a copy of `spec/tla/` that a
    test may change), `plan`, `verdict`, `expectAll`, `hold` and `run`,
    with a clean environment, `TLA_POLL_S=0` and a `TMPDIR` outside the
    repo.
- **`runner_test.go`** covers the job order and tiers, the ok rules, the
  fail-closed paths and the overrides.
- **`interrupt_test.go`** covers the interrupt cases above.
- **`props_test.go`** covers the props column: the shipped launch suite,
  with its cfgs packed from `launch/cfg/`, the props ok rule, and the
  preflight refusals for bad props or an ambiguous or missing cfg.
- **`provenance_test.go`** has four tests:
  - every pin matches its file, and only `.gitattributes`,
    `PROVENANCE.txt`, `ci/run_ci.sh` and `launch/` are unpinned;
  - `launch/cfg/` holds exactly the cfgs `launch/suite.tsv` names;
  - the runner names no checker or container command;
  - `make -n tla` and `make -n tla-print` run only the runner, none of
    `all`, `test` and `test-sandbox` reaches it, and `make tla-print` contacts no scheduler.

**Must use:** every test of `spec/tla/ci/run_ci.sh` or the `tla` make
targets goes in `test/tla/` and uses its `rig` and fake `jobsched`. Never
write another scheduler double, and never call a real scheduler. A test
that changes vendored files uses `rig.private`, never the worktree's
`spec/tla/`. Run the package with `-count=1` after a runner change. The Go
test cache does not track files that only the runner subprocess reads.

### CI lane

`.github/workflows/integration.yml` defines two jobs:

- `linux-integration` — runs `make test-docker EPIC=harness-smoke` on
  `ubuntu-latest` for every PR and push to `main`. Auth env vars come
  from `${{ secrets.ANTHROPIC_API_KEY }}` and
  `${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}`.
- `macos-stub` — runs on `macos-latest`, exits 0 with a stub message.
  Epic 8 (sysctl-based liveness probe) will swap this for a real macOS
  test that exercises the sysctl path. SRD §19 Q7.

The TLA+ model check has no workflow and no CI hook. It is an on-demand
check that runs only through `make tla`, on the fast tier by default (see
[Model check (`make tla`)](#model-check-make-tla)).

No workflow runs `test/driver-scripts/` either: the guard against a bare
`sqlite3` in a case, the `sql.sh` lock tests and the driver's re-run test
run in `make test-sandbox` (see
[Store access from a case: `sql.sh`](#store-access-from-a-case-sqlsh-reusable-driver-helper)).

### Audit standard

Per SRD §17, a Docker test plan is judged on the per-case JSON stream of
a `make test-docker` run, read directly. The run is accepted only when
that stream shows that each case executed and passed.

### Envelope-diff regression harness

`test/envelope-diff/` (Go test package `envelope_diff`) is the SR-7.4 regression gate: the canonical proof that `cmd/agent-director` and `*pkg/api.Client` return structurally-equivalent envelopes for every callable verb.

For each verb in `manifest.CallableVerbs()`, the harness copies a fixture store to a temp directory, runs the freshly-built `agent-director` binary as a subprocess (capturing stdout, stderr, and exit code), then invokes the same verb in-process via `*pkg/api.Client`. Each side is reduced to a single envelope per the shape contract — stdout when exit is 0, stderr when exit is non-zero. Both envelopes are normalized via sorted-key JSON re-encode, filtered through the per-verb non-determinism manifest at `test/envelope-diff/nondeterministic.json`, and structurally diffed. Mismatches are reported as path-style failures (e.g. `.spawns[0].claude_instance_id`).

**File roles** (one per concern, no cross-layer logic):

- `harness.go` — fixture-copy helper and one-time CLI/fake-tmux binary build (`sync.Once`).
- `runners.go` — `runCLI` (subprocess) and `runClient` (in-process) plus per-verb dispatch. `runCLI` passes the child a minimal environment (`HOME`, `PATH` with the fake tmux first) plus each of `TMUX`, `TMUX_TMPDIR` and `FAKE_TMUX_TABLES` (`faketmuxfix.EnvTables`) that is set in the test process (`forwardedTmuxEnv`), so a row's private socket and fake-tmux tables reach the CLI as they reach `runClient`.
- `error_cases.go` — the per-verb error-path table (`errorCases`), with `spawnTmuxErrorCases` appended.
- `error_cases_spawn_tmux.go` — the spawn error rows whose error comes from tmux (SR-20.5): `spawn` / `ErrTmuxUnresponsive` (the create hangs past a short configured create timeout on a minted-id spawn) and `spawn` / `ErrTmuxSessionConflict` (no row for the explicit id, and a session this store labelled for it under another token still runs, so the label scan refuses). Each row calls `usePrivateFakeTmux(t)`, which sets `TMUX` empty, a fresh `TMUX_TMPDIR` and a fresh `FAKE_TMUX_TABLES` directory, and returns the socket spawn resolves and the `faketmuxfix.Tables` the row writes or injects into; no row shares a socket or table with another test.
- `error_cases_pane_tmux.go`, `error_cases_keys_tmux.go`, `error_cases_kill_tmux.go` — the `read-pane`, keys-verb (`send-keys` and `pause`, the same Gone and Leftover rows each) and `kill` error rows whose error comes from tmux: each seeds a live row on a private socket whose fake-tmux table the row writes (or leaves empty), so both runners see the same tmux and nothing is sent or captured.
- `success_expire.go` — `expire`'s two success cases, appended to `successCases`: the `SeedExpireFixture` case (its rows record no socket, so each run looks them up on its private socket, whose empty table reads Gone) and the named case `kept` (`TestEnvelopeDiff_Success/expire/kept`: a finished row whose own labelled session is in the run's fake-tmux table, kept `ours`). Both pin `kept` and `kept_ids` through `want`. A `successCase` may set `name`; a verb has one or more cases (unique by verb and name, found with `successCasesFor(verb)`), and each runs as its own subtest (`subtestName()`).
- `success_pane.go` — the pane verbs' success seed: a live row on its own socket whose table holds the row's own labelled session and pane (`writePaneRowTable`), so both runners reach the pane through the lookup and pane listing and act on it by pane id.
- `success_pretrust.go` — the pre-trust fixture and check for the spawn and resume success cases (SR-22.6). `plantClaudeJSON` is an `extraSetup` hook that writes a `.claude.json` with no folder-trust entries into a run's HOME, so both sides report `ok`. `preTrustMismatch` checks that an envelope carries `pre_trust` equal to the case's `successCase.wantPreTrust` (`success_cases.go`); the diff alone cannot catch the field vanishing from both sides.
- `selectors.go` — path matching with `[*]` wildcard support.
- `diff.go` — JSON normalization and structural diff.
- `manifest_loader.go` — loads and validates `nondeterministic.json`.
- `nondeterministic.json` — per-verb selector manifest; every callable verb in `manifest.CallableVerbs()` is a key, deterministic verbs carry `[]`.
- `nondeterministic.md` — selector grammar and decision tree for classifying fields as deterministic or non-deterministic; consult before adding or removing entries.

`test/envelope-diff/nondeterministic.json` is verb-keyed: every callable verb in `manifest.CallableVerbs()` maps to the field-path selectors excluded from the diff (generated IDs, build stamps, call-time timestamps, and any other documented non-deterministic fields); deterministic verbs carry `[]`. See `test/envelope-diff/nondeterministic.md` for the selector grammar and the decision tree for classifying fields. A missing entry for a non-deterministic field causes the diff to fail. The Task 6 coverage gate, wired into the doc-drift CI check (SR-8.3), enforces that the manifest lists every callable verb in `manifest.CallableVerbs()` — no more, no fewer; adding a callable verb without a corresponding manifest entry fails CI.

**Epic scope.** Task 1 builds the scaffold and unit tests. Per-verb success cases (Task 3) and documented-error cases (Task 4) follow. CI wiring (Task 5) and the nondeterministic.json completeness check (Task 6) close the Epic. `serve` and `hook` are excluded — they are non-callable and carry no envelope contract.

**Error-coverage contract.** Every callable verb with non-empty `ErrorNames` has at least one error-path subtest in `test/envelope-diff/error_cases.go` asserting that CLI and Client envelopes carry an identical `err_name` and a matching `err_description` (prefix-match policy documented in `test/envelope-diff/nondeterministic.md`). `TestErrorTableCoverage` is the CI gate enforcing this: it iterates `manifest.CallableVerbs()` and fails if any verb with non-empty `ErrorNames` lacks a corresponding row in `errorCases` (`error_cases.go`, plus the appended `error_cases_spawn_tmux.go` rows), so new error sentinels cannot land without coverage — analogous to the `nondeterministic.json` completeness gate that enforces every callable verb is represented in the non-determinism manifest. Two entries are explicitly exempted: `ErrTemplateExists` for `make-template` (its `err_description` embeds an absolute temp-dir path that the prefix-match policy cannot normalize across the two fixture copies on Linux; `ErrTemplateNameUnsafe` provides alternative make-template coverage) and `ErrProbeUnsupported` for `find-missing` (no verb returns it; it stays catalogued and in `find-missing`'s `ErrorNames` by SR-1.7, so its row stays in `errorCases` and counts for `TestErrorTableCoverage`, while the row's skip hook skips the subtest on every platform; the empty-store success path covers find-missing on CI).

**`descContains`.** The prefix-match policy compares descriptions only up to the first `:` (what follows, such as a minted id, may differ between the two runs), so the text that shows which path produced an `err_name` goes unchecked. An `errorCase` may therefore set the optional `descContains []string`: phrases that both the CLI and the Client `err_description` must contain. The spawn tmux rows use it; rows without it keep the prefix check alone.

**CI integration.** The harness runs in CI via the `envelope-diff` job in `.github/workflows/integration.yml` on every PR and push to main. The job builds the CLI binary (`tmpbin/agent-director`) and the fake-tmux helper (`tmpbin/faketmux/tmux`) from the commit under test, then runs `go test ./test/envelope-diff/...` with `AGENT_DIRECTOR_TEST_BINARY` and `AGENT_DIRECTOR_FAKE_TMUX_DIR` set to absolute workspace paths so the test process does not pay the build cost a second time. The same step sets `BYPASS_CONTAINER_FOR_AGENT_DIRECTOR_TESTS` — the package carries `sandboxguard.Require()`, and the hosted runner is ephemeral with no real store to protect; see [Sandbox guard and the CI bypass](#sandbox-guard-and-the-ci-bypass) for the placement rule.

### Go smoke test

`test/smoke/go/` is the canonical home for the Go-side smoke test. Its purpose is to exercise every callable verb through `pkg/api.Client` exactly as an external consumer would — no subprocess invocations, no access to `internal/` implementation details.

**Verb coverage.** `manifest.CallableVerbs()` drives the verb list (15 verbs). `help`, `serve`, `trail-emit` and `hook` have `Callable=false` and are excluded.

**Import constraint.** The smoke target imports only `pkg/api`, `pkg/api/manifest`, `pkg/api/apitest` (for `SeedSpawn`), and `internal/testsupport/*`. Imports of `internal/api`, `internal/store`, or any other `internal/` package are prohibited and enforced at test time by `test/smoke/go/import_graph_test.go` (Task c8). This keeps the smoke test honest as a consumer: if `pkg/api` does not expose something, the smoke test cannot reach around it.

**No verb chaining.** Each subtest receives a fresh store and a fresh tmux recorder. Preconditions (e.g., a live Spawn row required by `status` or `send-keys`) are injected by the `internal/testsupport` seeders or `apitest.SeedSpawn` (a `pending` row with a launch start) — never produced by calling another verb first. This makes subtests independent and order-invariant; a subtest failure cannot cascade into later subtests through shared state.

**Seeder files.** `seeders.go` holds the spec and the registry;
`seeders_verbs.go` the verbs that need no tmux session; `seeders_tmux.go`
the verbs that reach the row's session (`send-keys`, `read-pane`, `kill`,
`pause`), whose Recorder holds the row's own labelled session
(`tmuxfix.NewRecorderForReadPane` / `NewRecorderForPause` /
`SeedRowSession`), so each reaches the agent's pane by id. `read-pane`'s
`Pane` check compares the result with the text the row's own pane
captures. `expire`'s seed (`seedExpired`) is an `ended` row with a fixed
old `ended_at` on `apitest.TestSocket`, whose Recorder server holds one
unrelated unlabelled session, so the lookup reads Gone; its check
(`seederSpec.Expired`, `assertExpiredRow`) expects the row deleted and
`kept_ids` `[]`.

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
`SeedClosedPermissionRequests`) — do NOT
hand-roll raw-connection backdating in new tests; call this instead. It
deliberately does not hardcode the 86400s default or restate the
non-positive→default fallback (that lives solely in
`config.Relay.EffectiveTimeoutSeconds`); callers choose `age` relative to
the effective window. Seed the open row first (e.g. via
`SeedCheckPermission`, which uses `TestRequestTokenA`).

**Agent-hook helpers (`agent_hooks.go`, SR-22.9).** Every hook write is
gated on the hook's parent being the row's recorded pane process, so the
seeders and tests that need a hook to apply go through these helpers and
never build a `store.HookGate` by hand:

- `WithSeedPane(dbPath, id, writes func(gate store.HookGate) error)` runs
  a seeder's gated writes as the row's own agent: it records the seed pane
  (`launchfix.TestPanePID` with `SeedPaneStarttime`) when the row records
  none (else uses the recorded pane, with `SeedPaneStarttime` for a NULL
  start), passes the matching gate (event `SeedTrigger`), then writes the
  row's pane and process identity back exactly. Its raw writes advance no
  `row_version`.
- `SeedAgentWrites(s, gate, id, sessionID, state)`: the gated soft refresh
  that records a session id (+1), then the gated transition (+1, none for
  `pending`). `SeedSessionStart(s, gate, id, sessionID)`: a gated
  SessionStart (+1; a different session id archives the outgoing pair).
- `FireHook(s, id, event, sessionID, parentPID, parentStart, opts...)`
  fires one event through the gated store path, classified by
  `hook.ClassifyEvent`; `ApplyAgentHook` / `ApplyForeignHook` wrap it with
  `AgentHookParent` (the row's pane pid and start) or
  `ForeignHookParentPID`. `HookOption` / `HookTranscript` add a transcript
  path.
- Seeders that take only a `*store.Store` find its file through
  `RegisterStorePath` / `StorePath`. `OpenTempStore` registers every store
  it opens; a fixture that opens its own store registers it first, or the
  seeding test fails with a message naming the helper.

`storefix` imports `internal/hook` for the classifier, so tests inside
package `hook` (white-box) cannot use it; hook tests that need these
helpers live in package `hook_test` (their shared parent-process fakes
are in `internal/hook/hook_parent_fakes_test.go`). Inside
`internal/store`, the white-box counterparts are in
`internal/store/agent_pane_helpers_test.go` (`insertAgentRow`,
`agentGate`, `foreignGate`, `agentHook`, `agentSessionStart`, …).

**`InjectWriteFailure(t, dbPath, kind, instanceID)`** makes one kind of
write to one id's rows fail on the concrete `*store.Store` (SR-20.3). The
five kinds (`WriteFailureKind`, an alias of `writefailfix.Kind`) are:

- `WriteFailReuseArchive`: an insert or update of the id's
  `session_history` entry (`ResetForReuse`'s archive, whose failure holds
  `ErrReuseArchive`).
- `WriteFailReuseReset`: an update moving the id's finished row (`ended` or
  `missing`) to `pending` (`ResetForReuse`'s reset).
- `WriteFailReusePermissionDelete`: a delete of one of the id's
  `permission_requests` (`ResetForReuse`'s deletion), including one done by
  the cascade when its `spawns` row is deleted.
- `WriteFailReuseRestore`: an update moving the id's `pending` row to
  `ended` or `missing` (`RestoreAfterFailedReuse`). This includes plain spawn's held-launch end write
  (`store.EndHeldLaunch`), so it is the kind for that write's store-error
  case (SR-5.8; the row stays `pending`, "the new row could not be ended
  and stays pending"). **Must use:** a test of that store error installs
  this kind; do not add a separate end-write kind.
- `WriteFailLaunchIdentity` (`writefailfix.LaunchIdentityWrite`): the
  launch identity write (`RecordLaunchIdentity`): any update of the id's
  `spawns` row whose SET names any of the three server identity columns,
  `pane_id` or `pane_pid`, and leaves `state` unchanged, whether or not
  the values differ. It never matches the insert or a hook write (a gated
  hook write sets only `pane_starttime` of those columns, which the
  trigger does not list), but it does match `SeedSpawn`'s option and
  default updates and the seeders' seed-pane writes (`WithSeedPane`), so
  install it after seeding. A later adoption write of the same shape would
  match it too.

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

### cwdfix: the working-directory fixture (reusable test fixture)

`internal/testsupport/cwdfix` moves a test's process into a fresh temp
directory for the rest of the test (b.nje), standing in for go 1.24's
`t.Chdir`, which go 1.22 lacks. The package doc comment carries the
detail.

- `Temp(t) string` makes a fresh `t.TempDir()` the working directory
  until `t` ends, restores the previous one in `t.Cleanup`, and returns
  the temp dir.
- The working directory is process-wide, so a test using it must not be
  parallel. `Temp` enforces that the way `t.Chdir` does: it sets `$PWD`
  with `t.Setenv`, which panics in a parallel test and makes a later
  `t.Parallel` call panic.
- Users: `TestPreTrustRefusesUnusableConfigDir`
  (`internal/spawn/pretrust_test.go`) and `seedRelativeTrustConfig`
  (see [pkg/api pre-trust fixture](#pkgapi-pre-trust-fixture-package-internal-test-fixture)),
  which check that a relative `CLAUDE_CONFIG_DIR` touches nothing where
  it would resolve from the process cwd, and
  `TestTildeStorePathWithoutHOMERefused`
  (`internal/store/expand_tilde_test.go`), which checks that a `~/` store
  path refused for an unset or empty `HOME` creates nothing in the cwd.

It is a leaf package (standard library only; it imports nothing from
agent-director), so any test package can use it.

**Must use:** a test that needs the process in a fresh working
directory calls `cwdfix.Temp`; do not hand-roll `os.Chdir` with a
restore. Two earlier tests still do: `chdirFor` in
`test/realtmux/socket_resolution_test.go` and the "relative
TMUX_TMPDIR" case in `pkg/api/spawn_launch_test.go`.

### Which tmux test double to use

- In-process verb tests: `tmuxfix.Recorder`
  ([tmux test doubles](#tmux-test-doubles-replay-catalogue-recorder-and-clock-reusable-test-fixtures)),
  paired with `apitest.SeedSpawn` rows ([Seed* contract](#apitest-seed-factory-contract-reusable-test-fixtures)).
- Subprocess tests (CLI, MCP, envelope-diff, TypeScript): `test/fake-tmux`, which Go tests get and drive through `faketmuxfix` ([fake-tmux](#testfake-tmux-the-subprocess-tmux-double-reusable-test-fixture)).
- Real-tmux proof, sandbox only: `test/realtmux` ([realtmux](#testrealtmux-real-tmux-proof-of-the-client-reusable-test-fixtures)).
- Reply wording always comes from the replay catalogue, and timing tests use `tmuxfix.Clock`.
- The server check's process reader (`tmux.ProcChecker`): `procfix.Checker` in in-process tests ([procfix](#procfix-the-process-checker-fake-reusable-test-fixture)); the production reader, `probe.NewProcChecker()`, in `test/realtmux`.

**Must use (server isolation, SR-20.3):** a test that reaches a tmux
server, real or fake, uses a per-test `TMUX_TMPDIR` with `TMUX` unset, or
puts the fake first on `PATH`, so no two tests share a server. Existing
per-package fixtures that do this: `newSpawnEnv` / `buildSpawnEnv` in
`pkg/api/spawn_test.go` (temp `HOME`, per-test `TMUX_TMPDIR`, `TMUX`
unset); `spawnTmuxTmpdir` in `cmd/agent-director/spawn_cli_test.go`
(`<home>/tmux-tmpdir`, which `runSpawnCLIEnv` passes to the child);
`newResumeEnv` in `pkg/api/resume_fixture_test.go` (see
[pkg/api resume fixture](#pkgapi-resume-fixture-package-internal-test-fixture));
and `usePrivateFakeTmux` in `test/envelope-diff/error_cases_spawn_tmux.go`
(fresh `TMUX_TMPDIR` and `FAKE_TMUX_TABLES`, `TMUX` empty), which the spawn
tmux error rows call and `TestEnvelopeDiff_Success` calls before the CLI run
and again before the Client run, so the two runs never share a server or
fake-tmux tables.

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
  `Silent()` and `reply/wording-on-stdout-exit0` are not lookup entries:
  the lookup always prints its identity line first, so to it they are
  malformed answers, as `lookup/empty-output` and `lookup/identity-missing`
  are.
- `LookupAnswers()`: the F1, F2 and F9 lookup answers, I2's zero-session
  server, plus malformed ones; `PaneListings()`; `CreateReplies()`;
  `Captures()`. Every lookup answer begins with the server identity line
  (`IdentityLine`), I2's included (LFR H5; b.47f). The malformed ones
  include an identity line that is missing, misplaced, disagrees with a
  session line, has a pid or start time that does not parse
  (`lookup/identity-pid-unparseable`, `lookup/identity-start-unparseable`)
  or a field missing or extra (`lookup/identity-missing-field`,
  `lookup/identity-extra-field`), and empty output. Each malformed lookup
  entry carries the client's fixed `FirstLine` for the rule it breaks (not
  an identity line, a bad session line, a session line in the scope
  section, or a session line disagreeing with the identity line), which
  quotes no line. `lookup/I2-identity-shaped-scope-value` is a scope value
  shaped like an identity line, which counts as a scope value.
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
- Builders for composed data: `IdentityLine(serverPID, serverStart)`,
  `SessionLine`, `LabelValue`,
  `ChainLabelValue`, `PaneLine` (its last argument the raw `@ad_pane`
  value, `""` for none), `PaneLabelValue(token, paneID)` (`<token> <%N>`),
  `ChainPaneLabelValue(token)` (`<token> #{pane_id}`), `Listing` with
  `PaneListing` (a pane and its raw value), `CreateReplyLine`, `Valid`,
  `Answer`; the
  tokens `Token` and `OtherToken`; the store ids `StoreID` and
  `OtherStoreID` (fixed 16-hex values for catalogue entries).
- Every label is in the five-field form `ad1 <token> <$N> <instance id>
  <store id>`. `LabelValue(token, sessionID, instanceID, storeID)`,
  `ChainLabelValue(token, instanceID, storeID)` (only the instance id
  format-escaped, as the client's `escapeFormat` does: each run of `#`
  doubled, except a run before `[`) and `Valid(token, instanceID,
  storeID)` all take the store id last. `LookupAnswers` and `StoredNames` use `StoreID`. `LabelShapes`
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
  unbound), `StopServer`, `EmptyServer` (every session removed, with no
  call recorded; the server stays bound with its identity, scope values
  and id counters, as tmux with `exit-empty` off; b.47f), `Servers` /
  `Server` for a process-checker fake.
  Seeding: `SeedSessions`, `SetCapture`; `Sessions` reads the table back.
- **Shared panes** (`SeedPane.Shared`). A pane the server already holds can
  be listed in another session too, as tmux does for a grouped session, a
  linked window or a viewing session: seed a `SeedPane{ID: <held pane id>,
  Shared: true}` (window and index are this session's; pid and `AdPane` are
  the pane's own). The pane listing shows it once per session with that
  session's id; a pane kill removes it from every session (a session left
  with no pane is removed); a session kill removes only that session's
  listing, so the pane lives on while another session lists it; the pane
  label is set on every listing. **Must use** it to model a shared agent pane
  (the Gone-path viewer pane, grouped sessions); never seed a pane id twice
  without it.
- **Default answers from the table.** Lookup, pane listing, both kills,
  sends, capture and label by id act on the table. The lookup's answer
  carries the bound server's pid and start with or without sessions, as
  the client's identity line does (LFR H5; b.47f). The create starts a
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
  next matching call. `AfterCall` hooks run on every matching call;
  `AfterCall(tmuxfix.AnyCall, fn)` matches every kind, so one hook sees
  the text, Enter and key sends in call order (the pane-input model
  below uses it).
- **Call order.** Each socket-taking call runs its table effect or scripted
  result, then the virtual-time charge, then the session hooks, then the
  after-call hooks (outside the Recorder's lock, so they may call back into
  the Recorder or the store), then returns.
- **Recorded calls.** `SocketCalls` / `SocketCallsOf(call)` return
  `SocketCall` records. A create or label-by-id record carries the label's
  `Token`, `InstanceID` and `StoreID`; a label-by-id record also carries
  `PaneID`, the pane its pane label is set on. `SendKeysPane` records
  `CallSendText` (with `Text`) and, when it makes one, `CallSendEnter`;
  `SendKeyPane` records `CallSendKey` with `Key`, the key name (`pause`'s
  `C-u`).
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
- **Name-based method.** `HasSession` is the only one left, kept because
  `api.TmuxClient` still declares it; no verb calls it. It is unscripted:
  it records a `CallHasSession` call (`Calls`,
  `CallsOfKind(CallHasSession)`) and always answers not found, is never
  charged and runs no hooks. `NewRecorderForResume` is kept and returns an
  empty Recorder (`NewRecorder()`), whose lookup lists no session. The
  name-based
  send and capture are gone: `read-pane`, `send-keys` and `pause` tests
  configure the row's own labelled session in the table and read the
  id-based calls (`SocketCallsOf`), with per-pane capture text from
  `SetCapture`. `Reset` clears recorded calls and scripts; tables, hooks
  and virtual time stay.

**Must use:** in-process verb tests use the Recorder, never a hand-written
`TmuxClient` fake.

**Clock and virtual time** (`clock.go`, `WithVirtualTime`).
`tmuxfix.Clock` (`NewClock(start)`, `Now`, `Advance`) is the shared test
clock that verbs under test read. It is safe for concurrent use, and
`Advance` with a negative duration steps it back.
`Recorder.WithVirtualTime(c, t)` binds the Recorder to `c`: every
socket-taking call, whatever its result, advances `c` by its class's
timeout from `t` (query: lookup and pane listing; action: kills, text,
Enter and key sends, capture, label by id; create: the create). A zero field of `t`
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

**Pane-verb constructors** (`seeders.go`). Both read the row from the
store, seed its own labelled session through `SeedRowSession` (the row's
recorded pane included) and return a `procfix.Checker` that answers every
running server, the agent pid and every seeded pane pid alive; they share
one core (`seedRow`) and one label choice, `RowLabel` (`RowLabelCurrent`,
the default, gives Ours; `RowLabelOld` Leftover, the pane carrying
`OtherToken`; `RowLabelForeign`; `RowLabelOtherStore` Gone;
`RowLabelNone`, `RowLabelMalformed`, `RowLabelBorrowed`).

- `NewRecorderForReadPane(t, dbPath, instanceID, paneText, opts...)`: the
  row's pane captures `paneText`. Options: `WithReadPaneVirtualTime(clock,
  timeouts)`, `WithReadPaneCreated(epoch)`, `WithReadPaneName(stored)`,
  `WithReadPaneLabel(RowLabel)`, `WithReadPaneLeftover(name, text)` (an
  extra leftover session, old label, one pane with `@ad_pane`
  `OtherToken`; repeatable).
- `NewRecorderForPause(t, dbPath, instanceID, opts...)`: the same without
  pane text, so on a `waiting` row `pause` gets Ours and sends `C-u`,
  `/exit` and Enter to the row's pane by id. Options: `WithPauseVirtualTime`,
  `WithPauseCreated`, `WithPauseName`, `WithPauseLabel`,
  `WithPauseLeftover(name)`. Nothing ends the row: the test ends it (an
  after-call hook on `tmux.CallSendEnter` applying the agent's SessionEnd)
  or the wait runs to its timeout.

**Must use:** tests outside `pkg/api` seed a pane verb's row session
through these constructors (or `SeedRowSession`), and subprocess tests
through `ts-helper seed-row-session` or the fake's table helpers; never
hand-built label strings.

`tmuxfix` imports `internal/tmux`, `internal/config` and `internal/store`.

### procfix: the process-checker fake (reusable test fixture)

`internal/testsupport/procfix` is the shared fake of the start-time reader
(SRD SR-20.3): `*procfix.Checker` satisfies `tmux.ProcChecker` (and so has
`probe.ProcChecker`'s method) from a per-pid process table the test sets,
and records what was asked of it. The package doc comment carries the
detail.

- `New()` gives an empty table; the zero `Checker` is the same. An unlisted
  pid answers gone.
- `Set(pid, Process)` puts or replaces a pid's entry. Calling it between
  lookups scripts a race (the process dies, the pid is reused).
- `Process` values: `Alive(start)`, `Gone()`, `Zombie()` (answers gone) and
  `Unreadable()` (answers not known), each a `State`
  (`StateAlive`, `StateGone`, `StateZombie`, `StateUnreadable`);
  `Process.WithEnv(env)` gives it an environment. `start` is empty whenever
  the answer is not alive, as the real reader promises.
- `StartTime(pid)` answers from the table and records the pid;
  `StartTimeCalls()` returns every asked pid in order, so a test proves the
  reader was not called (a server match, no recorded identity) or called
  once.
- `Env(pid)` is the only way to read an environment and counts each read;
  `EnvReads() == 0` proves no environment was read.
- Every method is safe for concurrent use.

It is a leaf package (standard library only; it mimics no
environment-reading judgement), so the external tests of `internal/tmux` (package
`tmux_test`) import it with no cycle and a `go list -deps` check that code
under test does not reach `internal/probe` stays meaningful. Its compile-time `tmux.ProcChecker` check
lives in its external test file. Start-time values come from
`procstarttimefix`, never literals.

**Must use:** in-process tests of code that takes a `ProcChecker` use
`procfix.Checker`, never a hand-written fake. Real-process proof
(`internal/probe`'s tests, `test/realtmux`) uses `probe.NewProcChecker()`.

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

It answers the Phase 1 call set: the lookup (the identity read
`display-message -p` with the server's `#{pid}` and `#{start_time}`, with
or without sessions, as tmux with `exit-empty` off prints it, and the
no-socket reply on a socket with no server (see per-socket tables below); then
`list-sessions -F` with `#{@ad_owner}` resolved by tmux's scope precedence;
then the three `show-options` scope reads; an invocation starting with
`display-message` is a lookup for injections), `list-panes -a`, `kill-pane` and
`kill-session` by id, the `send-keys` forms by pane id (the literal
text, the Enter, and, beside the call set, another key such as `pause`'s
`C-u`), `capture-pane` by pane id, `new-session`
with its `-P -F` reply and chained `set-option`s, and `set-option` by id.
Commands run in order; the first failure ends the invocation, as in tmux.
Its format expansion (`-F`, and the `display-message -p` format) follows tmux's escape rule: `##` is `#`, except that a
run of two or more `#` directly before `[` (a style) stays as written.
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
test keeps its own tables even on the shared test socket. On a socket with
no server (no table, or a table with no server) nothing answers the
lookup: it fails with the catalogue's no-socket reply, `error connecting to
<socket> (No such file or directory)` on standard error with exit 1, as
real tmux does there and as the Recorder's `FailNoSocket` models (b.47f);
the fake never names a server that does not run. The pane listing there
is still empty with exit 0. A table's server stays when its last session
goes, as with tmux's `exit-empty` off, and the lookup then names it with no
sessions. Every write takes a lock and replaces the file atomically.

**Must use:** tests read and write tables only through
`faketmuxfix.Tables{Dir}` (`Path`, `Env`, `Write`, `Read`, `Update`,
`Inject`). They never write a table file by hand.

**Injections** (per socket, per call kind, stored in the table). The call
kinds are `tmuxfix.AllCalls()`: the nine of SR-2.1 and the key send
(`tmux.CallSendKey`, b.9o4). The fake classifies each `send-keys` by its
form: `-l` is the text send, a final `Enter` the Enter send, any other key
(`pause`'s `C-u`) the key send. So an Enter injection never hits the
`C-u`, and a key-send injection hits only it.

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
`FAKE_TMUX_FAIL_NEWSESSION_NAME` applies to the socket-form create only,
which then prints the catalogue's `duplicate session: <stored name>`
wording. The socket form logs every invocation to `FAKE_TMUX_LOG` (one
element per line, then `---`); its `capture-pane` prints the pane's table
capture text, else `FAKE_TMUX_PANE_OUTPUT`, else the stub.

**No wording of its own.** Every reply the fake prints comes from the
replay catalogue. Its only literal output is the capture stub, which is
pane content, not a reply.

**Socket-less argv.** Argv without a leading `-u` has only two answered
forms. The bare invocation (no subcommand) exits 0, so `command -v tmux`
probes succeed. `has-session` exits 1 with no output, as for an absent
session; it is kept only for `HasSession`, which no verb calls. There is no
socket-less `new-session`, `send-keys`, `capture-pane` or `kill-session`.
Every other socket-less invocation, those four included, is logged to
`FAKE_TMUX_LOG` in the same format and refused with the catalogue's
`tmuxfix.NoServer` entry (`reply/no-server`) for the default socket, on
standard error with exit 1, as tmux answers when no server runs on the
default socket. A client call that names no socket therefore fails in
tests instead of passing with exit 0 (SRD SR-2.1). No table is read or
written.

**Build helper.** `faketmuxfix.Binary(t)` builds the fake once per test
binary and returns the path of a file named `tmux`; `faketmuxfix.Dir(t)`
returns its directory for a `PATH` prepend. **Must use:** new Go tests get
the fake only through `faketmuxfix.Binary` / `Dir`.
`cmd/agent-director/spawn_cli_test.go`'s `buildFakeTmux` is a one-line
wrapper over `faketmuxfix.Dir`; the older builder `buildFakeTmux` in
`test/envelope-diff/harness.go` stays as it is.

**Log reader.** `faketmuxfix.ReadLog(t, path)` parses a `FAKE_TMUX_LOG`
file into one argv per invocation, in call order, `argv[0]` included; it
returns nil when the file does not exist and fails the test on a record
with no `---` terminator. `test/fake-tmux`'s `readLog`,
`cmd/agent-director`'s `fakeTmuxInvocations` and `pkg/api`'s
`fakeTmuxArgvs` go through it. **Must use:** every Go test that reads the
fake's invocation log uses `faketmuxfix.ReadLog`; never parse the log
format by hand. The Makefile's
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
and `harness_proc_test.go` (polling, `/proc` readers). The shared lookup
fixture is in `lookup_helpers_test.go`.

**Exec wait.** `waitExeced(t, pid, argv0)` (`harness_proc_test.go`) polls
until the process's `argv[0]` has base name `argv0`; until a freshly forked
pane process execs, `/proc` shows the tmux server's argv and environment.
Its timeout dump never fails the test on a vanished pid. **Must use:**
every test that reads a pane process's `/proc` (argv, environment,
locale) or relies on what the pane's program set up after it started (a
signal trap, for example) calls `waitExeced` first; never read `/proc`
right after a create, and never write another exec poll.

tmux 3.2a is covered
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
- `semicolon_injection_test.go` (the argv escape of `internal/tmux`): the
  production `spawn` and `resume` through `pkg/api`, on the spawn fixture
  of `spawn_test.go`, with a stand-in `claude` on `PATH` that records its
  argv. Claude args `x;` then `kill-server`, or `x;` then `run-shell touch
  <file>`, run no second tmux command (a bystander session and its server
  survive, no file appears), and the agent receives the args literally,
  `x;` included. A session name, a cwd, an extra env value or an instance
  id ending in `;` spawns, labelled as usual, and tmux and the pane hold
  each value with its final `;`.
- `format_injection_test.go` (the format escape of `internal/tmux`): the
  production `spawn`, and `resume` of an ended row seeded with the same
  cwd (the spawn fixture's `seedResumable`), through `pkg/api`, in
  directories whose names hold format sequences: `#(touch <file>)` (alone
  and after a style `#[y]`), `#{session_name}`, `#S`, `##`, a trailing `#`,
  `#;`, the styles `#[y` and `##[y`, and a leading `#`. Each spawn is
  labelled as usual, the row's cwd, `#{session_path}` and the pane's
  `#{pane_current_path}` all equal the canonical cwd exactly, and no
  `#(...)` job creates its file.
- `kill_test.go` and `kill_names_test.go` (SR-6.1, SR-20.7): the production
  `kill` against real tmux. With the agent's window shared by a grouped
  session or a linked window, or a teammate's split pane beside it, `kill`
  succeeds only once every pane process is gone; a pane process that
  ignores SIGHUP (the agent's or a teammate's) is `ErrTmuxKillFailed`
  naming its pid; a `pending` row beside a session labelled for an earlier
  launch of its id is refused and the session runs on; a lost reply is
  adopted by `@ad_pane` with `base-index` and `pane-base-index` 1; `$` and
  `\` names, a prefix neighbour, a non-ASCII name under a hostile locale and
  a socket at mode 000 (`ErrTmuxNotAvailable`).
- `pane_verbs_names_test.go` and `pane_verbs_targets_test.go` (SR-3.7,
  SR-7.2, SR-20.7; Appendix E.9 T2a): `read-pane`, `send-keys` and `pause`
  through the production client, on the kill fixture. With the socket at
  mode 000 each verb is `ErrTmuxNotAvailable` naming the socket, and
  nothing changes. For the nine `$` and `\` names kill uses, beside the
  labelled bystanders `$0` and `$1`, `read-pane` returns the row's own pane
  and `send-keys` types into it, and the bystanders are unchanged. Sessions
  named `$7` and `%9` are never reached: a row whose recorded pane id is
  the never-issued `%9` is `ErrTmuxSessionConflict` (`DescPaneNotFound`), and
  a row whose own session `$7` ended, with a raw `$7` now holding the name,
  is the verb's gone error (`DescPaneGone`). With `base-index` and
  `pane-base-index` 1 and a lost reply, all three verbs reach the agent's
  pane by id. `send-keys` and `pause` write the adoption; `read-pane` writes
  nothing.
- `find_missing_test.go` (SR-3.8, SR-11.1, SR-11.3, SR-20.7): the
  production `find-missing` against real tmux, on the kill fixture. A dead
  agent whose session `remain-on-exit` keeps is marked `missing` with no
  tmux call and the session stays; under `LC_ALL=C` and with no locale
  variables, lost-reply rows named `ü-x` and `agent-ü1` adopt their panes
  and stay live while a sessionless control row is marked; with the socket
  at mode 000, rows with no process identity stay live with
  `process_not_seen_tmux_unchecked` after one refused tmux call.
- `unusable_name_procedure_test.go` (SR-18.17 steps 2 to 5, SR-20.7,
  Appendix E.10 N6; AC-DOC-18): the README "Operator actions" item "A row
  whose recorded name cannot be used" on real tmux, on the kill fixture.
  Each catalogue N6 name (holding `.`, `:` or a byte that is not valid
  UTF-8) is listed under its stored form with its session id, and the
  exact-name target of `dot.x` does not reach `dot_x`'s session. For a
  `dot.x` row whose session is labelled by id, or carries only the row's
  id in its environment, the procedure finds the session by id, ends it
  by id (its pane process goes), and `adminapi.Delete` (the operator
  tool's `delete`) removes the row. A `dot_x`
  session labelled for another row is left running, with that row
  unchanged, also when it carries the `dot.x` row's id in its environment
  (the label wins over the hint). An unlabelled bystander session runs
  throughout.
- `expire_test.go` (SR-12.2, SR-3.10, SR-20.7): the production `expire`
  against real tmux, on the kill fixture, through the built CLI
  (`buildHookCLI`, `expire --older-than 0d`) rather than the in-process
  client: the kept reasons exist only in the trail, and the trail writer
  resolves its path once per process, so a fresh CLI process per run
  writes under the test's own `HOME`. `trailRecords(t, dir)` reads that
  trail; `expireRun.assertLists` and `assertKept` check the lists and
  each kept row's one `ad.expire.kept`. Under `LC_ALL=C` and with no
  locale variables, finished rows named `ü-x` and every `$`/`\` catalogue
  name are kept `ours` while a sessionless row is deleted; with the socket
  at mode 000 every row is kept (the first `tmux_unavailable`, the rest
  `tmux_skipped`) after one refused call.
- Lookup (SR-3.3, SR-3.4, SR-3.7, SR-3.8, SR-3.10, SR-3.12, SR-3.13,
  SR-20.7; AC-LKP-04 and the real-tmux halves of AC-LKP-19 and AC-LKP-20):
  one `tmux.Lookup` per case against real tmux, through the lookup fixture
  below. These files drive only the lookup (and, for `remain-on-exit`, the
  shared process judgement), never `Sweep` or the pane listing. Four files:
  - `lookup_ownership_test.go`: the Ours side. A row with the create's
    server identity reads Ours (`ours`, server status `match`, no
    `Disagree`, `Adopt` false); a row with no recorded identity is also Ours
    with status `unknown` and `Adopt` true. Both rows report the create
    reply's server as the answering server. A renamed
    session is still Ours by the same session id, with no `Disagree`; the
    new name's holder is that session (class current) and the old name is
    not held. A grouped viewer session (`new-session -t`) and a session
    holding a linked window share the agent's pane id, but only the agent's
    own session is Ours; the viewers are never Ours or Leftover. Killing
    only the agent's session reads Gone while its pane process still reads
    alive. With `remain-on-exit` on and the pane dead (`pane_dead` 1), the
    row is still Ours while the process judgement reads gone.
  - `lookup_provenance_test.go`: paths that never make a session Ours. An
    `@ad_owner` value set alone at `-g`, `-s` or `-gw` gives Can't tell
    `provenance_conflict` with `scope_value`, both next to the agent's own
    label and when it is the exact label an unlabelled session would carry.
    Unsetting it returns the labelled shape to Ours and the unlabelled shape
    to Gone. A label carrying a raw newline gives `provenance_conflict` with
    `scope_value` when its session lists last, and a malformed answer
    (`cant_tell`) when it lists before another session; never Ours or
    Leftover either way. In the `cant_tell` case `Cause` is
    `FailUnrecognized` and its error quotes no label text. AC-LKP-04: an
    unlabelled session is created with `-e AGENT_DIRECTOR_INSTANCE_ID=<id>`
    and each case looks up two rows, the row and another row. In one case
    the row's id is in the session `-e`. In the other, the row's id is in
    the global environment (`set-environment -g`) and the other row's id
    is in the session `-e`. In each case both rows read Gone, with the
    session as the name's holder at class none.
  - `lookup_server_test.go`: the server check. After `kill-server`, once
    the recorded server reads gone, the lookup is Gone (status `restarted`,
    no `Disagree`). A new server on the same socket reads `restarted` with
    `server_restarted`, never `different_server`: Gone unlabelled, Ours
    when relabelled by id. A new server re-bound on the socket path while
    the recorded one still runs reads Can't tell `different_server` with
    `server_mismatch`, never Ours, even with the row's label. Before the
    re-bind the old server reads alive and carries no `AGENT_DIRECTOR_*`
    variables; after it, the old server is still Ours on the moved socket.
    With tmux's `exit-empty` off (b.47f), the recorded server left with no
    session, still running, reads Gone (status `match`, no `Disagree`),
    and a new server re-bound on the socket and left with no session while
    the recorded one runs reads `different_server` with `server_mismatch`.
    A row with no recorded identity accepts whichever server answers
    (status `unknown`; Ours with `Adopt` true only when that server's
    session carries the row's label). Every row checks the answering
    server's identity. Cases pass whether the killed server is reaped or a
    zombie.
  - `lookup_store_test.go`: two stores on one server. For both stores, each
    with and without a spaced id, the production create writes the
    five-field label ending in the store id it was given; the creating
    store's row reads Ours and the listed label parses as `Valid`. Another
    store's session with the row's id reads Gone for the row with the row's
    token, another token or none, and with no recorded identity (status
    `unknown`); never Ours or Leftover, no leftovers, `Adopt` false. As a
    name holder it has class other store, whose case words are "another
    agent-director store". With both stores' agents under one id, with
    different tokens and with the same token, each store's row is Ours with
    its own session only, and the other store's session holds the other
    name at class other store. Renaming either or both sessions changes
    nothing. A row with an empty `StoreID` reads Gone, with the holder at
    class other store, for this store's token, the other store's token and
    no token.

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
- Lookup fixture (`lookup_helpers_test.go`): `newLookupFix(t)` returns a
  `*lookupFix` (the embedded `*realTmux`, `Client` from `newClient()` and
  `PC` = `probe.NewProcChecker()`, the production start-time reader).
  `f.agent(t, createSpec)` and `f.agentOn(t, rt, spec)` run the production
  create and return an `agent` (`created`, `Socket`, `PaneStart`, and
  `Server`, a `serverIdentity{PID, Start, Starttime}` taken from the create
  reply plus the reader, never from a lookup). Another store's agent is
  `createSpec{StoreID: tmuxfix.OtherStoreID}`. `f.serverOf(t, rt,
  sessionID)` reads the identity of a server a raw starter began;
  `f.startOf(t, pid)` reads a live process's start time. Rows:
  `a.row(opts...)` and `rowFor(instanceID, token, socket, srv, opts...)`
  build a `tmux.Launch` whose store id is `tmuxfix.StoreID` by default
  (whatever store the agent was created for), with options
  `withoutIdentity()`, `withServer(srv)`, `withInstanceID(id)`,
  `withToken(tok)` (`""` is no token), `withStoreID(id)` (overrides the
  store id) and `withSocket(sock)`. `a.pane()` is the recorded pane identity for
  `f.judge(id)` (`tmux.JudgeProcess` with the production reader).
  `f.lookup(row, holder)` is the one `tmux.Lookup` call (`""` for no
  holder). `f.rebind(t, rt, spec)` moves the running server's socket
  aside (still tracked for cleanup) and starts a new server at the old
  path, returning the aside world and the new agent. `f.endServer(t, rt,
  srv)` sends `kill-server` and waits until `srv` reads gone (a zombie
  counts). `assertResult(t, what, got, wantResult{...})` compares
  `Verdict`, `CantTell`, `Token()`, the Ours `Session`, `Leftovers`,
  `Holder`, `HolderClass`, `Server` and `Disagree` strictly (empty means
  none) and never prints a label value; check `Adopt`, the answering
  server's identity and `Cause` directly.
- Kill fixture (`kill_helpers_test.go`; cases in `kill_test.go` and
  `kill_names_test.go`): `newKillFix(t)` is the lookup fixture plus a temp
  HOME holding a fresh store (`DBPath`, `StoreID`). `f.liveRow(t,
  killRowSpec{...})` runs the production create (labelled by id when the
  name needs it, as spawn does) and seeds the matching live row
  (`killRowSpec.PaneID` records a given pane id in place of the reply's,
  such as one the server never issued);
  `f.seedRow` seeds a row with no agent process recorded, and
  `f.assertRowUnchanged` checks every column still holds its seeded value.
  `f.open(t, wait, tmuxCommand)` opens the production client (`api.New`
  on the fixture's store, with the client timeouts and
  `kill_exit_wait_ms` = `wait`, default `killDefaultWait`; `tmuxCommand`
  `""` is tmux on `PATH`). `f.kill(t, id, wait)` runs one `kill` through
  it and returns a `killCall` (`Res`, `Err`, `Took`) with
  `assertSuccess(t, sent)` and `assertRefused(t, name, descCase,
  forbid...)`. `assertRefused` calls the shared
  `assertVerbError(t, verb, err, name, descCase, forbid...)`, which every
  real-tmux verb test uses for a refusal: the error matches exactly one
  catalogued name, `name`, and `apitest.AssertDescription` passes.
  `dollarBackslashNames(t)` gives the catalogue's stored forms of the nine
  `$` and `\` names, and `f.namedRow(t, n)` starts the labelled bystanders
  `$0` and `$1`, then the live row named `n`, returning the row and the
  bystanders' world. `f.baseIndexOneLostReply(t, state)` sets `base-index`
  and `pane-base-index` 1 on a new server and makes a row in `state` with a
  live session and no recorded pane. `f.assertAdopted(t, row, state)` checks
  that the row kept `state` and records the agent's pane id, pid and start
  time. `hupSurvivor()` is a pane command that ignores SIGHUP (a
  survivor); `endAtCleanup` ends such a process at cleanup; `assertProcs`,
  `rt.hasSession`, `rt.assertSession` and `rt.windowsOf` observe the
  result. `find-missing` (`find_missing_test.go`): `f.findMissing(t,
  recorded)` runs one sweep through `f.open` with the grace period and
  sweep budget at their defaults, returning an `fmSweep` (`Res`, and
  `Calls` when `recorded` runs tmux through the recording wrapper, so not
  for locale cases) with `assertListed(t, id, list)`;
  `f.assertStateNote(t, id, state, note)` checks the stored state and
  liveness note.
- Pane observation (`id_targets_test.go`): `rt.capture(t, paneID)` is the
  raw capture of one pane by id, without trailing empty rows.
  `idtWorld(t, rt)` reads every pane of the bound server, keyed by pane id
  (session id and name, pane pid, label and capture). `idtSame(t, what,
  before, after, paneIDs...)` fails unless each listed pane is unchanged.
  `idtSendAndSee(t, rt, cl, paneID)` types a fresh marker by pane id and
  waits until the pane shows it; it is both a check and a barrier.
  `idtIDShapedName(t, sigil)` gives the catalogue's `$N` or `%N` stored name.
- Pane verbs (`pane_verbs_names_test.go`, `pane_verbs_targets_test.go`):
  `paneVerbs` lists the three verbs as `apitest.PaneVerb`.
  `f.paneVerb(t, verb, id, text)` runs one call through `f.open` and returns
  a `paneCall` (`Pane`, `Err`). `send-keys` passes `allow_pending`. `pause`
  runs with a cancelled context, so its wait ends at once, and that
  `context.Canceled` (which means `/exit` and Enter went through) counts
  as success. `paneGoneName(verb)` is the verb's gone error
  (`ErrTmuxCaptureFailed` for `read-pane`, `ErrTmuxSendKeys` otherwise).
  `paneMarker(t)` is a fresh text to type. `waitPaneShows(t, rt, paneID,
  text)` polls the raw capture by pane id. `newIDShapedWorld(t, f)` builds
  an `idShapedWorld`: raw `$7` and `%9` sessions, a barrier session, the
  pane-not-found row and the gone row, with preconditions that neither id
  exists.
- Operator procedure (`unusable_name_procedure_test.go`): `n6Names(t)` and
  `dotX(t)` give the catalogue's E.10 N6 entries (`tmuxfix.StoredNames()`
  with source `E.10 N6`). `rt.procedureListing(t)` runs the README step 2
  listing (`list-sessions -F` with session id, creation time and name) and
  returns `listedSession` (`ID`, `Name`) in order.
  `f.procedureRemove(t, row, n)` runs the README steps 2 to 5 for a row
  recorded under `n`, targeting every session by id only: it reads each
  candidate's label (class by the production `ClassOf`) and environment
  line, ends only a candidate that is the row's own, checks it left the
  listing, then runs `adminapi.Delete` (agent-director-admin's `delete`,
  b.vqr) and checks the row is gone from the store. It returns each candidate as a `procCandidate` (`ID`, `Class`,
  `Env`, `Ended`).

**Must use:** later real-tmux tests (the lookup scenarios, then the verb
tests per SR-20.7) add their cases to `test/realtmux`. They reuse these helpers
and never shell out to tmux themselves, never pass a socket outside
`rt.Dir`, and never spell a reply wording: it comes from the catalogue.
Every real-tmux lookup test builds its agents, rows, lookups and
assertions with the lookup fixture and uses the production reader
`probe.NewProcChecker()` (never `procfix`). It never builds a
`tmux.Launch` by hand and never invents a server identity: every recorded
identity comes from the fixture (`agent.Server`, `f.serverOf`). A
`tmux.ProcIdentity{PID, Starttime}` view of such a recorded identity, for
`f.judge`, is allowed.
A real-tmux test of a verb that acts on a row (`kill`, `find-missing`,
`expire`, the pane verbs and `delete` in the operator procedure; `kill`'s
finished-row opt-in has no real-tmux case; its tests run on the tmux
doubles in `pkg/api` and `cmd/agent-director-admin`)
seeds its rows and runs the verb through the kill fixture (`liveRow`,
`seedRow`, `kill`, `findMissing`, `paneVerb` or a sibling built the same
way on `open`), never through its own store or client setup. A test of a
README tmux procedure runs its steps through `rt.run`/`rt.must` with
every `-t` target a session or pane id, as `procedureRemove` does. It checks refusals with `assertVerbError` and
adoption with `assertAdopted`. It observes panes by pane id through
`rt.capture`, `idtWorld` and `idtSame`, and uses `waitPaneShows` or
`idtSendAndSee` as a barrier, never a sleep.
New helpers go in a `harness_*_test.go` file other than `harness_test.go`,
or in a named fixture file such as `lookup_helpers_test.go` for a
feature's shared fixture.

### apitest `[tmux]` config writer (reusable test fixture)

`pkg/api/apitest.WriteTmuxConfig(t, path, settings...)` is the ONLY way
`pkg/api`, CLI and MCP tests write `[tmux]` settings, beside
`WriteRetentionConfig(t, path, days, settings...)`, which writes the same
and sets `[defaults] expire_retention_days` to `days` too (accepted and
refused values alike) and is the ONLY way those tests write that key.
Build each setting with `TmuxInt(k, v)`, or `TmuxFloat` / `TmuxString` /
`TmuxBool` for malformed-value cases, keyed by a `config.TmuxKey`; iterate
every key with `config.TmuxKeys()` (table order).

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
  byte for byte (for unparseable text). `WithNoEndedAt()` stores NULL in
  `ended_at`: a finished row whose end time is unknown (SR-4.2).
- Launch start:
  - `WithLaunchStartedAt(ms int64)`: epoch milliseconds.
  - `WithRawLaunchStartedAt(raw any)`: stored as bound. INTEGER affinity
    still turns integer-looking text into an integer.
  - `WithNoLaunchStartedAt()`: NULL.
- Life, version and pre-trust:
  - `WithLifeNumber(life int64)`.
  - `WithRowVersion(v int64)`: stores `v` as `row_version` after
    `SeedSpawn`'s own writes, in place of the version they left. It seeds
    rows no write sequence reaches (a row off `pending` at version 0, or a
    `pending` row at 0 that records a session id), so each guard of a
    conditional write can be tested alone (SR-5.3).
  - `WithNoPreTrust()`: stores 1.
  - `WithRawNoPreTrust(raw any)`: stored as bound.
- Launch identity:
  - `WithLaunchIdentity(id store.LaunchIdentity)`: sets all eight
    columns, replacing the live-row pane default. A zero field is stored as
    NULL, so `LaunchIdentity{}` gives no token, socket, pane or identity.
    The token is stored as given, so a malformed token is expressible.
  - `WithTmuxSocket(socket string)`: stores `socket` as the recorded tmux
    socket in place of `TestSocket` and keeps the other defaults (token,
    pane). Use it for a row a launch runs on (resume): the launch needs the
    socket's directory to exist, and a per-test socket keeps the fake-tmux
    tables apart.
  - `WithNoLaunchToken()`: seeds a row from before the release. The launch
    token, the socket and the whole launch identity, the pane default
    included, are NULL, the same as
    `WithLaunchIdentity(store.LaunchIdentity{})`. For a row with no token but
    a socket, use `WithLaunchIdentity(store.LaunchIdentity{Socket: TestSocket})`.
  - `WithNoPane()`: seeds a live row whose launch reply was lost (SR-11.3):
    `pane_id`, `pane_pid` and `pane_starttime` NULL, every other
    launch-identity column kept (the default token and `TestSocket`
    included), so its session can still read as Ours on the tmux path.
  - `WithNoPID()`: stores NULL in `pid` and `proc_starttime`, keeping the
    pane: a row whose latest launch never reported in (no SessionStart
    identity write, SR-6.7). It wins over an earlier `WithPID` or
    `WithProcStarttime`, so put it after them (a fixture spec's trailing
    `Opts`). **Must use** it when a row a fixture would seed with a pid
    must record none; never clear the columns with raw SQL.
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
  `WithLaunchIdentity`, `WithNoLaunchToken` and `WithNoPane` override it.
  Because the default pane's process is never found, `find-missing` judges
  a default-seeded live row gone and marks it `proc_absent`; a test that
  needs the row left live records a process the checker answers alive.
  Every live row
  gets the same pane, so two live rows on one socket that both need a
  Recorder session must set distinct panes with `WithLaunchIdentity`.

**Seeding through the hook gate (SR-22.9).** After `InsertPending`, the
session id and the state are written as the row's own agent would write
them, through the gated hook path: `storefix.WithSeedPane` records the
seed pane (`TestPanePID` with `storefix.SeedPaneStarttime`) when the row
records none, `storefix.SeedAgentWrites` makes the gated writes with the
matching gate (trigger `storefix.SeedTrigger`, `test_seed`): a gated soft
refresh that records `sessionID` (state unchanged, so a `pending` row
seeded with a session id stays `pending`), then, for a state other than
`pending`, the gated transition. The pane and process identity are then
written back exactly as they were, through a raw connection that advances
nothing. `pid` and `proc_starttime` default to NULL unless `WithPID` /
`WithProcStarttime` set them.

The options and defaults are applied by apitest-internal SQL in one
transaction, after the store's `InsertPending` and gated hook
sequence: one UPDATE writes the options and defaults;
for a `pending` row with no launch-start option a second UPDATE sets the
default launch start; then each `WithSessionHistory` entry is INSERTed into
`session_history` in seeding order, so with no stated `RecordedAt` a
later-seeded entry reads as newer. None of these statements advances
`row_version`.

**Seeding and `row_version`.** The store calls in that sequence are versioned
writes (see `internal/store`, "Versioned writes"): `InsertPending` starts at
0, and the gated soft refresh that records the session id (when given one)
and the gated transition (for a state other than `pending`) each add one.
The seed-pane writes, the option/default UPDATEs and the apitest and
storefix backdating fixtures add nothing. So a `pending` row seeded with no
session id is at 0, like a fresh insert, and every other seed starts above
0 (the totals are the same as before the gate: 0, 1 or 2). The other
seeders: `storefix.SeedSpawn` and its siblings leave 0 for `pending`, else
1, with no pane and NULL `pid`; `SeedCheckPermission` 1 (its gated request
INSERT adds nothing); `SeedResumable` 2; `SeedErrJsonlMissing` 3 (session
id, a rotating SessionStart that archives it, `ended`);
`SeedErrJsonlNeverWritten` 2; the permission-request seeders
(`SeedOpenPermissionRequests`, `SeedClosedPermissionRequests`,
`apitest.SeedPermissionRequest`, …) add nothing, their INSERT being gated
with a temporary seed pane that is then written back;
`apitest.SeedSessionID` adds 1. Tests assert version deltas
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
- A hook in a test comes through the gate like a real one, never from a
  hand-built `store.HookGate`. A hook that should apply is the row's own
  agent's: `apitest.ApplyAgentHook(t, dbPath, id, event, sessionID,
  opts...)` (or `storefix.ApplyAgentHook` on a `*store.Store`), whose
  parent is the row's recorded pane process. Another process's hook is
  `apitest.ApplyForeignHook` / `storefix.ApplyForeignHook` (a parent pid
  that is never the pane's), which reports `pid_mismatch`, or
  `no_pane_recorded` for a row with no pane. Both return the store's
  `store.HookApplied`, classify the event as the handler does
  (`storefix.FireHook`: SessionEnd ends the row; PreToolUse, PostToolUse
  and PermissionRequest are for the Bash tool), and take `HookTranscript`
  for a transcript path. A fixture row that needs a session id after it
  was seeded uses `apitest.SeedSessionID`. A row that must take the agent's
  hooks needs a pane: live `SeedSpawn` rows get the default one; for a
  recorded pane start time use `WithLaunchIdentity`.
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

### apitest description helper (reusable test fixture)

`pkg/api/apitest/descriptions.go` is the one shared description helper
(SR-20.2); `descriptions_resume.go`, `descriptions_lookup.go` and
`descriptions_kill.go` in the same package hold the cases `resume`, the
shared lookup and `kill` add; `descriptions_live_row.go` holds SR-18.6's
live-row sequence in the short form only `kill`'s manifest description
states, and the one-sentence pointer to it that ends the `find-missing` and
`spawn` descriptions; `descriptions_find_missing.go` holds `find-missing`'s
own description texts; `descriptions_expire.go` holds `expire`'s
Description and result-field texts and SR-18.7's cleanup guidance (full
form and pointer); `descriptions_held.go` holds the held-name cases
(an error after "duplicate session", or a holder found by resume's
pre-launch lookup) and spawn's held-name manifest texts;
`descriptions_starting.go` holds the starting-session refusal's cases;
`descriptions_resume_lookup.go` holds resume's pre-launch Leftover case and
the pre-launch holder overlay; `descriptions_reuse.go` holds the cases only
reuse returns and its launch kind; `descriptions_reuse_docs.go` holds the
cases for reuse's documentation (the reuse parameter text, the collision
text, the recovery recourse and the history-by-life forbidden forms) and
`AssertMustNot`;
`descriptions_older_serve.go` holds `DescOlderServeParam(verb, param)`,
the older-serve sentence ending each spawn and list param text that was
renamed or that MCP did not decode before 0.11.0;
`descriptions_pane.go` holds the pane verbs'
(`read-pane`, `send-keys`, `pause`) cases; `descriptions_kill_optin.go`
holds the cases of `kill`'s finished-row opt-in;
`descriptions_unusable.go` holds the cases of a row whose recorded tmux
session name cannot be used (every verb's refusal, the trigger, the
manifest pointer and the sweeps' result fields);
`descriptions_config.go` holds `ErrConfigMalformed`'s case for a config
file refused for its `[tmux]` values or its `[defaults]
expire_retention_days`, `DescConfigRefused(path, refusals...)` with one
`ConfigRefusal{Key, Value, Minimum, Derived, Create, Pipe, Retention}` per
refused value. It builds each `[tmux]` refused-value phrase from the key's
name, unit, default and safe minimum (a derived minimum from the given
create timeout and pipe-close wait), and, with `Retention` set, the
`expire_retention_days` phrase from `Value` and its range 1 to
`config.MaxExpireRetentionDays`, plus the closing sentence that a missing
key or 0 gives the default; with no refusals (a value of the wrong type)
only the path is required. **Must use:** every test outside
`internal/config`'s own tests (which pin the exact text) that checks a
config refusal's description asserts it with `AssertDescription` on
`DescConfigRefused`, never with its own phrases
(`cmd/agent-director/tmux_config_cli_test.go`,
`pkg/api/expire_retention_test.go`, and the serve test through its
`assertConfigRefused`).
It holds, as code, the required phrases of each SR-1.4 error
description case and the forms no agent-facing text may contain. The
package doc comment (`doc.go`, "# Description helper") says the same.

**API:**

- `AssertDescription(t, desc, c DescCase, forbid ...string)` checks an
  error description. It requires every phrase of case `c` and rejects `c`'s
  must-not phrases and forbidden values. It always rejects:
  - kill or pause named as a command to run;
  - the tmux commands `kill-session`, `kill-server` and `kill-pane`;
  - the tmux commands `attach-session` and `tmux attach` (whole words,
    so "tmux session" and "tmux attached" pass), so no agent-visible text
    tells an agent to run a tmux command;
  - every flag spelling of the operator-only opt-in (SR-6.8), and the
    operator tool's name `agent-director-admin` and its verb
    `kill-finished` in any spelling (b.vqr);
  - a label's raw value (`ad1 <16 hex> ...`);
  - every `forbid` value (empty values are ignored).

  The one exception is `ErrTmuxKillFailed`'s "retry kill later".
- `AssertAgentText(t, what, text)` checks a text agents see that may name
  kill as a documented procedure, such as a manifest description or help.
  It rejects only `kill-session`, `kill-server`, `kill-pane`,
  `attach-session`, `tmux attach`, the opt-in spellings and
  `agent-director-admin` / `kill-finished`;
  `what` names the text in failures.
- `AssertAgentTextCase(t, what, text, c DescCase)` is `AssertAgentText`
  plus case `c`'s required phrases, must-not phrases and forbid values:
  for a rule a manifest text must state. Kill named as a documented
  procedure stays allowed.
- `AssertMustNot(t, what, text, c DescCase)` (`descriptions_reuse_docs.go`)
  checks `text` for case `c`'s must-not phrases only: no required
  phrases, no forbid values and none of the agent-text forms. It reports
  each match with up to 80 bytes of context on each side. It is for texts
  the agent-text forms do not govern, such as a Go source file or a
  README, where `kill-session` and the like may appear legitimately.
- `DescCase{Name, Require, MustNot, Forbid}` is one case. Build it only
  with a `Desc*` constructor:
  - `DescInstanceIDControlChar(id)`
  - `DescPreCheckRead()`
  - `DescLaunchTimeout(LaunchTimeout{InstanceID, Timeout, Unrecognised, RowReset, ExplicitSpawnID})`:
    `RowReset` is reuse's timeout (`withRowReset` in
    `descriptions_reuse.go`): it also requires "the row was reset" and
    rejects any claim of the earlier life ("nothing was changed", "the row
    was restored", "earlier life", "stays ended" and the like).
    `ExplicitSpawnID` is a plain spawn of a caller-supplied id: it also
    requires the opted-in retry after the launch-timeout rule, the opt-in
    in its one spelling, `reuseOptIn` (b.1qq, b.c4u). A minted id's plain spawn, resume
    and reuse leave it unset, and the case then rejects "reuse opt-in"
    (a minted id's retry mints a new one; resume and reuse retry as they
    are).
  - `DescCallTimeout(call, timeout)`
  - `DescUnrecognisedReply(call, firstLine)`
  - `DescUnlabelledSession(UnlabelledSession{Name, SessionID, Ended, PlainSpawn, Restore})`:
    set `PlainSpawn` (the row stays pending) or `Restore` (a resume; applies
    `AfterResumeRestore`), not both.
  - `DescSessionCreateFailed(SessionCreateFailed{Name, Duplicate})`: the
    create failed without creating a session. With `Duplicate` it requires
    the quoted name and "session creation failed", and plain spawn's
    `AfterHeldName` overlay also requires its retry sentence and
    "instance <id>" (`HeldName.InstanceID`; the vanished holder, below).
    Without it, the case
    requires no phrase, because SR-1.4 has no row for the other create
    failures, and only the forbidden forms are checked.
  - `DescTmuxNotRun()`: `ErrTmuxNotAvailable` when the tmux binary could
    not be run. It requires no phrase (SR-1.4 has no row), so only the
    forbidden forms are checked.
  - `DescSocketPermission(socket)`: requires the socket and "not
    accessible to this user". It must not say "binary not available",
    because a permission failure must not read as a missing binary.
  - `DescSocketDir(socket, dir, reason)`
  - `DescIdentityWriteWarn(instanceID)`: the one client-log WARN line a
    failed identity write gives (SR-3.6). It is a log line, not an SR-1.4
    error description. It requires "WARN" and the instance id. Pass the
    launch token and the store id as `forbid`.
  - `DescScanLeftover(instanceID, []DescSession{{Name, ID}})`
  - `DescConflictingLabels(ConflictingLabels{InstanceID, Scope, Sessions})`
  - `DescSpawnLaunchTimeoutRule()` (the spawn manifest's launch-timeout
    rule, ending with "then retry an explicit id with the reuse opt-in";
    check with `AssertAgentTextCase`)
  - `DescSpawnScanRefusal()` (the spawn manifest's label-scan refusal;
    check with `AssertAgentTextCase`)
  - `DescLaunchStartedAtField(listRow bool)`: the `launch_started_at`
    result text of `status` and `get` (RFC3339 UTC with millisecond
    precision, shown only on a `pending` row, omitted otherwise). With
    `listRow` set, it checks `list`'s `spawns` text, which must also name
    `launch_started_at (timestamp?)`. Check with `AssertAgentTextCase`.
  - `DescTmuxSocketField()`: the `tmux_socket` result text of `get` (the
    tmux socket the row's launch uses, omitted for a row from before this
    release). Check with `AssertAgentTextCase`. **Must use** it for any Go
    check of that text; never spell its phrases in a test.
  - `DescKillSentinelText(name)`: the own text of a tmux sentinel that
    kill's descriptions wrap (`ErrTmuxNotAvailable`, `ErrTmuxKillFailed`,
    `ErrTmuxUnresponsive`, `ErrTmuxSessionConflict`); `name` is the
    sentinel's name. It requires no phrase. "dead", "gone" (SR-1.4) and the
    bare word "kill" (Epic 10's error-names rule) are must-nots, matched
    as whole words in any case: "retry kill later" belongs to
    `ErrTmuxKillFailed`'s description, never to a sentinel text.
  - Lookup (`descriptions_lookup.go`), the Can't tell cases every
    single-row verb and the plain-spawn label scan share:
    - `DescConflictingLabels(ConflictingLabels{InstanceID, Scope, Sessions, NothingWasDone})`:
      `Scope` for an `@ad_owner` scope value, else the quoted names and
      tmux ids of the sessions carrying the launch's label; set
      `NothingWasDone` for a single-row verb's refusal (the scan says
      nothing was written instead).
    - `DescDifferentServer(instanceID)`: `ErrTmuxNotAvailable` for a
      different tmux server, "nothing was done" and the same-environment
      sentence.
  - Kill (`descriptions_kill.go`):
    - `DescKillWaitExpired(KillWaitExpired{InstanceID, Name, Sent, ExitWait, AgentPID, SurvivorPIDs})`,
      `DescKillUncheckable(instanceID, name, sent)` and
      `DescKillNoPane(instanceID, name, agentPID)`: the three
      `ErrTmuxKillFailed` variants (the wait expired, naming every pid
      still running in listing order; the process cannot be checked and the
      labelled session is still there; no session or pane found while the
      process runs, no kill sent). `KillSent{Pane, Session}` says which
      kills the description states were sent. These are the only cases that
      allow "retry kill later".
    - `DescKillLeftover([]DescSession)`: `ErrTmuxSessionConflict` for a
      Leftover lookup, naming each leftover session and its tmux id.
    - `DescCase.AfterKillSent()`: turns a Can't tell case
      (`DescDifferentServer`, `DescConflictingLabels`,
      `DescSocketPermission`, `DescTmuxNotRun`) into the refusal after a
      sent kill ("the kill was sent and may or may not have taken effect",
      never "nothing was done"); `DescKillFollowUpUnresponsive(KillFollowUp{Name, Timeout, Unrecognised, FirstLine})`
      applies it to the unreadable follow-up lookup.
    - `DescSocketDirNothingDone(socket, dir, reason)`: the unusable socket
      directory of a single-row verb ("nothing was done", never "nothing
      was launched").
    - `DescKillInternalTrigger()`: `DescUnusableNameTrigger()` (below) as
      `kill`'s manifest description and `Client.Kill`'s Go doc prose state
      it, renamed for `kill`; saying that a tmux failure is swallowed or
      logged is also a must-not.
    - `DescKillRepeatedAfterLastSession(verb)`: the Q5 limitation
      (decision-0930b Q5): a repeated kill right after the last session on
      its tmux server ends can get `ErrTmuxUnresponsive` or
      `ErrTmuxNotAvailable` while the server exits, and the caller waits
      and checks again. `verb` is how the text names the verb: "kill" for
      `kill`'s manifest description, "Kill" for `Client.Kill`'s Go doc
      prose (checked with `AssertDescription`).
    - `DescKillManifest()`: `kill`'s manifest description (success only
      once the agent process is gone, `kill_sent`, a finished row's no-op
      is not verification, the row's state is unchanged, the per-call
      contract, each error's class with GONE as success, never delete, the
      same user and tmux environment). It includes
      `DescKillRepeatedAfterLastSession("kill")` and
      `DescKillInternalTrigger`. Check it with `AssertAgentTextCase`.

    Later single-row verbs reuse these cases (with `AfterKillSent` where
    they already acted) and add their own constructors in a file of their
    own, never phrases in a test.
  - Kill's finished-row opt-in (`descriptions_kill_optin.go`, SR-1.4,
    SR-6.5, SR-6.7). Its other refusals use `DescStillStopping`,
    `DescStillStarting`, `DescConflictingLabels`, `DescDifferentServer`
    and the kill cases unchanged.
    - `DescKillOptInLiveRow(instanceID, state)`: `ErrSpawnNotResumable` on
      a live row (`pending` included): "the row is live", the state, "no
      lookup was made and nothing was sent"; naming `find-missing` or the
      session-name param in either spelling (`tmux_session_name`,
      `tmux-session-name`) is a must-not.
    - `DescKillOptInNeverReportedIn(instanceID, name, bound)` and
      `DescKillOptInNeverReportedInLeftover(instanceID, []DescSession)`:
      the two "never reported in" `ErrTmuxSessionConflict` cases (an Ours
      session past the window and the bound, stating the bound in
      seconds; a Leftover, naming each session's quoted name and `$N`
      up to three, lowest `$N` first, then a count, and stating no
      bound). Both require "this row's own id", "no kill was sent", the
      human's-decision sentence, the list hint ("list tmux_session_name
      (--tmux-session-name on the CLI)") and the pointer; "dead", "gone", "a kill
      was sent", the still stopping or starting phrases and "not this
      launch's session" are must-nots. Pass any other row's id as
      `forbid`.
  - Unusable recorded name (`descriptions_unusable.go`, SR-1.4, SR-1.7,
    SR-3.2, SR-11.3, SR-12.2, SR-18.11). No case may say "delete" as an
    instruction (SR-18.8); only the README's "Operator actions" does.
    - `DescUnusableNameEmpty()`, `DescUnusableNameControlChar(name)` and
      `DescUnusableNameRewritten(name, RewrittenChars{Dot, Colon,
      InvalidUTF8})`: the three `ErrInternal` refusals every looking-up
      verb gives (the quoted name, the kind, "no tmux call was made",
      removing the row is a human's decision, the pointer). A rewritten
      case requires each set character and rejects each unset one.
    - `DescUnusableNameTrigger()`: the full verb-neutral trigger (the
      three kinds, `ErrInternal` with no tmux call, the human's decision,
      the pointer), for any looking-up verb's `Client` Go doc prose and
      `kill`'s manifest description.
    - `DescUnusableNamePointer()`: the short "(see kill)" pointer the
      `read-pane`, `send-keys`, `pause`, `resume` and `spawn` manifest
      descriptions carry; "unusable" is matched in any case. Check it with
      `AssertAgentTextCase`.
    - `DescUnusableNameFindMissingField()` and
      `DescUnusableNameExpireField(ExpireKept | ExpireKeptIDs)`:
      `find-missing`'s `unverified_ids` and `expire`'s `kept` / `kept_ids`
      on such a row, checked with `AssertAgentTextCase` beside
      `DescFindMissingField` and `DescExpireField`. They reject
      `ErrInternal` and delete instructions, not the plain word ("a failed
      delete" passes). The expire case panics on any other field.
  - Pane verbs (`descriptions_pane.go`, SR-1.4, SR-7.2, SR-7.3). `PaneVerb`
    (`PaneReadPane`, `PaneSendKeys`, `PanePause`) selects the verb's
    "nothing was read" / "nothing was sent" sentence. Every pane case
    forbids "no pane 0.0" and "base-index".
    - `DescPaneNotFound(PaneNotFound{Verb, InstanceID, Name, LostReply})`:
      "the agent's pane was not found" (with `LostReply`, also "the agent's
      pane was not adopted" and "no pane carries this launch's pane
      label"); also the lone leftover with no token pane, `Name` the
      leftover's.
    - `DescPaneLeftover(PaneLeftover{Verb, InstanceID, Sessions})`: a live
      row's Leftover (built on kill's leftover blocks); `read-pane` also
      requires "more than one leftover session exists" and needs two or
      more sessions.
    - `DescPaneGone(PaneGone{Verb, InstanceID, Name, FailedCall})`: the gone
      error ("the row's session is not there"), with "tmux <call> failed"
      after an action failure's follow-up.
    - `DescSendKeysPendingNoLaunch(instanceID)` and
      `DescSendKeysPendingLeftover(instanceID, sessions)`: `send-keys`' two
      `pending`-row `ErrSpawnNotInteractive` refusals.
    - `DescKeysTimeout(verb, call, timeout)` (text or Enter call, or for
      `PanePause` the key send, its `C-u`): a
      timed-out keys action, "the keys may have been delivered" (and, for
      Enter, "the text may be typed but not submitted"), then the verb's
      next step: for `PaneSendKeys` "read-pane; if the text is typed,
      send-keys with empty text, otherwise the same send-keys" (text) or
      "send-keys with empty text submits it" (Enter), never "retry later";
      for `PanePause` "retry later", never "send-keys with empty text".
      `DescCase.AfterEnterFailed(verb)` and `DescCase.AfterTextFailed()`
      turn a base case (`DescPaneGone`, `DescUnrecognisedReply`,
      `DescDifferentServer`, `DescTmuxNotRun`, `DescSocketPermission`)
      into the refusal after a non-timeout Enter or text failure
      (`AfterTextFailed` also after `pause`'s failed `C-u`, which typed
      nothing); `AfterEnterFailed` gives an `ErrTmuxUnresponsive` case the
      verb's next step the same way.
    - `DescPaneManifest(verb, errorNames)`: the tmux error classes a pane
      verb's manifest Description states, from one name-to-class map shared
      with `DescKillManifest`. `DescAllowPending(site)`: `send-keys`'
      SR-18.14 `allow_pending` text at a site (`AllowPendingFlag`,
      `AllowPendingRefusals`).

    The pane verbs' Can't tell, timeout and follow-up refusals use the
    lookup cases unchanged ("nothing was done"); only the pane refusals
    carry the verb's sentence.
  - Resume (`descriptions_resume.go`):
    - `DescResumeLaunchInProgress(LaunchInProgress{InstanceID, LaunchStart, NoSessionID})`:
      `ErrSpawnNotResumable` for a `pending` row. A zero `LaunchStart`
      requires "no launch start is recorded"; otherwise the start as RFC3339
      UTC, the form `get` shows. The other form, "dead", "gone" and any
      statement that the refusal ends are must-nots. `NoSessionID`
      (typically a spawn's or reuse's launch, b.uey) requires the step
      after find-missing (`noSessionStep`): "once find-missing marks it
      missing, if get still shows no session id, spawn the id again with"
      the reuse opt-in in its one spelling (`reuseOptIn`), before "nothing
      was written"; without it, "session id", the reuse opt-in and "spawn
      the id" are must-nots.
    - `DescResumeLostRace()`: `ErrSpawnNotResumable`, the row changed
      after resume examined it.
    - `DescResumeMoveStoreError()`: `ErrInternal`, the move to pending
      failed and nothing was launched.
    - `DescResumeInstanceIDControlChar(id)`: `ErrInternal` (SR-3.13). It
      includes the "Operator actions" pointer and forbids the id, raw and
      escaped.
    - `DescCase.AfterResumeRestore(ResumeRestore{Outcome, PriorState, Launch})`:
      adds the restore's row sentence (SR-8.5, SR-10.4) to the case of a
      launch error after a failed launch (`DescSocketPermission`,
      `DescTmuxNotRun`, `DescSessionCreateFailed`) and makes "the row stays
      pending" a must-not. `Outcome` is `RestoreApplied` (`PriorState`
      "ended" or "missing"), `RestoreRowChanged`, `RestoreRowRemoved` or
      `RestoreStoreError`; `RestoreNone` means no restore. `Launch` is the
      `LaunchKind` whose write began the launch: `LaunchResume` (the zero
      value, "resume moved it to pending") or `LaunchReuse` ("this spawn
      reset it"), which picks the changed and removed sentences and, for
      reuse, forbids resume's.

    Resume's launch timeout, socket-dir and pre-move socket-permission
    errors use `DescLaunchTimeout`, `DescSocketDir` and
    `DescSocketPermission` unchanged.
  - Reuse (`descriptions_reuse.go`, SR-1.4, SR-10.3):
    - `DescReuseLostRace(id)`: `ErrInstanceIdCollision`, "the row changed
      or was removed after this spawn examined it", "nothing was changed";
      the live-row "already live" text is a must-not.
    - `DescReuseArchiveFailure(id)` and `DescReuseChangeFailure(id)`:
      `ErrInternal`, "archiving the previous session failed" or "the reuse
      could not be applied", each "and nothing was changed", each
      rejecting the other's text.
    - None of the three may say "nothing was done" or "nothing was
      written". Reuse's timeout is `DescLaunchTimeout` with `RowReset`; its
      launch errors after a restore use `AfterResumeRestore` with `Launch:
      LaunchReuse`; its errors after "duplicate session" are the held-name
      cases with `HeldName{Name: <requested name>, Restore:
      ResumeRestore{..., Launch: LaunchReuse}}`; its pre-check holders are
      the held-name cases with `BeforeLaunch`, `Name` the requested name;
      its old-row refusals are `DescPreLaunchLeftover` and the
      starting-session cases, quoting the recorded name.
  - Reuse docs (`descriptions_reuse_docs.go`, Epic 17; SR-18.4, SR-18.8,
    SR-18.9, SR-18.10, SR-18.16). Check each with `AssertAgentTextCase`
    unless noted:
    - `DescReuseFinishedParam()`: spawn's `reuse_finished` parameter text,
      by key phrase: finished rows only, an explicit id, this call only, the
      default unchanged, a silent success, no memory of earlier lives, the
      failed-plain-spawn retry with the pending grace default (from
      `config.DefaultPendingGraceSeconds`), feature detection per surface,
      development builds and older binaries, the older-serve sentence (from
      `DescOlderServeParam`), and the same environment with SR-18.7's two
      consequences (it reuses `finishedRowNotVerification` and
      `wrongServerSecondAgent`). Its must-nots are the history-by-life
      claims and the other older-serve form's claim.
    - `DescReuseHistoryByLife()`: forbidden-only (SR-18.16): no claim that
      reuse reattaches an earlier conversation, gives a false lost-transcript
      report or `transcript_status` `rotated`, or that the history leak is
      an accepted risk.
    - `DescInstanceIDCollision(CollisionSite{Full, Code})`: SR-18.9's
      collision text: `ErrInstanceIdCollision` and, without the opt-in, any
      existing row "in any state". `Full` adds the opt-in's live row
      (pending included) and the lost race; `Code` is the quote a Markdown
      site puts around `pending` (a backtick), empty elsewhere. Claims that only a
      live row collides are must-nots.
    - `DescReuseRecourse(site)`, with `RecourseSite` values `RecourseGoDoc`
      and `RecourseTSREADME`: SR-18.4's recovery recourse in the site's
      wording (the Go doc's "spawn again with the same id, opting in to
      reuse" plus "(SpawnParams.ReuseFinished"; the TypeScript README's
      "spawn the same `claude_instance_id` again" with
      "`reuse_finished: true`"), both with "no memory of the". Every
      delete-then-spawn form is a must-not. It panics on any other site.
    - `DescReuseDocsForbidden()`: the delete-then-spawn, stale-collision
      and history-by-life forms together, forbidden-only. Check it with
      `AssertMustNot`, as `TestRecoveryWordingScan` does.
  - Live-row sequence (`descriptions_live_row.go`, SR-18.6,
    decision-0930b Q6):
    - `DescLiveRowSequence()`: the short form `kill`'s manifest
      description states, by key phrase: its opening "Live-row sequence (a
      pending row included):" and each numbered step. Its grace-period
      phrase is built from `config.DefaultPendingGraceSeconds`. The
      rationale the README and this doc keep, SR/OFR citations, the
      pointer and the pointer's opening are must-nots. Check it with
      `AssertAgentTextCase`.
    - `LiveRowPointer`: the pointer sentence, verbatim, that ends the
      `find-missing` and `spawn` descriptions.
    - `DescLiveRowPointer()`: the pointer case. It requires
      `LiveRowPointer`; the short form's opening and key phrases of every
      step are must-nots, so a text that restates any step fails. Check
      it with `AssertAgentTextCase`.
    - `LiveRowSequenceCount(text)` and `LiveRowPointerCount(text)`: how
      many times `text` states the sequence (short form, or the old long
      form) and how many times it carries the pointer. Neither counts the
      other. Use them to check that `kill` states the sequence once and no
      pointer, and every other verb states it zero times.
  - Find-missing (`descriptions_find_missing.go`, SR-18.9):
    - `DescFindMissingGrace()`: `find-missing`'s pending grace statement
      (a pending row is not judged inside the pending grace period,
      measured from its launch start; the default from
      `config.DefaultPendingGraceSeconds`; a resume's launch included).
      Check it with `AssertAgentTextCase` on `FindMissingOwnText`.
    - `FindMissingOwnText(text)`: the part of `find-missing`'s description
      before `LiveRowPointer`, so a check covers its own sentences only. It
      panics when `text` does not carry the pointer, so a description that
      lost the pointer fails the calling test.
    - `DescFindMissingManifest()`: `find-missing`'s liveness and
      same-environment rules (SR-18.9, SR-18.7, SR-3.8), by key phrase:
      liveness only from the agent process; tmux, on the row's recorded
      socket, only for a row whose process cannot be checked or was never
      recorded; the agents' user and tmux environment, and a run as another
      user, as root or against another tmux server can mark live rows
      `missing`; SR-18.7's two consequences. The environment probe-set scan
      and "on ambiguous evidence" are must-nots. Check it with
      `AssertAgentTextCase` on `FindMissingOwnText`.
    - `DescFindMissingField(f)`, with `FindMissingField` values
      `FindMissingIDs` and `FindMissingUnverifiedIDs`: the `ids` or
      `unverified_ids` result field text (SR-11.7, SR-18.3, SR-18.11). Both
      require that a row whose guarded write found it changed or gone, or
      failed, is in neither list, and "Never null"; "left untouched" is a
      must-not. It panics on any other field. Check it with
      `AssertAgentTextCase`.
    - `DescMissingNotProof()`: SR-18.2's statement for any text that
      describes `missing` (a manifest description or result field, a Go
      doc): the sweep's judgement on the evidence available to it, not proof
      that the agent has exited. Its must-nots are claims that `ended` or
      `missing` means dead or safe to delete; negations pass. It applies
      at the full sites only (decision-0930e): the `find-missing`
      description, the `state` / `spawns` / `ids` result fields and Go
      docs; such a text must not carry the short form as well.
    - `DescMissingNotProofShort()`: SR-18.2's short form for the four
      descriptions that carry the constant `missingNotProofShort`
      (`kill`, `resume`, `pause`, `expire`). It requires the
      short sentence verbatim ("`missing` is not proof the agent exited
      (see find-missing)."); its must-nots are the full statement's key
      phrases and the same dead-or-safe-to-delete claims. See
      "`missing` is a judgement, not proof" in
      [Degraded-mode reconciliation + cron user](#degraded-mode-reconciliation--cron-user).
    - SR-18.7's two consequences are shared constants in
      `descriptions_kill.go` (`notVerification`,
      `finishedRowNotVerification`, `wrongServerSecondAgent`), which both
      `DescKillManifest` and `DescFindMissingManifest` require; a new case
      stating them reuses the constants.
  - Expire (`descriptions_expire.go`, Epic 15; SR-18.9, SR-18.7,
    SR-12.4, SR-18.11):
    - `DescExpireManifest()`: `expire`'s Description, by key phrase: it
      reads tmux to decide, never kills a session, never touches
      transcripts, agents never run it, it keeps and reports rows whose
      session runs or cannot be checked, the same user and same tmux
      environment, and a run as another user, as root or against another
      tmux server can wrongly delete rows. The old "does not touch tmux" /
      "never touches tmux" claims and "harmless" are must-nots. It does not
      re-pin SR-18.2's short form; `DescMissingNotProofShort` covers it.
      Check it with `AssertAgentTextCase`.
    - `DescExpireField(f)`, with `ExpireField` values `ExpireCount`,
      `ExpireIDs`, `ExpireKept` and `ExpireKeptIDs`: one result field's
      text. `count` and `ids` require a row deleted after tmux showed no
      session of the agent (Gone) and its recorded process was not seen
      running, with the old retention-match meaning as must-nots; `ids`
      also requires "Sorted", "unchanged since expire examined it" and
      "Never null". `kept` and `kept_ids` require "kept rather than
      deleted"; `kept_ids` also names the trail event `ad.expire.kept`. It
      panics on any other field (the `DescFindMissingField` precedent).
      Check it with `AssertAgentTextCase`.
    - `DescCleanupGuidance()`: SR-18.7's cleanup guidance in full, the form
      only `expire`'s Description states (operator-scheduled, at the
      default retention, same user and tmux environment, keeps and reports
      rows it cannot prove gone, agents never run it, "zero window").
    - `DescCleanupPointer()`: the short pointer (`expireCleanupPointer`)
      the `kill` and `find-missing` Descriptions carry instead. It
      requires "Agents never run expire" and
      "operator-scheduled", and rejects the full sentence's phrases
      ("at the default retention", the keeps-and-reports phrase, "zero
      window"), so the full sentence cannot come back at those sites and
      push help stdout past the hard cap (`helpHardCap`). Check
      `find-missing`'s on `FindMissingOwnText`.
    - Both cleanup cases reject the schedule claims ("cron", "crontab",
      "daily", "hourly", "nightly", "weekly", "runs every"): no text says
      or implies a schedule is installed.
    - `pkg/api/manifest/manifest_expire_description_test.go` applies them
      to the manifest and `surface.json` alike (`verbDescriptionsBoth`
      fails when either source lacks the verb):
      `TestExpireDescription`, `TestExpireResultFieldTexts` (one subtest per
      field), `TestExpireParamAndResultTextsAgentText` (`AssertAgentText`
      over every `expire` parameter and result field) and
      `TestCleanupGuidanceInDescriptions` (full form at `expire`, pointer
      at `kill` and `find-missing`).
  - Held name (`descriptions_held.go`, SR-1.4, SR-9.4): `HeldName{Name,
    SessionID, Row, InstanceID}` gives the requested name, the holder's
    `$N` (empty when no single holder was identified) and the end write's
    result as a `HeldRow`: `HeldRowEnded`, `HeldRowLeftAsIs` or
    `HeldRowStoreError`, each mapped to exactly one of the three row
    sentences. `InstanceID` is plain spawn's new row's id, which only the
    vanished holder's case checks (below); the other cases ignore it.
    - `DescHeldLeftover(p)` ("left over from an earlier life", its label
      names this instance id, a human's decision, the pointer),
      `DescHeldNoValidID(p)` ("no valid instance id", the pointer),
      `DescHeldDifferentID(p)` ("a different instance id", must not be
      ended, no pointer), `DescHeldOtherStore(p, storeID)` ("another
      agent-director store", must not be ended, no pointer; forbids the
      store ids) and `DescHeldAmbiguous(p)` (`ErrTmuxUnresponsive`, more
      than one session's name matches, no `$N`, no label claim, the
      retry guidance by `HeldRow` as for an unanswered case below). The case
      words come from `tmux.LabelClass.CaseWords`.
    - `DescCase.AfterHeldName(p)`: overlays a Can't tell, unavailable or
      vanished case (`DescConflictingLabels`, `DescDifferentServer`,
      `DescCallTimeout`, `DescUnrecognisedReply`, `DescSocketPermission`,
      `DescTmuxNotRun`, `DescSessionCreateFailed`) for the held-name path:
      drops "nothing was done" and "retry later", requires the quoted
      name, the `$N` when set and the chosen row sentence, forbids the
      other two and "retry later", and makes no label claim. An
      unanswered case (`DescCallTimeout`, `DescUnrecognisedReply`,
      `DescHeldAmbiguous`) and the vanished holder
      (`DescSessionCreateFailed` with `Duplicate`) also require the retry
      guidance by `HeldRow`: for `HeldRowEnded`, the reuse opt-in in its
      one spelling (`reuseOptIn`) and "once the name is free" and not the
      launch-timeout rule; otherwise the launch-timeout rule ("do not
      retry until get shows the row ended or missing") followed by the
      same opted-in retry and "once the name is free" (b.1qq, b.c4u).
      Every other case must state neither.
      The vanished holder also requires "instance " followed by
      `HeldName.InstanceID`, the id its retry sentence's "this id" means
      (b.1qq); with `InstanceID` empty it requires the "instance " lead
      alone.
    - `DescSpawnHeldName()` and `DescSpawnSessionNameParam()`: spawn's
      manifest description and `tmux_session_name` parameter text on a
      held name. `DescSpawnHeldName()` requires "spawn the id again with "
      followed by the reuse opt-in's one spelling and forbids the three
      row sentences;
      `DescSpawnSessionNameParam()` forbids the old "live-collision" and
      "wrapped tmux new-session error" wording. Check them with
      `AssertAgentTextCase`.
    - `HeldName.BeforeLaunch` (resume's pre-launch lookup, `Name` the
      recorded name; or reuse's new-name pre-check, `Name` the requested
      name; `Row` zero): `DescHeldNoValidID`,
      `DescHeldDifferentID`, `DescHeldOtherStore`, `DescHeldAmbiguous` and
      `DescHeldLeftover` become the pre-launch holder refusals: "nothing
      was done" (and "retry later" for the ambiguous holder), with no
      "duplicate session", no row sentence and no label sentence that
      contradicts the class.
    - `HeldName.Restore` (a `ResumeRestore`; `Row` zero, excludes
      `BeforeLaunch`): resume's and reuse's errors after "duplicate
      session" (reuse's with `Launch: LaunchReuse` and `Name` the requested
      name), which
      require the restore's sentence and reject "nothing was done". The
      `DescHeld*` cases apply it themselves; `AfterHeldName` applies it to
      the Can't tell, vanished, own-session (`DescStillStopping`,
      `DescStillStarting`, `DescOwnOldSession`) and old-label
      (`DescPreLaunchLeftover`) cases.
  - Starting session (`descriptions_starting.go`, SR-1.4, SR-4.2):
    `StartingSession{InstanceID, Name, Window, Bound, NoSession,
    WindowChecked, SessionID}`.
    - `DescStillStopping(p)` and `DescStillStarting(p)`:
      `ErrTmuxUnresponsive` "appears to still be stopping" / "appears to
      still be starting", the window or bound in seconds, "retry later";
      `NoSession` (Gone while the agent process runs) is valid only for
      stopping.
    - `DescOwnOldSession(p)`: the own-id `ErrTmuxSessionConflict` ("this
      row's own id", may be hung, a human must look, the pointer; "the
      conversation stays resumable" only with `SessionID`; when the row
      ended only with `WindowChecked`).
    - The own old session is told apart from a Leftover by its sentinel
      and its own phrases, never by "this row's own id", which both say.
  - Resume pre-launch (`descriptions_resume_lookup.go`, SR-8.2):
    `DescPreLaunchLeftover(instanceID, []DescSession)`:
    `ErrTmuxSessionConflict` for a Leftover verdict, worded as the spawn
    scan's (`DescScanLeftover` shares its builder): "left over from an
    earlier life", each session's quoted name and `$N` up to three then
    a count, "nothing was written", a human's decision and the pointer.
    Pass the sessions lowest `$N` first.

  `call` is a `tmux.Call` (for example `tmux.CallCreate`).
  `DescCase.PointsToOperatorActions()` adds the pointer to the README's
  "Operator actions" section. The scan-leftover and conflicting-labels
  cases already include it. `apitest.OperatorActionsTitle` is that
  section's title. The pointer and the README section check
  (`pkg/api/readme_sections_test.go`) are both built from it.

**Rules (must use):**

- Every Go test that checks an error description or a manifest description
  written or changed from now on goes through this helper. Required phrases
  go through `AssertDescription` with a `Desc*` case. Forbidden forms go
  through `AssertDescription`, or `AssertAgentText` for manifest-like texts.
- No hard-coded phrase lists and no ad-hoc forbidden-word checks in
  individual Go tests. Do not copy the helper's lists into docs or tests;
  the helper is the single source.
- Pass the values a description must never carry as `forbid`: the launch
  token, the store id, label values, another row's id and
  session-environment values.
- A new SR-1.4 case is a new `Desc*` constructor in `descriptions.go`
  (resume's in `descriptions_resume.go`, reuse's in
  `descriptions_reuse.go`), not phrases spelled in a test. A launch onto a
  finished row selects its restore wording with `ResumeRestore.Launch`,
  never with its own sentence list.
- A Go check of a text that states the live-row sequence goes through
  `DescLiveRowSequence`, and one of a text that points to it through
  `DescLiveRowPointer` or `LiveRowPointer`; how often a text states either
  goes through `LiveRowSequenceCount` or `LiveRowPointerCount`. A check
  of `find-missing`'s own sentences cuts them with `FindMissingOwnText`. A
  check of a text that points to the README's "Operator actions" section
  goes through `PointsToOperatorActions` or `OperatorActionsTitle`. Never
  spell the sequence's phrases, the pointer or the section title as
  literals in a test. This applies to later changes to those texts too
  (Epics 12 and 17).
- A Go check that a whole agent-facing output (help, `tools/list`, the
  manifest, `surface.json`, generated agent docs) names no operator-only
  action matches `apitest.OperatorActionNames` (b.vqr): kill's former
  finished-row opt-in in any spelling, `agent-director-admin` and
  `kill-finished`. `AssertDescription` and `AssertAgentText` use the same
  pattern. Never a pattern of a test's own.
- A Go check of a held-name error description goes through the
  `DescHeld*` cases or `AfterHeldName`, with the end write's result as a
  `HeldRow`, or with `BeforeLaunch` or `Restore` for resume and reuse; never spell
  the row sentences, the restore sentences or the case words in a test.
- A Go check of a starting-session refusal goes through
  `DescStillStopping`, `DescStillStarting` or `DescOwnOldSession`, and one
  of resume's pre-launch Leftover through `DescPreLaunchLeftover`.
- **Must use** the `DescKillOptIn*` cases for any Go check of `kill`'s
  finished-row opt-in refusals (the live row, the two "never reported
  in" variants); never spell their phrases in a test.
- **Must use** the `descriptions_unusable.go` cases for any Go check of
  an unusable recorded name's text: a verb's refusal through the
  fixture's `desc` (`unusableNameFixtures()` in `pkg/api`, which picks
  `DescUnusableNameEmpty`, `DescUnusableNameControlChar` or
  `DescUnusableNameRewritten`), a `Client` Go doc or `kill`'s manifest
  description through `DescUnusableNameTrigger` (or
  `DescKillInternalTrigger` for `kill`), another verb's manifest pointer
  through `DescUnusableNamePointer`, and a sweep result field through
  `DescUnusableNameFindMissingField` or `DescUnusableNameExpireField`.
  Never spell their phrases in a test.
- **Must use** the reuse-docs cases for any Go check of the texts they
  cover: the reuse parameter through `DescReuseFinishedParam`, a collision text through
  `DescInstanceIDCollision`, a recovery recourse through
  `DescReuseRecourse`, and a history-by-life claim through
  `DescReuseHistoryByLife`. A new site of one of these texts picks its
  form with `CollisionSite` or a new `RecourseSite` value, never a
  sentence list of its own. A check for forbidden forms over a Go source
  or Markdown file goes through `AssertMustNot` with
  `DescReuseDocsForbidden` (or another `Desc*` case); never a hand-written
  `strings.Contains` loop.
- **Must use** `DescOlderServeParam(verb, param)` for any Go check of a
  param text's older-serve sentence (`TestManifestParamOlderServeSentences`,
  and `DescReuseFinishedParam` for `reuse_finished`); it panics on a param
  that has none.
- **Exception: literal-follow tests** (`TestAdviceFollow_*`, b.fji). A test
  that follows an error's or a manifest text's advice checks the advice it
  follows word for word, so any change to the advice turns it red and the
  follow is checked again: with `strings.Contains` on the exact phrase (the
  live-row sequence's steps included), or with `apitest.AssertDescription`
  where a `Desc*` case already pins it. That is the only case where a Go
  test spells these texts itself. In `pkg/api`, **must use** the shared
  helpers in `advice_follow_helpers_test.go` (`adviceAssertAdvice`,
  `adviceAssertPhrase`, `adviceAssertGoDoc`, `adviceAssertManifest`,
  `adviceAwaitFinished`, `adviceOnceAfter`, …) rather than new ones. See
  docs/test-writing-guide.md "Literal-follow tests for error advice".
- TypeScript tests cannot import the helper. They spell the phrases they
  check themselves.

### pkg/api resume fixture (package-internal test fixture)

`pkg/api/resume_fixture_test.go` (package `api_test`, no tests) is the
shared fixture for `resume` tests in `pkg/api`. Its doc comments carry the
detail.

- `newResumeEnv(t)` returns a `resumeEnv`: a real store; a
  `tmuxfix.Recorder` on a `tmuxfix.Clock` with no server started; a
  `procfix.Checker`; a captured logger; `config.Default()`; an `api.Client`
  on the same store file; the store id; and a per-test `TMUX_TMPDIR`
  (`TMUX` unset, `AGENT_DIRECTOR_INSTANCE_ID` empty) whose per-user
  socket directory exists. `e.resume(id)` calls `api.Resume` with all of
  them; `e.columns(t, id)` reads the row through `apitest.ReadSpawnColumns`.
- `e.seedResumable(t, state, opts...)` / `e.seedRow(t, resumableSpec)` seed
  a resumable row (default `ended`) through `apitest.SeedSpawn` options:
  a full launch identity on `e.socket`, a transcript, and one archived
  history entry. Seed after `newResumeEnv`. The returned `resumableRow`
  holds the seeded values and the raw columns (`Before`).
- `hookedResumeStore` is the `api.ResumeStore` `e.resume` passes. It
  delegates to the real store. `failMove` / `failRestore` inject a store
  error (default `errInjectedStore`) without writing. `afterGet` /
  `afterMove` run a function once after `GetSpawn` / `MoveToPending`
  returns, for race tests.
- `resumeLookupQ` is the virtual time the Recorder charges resume's one
  pre-launch lookup (the query timeout), and `e.moveStart()` the launch
  start the move records (the clock plus `resumeLookupQ`). A test that
  checks `launch_started_at` or the launch's timing uses them. With no
  server started, a normal resume's lookup reads Gone and proceeds.
- `vanishedUserSocket(t)` (`pkg/api/resume_pending_launch_test.go`) is a
  socket path under a per-user directory that does not exist, in a parent
  that does. `TestResumeLaunchSocket` and the unusable-name resume tests
  (`resume_unusable_name_test.go`) use it. **Must use:** a test that
  records a socket whose per-user directory vanished takes it from
  `vanishedUserSocket`, never its own path arithmetic.

**Lookup and held-name scenes on the kill fixture.** Tests of resume's
pre-launch lookup and of its path after "duplicate session" build on
[`killEnv`](#pkgapi-kill-fixture-and-shared-verb-tables-reusable-test-fixtures),
not `resumeEnv`:

- `pkg/api/resume_lookup_fixture_test.go`: `resumeRow`;
  `seedResumable` / `seedResumableRow` / `resumableSpec` (a finished row
  that ended a given time ago, its recorded server started, a transcript
  and a trust config directory), whose tail `seedOnServer` (cwd, trust
  directory, transcript, the default socket, the recorded server and a
  bystander) reuse shares; `seedHolder` (a session of a given
  `holderKind` holding the recorded name, or any name through
  `killRow.withName`; `holderOtherStoreOldToken` is another store's label
  with the row's id and another token); `storedFormOf`; the runners
  `resume`, `resumeWith` and `resumeClient` (through `api.Client`, with
  `[tmux]` settings); `ruleInstant` (the whole-second instant the
  starting-session rule reads, after the lookup's Q) and
  `createdBefore`; `verbDisagrees(t, verb, id)`, a call's
  `ad.provenance.disagree` records (`resumeDisagrees` and `killDisagrees`
  wrap it); and the one verb-neutral "a refusal wrote nothing" check:
  `e.snapshotWrites(t, verb, id, trust, sockets...)` takes a
  `writesSnapshot` just before the call, and
  `e.assertWroteNothing(t, before, except...)` fails if anything changed
  since: the row (every column, raw, through `apitest.ReadSpawnColumns`),
  its history over every life, its permission requests, its children's
  ids, the trust file (skipped with the option `exceptTrust`, for a
  refusal after pre-trust such as a reuse whose reset did not apply, or
  when the trust directory is empty, as for a live row), every trail
  record other than `ad.provenance.disagree`, any tmux call beyond one
  lookup, any name-based call and every bound socket's sessions.
  `snapshotResume` / `assertResumeWroteNothing` are resume's wrappers
  over it, and `snapshotReuse` reuse's.
- `pkg/api/resume_fixture_test.go` also holds `hookLock`, the lock and
  one-shot-hook helper shared by the hooked store wrappers
  (`hookedResumeStore`, `hookedReuseStore`).
- `pkg/api/resume_held_fixture_test.go`: `arrangeHeld(t, r, heldSpec)`
  returns a `*heldScene` in which the create answers "duplicate session"
  (the holder's kind and creation time, the row's own session under
  another name, a scope value, a failing re-lookup, a server rebound or
  restarted); `heldInstant`, `heldResumableSpec` / `seedHeldResumable`,
  `assertHeldRestored`, `assertHolderUntouched` and `removeHolders`.

New resume tests of the lookup or the held-name path use these rather
than their own seeding, runners or "wrote nothing" checks. **Must use:**
any test that must prove a refusal changed nothing, of any verb, uses
`snapshotWrites` / `assertWroteNothing` (or a verb's thin wrapper over
them), never a second "wrote nothing" check, and reads a call's
disagree records with `verbDisagrees`.

**Constraint:** a test that uses this fixture must not `t.Setenv("HOME",
...)`. The fixture keeps `TestMain`'s HOME so `readAPITrailLines`
(`find_missing_trail_test.go`) reads the trail resume writes.

**pkg/api `TestMain` and the trail path.** `TestMain`
(`pkg/api/example_main_test.go`) sets HOME to a temp dir (`apiTrailDir`)
and calls `trail.Default()` before `m.Run()`, so the trail singleton is
fixed at `apiTrailDir/.agent-director/ad-trail.jsonl` before any test runs.
A test that moves HOME and emits first no longer moves the trail. The one
exception is the `TestScanNameHeldFailOpen` child process
(`scanTrailChildEnv` set), which needs the singleton unfixed.

### pkg/api find-missing sweep helper (package-internal test fixture)

`pkg/api/find_missing_test.go` (package `api_test`) holds the shared
`find-missing` unit-test fakes and the one sweep helper. Its doc comments
carry the detail.

- `runFindMissing(s, pc, fmSweep{...})` runs one sweep: the one place the
  unit tests call `api.FindMissing`. By default it passes a fresh
  `tmuxfix.Recorder` as the `FindMissingTmux` (no server, so every lookup
  is Gone), the pending grace period and sweep budget from
  `internal/config`'s defaults (`fmGrace`, `fmBudget`), a
  `recordingLogger`, and one `tmuxfix.Clock` (`fmNow`) that is both the
  sweep's clock and the Recorder's virtual time. `fmSweep` overrides any
  of them (`grace`, `budget`, `now`, `clock`, `tmux`, `lg`).
  `fmBudgetSpent` is a spent budget (no tmux call; every row reaching the
  lookup gets the "not called" note). `mustSweep` adds the sweep-error
  check and, on the fake store, `assertGuards`; `mustFindMissing` is
  `mustSweep` with every default.
- `fakeFindMissingStore` is the `api.FindMissingStore` fake: a fixed
  live-row read, every write logged in order with the snapshot it carried,
  and each guarded write answered per row from `answers` (op `adopt`,
  `mark`, `note` or `clear`, then instance id; `fmAnswer` gives a
  `CondResult` or an error; unset is `CondApplied`). An applied adoption's
  returned snapshot becomes the row's guard, and `assertGuards` fails a
  verdict write that did not carry it. `StoreID()` is `tmuxfix.StoreID`.
- Rows and Recorders: `liveRow(id, opts...)` (a `working` row with a token
  on `apitest.TestSocket`, with `withSessionStart`, `withPane`,
  `withServer`, `withLaunch`, `withNote`); `fmOurs(rows...)` (a Recorder
  whose server `fmServer` holds each row's own labelled session, and one
  token-carrying pane for a row that records none); `fmCantTell()` (every
  lookup and pane listing unreadable). Assertions: `assertLists`,
  `assertMarkReason`, `assertLookups`. Process answers come from
  `procfix.Checker`.

**Must use:** every `find-missing` sweep test in `pkg/api` calls the sweep
through `runFindMissing` (or `mustSweep` / `mustFindMissing`) with a
`tmuxfix.Recorder` and `procfix.Checker`; it never adds a second store,
tmux or process-checker fake and never waits in real time. A budget or
timeout is reached through the Recorder's virtual time on the shared
`tmuxfix.Clock` (or `fmBudgetSpent`), never by sleeping. Tests through
`Client.FindMissing` use a Recorder bound to a `tmuxfix.Clock` the same
way.

**Store tests of the guarded writes.** Tests of `MarkMissingIfSameLife`,
`SetLivenessNoteIfSameLife`, `ClearLivenessIfSameLife`,
`AdoptIdentityIfSameLife` and the live-row read live in the external
`store_test` package (`internal/store/find_missing_writes_test.go`,
`live_identity_read_test.go`) and seed rows only through `apitest`
(`SeedSpawn` and its options). Their version deltas are cases of the SR-5.2
versioning test: `row_version_find_missing_test.go` appends them to
`internal/store/row_version_test.go`'s applied and no-op tables. A new
guarded write adds its cases the same way. The note write's and clear's
store errors are covered through `pkg/api`'s fake store, not injected in
the store.

### pkg/api pre-trust fixture (package-internal test fixture)

`pkg/api/pretrust_fixture_test.go` (package `api_test`, no tests) is the
shared `.claude.json` fixture for the pre-trust tests in `pkg/api`
(`spawn_pretrust_test.go`, `resume_pretrust_test.go`). Its doc comments
carry the detail.

- `seedTrustConfig(t, dir, file)` puts a `.claude.json` into `dir` in one
  of three `trustFile` states and returns a `trustConfig`:
  `trustLacksEntry` (present, trusting another folder only),
  `trustMissing` (absent) or `trustUnwritable` (present, in a `0500`
  directory; skipped as root).
- `seedRelativeTrustConfig(t, file)` is `seedTrustConfig` for the
  relative `CLAUDE_CONFIG_DIR` `"rel"` (b.nje): it moves the process
  into a fresh temp dir until the test ends (`cwdfix.Temp`, see
  [cwdfix](#cwdfix-the-working-directory-fixture-reusable-test-fixture))
  and seeds `file` in its `rel`, where `"rel"` would resolve from there,
  so a test can check that pre-trust touches nothing there. A test using
  it must not call `t.Parallel`; `cwdfix.Temp` makes that call panic.
- `c.env()` is the `apitest.SpawnOption` that points a seeded row's
  `CLAUDE_CONFIG_DIR` at `dir`; `c.extraEnv()` is the same as an extra-env
  map for a launch. Either gives each test its own config directory.
- `c.reset(t)` writes the seeded bytes back, removing an entry a resume
  wrote.
- `c.check(t, cwd, trusted, when)` asserts that the file trusts `cwd`
  (`trusted`) or is exactly as seeded (still absent when it was); `when`
  labels the failure.
- `checkPreTrustJSON(t, result, want)` asserts that `result`'s JSON
  encoding carries `pre_trust` equal to `want`, pinning the field's tag.

**Must use:** a pre-trust test in `pkg/api` uses this fixture to seed and
check `.claude.json`. Do not write a new `.claude.json` reader or seeder.

### pkg/api kill fixture and shared verb tables (reusable test fixtures)

`kill` brought the first live-row verb tests on the shared lookup. Their
fixture and the per-verb tables are package `api_test` files in `pkg/api`;
each file's doc comments carry the detail.

- **Kill fixture** (`kill_fixture_test.go`, with its row factory in
  `kill_fixture_rows_test.go`; no tests). `newKillEnv(t)`
  returns a `*killEnv`: a real store (`e.st`, registered with `storefix`)
  behind the wrapper `e.store`, which is also the pane verbs'
  `api.SendKeysStore` and `api.PauseStore` (`failAdopt(err)` /
  `refuseAdopt(result)` inject an adoption-write failure without writing;
  `adoptTries` counts adoption attempts; `failPermissionRequests` and
  `failStateReads` fail the relay guard's read and pause's wait polls), a
  `tmuxfix.Recorder` on virtual
  time (`e.rec`, `e.clock`), a `procfix.Checker` (`e.pc`), the `[tmux]`
  defaults (`e.cfg`), `kill`'s sleep (`e.sleep`, the clock's `Advance`) and a
  per-test `TMUX_TMPDIR` (so no `t.Parallel`). `e.kill(id)` calls the
  exported `api.Kill`; `e.client(t, settings...)` gives an `api.Client` on
  the same store with a config written by `apitest.WriteTmuxConfig`, and
  `e.clientFor(t, cfgPath)` the same on a config file already written,
  returning `api.New`'s error (a refused config) instead of failing the
  test.
  `e.seedRow(t, killRowSpec{...})` seeds a live row with a full launch
  identity, its own labelled session and its agent process (options for a
  lost reply, no session, teammate split panes, the agent's process state,
  `RelayOn`, `SessionID`); `e.seedRawRow(t, spec, ident)` seeds a row
  through `apitest.SeedSpawn` without reading it back through `GetSpawn`
  (which refuses malformed labels, `claude_args`, `extra_env` and
  timestamps), so a malformed or hand-edited row can be seeded; `expire`'s
  and reuse's raw rows go through it;
  `seedSession`, `seedTeamSession`, `seedViewer` (a session sharing the
  agent's pane, `SeedPane.Shared`) and `seedBystander` (an unrelated session,
  so the server outlives the row's session; or `serverExitsWhenEmpty`) add
  the rest of the world, and `syncServers` keeps the checker's server pids
  in step. `setAfterCall` / `setAfterWaiting` change processes after a call
  kind or after some waiting; `killCalled` / `killDisagrees` (over
  `verbDisagrees`) read the call's trail records; `assertKillCalls` / `assertKillsByID` check the recorded
  tmux calls. `export_test.go` gives `api.SetSleepForTest` and
  `api.KillPollInterval`.
- **Starting-row fixture** (`starting_row_fixture_test.go`, no tests;
  SR-4.2, SR-6.7), a finished row as the starting-session rule and kill's
  reported-in rule see it, on the kill fixture:
  - `defBound` / `defWindow`: the configured defaults, read from
    `config.DefaultStartingSessionSeconds` and
    `config.DefaultStoppingWindowSeconds`.
  - `startingRow{state, endedAgo, noEndedAt, agent, noPID, noSessionID,
    noSession, age}`: `endedAgo` and `age` count back from `ruleInstant`,
    the instant both rules read. A session counts as reported in when the
    row records a pid and `age > endedAgo` (an earlier whole second than
    `ended_at`); `age == endedAgo` is the same second.
  - `e.seedStarting(t, s)` seeds it on its server (`seedOnServer`), with a
    session id and transcript unless `noSessionID`, `apitest.WithNoPID`
    for `noPID`, and its own labelled session `age` old unless
    `noSession`. `s.recordsPID()` says whether the row records a pid.
  - `s.startingCase(r, bound, window)` gives the `apitest.StartingSession`
    for `DescStillStopping`, `DescStillStarting` and `DescOwnOldSession`.
  - Used by `resume`'s starting-session tests and `kill`'s finished-row
    opt-in tests.
- **Finished-row opt-in runners** (`kill_optin_fixture_test.go`, no
  tests), the kill runners with the opt-in set, which only
  `agent-director-admin kill-finished` sets (b.vqr): `e.killOptIn(id)`
  (the unexported `killFinished`, through `export_test.go`, at `e.cfg`'s
  durations), `e.killOptInWith(id, bound, window)` (the bound and window
  passed straight to it, for a case at their safe minimums) and
  `e.killOptInClient(t, id, settings...)` (through `adminapi.KillFinished`
  on `e.client`, returning the captured log). The seeded agent stays alive
  in the fake, so a case that expects the kill to succeed makes it exit
  (`e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)`);
  otherwise the call ends in `ErrTmuxKillFailed`.
- **Pane-verb fixture** (`pane_verb_fixture_test.go`, no tests; SR-20.2),
  the kill fixture's extension for `read-pane` and `send-keys`:
  - invocations: `e.readPane` / `e.readPaneClient` / `e.readPaneRun`,
    `e.sendKeys` / `e.sendKeysAt(window, now, p)` / `e.sendKeysClient` /
    `e.sendKeysRun`; the generic `verbRun[R]`, `runVerb`, `assertSameRun`
    (a repeat makes the same calls and error) and `errText`;
  - seeds: `e.seedOurs` (hand-laid panes), `e.seedLeftover(t, r, token)`,
    `r.pane()`, `newToken()`, `labelledPane` (the one pane carrying a
    token), and per-pane capture text (`e.setPaneTexts(socket)`,
    `paneText(socket, paneID)`) to tell which pane was read;
  - launch-kind seeds: `pendingKind` (`pendingFresh`, `pendingResumed`;
    `pendingKinds()`) × `pendingShape` (`pendingOurs`, `pendingLostReply`,
    `pendingLeftover`) through `e.pendingSpec` / `e.seedPending`;
  - between the row read and the send: `e.sessionStartAfter(t, call, r,
    sessionID)` applies the agent's SessionStart through
    `apitest.ApplyAgentHook` once, after the first call of a kind;
    `e.rowWriteAfter(t, call, r)` makes a non-hook versioned write (the
    lost-reply variant); both fail the test if they did not apply or never
    ran;
  - call lists and assertions: `paneReadCalls`, `paneSendCalls`,
    `paneTextCalls`, `withFollowUp(calls)`, `paneWriteCalls`;
    `recordedCall(c)`, the kind the lists compare, which gives a key send
    of `paneClearKey` (`C-u`) as `callClearLine`;
    `e.assertPaneCalls` (kinds in order, every action by pane id, no
    name-based call), `e.assertCaptured`, `e.assertDelivered` (text after
    CR stripping, then Enter), `e.assertEnterOnly` (`send-keys` with empty
    text: the lookup, the listing and one Enter, an empty text call
    aside), `e.assertTextSent`, `e.assertNothingSent` (no text, Enter or
    line-clear call), `e.assertNoTmuxCall`, `e.assertNoCalls(kinds...)`;
  - "changes nothing" readers: `e.assertRowUnchanged(t, id, e.columns(t,
    id))` (every column, `row_version` and the snapshot included),
    `trailMark` / `assertNoTrailSince` (no trail record for the id), and
    `e.adoption(t, id)` (`adoptionColumns`: unchanged, written once, or
    not applied).
- **Pause fixture** (`pause_fixture_test.go`, no tests): `e.pause` /
  `e.pauseWithin(ctx, timeoutSeconds, p)` / `e.pauseRun` / `e.pauseClient`,
  `pauseParams`, `exitText`; pause's call lists with the line clear before
  `/exit`, `pauseSendCalls`, `pauseTextCalls` and `pauseClearCalls`, and
  `pauseActions` (the line clear, `/exit` and Enter calls a failure can
  hit, with the calls made up to it); `e.endAfterEnter(t, r)` (a row-ending
  after-call hook: the agent's SessionEnd through `apitest.ApplyAgentHook`
  when the first Enter returns); `e.assertExitDelivered` /
  `e.assertExitTyped` (both check `e.assertLineCleared`: exactly one
  `C-u` to the agent's pane, before every text call);
  `pauseDisagrees(t, id)`. The wait is driven through
  the test knob seam: `fastPausePolls(t)` (`api.SetPauseTestKnobs` with a
  1 ms poll, restored at cleanup through `api.PauseTestKnobs`) and
  `pauseTimeoutSeconds` (1 s): a row nothing ends times out after about
  1 s. `e.store` is the failing `PauseStore` wrapper (`failStateReads`,
  `failAdopt`, `refuseAdopt`).
- **Pane-input model** (`pane_input_fixture_test.go`, no tests; b.fji,
  b.9o4): `watchPaneInput(e, pane, timeoutsReach)` models the agent's
  input box from every keys call to `pane`, in call order, through one
  `AfterCall(tmuxfix.AnyCall, …)` hook, as Claude Code's prompt input
  behaves: a text call types its text, `C-u` deletes back to the start of
  the current line, Enter submits the box and on an empty box submits
  nothing. The box is the pane's capture, so `read-pane` shows what is
  typed. With `timeoutsReach` a timed-out call still reaches the pane (the
  descriptions' "may have been delivered"). `paneAgentExiting(t, e, r,
  timeoutsReach, exitOn)` also ends the row (SessionEnd) on the
  `exitOn`-th `/exit` submitted; `in.assertSubmittedOnce(t, text)` checks
  the agent got `text` exactly once with nothing left typed. **Must use**
  it for any `pkg/api` test of what an agent's input received from
  `send-keys` or `pause`; never a second input-box model.
- **Disagree records** (`disagree_record_test.go`, no tests):
  `assertDisagreeRecord(t, rec, r, verb, source, disagreeWant)` (the one
  check of an `ad.provenance.disagree` record, used by `kill`, `send-keys`
  and `pause`), `adoptedRecords(t, verb, id)`, and the 14-row disagree
  table the keys verbs share (`keysDisagreeCases()`,
  `e.seedKeysDisagreeCase`, `assertKeysDisagrees`).
- **One name per error** (`one_name_per_error_test.go`): `assertOneName(t,
  err, want)` checks SR-1.5 on a returned error (exactly one catalogued
  sentinel, or none for `ErrInternal`), and `oneNameRows()` is the table of
  every tmux-caused error (and reachable `ErrInternal` case) the verbs
  return, one `oneName<Verb>Rows()` list per verb (`oneNameKillRows()` for
  `kill`; the pane verbs' in `one_name_pane_verbs_test.go`; `kill`'s
  finished-row opt-in's `oneNameKillOptInRows()` in
  `one_name_kill_optin_test.go`, run by its own test). A row's
  "unusable recorded name" case seeds `unusableNameSpec()`
  (`one_name_pane_verbs_test.go`: a waiting row with no session,
  recording the pre-b.gqe default name).
- **Call-site table** (`lookup_calltable_test.go`, SR-20.5): every lookup
  outcome (the verdicts and their variants, the server cases, and the
  action-failure column) against every single-row verb, built on the kill
  fixture. A verb is a `callTableVerb` adapter with one cell per column
  (`callTableCell`, with `na` for a column that does not apply, `held` and
  `fm` for the spawn and find-missing variants; `run` replaces the
  seeded-row run for a verb that builds its own world), listed in
  `callTableVerbs()`. Each verb's adapter is in a sibling file
  (`lookup_calltable_<verb>_test.go`; `send-keys` and `pause` add
  `pending`-row and finished-row adapters, and `kill`'s finished-row
  opt-in has its own, `lookup_calltable_kill_optin_test.go`).
  `callTableColumns()` is the lookup columns (`callTableLookupColumns()`)
  followed by the unusable-name column family
  (`lookup_calltable_unusable_test.go`, SR-3.2): `callTableUnusables()`
  gives one column per `unusableNameFixtures()` entry ("Unusable name,
  <label>", a row with no session recording that raw name), plus one whose
  row records no socket while the socket directory is unusable, so a
  guard placed after the socket resolution fails the table. A verb merges
  its cells with `maps.Copy`: `callTableUnusableRefused()` (`ErrInternal`
  with the fixture's `desc`, no tmux call) or `callTableUnusableNA(reason)`;
  `find-missing` and `expire` build theirs from the fixture's `note` and
  `kept`. `callTableCell.keptLater` is `expire`'s expected kept reason for
  a later row on the same socket (`""` deleted). `-run
  'CallTable/.*/Unusable'` selects the family.
- **Unusable recorded names** (`unusable_name_fixture_test.go`, no tests;
  SR-3.2, SR-11.3, SR-12.2). `unusableNameFixtures()` is the one table of
  unusable recorded names: empty, a newline, DEL, ESC, the pre-b.gqe
  default name (`preGqeDefaultName`, holding `.`), `:`, invalid UTF-8,
  and `.` with a newline (the control character decides). Each
  `unusableNameFixture` carries `label`, `raw`, `kind` (`tmux.Unusable`'s
  verdict), `desc` (its `apitest.DescUnusableName*` case), `note`
  (`find-missing`'s liveness note) and `kept` (`expire`'s kept reason).
  `usableNameFixture()` is the usable control (`usableDefaultName`, kind
  `UnusableNone`, no note or reason). `unusableNameTokens()` gives the
  three kinds in SR-3.2's order with their note and kept reason, the one
  place those six tokens are spelled in tests; `unusableNameTokenOf(kind)`
  is one kind's entry (zero for `UnusableNone`); `newNameFixture` builds
  a fixture from them. On the kill fixture,
  `unusable_name_row_fixture_test.go` adds `unusableFixture(t, label)`
  (one entry by label) and `e.seedUnusableRow(t, spec, f)` (a row
  recording `f`'s raw name with its own current-labelled session up under
  a usable name, so only the name guard stops the verb).
- **Security** (`security_test.go`, SR-15): beside the row a verb acts on,
  a session with no id and another row's session carry a planted secret; the
  secret, both rows' launch tokens and the other row's id must appear in
  none of the verb's result, error description, client log or trail. A
  per-verb table (`securityVerb`, its cases `securityCase` with `server`,
  `event`, `fields` and `others`; `record` takes the call's `texts`), one
  file per later verb (`security_<verb>_test.go`; `kill`'s finished-row
  opt-in in `security_kill_optin_test.go`). A verb with no call event
  (`read-pane`, `pause`) sets `event` `""`: the harness then requires no
  record for the subject but the `ad.provenance.disagree` records a case
  expects (`disagree`) and the events listed in `others` (the agent's own
  hooks, such as `pause`'s SessionEnd).
- **Go doc "Errors:" lists** (`pkg/api/manifest/godoc_errors_test.go`,
  package `manifest_test`): `assertGoDocErrorsMatchManifest(t, method,
  verb)` checks that a `Client` method's "Errors:" list names exactly its
  verb's manifest `ErrorNames`, with `clientMethodDoc`, `splitGoDocErrors`
  (the list apart from the prose) and `goDocErrorNames`.
  `clientGoDocProse(t, method)` returns a `Client` method's Go doc prose
  outside "Errors:" and its `CLI:` line, with line wrapping rejoined; a
  check of what a method's Go doc says (such as
  `DescUnusableNameTrigger` in `manifest_unusable_name_test.go`, or
  `kill`'s cases) reads the prose through it, never its own doc reader.
- **README section checks** (`readme_sections_test.go`, SR-1.4, SR-18.1,
  SR-18.17). The file holds the one Markdown heading parser in `pkg/api`
  tests:
  - `readMD(t, path)` returns an `mdDoc`: the file's lines and its ATX
    headings (`mdHeading{level, title, line}`), skipping fenced code
    blocks.
  - `d.titled(title)` and `d.anchored(anchor)` find headings by exact
    title or by GitHub anchor. `d.body(h)` is a heading's section text, up
    to the next heading of its level or higher. `d.nearMiss(title)` names
    a heading that differs from `title` only in case or spacing, for
    failure messages.
  - `mdAnchor(title)` is GitHub's anchor for a title.
  - The source walkers: `walkGoSources(t, fn)` calls `fn(rel, text)` with
    each non-test Go source under `pkg/`, `internal/` and `cmd/` (skipping
    `node_modules` and `testdata`), by repo-relative path, with wrapped
    comment lines joined and `\"` unescaped. `eachManifestText(fn)` calls
    `fn(source, text)` with every manifest text: each verb's
    `Description`, parameter texts and result-field texts, named
    "manifest <verb> Description", "manifest <verb> param <name>" or
    "manifest <verb> result field <name>".
  - The pointer collector: `collectREADMEPointers(t)` gathers every
    pointer to a README section by title (`readmePointer`, found by
    `pointersIn`). It reads non-test Go sources (`walkGoSources`), every
    manifest text (`eachManifestText`), and `apitest.OperatorActionsTitle`.
    `TestREADMEPointersNameExistingSections` checks that each pointer names
    a heading that exists exactly once, and that a list of required sites
    all carry a pointer.
  - The anchor check: `TestREADMEAnchorLinksResolve` checks that every
    anchored `.md` link in `README.md` and `pkg/api/README.md` resolves to
    exactly one heading. `TestREADMESectionHeadings` checks that each
    pointed-to heading exists exactly once, the "Caller contract: tmux
    refusal classes" heading of this document included.

  Each of its test names contains `README`, so `-run README` runs them.
  Five sibling files check sections on the same parser:
  - `readme_optin_test.go` (SR-6.8, SR-18.15, SR-18.17, b.vqr): `kill`'s
    finished-row opt-in, and the operator tool's `kill-finished`, are
    spelled in the README only inside "Operator actions"; the opt-in is
    spelled in no other doc under `docs/` or either package README, except
    the generated `docs/admin-reference.md` and the opt-in's trail field in
    this document's `ad.kill.called` row. It holds the
    section helpers `operatorActions(t, d)` (the "Operator actions"
    heading, by `apitest.OperatorActionsTitle`, failing when missing) and
    `sectionLines(d, h)` (a heading's 0-based body line range).
  - `readme_operator_actions_test.go` (SR-18.17, SR-20.6; AC-DOC-18):
    `TestReadmeOperatorActionsUnusableNameSteps` checks that the item "A
    row whose recorded name cannot be used" gives SR-18.17's five steps in
    order (`unusableNameSteps()`, each step's required content and tmux
    command shapes, the notes and kept reasons from
    `unusableNameTokens()`); `TestReadmeOperatorActionsTargetsByID`
    checks that every tmux command in "Operator actions" targets a
    session or pane by id (`byIDTargets`). Its extractors:
    `tmuxCommands(lines, first)` collects the tmux commands of fenced-block
    lines and inline code spans as `tmuxCmd` (subcommand, words,
    `targets()`, `hasFlag`), and `numberedSteps(lines, first)` splits a
    top-level numbered list into `procStep`s. Two shared helpers live
    here too: `operatorActionsItem(t, d, title)` returns the one item
    heading of that title inside "Operator actions" (failing unless there
    is exactly one), and `checkSteps(t, d, item, steps, want)` checks a
    numbered list against `stepSpec`s (count, numbering, each step's prose
    and its tmux command shapes in order, one subtest per step).
    `-run ReadmeOperatorActions` runs them.
  - `readme_operator_actions_more_test.go` (SR-18.17, SR-5.4; AC-DOC-19):
    `TestReadmeOperatorActionsACDOC19Items` checks the AC-DOC-19 items
    (their titles are constants here, `stoppingItemTitle` and
    `switchOverItemTitle`) come once each after the pending-row item, with
    their prose, the items they name and their numbered procedures
    (`acDoc19Items()`, `itemProcedure`, `stepSpec`s through `checkSteps`).
    A step that may name a set in more than one way is checked by
    `stepForms`; the live-state step takes its forms from
    `liveStateForms()`, which splits the store's state constants with
    `store.IsLiveState`, so it holds no state literal.
    `TestReadmeOperatorActionsACDOC19TitlesNotAgentVisible` checks no
    manifest text, Go source or package README names those items.
    `TestReadmeOperatorActionsStoreIDCommand` and
    `TestReadmeOperatorActionsStoreIDPointers` pin "This store's id" (title
    `storeIDItemTitle`, command `storeIDCommand`) and the items that link
    to it beside the trail's `store_id`. Its shared helper
    `storeIDItemCommands(t, d)` returns the trimmed `sqlite3` lines in that
    item's fenced blocks.
  - `readme_store_id_test.go` (SR-5.4): `TestReadmeStoreIDCommandPrintsStoreID`
    runs the README's store-id line (`runnableStoreIDLine`, through
    `storeIDItemCommands`, refusing a line without `-readonly`) with HOME at
    a temp dir holding a fresh store, closed and held open, and checks it
    prints `Store.StoreID`. A missing `sqlite3` fails inside the sandbox
    and skips only outside it.
  - `readme_timing_values_test.go` (Epic 21): pins documented `[tmux]`
    timing values to the `internal/config` constants, recomputing each
    from them, so a changed constant or a changed pinned value fails.
    `TestReadmeTimingValuesStatements` checks the listed prose statements
    in `docTimingStatements()` (a `docStatement` names the doc, the
    section, its `anchors` and the values the constants give; value i is
    the first whole number after anchors[i], searched from the end of
    value i-1, and an empty anchor takes the next number): the defaults,
    safe minimums and the `kill` and `pause` worst cases in the README's
    [Timing settings](../README.md#timing-settings-tmux) and caller
    contract and in this document's [Stop semantics](#stop-semantics),
    caller contract and package inventory. Anchors are key code spans,
    the worst-case formulas or short terms such as "stopping window is";
    the first occurrence after an anchor counts. Other timing statements
    in this document, the kill fixture's included, are not pinned. `TestReadmeTimingValuesExampleBlock`
    and `TestReadmeTimingValuesTable` check the README's commented
    `[tmux]` example block and its timing table: one entry per key in
    `config.TmuxKeys()` order, at its default, with its unit and safe
    minimum (or "none"). `TestReadmeTimingValuesClaudeCodeMinimum` checks
    that the README's Claude Code minimum statements (Prerequisites and
    the `no_exec_form` item, found through `operatorActionsItem`) state
    one version. Key names come from `config.TmuxKey`, never spelled.
    Run it with `go test ./pkg/api/ -run TestReadmeTimingValues -count=1`
    (in the sandbox).
- **Recovery wording scan** (`pkg/api/recovery_wording_test.go`, Epic 17;
  SR-18.4, SR-18.9, SR-18.16):
  - `TestRecoveryWordingScan` runs `apitest.AssertMustNot` with
    `apitest.DescReuseDocsForbidden()` over every manifest text
    (`eachManifestText`), every non-test Go source (`walkGoSources`,
    except `pkg/api/apitest/`, which spells the forms itself), and
    `pkg/api/README.md` and `pkg/ts-bun-client/README.md` (through
    `readMD`). It fails when it did not reach every source named in
    `recoveryWordingSites`, so it cannot pass vacuously. It does not read
    `README.md` or this document.
  - `TestRecoveryTSREADMERows` pins the TypeScript README's
    `ErrNoSessionId` and `ErrJsonlMissing` rows
    (`DescReuseRecourse(RecourseTSREADME)`) and its `ErrInstanceIdCollision` row
    (`DescInstanceIDCollision` in the full form).
  - `d.tableRow(name)` (an `mdDoc` method) returns the Markdown table row
    whose first cell is the code span `name`, and whether one exists.
  - The Go doc sites of SR-18.4 are pinned in
    `pkg/api/manifest/manifest_recovery_wording_test.go`
    (`TestRecoveryGoDocSites`), beside the Go doc readers: for
    `ErrNoSessionId`, `ErrJsonlMissing` and `ErrJsonlNeverWritten` it
    checks both the var's doc comment (`apiDeclDoc`) and its `Client.Resume`
    "Errors:" bullet (`goDocErrorBulletText`) with
    `DescReuseRecourse(RecourseGoDoc)`. Run both files with `-run Recovery`
    on `./pkg/api/` and `./pkg/api/manifest/`.

**Must use:** a later single-row verb (`resume`, reuse, `spawn`, the
sweeps, the finished-row opt-in) extends these rather than writing its own
row seeding, Recorder tables or call assertions: its tests build on the
kill fixture's pattern and world helpers and the pane-verb fixture's
invocation, seed and assertion helpers, every error it returns gets a row
through `assertOneName`, it appends its adapter to `callTableVerbs()`, its
SR-15 case to the security table, and its "Errors:" check goes through
`assertGoDocErrorsMatchManifest`.

**Must use:** a test of a row whose recorded name cannot be used takes
its names, kinds, description cases, notes and kept reasons from
`unusableNameFixtures()` (and its usable control from
`usableNameFixture()`), or the six tokens alone from `unusableNameTokens()`
/ `unusableNameTokenOf`, never a second name table or a spelled note or
reason; on the kill fixture it picks an entry with `unusableFixture` and
seeds a row with its own session through `seedUnusableRow`. A verb that
looks a row up gets the unusable-name columns by merging
`callTableUnusableRefused()` or `callTableUnusableNA` (or cells built from
the fixtures' `note` / `kept`) into its call-table cells, never columns of
its own.

**Must use:** a test that seeds a finished row against the
starting-session rule or kill's reported-in rule on the kill fixture
(`resume`'s starting-session tests, `kill`'s finished-row opt-in, a later
verb) seeds it through `seedStarting` and builds its description
parameters with `startingCase`, never its own `ended_at` or session-age
arithmetic. A kill-fixture test that runs `kill` with the opt-in calls it
through `killOptIn`, `killOptInWith` or `killOptInClient`, never on its
own; only the security table (whose scene builds its own client) calls
`adminapi.KillFinished` itself. `KillParams` has no field for the opt-in
(`TestKillParamsHasOnlyClaudeInstanceID`, b.vqr).

**Pane-verb test patterns.** Every recorded action must target a pane id
(`assertPaneCalls`). A state change between the row read and the send is
made with `sessionStartAfter` / `rowWriteAfter` (or `endAfterEnter` for
pause's wait), never with sleeps. Pause's wait runs through the test knob
seam (`fastPausePolls`, the 1 s `pauseTimeoutSeconds`). Store-write and
read failures use the failing-store wrapper (`failAdopt`, `refuseAdopt`,
`failStateReads`, `failPermissionRequests`), and trail-write failures the
re-exec pattern of `kill_trail_failopen_test.go`; no source seam is added
for either. Tests seed the row's own labelled session through the fixture
or the Recorder constructors, never hand-built label strings.

**Must use:** a later check of a README or doc section (a heading exists,
a pointer names it, a section's text says something) is built on
`readMD` / `mdDoc` / `mdAnchor` and the pointer collector, in
`readme_sections_test.go` or a sibling `readme_*_test.go`
(`readme_optin_test.go`, `readme_operator_actions_test.go`,
`readme_operator_actions_more_test.go`, `readme_store_id_test.go`,
`readme_timing_values_test.go`), never a second Markdown heading parser or
pointer scan. It finds "Operator actions" through `operatorActions`, an
item inside it through `operatorActionsItem` (never `d.titled` and its own range check), and a
section's lines through `sectionLines`; a check of the tmux commands or
numbered steps of a README section goes through `tmuxCommands` and
`numberedSteps`, never a second extractor, and a check of a numbered
procedure against expected steps goes through `checkSteps` with
`stepSpec`s, never its own step loop. A new pointer form is a new pattern
in `pointersIn`.

**Must use:** a later check of the AC-DOC-19 items names them by the
title constants in `readme_operator_actions_more_test.go`, never a spelled
title, and a step that may name the live states in more than one way takes
them from `liveStateForms()`, never a list of state literals. A check or a
run of the "This store's id" command reads it through
`storeIDItemCommands`, never its own scan of that item's code blocks; only
`readme_store_id_test.go` runs it, and only after `runnableStoreIDLine`'s
`-readonly` check.

**Must use:** a new doc statement of a `[tmux]` default, safe minimum or
worst case (in the README or this document) gets an entry in
`docTimingStatements()` in `readme_timing_values_test.go`, never a second
scan of the docs for timing values.

**Must use:** a later check that reads every Go source or every manifest
text (a wording scan, a pointer scan) walks them through `walkGoSources`
and `eachManifestText`, never its own `filepath.WalkDir` or loop over
`manifest.Verbs`. A check of one row of a Markdown table goes through
`mdDoc.tableRow`. A new forbidden form for agent-facing recovery,
collision or history-by-life wording goes into the lists behind
`apitest.DescReuseDocsForbidden`, which `TestRecoveryWordingScan` already
applies everywhere; a new site it must reach is a new entry in
`recoveryWordingSites`. Do not write a second scan.

### pkg/api expire fixture (package-internal test fixture)

`pkg/api/expire_fixture_test.go` (package `api_test`, no tests) extends the
[kill fixture](#pkgapi-kill-fixture-and-shared-verb-tables-reusable-test-fixtures)
for `expire` with methods on `*killEnv`; it adds no env type. Its doc
comments carry the detail.

- **Store wrapper.** `e.expireStore()` returns an `expireStore`, an
  `api.ExpireStore` over the env's real store with nothing injected:
  `failList(err)` fails every candidate read; `failDelete(id, err)` and
  `refuseDelete(id, res)` (`CondChanged` or `CondAbsent`) answer one row's
  delete without deleting; `beforeDelete(id, fn)` runs `fn` once just
  before that row's real delete (another caller's delete, or a write
  between the lookup and the delete). An injected error wins over an
  injected result, which wins over the hook.
- **Seeds** on the fixture clock: `e.finishedSpec(age, agentState,
  opts...)` (an `ended` row, `ended_at` `age` before the clock, no session;
  change `State`, `NoPane`, `NoServerIdentity` or `NoSession` on the spec)
  and `e.seedFinished(t, age, agentState, opts...)`. `agentAlive` keeps a
  row `process_alive`; the other agent states reach the lookup, which with
  no session is Gone. Other worlds come from the kill fixture's
  `seedSession` (Ours), `seedLeftover` (Leftover) and `tmuxfix` label
  options (a no-label name holder, another store's session,
  `WithRowSessionName` for `name_changed`).
- **Runs.** `olderThan(d)` is the override pointer (0 selects every
  finished row). `e.expireWithDays(s, days, over)` calls the exported
  `api.Expire` with the Recorder, the checker, a retention of `days`,
  `e.cfg`'s effective budget, the fixture clock and a fresh
  `recordingLogger` (returned for its lines); `e.expire(over)` (on `e.st`)
  and `e.expireWith(s, over)` run it at `config.DefaultExpireRetentionDays`.
  `e.expireClient(t, over, settings...)` calls `Client.Expire` on an
  `e.client` and returns the client's log; `e.expireClientDays(t, days)`
  calls `Client.Expire(nil)` on an `e.clientFor` whose config
  `apitest.WriteRetentionConfig` wrote with `days`, and returns the
  client's log.
- **Readers and assertions.** `expireKeptSince(t, mark)` and
  `expireDisagreesSince(t, mark, id)` read the run's `ad.expire.kept` and
  `ad.provenance.disagree` records after `trailMark`; pair the second with
  `assertDisagreeRecord(t, rec, r, "expire", "ad_expire", ...)`.
  `assertExpired(t, res, mark, want)` (`""` means deleted, a reason means
  kept) checks both lists (sorted, never nil), both counts, and exactly one
  `ad.expire.kept` with the right reason per kept row and none for any
  other id. `e.assertLookupsOn(t, sockets...)` checks the run made exactly
  one lookup per listed socket and no name-based call.

The tests built on it, one concern per file: `expire_test.go` (selection,
the clock-driven cutoff, the candidate read's failure, store errors, the
result shape, transcripts untouched, `Client.Expire`'s default retention),
`expire_reasons_test.go` (each kept reason), `expire_conditional_test.go`
(another caller's write between a row's examination and its delete,
through `beforeDelete`),
`expire_sweep_test.go` (a hung socket, the budget, unreachable tmux, a
different server, the no-socket rule), `expire_rows_test.go` (malformed
rows, names holding `$` or `\`, a non-ASCII name under a hostile locale) and `expire_trail_test.go` (both events,
every run, fail-open through the re-exec child). Beside them:

- **Retention** (`expire_retention_test.go`): no retention day count wraps
  the window (exported `Expire` through `e.expireWithDays`, at 106751, at
  106752 and up to `math.MaxInt`), and `Client.Expire` under each accepted
  `expire_retention_days` (0, 1, 106751) through `e.expireClientDays`, or,
  through `e.clientFor`, its load refusal (negative, above 106751), which
  touches no row. Its rows are `finishedSpec` rows seeded with
  `e.seedRow`. A missing key is `TestClientExpireDefaultRetention`'s case
  (`expire_test.go`).
- **Call-site table** (`lookup_calltable_expire_test.go`): `expire`'s
  adapter in `callTableVerbs()`, run under two agent states (process gone
  and not recorded). `callTableCell.kept` is the expected kept reason (`""`
  deleted); both action-failure columns are `na`, since `expire` makes only
  lookups.
- **Security** (`security_expire_test.go`): two `securityVerbs` entries,
  "expire" (kept cases, event `ad.expire.kept`) and "expire, deleted rows"
  (Gone cases, no event), plus `TestSecurityExpireStoreErrorLog`, which
  checks that a failed delete's one log line names the id and carries no
  planted value.
- **Store tests** (`internal/store/expire_candidates_test.go`,
  `row_version_expire_test.go`): the candidate read and the conditional
  delete, seeded through `apitest`; the delete's version cases join the
  SR-5.2 versioning test's tables.
- **Surfaces**: `cmd/agent-director/expire_cli_test.go` (the built CLI
  through fake-tmux: deleted, kept `ours`, and a hung lookup kept
  `cant_tell`), `internal/mcp/expire_test.go`, the envelope-diff cases in
  `test/envelope-diff/success_expire.go`, and `test/realtmux/expire_test.go`.

**Must use:** a new `expire` test in `pkg/api` seeds through
`seedFinished` / `finishedSpec`, runs through `e.expire` / `e.expireWith` /
`e.expireWithDays` / `e.expireClient` / `e.expireClientDays`, injects
store faults through `expireStore`, and checks
through `assertExpired` and `assertLookupsOn`. Never seed with
`apitest.SeedExpireFixture` there: it backdates by wall time and records no
socket.

### pkg/api reuse fixture (package-internal test fixture)

`pkg/api/spawn_reuse_fixture_test.go` (package `api_test`, no tests)
extends the [kill fixture](#pkgapi-kill-fixture-and-shared-verb-tables-reusable-test-fixtures)
and the [resume fixture](#pkgapi-resume-fixture-package-internal-test-fixture)'s
lookup and held-name helpers for `spawn` with the reuse opt-in (SR-10,
SR-20.2), with methods on `*killEnv`. Its doc comments carry the detail.

- **The reusable row.** `e.seedReusable(t, agentState, reuseRowSpec{...})`
  returns a `reuseRow`: a finished row with every column set (a full
  launch identity on the default socket with its recorded server and a
  bystander session, pid and start time, a session id with its transcript
  under a trust directory, life `reuseLife` (3), both liveness columns,
  well-formed but non-canonical raw `labels` and `claude_args`, `ended_at`
  a given age before the rule's instant, a parent row, history entries of
  this life and the one before, one open and one decided permission
  request). Variants on the spec: `State` `missing`, `NoSessionID`,
  `EndedAt` (`endedAged`, `endedNull`, `endedUnparseable`), `NoPreTrust`,
  `Malformed` (labels, `claude_args` and `extra_env` that do not decode),
  `Bare` (no parent, history or permission requests), `Child`, `Held`
  (the held-name instants), `ID` (a given instance id in place of the
  default `reuse-<8 hex>`) and extra `apitest` options. Raw variants are
  seeded through `seedRawRow`. `seedRelative` and `seedRequest` add a
  parent or child row and an open permission request.
- **The request and the runners.** `reuseParams(t, r, reuseRequest{...})`
  builds the opted-in `SpawnParams` (defaults: the row's recorded name and
  cwd, and `CLAUDE_CONFIG_DIR` at the row's trust directory, so the trust
  check covers the call's pre-trust; `Parent` sets
  `AGENT_DIRECTOR_INSTANCE_ID`). `e.reuse(t, p, settings...)` runs
  `Client.Spawn` with the fixture clock, the Recorder, the process fake and
  a captured logger; `e.reuseWith(t, rs, p, settings...)` runs it through
  the seam `api.SpawnWithReuseStore`.
- **The store seam.** `hookedReuseStore`, an `api.ReuseStore` over the
  real store: `failRead(err)` fails every later `ReadForReuse`;
  `afterRead`, `beforeReset` and `afterReset` run a function once (a
  competing write such as `SetParentID` or `DeleteSpawn`, or a competing
  call); `readCount()` counts reads (the lost-race re-read shows as 2).
  Store write failures go through `storefix.InjectWriteFailure` with the
  `WriteFailReuse*` kinds.
- **A real reuse.** `e.reuseLaunch(t, r, a, q)` reuses `r` with `q`
  through `Client.Spawn`, the new pane's agent in state `a`, failing
  unless it succeeds, and returns the row as reused and the
  `api.SpawnResult`. `e.reusedAs(t, r)` reads a reused row back (pending,
  its stored name and token, the session carrying the new token).
  `e.reusePending(t, a, spec, q)` seeds `spec`'s row with its old agent
  gone and runs `reuseLaunch`, giving a `pending` row made by a real
  reuse. `e.agentOnCreate(id, a)` puts the first pane of a session a
  successful create labels in the fake in state `a` (for `id`'s first
  create, or every create when `id` is `""`); `newReuseEnv(t)` is a kill
  fixture with it set for every create, for tests that reuse rows more
  than once.
- **Other arrangements.** `killRow.withName(name)` places a holder of a requested
  name through `seedHolder`; `e.snapshotReuse(t, r)` takes the
  `writesSnapshot` for `assertWroteNothing`; "duplicate session" is
  `arrangeHeld`; the agent's process state, the bound and the window come
  from the runners' arguments.

**Must use:** a later test of reuse, or of any path that must prove a
refusal of a finished-row launch changed nothing, seeds through
`seedReusable` (or `seedRawRow` for a row no `Spawn` read accepts), builds
its request with `reuseParams`, runs through `e.reuse` / `e.reuseWith`
(or `reuseLaunch` / `reusePending` when the reuse is the arrangement, not
the subject), injects reads and interleavings through `hookedReuseStore`, and checks
"wrote nothing" with `snapshotReuse` / `assertWroteNothing`; it never adds
a second reusable-row seeder, runner, store wrapper or "wrote nothing"
check. A row's columns are compared byte for byte only through
`apitest.ReadSpawnColumns`, and a NULL `ended_at` is seeded with
`apitest.WithNoEndedAt()`.

### ts-helper wrapper CLI

`test/smoke/ts-helper/` is a small Go binary (a normal Go program, no
build tags).  It bridges Go fixture-seed
helpers into the TypeScript smoke test suite so that Bun-side tests can shell
out to it for store / template setup without reimplementing the seeding logic
in TypeScript.

**Why a subprocess instead of reusing the apitest package directly?**

The existing `pkg/api/apitest` helpers all accept `*testing.T` and are
designed for Go-internal use.  Bun's test runner cannot call Go functions
in-process.  A thin CLI wrapper is the minimal seam that avoids duplicating
store-schema knowledge and state-machine details in a second language.

**Seeding sources.** The CLI seeds stores through `pkg/api/apitest`
(`SeedSpawn` and its options) and writes fake-tmux tables through
`internal/testsupport/faketmuxfix` with label values from `tmuxfix`'s
builders; it writes no row or label by hand. It is not linked into the
production binary.

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
| `seed-spawn` | `--store`, `--state`, `--id`, `--cwd`, `--relay-mode`, `--session-id`, `--create-store`, `--socket` (recorded tmux socket via `apitest.WithTmuxSocket`; default `apitest.TestSocket`), `--no-pre-trust` (records the pre-trust opt-out via `apitest.WithNoPreTrust`; default pre-trust allowed), `--no-launch-identity` (a row from before the release: no launch token, socket or identity, via `apitest.WithNoLaunchToken`; not with `--socket`) | `{"claude_instance_id": "..."}` |
| `seed-parent-child` | `--store`, `--parent-id`, `--child-id` | `{"parent_id": "...", "child_id": "..."}` |
| `seed-permission-request` | `--store`, `--spawn-id`, `--tool` | `{"request_id": <number>}` |
| `seed-template` | `--templates-dir`, `--name`, `--body` | `{"path": "..."}` |
| `seed-empty-store` | `--store` | `{"path": "..."}` |
| `seed-row-session` | `--store`, `--id`, `--capture` (the row's pane's capture text; empty uses the fake's default), `--tables-dir` (default `$FAKE_TMUX_TABLES`, else the table beside the socket) | `{"socket": "...", "session_id": "...", "pane_id": "...", "table": "..."}` |
| `json-schema` | — | machine-readable result-shape map for all subcommands |

`seed-row-session` (`row_session.go`) writes a seeded row's own labelled
session and pane into `test/fake-tmux`'s table for the row's recorded
socket, so a CLI `read-pane` or `send-keys` on the row finds it Ours and
reaches its pane by id. The token, socket, name and pane come from the
store (`apitest.ReadSpawnColumns`, `apitest.ReadStoreID`), the label values
from `tmuxfix.LabelValue` / `PaneLabelValue`; a row with no recorded pane
gets `apitest.TestPaneID`, found by its `@ad_pane`. It fails, writing
nothing, when the row records no launch token or socket or its pane is
already in the table. **Must use:** a TypeScript test of a pane verb seeds
the row's session with it, never by writing a table or a label itself.

**Must use:** a TypeScript test that needs a row from before the release
(no launch token, socket or identity) seeds it with `seed-spawn
--no-launch-identity`, never by clearing columns itself. The flag cannot be
combined with `--socket`: that call exits 1 with "--socket and
--no-launch-identity cannot be combined" and creates no store.

**Makefile target.**

`make ts-helper` builds `bin/ts-helper`. Its prerequisites
(`TS_HELPER_SRCS`) are every non-test `.go` source of the directories in
`TS_HELPER_SRC_DIRS` (every package of this module the binary links, `go
list -deps ./test/smoke/ts-helper`: the CLI itself, `pkg/api/apitest`,
`faketmuxfix`, `tmuxfix` and the other `internal/testsupport` packages,
`internal/spawn`, `internal/hook`, `internal/tmux`, `internal/config`,
`internal/store`, `internal/trail`) plus `go.mod` and `go.sum`, so Make
rebuilds when a seeder changes and skips the build otherwise. A package
that gains a module import extends the list, or the binary can go stale
(b.93m).

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
    make-template.test.ts
    list.test.ts
    pause.test.ts
    version.test.ts
  internal/
    tempHome.ts              # withTempHome() helper
    helper.ts                # runHelper() wrapper for ts-helper subprocess; privateTmuxSocket(); fakeTmuxCalls(), withProcessEnv(), CLAUDE_JSON, trustEntry(), seedOuterParent()
```

**`withTempHome` helper.**

`test/internal/tempHome.ts` exports `withTempHome(testFn)`.  It creates a fresh
`mkdtemp` directory, sets `process.env.HOME` and `process.env.PATH` in the main
thread through `withProcessEnv` (below), runs `testFn(homeDir)`, then restores
env and cleans up the temp dir (on success) or preserves it (on failure, for
inspection).

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
subprocess inherits this value and writes it as `parent_id` in the two
verb writes that carry one: `spawn`'s insert (`InsertPending`) and
`resume`'s move to `pending` (`MoveToPending`). No verb calls the
store's `SetParentID`; only the test seeder `apitest.SeedParentChild`
does. Because the test stores are
fresh SQLite files that do not contain a row for the session UUID, the
FOREIGN KEY constraint fails.

Any smoke test that exercises a verb that writes `parent_id` (`spawn`,
`resume`) pre-seeds a row with `id = process.env.AGENT_DIRECTOR_INSTANCE_ID`
in the same store before calling the verb. This satisfies the FK
constraint without altering the subprocess's inherited environment.

**fake-tmux stub.**

`test/fake-tmux` is the subprocess tmux double (see [test/fake-tmux: the
subprocess tmux double](#testfake-tmux-the-subprocess-tmux-double-reusable-test-fixture)).
`spawn` and `resume` create their sessions with the socket form
(`-u -S <socket>`: the labelled create, `spawn`'s label scan and
`resume`'s pre-launch lookup), which
the fake answers from its per-socket table; so does `kill` (its lookup,
pane listing and kills by id), and so do `read-pane`, `send-keys` and
`pause` (lookup, pane listing, then by pane id the capture, or the text
and Enter, with `pause`'s `C-u` line clear before its text).
A `read-pane` or `send-keys` test seeds the row's own labelled session
into the table first (`runHelper("seed-row-session", ...)`, with
`capture` for the text `read-pane` must return); without it the lookup is
Gone. The `pause` smoke cases cover only a finished row and a refused
state, which make no tmux call. Without a socket the fake answers only the
bare invocation and `has-session` (exit 1), which no verb sends; it refuses
any other socket-less call with the catalogue's no-server reply, so a verb
that named no socket would fail its smoke test.  The binary is built
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

It also exports `privateTmuxSocket(dir)`, which makes `<dir>/tmux` (mode
0700) and returns `<dir>/tmux/default`. Pass it as `seed-spawn --socket` for
a row a resume launches on: resume uses the row's recorded socket and refuses
one whose directory is missing. The resume happy paths in
`smoke/resume.test.ts` and `subprocess-smoke.test.ts` pass the temp HOME, so
the socket and the fake-tmux tables beside it are removed with it.

Its other shared helpers, each with its must-use rule:

- `fakeTmuxCalls(logPath)` returns the argv (`argv[0]` included) of each
  fake-tmux invocation in a `FAKE_TMUX_LOG` file, `[]` when nothing was
  logged. **Must use:** every TS test that reads the fake's log goes
  through it; never split the log format by hand (the Go side's
  counterpart is `faketmuxfix.ReadLog`).
- `withProcessEnv(vars, fn)` runs `fn` with each `process.env` variable set
  (`undefined` unsets it) and restores the prior values afterwards, even
  on a throw; a `Client`'s CLI inherits `process.env` per call. **Must
  use:** a new test that sets `FAKE_TMUX_*` (or any variable the CLI child
  must see) for one call uses it, never its own save/restore block.
- `CLAUDE_JSON`, a temp HOME's `.claude.json` as planted before a launch
  (no trust entry for any folder), and `trustEntry(claudeJsonPath, cwd)`,
  which reads `projects[cwd].hasTrustDialogAccepted` (undefined when
  absent). **Must use:** pre-trust tests plant and read `.claude.json`
  through these, never their own literal or parser.
- `seedOuterParent(storePath)` seeds the outer agent's row when the tests
  run inside an agent (whose `AGENT_DIRECTOR_INSTANCE_ID` a launch records
  as `parent_id`), so the foreign key holds; it does nothing otherwise.
  **Must use:** a TS test that launches into a fresh store seeds the outer
  parent through it, never its own seed.

**`smoke-invariants.test.ts` meta-test.**

Three static assertions are enforced by grepping test file contents:

| Assert | Rule |
| --- | --- |
| (a) | Every verb in `src/internal/verbs.ts::VERBS` has a `test/smoke/<verb>.test.ts` file. |
| (b) | Every smoke file imports `withTempHome` and calls it. |
| (c) | Every smoke file outside the allow-list contains `instanceof Err` or `toBeInstanceOf(Err`. |

Allow-list for (c): `version`, `expire`, `find-missing` — verbs whose
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

The guard defends against b.8dr: a host-side `go test` can rewrite the real
store no matter how `HOME` is set. Redirecting `HOME` does not hold as a
boundary: a test that does not redirect it, an absolute path to the real
store, a child binary started with the host's environment, or a home lookup
that reads the passwd entry instead of `$HOME` (a spawn cwd's `~`, SRD §7.2)
all still reach real state. The container is the only isolation boundary; the
bypass is not isolation, it is an assertion that there is nothing to isolate
from.

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
Epic 3's Go-side envelope-diff harness. Both suites run the same 15 callable
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

The resume success path seeds both store copies with one socket from
`privateTmuxSocket` in a per-test `ed-tmux-*` temp dir, and gives each run
its own `FAKE_TMUX_TABLES` dir (the CLI run through its env, the Client run
through `process.env`, restored in `finally`), so the Client's create never
meets the session the CLI run created. The temp dir is removed in `finally`.

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

Allow-list for (c): `version`, `expire`, `find-missing`.

**`make envelope-diff-ts`.**

The Makefile target builds all three required binaries incrementally, then
runs the two test files:

```
envelope-diff-ts: agent-director ts-helper fake-tmux
    cd pkg/ts-bun-client && bun test test/envelope-diff.test.ts test/envelope-diff-invariants.test.ts
```

It is wired into `make test` so `go test ./...` will not run unless the TS
envelope-diff suite passes first.
