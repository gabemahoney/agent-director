# agent-director

TypeScript/Bun client for the agent-director CLI (FFI-backed; shares the Go API surface 1:1).

## Install

```sh
bun add agent-director
```

Requires Bun >=1.0.21. The package ships a prebuilt shared library for each supported platform via optional dependencies — they install automatically on `bun add`.

## Quick start

`using` block (preferred):

```ts
using client = new Client({ storePath: "~/.agent-director/state.db" });
const v = await client.version({});
console.log(v.version);
```

Explicit `try/finally` (portable fallback):

```ts
const client = new Client({ storePath: "~/.agent-director/state.db" });
try {
  const v = await client.version({});
  console.log(v.version);
} finally {
  client.close();
}
```

`storePath` is the only required constructor option. Tilde expansion (`~` → home directory) is handled automatically before paths cross the FFI boundary. The `using` form calls `client.close()` automatically at block exit and requires Bun >=1.0.21 (or a TypeScript project with `"lib": ["ESNext.Disposable"]`).

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

### kill

Terminate a Spawn's tmux session.

```sh
agent-director kill --claude-instance-id <id>
```

```ts
await client.kill({ claude_instance_id: "<id>" });
```

## Versioning

The library version equals the agent-director release tag — released in lockstep:

| npm package | CLI binary |
|---|---|
| `agent-director@v0.5.0` | `agent-director CLI v0.5.0` |

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

See [`../../docs/architecture.md`](../../docs/architecture.md) for the internal design. Dedicated subsections cover: Client lifecycle, FFI call recipe, Per-platform packaging, Error mapping, TS smoke-test harness, and TS envelope-diff regression.
