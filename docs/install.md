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
sets up `~/.config/agent-director/` (and `~/.config` itself, mode `0700`, if
it is missing), symlinks `agent-director` (see `--symlink-dir`),
installs `agent-director-admin` at `~/.config/agent-director/admin/` (never on PATH),
migrates `state.db` and installs the SessionStart/SessionEnd help hooks.

`install.sh` flags:

- `--from-release [tag]`: download from GitHub Releases (latest, or a tag of
  this release or later). A tag of an earlier release is refused: pass a tag
  of this release or later, or install the earlier release with that
  release's own `install.sh`, fetched from its tag. To verify, pass both
  `--sha256 <hex>` and `--admin-sha256 <hex>`, or neither.
- `--binary <path>`, `--admin-binary <path>`: install from explicit binaries
  of the same build.
- `--register-mcp`: register the stdio MCP server with Claude Code; by hand:
  `claude mcp add agent-director -- ~/.config/agent-director/bin/agent-director serve --stdio`
  (the `--` before the path is required; without it Claude Code refuses
  `--stdio` and registers nothing). See
  [MCP registration](#mcp-registration).
- `--no-hooks`: leave `~/.claude/settings.json` untouched (see
  [Upgrade](#upgrade) for the old entries it prints).
- `--symlink-dir <dir>`: PATH symlink dir (default `~/.local/bin`, if on PATH).
- `--no-symlink`: create no PATH symlink.
- `--keep-prior`: keep each overwritten binary as `<target>.prior` (roll back
  with `mv`). The upgrade that moves the directory makes no snapshot (see
  [Upgrade](#upgrade)).

## MCP registration

With `--register-mcp`, `install.sh` runs
`claude mcp add agent-director -- <path> serve --stdio` (`-s <scope>` after the
name when it re-points a registration, below). What it does depends on what
`claude mcp get agent-director` shows from the directory the install runs in:

- **Nothing registered:** it adds the registration.
- **Already registered for this install's binary** (its command is this
  install's `agent-director` and its arguments are `serve --stdio`): it says so
  and adds nothing, so running the install again with `--register-mcp` is safe.
- **Registered under the same name for another command:** the add fails, because
  Claude Code refuses a name that already exists. The install ends with exit 6
  (below). If that registration should be replaced, remove it, then run the
  install again or the add by hand:

  ```sh
  claude mcp remove agent-director -s <scope>
  ```

- **Naming the old directory's binary** (an upgrade): see [Upgrade](#upgrade).

**Exit 6: the registration failed.** `claude mcp add` (or the `claude mcp remove`
before a re-add) exited non-zero. Every other step is done and kept: the
binaries, the store, the hooks and the links. The message on stderr shows the
command that ran, its exit status, its stderr and the command to run by hand;
the last line says the registration failed. Fix the cause the stderr names,
then run that command. The read of an existing registration (`claude mcp get`)
never fails the install.

**Which directories see a registration.** A `local` registration made inside a
git repository is found from every directory of that repository (it is kept
for the repository's root); made outside one, it is found only from the exact
directory it was made in. A `project` registration is found from the directory
that holds its `.mcp.json` and every directory below it, not from a parent. A
`user` registration is found everywhere. When one name is registered in several
scopes, `claude mcp get` shows one of them: `local` before `user` before
`project`.

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

Run the install again by any path above, from a shell agent-director did not
start (not one inside an agent's session). This release keeps its files in
`~/.config/agent-director`; an earlier release kept them in `~/.agent-director`.
When the install finds that old directory, it moves it first. Later upgrades make
no move: run the install again, with `--keep-prior` to keep the previous
binaries. It brings `state.db` to the current schema; a store newer than the
binary needs the matching release.

**What the move needs:**

- No agent, `agent-director serve` or caller running. The move changes the path
  every hook names, so an agent that runs across it breaks. Stop them as in
  [Stopping a set of agents before a binary change](operator-actions.md#stopping-a-set-of-agents-before-a-binary-change).
  A Claude Code session with agent-director's MCP server registered runs
  `agent-director serve --stdio`, which counts: end that session too.
- The directory the original install ran in as the working directory (or one
  inside the same git repository): see
  [which directories see a registration](#mcp-registration).
- No second agent-director directory, and no link where the move needs a copy.
- For a store kept outside the directory (`[store] db_path`): a copy of
  `state.db`, `state.db-wal` and `state.db-shm` made first. The upgrade
  migrates that store in place.

**What the move does:**

- Copies everything in the old directory except `bin/` and `admin/` into
  `~/.config/agent-director`, with each file's mode, and checks every copy. The
  store is copied with SQLite's backup. A `[store] db_path` or `[log]
  error_log_path` value that names a place inside the old directory is rewritten
  to the same place in the new one. A relative value that points outside the
  old directory, including one that leaves it through a link inside it, is
  rewritten to the absolute path it reaches. An absolute or `~/` value that is
  inside the old directory as written but reaches outside through a link inside
  it is refused before anything changes: set it to the absolute path the
  message shows (see [`ErrMoveConflict`](operator-actions.md#installsh-refuses-with-errmoveconflict)).
- Renames the old directory to `~/.agent-director.pre-move-<YYYYmmdd-HHMMSS>`:
  the original, untouched, old binaries included. Delete it once the upgrade is
  confirmed; `uninstall.sh --purge` removes it too.
- Swaps the help-hook entries in `~/.claude/settings.json`: it removes every
  entry that holds a hook naming the old directory's binary, then adds the new
  ones. An entry that holds such a hook and another hook is removed whole. With
  `--no-hooks` the file is left as it is and the old entries are printed with
  the fix.
- With Claude Code present, reads agent-director's MCP registration from the
  directory the install runs in. With `--register-mcp` it re-points a
  registration that names the old binary; without it, it prints the commands to
  do so. A registration it cannot read as one it can re-point is left as it is,
  with the commands printed.
- Re-points the PATH link it manages to the new binary, and prints each other
  link and each `$PATH` entry that leads into the old `bin` directory, with its
  fix.
- Prints each line of `config.toml` or a template that still names the old
  directory (a `[launch]` value, an `extra_env` value, `cwd`) and leaves it as
  written.
- Makes no `--keep-prior` snapshot: the new `bin/` and `admin/` hold no earlier
  binary.

**After the upgrade:**

- Restart every `serve` and every caller, TypeScript clients included.
- A link `~/.agent-director` that points to `~/.config/agent-director` is allowed,
  for scripts that still name the old path.
- Change by hand each entry that names the old path: a `claude mcp add`
  registration the installer did not find, help hooks in the `settings.json` of
  other `CLAUDE_CONFIG_DIR` folders, Codex MCP and help entries, and scripts or
  test setups that pass the old binary's path.
- An earlier release is installed with its own `install.sh`, fetched from its
  tag; this release's `install.sh` refuses an earlier release's binaries (exit
  3).

**Refusals** (exit 5, with every file unchanged; each has its check and fix in
Operator actions):
[`ErrMoveConflict`](operator-actions.md#installsh-refuses-with-errmoveconflict)
(the layout cannot be moved as it is),
[`ErrAgentsLive`](operator-actions.md#installsh-refuses-with-erragentslive)
(an agent or an agent-director process still uses the old directory), and
[`ErrMoveFailed`](operator-actions.md#installsh-stops-with-errmovefailed)
(the copy failed; the old directory is untouched).

**Going back** to the previous release is
[Going back to the previous release](operator-actions.md#going-back-to-the-previous-release).
Every write since the upgrade is lost.

## Install the TypeScript client

The `agent-director` npm package is the TypeScript/Bun client only; it runs the
installed CLI per call, so install the CLI first, then `bun add agent-director`.
APIs: `pkg/ts-bun-client/README.md` (TypeScript), `pkg/api/README.md` (Go).

## Uninstall

```sh
skills/install-agent-director/uninstall.sh           # keep state.db + templates
skills/install-agent-director/uninstall.sh --purge   # remove everything
```

It works on `~/.config/agent-director`, or on the old directory (see
[Upgrade](#upgrade)) for an install that was never moved. It removes the
binaries, the PATH link, the help hooks in `~/.claude/settings.json` (those of
an install from before the move included) and the `inject_help_hook` setting,
and keeps the directory unless `--purge`.

`--purge` also removes every directory the upgrade or the way back left:
each `~/.agent-director.pre-move-*`, `~/.config/agent-director.after-upgrade-*`
and `~/.agent-director.recreated-*`, an old directory present beside the new
one, and a link to the new directory at the old path. Its prompt names each. A
link is removed as a link, never what it points to. A directory named
`*.aside-*` is always left, and named in the output: an operator set it aside
and it may hold data.

Without `--purge`, every kept directory is named, with the advice to delete it
once the upgrade is confirmed.

`--mcp-also` also runs `claude mcp remove agent-director` from the directory
`uninstall.sh` runs in:

- Removed: it says so.
- No registration found from that directory (Claude Code answers `No MCP server
  named ...`): it says so; this is not a failure. See
  [which directories see a registration](#mcp-registration).
- Any other failure: stderr gets the command run, its exit status and its
  stderr, with the command to run by hand. Every later step, `--purge`
  included, still runs, and `uninstall.sh` then exits 6.

A name registered in several scopes is refused by `claude mcp remove` without a
scope, so `--mcp-also` reports that failure. Remove each one by hand with
`claude mcp remove agent-director -s <scope>`, once per scope
(`claude mcp get agent-director` shows one scope at a time).
