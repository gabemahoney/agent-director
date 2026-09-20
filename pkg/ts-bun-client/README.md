# agent-director

TypeScript/Bun client for the agent-director CLI. Shares the Go API
surface 1:1. The `Client` discovers the system-installed
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

**You must separately install the CLI binary on the host.** The
fastest path on a fresh machine is the one-liner published with the
agent-director GitHub repo — it downloads the binary for your OS/arch
from the [latest release](https://github.com/gabemahoney/agent-director/releases/latest)
and sets up `~/.agent-director/`:

```sh
curl -fsSL https://raw.githubusercontent.com/gabemahoney/agent-director/main/skills/install-agent-director/install.sh | bash -s -- --from-release
```

`Client.create()` discovers the installed binary at construction time
(either at `~/.agent-director/bin/agent-director` or anywhere on
`$PATH`) and rejects if it cannot find one. Other supported install
mechanisms — `install.sh --binary <path>` with a locally-staged
artifact, or any drop-in copy onto `$PATH` — are documented in the
[repo README](https://github.com/gabemahoney/agent-director#install-the-cli).

## Supported platforms

- `linux/x64` (Linux on x86_64)
- `darwin/arm64` (Apple Silicon Mac)

The library's published npm package admits installs on any host (no
`os`/`cpu` restrictions on the library itself); the platform gate is
the CLI binary's own platform coverage. The CLI must be installed
separately per the platform list above.

## Quick start

`using` block (preferred):

```ts
using client = await Client.create({});
const v = await client.version({});
console.log(v.version);
```

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

All constructor options are optional. Omitted fields fall back to the CLI binary's own three-tier default resolution (config.toml value, then hardcoded fallback such as `~/.agent-director/state.db`) — the CLI is the single source of truth for defaults. Tilde expansion (`~` → home directory) is handled automatically before paths are forwarded to the CLI subprocess. The `using` form calls `client.close()` automatically at block exit and requires Bun >=1.0.21 (or a TypeScript project with `"lib": ["ESNext.Disposable"]`).

`ClientOptions` overrides forward verbatim to the CLI subprocess as global flags:

- `storePath` → `--store-path`
- `home` → `--home`
- `tmuxCommand` → `--tmux-command`

Set them only when the consumer needs to override the CLI's default for that field.

## Verb examples

### spawn

Launch a tracked Claude Code instance in a new tmux session.

```sh
agent-director spawn --cwd ~/my-project
```

```ts
const result = await client.spawn({ cwd: "~/my-project" });
console.log(result.claude_instance_id);
```

### status

Get the current lifecycle state of a Spawn.

```sh
agent-director status --claude-instance-id <id>
```

```ts
const result = await client.status({ claude_instance_id: "<id>" });
console.log(result.state);
```

### list

Query Spawns with optional filters.

```sh
agent-director list --state waiting
```

```ts
const result = await client.list({ state: ["waiting"] });
for (const spawn of result.spawns) {
  console.log(spawn.claude_instance_id, spawn.state);
}
```

### sendKeys

Send text to a Spawn's tmux pane.

```sh
agent-director send-keys --claude-instance-id <id> --text "what is 2+2?"
```

```ts
await client.sendKeys({ claude_instance_id: "<id>", text: "what is 2+2?" });
```

Pass `allow_pending: true` to also permit sending to a `pending` Spawn (state
before `SessionStart` fires). The primary use case is dismissing interactive
prompts that Claude Code renders before the session becomes interactive — for
example the `--dangerously-load-development-channels` safety warning. `ended`
and `missing` Spawns are still rejected regardless of the flag.

Send an empty string with `allow_pending: true` to press Enter and dismiss the
pre-`SessionStart` prompt:

```ts
await client.sendKeys({
  claude_instance_id: "<id>",
  text: "",
  allow_pending: true,
});
```

### readPane

Read the last N lines of a Spawn's tmux pane (default 25).

```sh
agent-director read-pane --claude-instance-id <id> --n-lines 50
```

```ts
const result = await client.readPane({ claude_instance_id: "<id>", n_lines: 50 });
console.log(result.pane);
```

`readPane` has no state guard — it works on `pending`, `ended`, and `missing`
Spawns as well as live ones. The `allow_pending` flag is accepted for symmetry
with `sendKeys` but has no behavioral effect.

---

### kill

Terminate a Spawn's tmux session.

```sh
agent-director kill --claude-instance-id <id>
```

```ts
await client.kill({ claude_instance_id: "<id>" });
```

### makeTemplate

Save a reusable spawn preset. Pass `overwrite: true` to atomically replace an existing template; omit the field to keep the default rejection on collision.

```sh
agent-director make-template --name dev --cwd /repos/widget --overwrite
```

```ts
await client.makeTemplate({ name: "dev", cwd: "/repos/widget", overwrite: true });
```

## Consumption

The supported consumption mode is Bun-runtime ESM:

```sh
bun add agent-director
```

```ts
import { Client } from "agent-director";
```

The package uses `import.meta.resolve` and `import.meta.url` at runtime to locate the installed `package.json`. Bundling it through webpack or other bundlers that do not support these features is not supported.

## Versioning

The library version equals the agent-director release tag — released in lockstep:

| npm package | CLI binary |
|---|---|
| `agent-director@v0.5.0` | `agent-director CLI v0.5.0` |

## Minimum required CLI binary version

The library declares the minimum CLI-binary version it requires on two
surfaces, both backed by the same single source of truth shipped in the
published npm package at `dist/version-floor.json`.

**TS export (preferred for JS/TS consumers):**

```ts
import { MIN_BINARY_VERSION, DEV_SENTINEL_VERSION } from "agent-director";

console.log(`requires agent-director >= ${MIN_BINARY_VERSION}`);

if (binaryVersion === DEV_SENTINEL_VERSION) {
  // dev-built binary stamps the sentinel; accept it as satisfying the floor.
}
```

`MIN_BINARY_VERSION` is a strict-SemVer-2.0 string (e.g. `0.7.0` or
`0.7.0-rc1`). The value is inlined into the bundle at build time; no
runtime file read. `DEV_SENTINEL_VERSION` is the literal `"0.0.0-dev"`
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
the field from the file at the stable documented path. The `-r` flag
returns a bare string suitable for shell comparison.

## Supported Bun versions

Minimum: `>=1.0.21` (set in `engines.bun`). Tested on Bun 1.3.x as of this release. The `using` block syntax (Explicit Resource Management) requires Bun 1.0.21+.

## Errors

Every error thrown by this package extends `AgentDirectorError`, so you can catch by subclass with `instanceof`:

```ts
import { Client, ErrSpawnNotFound } from "agent-director";
try {
  await client.status({ claude_instance_id: "bogus" });
} catch (e) {
  if (e instanceof ErrSpawnNotFound) {
    // recover
  } else {
    throw e;
  }
}
```

The public typed-error surface falls into four groups. Every class named below is exported from the package entry point. The tables are self-sufficient for choosing what to catch; the raw generated catalog for the group-4 classes lives in [`../../pkg/api/errnames/catalog.json`](../../pkg/api/errnames/catalog.json).

### Realistic catch-site shortlist

Most services only need to route on the "agent-director is sick" set. Alert on these six and let everything else propagate:

- `ErrSystemInstallNotFound`
- `ErrSystemInstallTooOld`
- `ErrSystemInstallUnreachable`
- `ErrCallerCwdUnreachable`
- `ErrSystemInstallDisappeared`
- `ErrCallTimeout`

Everything else is either **programmer error** (bad arguments — fix the call site, do not retry) or a **normal operational signal** (an expected verb outcome you branch on, like "no such spawn" or "already decided"). `ErrConsumerSignal` sits in between: it is a runtime infrastructure failure, but a routine one during shutdown, so treat it as an operational signal rather than a page.

### 1. Library lifecycle

Raised by the client wrapper itself, independent of any agent-director binary.

| Error | When it fires |
|---|---|
| `ErrClientClosed` | A verb was called on a `Client` after `close()`/disposal. Obtain a fresh handle with `Client.create()`. Programmer error. |
| `ErrBunVersionTooOld` | The running Bun runtime is below the package's minimum. Upgrade Bun. Fires at construction. |

### 2. System install (agent-director binary)

The "AD is sick" group. **Construction-time** errors are thrown by `Client.create()` / `resolveSystemBinary()` before any verb runs; **runtime** errors are thrown at verb-dispatch time on a client that constructed successfully.

| Error | Phase | When it fires |
|---|---|---|
| `ErrSystemInstallNotFound` | Construction | No agent-director binary was found in any checked location. Carries `checkedLocations`. |
| `ErrSystemInstallTooOld` | Construction | A binary was found but its version is below the required floor. Carries `actualVersion`, `requiredVersion`, `binaryPath`. |
| `ErrSystemInstallUnreachable` | Construction | A binary was found but failed the version probe (not executable, wrong arch, non-zero exit, killed). Carries `binaryPath`, `reason`, `diagnostic`, `exitCode`, `signal`. |
| `ErrCallerCwdUnreachable` | Construction **and** runtime | `process.cwd()` does not resolve to a real directory. Thrown at construction if the cwd is already gone, or at the first verb call after the cwd disappears mid-flight. Carries `cwd`, `cause`. Restart your service from a valid directory. |
| `ErrSystemInstallDisappeared` | Runtime | The binary path resolved at construction no longer exists at verb-dispatch (uninstalled or replaced mid-flight). Carries `verb`, `binaryPath`, `cause`. Re-install the binary, then create a new `Client`. |

### 3. Per-call infrastructure

Thrown per verb call by the subprocess transport, not by the CLI's own validation.

| Error | When it fires | Category |
|---|---|---|
| `ErrCallTimeout` | The subprocess did not complete within the configured per-call timeout. Carries `verb`, `elapsedMs`, `timeoutMs`. | Operational — include in your "AD is sick" alert set. |
| `ErrConsumerSignal` | The subprocess was killed by an OS signal (e.g. `SIGTERM`, `SIGINT`) before producing a result. Carries `verb`, `signal`. | Operational — routine during shutdown. |
| `ErrUnknownErrorName` | The CLI returned an error envelope whose `err_name` this client version does not recognize (client older than the binary). Carries `unknownName`, `envelope`. | Programmer/version error — upgrade the client. |

### 4. Catalog-derived (CLI-side validation)

These 37 classes are generated one-to-one from the shared `err_name` catalog ([`../../pkg/api/errnames/catalog.json`](../../pkg/api/errnames/catalog.json), the canonical source). They surface bad input or a verb's own state preconditions — almost all are either **programmer error** or a **normal operational signal**, so few catch sites need to name them individually. They are grouped by domain below.

**cwd validation** (bad `cwd` argument to `spawn` — programmer error):

| Error | When it fires |
|---|---|
| `ErrCwdMissing` | No `cwd` was supplied; spawn requires it to derive the JSONL/resume path. |
| `ErrCwdNotAPath` | `cwd` is neither absolute nor a `~/` form (URLs, bare relative paths, non-path values). |
| `ErrCwdNotFound` | `cwd` resolved to a path that does not exist on disk. |
| `ErrCwdNotADirectory` | `cwd` resolved to a file (or other non-directory inode). |

**spawn config validation** (bad `spawn` arguments — programmer error):

| Error | When it fires |
|---|---|
| `ErrRelayModeInvalid` | `relay_mode` was something other than `on` / `off` / empty. |
| `ErrSpawnDeniedFlag` | `claude_args` contains a flag the supervisor must own (`--settings`, `--resume`, `--continue`, `--print`, `--output-format`). |
| `ErrReservedEnvKey` | `extra_env` contains an `AGENT_DIRECTOR_*` key (reserved prefix). |
| `ErrInstanceIdCollision` | The supplied `claude_instance_id` is already in use by a live spawn. |

**tmux session naming** (bad `--tmux-session-name` — programmer error):

| Error | When it fires |
|---|---|
| `ErrTmuxSessionNameEmpty` | `--tmux-session-name` was explicitly supplied but empty. |
| `ErrTmuxSessionNameInvalid` | The name contains `#`, `:`, `.`, an ASCII control character, or is invalid UTF-8. |
| `ErrTmuxSessionNameTooLong` | The name exceeds the app-layer byte cap. |

**spawn state / lookup** (verb preconditions — mostly normal operational signals):

| Error | When it fires |
|---|---|
| `ErrSpawnNotFound` | No spawn row matches the supplied `claude_instance_id`. |
| `ErrSpawnNotInteractive` | The target spawn is not in a live interactive state (`waiting`/`working`/`ask_user`/`check_permission`); `pending`, `ended`, and `missing` are rejected (`AllowPending=true` relaxes the `pending` rejection). |
| `ErrSpawnNotPausable` | The target spawn is not in a pausable (`waiting`) state. |
| `ErrPauseTimeout` | The spawn did not reach `ended` within `pause.timeout_seconds` after `/exit`. Retry or `kill`. |
| `ErrSpawnNotResumable` | The target spawn is not terminal (`ended`/`missing`), so it cannot be resumed. |
| `ErrNoSessionId` | The spawn has no `claude_session_id` (killed before its first SessionStart), so there is nothing to resume — `delete` and spawn fresh. |
| `ErrJsonlMissing` | The resume JSONL could not be located at any candidate path — `delete` and spawn fresh. |
| `ErrListInvalidLabel` | A `list` label filter could not be parsed as `key=value`. |
| `ErrProbeUnsupported` | The liveness probe has no implementation for the current platform (`find-missing`). |

**tmux transport** (infrastructure failures at the tmux layer — runtime):

| Error | When it fires |
|---|---|
| `ErrTmuxNotAvailable` | The `tmux` binary is not on PATH or refuses to execute. |
| `ErrTmuxSessionCreate` | `tmux new-session` exited non-zero (name collision, invalid cwd, missing default-shell). |
| `ErrTmuxSendKeys` | `tmux send-keys` exited non-zero (typically no live pane). |
| `ErrTmuxCaptureFailed` | `tmux capture-pane` exited non-zero (session/pane vanished mid-call). |

**relay / permissions** (relay-mode and permission-decision preconditions — normal operational signals):

| Error | When it fires |
|---|---|
| `ErrSendKeysWhileRelayed` | `send-keys` was attempted against a spawn sitting on a `check_permission` row with `relay_mode=on`. |
| `ErrRelayModeOff` | `decide` was called on a spawn whose `relay_mode` is not `on`. |
| `ErrInvalidDecision` | `--decision` was neither `allow` nor `deny`. |
| `ErrMissingRequestToken` | `decide` was called with an empty `request_token`. |
| `ErrNoOpenPermissionRequest` | No open permission-request row matches the `(instance_id, request_token)` pair (or it was already decided). |
| `ErrAlreadyDecided` | A permission-request row exists but has already been decided; first decide wins. |
| `ErrPermissionRequestNotFound` | No permission-request row exists for the supplied `request_token`. |
| `ErrAmbiguousRequest` | `request_token` was empty and more than one open request exists for the spawn. |

**config templates** (template-management verbs — mixed programmer error / operational signal):

| Error | When it fires |
|---|---|
| `ErrTemplateNameUnsafe` | A template name fails the safety check (path traversal, absolute path, hidden name, or trivial garbage). |
| `ErrTemplateNotFound` | The named template `.toml` does not exist on disk. |
| `ErrTemplateMalformed` | A template file exists but fails schema validation (unknown keys, wrong types, bad enums). |
| `ErrTemplateExists` | `make-template` target already exists; the verb never overwrites. |

**misc**:

| Error | When it fires |
|---|---|
| `ErrInvalidFlags` | CLI flag parsing rejected the invocation; not tied to any single verb handler. |

## Architecture

See [`../../docs/architecture.md`](../../docs/architecture.md) for the internal design. Dedicated subsections cover: Client lifecycle, the subprocess call recipe, Per-platform packaging, Error mapping, TS smoke-test harness, and TS envelope-diff regression.
