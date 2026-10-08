# Multi-Account

Launching a Spawn against a different Claude account than the
operator's default. agent-director's `extra_env` parameter is the
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
relative dir unless the row's recorded `jsonl_path` (or a
`prior_sessions` entry's) still points at it.

If `extra_env` sets `HOME` and not `CLAUDE_CONFIG_DIR`, Claude Code
reads `<HOME>/.claude.json`, so the pretrust write targets that file.
The same rule applies: a `HOME` that is not an absolute path gets no
pretrust write and `pre_trust: failed`. A `CLAUDE_CONFIG_DIR` in
`extra_env`, usable or not, takes precedence over `HOME`. Nothing else
in agent-director is built for an extra-env `HOME`: the agent's
hooks look for agent-director's store under that home, so the spawn's
row can stay `pending`, and `resume` does not look for transcripts
there. To give a Spawn its own Claude config, set
`CLAUDE_CONFIG_DIR`, not `HOME`.

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

Both env-var paths bypass the local `~/.claude.json` auth cache —
verified empirically against Claude Code 2.1.120 (see
`reference/anthropic-api-key-auth-research.md` and
`reference/max-account-auth-research.md`). The bogus-token failure mode
proves the env var is *the* auth path, not a fallback to a cached file.

## Passing the env var at spawn time

```
agent-director spawn \
  --cwd /work/project-foo \
  --extra-env ANTHROPIC_API_KEY=sk-ant-api-test-...
```

Or for a Max account:

```
agent-director spawn \
  --cwd /work/project-foo \
  --extra-env CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-...
```

The reserved-key validation (SRD §7.2 step 4) rejects `AGENT_DIRECTOR_*`
keys but *does not reserve* the auth env vars — they pass through to
the tmux session and into Claude verbatim. agent-director never logs
the value. It *does* persist the per-spawn `--extra-env` map to the
store — with no opt-out — so `resume` can restore a finished (`ended` or
`missing`) row's original env (see `internal/spawn/relaunch.go`: "ExtraEnv is restored
from the persisted row … including CLAUDE_CONFIG_DIR and any auth vars").
That means the auth token sits at rest in `~/.agent-director/state.db`;
the store file is forced `0600` in a `0700` directory on every open, so
it is readable only by the owner. (See also the secrets-at-rest caveat
in the README's Configuration section.)

**Caveat — templates persist `extra_env` as plaintext TOML.** Both
paths now put auth vars on disk; the difference is *where*. Per-spawn
`--extra-env` values land in the owner-only (`0600`) state DB described
above. Auth vars baked into a template's `extra_env`, by contrast, are
written verbatim to the template's plaintext TOML under
`~/.agent-director/templates/` (see `pkg/api/make_template.go`), and
spawn merges them back in at resolve time (`internal/spawn/params.go`).
Prefer per-spawn `--extra-env` for auth, and do not bake auth tokens
into a template unless you accept them sitting on disk in plaintext.

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
  --extra-env CLAUDE_CODE_OAUTH_TOKEN="$LONG_LIVED_TOKEN"
```

This is the same pattern Claude's official GitHub Action uses.

## Multi-container parallelism

Env-var auth avoids the concurrent-write race that file-based auth has
on `~/.claude.json`. Multiple Spawns (or test containers — see
`architecture.md`'s "Test Harness" section) can run side-by-side without
fighting over a shared credential file. Each Spawn gets a fresh
`~/.claude/` directory inside its working tree on first contact.

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

## References

- API-key auth:
  <https://docs.claude.com/en/docs/claude-code/iam#anthropic-api-key>
- `claude setup-token`:
  <https://docs.claude.com/en/docs/claude-code/headless#setup-token>
- Empirical investigation (gitignored, in-repo):
  `reference/anthropic-api-key-auth-research.md`,
  `reference/max-account-auth-research.md`
