# BREAKING: agent-director 0.12.0

<!-- Releaser: put this whole file at the top of the 0.12.0 release notes. -->

This is a **breaking** release. It adds Codex as a second agent next to Claude Code, and it renames every Claude-named name on agent-director's own surface, with no alias for the old names. It also moves agent-director's directory from `~/.agent-director` to `~/.config/agent-director`. Callers, scripts, templates and the config file that use an old name must change. Read the upgrade steps before you run the installer: the upgrade cannot be done by copying binaries, and a running agent breaks it.

## Required agents

Minimum supported versions: **Codex 0.159.2** and **Claude Code 2.1.296**. These are the installed endpoints used for this prerelease research; no version matrix was run.

## Prerelease limitations

This release handoff is **HELD** for safe final validation and the authoritative premerge schema comparison. Final Go/Bun and integration groups have not completed at the final revision. A host Go run was stopped after its real-home smoke guard read and printed real agent-director store tails despite a throwaway HOME. The owned logs were redacted; already emitted tool output cannot be retracted. No real mutation was established, and real-folder preservation was not independently proved. No further host tests were run.

- **NOT CHECKED LIVE:** sandbox-dependent shell, file-edit, running-command-input, MCP and network approvals; relay holds, cancellation, timeout and fallback dialogs; nested callers and sandbox environment/socket/store access. User namespaces are unavailable on this host, so no working Codex sandbox was established.
- **NOT CHECKED LIVE:** native Claude → Codex → Claude switch-back. No Claude login was used. Historical stand-in Docker tests cover this sequence; they are not a current final pass.
- **NOT CHECKED LIVE:** same-account continuity, signed-in behavior and actual remote compaction/help reinjection. Codex reads its login from CODEX_HOME; a disposable CODEX_HOME cannot use the existing login in place within the authorized isolation rules. No login or credentials were copied or used; billed turns were zero.
- **NOT CHECKED LIVE:** actual memory consolidation and `/review` as the first action, including their internal-thread hook ordering and row effects. Native subagent launch and the untracked empty view were observed with a local scripted model, without establishing every marked/unmarked hook case.
- Native `/new`, `/fork`, older-session picker selection, latest-session AD resume and the 64-visible-line pause were observed with a local scripted model. The prior product question about the native picker's place in AD's guarantee remains unresolved; this observation does not settle it.
- Default/Plan synchronous question controls were observed with suppression enabled and disabled. The installed Codex endpoint rejects forced asynchronous question calls as unsupported before hook execution; the asynchronous deny hook is **NOT CHECKED LIVE**.
- **NOT CHECKED LIVE:** wrapper/npm launcher ancestry, full shell and startup-file variants, every-tool matcher behavior, long runtime hook holds, broader replay/image/prompt/settings cases, signed-in timing/load guarantees and desktop command survival after Esc under `systemd --user`. Native listings match the historical trust corpus, but listings do not prove runtime duration or unavailable tool execution. macOS parity remains an assumption.
- Rollback is **NOT CHECKED BY DECISION**: the User chose roll forward and dropped rollback testing from acceptance. The existing way-back procedure below is retained unchanged; missing previous-release artifacts are no longer an acceptance hold.

## Upgrade steps

