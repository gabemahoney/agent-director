# Install

## Requirements

- Linux x86_64 or macOS arm64 (Apple Silicon); no other platform.
- `claude` (Claude Code) 2.1.280 or later on PATH
  (<https://claude.com/claude-code>): its native binary, or a wrapper that
  `exec`s it, not a shim that runs it as a child.
- `tmux` 3.2 or later on PATH (verified: 3.2a and 3.3a), with `remain-on-exit`
  off (the default). No tmux server needs to be running.
- Agents need `HOME` set (a tmux server started from a systemd system unit
  with no `User=` has none).
- `jq`, `sqlite3` and `file` on PATH (the installer checks for them).

## Install from a release

This installs agent-director and its operator tool `agent-director-admin`:

```sh
curl -fsSL https://raw.githubusercontent.com/gabemahoney/agent-director/main/skills/install-agent-director/install.sh | bash -s -- --from-release
```

It downloads the binaries from the
[latest release](https://github.com/gabemahoney/agent-director/releases/latest),
sets up `~/.agent-director/`, symlinks `agent-director` (see `--symlink-dir`),
installs `agent-director-admin` at `~/.agent-director/admin/` (never on PATH),
migrates `state.db` and installs the SessionStart/SessionEnd help hooks.

`install.sh` flags:

- `--from-release [tag]`: download from GitHub Releases (latest, or a tag of
  0.11.0 or later). To verify, pass both `--sha256 <hex>` and
  `--admin-sha256 <hex>`, or neither.
- `--binary <path>`, `--admin-binary <path>`: install from explicit binaries
  of the same build.
- `--register-mcp`: register the stdio MCP server with Claude Code; by hand:
  `claude mcp add agent-director ~/.agent-director/bin/agent-director serve --stdio`
- `--no-hooks`: leave `~/.claude/settings.json` untouched.
- `--symlink-dir <dir>`: PATH symlink dir (default `~/.local/bin`, if on PATH).
- `--no-symlink`: create no PATH symlink.
- `--keep-prior`: keep each overwritten binary as `<target>.prior` (roll back
  with `mv`).

## Install binaries you downloaded

Both assets must come from the same
[release](https://github.com/gabemahoney/agent-director/releases/latest); this
needs no clone:

```sh
p=linux-amd64   # or darwin-arm64
base=https://github.com/gabemahoney/agent-director/releases/latest/download
curl -fL -o agent-director "$base/agent-director-$p"
curl -fL -o agent-director-admin "$base/agent-director-admin-$p"
chmod +x agent-director agent-director-admin
curl -fsSL https://raw.githubusercontent.com/gabemahoney/agent-director/main/skills/install-agent-director/install.sh | bash -s -- --binary ./agent-director --admin-binary ./agent-director-admin
```

## Install from inside Claude Code

From the root of your clone, link the `install-agent-director` skill once,
then say **install agent-director** in Claude Code:

```sh
mkdir -p ~/.claude/skills && ln -s "$PWD/skills/install-agent-director" ~/.claude/skills/install-agent-director
```

The skill asks four questions (source, symlink, MCP, hooks), then runs
`install.sh`.

## Install from source

In a git checkout (`install.sh` refuses a plain `go build`):

```sh
make build
bash skills/install-agent-director/install.sh --binary ./bin/agent-director --admin-binary ./bin/agent-director-admin
```

## Upgrade

Stop every agent and every `agent-director serve` first, as in
[Stopping a set of agents before a binary change](operator-actions.md#stopping-a-set-of-agents-before-a-binary-change).
Copy `~/.agent-director/state.db`, `state.db-wal` and `state.db-shm`. Then run
the install again by any path above, with `--keep-prior` to keep the previous
binaries; it brings `state.db` to the current schema. A store newer than the
binary needs the matching release.

## Install the TypeScript client

The `agent-director` npm package is the TypeScript/Bun client only; it runs the
installed CLI per call, so install the CLI first, then `bun add agent-director`.
APIs: `pkg/ts-bun-client/README.md` (TypeScript), `pkg/api/README.md` (Go).

## Uninstall

```sh
skills/install-agent-director/uninstall.sh           # keep state.db + templates
skills/install-agent-director/uninstall.sh --purge   # remove everything
```
