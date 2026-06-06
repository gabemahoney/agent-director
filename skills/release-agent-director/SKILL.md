---
name: release-agent-director
description: |-
  LLM-driven /release pipeline for agent-director. Invoke as `/release patch`,
  `/release minor`, or `/release major` to cut a coordinated release: discover
  every test surface, cross-compile three CLI binaries, pack and install-verify
  the npm tarball, generate release notes, then publish to npm + GitHub (only
  after every gate passes). Defaults to dry-run; pass `--release` to execute
  irreversible steps (npm publish, git tag, GitHub Release, fast-forward main,
  delete remote branch). Halt-on-first-failure with structured per-gate
  diagnostics and a machine-readable `dist/release-report.json`. Trigger
  phrases: "release agent-director", "cut a release", "publish v<X.Y.Z>".
---

## When to invoke

- "release agent-director"
- "cut a release"
- "publish v<X.Y.Z>"
- "bump and release"
- "ship a new version"

## Invocation

- `/release patch` — bumps Z (X.Y.Z → X.Y.(Z+1))
- `/release minor` — bumps Y (X.Y.Z → X.(Y+1).0)
- `/release major` — bumps X (X.Y.Z → (X+1).0.0)

No explicit version argument is supported. The bump kind is always one of the
three above; the target version is derived from the current source version.

## Modes

- **Default (no flag) — dry-run.** Every gate runs, every artifact is built
  (binaries, tarball, notes preview), nothing is published. Zero side effects
  on GitHub or npm.
- **`--release`** — same flow but the publish phase is irreversible: pushes
  the tag, creates the GitHub Release, runs `npm publish`, fast-forwards main,
  and deletes the remote release branch.

## Phase model

Phases are ordered most-reversible to least-reversible. The pipeline halts on
the first failure. Each phase prefixes its terminal output with `[<phase>] `
so a failing run is greppable. Later Epics fill in detailed sub-steps; the
list below names each phase and its Epic owner.

1. **`preflight`** — Read-only gates that prevent obviously-broken runs: gh
   auth, npm token, npm whoami, worktree clean, main synced, version novelty,
   source-of-truth invariant, supported platforms, no custom build tags.
   Built in E4.

2. **`branch-and-bump`** — Create release branch, bump
   `pkg/ts-bun-client/package.json`, commit. Built in E5.

3. **`coverage`** — Discover and run every test surface (Go full tree, bun
   test, docker-harness epic enumeration). Built in E5.

4. **`compile`** — Cross-compile three CLI binaries; per-binary smoke;
   binary-version coherence. Built in E6.

5. **`pack`** — `bun pm pack` of umbrella; install-verify the tarball into a
   temp HOME; tarball-coherence check (no inline version constants, etc.).
   Built in E7.

6. **`notes`** — Generate `dist/release-notes.md` from
   `git log <prev-tag>..HEAD` grouped by Epic ID. Built in E8.

7. **`publish`** — `npm publish` + `git tag -a v<X.Y.Z> && git push origin
   v<X.Y.Z>` + `gh release create` + fast-forward main + delete remote
   branch. Built in E9.

8. **`finalize`** — Write `dist/release-report.json`. Print terminal summary.
   Built incrementally; scaffolded in E4.

## Diagnostic shape

Every gate failure emits a structured diagnostic with the following schema:

```json
{
  "gate": "<stable-gate-name>",
  "offending_file_or_artifact": "<path or null>",
  "description": "<human-readable explanation>",
  "corrective_action": "<concrete suggestion for the operator>"
}
```

Stable gate names use the form `phase.specific-check`. Examples:

- `preflight.worktree-clean`
- `preflight.invariant-source-of-truth`
- `coverage.go-full-tree`
- `compile.binary-version-coherence`

## Report

At end-of-run (success OR failure), the skill writes `dist/release-report.json`
containing:

```json
{
  "invocation_timestamp": "<ISO-8601>",
  "mode": "dry-run | release",
  "bump_kind": "patch | minor | major",
  "source_version": "<X.Y.Z>",
  "target_version": "<X.Y.Z>",
  "phases": [
    { "name": "preflight", "outcome": "passed | failed | skipped", "sub_checks": [] },
    ...
  ],
  "diagnostics": [],
  "elapsed_seconds": 0
}
```

A condensed terminal summary also prints at end-of-run.

## Legacy artifacts being retired

`release.sh` and the eight `test-*.sh` files remain on disk through Epics
E4–E9 while their LLM-driven replacements are built. Per SR-18.3
(build-new → demonstrate → delete), they are removed in Epic E10 after every
replacement gate is proven against the legacy invariants.