1. Upgrade only with this release's installer (`skills/install-agent-director/install.sh`), run from a shell agent-director did not start (not one inside an agent's session). Binaries copied by hand migrate no file: the directory stays where it was, `config.toml` and the templates are not migrated, and a template left with the old arguments key is refused at spawn.
2. No agent may run across the upgrade. Stop whatever starts agents, then every agent, every `agent-director serve` (a Claude Code session with agent-director's MCP server registered counts: end it) and every caller. The installer refuses with `ErrAgentsLive` while an agent row is live or a process runs the old binary.
3. Run the installer from the directory the original install ran in (or one inside the same git repository), so it finds a `claude mcp add` registration made there.
4. If your store is outside agent-director's directory (`[store] db_path`, including one whose `db_path` reaches it through a link inside the directory), copy `state.db`, `state.db-wal` and `state.db-shm` first. The upgrade migrates that store in place, and the way back needs the copy.
5. Before the upgrade, do not run this release's `make-template --store-path` or the Go client's `MakeTemplate`: they create `~/.config/agent-director/templates/`, and the installer then refuses with `ErrMoveConflict`, whose remedy covers it.
6. After the upgrade, restart every `serve` and every caller process, TypeScript clients included.

### The directory move

Every file agent-director kept in `~/.agent-director` now lives in `~/.config/agent-director`, with the same layout (`config.toml`, `templates/`, `state.db`, the trail, the errors log). The installer copies the old directory, checks the copy, rewrites `[store] db_path` and `[log] error_log_path` values that point into the old directory (a value that is inside it as written but reaches outside through a link inside it, or a relative value that points outside it, is rewritten to the absolute path it reaches today), and then renames the old directory to `~/.agent-director.pre-move-<YYYYmmdd-HHMMSS>`. That kept directory is your original, untouched, old binaries included. Delete it once the upgrade is confirmed (`uninstall.sh --purge` removes it too). Every upgrade from the previous release makes this move.

- `--keep-prior` makes no snapshot on this upgrade: the new `bin/` and `admin/` hold no earlier binary. The way back is the one below.
- With Claude Code present the installer swaps the help-hook entries in `~/.claude/settings.json` (old-directory entries out, new ones in; with `--no-hooks` it leaves the file and prints the old entries and the fix), and re-points a Claude Code MCP registration that names the old binary when you pass `--register-mcp` (without it, it prints the commands).
- Links and `$PATH` entries into the old `bin` directory: the installer re-points the PATH link it manages to the new binary, and prints every other such link and every such `$PATH` entry with its fix.
- It prints each line of `config.toml` or a template that still names the old directory (a `[launch]` value, an `extra_env` value, `cwd`) and leaves it as written.
- A link `~/.agent-director` that points to `~/.config/agent-director` is allowed, for scripts that still name the old path.

### Config and template migration

An install that finds a store older than this release's schema also migrates `config.toml` and every template, once. In `config.toml`, `[defaults] disable_askuserquestion` becomes `disable_ask_user`. In each template, a `claude_args` line becomes `agent_args`, and a template with no `agent` gets `agent = "claude"`. Each file it changes is first copied beside itself as `<file>.pre-agent-rename.<YYYYmmdd-HHMMSS>`. The migration is idempotent. A `config.toml` or template it cannot read safely, or a link it cannot write through, stops the install in pre-flight (exit 5, `ErrConfigMalformed` or `ErrTemplateMalformed`) with every file unchanged. A migration write that fails after the binary swap stops the install (exit 1) with the file as it was.

### Entries made by hand change by hand

The installer cannot find these. Change each to the new path:

- A `claude mcp add` registration the installer did not find (one made in another directory or scope).
- Help hooks in the `settings.json` of other `CLAUDE_CONFIG_DIR` folders.
- Codex MCP and help entries.
- Scripts or test setups that pass the old binary's path, for example to the TypeScript client's internal `_cliPath` hook or to its package check's `AD_CLI_PATH`. Neither is a user setting.

### TypeScript client floor

The TypeScript client's minimum installed CLI is now `0.12.0`. A client of this release refuses an older installed CLI with `ErrSystemInstallTooOld`, rather than sending names the old CLI does not know. Upgrade the CLI before the client.

### Installer fixes in this release

- Custom arguments, environment and unreadable MCP registrations are preserved even with `--register-mcp`; the installer adds no local entry that shadows them. Follow its manual advice with the original scope, arguments and environment.
- `--register-mcp` uses the supported command form on Claude Code 2.1.296. Before, it registered nothing and still exited 0, printing only `registration failed (continuing anyway)`. It now runs `claude mcp add agent-director -- <path> serve --stdio` and, when that add fails, reports the command, its exit status and its stderr and exits 6 after every other step has run.

## Every spawn names its agent

Every spawn must name its agent, `claude` or `codex`, per call (`--agent`, `agent`) or by template (`agent` in the template). A spawn with no agent is refused with `ErrAgentMissing`. A template migrated by the installer names `claude`.

## Renamed (old name, new name; no alias)

The old names are refused, not ignored.

| Surface | Old | New |
|---|---|---|
| CLI flag: `spawn`, `status`, `get`, `send-keys`, `read-pane`, `pause`, `resume`, `kill`, `decide`; `agent-director-admin kill-finished`, `delete` | `--claude-instance-id` | `--instance-id` |
| CLI flag: `make-template` | `--claude-args` | `--agent-args` |
| MCP parameter: `spawn`, `status`, `get`, `send-keys`, `read-pane`, `pause`, `resume`, `kill`, `decide` | `claude_instance_id` | `instance_id` |
| MCP parameter: `spawn`, `make-template` | `claude_args` | `agent_args` |
| Result field: `spawn`, `resume`, `get`, `list` rows | `claude_instance_id` | `instance_id` |
| Result field: `get` | `claude_args` | `agent_args` |
| Result field: `get` | `claude_session_id` | `session_id` |
| Result field: `get`'s `prior_sessions` elements | `claude_session_id` | `session_id` |
| TypeScript client keys (parameter and result types) | `claude_instance_id`, `claude_args`, `claude_session_id` | `instance_id`, `agent_args`, `session_id` |
| Go library fields (`pkg/api` types and the aliased `Spawn`, `RowSnapshot`, `LiveSpawnIdentity`, `ExpireCandidate`, `PermissionRow`, `SessionHistoryEntry`) | `ClaudeInstanceID` | `InstanceID` |
| Go library fields (same) | `ClaudeSessionID` | `SessionID` |
| Go library fields (`SpawnParams`, `MakeTemplateParams`, `SpawnRow`, `Spawn`) | `ClaudeArgs` | `AgentArgs` |
| Go method parameter names (`Client.Status`, `Client.Get`, `adminapi`) | `claudeInstanceID`, `claudeInstanceIDs` | `instanceID`, `instanceIDs` |
| `pkg/api/apitest` | `WithRawClaudeArgs` | `WithRawAgentArgs` |
| `pkg/api/apitest` raw store readers | `SpawnColumns.ClaudeArgs`, `SpawnColumns.ClaudeSessionID`, `HistoryEntry.ClaudeSessionID` | `SpawnColumns.AgentArgs`, `SpawnColumns.SessionID`, `HistoryEntry.SessionID` |
| `config.toml` key | `[defaults] disable_askuserquestion` | `[defaults] disable_ask_user` |
| Go config field (`internal/config`) | `Defaults.DisableAskUserQuestion` | `Defaults.DisableAskUser` |
| Template key | `claude_args` | `agent_args` |
| Trail field: every record that carries it (written after the upgrade) | `claude_instance_id` | `instance_id` |
| Trail field: every record that carries it (`ad.resume.moved_to_pending`, `ad.session.archive_failed`, `ad.session.jsonl_healed` and any other) | `claude_session_id` | `session_id` |
| AD's directory, in every default, description, message and doc | `~/.agent-director` | `~/.config/agent-director` |

Help text, descriptions and error advice that said "Claude Code" where either agent is meant now use agent-generic wording; text specific to one agent names that agent.

Unchanged: `AGENT_DIRECTOR_INSTANCE_ID`, `AGENT_DIRECTOR_RELAY_MODE`, `AGENT_DIRECTOR_LABEL_*`, `parent_id`, `jsonl_path`, `transcript_status`, `prior_sessions`, `relay_mode`, `inject_help_hook`, `no_pre_trust`, `reuse_finished`, `extra_env`, `labels`, `allow`/`deny`/`ask`, `--register-mcp`, `--no-hooks`, `--mcp-also`, `trail-emit --instance-id`, `hook_session_id`, `row_session_id`, `prior_session_id`, `new_session_id`, every existing error name, trail event name and trail reason, the arguments after `--` on `spawn` (they belong to the spawn's agent), and Claude Code's own `CLAUDE_CONFIG_DIR`, `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_API_KEY`, `.claude.json`, `~/.claude/settings.json`, `--settings`, `--resume` and `AskUserQuestion`.

## New

- The `agent` input: `--agent` on `spawn` and `make-template`, the `agent` MCP parameter, the `agent` template key, `Agent` in the Go library and `agent` in the TypeScript client. Rows record their agent: `get` and `list` rows carry `agent`.
- Go library: `EndReportedAtMillis` on `Spawn` (0 = no end report; it is not on `get`, `list`, the CLI, MCP or the TypeScript client). In `pkg/api/apitest`: `WithAgent`, `WithEndReportedAt`, `ConfigKeys.ClaudePath`, `ConfigKeys.CodexPath`, `SpawnColumns.Agent` and `SpawnColumns.EndReportedAt`. In `internal/config`: `Config.Launch`, with `ClaudePath` and `CodexPath`.
- `[launch] claude_path` and `[launch] codex_path` in `config.toml`, both optional: the program each agent launches (otherwise `claude` or `codex` from PATH). The installer never writes `[launch]`.
- `version.config_dir` in the `version` result of both binaries (Go `ConfigDir`, TypeScript `VersionResult`): the directory each binary keeps agent-director's files in. The installer refuses with exit 3 a pair that lacks it or names another directory, which is how it refuses an earlier release's binaries.
- New error names: `ErrAgentMissing`, `ErrAgentInvalid`, `ErrAgentArgsMismatch`, `ErrTemplateAgentMissing`, `ErrPermissionsUnsupported` and `ErrOldLayout` (agent-director's old directory is not yet moved: run this release's `install.sh`). Each has a TypeScript error class.
- The installer's own exit-5 names: `ErrMoveConflict`, `ErrAgentsLive`, `ErrMoveFailed` and `ErrTemplateMalformed`.
- `install.sh` and `uninstall.sh` stop at once, exit 2, with `HOME` unset, empty or relative.
- The installer needs at least one of Claude Code or Codex (found by its `[launch]` path or on PATH). Without Claude Code it skips the `settings.json` step and `--register-mcp`, with a message. It registers nothing with Codex and writes nothing into any Codex folder.
- Store schema: the store hops to schema version 9 on the first upgraded install (`agent`, `agent_args`, `session_id`, `instance_id` and `end_reported_at` columns; the `subagents` and `subagent_launches` tables, `spawns.subagent_tracking` and `subagents.life_number`). Subagent records are keyed by `(instance_id, life_number, subagent_id)`. There is no downgrade recipe; the way back below restores the copy.

- `list-subagents` on the CLI, `list_subagents` on MCP, Go `Client.ListSubagents` and TypeScript `client.listSubagents`: Claude subagent records across launches of the current life. Codex always returns `tracked: false` and `subagents: []`, including after reuse from Claude. A Claude row predating tracking becomes tracked on its next Claude resume or reuse. `subagent_id` is stable and unique among the records shown in one life; hidden earlier-life records still count toward retention.
- A finished id can be reused with either agent. Only an agent switch starts another life; switching back starts fresh too. AD-agent children remain listed by `list --parent`, independently of the parent's life. A failed reuse restore restores the prior agent, life, history and subagent visibility, but deleted permission requests stay deleted.
- Spawn result `dropped_args` (Go `SpawnResult.DroppedArgs`, TypeScript `SpawnResult.dropped_args`) reports removed original argument tokens in argv order on a switching reuse. It is omitted when empty. The CLI prints one stderr line when non-empty. Trail event `ad.spawn.reused` carries `dropped_args`, `null` when empty.
- Fixture names: `pkg/api/apitest.WithSubagentLife` and `SubagentEntryColumns.LifeNumber`; `SeedSubagent` defaults to the seeded row's current life.

## Meaning changes

- A same-agent reuse retains its life, `prior_sessions` and subagent records, while starting a fresh conversation. An agent switch hides every earlier-life session and record permanently from calls; switching back cannot resume the first agent's old sessions. Stored hidden records remain subject to the existing retention cap and row deletion.
- `resume` continues only the recorded agent's latest reported session. It never tries a history entry, even when its file exists. If the current transcript cannot be found, it refuses with `ErrJsonlMissing` (tries only `persisted` and Claude's current-session `fallback`, never `history`) or `ErrJsonlNeverWritten` under the empty-path and empty-visible-history condition. After same-agent reuse, a new session without a transcript can read `transcript_status: rotated` and give `ErrJsonlMissing`, where the previous release gave `never_written` and `ErrJsonlNeverWritten`.
- Every recognized existing-session selector is refused at spawn on a plain spawn or same-agent reuse, including inherited template arguments, and dropped on a verified finished-agent switch, which starts fresh without an error. `make-template` stores `agent_args` unchanged; selector validation happens at spawn. Claude selectors include `--resume` / `-r`, `--continue` / `-c`, `--fork-session`, `--from-pr`, `--teleport`, `--resume-session-at`, `--resume-drops-turn`, `--rewind-files`, `--reply-on-resume`, `--cloud`, `--remote` and `--attach-serve`. Codex selectors include command-position `resume` / `fork` and their `--last`, `--all` and resume-only `--include-non-interactive`. Claude print (`--print` / `-p`, parsed clusters included) is always refused at spawn. Exact denied long names used as another option's value are refused, never dropped. Claude, Codex, then Claude with `--resume` on one id starts a fresh Claude conversation and reports the removed selector; it never resumes the first Claude session.
- `resume` removes recognized selectors and their owned values, and Claude print options, from the arguments it replays; stored arguments are unchanged. Claude `--forward-home-settings`, `--ref` and `--on-branch` and their values are removed only when removing an actual cloud/remote selector leaves no parsed environment/pool context with its required value. Complete Claude inputs preserve surviving parsed roles, repairing spelling or prompt placement only where literal removal would change their meaning. Inputs missing a recognized required value receive literal filtering only, with no repair. `dropped_args` reports removals, not rendering changes.
- The guarantee covers agent-director's launches, not an agent's own in-session picker. Recognition covers the supported parser options; a newer agent option can escape it until the option table is updated. Whether the agent can reopen its latest session remains up to that agent.

## The way back

Use this when the upgrade must be undone. Whatever was written since the upgrade is lost: the previous release reads the directory as it was before the upgrade. The previous release's schema cannot read this release's store, so this is the only way back; a binary swap, `--keep-prior` or a downgrade of the store does not work. The full steps, with the commands, are in [Going back to the previous release](https://github.com/gabemahoney/agent-director/blob/main/docs/operator-actions.md#going-back-to-the-previous-release). In order:

1. Stop whatever starts agents, then every agent, every `serve` and every caller.
2. Set the new directory aside: rename `~/.config/agent-director` to `~/.config/agent-director.after-upgrade-<YYYYmmdd-HHMMSS>`.
3. If `~/.agent-director` exists again (something recreated it since the upgrade), set it aside too: rename it to `~/.agent-director.recreated-<YYYYmmdd-HHMMSS>`. Then rename the newest kept `~/.agent-director.pre-move-*` back to `~/.agent-director`. It holds the store, `config.toml` and templates as they were before the upgrade.
4. A store kept outside agent-director's directory (`[store] db_path`) was migrated in place by the upgrade. Put back the copy made before the upgrade: `state.db` with its `-wal` and `-shm`, deleting a `-wal` or `-shm` the copy lacks. Skip this when the store was inside the directory.
5. In `~/.claude/settings.json`, remove only the entries that hold a hook command starting with `~/.config/agent-director/bin/agent-director` (written out with your home directory). Leave every other entry. Check the result is valid JSON.
6. From the directory the upgrade ran in (for a `local` registration made inside a git repository, any directory of that repository), remove agent-director's MCP registration with Claude Code, if there is one, in the scope `claude mcp get agent-director` shows (once per scope when a name is registered in several).
7. From the same directory, install the previous release with that release's own `install.sh`, fetched from its tag, with the flags the original install used (`--register-mcp`, `--no-hooks`, `--symlink-dir` and the rest). It puts back its help hooks. Its `--register-mcp` registers nothing with the current Claude Code (it prints `registration failed (continuing anyway)`), so when step 6 showed a registration, add it back by hand, in the scope step 6 showed (`local`, `user` or `project`), with the old path:

   ```sh
   claude mcp add agent-director -s <scope> -- ~/.agent-director/bin/agent-director serve --stdio
   claude mcp get agent-director
   ```

   This release's `install.sh` cannot install the previous release: it refuses an earlier release's binaries.
8. Change back by hand what was changed by hand to the new path after the upgrade: help hooks in the `settings.json` of other `CLAUDE_CONFIG_DIR` folders, Codex MCP and help entries, scripts, `$PATH` entries and an MCP registration made by hand.
9. Start the caller's previous version.

Delete the `after-upgrade` and `recreated` directories once the previous release is confirmed working; `uninstall.sh --purge` removes them.
