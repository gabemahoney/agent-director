# Multi-Account

Launching a Spawn against a different Claude or Codex account than the
operator's default. Everything up to "Codex group folders" covers spawns whose
agent is `claude`; that section covers `codex`.
agent-director's `extra_env` parameter is the
single mechanism for this — no file mounts, no profile directories.
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

## References

- API-key auth:
  <https://docs.claude.com/en/docs/claude-code/iam#anthropic-api-key>
- `claude setup-token`:
  <https://docs.claude.com/en/docs/claude-code/headless#setup-token>
