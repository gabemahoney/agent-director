# release-agent-director skill

## Overview

The `/release` skill is an LLM-driven release pipeline for `agent-director`. It
discovers every test surface, cross-compiles the three CLI binaries, packs and
install-verifies the npm tarball, generates release notes, and — once every gate
passes — executes the irreversible publish sequence (npm publish, git tag push,
GitHub Release create, fast-forward `main`, delete remote release branch).

Use `/release` any time you want to cut a new version of agent-director. Its
primary purpose is a **pre-publish gate**: it catches authentication gaps,
version collisions, source-of-truth violations, and build-config drift before
anything escapes to npm or GitHub. The pipeline halts on first failure and always
writes a machine-readable run report, so failed runs are inspectable.

---

## Invocation

```
/release patch    # X.Y.Z → X.Y.(Z+1)
/release minor    # X.Y.Z → X.(Y+1).0
/release major    # X.Y.Z → (X+1).0.0
```

The bump kind must be one of `patch`, `minor`, or `major`. Explicit version
numbers are not accepted; the target version is derived automatically from the
current version in `pkg/ts-bun-client/package.json`.

---

## Modes

**Dry-run (default)** — every gate runs, every artifact is built (binaries,
tarball, release-notes preview) locally, nothing is published. Zero side effects
on GitHub or npm. This is the safe default for checking release readiness.

**`--release`** — identical flow, but the publish phase is live and irreversible:
pushes the git tag, creates the GitHub Release, runs `npm publish`,
fast-forwards `main`, and deletes the remote release branch.

---

## Preflight gates

All 12 gates run before any mutation. Each gate exits 0 on pass (silent) or
exits non-zero and emits one or more SR-14 JSON diagnostics to stderr on
failure. The pipeline halts at the first failure.

| Gate name | What it checks | Common cause of failure | Corrective hint |
|---|---|---|---|
| `preflight.gh-auth` | `gh auth status` exits 0 — the GitHub CLI is authenticated | Not logged in, or token expired | Run `gh auth login` |
| `preflight.npm-token-present` | `$NPM_TOKEN` env var is set and non-empty | Variable not exported in the shell | `export NPM_TOKEN=<your-token>` before invoking `/release` |
| `preflight.npm-whoami` | `npm whoami` exits 0 — the token is valid against the registry | Token expired or revoked | Generate a new npm token and update `NPM_TOKEN` |
| `preflight.worktree-clean` | No modified, staged, or untracked files (`git status --porcelain` empty) | Uncommitted work left over | Commit, stash, or discard the dirty files |
| `preflight.main-synced` | Current branch is `main` and local is exactly in sync with `origin/main` | On a feature branch, or local `main` is ahead/behind/diverged | `git checkout main && git pull --ff-only origin main` |
| `preflight.version-novelty-tag` | `v<target>` does not already exist as a remote git tag | Version was already tagged | Bump to a version not yet tagged on origin |
| `preflight.version-novelty-release` | No GitHub Release for `v<target>` exists | Release was already published on GitHub | Bump the version to one without an existing release |
| `preflight.version-novelty-npm` | `agent-director@<target>` is not yet published on npm | Version already published; npm will reject a re-publish | Bump to a semver value not present on npm |
| `preflight.version-novelty-bump-strict` | Computed target version (`source + bump_kind`) is strictly greater than the latest `vX.Y.Z` git tag | `package.json` version wasn't updated after the previous release, making the computed target stale | Ensure `pkg/ts-bun-client/package.json` reflects the current unreleased version before invoking |
| `preflight.invariant-source-of-truth` | No stray version sites — delegates to `pkg/ts-bun-client/scripts/check-source-of-truth.ts` (SR-16) | A version constant was hard-coded in a file other than the single source-of-truth | Fix every SR-16 violation reported in the preceding JSON lines |
| `preflight.invariant-supported-platforms` | The `release-binaries` Makefile target compiles exactly `linux/amd64`, `linux/arm64`, `darwin/arm64` — no more, no less | Someone edited the Makefile target list | Restore the Makefile `release-binaries` target to the canonical three platforms |
| `preflight.invariant-no-custom-build-tags` | Every `//go:build` identifier in the repo is a standard Go constraint (OS, arch, cgo, unix, `go1.X`, `ignore`) | A custom build tag (e.g., `integration`, `slow`) was added during development | Remove or replace non-standard identifiers; only standard Go constraints are permitted |

