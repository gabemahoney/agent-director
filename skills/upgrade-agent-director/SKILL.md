---
name: upgrade-agent-director
description: Upgrade or re-install agent-director. Classify by layout files first; a versioned install upgrades file-only with agents running. An exempt install retains operator-authorized stops, migration, verification and restart. Use when asked to upgrade, update or re-install an existing install. For a first install, use install-agent-director.
---

# Upgrade agent-director

You can upgrade directly to this release from any earlier agent-director
release starting at 0.3.0; no intermediate release is needed. Releases 0.1.0–0.2.3
(`claude-director`, `~/.claude-director`) cannot be upgraded by this installer.
See [Upgrade](https://github.com/gabemahoney/agent-director/blob/main/docs/install.md#upgrade)
for rules on installing an earlier release or opening a newer store.

Follow the steps in order, as the agents' user. The commands name an install
in `~/.config/agent-director` (0.12.0 onward; no directory move). For
0.3.0–0.11.1 in
`~/.agent-director` ([the old directory](https://github.com/gabemahoney/agent-director/blob/main/skills/install-agent-director/SKILL.md#upgrading-an-install-from-the-old-directory)),
use that directory in exempt steps 1 and 2, and do that section's "Before you
run" steps with the operator before step 3's one-time move.

## Choose the upgrade path first

Follow the [install policy](https://github.com/gabemahoney/agent-director/blob/main/docs/install.md#choose-the-upgrade-path).
Before any process exclusion, MCP/session check, config-based store resolution,
schema copy/read or runtime command, classify using files only:

```sh
ad_upgrade_root="$HOME/.config/agent-director"
ad_upgrade_path=exempt
for ad_entry in install-contract.json install-initialization.json current versions publications install.lock; do
  if [ -e "$ad_upgrade_root/$ad_entry" ] || [ -L "$ad_upgrade_root/$ad_entry" ]; then
    ad_upgrade_path=file-only
  fi
done
ad_upgrade_clean_path() {
  local rest="$1" part clean=
  while [ -n "$rest" ]; do
    part=${rest%%/*}
    if [ "$part" = "$rest" ]; then rest=; else rest=${rest#*/}; fi
    case "$part" in
      ''|.) ;;
      ..) clean=${clean%/*} ;;
      *) clean="$clean/$part" ;;
    esac
  done
  printf '%s\n' "${clean:-/}"
}
ad_upgrade_root=$(ad_upgrade_clean_path "$ad_upgrade_root")
for ad_entry in "$ad_upgrade_root/bin/agent-director" "$ad_upgrade_root/admin/agent-director-admin"; do
  if [ -L "$ad_entry" ]; then
    ad_target=$(readlink "$ad_entry") || exit 1
    case "$ad_target" in /*) ;; *) ad_target="${ad_entry%/*}/$ad_target" ;; esac
    ad_target=$(ad_upgrade_clean_path "$ad_target")
    case "$ad_target" in
      "$ad_upgrade_root/current"|"$ad_upgrade_root/current/"*|"$ad_upgrade_root/versions"|"$ad_upgrade_root/versions/"*) ad_upgrade_path=file-only ;;
    esac
  fi
done
printf '%s\n' "$ad_upgrade_path"
```

Any evidence, dangling links included, selects file-only validation/recovery
or `layout_invalid`. Missing/corrupt `current` or old-script regular-file entry
overwrites never justify falling back to the exempt path while evidence remains.
Do not inspect a DB or infer its age from config to change this decision.

### Choose the release

Choose a layout-aware release tag once for this upgrade and reuse it for every
re-run. To find the latest, run this read-only command:

```sh
curl -fsSLI -o /dev/null -w '%{url_effective}\n' https://github.com/gabemahoney/agent-director/releases/latest
```

If curl fails or its URL does not end in `/releases/tag/<tag>`, stop and
hand the operator the output unless they provide a release tag. Replace both
`<tag>`s in the install command with that tag: installer and binaries must
come from the same release.

### File-only upgrade

When classification prints `file-only`, run this branch and finish here.
It may run from an AD-managed session or one using AD MCP tools; no process,
agent, caller or `serve` must stop or resume. Retain the original install's
flags and any the operator requests, appended after `upgrade-agent-director`;
the outer `pipefail` catches download failure and `"$@"` forwards the flags.

```sh
bash -o pipefail -c 'curl -fsSL https://raw.githubusercontent.com/gabemahoney/agent-director/<tag>/skills/install-agent-director/install.sh | bash -s -- --from-release <tag> "$@"' upgrade-agent-director
```

On exit 0 verify using files and the store-free version command only:

```sh
~/.config/agent-director/bin/agent-director version
readlink ~/.config/agent-director/current
readlink ~/.config/agent-director/bin/agent-director
readlink ~/.config/agent-director/admin/agent-director-admin
```

Check the chosen stamp, a `versions/<id>` selection, and canonical entry targets
`../current/agent-director` and `../current/agent-director-admin`. This makes no
claim about the store's schema. Direct admin inspection or rollback is human-run
or requires explicit human approval for that individual run; prepare the full
command from [versioned-install recovery](https://github.com/gabemahoney/agent-director/blob/main/docs/operator-actions.md#recovering-a-versioned-install).
The installer's internal admin call is part of the authorized install.

On exit 5, read `details.reason`, `publication_state`, `durable` and recovered
facts using that procedure, not the journal's `details.refusal`/`cutover` shape.
Unknown or unreadable output proves no rollback: preserve it and rerun the
same install for forward recovery once the reported cause is corrected.
Do not invent a reason or restore old files. Exit 3 for missing version-command
support needs a compatible candidate. On exit 6, installation completed but MCP
registration failed: hand its stderr and the
[MCP remedy](https://github.com/gabemahoney/agent-director/blob/main/docs/operator-actions.md#the-mcp-registration-or-deregistration-failed-exit-6)
to the operator, then perform the file-only verification above and finish.
Other failures go to the operator with their output.

No failure or verification here authorizes `list`, a store/sidecar snapshot or
header read, holder/process inventory, `pause`, `kill`, `sweep`, `find-missing`,
`resume`, a wake or input. An ordinary later upgrade finishes here. If this
branch was invoked by [an original exempt publication-failure continuation](#publication-failure-after-an-exempt-install),
return to that continuation after file-only verification; it retains the
original exempt settlement obligations. Do not start the generic exempt steps.

### Exempt upgrade

Run only one installer at a time throughout the exempt attempt and its
verification, recovery or restore settlement. Keep that exclusion until the
attempt is settled; the publication lock does not serialize exempt migration.

Only classification `exempt` proceeds through steps 1–6. Keep all their
authorization, same-user/tmux and every-agent restart requirements. An older
candidate without version commands can install here without creating a layout;
the next current-release install is exempt again.

## 1. Stop the agents

The upgrade runs only from a shell or agent that agent-director did not start.
agent-director sets
[`AGENT_DIRECTOR_INSTANCE_ID`](https://github.com/gabemahoney/agent-director/blob/main/docs/usage.md#drive-a-spawn-from-another-agent)
for every Claude, Codex and Cursor agent it starts:

```sh
if [ -n "${AGENT_DIRECTOR_INSTANCE_ID:-}" ]; then echo "started by agent-director"; fi
```

If it prints `started by agent-director`, stop here and hand the upgrade to the
user or to a session agent-director did not start, saying why: your own row is
live, so the install refuses, and your own hooks would run the new binary
mid-upgrade. Do the same if this session has agent-director's MCP tools: their
`agent-director serve` must stop too.

### Read the store schema

Before step 3, resolve `[store] db_path` from the installed `config.toml`
as [Which database install.sh checks](https://github.com/gabemahoney/agent-director/blob/main/skills/install-agent-director/SKILL.md#which-database-installsh-checks)
specifies: an unset or empty value uses `~/.config/agent-director/state.db`
(`~/.agent-director/state.db` for 0.3.0–0.11.1); `~/` expands against `HOME`;
an absolute path stays as written; every other value is relative to the
install directory (`~/.config/agent-director`, or `~/.agent-director` for
0.3.0–0.11.1), never the shell's working directory. Do not expand `$VAR`.
Resolve to an absolute path before checking whether the store file exists.
If there is no store file, record 0. Otherwise replace `<store>` with that
resolved absolute path, quoted, and read a private copy to leave the store
folder unchanged:

```sh
(
  umask 077
  copy=$(mktemp -d "${TMPDIR:-/tmp}/ad-schema.XXXXXX") || exit 1
  trap 'rm -rf "$copy"' EXIT
  if [ -f <store>-wal ]; then
    cp <store>-wal "$copy/state.db-wal" || exit 1
  fi
  cp <store> "$copy/state.db" || exit 1
  sqlite3 -batch -init /dev/null "$copy/state.db" 'PRAGMA user_version;'
)
```

If the command fails or does not print one nonnegative integer, stop and
hand the operator the output. Versions 1–15 cross the native-input journal
cutover; 0 and versions 16 or above do not. Keep this result for steps 3 and
4, even after the install changes the schema version.

### Check the agents and holders

List the live rows:

```sh
~/.config/agent-director/bin/agent-director list --state pending,waiting,working,check_permission,ask_user
```

Keep each live row's id and `parent_id` from this output for step 6, before
stopping any row. An empty `parent_id` means no parent.

If it prints `{"spawns":[]}`, skip the checks below and ask the operator.
Otherwise, while the agents still run, check that this shell is in their
environment. Each must hold:

- `id -un` prints the owner that `ls -ld ~/.config/agent-director` shows, and
  not `root`.
- `tmux list-sessions` shows the `tmux_session_name` of each live row whose
  agent still runs. A live row whose session is not there is one for step 2's
  sweep to clear, not a failed check. If no live row's session is there, ask
  the operator whether any of those agents still runs: if one does, this check
  fails.
- For a live row whose session is there,
  `tmux list-panes -a -F '#{session_name} #{pane_pid}'` gives its pid, and
  `ps -p <pid>` prints that process.

If one fails, this shell is another user's, root's, or on another container,
tmux server or PID namespace, where a sweep can mark running agents `missing`
([why](https://github.com/gabemahoney/agent-director/blob/main/docs/maintenance.md#scheduling-the-sweeps)).
Move to the agents' own shell and tmux environment and start step 1 again.

Ask the operator to stop whatever starts agents (a scheduler, a loop, a
caller), every `agent-director serve` (each session with agent-director's MCP
tools) and every caller, as
[Stopping a set of agents before a binary change](https://github.com/gabemahoney/agent-director/blob/main/docs/operator-actions.md#stopping-a-set-of-agents-before-a-binary-change)
says, and to confirm that you may stop each live row listed. If they say no,
stop here and change nothing. Once they confirm, check that no `serve` runs:
this prints nothing.

```sh
ps -Aww -o pid= -o ucomm= -o args= |
  awk '$2 == "agent-director" {
    pid = $1
    sub(/^[[:space:]]*[0-9]+[[:space:]]+agent-director[[:space:]]+/, "")
    if ($0 ~ /^[^[:space:]]+[[:space:]]+serve([[:space:]]|$)/ ||
        $0 ~ /^.*\/agent-director[[:space:]]+serve([[:space:]]|$)/)
      print pid, $0
  }'
```

The `ucomm` column reads the kernel command name on Linux and macOS;
macOS's `comm` instead reads argv[0]. `agent-director` is 14 characters,
within Linux's 15-character and macOS's 16-character name limits. Only
after that kernel-name match do arguments select the `serve` verb, including
a renamed argv[0] or an executable path containing spaces. `-ww` keeps long
arguments from being truncated. A tmux server or other program whose
arguments merely mention `agent-director serve` does not count. If a `serve`
is listed, stop here and have the operator close it before starting step 1
again.

Only when the schema read above says this upgrade crosses the cutover,
before stopping any row, first confirm with the operator that steps 1 and 2 of
[Upgrading the native-input journal](https://github.com/gabemahoney/agent-director/blob/main/docs/operator-actions.md#upgrading-the-native-input-journal)
hold. The journal flags assert the operator's maintenance window. If they
do not confirm, stop here. Then run this read-only holder check from the
chosen release:

```sh
bash -o pipefail -c 'curl -fsSL https://raw.githubusercontent.com/gabemahoney/agent-director/<tag>/skills/install-agent-director/install.sh | bash -s -- --from-release <tag> "$@"' upgrade-agent-director --check-holders
```

The outer Bash enables `pipefail`, so a failed download fails the command
even if no script reaches the inner Bash. Append flags after
`upgrade-agent-director`; `"$@"` forwards them to the installer.

For a crossing upgrade, append
`--journal-maintenance-window --journal-roots-complete --journal-backup <new-absolute-directory>`
and add `--journal-legacy-root <dir>` for each additional input root.
Keep exactly these journal flags for the second holder check and step 3's
install. Include the original install's other flags and any the operator
adds now in both checks and the install too.
Exit 5: show the operator the output, including the listed processes, and stop.
No row has been stopped. On any other non-zero exit, stop and hand the
operator the output. Exit 0: continue below.
With no crossing, skip this confirmation and both holder checks, and pass
no journal flags.

With no live row, go to step 3. Otherwise stop each one:
`pause` a `waiting` row; `kill` any other live row, or one `pause`
refuses.

For 0.3.0–0.11.1, `pause` and `kill` take `--claude-instance-id` instead of
`--instance-id`; `list` in steps 1–2 names the id `claude_instance_id`.

```sh
~/.config/agent-director/bin/agent-director pause --instance-id <id>
~/.config/agent-director/bin/agent-director kill --instance-id <id>
```

## 2. Sweep

From the same shell, run the installed release's `find-missing`, then list
again:

```sh
~/.config/agent-director/bin/agent-director find-missing
~/.config/agent-director/bin/agent-director list --state pending,waiting,working,check_permission,ask_user
```

`find-missing`'s `ids` names the rows it marked `missing`; a row `kill` ended
stays live until then. Repeat both about 5 s apart, at most three times, until
`list` prints `{"spawns":[]}`. Go to step 3 only then.

A row still live goes back to step 1: either its agent still runs (step 1
stops it, the operator confirming), or the sweep ran in the wrong place
(step 1's checks find it; then sweep from the agents' environment). Never
mark, delete or edit rows by hand.

## 3. Install

For a crossing upgrade, after step 2's sweep and just before installing,
repeat step 1's `--check-holders` command with the chosen tag and exactly
the same journal flags. Exit 5: show the operator the output, including the
listed processes, and stop. On any other non-zero exit, stop and hand the
operator the output. With no crossing, skip this check.

For this exempt upgrade, from this shell:

```sh
bash -o pipefail -c 'curl -fsSL https://raw.githubusercontent.com/gabemahoney/agent-director/<tag>/skills/install-agent-director/install.sh | bash -s -- --from-release <tag> "$@"' upgrade-agent-director
```

Use step 1's argument-forwarding rule here too. If the download fails,
stop and hand the operator the output.

For a crossing upgrade, append exactly the journal flags kept in step 1.
Otherwise pass no journal flags.
Add the original install's other
[flags](https://github.com/gabemahoney/agent-director/blob/main/docs/install.md#install-from-a-release),
plus any the operator adds now, such as `--register-mcp`.
Exit 0: step 4. Exit 5 or 6: step 5. If the cause is
`ErrVersionInstallRefused`, use its exempt publication continuation there.
Keep step 1's original schema result, recorded parents, stopped writers and
flags across retries; do not replace them with a post-conversion schema read.
Another exit:
[When install.sh fails](https://github.com/gabemahoney/agent-director/blob/main/skills/install-agent-director/SKILL.md#when-installsh-fails).

## 4. Verify

This is verification of the original exempt install, not of a file-only retry.
After an exempt publication failure, use the separate continuation in step 5;
do not route a file-only retry through this section's `list` command.

```sh
~/.config/agent-director/bin/agent-director version
~/.config/agent-director/bin/agent-director list
```

`version` shows the release just installed, and `list` exits 0. If the schema
read before step 3 said this upgrade does not cross the cutover, verification
is complete: go to step 6. Otherwise prepare the administrator's
[verification](https://github.com/gabemahoney/agent-director/blob/main/docs/operator-actions.md#upgrading-the-native-input-journal),
filled in with the store's resolved absolute path under the rules above
(using the installed config after any old-directory move), a new backup
directory and step 3's `--journal-legacy-root` list as `--legacy-root`:

```sh
~/.config/agent-director/admin/agent-director-admin --store-path <store> migrate-journal --maintenance-window --roots-complete --backup-dir <new-absolute-directory>
```

`agent-director-admin` runs only with a human's approval for that run
([Operator actions](https://github.com/gabemahoney/agent-director/blob/main/docs/operator-actions.md)):
show the operator the filled-in command, and run it only once they approve
that run; otherwise they run it. It must exit 0 with `"authority": "sqlite"`.
An error goes to step 5.

## 5. On a refusal

First distinguish `ErrVersionInstallRefused` (or interrupted/unreadable
publication output) from a journal/store failure. For publication after this
exempt attempt, follow the subsection below. Its `details.reason` and
publication facts do not use `details.refusal` or `cutover`.

`install.sh`'s last stderr line names the cause, `install.sh: err_name=<Name>`;
`agent-director-admin`'s JSON envelope names it as `err_name`. The envelope
gives `details.refusal.reason` and `action`. Find that reason's row in
[Upgrade refusal reasons](https://github.com/gabemahoney/agent-director/blob/main/docs/operator-actions.md#upgrade-refusal-reasons),
or, with no reason, the `err_name`'s row in
[Exit 5's cause line](https://github.com/gabemahoney/agent-director/blob/main/skills/install-agent-director/SKILL.md#exit-5s-cause-line).
Show the operator the output (the envelope, when there is one) and that row:
the operator performs its fix,
up to its re-run of the install or its verification, which stay yours. Then
resume at that step: step 3 to re-run the install, or step 4 for the
verification, under that step's rules.

For `agents_live` or `ErrAgentsLive`, instead, the row's stops are step 1's
and its `find-missing` is step 2's: do those steps, with step 1's checks and
the operator's confirmation, then step 3.

On exit 6, the install finished but the MCP registration did not: tell the
operator so, hand them its stderr to register by hand, as
[its row](https://github.com/gabemahoney/agent-director/blob/main/skills/install-agent-director/SKILL.md#when-installsh-fails)
says, then go on with step 4 and step 6.

For a row that leads to neither step, or an `err_name` or reason with no row,
stop and hand the operator the output.

### Publication failure after an exempt install

Keep the original exempt attempt open: all stopped writers and launchers stay
excluded, along with a second installer. Retain its pre-install schema result,
resolved store path (updated for a completed directory move), recorded parent
links, chosen tag and flags. The installer reached publication only after its
store setup and schema verification settled; publication failure does not mean
the store open failed or its migration authorization was unconsumed.

Use [versioned-install recovery](https://github.com/gabemahoney/agent-director/blob/main/docs/operator-actions.md#recovering-a-versioned-install)
for the envelope's `details.reason`, requested `publication_state`, `durable`,
`published_version`, `recovered_version` and initialization facts. Unknown or
unreadable output proves no rollback. After correcting the cause, run
[the file classifier](#choose-the-upgrade-path-first) again before retrying:

- `file-only`: rerun the same tag/pair/flags using **File-only upgrade** and
  verify only its version/layout facts. That retry performs no store read,
  holder inventory, stop, sweep or resume. Instead of ending the original
  exempt workflow, return here after that branch succeeds.
- `exempt`: evidence was not established. With the original exclusion still
  maintained, rerun step 3 using the same tag and flags. On success return
  here, retaining the original schema result.

Exit 6 after recovery keeps the installation: give the MCP remedy to the
operator, perform the selected branch's file-only version/layout checks and
return here. Another failure stays in its branch's recovery; it does not
authorize a store-opening fallback.

Once publication and entry initialization complete, finish the **original
exempt settlement** separately. Do not run step 4's generic `list` block for
the file-only retry. Store verification already completed before the original
publication attempt. If the saved original schema result was 0 or at least 16,
that exempt verification is complete. If it was 1–15, prepare only the
original journal verification, with its saved resolved store and declared
roots (repeat `--legacy-root` for each additional root):

```sh
~/.config/agent-director/admin/agent-director-admin --store-path <store> migrate-journal --maintenance-window --roots-complete --backup-dir <new-absolute-directory>
```

Show the filled-in command to the operator for explicit approval for this
individual run, or let them run it. This is settlement of the original exempt
cutover, not verification of the file-only retry. It must exit 0 with
`"authority": "sqlite"`; a refusal follows the journal procedure while writers
remain excluded. Then perform step 6 using the original recorded parents:
restart every stopped agent, `serve`, caller and launcher, parents before
children. A file-only retry never cancels those exempt restart obligations.

## 6. Resume the agents

Ask the operator to restart whatever starts agents, every `serve` and every
caller
([after the upgrade](https://github.com/gabemahoney/agent-director/blob/main/docs/install.md#upgrade)),
then bring back each agent stopped in step 1, parents before children. If a
row's recorded parent was also stopped in step 1, attempt that parent's
resume first.

Every agent must restart on the exempt release, including any still running
from before the install; do not carry an old launch across the exempt hop.

With no recorded parent, use the first resume command below. With a recorded
parent, check its row first:

```sh
~/.config/agent-director/bin/agent-director get --instance-id <parent>
```

If `get` exits 0, use the second resume command, with that recorded parent,
whatever its state. If it returns `ErrSpawnNotFound`, tell the operator:
`<id>`'s parent `<parent>` no longer has a row; it will resume with no parent.
Use the first command. On any other error, stop and hand the operator the
output.

```sh
~/.config/agent-director/bin/agent-director resume --instance-id <id>
AGENT_DIRECTOR_INSTANCE_ID=<parent> ~/.config/agent-director/bin/agent-director resume --instance-id <id>
```

Set the variable for that command only; never export it, so the shell still
passes step 1's guard and later resumes use their own recorded parent.
Keep failed resume commands and their output; continue to their children.
If a parent's resume failed, use the second command and tell the operator:
`<id>`'s parent `<parent>` resume failed; its parent link was restored.
Include that command, its output and the parent's state from `get`. Otherwise,
only if the parent's `get` shows `ended` or `missing`, tell the operator:
`<id>`'s parent `<parent>` is not running; its parent link was restored.
After a create timeout (`ErrTmuxUnresponsive`), do not retry `resume` until
`get` shows the row `ended` or `missing`.
