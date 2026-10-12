# Install

## Requirements

- Linux x86_64 or macOS arm64 (Apple Silicon); no other platform.
- At least one of Claude Code and Codex. Each counts as installed when its
  `[launch]` key in `config.toml` (`claude_path`, `codex_path`; see
  [configuration](configuration.md)) names an executable file, or its program
  name (`claude`, `codex`) is on PATH. With neither, the install stops and names
  both.
- For Claude Code: `claude` (Claude Code) 2.1.280 or later
  (<https://claude.com/claude-code>): its native binary, or a wrapper that
  `exec`s it, not a shim that runs it as a child.
- For Codex: `codex` (Codex) 0.156.0 or later, the native Codex program, not a
  launcher that runs it as a child. Codex installed with npm puts a node
  launcher script on PATH: set `[launch] codex_path` to the native program
  instead. On Linux x86_64 it is the file
  `vendor/x86_64-unknown-linux-musl/bin/codex` inside the npm package
  `@openai/codex-linux-x64`, which this finds:

  ```sh
  find "$(npm root -g)" -path '*codex-linux-x64*' -name codex -type f
  ```

  Codex's standalone install, which needs no setting:

  ```sh
  curl -fsSL https://chatgpt.com/codex/install.sh | sh
  ```

  It puts a link to its native program on PATH. On Linux x86_64 that program is
  `$HOME/.codex/packages/standalone/releases/<version>-x86_64-unknown-linux-musl/bin/codex`,
  whose `<version>` changes with each update, so leave `codex_path` unset and
  use the link. Where the native program is on macOS arm64 is not documented
  here.
- `tmux` 3.2 or later on PATH (verified: 3.2a and 3.3a), with `remain-on-exit`
  off (the default). No tmux server needs to be running.
- `HOME` set to an absolute path when `install.sh` or `uninstall.sh` runs: each
  stops at once, exit 2, with it unset, empty or relative, before it reads or
  writes anything. A tmux server started from a systemd system unit with no
  `User=` has no `HOME`; run the script with `HOME` set to the user's home
  directory. (A `~/` path in `config.toml` is refused at load while `HOME` is
  unset or empty: see [configuration](configuration.md).)
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
migrates `state.db` and installs the SessionStart/SessionEnd help hooks (with
Claude Code installed; see [Claude Code and Codex](#claude-code-and-codex)).

`install.sh` flags:

- `--from-release [tag]`: download from GitHub Releases (latest, or a tag of
  this release or later). A tag of an earlier release is refused: pass a tag
  of this release or later, or install the earlier release with that
  release's own `install.sh`, fetched from its tag. To verify, pass both
  `--sha256 <hex>` and `--admin-sha256 <hex>`, or neither.
- `--binary <path>`, `--admin-binary <path>`: install from explicit binaries
  of the same build.
- `--register-mcp`: register the stdio MCP server with Claude Code (Claude Code
  only; a Codex folder's entry is made by hand, see [Set up agent-director in a
  Codex folder](settings.md#set-up-agent-director-in-a-codex-folder)). It runs
  the program `[launch] claude_path` names when that is an executable file, else
  `claude` from PATH. By hand, with that
  program for `claude`:
  `claude mcp add agent-director -- ~/.config/agent-director/bin/agent-director serve --stdio`
  (the `--` before the path is required; without it Claude Code refuses
  `--stdio` and registers nothing). See
  [MCP registration](#mcp-registration).
- `--no-hooks`: leave `~/.claude/settings.json` untouched (see
  [Upgrade](#upgrade) for the old entries it prints) and do not set
  `inject_help_hook` in `config.toml` (see
  [settings](settings.md#defaultsinject_help_hook-config)), which is also what
  gives a Codex launch its help entry. It does not skip the [rename
  migration](#upgrade).
- `--schedule`: schedule the `find-missing` and `expire` sweeps for the user
  running the script, who must be the agents' user. Off by default. See
  [Schedule the sweeps](#schedule-the-sweeps).
- `--symlink-dir <dir>`: PATH symlink dir (default `~/.local/bin`, if on PATH).
- `--no-symlink`: create no PATH symlink.
- `--keep-prior`: keep each overwritten binary as `<target>.prior` (roll back
  with `mv`). The upgrade that moves the directory makes no snapshot (see
  [Upgrade](#upgrade)).

## Claude Code and Codex

Before it changes anything, `install.sh` looks for each agent (see
[Requirements](#requirements)) and prints one line per agent in its summary:

- **Claude Code found:** its `--version` line and the program's path. The
  program is `[launch] claude_path` when that names an executable file, else
  `claude` from PATH; every Claude Code command the installer runs (the version
  line, `claude mcp get`, `add` and `remove`) goes through it.
- **Codex found:** the program's path, with a note that its version was not
  checked and the command to check it. `install.sh` never runs Codex, because
  `codex --version` writes Codex's own files (`tmp/arg0/...`) in the Codex
  folder it runs against: the default one unless `CODEX_HOME` names another.
- **An agent not found:** `not installed`, with the reason.
- **A `[launch]` path that names no executable file:** a line saying so. The
  agent counts as installed only if its program is on PATH, and that program is
  then the one used.

Without Claude Code (a Codex-only host) the install still succeeds, with these
differences:

- `~/.claude/settings.json` is not read, written or checked, so the help hooks
  are not added and a dangling link there is not an error. A line says it was
  skipped because Claude Code is not installed, with or without `--no-hooks`.
- `--register-mcp` registers nothing, and says so. No `claude mcp get` runs.
- `inject_help_hook` is still set in `config.toml` unless `--no-hooks`.

`install.sh` registers nothing with Codex, writes nothing in any Codex folder
and never writes `[launch]`. A Codex folder's MCP entry and help entry are made
by hand: see [Set up agent-director in a Codex
folder](settings.md#set-up-agent-director-in-a-codex-folder). With neither agent
found it stops, exit 2, before anything changes, and says how to make one found: put `claude` or `codex` on
PATH, or set `[launch] claude_path` or `codex_path` in `config.toml`. The
message names the `config.toml` the install read: the old directory's while the
move is due (see [Upgrade](#upgrade)), else `~/.config/agent-director/config.toml`.
This stop comes after every `config.toml` refusal: a file that is malformed is
refused first, exit 5 (see
[`install.sh` refuses config.toml or a template](operator-actions.md#installsh-refuses-configtoml-or-a-template)),
and the no-agent stop follows once it is fixed.

## MCP registration

With `--register-mcp`, `install.sh` runs
`claude mcp add agent-director -- <path> serve --stdio` (`-s <scope>` after the
name when it re-points a registration, below), with `claude` the program
chosen as [above](#claude-code-and-codex). What it does depends on what
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

The skill asks five questions (source, symlink, MCP, hooks, schedule), then
runs `install.sh`.

## Install from source

In a git checkout (`install.sh` refuses a plain `go build`):

```sh
make build
bash skills/install-agent-director/install.sh --binary ./bin/agent-director --admin-binary ./bin/agent-director-admin
```

## Schedule the sweeps

`--schedule` schedules the two sweeps:

```sh
bash skills/install-agent-director/install.sh --schedule
```

What it installs, how to do it by hand and the same-environment rule are in
[Maintenance](maintenance.md#scheduling-the-sweeps). An install that finds no
schedule ends with a warning on stderr: see
[`install.sh` warns that nothing schedules the sweeps](operator-actions.md#installsh-warns-that-nothing-schedules-the-sweeps).

**A piped install** (`curl ... | bash -s -- ...`) has no `schedule/` beside it.
When `--schedule` is given or nothing is scheduled yet, it fetches the same five
files from `raw.githubusercontent.com` at the release tag (with
`--from-release`) or, with `--binary`, at the full commit stamp of the binaries,
all or none, and keeps them in `~/.config/agent-director/schedule/`. When the
release tag or commit has no `schedule/` (an older release: HTTP 404), it tries
once more at `main`, and the `schedule:` line says so.

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
  Do not run `make-template` with `--store-path`, or the Go client's
  `MakeTemplate`, before the install: see
  [`ErrMoveConflict`](operator-actions.md#installsh-refuses-with-errmoveconflict).
- For a store kept outside the directory (`[store] db_path`), including one
  whose `db_path` reaches it through a link inside the directory: a copy of
  `state.db`, `state.db-wal` and `state.db-shm` made first. The upgrade
  migrates that store in place.

**What the move does:**

- Copies everything in the old directory except `bin/` and `admin/` into
  `~/.config/agent-director`, with each file's mode, and checks every copy. The
  store is copied with SQLite's backup. A `[store] db_path` or `[log]
  error_log_path` value that names a place inside the old directory is rewritten
  to the same place in the new one. A value that is inside the old directory as
  written but reaches outside it through a link inside the old directory is
  rewritten to the absolute path it reaches today, whether it is relative,
  absolute or `~/`. A relative value that points outside the old directory is
  rewritten to the absolute path it reaches too.
- Renames the old directory to `~/.agent-director.pre-move-<YYYYmmdd-HHMMSS>`:
  the original, untouched, old binaries included. Delete it once the upgrade is
  confirmed; `uninstall.sh --purge` removes it too.
- With Claude Code present, swaps the help-hook entries in
  `~/.claude/settings.json`: it removes every entry that holds a hook naming the
  old directory's binary, then adds the new ones. An entry that holds such a
  hook and another hook is removed whole. With `--no-hooks` the file is left as
  it is and the old entries are printed with the fix. Without Claude Code the
  file is not touched.
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

**What the rename migration does.** An install that finds a store older than
this release's schema also migrates `config.toml` and every template in
`templates/`, once, whether or not the directory moves. With no store, or a
store already at this release's schema, it reads and changes none of them. It
runs with `--no-hooks` too, after the move and the binary swap and before the
store is opened:

- In `config.toml`, the question-tool key under `[defaults]` is renamed from its
  old name to `disable_ask_user`, in place.
- In each template, only the lines before its first table header are read. A
  line that sets the arguments key by its old name is renamed `agent_args`, in
  place, and a template with no `agent` there gets the line `agent = "claude"`
  just before its first table header, or at the end of the file when it has no
  table. The tables are never read or changed.
- Each file it changes is first copied beside itself as
  `<file>.pre-agent-rename.<YYYYmmdd-HHMMSS>`, with the file's mode, and is then
  replaced whole, keeping its mode; a link is written through and kept. A file
  that needs no change gets no write and no copy, so running the install again
  changes nothing already migrated.
- While the move is due the files are read in the old directory, and the
  migration changes the copies in the new one.

Only this release's installer migrates. A template copied in afterwards that
holds the old name of the arguments key is refused at spawn, naming
`agent_args`; one with no `agent` supplies no agent.

**After the upgrade:**

- Restart every `serve` and every caller, TypeScript clients included.
- A link `~/.agent-director` that points to `~/.config/agent-director` is allowed,
  for scripts that still name the old path.
- Change by hand each entry that names the old path: a `claude mcp add`
  registration the installer did not find, help hooks in the `settings.json` of
  other `CLAUDE_CONFIG_DIR` folders, [Codex MCP and help
  entries](settings.md#set-up-agent-director-in-a-codex-folder), and scripts or
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

The migration refuses a `config.toml` or a template it cannot read safely, or a
link it cannot write through (exit 5, `ErrConfigMalformed` or
`ErrTemplateMalformed`, every file unchanged): see
[`install.sh` refuses config.toml or a template](operator-actions.md#installsh-refuses-configtoml-or-a-template).
A migration write that fails after the binaries are installed stops the install
(exit 1, the file as it was, every copy kept), as does a migration that refuses
a file the checks accepted (exit 5). After a move, each of these two stops also
prints the lines that still name the old directory, as the move's summary does,
because a re-run has no move to print them. See
[A migration write failed after the binary swap](operator-actions.md#a-migration-write-failed-after-the-binary-swap).

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
and keeps the directory unless `--purge`. It leaves the MCP and help entries
made by hand in Codex folders; remove them as [Set up agent-director in a Codex
folder](settings.md#set-up-agent-director-in-a-codex-folder) says.

It also removes the schedule `--schedule` added and nothing else: the crontab
lines tagged `agent-director-schedule` (the crontab itself only when nothing
else is left in it), the tagged systemd user units with their timers, and the
schedule files a piped install kept in `~/.config/agent-director/schedule/`
(the directory stays, with a note, when it holds other files). With no schedule
present it prints nothing about it.

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
`uninstall.sh` runs in, through the program `[launch] claude_path` names in the
`config.toml` of the directory it works on when that is an executable file, else
`claude` from PATH. It never stops or fails over `[launch]`:

- A `[launch]` line it cannot read, or a `claude_path` that names no executable
  file: a note names it and the step uses `claude` from PATH.
- No Claude Code program found: a line says the step was skipped and
  agent-director was not deregistered from Claude Code. The exit status is
  unchanged.
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
