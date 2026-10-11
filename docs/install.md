# Install

## Requirements

- A Linux x86_64 or Apple Silicon (arm64) macOS host. Other hosts,
  Linux arm64 included, are not supported.
- `claude` (Claude Code) 2.1.280 or later on PATH — install per
  <https://claude.com/claude-code>. agent-director launches only
  Claude Code; other agent CLIs are unsupported. With an older Claude Code
  the agent's hooks may apply nothing: rows stay `pending` and the trail
  records `ad.hook.ignored` with reason `no_exec_form`; upgrade Claude Code.
  - The `claude` on PATH must be the agent process itself: Claude Code's
    native binary, or a wrapper that `exec`s it. A hook moves a row only
    when its parent process is the pane's own process, so a launcher or
    shim that runs Claude Code as a child (for example a JS launcher left
    by an install that skipped its scripts, or a version-manager shim)
    makes every hook `ad.hook.ignored` with reason `pid_mismatch`, and the
    rows stay `pending` (see
    [A row stays `pending` and the trail shows `pid_mismatch`](operator-actions.md#a-row-stays-pending-and-the-trail-shows-pid_mismatch)).
- `tmux` 3.2 or later on PATH. Verified: 3.2a and 3.3a.
  - Keep `remain-on-exit` off, the tmux default. With it on, a finished
    agent's session outlives its agent: `resume` and `spawn
    --reuse-finished` get `ErrTmuxUnresponsive`, then
    `ErrTmuxSessionConflict`, and `expire` keeps the row on every run.
    `find-missing` still marks a row whose agent process is dead,
    whatever tmux shows.
  - No tmux server needs to be running: agent-director starts one when
    it launches an agent.
  - Agents need `HOME` set. A tmux server started from a systemd system
    unit with no `User=` gives its panes no `HOME`: Claude Code still
    runs there, but agent-director's hooks refuse to work without `HOME`,
    so rows stay `pending`.
- `jq`, `sqlite3` and `file` on PATH (the installer checks for them).

## Install from a release

agent-director ships with its operator tool `agent-director-admin` from the
same build. Install both with:

```sh
curl -fsSL https://raw.githubusercontent.com/gabemahoney/agent-director/main/skills/install-agent-director/install.sh | bash -s -- --from-release
```

That fetches `install.sh` from `main` and runs it with `--from-release`. It
detects your OS/arch, downloads the matching binaries from the
[latest GitHub release](https://github.com/gabemahoney/agent-director/releases/latest),
sets up `~/.agent-director/`, drops a PATH symlink for `agent-director`,
installs `agent-director-admin` at
`~/.agent-director/admin/agent-director-admin` (never on PATH), brings
`state.db` to the current schema (creating it on a fresh host, or upgrading
it in place; a schema problem fails the install loudly rather than
half-installing), and installs the SessionStart/SessionEnd help hooks.

`install.sh` flags:

- `--register-mcp` — register the stdio MCP server with Claude Code.
  To register it yourself instead, run
  `claude mcp add agent-director ~/.agent-director/bin/agent-director serve --stdio`
  (or give the path of your binary).
- `--no-hooks` — leave `~/.claude/settings.json` untouched.
- `--symlink-dir <dir>` — override the default PATH-symlink directory.
- `--binary <path>` — install from an explicit source binary.
- `--admin-binary <path>` — install `agent-director-admin` from an
  explicit source binary, from the same build as `--binary`.
- `--from-release [tag]` — download pre-built binaries for this host's
  OS/arch from GitHub Releases (latest, or a specific tag, 0.11.0 or
  later) and install them. To verify the downloads, pass both
  `--sha256 <hex>` and `--admin-sha256 <hex>`; one without the other is
  refused.

## Install binaries you downloaded

Grab both assets for your platform from the
[latest release](https://github.com/gabemahoney/agent-director/releases/latest),
then point the installer at them:

```sh
# Linux amd64:
curl -L -o agent-director https://github.com/gabemahoney/agent-director/releases/latest/download/agent-director-linux-amd64
curl -L -o agent-director-admin https://github.com/gabemahoney/agent-director/releases/latest/download/agent-director-admin-linux-amd64
# macOS Apple Silicon:
curl -L -o agent-director https://github.com/gabemahoney/agent-director/releases/latest/download/agent-director-darwin-arm64
curl -L -o agent-director-admin https://github.com/gabemahoney/agent-director/releases/latest/download/agent-director-admin-darwin-arm64

chmod +x agent-director agent-director-admin
bash skills/install-agent-director/install.sh --binary ./agent-director --admin-binary ./agent-director-admin
```

The two binaries must come from the same release: `install.sh` refuses
a pair whose `version` stamps differ or carry no commit.

## Install from inside Claude Code

The `install-agent-director` skill ships in this repo at
`skills/install-agent-director/`. Claude Code loads skills only from
`~/.claude/skills/` (or a project's `.claude/skills/`), so link the skill
there once, from the root of your clone:

```sh
mkdir -p ~/.claude/skills
ln -s "$PWD/skills/install-agent-director" ~/.claude/skills/install-agent-director
```

Then, in Claude Code, say:

> **install agent-director**

The skill asks four questions (binary source, PATH symlink, MCP
registration, persistent help hooks), then runs `install.sh` with the
resolved flags.

## Install from source

`make build` builds both `bin/agent-director` and
`bin/agent-director-admin`:

```sh
make build
bash skills/install-agent-director/install.sh --binary ./bin/agent-director --admin-binary ./bin/agent-director-admin
```

`install.sh` compares the binary's `version` stamp with `git rev-parse HEAD`
and refuses a stale `--binary`; re-run `make build` to refresh it. It also
refuses unless `agent-director-admin version` reports the same stamp, with
a real commit: `make build` in a git checkout stamps both, a plain
`go build` does not.

## Upgrade

Run the install again, by any of the paths above. It brings an existing
`state.db` up to the current schema. If a session reports a schema-version
error, re-run the install. If the store is newer than the binary, install
the matching agent-director release instead. If a command that opens the
store still reports a schema error after the install has run, restore the
copy of `~/.agent-director/state.db` (with its `-wal` and `-shm` files)
taken before the install, then re-run the install.

## Install the TypeScript client

The `agent-director` npm package is the TypeScript/Bun client only: it runs
the installed CLI binary as a subprocess per call. It does not install the
CLI; install that first.

```sh
bun add agent-director
```

Load it from Bun as ESM; it is not built for webpack. The package ships no
lifecycle scripts and no native or optional platform dependencies. Its API is in `pkg/ts-bun-client/README.md`. The Go library,
`github.com/gabemahoney/agent-director/pkg/api`, is in `pkg/api/README.md`.

## Uninstall

```sh
skills/install-agent-director/uninstall.sh           # preserve state.db + templates
skills/install-agent-director/uninstall.sh --purge   # remove everything
```
