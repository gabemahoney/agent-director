# Multi-Account

Use `extra_env` directly or through a
[template](settings.md#templates-and-per-call-merge) to select another agent
account or configuration directory. agent-director adds no profile manager.
`HOME` remains unchanged; use the agent-specific settings below.

## Claude accounts

A spawn uses `extra_env.CLAUDE_CONFIG_DIR` when present (empty means unset),
otherwise agent-director's own `CLAUDE_CONFIG_DIR`. A selected value is saved;
`resume` uses that saved selection, not the resuming process's environment.

Use an absolute directory. Pre-trust uses
`<CLAUDE_CONFIG_DIR>/.claude.json`, and transcript lookup uses
`<CLAUDE_CONFIG_DIR>/projects/`. Without a selection, pre-trust uses
`~/.claude.json` and the pane uses tmux's environment, except a fresh launch
with [ordinary delivery settings](settings.md#ordinary-delivery-launch-settings)
receives `CLAUDE_CONFIG_DIR=$HOME/.claude`.

```sh
agent-director spawn --cwd /work/project-foo --agent claude \
  --extra-env CLAUDE_CONFIG_DIR=/work/claude-team-a
```

Environment-variable authentication does not isolate configuration or move
transcripts. Spawns sharing a config directory share its files; choose
separate absolute directories when needed. Accounts also share their own
usage and rate-limit pool.

## Two account types, two env vars

| Claude account type | `extra_env` key |
| --- | --- |
| API key | `ANTHROPIC_API_KEY` |
| Max / OAuth | `CLAUDE_CODE_OAUTH_TOKEN` |

These select authentication without the local auth cache. For provider
details, see [Claude authentication](https://docs.claude.com/en/docs/claude-code/iam#anthropic-api-key).

## Passing the env var at spawn time

```sh
agent-director spawn --cwd /work/project-foo --agent claude \
  --extra-env ANTHROPIC_API_KEY="$ANTHROPIC_API_KEY"
```

For OAuth, use `--extra-env CLAUDE_CODE_OAUTH_TOKEN="$LONG_LIVED_TOKEN"` instead.

Auth values are not logged. Saved `extra_env`, including auth values, remains
in the owner-only database for resume and is omitted from public spawn views.
Template environments are plaintext TOML; see [Configuration](configuration.md)
for storage protections.

## Use `claude setup-token` for long-lived OAuth tokens

Create a long-lived token on a trusted host with `claude setup-token`, store
it securely, then pass it as `CLAUDE_CODE_OAUTH_TOKEN`. Do not substitute the
short-lived token from a credentials file. See
[Claude's setup-token reference](https://docs.claude.com/en/docs/claude-code/headless#setup-token).

`resume` needs the current session's transcript on disk; changing an auth
variable does not relocate it. Same-agent reuse starts a fresh conversation
while keeping inspectable history. Switching agents starts a new life, and
switching back does not restore the prior conversation. See
[Transcript recovery](caller-contract.md#transcript-recovery).

## Codex group folders

Use an absolute `CODEX_HOME` to select a folder holding Codex's login, rules
and sessions:

```sh
agent-director spawn --cwd /work/project-foo --agent codex \
  --extra-env CODEX_HOME=/work/codex-team-a
```

agent-director neither sets nor copies `CODEX_HOME`. Without an explicit
selection, Codex uses the tmux server's environment and its normal default;
a parent agent's folder is not automatically passed to its children.
Multiple agents can share a folder; agent-director takes no lock and does
not manage its files or login.

### Setting up a group folder

Create the folder outside the system temp directory and sign it in once,
by hand. Add `--device-auth` on a host without a browser:

```sh
CODEX_HOME=/work/codex-team-a codex login
printenv OPENAI_API_KEY | CODEX_HOME=/work/codex-team-a codex login --with-api-key
```

An API key passed only through `extra_env` does not sign Codex in.
Never copy login credentials between folders: refreshing one copy can
invalidate the other.

A separate folder does not isolate `~/.agents` skills and plugins,
`/etc/codex`, project `.codex/` settings or MCP tokens in the system keyring.
The first start can take longer while Codex prepares the folder.

Add agent-director's MCP and help entries
[in each folder](settings.md#set-up-agent-director-in-a-codex-folder).
Launch trust is described in [Pre-trust for Codex](settings.md#pre-trust-for-codex).
Resume uses only the current session's recorded transcript path; see
[Transcript recovery](caller-contract.md#transcript-recovery).

## Cursor configuration and data directories

Directory selection uses the existing personal login; it does not select a
second account. Pass `CURSOR_CONFIG_DIR` and `CURSOR_DATA_DIR` independently
through `extra_env` or a template:

```sh
agent-director spawn --cwd /work/project-foo --agent cursor \
  --extra-env CURSOR_CONFIG_DIR=/work/cursor-config \
  --extra-env CURSOR_DATA_DIR=/work/cursor-data
```

Each supplied path must be nonempty, absolute and lexically clean, without
ASCII control bytes (0x00–0x1f or 0x7f). Do not use a leading `~` or `$VAR`,
dot/dot-dot components, redundant separators or trailing separators other
than `/`. Accepted values pass unchanged; an internal `$` or `~` stays
literal. agent-director does not expand, validate on disk or create them.

A launch does not inherit these directories or Cursor credential selectors
from its caller, parent or tmux server. Resume retains the saved selection;
finished-id reuse takes the new call/template selection and starts fresh.

Shared directories remain shared Cursor storage. Separate directories do not
relocate the personal login or establish credential, account, permission-policy
or operating-system isolation. Local project policy can override global lists;
a global/group deny is not an immutable boundary against writable project policy.
Persistent pane approvals can affect later requests under shared policy;
their storage and full scope are unverified. See
[Cursor permissions](permissions.md#cursor-spawns).

The Cursor credential profile is unreleased. Explicit `CURSOR_API_KEY` and
`AGENT_CLI_CREDENTIAL_STORE` are refused, including empty selections. There is
no supported second-account or memory-store recipe. Do not remove intended
account selection just to obtain a personal-login launch; follow
[input refusals](caller-contract.md#spawn-and-template-input-refusals).
The protected gateway is Linux-only; see
[Cursor operation boundaries](caller-contract.md#cursor-operation-boundaries).

Choose models through normal [agent arguments](usage.md#fresh-cursor-use).
Availability and billing depend on the account and provider; agent-director
offers no model catalog, billing controls or account administration. See
[Cursor billing](https://cursor.com/docs/models-and-pricing) and
[provider-key terms](https://prod.cursor.com/help/models-and-usage/api-keys).
These do not enable the refused credential profile.
