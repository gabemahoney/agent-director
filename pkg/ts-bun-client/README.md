# agent-director

TypeScript/Bun client for the agent-director CLI. Shares the Go API's verbs
and CLI semantics; native request and proof fields have the declaration and
runtime limitations described under [sendKeys](#sendkeys).
The `Client` discovers the system-installed
`agent-director` CLI binary at construction time and drives it as a
subprocess per verb call — no FFI, no network hop, no bundled binary.

## Install

```sh
bun add agent-director
```

Requires Bun >=1.0.21. The package ships pure JavaScript — there are
**no lifecycle scripts** (`postinstall`, `prepare`, etc.) and no
optional platform dependencies. `bun add --ignore-scripts agent-director`
is a no-op and installs the library with identical functionality.

Install the CLI separately using the [install guide](../../docs/install.md).
`Client.create()` finds it at the standard install path or on `$PATH`;
its version must meet the client floor.

## Supported platforms

- `linux/x64` (Linux on x86_64)
- `darwin/arm64` (Apple Silicon Mac)

Package installation may also succeed on `linux/arm64` and `darwin/x64`;
those are not supported CLI install hosts. See [Requirements](../../docs/install.md#requirements).

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

All constructor options are optional. Omitted fields fall back to the CLI binary's own three-tier default resolution (config.toml value, then hardcoded fallback such as `~/.config/agent-director/state.db`) — the CLI is the single source of truth for defaults. The `using` form calls `client.close()` automatically at block exit and requires Bun >=1.0.21 (or a TypeScript project with `"lib": ["ESNext.Disposable"]`).

`ClientOptions` overrides forward to the CLI subprocess as global flags:

- `storePath` → `--store-path`
- `home` → `--home`
- `tmuxCommand` → `--tmux-command`
- `createIfMissing: false` → `--create-if-missing false` (`true` forwards nothing)

The paths are forwarded verbatim. The CLI, not the client, expands a `~` in
them, against `HOME` and nothing else (never the passwd entry's home). It applies `home` first, so with `home`
set, a `~/` in `storePath` or `tmuxCommand` resolves under `home`. With
`HOME` unset or empty, a `home` of `~` or `~/…` rejects every call with
`ErrInvalidFlags`, and with no `home` either, every call that opens the store
is refused with `ErrStoreOpen`, whatever `storePath` is, because the CLI cannot
expand its config path `~/.config/agent-director/config.toml`.

Set them when the consumer needs to override that field's CLI default.

`callTimeoutMs` defaults to 30,000 ms per subprocess call. Cursor's separate
45-second ACP operation bound does not extend that client budget. Spawn and
resume do not wait for initialization, load, generation, a dialog answer or
readiness. For a deliberately longer
configured `pause.timeout_seconds`, give the client enough `callTimeoutMs`
for that wait and reply overhead; 30 seconds is not a universal operation cap.

By default, the first call that opens the store creates it when it does not
exist (parent directories, file and schema). With `createIfMissing: false`,
such a call rejects with `ErrStoreOpen` (`database not initialized`) and
creates none of it; an existing store opens as usual, and calls that open no
store, such as `version`, are unaffected.

For spawning under another home or store, use
[Permissions](../../docs/permissions.md#one-spawn-under-another-home-or-store).

## Verb examples

Shared CLI use is in [Usage](../../docs/usage.md).

A call that carries an old name of a renamed key (on any verb) throws
`ErrInvalidFlags` before the client starts any process; its description names
the new key.

### spawn

Launch a tracked agent in a new tmux session. A spawn names its agent, `"claude"`, `"codex"` or `"cursor"` (case-sensitive), in `agent` or through its template; with neither it throws `ErrAgentMissing`. An empty `agent` counts as not given.

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

For all agents, same-agent reuse retains current-life session history and subagent records;
switching agents starts another life, including when switching back.
`resume` continues only the recorded agent's latest reported session and
never selects an archived one. See [Usage](../../docs/usage.md#naming-the-agent)
and [Transcript recovery](../../docs/caller-contract.md#transcript-recovery).

Cursor uses the existing result and row fields; `status` keeps its existing
shape. `resume({ instance_id: id })` launches load of only its latest saved session;
the row can remain `pending` until a real first prompt. After inspecting the
pane, pass `allow_pending: true` to `sendKeys` for that prompt; binding and
dialog guards still apply. Saved configuration and initial-prompt replay are
defined in [Pause and resume](../../docs/usage.md#pause-and-resume).
`reuse_finished: true` supports Cursor and agent switches under the linked
life rules. `makeTemplate`
and `spawn` refuse nonempty Cursor permission lists with
`ErrPermissionsUnsupported`. The fresh workflow is in
[Usage](../../docs/usage.md#fresh-cursor-use); argument and asynchronous failure
handling are in the [caller contract](../../docs/caller-contract.md#cursor-operation-boundaries).

### status

Get the current lifecycle state of a Spawn.

```ts
const result = await client.status({ instance_id: "<id>" });
console.log(result.state);
```

`StatusResult`, `GetResult` and each `ListRow` expose
`family_state: "working" | "waiting"`, separately from `state`.
[Usage](../../docs/usage.md#family-state) defines membership and raw
aggregation; the [caller contract](../../docs/caller-contract.md#family-reporting-limits)
defines reporting limits.

`state` is a `string` and can be `"needs_intelligence"` or `"idle_screen"`.
Then `StatusResult` and `GetResult` carry `since` and that state's detail:
`request_token` and `tool_name` for `"needs_intelligence"`, or
`unchanged_minutes` for `"idle_screen"`; `StatusResult` also carries
`tmux_session_name`, and each `ListRow` carries `state`, no detail. A key that
does not apply is absent from the result, never `null`, so test for presence
(`result.since !== undefined`). Every `StatusResult`, `GetResult` and
`ListRow` carries `idle_screen_availability` (`IdleScreenAvailability`:
`"available"` for Claude and Codex, `"unavailable"` for Cursor).
The [caller contract](../../docs/caller-contract.md#needs-intelligence-and-idle-screen-rows)
defines the states, fields, omissions and detection.

`GetResult` and each `ListRow` carry `state_changed_at: string`, when the raw
state last changed; `StatusResult` does not. The
[caller contract](../../docs/caller-contract.md#state-change-time) defines it
and how it differs from `last_seen_at`, the settled `state` and `since`.

`status`, `get` and `list` use `[state] settle_ms` in the CLI's
`~/.config/agent-director/config.toml` for [independent state and family
settling](../../docs/configuration.md#settle-time-state). Verbs that act on a
Spawn judge its live state, which can be newer than its reported state.

### list

Query Spawns with optional filters.

```ts
const result = await client.list({ state: ["waiting"] });
for (const spawn of result.spawns) {
  console.log(spawn.instance_id, spawn.state, spawn.agent);
}
```

Each row, like `get`'s result, carries `agent` (`"claude"`, `"codex"` or `"cursor"`), the agent the row runs.
A `state` filter of `"needs_intelligence"` or `"idle_screen"` is matched on
the displayed state, before any `limit`.

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

`tracked` is `false`, with `subagents` `[]`, for a `"cursor"` agent, and for a `"codex"` agent (from its spawn, and after every `resume` or reuse of its id) and for a `"claude"` agent that predates subagent tracking (it becomes `true` when that agent is next resumed or its id is reused). `subagents` is never null; a field with no known value is `null`, and times are RFC 3339 UTC strings. The fields and values are described in [Read an agent's subagent records](../../docs/usage.md#read-an-agents-subagent-records).

### sendKeys

Send text to the agent's own pane, then one Enter to submit it. An empty
or omitted `text` sends the Enter only, submitting what is already typed.
`no_enter: true` types the text without the Enter, and `key` sends one key
alone (`"Escape"`, `"Enter"`, `"Up"`, `"Down"`, `"Tab"`, or one character),
never followed by Enter. With `expect_pane_sha256` (the `pane_sha256` of
the `readPane` you looked at, with its `n_lines`), nothing is sent if the
pane changed since (`ErrPaneChanged`); on a relayed Spawn a match also
passes the dialog hold (`ErrDialogMaybeOpen`, see "Answering a request at
the pane" below). Pass it only for a pane a person or an LLM judged, never
from an automatic flow.

```ts
await client.sendKeys({
  instance_id: "<id>",
  text: "what is 2+2?",
});
```

On a supported native launch, ordinary nonempty text with Enter and no pane
hash returns runtime proof fields
`outcome: "submitted"`, `request_id`, `session_id`, `record_sha256` and
`record_id` when the native record supplies it. They confirm the exact
normalized text in the launch's native transcript. The published
`SendKeysParams` and `SendKeysResult` declarations do not expose native request
IDs or proof fields; use the Go API or CLI for explicit delivery request IDs.
Launches without
native capability retain literal text and one Enter, return an empty object on
success and make no exact transcript-confirmation claim. Manual
operations (`no_enter`, `key`, empty text, pane hashes and permission answers)
return an empty object on success; their input-ownership refusals are in
[Manual input ownership](../../docs/caller-contract.md#manual-input-ownership).
Supported launches, omitted-ID behavior, structured
outcomes and recovery are in
[Ordinary text submission](../../docs/caller-contract.md#ordinary-text-submission).
Allow sufficient `callTimeoutMs` for that delivery window; a timeout does not
cancel submission.

Pass `allow_pending: true` to also allow a `pending` row: a launch (spawn,
reuse or resume) whose agent has not reported in yet, for example to dismiss a
prompt the agent shows before it reports in. Keys are delivered only to a
session started by the row's current launch. `ended` and `missing` rows are
rejected, including retries of an existing delivery request. State, relay and
dialog guards precede native capability selection and request reconciliation.
A `pending` row whose launch start or launch token is not recorded, or where
only a session an earlier launch left behind is found, gets
`ErrSpawnNotInteractive` and nothing is sent.

`allow_pending` preserves native readiness and transcript requirements on
supported launches; it does not make a startup dialog an ordinary composer.
For fallback input to a pending Codex row, the bounded hook-state observation
and its unconfirmed result are described in the caller contract.
`SubmissionUnconfirmedDetails` describes that fallback payload only:
`keys_sent: true`, `submission_confirmed: false` and `row_state`. Native
uncertainty adds `outcome`, `request_id`, `phase`, `bytes_attempted`,
`enter_may_have_been_sent`, optional `last_queue` and `reason` at runtime.
Read these through `AgentDirectorError.errDetails`, whose type is
`Readonly<Record<string, unknown>> | null`, and check their values; see
[Ordinary text submission](../../docs/caller-contract.md#ordinary-text-submission)
for the fields and recovery.

`liveness_note: "unreported"` means an alive agent has not reported since
launch. Action on a pending row belongs to the
[caller contract](../../docs/caller-contract.md#caller-contract).

### readPane

Read the last N lines of the agent's own pane (default 25).

```ts
const result = await client.readPane({ instance_id: "<id>", n_lines: 50 });
console.log(result.pane, result.pane_sha256);
```

`pane_sha256` is the SHA-256 of exactly the bytes of `pane`; pass it back as
`sendKeys`' or `recordPaneAnswer`'s `expect_pane_sha256`, with the same
`n_lines`, when you act on what this pane shows.

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
exited. `kill` never changes the row's state: `find-missing` marks the row once
its agent process is gone.

```ts
const { kill_sent } = await client.kill({ instance_id: "<id>" });
console.log(kill_sent);
```

Errors to catch: `ErrTmuxKillFailed` and `ErrTmuxUnresponsive` (UNAVAILABLE:
retry later), `ErrTmuxSessionConflict` (CONFLICT),
`ErrTmuxNotAvailable` (ENVIRONMENT) and
`ErrSpawnNotFound`. None of these proves exit. See the [caller contract](../../docs/caller-contract.md#caller-contract)
for the live-row sequence and finished-row limits.

### pause

Shut down a `waiting` Spawn: type `/exit` in its pane and wait up to
`pause.timeout_seconds` for `ended`. A finished Spawn is a no-op. A live Codex
Spawn whose agent already exited with an end report resolves, in any live
state, having typed nothing, and its row is `ended`; otherwise any live state
but `waiting` throws `ErrSpawnNotPausable`. On a Codex Spawn
`pause` ends the row itself once the agent exits (see the
[caller contract](../../docs/caller-contract.md#when-a-codex-agent-exits)).

```ts
await client.pause({ instance_id: "<id>" });
```

For input ownership or journal failures (`ErrInternal` before any input), see
[Manual input ownership](../../docs/caller-contract.md#manual-input-ownership).

For `ErrPauseTimeout`, `errDetails` has the exported `PauseTimeoutDetails`
shape. Its fields and required inspection procedure are defined in
[When pause cannot confirm exit](../../docs/caller-contract.md#when-pause-cannot-confirm-exit).

`pause` is held like a `sendKeys` without `request_token` and without
`expect_pane_sha256`: on a Spawn with `relay_mode: "on"` it throws
`ErrSendKeysWhileRelayed`, `ErrRelayFallenBack` or `ErrDialogMaybeOpen`,
with the same `errDetails`, typing nothing and starting no wait, whenever
that `sendKeys` would. It takes no pane hash, so it stays held until
valid agent evidence proves each request gone; see
[The dialog hold](../../docs/permissions.md#the-dialog-hold).
Stopping a held spawn and handling these refusals are in the
[caller contract](../../docs/caller-contract.md#permission-relay).

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

`"delivered"` means durable relay acknowledgement, not agent receipt,
execution or dialog closure. `"not_confirmed"` means no acknowledgement yet;
`getPermission` exposes subsequent delivery and `confirm_by`. See the
[caller contract](../../docs/caller-contract.md#permission-relay) for
confirmation handling. `decision`
is the verdict recorded, not the outcome: read `delivery`. `max_wait_ms`
bounds the verb's reads, write, cleanup and reporting under one original
deadline, and has no default; without it `decide` can wait up to the store's
busy timeout. Allow the reply margin shown above for CLI audit teardown;
its stderr diagnostic does not change the structured verdict/error. Exact
timing and audit limits are owned by the
[caller contract](../../docs/caller-contract.md#permission-relay). `decide` rejects,
recording no verdict, with `ErrRelayFallenBack` when the request's relay
settled without acknowledgement, with `ErrStoreBusy` when `max_wait_ms`
ran out before the verdict was recorded, and with
`ErrNoOpenPermissionRequest` when no open request has that token or the
request is closed. Pane answers, retries and closed-request handling are
in the [caller contract](../../docs/caller-contract.md#permission-relay). The
`permission_requests` of `get` and of each `list` row carry the same
delivery fields, `pane_answer` and `pane_as` included, for requests that
still await an answer only; a row can read `waiting`, `working` or
`ask_user` while a request is still open, so follow the requests you track
with `getPermission`.

Cursor uses these same methods and fields for permissions and blocking plans.
Its `hook_alive` is null; `confirm_by` is finite and unchanged by recreating
a client. Without acknowledgement, no relay answer is sent, including a denial. Cursor's once-only decisions,
native choices, question denial and conservative unknown-origin holds are
defined in [Cursor permissions](../../docs/permissions.md#cursor-spawns);
uncertain answers and fallback follow the
[caller contract](../../docs/caller-contract.md#permission-relay).

`decision_reason` is one of `"operator"`, `"timeout"`, `"find_missing"`,
`"ended"`, `"pane"`, `"pane_outside"` and `"tool_ran"`. For a reason you
do not know, read the recorded verdict from `decision` (`null` for a
`"pane_outside"` `"unknown"` claim) and the outcome from `delivery` and
`pane_answer`; never read a reason as a verdict.

`proven_gone_at` and `proven_gone_how` (`ProvenGoneHow`: `"tool_ran"`, Claude's
tool ran or Cursor supplied validated exact matching invocation proof under
[Cursor permissions](../../docs/permissions.md#cursor-spawns), including a failed
MCP invocation that proves dialog closure without inferring execution or changing
decision, delivery or pane-answer fields;
`"turn_end"`, an eligible main request's turn ended under the age and identity
guards; or `"agent_gone"`, the agent ended) say when and how the request's
dialog was proven gone; `"delivered"` and a pane answer are
agent-director's own records, not proof. `get`'s `unproven_requests` lists
every request not yet proven gone, each with `unproven_since`. A request
that reads `"delivered"` or closed at the pane can remain unproven; its dialog
may still be there. Handling is in the
[caller contract](../../docs/caller-contract.md#permission-relay).

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

`as` is the caller's claim; use `"unknown"` when the answer was not seen.
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

The library version equals the agent-director release tag — released in lockstep:

| npm package | CLI binary |
|---|---|
| `agent-director@v0.5.0` | `agent-director CLI v0.5.0` |

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
the error envelope's optional `err_details` object, or `null`. Seven errors
carry one, typed as exported interfaces: `ErrRelayFallenBack`
(`RelayFallenBackDetails`, with `OpenRequestFacts`), `ErrPaneAnswerInProgress`
(`PaneAnswerInProgressDetails`), `ErrClaimTooSoon` (`ClaimTooSoonDetails`),
`ErrPaneChanged` (`PaneChangedDetails`), `ErrDialogMaybeOpen`
(`DialogMaybeOpenDetails`), `ErrPauseTimeout` (`PauseTimeoutDetails`) and
`ErrSubmissionUnconfirmed`
(`SubmissionUnconfirmedDetails`, for fallback pending-Codex uncertainty).
Native submission facts remain available through the generic `errDetails`
record. So does the `ErrInternal` of a
pane answer whose key was sent but not recorded (`PaneKeySentDetails`:
`key_sent` `true` and `request_token`). Read facts from `errDetails`, never
from `errDescription`.

The public typed-error surface falls into four groups. Every class named below is exported from the package entry point. The tables are self-sufficient for choosing what to catch; the CLI error contract also applies to group 4.

For retry, escalation and shared CLI refusal handling see the
[caller contract](../../docs/caller-contract.md). Binary installation and
upgrade procedures are in [Installation](../../docs/install.md), and
human repairs in [Operator actions](../../docs/operator-actions.md).
The tables below describe the typed classes and their fields.

### 1. Library lifecycle

Raised by the client wrapper itself, independent of any agent-director binary.

| Error | When it fires |
|---|---|
| `ErrClientClosed` | A verb was called on a `Client` after `close()`/disposal. Obtain a fresh handle with `Client.create()`. Programmer error. |
| `ErrBunVersionTooOld` | The running Bun runtime is below the package's minimum. Fires at construction. |

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

| Error | When it fires | Category |
|---|---|---|
| `ErrCallTimeout` | The subprocess did not complete within the configured per-call timeout. Carries `verb`, `elapsedMs`, `timeoutMs`. | Operational; see the caller contract. |
| `ErrConsumerSignal` | The subprocess was killed by an OS signal (e.g. `SIGTERM`, `SIGINT`) before producing a result. Carries `verb`, `signal`. | Operational — routine during shutdown. |
| `ErrUnknownErrorName` | The CLI returned an error envelope whose `err_name` this client has no class for. This client has a class for every `err_name` the binary of its own version gives, so the binary is of a different version, most often a newer one. Carries `unknownName` (the real `err_name`) and `envelope`. | Version error; see [Installation](../../docs/install.md). |

### 4. Catalog-derived (CLI-side validation)

These classes correspond to the shared CLI `err_name` contract. They surface bad input, a verb's own state preconditions, a config or store the CLI cannot open, or a failure of the CLI itself — almost all are either **programmer error** or a **normal operational signal**, so few catch sites need to name them individually. They are grouped by domain below.

**cwd validation** (bad `cwd` argument to `spawn` — programmer error):

| Error | When it fires |
|---|---|
| `ErrCwdMissing` | No `cwd` was supplied; spawn requires it to derive the JSONL/resume path. |
| `ErrCwdNotAPath` | `cwd` is neither absolute nor a `~/` form (URLs, bare relative paths, non-path values). |
| `ErrCwdNotFound` | `cwd` resolved to a path that does not exist on disk. |
| `ErrCwdNotADirectory` | `cwd` resolved to a file (or other non-directory inode). |

**spawn and template config validation** (bad `spawn` or `makeTemplate` arguments — programmer error):

| Error | When it fires |
|---|---|
| `ErrRelayModeInvalid` | `relay_mode` was something other than `on` / `off` / empty. |
| `ErrSpawnDeniedFlag` | Cursor argument refusals and switch selector rules are in [Cursor operation boundaries](../../docs/caller-contract.md#cursor-operation-boundaries). The `agent_args` of a `spawn` contain an argument the supervisor must own; nothing is written, and the description names it. A `"claude"` spawn always refuses `--settings`, `--output-format` and print (`--print`, `-p`, parsed clusters included). Session selectors and owned values are refused except on a verified finished-agent switch, where they are removed and the remainder is checked as for a plain spawn. Exact denied long names owned as option values always refuse. A `"codex"` spawn also refuses other Codex subcommands as the first word that is neither an option nor an option's value, `--remote`, `--remote-auth-token-env`, a `-c` / `--config` key at, above or under `hooks`, `tui.disable_paste_burst`, `features.hooks` or its alias `features.codex_hooks`, `--disable hooks` or `--disable codex_hooks`, and, while `disable_ask_user` is on, `--enable default_mode_request_user_input` and the question-tool keys. `resume` filters stored arguments without applying spawn's argument refusals; see [Pause and resume](../../docs/usage.md#pause-and-resume) for replay rules. The refused forms and argument recognition limits are in [Caller contract](../../docs/caller-contract.md#spawn-and-template-input-refusals). |
| `ErrAgentMissing` | `spawn` got no `agent` and its template names none; nothing is written. Pass `agent` (`"claude"`, `"codex"` or `"cursor"`), or put one in the template. |
| `ErrAgentInvalid` | `agent` is none of `"claude"`, `"codex"` or `"cursor"` (the value is case-sensitive); nothing is written. |
| `ErrAgentArgsMismatch` | The template's `agent_args` are for another agent than the one this spawn resolves to, so they are not inherited; nothing is written. Spawn that agent, or pass this call's own non-empty `agent_args`, which replace the template's. |
| `ErrTemplateAgentMissing` | A template has `agent_args` and names no agent. From `makeTemplate` (`agent_args` without `agent`) and from a `spawn` that loads such a template; nothing is written. Give the template an `agent`, or remove its `agent_args`. |
| `ErrPermissionsUnsupported` | A `"codex"` or `"cursor"` spawn/template carries an `allow`, `deny` or `ask` entry; nothing is written. Handling is in [Permissions](../../docs/permissions.md#codex-spawns) and [Cursor permissions](../../docs/permissions.md#cursor-spawns). |
| `ErrReservedEnvKey` | From `spawn`: an `extra_env` key (a template's included) is reserved or is not a valid env-var name; nothing is written or launched, and the description says which. Reserved: an `AGENT_DIRECTOR_*` key, or a key that sets `HOME` (`HOME` itself, or one such as `HOME=/x`), with any value, an empty one included. An `extra_env` `HOME` would send the agent's hook events to another agent-director store; set an absolute `CLAUDE_CONFIG_DIR` (a Claude Code agent) or `CODEX_HOME` (a Codex agent) to give the agent its own config. Not a valid name: the key is empty, or contains `=` or a NUL character; give each variable its own name as the key and its value as the value (`{ CLAUDE_CONFIG_DIR: "/cfg" }`, not `{ "CLAUDE_CONFIG_DIR=/cfg": "" }`). This client refuses an invalid key in the call's `extra_env` itself, before it runs the CLI. From `makeTemplate`: the template's `extra_env` has such a key, reserved or not a valid name, refused by spawn's own check; no template is written or replaced, so none is saved that `spawn` would refuse (this client refuses an invalid key itself, as for `spawn`). From `resume`: the row's stored `extra_env` has a key that sets `HOME` or is not a valid name (a row spawned before spawn refused it); nothing is written or launched. Shared repair and reuse handling is in [Spawn and template input refusals](../../docs/caller-contract.md#spawn-and-template-input-refusals); a fresh conversation has no memory of the old conversation. |
| `ErrInstanceIdCollision` | Without `reuse_finished`, a row already exists for the supplied `instance_id`, in any state. With `reuse_finished: true`, the row is live (`pending` included), or it changed or was removed after this spawn examined it (a lost race); nothing was changed. |

**tmux session naming** (bad `tmux_session_name` — programmer error):

| Error | When it fires |
|---|---|
| `ErrTmuxSessionNameEmpty` | `tmux_session_name` was explicitly supplied but empty. |
| `ErrTmuxSessionNameInvalid` | The name contains `#`, `:`, `.`, `$`, `\`, an ASCII control character, or is invalid UTF-8. |
| `ErrTmuxSessionNameTooLong` | The name exceeds the app-layer byte cap. |

**spawn state / lookup** (verb preconditions — mostly normal operational signals):

| Error | When it fires |
|---|---|
| `ErrSpawnNotFound` | No spawn row matches the supplied `instance_id`. |
| `ErrSpawnNotInteractive` | New input requires a live interactive state (`waiting`/`working`/`ask_user`/`check_permission`); `pending`, `ended`, and `missing` are rejected. With `allow_pending: true`, `sendKeys` allows a `pending` row but delivers keys only to a session started by the row's current launch: a `pending` row with no launch start or launch token recorded, or where only a session an earlier launch left behind is found, is still refused and nothing is sent. `ended` and `missing` rows are refused even when observing an existing delivery request; see [Ordinary text submission](../../docs/caller-contract.md#ordinary-text-submission). |
| `ErrSubmissionUnconfirmed` | `SubmissionUnconfirmedDetails` describes fallback pending-Codex uncertainty; native outcome fields are available through the generic `errDetails` record. See [Ordinary text submission](../../docs/caller-contract.md#ordinary-text-submission) for automatic delivery and [Manual input ownership](../../docs/caller-contract.md#manual-input-ownership) for manual `sendKeys`. |
| `ErrSpawnNotPausable` | The target spawn is not in a pausable (`waiting`) state. |
| `ErrPauseTimeout` | Exit keys were sent but `ended` was not observed; `errDetails` has the `PauseTimeoutDetails` shape. See [When pause cannot confirm exit](../../docs/caller-contract.md#when-pause-cannot-confirm-exit). |
| `ErrSpawnNotResumable` | `resume` applies only to a finished (`ended`/`missing`) spawn. A live spawn is refused because its agent is running. A `pending` spawn is refused too: it is a launch (spawn, reuse or resume) in progress whose agent has not reported in, a resumed row included. Abandoned or failed launch handling is owned by [Caller contract](../../docs/caller-contract.md) and [Maintenance](../../docs/maintenance.md). Also returned when the row changed after `resume` examined it; nothing is written. |
| `ErrNoSessionId` | The spawn has no `session_id`, so there is nothing to resume. For any agent, spawn the same `instance_id` again with `reuse_finished: true` and the row's `agent` (`get` shows it), starting a fresh conversation with no memory of the old conversation; see [Transcript recovery](../../docs/caller-contract.md#transcript-recovery). For an artifact-preserving fresh generated-ID alternative, follow [Cursor operation boundaries](../../docs/caller-contract.md#cursor-operation-boundaries). |
| `ErrJsonlMissing` | The current session's transcript is unavailable. Cursor requires its protected gateway transcript; native session availability is decided later during load. Older sessions are never tried. For any agent, spawn the same `instance_id` again with `reuse_finished: true` and the row's `agent` (`get` shows it), starting a fresh conversation with no memory of the old conversation; see [Transcript recovery](../../docs/caller-contract.md#transcript-recovery). For an artifact-preserving fresh generated-ID alternative, follow [Cursor operation boundaries](../../docs/caller-contract.md#cursor-operation-boundaries). |
| `ErrListInvalidLabel` | A `list` label filter could not be parsed as `key=value`. |
| `ErrProbeUnsupported` | No verb returns it: `find-missing` no longer reads process environments, the only path that returned it. The name stays catalogued (and listed for `find-missing`) so the client still maps it. |

**tmux transport** (infrastructure failures at the tmux layer — runtime). Each name has a class: GONE (the row's session is not there), UNAVAILABLE (transient; retry later), CONFLICT (permanent until a human looks), ENVIRONMENT (an environment problem for an operator to fix) or LAUNCH FAILURE (the launch failed).

| Error | Class | When it fires |
|---|---|---|
| `ErrTmuxNotAvailable` | ENVIRONMENT | The `tmux` binary is not on PATH or refuses to execute, the tmux socket is not accessible to this user, or its per-user socket directory cannot be used (`spawn` then writes nothing). `kill`, `resume`, `readPane`, `sendKeys` and `pause` return it too: tmux could not be run, the socket is not accessible to this user, or this is not the tmux server the agent was launched on. |
| `ErrTmuxSessionCreate` | LAUNCH FAILURE | The session could not be created or labelled. A plain launch normally stays `pending`; a duplicate-name re-lookup finding no holder ends its new row. Resume and reuse attempt a guarded restore after a non-timeout failure; the description reports whether it applied. |
| `ErrTmuxUnresponsive` | UNAVAILABLE | tmux did not answer usably, or a launch still appears to be starting or stopping. A timed-out launch stays `pending`; after a timed-out send, keys may have been delivered. See the [caller contract](../../docs/caller-contract.md#caller-contract) before retrying. |
| `ErrTmuxSessionConflict` | CONFLICT | A held name, a session left over from an earlier life (an earlier launch), conflicting labels, or the agent pane was not found. The description names the blocker and whether a new row was ended or a prior row restored. Nothing is sent to a conflicting session. Human repair is owned by [Operator actions](../../docs/operator-actions.md). |
| `ErrTmuxKillFailed` | UNAVAILABLE | `kill` only: the agent process still runs after `kill`. A kill was sent and the agent process, or another process in a pane of the agent's session, still ran after the kill exit wait (`kill_exit_wait_ms`); or the process cannot be checked and its labelled session is still there; or no session or pane of this launch was found while the agent process runs, so no kill was sent. Retry and refusal handling is owned by [Caller contract](../../docs/caller-contract.md). |
| `ErrTmuxSendKeys` | GONE | `sendKeys` and `pause`: the row's session or pane is not there. |
| `ErrTmuxCaptureFailed` | GONE | `readPane`: the row's session or pane is not there. |

Only a GONE error means the row's session is not there (for `kill`, GONE is success); no other tmux error ever means the agent is dead.

**relay / permissions** (relay-mode and permission-decision preconditions — normal operational signals):

| Error | When it fires |
|---|---|
| `ErrSendKeysWhileRelayed` | A `sendKeys` or `pause` while the request relay may still answer. Nothing was sent. The message distinguishes an undecided request from a recorded verdict still being delivered; see the [caller contract](../../docs/caller-contract.md#permission-relay). |
| `ErrRelayFallenBack` | The request's relay settled without acknowledgement and no pane answer is recorded through agent-director. Cursor uses finite `confirm_by` with null per-request hook process evidence; see [Cursor permissions](../../docs/permissions.md#cursor-spawns). Thrown by `decide` on that request, within seconds of the hook's end (after the request's `confirm_by` when agent-director cannot check the hook's process); no verdict was recorded, the caller's is kept as the request's `attempted_decision`. Thrown by a `sendKeys` without `request_token`, and by `pause`, while any request of the spawn is so; nothing was sent. `errDetails` is a `RelayFallenBackDetails`: the request's facts, the spawn's `state` and its other `open_requests`. Pane-answer handling is owned by [Permission relay](../../docs/caller-contract.md#permission-relay); the relevant API methods are `sendKeys` with `request_token` and `recordPaneAnswer`. For a request recorded by an older binary, `decide` throws it once the request's record is still open 2 s after its relay window ended (a call from 1 s before the window ends first waits for that instant, at most 3 s), and only while the spawn is still in `check_permission` with no other open request and none recorded after this one, otherwise `ErrNoOpenPermissionRequest`. |
| `ErrDialogMaybeOpen` | A `sendKeys` without `request_token` and without `expect_pane_sha256`, or a `pause`, on a spawn with `relay_mode=on`, while a permission request of the spawn is not proven gone by valid agent evidence (see [The dialog hold](../../docs/permissions.md#the-dialog-hold)), whatever agent-director's records say of it (`"delivered"`, a pane answer, `recordPaneAnswer`); nothing was sent. `errDetails` is a `DialogMaybeOpenDetails`: the oldest such request with its facts and `unproven_since`, the spawn's `state` and its other `unproven_requests`. Retry, escalation and pane-hash handling are owned by [Permission relay](../../docs/caller-contract.md#permission-relay). `pause` takes no hash; stopping a held Spawn is covered by [Caller contract](../../docs/caller-contract.md). |
| `ErrPaneChanged` | `sendKeys` or `recordPaneAnswer` with `expect_pane_sha256`: the agent's pane no longer has that hash; nothing was sent or recorded. `errDetails` (`PaneChangedDetails`) never carries the new hash; shared pane handling is in [Permission relay](../../docs/caller-contract.md#permission-relay). |
| `ErrPaneAnswerInProgress` | A pane answer or `recordPaneAnswer` while another sender still owns the request. Nothing was sent or recorded; `errDetails` is `PaneAnswerInProgressDetails`. |
| `ErrClaimTooSoon` | `recordPaneAnswer` before the request's relay hook has been gone 2 s, or while it may still answer the request; nothing was recorded. `errDetails` is `ClaimTooSoonDetails`, with `not_before` (`null` only while the hook is seen running); retry handling is in [Permission relay](../../docs/caller-contract.md#permission-relay). |
| `ErrRelayModeOff` | `decide` was called on a spawn whose `relay_mode` is not `on`. |
| `ErrInvalidDecision` | `decision` was neither `allow` nor `deny`. |
| `ErrMissingRequestToken` | `decide` was called with an empty `request_token`. |
| `ErrNoOpenPermissionRequest` | No open permission-request row matches the `(instance_id, request_token)` pair. `decide` also throws it, recording nothing, for an open request of a spawn that is `ended` or `missing`: such a request is closed; see [Permission relay](../../docs/caller-contract.md#permission-relay) for handling. It throws it too for a request closed with its spawn (when its agent ended it, or `find-missing` or `pause` moved a Codex row to `ended`, `decision_reason` `"ended"` for an undecided one, or when `find-missing` marked it `missing`) before the request relay acknowledged its recorded verdict, `find-missing`'s own deny excepted, whether or not the spawn was resumed since: the request stays closed, `get` and `list` no longer show it, and `getPermission` reads its `delivery` as for any request (for a request with a settle instant, `"delivered"` requires durable relay acknowledgement; requests recorded by an older binary retain the [legacy delivery rules](../../docs/permissions.md#requests-recorded-by-an-older-binary), including derived delivery after expiry even when closed). Pane handling is owned by [Permission relay](../../docs/caller-contract.md#permission-relay). A pane answer (`sendKeys` with `request_token`) and `recordPaneAnswer` throw it for such a closed request too, nothing sent or recorded. For a request recorded by an older binary, `decide` also throws it when the request's record is still open 2 s after its relay window ended but the spawn has left `check_permission`, has recorded a later request, or has another request open: the request's dialog may have closed, and a pane answer to a closed dialog is typed into the agent's prompt; see [Permission relay](../../docs/caller-contract.md#permission-relay) for handling. `send-keys` does not apply this check. |
| `ErrAlreadyDecided` | A permission-request row exists but has already been decided; first decide wins. This includes a request the relay denied when its time ran out (`getPermission` returns `decision_reason` `"timeout"`); the recorded decision and acknowledgement prevent a second API answer. Acknowledgement does not establish agent receipt or dialog closure; see [Permission relay](../../docs/caller-contract.md#permission-relay). It also includes a request `find-missing` denied when it marked the spawn `missing` (`decision_reason` `"find_missing"`), and one closed at the pane (`pane_answer` `"sent"`, `"outside"` or `"tool_ran"`, with a `decision` or, for an `"unknown"` claim, none): `decide` on it records nothing, not even `attempted_decision`. A pane answer or `recordPaneAnswer` on a request its relay acknowledged, or one already closed at the pane, throws it too. |
| `ErrStoreBusy` | `decide` was called with `max_wait_ms`, and its precommit budget ran out, including acquisition or SQL work. It does not establish another process held the store. Once the verdict is confirmed committed, `decide` never throws it: a bound reached while it waits for confirmation resolves with `delivery` `"not_confirmed"`. An unconfirmed commit is an internal error instead. Without `max_wait_ms`, `decide` never throws `ErrStoreBusy`. A pane answer (`sendKeys` with `request_token`) or `recordPaneAnswer` throws it when the store's write lock was not taken within its busy timeout. Recovery and zero-wait semantics are in the [caller contract](../../docs/caller-contract.md#permission-relay). |
| `ErrPermissionRequestNotFound` | No permission-request row exists for the supplied `request_token`. |
| `ErrAmbiguousRequest` | `request_token` was empty and more than one open request exists for the spawn. |

**config templates** (template-management verbs — mixed programmer error / operational signal):

| Error | When it fires |
|---|---|
| `ErrTemplateNameUnsafe` | A template name fails the safety check (path traversal, absolute path, hidden name, or trivial garbage). |
| `ErrTemplateNotFound` | The named template `.toml` does not exist on disk. |
| `ErrTemplateMalformed` | A template file exists but fails schema validation (unknown keys, an `agent` other than `claude`, `codex` or `cursor`, the old name of the `agent_args` key, wrong types, bad enums, or one key set under names that differ only in letter case, such as `RELAY_MODE` and `relay_mode`). `makeTemplate` also throws it, before writing anything, for an `agent` that is not exactly `claude`, `codex` or `cursor`. |
| `ErrTemplateExists` | `make-template` target already exists and `overwrite` is not `true`. |

**misc**:

| Error | When it fires |
|---|---|
| `ErrInvalidFlags` | CLI flag parsing rejected the invocation; a call carried an old name of a renamed key (thrown before any process starts, naming the new key); `spawn` was given an explicit `instance_id` containing an ASCII control character (0x00–0x1f or 0x7f); `readPane`'s `n_lines` or `list`'s `limit` is negative (0 or omitted gives the default: 25 lines, no cap); `decide` was given a negative `max_wait_ms`; `sendKeys` was given params that do not fit together (a pane answer without `as`, `key` or `expect_pane_sha256` or with `text`; `key` with a non-empty `text`; `no_enter` with neither; `as` without `request_token`; a `key` that is neither a named key nor one character; a malformed hash); or `recordPaneAnswer` was given an `as` other than `"allow"`, `"deny"` or `"unknown"`, or a missing or malformed hash. Nothing was changed, sent or recorded. |

**CLI setup** (the CLI could not load its config or open its store, so no verb ran — operational):

| Error | When it fires |
|---|---|
| `ErrConfigMalformed` | The CLI refused its config file, `~/.config/agent-director/config.toml`: it cannot be read, does not parse as TOML, or sets a value agent-director refuses (for example a negative `[pause] timeout_seconds`). The description names the file and why: the parse error, or every refused key with its value and the values it allows. Every call that opens the store fails this way until an operator fixes the file. See the caller contract for handling. |
| `ErrStoreOpen` | The CLI could not open its store; the description says what failed. This includes `HOME` unset or empty with no `home` option (`api: expand config path: …`): every call that opens the store is then refused, whatever `storePath` is. With `createIfMissing: false` it includes a store that does not exist (`database not initialized`); nothing was created. It says nothing about any agent. |
| `ErrSchemaMismatch` | The CLI refused to open its store: the store was written by a newer agent-director than the binary (`found user_version=<N>, want <M>`), or it has no valid store id. The description says which. Nothing was written, and every call that opens the store fails this way until an operator acts: a newer store needs the agent-director release that wrote it. See Operator actions for recovery. It says nothing about any agent. |
| `ErrSchemaMigrationRequired` | The CLI refused to open its store: the store is older than the binary and must be migrated first. The description gives both schema versions. Nothing was written, and every call that opens the store fails this way until an administrator migrates the store; no call through this client can. See the caller contract for handling. It says nothing about any agent. |
| `ErrOldLayout` | The CLI refused to open its store: it was about to open the default store (no `storePath`, and no `[store] db_path` in its config file), no store exists in `~/.config/agent-director`, and the old directory exists. Nothing was created, and every call that opens the store fails this way until an operator runs this release's `install.sh`. See the caller contract for handling. It says nothing about any agent. |

**CLI itself** (the CLI failed, or does not know the verb, rather than refusing the call's input or state):

| Error | When it fires |
|---|---|
| `ErrInternal` | A CLI failure with no more specific name. Its description reports what failed and whether anything changed. A pane answer whose key was sent but not recorded carries `PaneKeySentDetails` with `key_sent: true`; see the [caller contract](../../docs/caller-contract.md#permission-relay). |
| `ErrJSONMarshal` | The verb ran and succeeded, but the CLI could not write its JSON result, so the result is lost and whatever the call changes was changed (a `spawn` launched its agent). A bug in agent-director. Check with `get`, `status` or `list` before you repeat a call that changes something. |
| `ErrUnknownVerb` | The binary does not know the called verb. No verb ran; binary and client versions differ. See [Installation](../../docs/install.md). |
| `ErrTrailWrite` | Only the CLI's internal `trail-emit` command gives it, when it cannot write its trail event. No method of this client runs `trail-emit`, so a call through this client does not get it. |

## See also

[Caller contract](../../docs/caller-contract.md), [Permissions](../../docs/permissions.md)
and [Installation](../../docs/install.md) own shared procedures.
