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
mechanisms — `install.sh --binary <path> --admin-binary <path>` with
locally-staged artifacts, or any drop-in copy onto `$PATH` — are documented in the
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

Send text to the agent's own pane, then one Enter to submit it. An empty
`text` sends the Enter only, submitting what is already typed.

```sh
agent-director send-keys --claude-instance-id <id> --text "what is 2+2?"
```

```ts
await client.sendKeys({ claude_instance_id: "<id>", text: "what is 2+2?" });
```

Pass `allow_pending: true` to also allow a `pending` row: a launch (spawn,
reuse or resume) whose agent has not reported in yet, for example to dismiss a
prompt the agent shows before it reports in. Keys are delivered only to a
session started by the row's current launch. `ended` and `missing` rows are
still rejected. A `pending` row whose launch start or launch token is not recorded, or where
only a session an earlier launch left behind is found, gets
`ErrSpawnNotInteractive` and nothing is sent.

Send an empty string with `allow_pending: true` to press Enter and dismiss such
a prompt:

```ts
await client.sendKeys({
  claude_instance_id: "<id>",
  text: "",
  allow_pending: true,
});
```

### readPane

Read the last N lines of the agent's own pane (default 25).

```sh
agent-director read-pane --claude-instance-id <id> --n-lines 50
```

```ts
const result = await client.readPane({ claude_instance_id: "<id>", n_lines: 50 });
console.log(result.pane);
```

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

```sh
agent-director kill --claude-instance-id <id>
```

```ts
const { kill_sent } = await client.kill({ claude_instance_id: "<id>" });
console.log(kill_sent);
```

Errors to catch: `ErrTmuxKillFailed` and `ErrTmuxUnresponsive` (UNAVAILABLE:
retry later), `ErrTmuxSessionConflict` (CONFLICT: a human must look),
`ErrTmuxNotAvailable` (ENVIRONMENT: an operator must fix the environment) and
`ErrSpawnNotFound`. None of these means the agent is dead. The caller must run
as the same user and in the same tmux environment as the agents. See the "tmux
transport" table under [Errors](#errors).

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

These 42 classes are generated one-to-one from the shared `err_name` catalog ([`../../pkg/api/errnames/catalog.json`](../../pkg/api/errnames/catalog.json), the canonical source). They surface bad input or a verb's own state preconditions — almost all are either **programmer error** or a **normal operational signal**, so few catch sites need to name them individually. They are grouped by domain below.

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
| `ErrInstanceIdCollision` | Without `reuse_finished`, a row already exists for the supplied `claude_instance_id`, in any state. With `reuse_finished: true`, the row is live (`pending` included), or it changed or was removed after this spawn examined it (a lost race); nothing was changed. |

**tmux session naming** (bad `tmux_session_name` — programmer error):

