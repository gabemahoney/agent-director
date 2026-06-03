# agent-director

TypeScript/Bun client for the agent-director CLI. Shares the Go API
surface 1:1. The `Client` spawns the bundled `agent-director` CLI
binary as a subprocess per verb call — no FFI, no network hop.

## Install

```sh
bun add agent-director
```

Requires Bun >=1.0.21. The package ships the prebuilt CLI binary for
each supported platform via optional dependencies — they install
automatically on `bun add`. The `Client` verifies the binary is present at construction time so install
errors surface immediately, then re-resolves its path on every verb call so
a binary replacement (e.g. a background `bun install`) is handled
transparently.

On install, a postinstall script copies the `install-agent-director` skill body into `~/.claude/skills/install-agent-director/` so `claude /install-agent-director` is immediately discoverable in Claude Code. The postinstall only writes under `~/.claude/skills/`; it does not touch PATH, `~/.agent-director/`, or your Claude Code settings.

Bun blocks dependency postinstalls by default. Trust this package so the skill copy can run:

```sh
bun pm trust agent-director
```

Or pre-declare it in your `package.json` before `bun add`:

```json
{
  "trustedDependencies": ["agent-director"]
}
```

### Verbose install logs

Set `AD_POSTINSTALL_VERBOSE=1` (also accepts `true` / `yes`, case-insensitive) before `bun add` to see what the postinstall resolved and decided. The default is quiet — five lines maximum.

### Skipping the postinstall

`bun add --ignore-scripts agent-director` installs the library without running the postinstall. The skill is not copied. To get skill discoverability after the fact, either:

```sh
cp -r node_modules/agent-director/skills/install-agent-director ~/.claude/skills/
```

or, once the library is on disk, invoke `claude /install-agent-director` from any Claude Code session — the install skill copies itself into `~/.claude/skills/` as a side effect of running.

### How the postinstall decides whether to overwrite

The skill body carries a `version:` field in its YAML frontmatter. On every install the postinstall reads the bundled version and the version already on disk:

- **Same version** — no filesystem changes, no output.
- **Older or missing on disk** — overwrite, leaving a timestamped `install-agent-director.bak.<unix-ts>` sibling under `~/.claude/skills/`.
- **Newer on disk** — leave it alone, single-line warning to stderr.

The authoritative behavior contract lives in Idea Bee `b.fg3`.

## Supported platforms

**Supported** — install + library + skill all work:

- `linux/x64` (Linux on x86_64)
- `darwin/arm64` (Apple Silicon Mac)

**Refused at install time by npm/bun** — the umbrella's `os`/`cpu` fields fail resolution and no postinstall runs:

- Windows (any architecture)
- FreeBSD, OpenBSD, and other non-Linux/non-Darwin OSes
- any architecture not in `[x64, arm64]` (e.g. `ia32`, `mips`, `arm`)

**Refused by the postinstall after npm/bun admits the install** — the umbrella runs but the postinstall exits non-zero with an `agent-director: unsupported host` message before any filesystem write:

- `darwin/x64` (Intel Mac)
- `linux/arm64`

The refusal is two-layered because npm/bun's `os` and `cpu` fields are a cross-product, not a per-pair set: the coarse gate cannot distinguish "supported pair" from "supported OS plus supported arch in any combination." The postinstall's host-pair check catches the two cross-product members that should not actually install.

See Idea Bee `b.fg3` for cross-platform expansion status.

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

Every error thrown by this package extends `AgentDirectorError`. A typed subclass is generated for each `err_name` in the shared catalog so you can catch by subclass:

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

The full `err_name` catalog is in [`../../pkg/api/errnames/catalog.json`](../../pkg/api/errnames/catalog.json).

## Architecture

See [`../../docs/architecture.md`](../../docs/architecture.md) for the internal design. Dedicated subsections cover: Client lifecycle, the subprocess call recipe, Per-platform packaging, Error mapping, TS smoke-test harness, and TS envelope-diff regression.
