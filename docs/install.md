# Install

## Requirements

- Linux on x86_64 or arm64 (aarch64), or macOS 13 or later on arm64
  (Apple Silicon); no other platform. Debian 12 (bookworm) is long-term
  supported on both x86_64 and arm64.
- At least one of Claude Code, Codex and Cursor. Each counts as installed when
  its `[launch]` key in `config.toml` (`claude_path`, `codex_path`, `cursor_path`; see
  [configuration](configuration.md)) names an executable file, or its program
  name (`claude`, `codex`, `agent`) is on PATH. With none, the install stops
  and names all three. This checks presence, not runtime compatibility.
- For Cursor: the standalone `agent` program from <https://cursor.com/install>,
  on PATH or named by `[launch] cursor_path`. The editor's `cursor` command
  does not count. The Cursor gateway currently runs on Linux only; no minimum
  Cursor version or macOS Cursor runtime support is established. The recorded
  linux/amd64 candidate, `2026.10.12-d47ca18`, is not a minimum-version guarantee.
  Compatibility requires ACP protocol 1 with a non-null `agentCapabilities`
  object. Optional capabilities may be omitted for a fresh text session;
  resume additionally requires `loadSession: true`. Requested session, mode
  and prompt operations must receive valid authoritative responses. Presence
  discovery does not check those operations; see
  [Cursor operation boundaries](caller-contract.md#cursor-operation-boundaries).
- For Claude Code: `claude` (Claude Code) 2.1.280 or later
  (<https://claude.com/claude-code>): its native binary, or a wrapper that
  `exec`s it, not a shim that runs it as a child. Its `feedbackDrafts` user
  setting, shown in `/config` as "Claude-drafted feedback", defaults to
  "notify", which can put a feedback-draft menu in a driven agent's pane that
  may stop agent-director's input reaching the agent; we suggest
  setting it to "off".
- For Codex: `codex` (Codex) 0.159.2 or later, the native Codex program, not a
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
  use the link. Native Codex paths for npm and standalone installs on
  Linux arm64 and macOS arm64 are not documented here.
- `tmux` 3.2 or later on PATH (verified: 3.2a and 3.3a), with `remain-on-exit`
  off (the default). No tmux server needs to be running.
- `HOME` set to an absolute path when `install.sh` or `uninstall.sh` runs: each
  stops at once, exit 2, with it unset, empty or relative, before it reads or
  writes anything. A tmux server started from a systemd system unit with no
  `User=` has no `HOME`; run the script with `HOME` set to the user's home
  directory. (A `~/` path in `config.toml` is refused at load while `HOME` is
  unset or empty: see [configuration](configuration.md).)
- Bash to run `install.sh`, and these commands on PATH in every install mode:
  `jq`, `sqlite3`, `file`, `uname`, `basename`, `cat`, `chmod`, `cmp`, `cp`,
  `date`, `dirname`, `find`, `grep`, `head`, `id`, `ln`, `mkdir`, `mktemp`,
  `mv`, `od`, `readlink`, `rm`, `rmdir`, `sed`, `stat`, `tail` and `tr`,
  in addition to `tmux` above.
- `awk` unless `--no-hooks` is supplied; checksum verification also requires
  it, even with `--no-hooks`.
- With `--from-release`: `curl` and `sleep`. With `--sha256` and
  `--admin-sha256`: either `sha256sum` or `shasum`.
- On macOS, an upgrade of an existing store reads the user's processes with
  the system `/usr/sbin/lsof` and access-control entries with `/bin/ls`: run
  the install as the agents' user, not as root, and grant the terminal access
  if macOS asks. [Upgrade refusal
  reasons](operator-actions.md#upgrade-refusal-reasons) gives the remedies.

After validating arguments and the install path, the installer checks all
required commands for the selected mode before running external commands,
downloading files or changing the installation. If any are missing, it exits
2 with one report listing all of them and package-install hints.

`gh` is optional for release lookup and download fallback; `curl` remains
required for release installs. `git`, `ps`, `lsof`, `pgrep`, `getent`, `dscl`,
`crontab` and `systemctl` remain optional probes or alternatives, except
the macOS upgrade tools above. Scheduling
keeps its [cron/systemd fallback and warnings](#schedule-the-sweeps).

Native confirmed ordinary text delivery has narrower requirements: Linux, the exact
agent versions above and a fresh supported launch. See
[Ordinary text submission](caller-contract.md#ordinary-text-submission).
Other launches retain literal text delivery without exact transcript confirmation,
as described in that contract.

## Install from a release

Choose a release tag, or find the latest with this read-only command:

```sh
curl -fsSLI -o /dev/null -w '%{url_effective}\n' https://github.com/gabemahoney/agent-director/releases/latest
```

The URL must end in `/releases/tag/<tag>`. Replace both `<tag>`s below with
that tag to install agent-director and its operator tool `agent-director-admin`:

```sh
curl -fsSL https://raw.githubusercontent.com/gabemahoney/agent-director/<tag>/skills/install-agent-director/install.sh | bash -s -- --from-release <tag>
```

It downloads the binaries from that release,
sets up `~/.config/agent-director/` (and `~/.config` itself, mode `0700`, if
it is missing), symlinks `agent-director` (see `--symlink-dir`),
installs `agent-director-admin` at `~/.config/agent-director/admin/` (never on PATH),
migrates `state.db` and installs the SessionStart/SessionEnd help hooks (with
Claude Code installed; see [Claude Code and Codex](#claude-code-and-codex)).

`install.sh` flags:

- `--from-release [tag]`: download from GitHub Releases (latest, or a tag of
  this release or later). A tag of an earlier release is refused: pass a tag
  of this release or later, or install the earlier release with that
  release's own `install.sh`, fetched from its tag, provided it
  [supports this host](#requirements). To verify, pass both
  `--sha256 <hex>` and `--admin-sha256 <hex>`, or neither.
- `--binary <path>`, `--admin-binary <path>`: install from explicit binaries
  of the same build.
- `--check-holders`: strictly read-only process inventory for native-input cutover
  holders in the current layout, then exit without installing. Use the candidate
  source and journal flags described under [Upgrade](#upgrade).
- `--register-mcp`: register the stdio MCP server with Claude Code (Claude Code
  only; a Codex folder's entry is made by hand, see [Set up agent-director in a
  Codex folder](settings.md#set-up-agent-director-in-a-codex-folder)). It runs
  the program `[launch] claude_path` names when that is an executable file, else
  `claude` from PATH. By hand, with that
  program for `claude`:
  `claude mcp add agent-director -- ~/.config/agent-director/bin/agent-director serve --stdio`
  (the `--` before the path is required). See
  [MCP registration](#mcp-registration).
- `--no-hooks`: leave `~/.claude/settings.json` untouched (see
  [Upgrade](#upgrade) for the old entries it prints) and do not set
  `inject_help_hook` in `config.toml` (see
  [settings](settings.md#defaultsinject_help_hook-config)), which is also what
  enables Codex help entries and Cursor session guidance. It does not skip the [rename
  migration](#upgrade).
- `--schedule`: schedule recovery (`sweep` when supported) and separate
  `expire` cleanup for the user running the script, who must be the agents'
  user. Off by default. See
  [Schedule the sweeps](#schedule-the-sweeps).
- `--journal-maintenance-window`: assert that old writers are excluded; see
  [Upgrading the native-input journal](operator-actions.md#upgrading-the-native-input-journal).
- `--journal-roots-complete`: assert that every legacy input root is accounted
  for; see
  [Upgrading the native-input journal](operator-actions.md#upgrading-the-native-input-journal).
- `--journal-backup <dir>`: choose a new absolute directory for the
  stopped-writer backup when crossing the native-input cutover; see
  [Upgrading the native-input journal](operator-actions.md#upgrading-the-native-input-journal).
- `--journal-legacy-root <dir>`: include an additional absolute legacy input
  root (repeat for each); see
  [Upgrading the native-input journal](operator-actions.md#upgrading-the-native-input-journal).
- `--symlink-dir <dir>`: PATH symlink dir (default `~/.local/bin`, if on PATH).
- `--no-symlink`: create no PATH symlink.
- `--keep-prior`: keep each overwritten binary as `<target>.prior`. Restoring
  binaries alone requires an unchanged directory and schema. The upgrade that
  moves the directory makes no snapshot (see
  [Upgrade](#upgrade)).

## Claude Code and Codex

Before it changes anything, `install.sh` looks for each agent (see
[Requirements](#requirements)) and prints one line per agent in its summary:

- **Claude Code found:** its `--version` line and the program's path. The
  program is `[launch] claude_path` when that names an executable file, else
  `claude` from PATH; every Claude Code command the installer runs (the version
  line, `claude mcp get`, `add` and `remove`) goes through it.
- **Codex found:** the program's path, with a note that its version was not
  checked and the command to check it. `install.sh` never runs Codex.
- **Cursor found:** the standalone `agent` program's path, with a note that
  this is presence only: version and compatibility were not checked and
  `agent` was not run.
- **An agent not found:** `not installed`, with the reason.
- **A `[launch]` path that names no executable file:** a line saying so. The
  agent counts as installed only if its program is on PATH, and that program is
  then the one discovered for installation. This does not rewrite `[launch]`.
  For Cursor, a configured missing path remains the runtime launch path;
  discovering `agent` on PATH allows installation only. See
  [`cursor_path`](configuration.md).

Without Claude Code (a Codex-only or Cursor-only host) the install still
succeeds, with these differences:

- `~/.claude/settings.json` is not read, written or checked, so the help hooks
  are not added and a dangling link there is not an error. A line says it was
  skipped because Claude Code is not installed, with or without `--no-hooks`.
- `--register-mcp` registers nothing, and says so. No `claude mcp get` runs.
- `inject_help_hook` is still set in `config.toml` unless `--no-hooks`.

`install.sh` registers nothing with Codex or Cursor, writes nothing in their
agent folders and never writes `[launch]`. A Codex folder's MCP entry and help entry are made
by hand: see [Set up agent-director in a Codex
folder](settings.md#set-up-agent-director-in-a-codex-folder). With no supported agent
found it stops, exit 2, before anything changes, and says how to make one found:
put `claude`, `codex` or `agent` on PATH, or set `[launch] claude_path`,
`codex_path` or `cursor_path` in `config.toml`. The message names the
`config.toml` the install read: the old directory's while the
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
- **Registered under the same name for another command:** the add fails and the
  install ends with exit 6 (below).
- **Naming the old directory's binary** (an upgrade): see [Upgrade](#upgrade).

**Exit 6: the registration failed.** Every other install step is done and kept;
see [The MCP registration or deregistration failed (exit 6)](operator-actions.md#the-mcp-registration-or-deregistration-failed-exit-6).

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
needs no clone. Choose `<tag>` with the read-only lookup in
[Install from a release](#install-from-a-release), and replace both `<tag>`s
below with it:

```sh
p=linux-amd64   # or linux-arm64 or darwin-arm64
base=https://github.com/gabemahoney/agent-director/releases/download/<tag>
curl -fL -o agent-director "$base/agent-director-$p"
curl -fL -o agent-director-admin "$base/agent-director-admin-$p"
chmod +x agent-director agent-director-admin
curl -fsSL https://raw.githubusercontent.com/gabemahoney/agent-director/<tag>/skills/install-agent-director/install.sh | bash -s -- --binary ./agent-director --admin-binary ./agent-director-admin
```

## Install from inside Claude Code

From the root of your clone, link the `install-agent-director` and
`upgrade-agent-director` skills once, then say **install agent-director** in
Claude Code, or **upgrade agent-director** for an existing install:

```sh
mkdir -p ~/.claude/skills
for s in install-agent-director upgrade-agent-director; do [ -e ~/.claude/skills/$s ] || [ -L ~/.claude/skills/$s ] || ln -s "$PWD/skills/$s" ~/.claude/skills/$s; done
```

The install skill asks five questions (source, symlink, MCP, hooks, schedule),
then runs `install.sh`.

## Install from source

In a git checkout (`install.sh` refuses a plain `go build`):

```sh
make build
bash skills/install-agent-director/install.sh --binary ./bin/agent-director --admin-binary ./bin/agent-director-admin
```

## Schedule the sweeps

`--schedule` opts in to scheduling recovery and separate `expire` cleanup:

```sh
bash skills/install-agent-director/install.sh --schedule
```

Without the flag, an install or upgrade preserves existing schedule commands,
cadence and timer enablement. With it, the installer replaces its own entries
using the template cadence. Choose the defaults or set your own cadence in
[Maintenance](maintenance.md#schedule-them-by-hand).

What it installs, how to do it by hand and the same-environment rule are in
[Maintenance](maintenance.md#scheduling-the-sweeps). An install that cannot find active
`--schedule` entries for both jobs ends with a warning on stderr: see
[The install schedule warning](operator-actions.md#installsh-warns-that-nothing-schedules-the-sweeps).

**A piped install** (`curl ... | bash -s -- ...`) has no `schedule/` beside it.
When `--schedule` is given or its entries are not found, it fetches the same five
files from `raw.githubusercontent.com` at the release tag (with
`--from-release`) or, with `--binary`, at the full commit stamp of the binaries,
all or none, and keeps them in `~/.config/agent-director/schedule/`. When the
release tag or commit has no `schedule/` (HTTP 404), it tries
once more at `main`, and the `schedule:` line says so.

Raw downloadable templates use `find-missing`. The installer checks only the
target binary's `help`: if it advertises `sweep`,
rendered cron lines, services and advice use `sweep`; otherwise they use
`find-missing`. Failed or unreadable help also keeps `find-missing`. It never
runs recovery to discover support. Existing `find-missing` schedules remain
valid aliases on a supporting binary; an upgrade needs no schedule rewrite.

## Upgrade

An agent upgrading an install follows the
[upgrade skill](../skills/upgrade-agent-director/SKILL.md) step by step.

You can upgrade directly to this release from any earlier agent-director
release starting at 0.3.0; no intermediate release is needed. This installer
cannot upgrade `claude-director` 0.1.0–0.2.3 in `~/.claude-director`: it would make a fresh
install beside those files.

An upgrade of an existing store below schema v16 crosses the native-input
journal cutover, once, and needs the journal flags listed above; a fresh
install or a store at schema v16 or later needs none. Follow the
[maintenance-window procedure](operator-actions.md#upgrading-the-native-input-journal)
for check timing, the complete recovery set and writer exclusion.

Check holders with the candidate release and the same journal flags planned
for installation:

```sh
bash -o pipefail -c 'curl -fsSL https://raw.githubusercontent.com/gabemahoney/agent-director/<tag>/skills/install-agent-director/install.sh | bash -s -- --from-release <tag> "$@"' upgrade-agent-director --check-holders --journal-maintenance-window --journal-roots-complete --journal-backup /absolute/new-backup-directory --journal-legacy-root /absolute/additional/input
```

The `pipefail` wrapper makes a failed download fail the check.

Choose `<tag>` once as in [Install from a release](#install-from-a-release)
and reuse it for both checks and installation, or use `--binary` and
`--admin-binary` for matching local candidates. Include the original install's
other flags and any additions in both checks and installation too. Omit the last
root pair if none is needed; repeat it for every extra root. This strictly
read-only mode allows live rows. It exits 0 when clear or 5 on refusal,
listing any holders. With no store, schema v0 or schema v16 or later, it
reports no check is needed and exits 0. Argument, dependency and source-selection
errors retain their usual exit codes. Temporary downloads and private snapshots
are removed at exit; HOME files, the store and sidecars,
backups, sentinels, hooks, MCP registrations, schedules and links stay unchanged.

The inventory checks the current configured store, its sidecars and the complete
input roots, resolving aliases. With a directory move due, it checks the old
store before the move. It does not refuse a store or root merely because it is
not yet private. Ownership, modes, link components and access-control entries
are checked by the real migration after the installer prepares the paths.
An unresolvable inventory path still refuses with `process_inventory_unreadable`.

The candidate administrator also offers strictly read-only
`--store-path /absolute/actual-state.db migrate-journal --check-only` with
the corresponding journal flags; without `--store-path`, it checks arguments
only. See the [operator procedure](operator-actions.md#upgrading-the-native-input-journal).

Recovery belongs in the
[operator runbook](operator-actions.md#going-back-to-the-previous-release).

Run the install again by any path above, from a shell agent-director did not
start (not one inside an agent's session). Releases 0.3.0–0.11.1 keep their
files in `~/.agent-director`; the installer moves that directory once to
`~/.config/agent-director`, before the other install steps. Releases 0.12.0
and later already use the new directory and need no move. Run the install
again, with `--keep-prior` to keep the previous
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
  registration that names the old binary with exactly `serve --stdio` and no
  custom environment; without it, it prints the commands to do so. A
  registration with custom arguments or environment, or one it cannot read,
  is left as it is even with `--register-mcp`, and no registration is added
  over it. The printed commands are a starting point for a manual re-point:
  use the scope `claude mcp get agent-director` shows, and retain the
  registration's custom arguments and environment.
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

The installer migrates these files only during a schema upgrade. Templates
added afterwards use `agent_args` for arguments; omitting `agent` leaves
agent selection to the caller.

**After the upgrade:**

- Restart every `serve` and every caller, TypeScript clients included.
- Resume each agent stopped for the upgrade, parents first, with
  `AGENT_DIRECTOR_INSTANCE_ID=<its recorded parent>` set for each child's
  command only; follow [step 6 of the upgrade skill](../skills/upgrade-agent-director/SKILL.md#6-resume-the-agents).
- Cursor gateway refusal reporting requires both the caller and the running
  gateway to be agent-director 0.17.2 or later. Relaunch Cursor rows (`kill` or
  `pause`, then `resume`) to run the installed gateway; follow
  [Cursor input outcomes](caller-contract.md#cursor-input-outcomes) if input is held.
- Check, then delete, any `~/.claude/settings.json.bak.<YYYYmmdd-HHMMSS>`
  files: they may hold your tokens. Install and uninstall keep no
  copy of `settings.json` and delete none of these, but each names those it
  finds on one line, each path shell-quoted, ready to paste after `rm --`.
- A link `~/.agent-director` that points to `~/.config/agent-director` is allowed
  for scripts that still name the old path when the install does not cross the
  native-input cutover. For a crossing upgrade, follow the
  [link refusal procedure](operator-actions.md#installsh-refuses-with-errjournalmigrationrefused).
- Change by hand each entry that names the old path: a `claude mcp add`
  registration the installer did not find, help hooks in the `settings.json` of
  other `CLAUDE_CONFIG_DIR` folders, [Codex MCP and help
  entries](settings.md#set-up-agent-director-in-a-codex-folder), and scripts or
  test setups that pass the old binary's path.
- An earlier release is installed with its own `install.sh`, fetched from its
  tag, provided it [supports this host](#requirements); this release's
  `install.sh` refuses an earlier release's binaries (exit 3).

For upgrade refusals, incomplete cutovers or partial restores, see
[refusal reasons](operator-actions.md#upgrade-refusal-reasons),
[cutover recovery](operator-actions.md#installsh-stops-with-errjournalmigrationincomplete)
and [kept-install recovery](operator-actions.md#putting-back-a-kept-install).

For refused files or failed migration writes, see
[config and template refusals](operator-actions.md#installsh-refuses-configtoml-or-a-template)
and [migration-write recovery](operator-actions.md#a-migration-write-failed-after-the-binary-swap).

For rollback, follow
[Going back to the previous release](operator-actions.md#going-back-to-the-previous-release).

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
and keeps the directory unless `--purge`. Like the install, it names old copies
of `settings.json` (see [Upgrade](#upgrade)). It leaves the MCP and help entries
made by hand in Codex folders; remove them as [Set up agent-director in a Codex
folder](settings.md#set-up-agent-director-in-a-codex-folder) says.

It also removes the schedule `--schedule` added and nothing else: the crontab
lines tagged `agent-director-schedule` (the crontab itself only when nothing
else is left in it), the tagged `agent-director-find-missing` and
`agent-director-expire` systemd user services and timers, and the
schedule files a piped install kept in `~/.config/agent-director/schedule/`
(the directory stays, with a note, when it holds other files). With no schedule
present it prints nothing about it. Cleanup recognizes the same markers and
unit names whether the recovery command is `sweep` or `find-missing`.

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
- Any other failure, a name registered in several scopes included: every later
  step still runs and `uninstall.sh` exits 6; see
  [The MCP registration or deregistration failed (exit 6)](operator-actions.md#the-mcp-registration-or-deregistration-failed-exit-6).
