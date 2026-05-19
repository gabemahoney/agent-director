# Multi-Account

Launching a Spawn against a different Claude account than the
operator's default. claude-director's `extra_env` parameter is the
single mechanism for this — no file mounts, no profile directories,
no `CLAUDE_CONFIG_DIR` redirection.

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
claude-director spawn \
  --cwd /work/project-foo \
  --extra-env ANTHROPIC_API_KEY=sk-ant-api-test-...
```

Or for a Max account:

```
claude-director spawn \
  --cwd /work/project-foo \
  --extra-env CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-...
```

The reserved-key validation (SRD §7.2 step 4) rejects `CLAUDE_DIRECTOR_*`
keys but *does not reserve* the auth env vars — they pass through to
the tmux session and into Claude verbatim. claude-director never logs
the value (only the key name on validation paths) and never persists it
to disk.

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
claude-director spawn \
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
  this path. For resume use cases (Epic 9), the JSONL must be on the
  expected disk path; setting `CLAUDE_CONFIG_DIR` per Spawn is a future
  hook if multi-account resume becomes a hard requirement.
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
