# Multi-Account

Launching a Spawn against a different Claude or Codex account than the
operator's default, and selecting Cursor configuration/data directories.
Everything up to "Codex group folders" covers spawns whose agent is `claude`;
the later sections cover `codex` and `cursor` separately.
Selection uses agent-director's existing `extra_env` parameter; AD adds no
separate profile mechanism.
If `CLAUDE_CONFIG_DIR` is supplied via `extra_env`, it must be an
absolute path. The pretrust write then targets
`<CLAUDE_CONFIG_DIR>/.claude.json` instead of the operator's
`~/.claude.json`, and
`resume`, when it has to recompute a transcript's path, looks under
`<CLAUDE_CONFIG_DIR>/projects/`. A relative, `~`-prefixed or
whitespace-only value is not used by agent-director (Claude Code still
receives it in the pane's environment and resolves it against the
pane's cwd): pretrust writes nothing and reports `pre_trust: failed`
(the spawn still goes ahead, and the agent may stop at Claude Code's
folder-trust prompt), and `resume` recomputes under `~/.claude`
instead, so it does not find a transcript Claude Code wrote under the
relative dir unless the row's current `jsonl_path` still points at it.
Only the current session is tried: its recorded path first, then its path
recomputed from the current row's cwd and config directory after any stat
failure. No earlier session's path or config folder is a resume candidate.

Use an absolute `CLAUDE_CONFIG_DIR` for a separate Claude config. `HOME`
and malformed environment keys are refused; shared validation and recovery
are in [Spawn and template input refusals](caller-contract.md#spawn-and-template-input-refusals).
A same-agent reuse retains the life and earlier sessions, but starts a fresh
conversation; another config directory does not resume those sessions.
A switch starts a new life with no memory of the earlier life; switching
back does not recover the old one.

For Claude Code's own auth reference, see:

- API-key auth:
  <https://docs.claude.com/en/docs/claude-code/iam#anthropic-api-key>
- `claude setup-token` (long-lived OAuth tokens):
  <https://docs.claude.com/en/docs/claude-code/headless#setup-token>

## Two account types, two env vars

| Account type | Env var | Token shape |
| --- | --- | --- |
| API-key (`sk-ant-api...`) | `ANTHROPIC_API_KEY` | 108-char `sk-ant-...` |
| Max / OAuth (`sk-ant-oat01-...`) | `CLAUDE_CODE_OAUTH_TOKEN` | 108-char `sk-ant-oat01-...` |

Both environment variables select auth without the local `~/.claude.json`
auth cache.

## Passing the env var at spawn time

```
agent-director spawn \
  --cwd /work/project-foo \
  --agent claude \
  --extra-env ANTHROPIC_API_KEY=sk-ant-api-test-...
```

Or for a Max account:

```
agent-director spawn \
  --cwd /work/project-foo \
  --agent claude \
  --extra-env CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-...
```

Auth variables pass through to the pane; agent-director never logs their
values. It persists `extra_env` for resume, including auth values, in its
owner-only database. Templates persist their environment as plaintext TOML.
See [Configuration](configuration.md) for secrets at rest and
[Settings](settings.md#templates-and-per-call-merge) for template merging.

## Use `claude setup-token` for long-lived OAuth tokens

The token in `~/.claude-maxauth/.credentials.json` is *short-lived*
(`expiresAt` is ~9 hours after minting). Scraping it for use with
`CLAUDE_CODE_OAUTH_TOKEN` works but expires fast.

For production / CI use:

```bash
# On a trusted host, interactively:
claude setup-token
# → emits a long-lived sk-ant-oat01-... token

# Store the token securely (e.g. a CI secret), then pass at spawn time:
agent-director spawn \
  --cwd /work/project-bar \
  --agent claude \
  --extra-env CLAUDE_CODE_OAUTH_TOKEN="$LONG_LIVED_TOKEN"
```

This is the same pattern Claude's official GitHub Action uses.

## Parallel agents

Environment-variable auth does not give each spawn a separate Claude
configuration directory. Spawns sharing a home or `CLAUDE_CONFIG_DIR` share
that directory; it is not created inside each spawn's working tree. Use
separate absolute config directories when isolation is required.

## What the env var does NOT cover

- **Resuming a previously-authenticated session.** If a Spawn needs to
  read JSONL transcript history that was created by a specific account,
  the JSONL lives in `~/.claude/projects/<slug(cwd)>/` on the host
  filesystem — which is account-scoped. Env-var auth does not redirect
  this path. An absolute `CLAUDE_CONFIG_DIR` in `extra_env` does: the
  transcript is then under `<CLAUDE_CONFIG_DIR>/projects/<slug(cwd)>/`,
  and `resume` looks there (see above). Either way, the JSONL must still
  be on that disk path for `resume` to find it.
- **Concurrent operator + Spawn sessions on the same account.** Two
  Claude sessions sharing one OAuth token are fine for env-var auth
  (no contention) but share the same usage / rate-limit pool. Use
  separate accounts (one operator, one Spawn) for clean accounting.

## Codex group folders

A Codex agent keeps its login, its rules and its sessions in a Codex folder,
and `CODEX_HOME` moves it. Agents that share an account share a group folder.

- **`CODEX_HOME` is an ordinary `extra_env` key**, from the call or a template:

  ```sh
  agent-director spawn --cwd /work/project-foo --agent codex \
    --extra-env CODEX_HOME=/work/codex-team-a
  ```

  agent-director never sets or copies it. A spawn that names none leaves Codex
  to its default folder, and a parent agent's folder is not passed to a spawn
  it makes.
- **The value must be an absolute path.** A relative, `~`-prefixed or
  whitespace-only value is passed to Codex as it is, which resolves it against
  the pane's cwd. Pre-trust then reports `pre_trust: failed` with one warning,
  and the launch goes ahead (the agent may stop at Codex's folder-trust prompt).
- **Any number of agents may run on one folder at once.** agent-director takes
  no lock on it.
- **No agent-director command writes, moves, links or deletes anything in a
  Codex folder,** and agent-director never logs in or out. `spawn`, `pause`,
  `resume`, `kill` and `expire` leave the folder as they found it.
- **`resume` finds a Codex transcript only by the current session's recorded
  `jsonl_path`.** Earlier sessions are never tried. It
  never builds a path from the cwd and the session id, and never looks under a
  Claude config directory.

### Setting up a group folder

- **One login per folder.** Sign each folder in once, by hand, before an agent
  uses it (add `--device-auth` on a host with no browser). An API-key login is
  set up once in the folder the same way, with Codex's own key login:

  ```sh
  CODEX_HOME=/work/codex-team-a codex login
  printenv OPENAI_API_KEY | CODEX_HOME=/work/codex-team-a codex login --with-api-key
  ```
- **An API key in `extra_env` does not sign Codex in.** Codex reads its login
  only from its folder, so a key passed as `--extra-env` is ignored for signing
  in. Sign the folder in once with the key.
- **Never copy a login between folders.** A copy can sign the original out,
  because a refresh of one copy's token invalidates the other.
- **Some settings still apply from outside a folder:** the `~/.agents` skills
  and plugins, `/etc/codex`, the project's own `.codex/` and MCP tokens kept in
  the system keyring.
- **A first start on a new folder can be slower** than later ones, while Codex
  sets the folder up.
- **agent-director's MCP server and help entry are set up by hand in each
  group folder,** as in the default one (see [Set up agent-director in a Codex
  folder](settings.md#set-up-agent-director-in-a-codex-folder)).

## Cursor configuration and data directories

Use `CURSOR_CONFIG_DIR` and `CURSOR_DATA_DIR` through `extra_env` or a
[template](settings.md#templates-and-per-call-merge) to select Cursor's
configuration and data directories with the existing personal login. Each is
optional independently. For example, when that login is the intended account:

```sh
agent-director spawn --cwd /work/project-foo --agent cursor \
  --extra-env CURSOR_CONFIG_DIR=/work/cursor-config \
  --extra-env CURSOR_DATA_DIR=/work/cursor-data
```

A supplied directory must be nonempty, absolute and lexically clean, without
ASCII control bytes (0x00–0x1f or 0x7f). Relative paths, leading `~` or `$VAR`,
dot/dot-dot components, redundant separators and trailing separators other
than `/` are refused. Accepted values pass unchanged: `$` or `~` inside an
otherwise valid absolute path stays literal. AD does no expansion, filesystem
validation or directory creation.

A launch without explicit selection does not inherit these directories or
Cursor credential selectors from its caller, parent agent or tmux server.
[Resume](usage.md#pause-and-resume) retains the saved selection; finished-id
reuse takes the new resolved call/template selection and starts fresh.
`HOME` stays unchanged. Selecting these directories neither relocates the
personal login nor establishes credential, account or operating-system
isolation.

Multiple launches can select the same config/data directories at the same cwd,
or select distinct directory pairs there. Each retains its own launch and
conversation authority; shared directories remain shared Cursor storage.
Stopping one owned launch does not grant it authority over another launch or
an independent worker. A worker's survival or useful work does not establish
separate credentials, account isolation or permission-policy isolation.

The Cursor credential profile is unreleased. Explicit `CURSOR_API_KEY` or
`AGENT_CLI_CREDENTIAL_STORE`, including empty or incomplete selections, is
refused; there is no approved memory-store account recipe. Second-account
isolation, API-key exchange/renewal, expiry/invalid keys, personal-auth
preservation and cross-account workers remain unverified pending authorized
account acceptance. Cursor's protected gateway is Linux-only; macOS needs a
protected-runtime implementation before platform acceptance. Directory selection
does not establish those capabilities or policy isolation. Handling a structured
rejection does not establish that AD can reliably detect native malformed-policy
repair or silent fallback. Refusals are in the
[caller contract](caller-contract.md#spawn-and-template-input-refusals);
[fresh recovery](caller-contract.md#cursor-operation-boundaries) must preserve
caller-known supported account/configuration intent or stop for a human.

Saved `extra_env` remains in the owner-only database and is omitted from public
spawn views; template environment remains plaintext TOML. See
[Configuration](configuration.md) for storage protections and
[Settings](settings.md#templates-and-per-call-merge) for merging.

Cursor's local project rules can override its global lists. The retained Linux
`2026.10.12-d47ca18` observation for `Shell(printf)` found project deny blocked
global allow, and project allow overrode global deny. This is a limited
local-list observation: organization rules, hooks, other permission kinds,
configuration repair and full policy layering are unverified. A global/group
deny is not an immutable boundary against writable project policy, and a group
is not an operating-system security boundary.

An explicitly offered persistent pane approval may affect later requests using
the applicable shared Cursor policy; its storage and full scope are unverified.
AD's `decide` grants only a one-shot decision. The request and pane decision
mechanics are in [Cursor permissions](permissions.md#cursor-spawns).

Models available to the authenticated account are selected through normal
[agent arguments](usage.md#fresh-cursor-use), including template arguments.
Availability depends on that account and environment; AD supplies no model
catalog or availability guarantee. Cursor account/team usage follows
[Cursor's billing terms](https://cursor.com/docs/models-and-pricing).
An independently configured provider can bill the model cost directly;
[Cursor's provider-key terms](https://prod.cursor.com/help/models-and-usage/api-keys)
describe any additional applicable Cursor charges. This does not enable the
refused AD credential profile. AD adds no billing controls or account administration.

## References

- API-key auth:
  <https://docs.claude.com/en/docs/claude-code/iam#anthropic-api-key>
- `claude setup-token`:
  <https://docs.claude.com/en/docs/claude-code/headless#setup-token>