---

## Diagnostic shape

When a gate fails it emits one or more **SR-14** JSON objects to stderr, one per
line. The orchestrator captures stderr, parses these lines, and accumulates them
in the run report.

```json
{
  "gate": "<stable-gate-name>",
  "offending_file_or_artifact": "<path or null>",
  "description": "<human-readable explanation>",
  "corrective_action": "<concrete suggestion for the operator>"
}
```

| Field | Type | Description |
|---|---|---|
| `gate` | string | Stable dotted name (`phase.specific-check`). Greppable across logs. |
| `offending_file_or_artifact` | string \| null | Path or artifact identifier relevant to the failure; `null` when there is no single file to blame. |
| `description` | string | Human-readable explanation of what went wrong. |
| `corrective_action` | string | Concrete step the operator should take to resolve the failure. |

**Example** (worktree-clean failure):

```json
{
  "gate": "preflight.worktree-clean",
  "offending_file_or_artifact": "dist/agent-director",
  "description": "working tree has uncommitted changes: 3 file(s) (first: dist/agent-director)",
  "corrective_action": "Commit, stash, or discard before retrying."
}
```

Gate scripts produce diagnostics via
`skills/release-agent-director/gates/lib/emit-diagnostic.sh`, which handles
JSON-escaping of all four fields. Hand-rolling JSON in gate scripts is
prohibited.

---

## Run report

At end-of-run — regardless of outcome — the skill writes a **SR-15** report to
`dist/release-report.json`. This is the canonical artifact for post-run
inspection, CI upload, and audit trail.

```json
{
  "invocation_timestamp": "<ISO-8601>",
  "mode": "dry-run | release",
  "bump_kind": "patch | minor | major",
  "source_version": "<X.Y.Z>",
  "target_version": "<X.Y.Z>",
  "phases": [
    {
      "name": "<phase-name>",
      "outcome": "passed | failed | skipped",
      "started_at": "<ISO-8601>",
      "elapsed_ms": 0,
      "sub_checks": [
        {
          "name": "<check-name>",
          "outcome": "passed | failed | skipped",
          "diagnostic": "<SR-14 payload or null>"
        }
      ]
    }
  ],
  "publish_substeps": [],
  "diagnostics": [],
  "elapsed_seconds": 0
}
```

The report is written by
`skills/release-agent-director/gates/finalize/write-report.sh`. See the inline
comments in that file for the full positional argument contract.

---

## Subsequent phases

The following phases run after preflight (in order). Each is more consequential
than the last; the pipeline halts on any failure. These phases are being built
in later Epics — this section will be extended as each one lands.

**`branch-and-bump`** *(E5)* — Creates a `release/vX.Y.Z` branch off `main`,
bumps the version in `pkg/ts-bun-client/package.json`, and commits the bump.
This is the first mutation in the pipeline.

**`coverage`** *(E5)* — Discovers and runs every test surface: Go full-tree
tests, `bun test`, and any docker-harness integration tests enumerated in the
Epic list. A single test failure aborts the release.

**`compile`** *(E6)* — Cross-compiles the three CLI binaries
(`linux/amd64`, `linux/arm64`, `darwin/arm64`), runs a per-binary smoke check,
and verifies that the binary version string matches the target semver.

**`pack`** *(E7)* — Runs `bun pm pack` on the umbrella npm package,
install-verifies the tarball into a temp `HOME`, and checks the tarball for
coherence (no inline version constants, expected files present, etc.).

**`notes`** *(E8)* — Generates `dist/release-notes.md` from
`git log <prev-tag>..HEAD`, grouped by Epic ID, for inclusion in the GitHub
Release body.

**`publish`** *(E9)* — The irreversible phase: `npm publish`, push annotated git
tag, `gh release create`, fast-forward `main` to the release branch tip, delete
the remote release branch. Skipped entirely in dry-run mode.

**`finalize`** *(E4+)* — Writes `dist/release-report.json` via
`write-report.sh` and prints a condensed terminal summary. Runs unconditionally
at end-of-run.