| Error | When it fires |
|---|---|
| `ErrTmuxSessionNameEmpty` | `tmux_session_name` was explicitly supplied but empty. |
| `ErrTmuxSessionNameInvalid` | The name contains `#`, `:`, `.`, `$`, `\`, an ASCII control character, or is invalid UTF-8. |
| `ErrTmuxSessionNameTooLong` | The name exceeds the app-layer byte cap. |

**spawn state / lookup** (verb preconditions — mostly normal operational signals):

| Error | When it fires |
|---|---|
| `ErrSpawnNotFound` | No spawn row matches the supplied `claude_instance_id`. |
| `ErrSpawnNotInteractive` | The target spawn is not in a live interactive state (`waiting`/`working`/`ask_user`/`check_permission`); `pending`, `ended`, and `missing` are rejected. With `allow_pending: true`, `sendKeys` allows a `pending` row but delivers keys only to a session started by the row's current launch: a `pending` row with no launch start or launch token recorded, or where only a session an earlier launch left behind is found, is still refused and nothing is sent. |
| `ErrSpawnNotPausable` | The target spawn is not in a pausable (`waiting`) state. |
| `ErrPauseTimeout` | The spawn did not reach `ended` within `pause.timeout_seconds` after `/exit`. Retry the pause; the agent may still be running. |
| `ErrSpawnNotResumable` | `resume` applies only to a finished (`ended`/`missing`) spawn. A live spawn is refused because its agent is running. A `pending` spawn is refused too: it is a launch (spawn, reuse or resume) in progress whose agent has not reported in, a resumed row included. Also returned when the row changed after `resume` examined it; nothing is written. |
| `ErrNoSessionId` | The spawn has no `claude_session_id` (killed before its first SessionStart), so there is nothing to resume — spawn the same `claude_instance_id` again with `reuse_finished: true`, which starts with no memory of the old conversation. |
| `ErrJsonlMissing` | The resume JSONL could not be located at any candidate path — spawn the same `claude_instance_id` again with `reuse_finished: true`, which starts with no memory of the old conversation. |
| `ErrListInvalidLabel` | A `list` label filter could not be parsed as `key=value`. |
| `ErrProbeUnsupported` | No verb returns it: `find-missing` no longer reads process environments, the only path that returned it. The name stays catalogued (and listed for `find-missing`) so the client still maps it. |

**tmux transport** (infrastructure failures at the tmux layer — runtime). Each name has a class: GONE (the row's session is not there), UNAVAILABLE (transient; retry later), CONFLICT (permanent until a human looks), ENVIRONMENT (an environment problem for an operator to fix) or LAUNCH FAILURE (the launch failed).

| Error | Class | When it fires |
|---|---|---|
| `ErrTmuxNotAvailable` | ENVIRONMENT | The `tmux` binary is not on PATH or refuses to execute, the tmux socket is not accessible to this user, or its per-user socket directory cannot be used (`spawn` then writes nothing). `kill`, `resume`, `readPane`, `sendKeys` and `pause` return it too: tmux could not be run, the socket is not accessible to this user, or this is not the tmux server the agent was launched on. |
| `ErrTmuxSessionCreate` | LAUNCH FAILURE | The session could not be created or labelled; the new row stays `pending`. After tmux answers "duplicate session", `spawn` returns it only when the session holding the requested name was gone by the re-lookup; the new row is then ended (the error says if it was not), and the error names the instance id (a minted one included) and the retry: spawn that `claude_instance_id` again with `reuse_finished: true` once the name is free (if the row was not ended, once `get` shows it `ended` or `missing`), since a plain `spawn` of the id collides with its row. From `resume`, the session could not be created or labelled, or after "duplicate session" no session held the name when it was looked up again; the row is restored to `ended` or `missing` (the error says if it was not). A name found held is never this error. |
| `ErrTmuxUnresponsive` | UNAVAILABLE (transient) | tmux did not answer in time, or gave a reply agent-director does not recognise. From `spawn`'s or `resume`'s session-creating call: the session may have been created and the row stays `pending`; do not retry until `get` shows the row `ended` or `missing`; then a `spawn` of an explicit `claude_instance_id` is retried with `reuse_finished: true`, since a plain `spawn` of the id collides with its finished row. From a plain `spawn` after "duplicate session": the re-lookup could not be read, or more than one session's name matches the requested name; the new row is ended (the error says if it was not) and the error names the retry with `reuse_finished: true`, as for `ErrTmuxSessionCreate`. From `resume`'s check before the launch, with nothing written: the row's own session or agent process appears to still be stopping or still starting, tmux's answer could not be read, or more than one session's name matches the row's session name; retry later. After "duplicate session" at the create, the same cases, and the row is restored (the error says if it was not). From `kill`: the lookup or the pane listing could not be read (nothing was done), or a kill was sent, the agent process could not be checked and the follow-up lookup could not be read (the kill may or may not have taken effect); retry later with backoff. From `readPane`, `sendKeys` and `pause`: tmux did not answer usably; after a timed-out send the keys (or `/exit`) may have been delivered. Where `sendKeys`' text may be typed, the error names the next step instead of "retry later", because the same call would type the text a second time: after a timed-out text call, `readPane` and, if the text is typed, `sendKeys` with empty `text`, otherwise the same `sendKeys`; after a failed or timed-out Enter, `sendKeys` with empty `text`, which submits it. A `pause` is retried later as it is: before typing `/exit` it clears the line the agent's cursor is on, so an `/exit` the failed `pause` left typed is not doubled. |
| `ErrTmuxSessionConflict` | CONFLICT (permanent until a human looks) | A tmux session conflict that needs a human. From `spawn` with an explicit `claude_instance_id` that has no row: a session of this store still labelled with that id is left over from an earlier life, or labels conflict; nothing is written. From a plain `spawn` whose requested tmux session name is already held (tmux answered "duplicate session"): the holder is a session left over from an earlier life of this id, another row's session, a session of another agent-director store, or one with no valid instance id, or labels conflict; the new row is ended (the error says if it was not) and the error names the blocking session. Another row's or another store's session is another agent and is never ended; the holding session is never read or typed into. From `resume` of a finished row, before anything is written and with no session touched: a session holds the row's session name (another row's session, a session of another agent-director store, or one with no valid instance id), a session is left over from an earlier life of this id, the row's own old session (or its agent process still running with no session) is past the stopping window and the starting-session bound, or labels conflict; the error names the blocking session. After "duplicate session" at the create, the same cases, and the row is restored (the error says if it was not). None of these means the agent is dead. From `kill` on a live row: the session found is not this launch's session (it carries the label of an earlier launch with this row's own id), or labels conflict; no kill was sent. From `readPane`, `sendKeys` and `pause`: the agent's pane was not found, a session an earlier launch left behind is there (for `readPane`, more than one; for `sendKeys`, on a live row), or labels conflict; nothing was read or sent. See "Operator actions" in the agent-director README. |
| `ErrTmuxKillFailed` | UNAVAILABLE | `kill` only: the agent process still runs after `kill`. A kill was sent and the agent process, or another process in a pane of the agent's session, still ran after the kill exit wait (`kill_exit_wait_ms`); or the process cannot be checked and its labelled session is still there; or no session or pane of this launch was found while the agent process runs, so no kill was sent. Retry `kill` later; never `delete` the row. |
| `ErrTmuxSendKeys` | GONE | `sendKeys` and `pause`: the row's session or pane is not there. |
| `ErrTmuxCaptureFailed` | GONE | `readPane`: the row's session or pane is not there. |

Only a GONE error means the row's session is not there (for `kill`, GONE is success); no other tmux error ever means the agent is dead.

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
| `ErrInvalidFlags` | CLI flag parsing rejected the invocation, or `spawn` was given an explicit `claude_instance_id` containing an ASCII control character (0x00–0x1f or 0x7f). |

## Architecture

See [`../../docs/architecture.md`](../../docs/architecture.md) for the internal design. Dedicated subsections cover: Client lifecycle, the subprocess call recipe, Per-platform packaging, Error mapping, TS smoke-test harness, and TS envelope-diff regression.
