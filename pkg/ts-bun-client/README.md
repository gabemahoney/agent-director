# agent-director

TypeScript/Bun client for agent-director. `Client.create()` discovers the
installed binary and each verb call runs it as a subprocess. Native request
and proof fields have the declaration limits described under [sendKeys](#sendkeys).

## Install

```sh
bun add agent-director
```

Requires Bun >=1.0.21. The package contains JavaScript with no lifecycle
scripts, optional platform dependencies or bundled binary. Installing with
`bun add --ignore-scripts agent-director` gives identical functionality.

Install the binary separately using the [install guide](../../docs/install.md).
`Client.create()` finds it at the standard install path or on `$PATH`;
its version must meet the [client floor](#minimum-required-cli-binary-version).

## Supported platforms

See [Requirements](../../docs/install.md#requirements) for supported binary
platforms. Package installation alone does not establish platform support.

## Quick start

`using` block (preferred):

```ts
using client = await Client.create({});
const v = await client.version({});
console.log(v.version);
```

The `version` result is `{ version, commit, config_dir }`; `config_dir` is the
directory the CLI keeps its files in, `~/.config/agent-director` (unexpanded).

Explicit `try/finally` (portable fallback):

```ts
const client = await Client.create({});
try {
  const v = await client.version({});
  console.log(v.version);
} finally {
  client.close();
}
```

All `ClientOptions` fields are optional. Omitted store, home and tmux options
use the binary's configuration and defaults. The `using` form calls
`client.close()` at block exit; TypeScript projects need
`"lib": ["ESNext.Disposable"]`.

- `storePath`: store path.
- `home`: home used for configuration and path expansion.
- `tmuxCommand`: tmux executable.
- `createIfMissing`: defaults to true; create the store on its first use.
  False leaves an absent store uncreated and returns `ErrStoreOpen`.
  Calls such as `version` that do not open the store are unaffected.
- `callTimeoutMs`: defaults to 30,000 ms per subprocess call. Allow enough
  time for configured pause waits, input delivery and reply overhead.

Paths are forwarded verbatim. The binary expands `~` against `home` when
set, otherwise `HOME`, never the passwd home. It applies `home` before
expanding `storePath` or `tmuxCommand`. Environment refusals and timeout handling are in the
[caller contract](../../docs/caller-contract.md#caller-contract); a timeout
does not prove the operation stopped.

For spawning under another home or store, use
[Permissions](../../docs/permissions.md#one-spawn-under-another-home-or-store).

## Verb examples

A call that carries an old name of a renamed key (on any verb) throws
`ErrInvalidFlags` before the client starts any process; its description names
the new key.

### spawn

Launch a tracked agent in a new tmux session. A spawn names its agent, `"claude"`, `"codex"` or `"cursor"` (case-sensitive), in `agent` or through its template; with neither it throws `ErrAgentMissing`. An empty `agent` counts as not given.

An explicit `instance_id` cannot contain ASCII control bytes (`0x00`–`0x1f`
or `0x7f`). Each `extra_env` key must be nonempty with no `=` or NUL;
reserved keys and account selection are covered by
[Settings](../../docs/settings.md#templates-and-per-call-merge).

```ts
const result = await client.spawn({ cwd: "~/my-project", agent: "claude" });
console.log(result.instance_id);
```

The `SpawnResult` has `instance_id`, `pre_trust` and optional
`dropped_args?: string[]`. On a switching reuse, `dropped_args` contains
removed original argument tokens in argv order; it is omitted when empty.
The switch starts fresh and resumes nothing. Selector lists and reporting
rules are in the
[caller contract](../../docs/caller-contract.md#spawn-and-template-input-refusals).

`pre_trust` uses the existing `"ok"`, `"skipped"` and `"failed"` values.
Cursor reports `"failed"` for unsupported pre-trust or `"skipped"` for its saved
opt-out, never a validated `"ok"`; see
[Cursor launch settings](../../docs/settings.md#cursor-launch-settings).

`idle_screen_timeout_seconds` gives the row its own `idle_screen` timeout in
whole seconds (`0`, `null` or omitted for none); its range and lifecycle are
in [Configuration](../../docs/configuration.md#idle-screen-timeout-state).

`reuse_finished: true` starts a fresh conversation on a finished id.
`resume({ instance_id: id })` continues the latest recorded session.
Life and replay rules are in [Usage](../../docs/usage.md#naming-the-agent)
and [Pause and resume](../../docs/usage.md#pause-and-resume).
Cursor startup, argument refusals and asynchronous failures are covered by
[Cursor operation boundaries](../../docs/caller-contract.md#cursor-operation-boundaries).

### status

Get the current lifecycle state of a Spawn.

`status`, `get`, `getMany`, `list` and `sweep` (also `findMissing`) share
[bounded recovery](../../docs/caller-contract.md#bounded-recovery-on-state-reads).
Single-row reads check only that row; read success does not confirm every write.

```ts
const result = await client.status({ instance_id: "<id>" });
console.log(result.state);
```

`StatusResult`, `GetResult` and each `ListRow` carry:

- `family_state: "working" | "waiting"`; see
  [Family state](../../docs/usage.md#family-state) and
  [Family reporting limits](../../docs/caller-contract.md#family-reporting-limits).
- `process_liveness: ProcessLiveness` (`"alive" | "exited" | "unknown"`).
- `confirmations: Confirmations`: exactly `state`, `process_liveness` and
  `displayed_state`. Each `Confirmation` has non-null `status`
  (`ConfirmationStatus`) and nullable `confirmed_at`, `source`
  (`ConfirmationSource`), `checked_at` and `reason` (`ConfirmationReason`).
  All five keys are present; see [Confirmations](../../docs/caller-contract.md#confirmations).
- Optional `displayed_state`: `"idle_screen"` or the defined-but-unused
  `"needs_intelligence"`. Test this field, not `state`, for a display.
- `idle_screen_availability: IdleScreenAvailability`: `"available"` for
  Claude/Codex, `"unavailable"` for Cursor.

`StatusResult` and `GetResult` also carry display detail, `since` and
`unchanged_minutes`; `StatusResult` adds `tmux_session_name`. Non-applicable
display keys are absent, never null (`result.displayed_state !== undefined`).
See [Displayed states](../../docs/caller-contract.md#needs-intelligence-and-idle-screen-rows).
`GetResult` and `ListRow`, but not `StatusResult`, carry
`state_changed_at: string`; see [State change time](../../docs/caller-contract.md#state-change-time).

State and family settling use the binary's
[`[state] settle_ms`](../../docs/configuration.md#settle-time-state).
Commands judge live state, which may differ from the reported state.

### getMany

```ts
const result = await client.getMany({ instance_ids: ["agent-a", "agent-b"] });
for (const row of result.spawns) {
  console.log(row.instance_id, row.state);
}
console.log(result.not_found);
```

Returns `GetManyResult`; each `spawns` element is `get`'s `GetResult`, with
the fields and guarantees described here. Ordering, the Id Cap and read
failures are defined in the
[caller contract](../../docs/caller-contract.md#reading-many-agents).

Before running anything, the client refuses with `ErrInvalidFlags` an ID
equal to `--store-path`, `--home`, `--tmux-command` or `--create-if-missing`,
starting with one of them followed by `=`, or containing a NUL byte. The
error names its 1-based position.

### list

Query Spawns with optional filters. `limit` is nonnegative; 0 or omitted means
no cap.

```ts
const result = await client.list({ state: ["waiting"] });
for (const spawn of result.spawns) {
  console.log(spawn.instance_id, spawn.state, spawn.agent);
}
```

Each row, like `get`'s result, carries `agent` (`"claude"`, `"codex"` or `"cursor"`), the agent the row runs.
A `state` filter keeps a row whose `state` or `displayed_state` is one of its
values, before any `limit`, so `["working"]` keeps a working row that shows
`"idle_screen"`. Filters and limit select stored IDs before recovery checks.
The result keeps those IDs even if refreshed values no longer match; it does
not refill slots or add newly matching rows. Recovery and family freshness
follow the [caller contract](../../docs/caller-contract.md#bounded-recovery-on-state-reads).

### listSubagents

Read the subagent records across launches of one agent's current life, and
whether the agent is tracked. `subagent_id` is unique among the returned
records; earlier-life records remain stored but are hidden. See
[visibility and retention](../../docs/usage.md#read-an-agents-subagent-records).

```ts
const result = await client.listSubagents({ instance_id: "<id>" });
console.log(result.tracked);
for (const sub of result.subagents) {
  console.log(sub.subagent_id, sub.state, sub.subagent_type ?? "unknown type");
}
```

`subagents` is never null; an unknown field is `null`, and times are RFC 3339
UTC strings. Agent support, `tracked`, field values and retention are defined
in [Read an agent's subagent records](../../docs/usage.md#read-an-agents-subagent-records).

### sendKeys

Send text to the agent's own pane, then one Enter to submit it. An empty
or omitted `text` sends the Enter only, submitting what is already typed.
`no_enter: true` types the text without the Enter, and `key` sends one key
alone (`"Escape"`, `"Enter"`, `"Up"`, `"Down"`, `"Tab"`, or one character),
never followed by Enter. With `expect_pane_sha256` (the `pane_sha256` of
the plain `readPane` you looked at, with its `n_lines`), nothing is sent if the
pane changed since (`ErrPaneChanged`); on a relayed Spawn a match also
passes the API's dialog hold (`ErrDialogMaybeOpen`, see "Answering a request at
the pane" below). Pass it only for a pane a person or an LLM judged, never
from an automatic flow.

`key` cannot accompany nonempty `text`; `no_enter` needs text or a key.
`as` requires `request_token`, and pane hashes must be valid SHA-256 values.

```ts
await client.sendKeys({
  instance_id: "<id>",
  text: "what is 2+2?",
});
```

On a supported native launch, ordinary text submission returns runtime proof
fields `outcome: "submitted"`, `request_id`, `session_id`, `record_sha256`
and optional `record_id`. The published `SendKeysParams` and `SendKeysResult`
do not expose request IDs or proof fields; use the Go API or CLI for explicit
delivery request IDs. Fallback and manual operations return `{}` on success.
Supported launches, proof, input restrictions and recovery are in
[Ordinary text submission](../../docs/caller-contract.md#ordinary-text-submission)
and [Manual input ownership](../../docs/caller-contract.md#manual-input-ownership).

`ErrSubmissionUnconfirmed.errDetails` is a
`Readonly<Record<string, unknown>> | null`. The exported
`SubmissionUnconfirmedDetails` covers only failed fallback hook observation
(`keys_sent: true`, `submission_confirmed: false`, `row_state`). Additional
runtime fields include `outcome`, `phase`, `bytes_attempted`,
`enter_may_have_been_sent`, `reason`, and native `request_id` and `last_queue`.
A cleanup `ErrInternal` after native confirmation retains the submitted
receipt in `errDetails`. Read these fields with runtime value checks; their
meanings and no-replay rules are in the linked contract, including
[Cursor input outcomes](../../docs/caller-contract.md#cursor-input-outcomes).

Pass `allow_pending: true` to allow input to a pending launch, for example a
startup choice with `key: "Enter"`. It defaults to false and preserves
ownership, readiness, relay and dialog guards; see
[Pending rows and startup screens](../../docs/caller-contract.md#pending-rows-and-startup-screens).

### readPane

Read the last N lines of the agent's own pane. `n_lines` is nonnegative;
0 or omitted selects 25 lines.

```ts
const result = await client.readPane({ instance_id: "<id>", n_lines: 50 });
console.log(result.pane, result.pane_sha256);
```

`pane_sha256` is the SHA-256 of exactly the bytes of `pane`; pass it back as
`sendKeys`' or `recordPaneAnswer`'s `expect_pane_sha256`, with the same
`n_lines`, when you act on what this pane shows. Use the default ANSI-stripped
read for this guard; the hash of an `ansi: true` read never matches.

`readPane` has no state guard — it works on `pending`, `ended`, and `missing`
rows as well as live ones. The `allow_pending` flag is accepted for symmetry
with `sendKeys` but has no behavioral effect.

---

### kill

End the agent of a live Spawn's current launch (`pending` included). `kill`
resolves only once the agent process is gone, and returns `{ kill_sent }`:
`true` when a kill was sent to the agent's pane or its session, `false` when
none was. On a finished Spawn (`ended` or `missing`) `kill` is a no-op that
resolves with `kill_sent: false`; that is not verification that the agent
exited. `kill` never changes the row's state: a state read or `sweep` marks it
once its agent process is gone.

```ts
const { kill_sent } = await client.kill({ instance_id: "<id>" });
console.log(kill_sent);
```

Errors include `ErrTmuxKillFailed`, `ErrTmuxUnresponsive`,
`ErrTmuxSessionConflict`, `ErrTmuxNotAvailable` and `ErrSpawnNotFound`.
Handling and the live-row sequence are in the
[caller contract](../../docs/caller-contract.md#caller-contract).

### sweep

`client.sweep()` returns `Promise<SweepResult>` for one bounded all-agent pass.
`checks: SweepCheck[]` carries each selected identity's values, three
confirmation records and independent `completion`/`reason`. Legacy counters
remain in the same result. See [Sweep results](../../docs/caller-contract.md#sweep-results)
for membership, coverage and failure handling.

`findMissing()` remains supported and invokes the CLI's `find-missing` alias;
`sweep()` invokes `sweep`. `SweepParams`, `SweepResult` and `SweepCheck` are
exported alongside the retained `FindMissingParams` and `FindMissingResult`
types. Both parameter types accept `timeout_ms` but ignore it: neither command
receives that flag. `callTimeoutMs` bounds the transport call; timeout proves
neither rollback nor completion and need not return partial checks.

### pause

Shut down a `waiting` Spawn and wait up to `pause.timeout_seconds` for `ended`.
A finished Spawn is a no-op. Codex exit handling is defined in the
[caller contract](../../docs/caller-contract.md#when-a-codex-agent-exits).

```ts
await client.pause({ instance_id: "<id>" });
```

`pause` takes no pane hash. It shares plain `sendKeys` permission and input
guards; see [Permission relay](../../docs/caller-contract.md#permission-relay)
and [Manual input ownership](../../docs/caller-contract.md#manual-input-ownership).

For `ErrPauseTimeout`, `errDetails` has the exported `PauseTimeoutDetails`
shape. Its fields and handling are defined in
[When pause cannot confirm exit](../../docs/caller-contract.md#when-pause-cannot-confirm-exit).

### makeTemplate

Save a reusable spawn preset. Pass `agent` to bake in the template's agent; a template with `agent_args` needs one. Pass `overwrite: true` to atomically replace an existing template; omit the field to keep the default rejection on collision.

Cursor templates use the same argument validation as `spawn` and can throw
`ErrSpawnDeniedFlag` before writing. Supported arguments and asynchronous mode
configuration failures are in
[Cursor operation boundaries](../../docs/caller-contract.md#cursor-operation-boundaries).
Argument replacement, including TypeScript's empty-list omission, is defined in
[Settings](../../docs/settings.md#the-agent-and-its-arguments).

```ts
await client.makeTemplate({ name: "dev", cwd: "/repos/widget", agent: "claude", overwrite: true });
```

### decide

Answer a relayed permission request of a Spawn launched with
`relay_mode: "on"`. `decide` records the verdict, waits at most 1 s for
relay acknowledgement, and resolves with the request's
`delivery`.

```ts
const result = await client.decide({
  instance_id: "<id>",
  request_token: "<token>",
  decision: "allow",
  max_wait_ms: 2000, // at most your deadline minus 1 s
});
if (result.delivery === "not_confirmed") {
  const later = await client.getPermission({ request_token: "<token>" });
  console.log(later.delivery, later.confirm_by);
}
```

Read `delivery` (`"delivered"` or `"not_confirmed"`), not the recorded
`decision`. `getPermission` exposes subsequent delivery (also `"fallen_back"`)
and `confirm_by`. `max_wait_ms` bounds the whole call
and has no default; it must be nonnegative and at most your deadline minus 1 s.
The [caller contract](../../docs/caller-contract.md#permission-relay) owns
confirmation, timing, refusal and closed-request handling.

`get` and each `list` row expose still-unanswered `permission_requests`.
Use `getPermission` to follow a request by token after it leaves those lists.
Cursor uses the same fields for permissions and blocking plans, with
`hook_alive: null`; see [Cursor permissions](../../docs/permissions.md#cursor-spawns).

`decision_reason` is `"operator"`, `"timeout"`, `"find_missing"`, `"ended"`,
`"pane"`, `"pane_outside"` or `"tool_ran"`. Read the verdict from `decision`,
not its reason. `proven_gone_at` and `proven_gone_how` (`ProvenGoneHow`:
`"tool_ran"`, `"turn_end"` or `"agent_gone"`) describe dialog proof;
`get.unproven_requests` lists remaining holds with `unproven_since`.
Delivery and pane-answer records are not dialog proof. The field meanings
are in [Permissions](../../docs/permissions.md#delivery).

### Answering a request at the pane

`sendKeys` with a `request_token` answers a request at the pane.
The [caller contract](../../docs/caller-contract.md#permission-relay) owns
when to use a pane answer and how to judge the screen. Example:

```ts
const seen = await client.readPane({ instance_id: "<id>" });
// having looked at seen.pane, the caller picks the key and the verdict it gives
await client.sendKeys({
  instance_id: "<id>",
  request_token: "<token>",
  as: "deny",
  key: "<key>",
  expect_pane_sha256: seen.pane_sha256,
});
```

It sends exactly that key and no Enter, then records the request
`pane_answer` `"sent"` with `decision` your `as` and `decision_reason`
`"pane"`. `as`, `key` and `expect_pane_sha256` are required.
Pane-answer calls cannot include `text`.
An `ErrInternal` whose `errDetails` is a `PaneKeySentDetails`
(`key_sent` `true`) means the key was sent but not recorded; hash use and
recovery are in the [caller contract](../../docs/caller-contract.md#permission-relay).
`recordPaneAnswer` records an answer made outside agent-director and types
nothing:

```ts
try {
  await client.recordPaneAnswer({
    request_token: "<token>",
    as: "unknown",
    expect_pane_sha256: "<pane_sha256>",
  });
} catch (e) {
  if (e instanceof ErrClaimTooSoon) {
    // errDetails is a ClaimTooSoonDetails here.
    console.log("retry at", e.errDetails?.["not_before"]);
  } else {
    throw e;
  }
}
```

`as` is `"allow"`, `"deny"` or `"unknown"`; use `"unknown"` when the answer
was not seen. `recordPaneAnswer` requires a valid `expect_pane_sha256`.
`ErrPaneChanged`, `ErrPaneAnswerInProgress`, `ErrClaimTooSoon` and
`ErrDialogMaybeOpen` carry structured facts in `errDetails`.
`recordPaneAnswer` types nothing; `sendKeys` with a request token sends one
key and records it. Delivery does not prove the dialog gone. See
[Permissions](../../docs/permissions.md) and the
[caller contract](../../docs/caller-contract.md#permission-relay) for the
complete pane-answer and held-agent procedures.

## Consumption

The supported consumption mode is Bun-runtime ESM:

```ts
import { Client } from "agent-director";
```

Bundling without support for module-relative resource resolution is unsupported.
Use the package as Bun-runtime ESM.

## Versioning

The package version equals its release tag. Use the matching binary release
for the full API; the [minimum version check](#minimum-required-cli-binary-version)
does not negotiate features.

## Minimum required CLI binary version

The library declares the minimum CLI-binary version it requires on two
surfaces, available from the published package at `dist/version-floor.json`.

**TS export (preferred for JS/TS consumers):**

```ts
import { MIN_BINARY_VERSION, DEV_SENTINEL_VERSION } from "agent-director";

console.log(`requires agent-director >= ${MIN_BINARY_VERSION}`);

if (binaryVersion === DEV_SENTINEL_VERSION) {
  // dev-built binary stamps the sentinel; accept it as satisfying the floor.
}
```

`MIN_BINARY_VERSION` is a strict-SemVer-2.0 string (e.g. `0.7.0` or
`0.7.0-rc1`). `DEV_SENTINEL_VERSION` is the literal `"0.0.0-dev"`
— a dev-built CLI binary stamps this value and satisfies any floor by
short-circuit. The library returns the binary's reported version
verbatim — no leading-`v` stripping, no normalization. Consumers
comparing two real versions should use a standard semver library;
agent-director does not export a comparator.

The floor is a construction check, not feature negotiation: an older CLI
that meets it can lack `sweep` or `getMany` (`ErrUnknownVerb`) and return
`status`/`get`/`list`/`findMissing` results without `process_liveness`,
`confirmations` or `checks`, which the client does not synthesize. Those
need the CLI release matching this package ([install guide](../../docs/install.md)).
`client.binaryVersion` is the installed CLI's version; `client.version()`
reports this package's version.

**Bash read pattern (for install scripts and non-JS consumers):**

```sh
jq -r .min_binary_version < node_modules/agent-director/dist/version-floor.json
```

This pattern is part of the public contract. It does not require the
agent-director CLI to be installed, does not spawn a JS runtime, and
does not require any agent-director-specific environment setup — read
the field from the file at the stable documented path.

## Supported Bun versions

Minimum: `>=1.0.21` (set in `engines.bun`). The `using` block syntax (Explicit Resource Management) requires Bun 1.0.21+.

## Errors

Named client and CLI errors extend `AgentDirectorError`, so you can catch
by subclass with `instanceof`. Subprocess spawn failures can also surface
as their original errors: an unclassified `ENOENT` (neither the binary nor
the caller's cwd is verifiably gone) and every other spawn failure are
re-thrown unchanged. Keep a general fallback catch for these paths:

```ts
import { Client, ErrSpawnNotFound } from "agent-director";
try {
  await client.status({ instance_id: "bogus" });
} catch (e) {
  if (e instanceof ErrSpawnNotFound) {
    // recover
  } else {
    throw e;
  }
}
```

Every `AgentDirectorError` has `errName`, `errDescription` and `errDetails`:
the error envelope's optional `err_details` object, or `null`. Eight errors
carry one, typed as exported interfaces: `ErrRelayFallenBack`
(`RelayFallenBackDetails`, with `OpenRequestFacts`), `ErrPaneAnswerInProgress`
(`PaneAnswerInProgressDetails`), `ErrClaimTooSoon` (`ClaimTooSoonDetails`),
`ErrPaneChanged` (`PaneChangedDetails`), `ErrDialogMaybeOpen`
(`DialogMaybeOpenDetails`), `ErrPauseTimeout` (`PauseTimeoutDetails`),
`ErrTooManyIds` (`TooManyIdsDetails`) and
`ErrSubmissionUnconfirmed`
(`SubmissionUnconfirmedDetails`, for failed fallback hook observation).
Native, Claude readiness, Claude paced typing, Codex paste and Cursor gateway
facts remain available through the generic `errDetails` record. So does the
`ErrInternal` of a pane answer whose key was sent but not recorded
(`PaneKeySentDetails`: `key_sent` `true` and `request_token`). Read facts from
`errDetails`, never from `errDescription`.

All classes below are exported from the package entry point. The tables name
what to catch and any client-specific fields. Shared retry, escalation and
refusal handling belong to the [caller contract](../../docs/caller-contract.md);
human repairs belong to [Operator actions](../../docs/operator-actions.md).

### 1. Library lifecycle

Raised by the client wrapper itself, independent of any agent-director binary.

| Error | When it fires |
|---|---|
| `ErrClientClosed` | A verb was called after `close()` or disposal. |
| `ErrBunVersionTooOld` | The Bun runtime is below the package minimum; thrown at construction. |

### 2. System install (agent-director binary)

**Construction-time** errors are thrown by `Client.create()` / `resolveSystemBinary()` before any verb runs; **runtime** errors are thrown at verb-dispatch time on a client that constructed successfully.

| Error | Phase | When it fires |
|---|---|---|
| `ErrSystemInstallNotFound` | Construction | No agent-director binary was found in any checked location. Carries `checkedLocations`. |
| `ErrSystemInstallTooOld` | Construction | A binary was found but its version is below the required floor. Carries `actualVersion`, `requiredVersion`, `binaryPath`. |
| `ErrSystemInstallUnreachable` | Construction | A binary was found but failed the version probe (not executable, wrong arch, non-zero exit, killed). Carries `binaryPath`, `reason`, `diagnostic`, `exitCode`, `signal`. |
| `ErrCallerCwdUnreachable` | Construction **and** runtime | `process.cwd()` does not resolve to a real directory. Thrown at construction if the cwd is already gone, or at the first verb call after the cwd disappears mid-flight. Carries `cwd`, `cause`. See [Installation](../../docs/install.md) for environment requirements. |
| `ErrSystemInstallDisappeared` | Runtime | The binary path resolved at construction no longer exists at verb-dispatch (uninstalled or replaced mid-flight). Carries `verb`, `binaryPath`, `cause`. Installation repair is owned by [Installation](../../docs/install.md). |

### 3. Per-call infrastructure

Thrown per verb call by the subprocess transport, not by the CLI's own validation.

| Error | Meaning |
|---|---|
| `ErrCallTimeout` | The subprocess exceeded `callTimeoutMs`; carries `verb`, `elapsedMs`, `timeoutMs`. |
| `ErrConsumerSignal` | The subprocess ended by signal; carries `verb`, `signal`. |
| `ErrUnknownErrorName` | An unmapped `err_name`; carries `unknownName` and the original `envelope`. |

### 4. Catalog-derived (CLI-side validation)

These classes map the binary's `err_name` contract. Input rules are in
[Spawn and template input refusals](../../docs/caller-contract.md#spawn-and-template-input-refusals),
[Cursor operation boundaries](../../docs/caller-contract.md#cursor-operation-boundaries)
and [Settings](../../docs/settings.md). Request handling is in
[Permission relay](../../docs/caller-contract.md#permission-relay).
**cwd validation**:

| Error | When it fires |
|---|---|
| `ErrCwdMissing` | No `cwd` was supplied to `spawn`. |
| `ErrCwdNotAPath` | `cwd` is neither absolute nor a `~/` path. |
| `ErrCwdNotFound` | The supplied or saved `cwd` does not exist or cannot be entered. |
| `ErrCwdNotADirectory` | The supplied or saved `cwd` is not a directory. |

**spawn and template config validation**:

| Error | When it fires |
|---|---|
| `ErrRelayModeInvalid` | `relay_mode` is not `on`, `off` or empty. |
| `ErrSpawnDeniedFlag` | `agent_args` contains a supervisor-owned or unsupported argument. |
| `ErrAgentMissing` | Neither the call nor its template names an agent. |
| `ErrAgentInvalid` | `agent` is not exactly `claude`, `codex` or `cursor`. |
| `ErrAgentArgsMismatch` | The template's `agent_args` belong to another agent. |
| `ErrTemplateAgentMissing` | A template has `agent_args` without an `agent`. |
| `ErrPermissionsUnsupported` | A Codex or Cursor spawn/template carries `allow`, `deny` or `ask` entries. |
| `ErrReservedEnvKey` | An `extra_env` key is reserved or invalid; call-level invalid names are refused before starting a process. |
| `ErrInstanceIdCollision` | Without `reuse_finished`, a row already exists for the supplied `instance_id`, in any state. With `reuse_finished: true`, the row is live (`pending` included), or it changed or was removed after this spawn examined it (a lost race); nothing was changed. |

**tmux session naming**:

| Error | When it fires |
|---|---|
| `ErrTmuxSessionNameEmpty` | An explicitly supplied `tmux_session_name` is empty. |
| `ErrTmuxSessionNameInvalid` | The name contains `#`, `:`, `.`, `$`, `\`, an ASCII control character or invalid UTF-8. |
| `ErrTmuxSessionNameTooLong` | The session name exceeds the byte cap. |

**spawn state / lookup**:

| Error | When it fires |
|---|---|
| `ErrSpawnNotFound` | No row matches the supplied `instance_id`. |
| `ErrSpawnNotInteractive` | The row cannot accept input under the current launch guards. |
| `ErrSubmissionUnconfirmed` | Submission is unconfirmed; inspect `errDetails` as described under [sendKeys](#sendkeys). |
| `ErrSpawnNotPausable` | The row is not in a pausable state. |
| `ErrPauseTimeout` | Exit input was sent but `ended` was not observed; carries `PauseTimeoutDetails`. |
| `ErrSpawnNotResumable` | The row is live, including `pending`, or changed during resume. |
| `ErrNoSessionId` | The spawn has no `session_id`, so there is nothing to resume. For any agent, spawn the same `instance_id` again with `reuse_finished: true` and the row's `agent` (`get` shows it), starting a fresh conversation with no memory of the old conversation; see [Transcript recovery](../../docs/caller-contract.md#transcript-recovery). For an artifact-preserving fresh generated-ID alternative, follow [Cursor operation boundaries](../../docs/caller-contract.md#cursor-operation-boundaries). |
| `ErrJsonlMissing` | The current session's transcript is unavailable. Cursor requires its protected gateway transcript; native session availability is decided later during load. Older sessions are never tried. For any agent, spawn the same `instance_id` again with `reuse_finished: true` and the row's `agent` (`get` shows it), starting a fresh conversation with no memory of the old conversation; see [Transcript recovery](../../docs/caller-contract.md#transcript-recovery). For an artifact-preserving fresh generated-ID alternative, follow [Cursor operation boundaries](../../docs/caller-contract.md#cursor-operation-boundaries). |
| `ErrListInvalidLabel` | A label filter is not in `key=value` form. |
| `ErrProbeUnsupported` | Retained error class; no current verb returns it. |

**tmux transport**:

| Error | Class | Meaning |
|---|---|---|
| `ErrTmuxNotAvailable` | ENVIRONMENT | tmux or its socket is unavailable |
| `ErrTmuxSessionCreate` | LAUNCH FAILURE | The session could not be created or labelled |
| `ErrTmuxUnresponsive` | UNAVAILABLE | tmux did not answer usably or a launch is still starting/stopping |
| `ErrTmuxSessionConflict` | CONFLICT | Session or pane ownership conflicts |
| `ErrTmuxKillFailed` | UNAVAILABLE | Exit could not be confirmed |
| `ErrTmuxSendKeys` | GONE | The target session or pane is absent |
| `ErrTmuxCaptureFailed` | GONE | The target session or pane is absent |

Handling and the limits of each class are in the
[caller contract](../../docs/caller-contract.md#caller-contract).
**relay / permissions**:

| Error | When it fires |
|---|---|
| `ErrSendKeysWhileRelayed` | A permission relay may still answer. |
| `ErrRelayFallenBack` | The relay settled without acknowledgement or a recorded pane answer; carries `RelayFallenBackDetails`. |
| `ErrDialogMaybeOpen` | A permission dialog is not proven gone; carries `DialogMaybeOpenDetails`. |
| `ErrPaneChanged` | The pane no longer matches `expect_pane_sha256`; carries `PaneChangedDetails` without a new hash. |
| `ErrPaneAnswerInProgress` | Another sender owns the request; carries `PaneAnswerInProgressDetails`. |
| `ErrClaimTooSoon` | An outside-answer claim is not yet allowed; carries `ClaimTooSoonDetails` with `not_before`. |
| `ErrRelayModeOff` | `decide` requires `relay_mode: "on"`. |
| `ErrInvalidDecision` | `decision` is neither `allow` nor `deny`. |
| `ErrMissingRequestToken` | `decide` received an empty `request_token`. |
| `ErrNoOpenPermissionRequest` | No eligible request matches the instance and token, including closed, proven-gone or obsolete requests. |
| `ErrAlreadyDecided` | The request has a verdict, relay acknowledgement or completed pane answer. |
| `ErrStoreBusy` | A bounded store operation did not finish in time. |
| `ErrPermissionRequestNotFound` | No permission-request row matches `request_token`. |
| `ErrAmbiguousRequest` | No token was supplied and multiple open requests exist. |

**config templates**:

| Error | When it fires |
|---|---|
| `ErrTemplateNameUnsafe` | The template name fails path/name safety checks. |
| `ErrTemplateNotFound` | The named template does not exist. |
| `ErrTemplateMalformed` | The template has invalid, unknown, duplicate case-insensitive or wrong-typed keys/values. |
| `ErrTemplateExists` | The template exists and `overwrite` is not true. |

**misc**:

| Error | When it fires |
|---|---|
| `ErrInvalidFlags` | A parameter, key or parameter combination is invalid; no change or input occurred. |
| `ErrTooManyIds` | `getMany` exceeds the configured Id Cap; carries `TooManyIdsDetails.id_cap`. |

**Configuration and store setup**:

| Error | When it fires |
|---|---|
| `ErrConfigMalformed` | The binary cannot read or validate its configuration. |
| `ErrStoreOpen` | The store cannot be opened, including an absent store with `createIfMissing: false`. |
| `ErrSchemaMismatch` | The store schema, store id or input authority is incompatible. |
| `ErrSchemaMigrationRequired` | The store needs an administrator-run migration. |
| `ErrOldLayout` | The default store still uses the old directory layout. |

**Other binary errors**:

| Error | When it fires |
|---|---|
| `ErrInternal` | An otherwise unclassified failure; pane input may carry `PaneKeySentDetails`. |
| `ErrJSONMarshal` | The verb succeeded but its result could not be serialized. |
| `ErrUnknownVerb` | The installed binary does not know the requested verb. |
| `ErrTrailWrite` | Internal trail-writing failure; no client method returns it. |

Requests recorded by an older binary retain the
[legacy delivery rules](../../docs/permissions.md#requests-recorded-by-an-older-binary),
including derived delivery after expiry even when closed. Compatibility
refusals for a request recorded by an older binary are defined there too.

## See also

[Caller contract](../../docs/caller-contract.md), [Permissions](../../docs/permissions.md)
and [Installation](../../docs/install.md) own shared procedures.
