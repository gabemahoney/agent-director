---
name: release-agent-director
description: Cut a coordinated agent-director release. Cross-compiles three CLI binaries (linux/amd64, linux/arm64, darwin/arm64 — darwin/amd64 dropped 2026-05-24), stages them into the per-platform npm sub-packages, tags the commit, publishes the umbrella npm package plus two per-platform optional dependencies, and creates a GitHub release with the CLI binaries attached. Runs as a phased pipeline (preflight → notes → build → verify → tag → publish → gh-release → report) ordered most-reversible to least-reversible, with halt-on-failure semantics. Defaults to --dry-run; pass --release to execute irreversible steps. Use this skill when the user says "release agent-director", "cut a release", or "publish v<X.Y.Z>".
---

## When to invoke

Trigger phrases: "release agent-director", "cut a release",
"publish v<X.Y.Z>", "tag and release".

## What this skill does

This skill runs `release.sh` from the same directory. The script is
organized as a phased pipeline; phases are ordered
most-reversible → least-reversible and the script halts on the
first failing phase. Every phase prefixes its stdout/stderr with
`[<phase>] ` so a failing run is greppable.

| Phase | Reversibility | What it does |
|---|---|---|
| `preflight` | none — read-only | semver normalization; gh on PATH (for `--release`); working tree clean; tag does not exist locally; current branch matches `--branch` |
| `notes` | local file write | template `dist/release-notes.md` from `git log <prev-tag>..HEAD` grouped by Epic ID |
| `build` | local file write | `make release-binaries` (3 CLI cross-compiles into `dist/`) + stage each into the matching `pkg/ts-bun-client/platforms/<dir>/bin/agent-director` |
| `verify` | local file write | stage a release-stamped copy of the umbrella + host platform sub-package, `bun pm pack`, install into a temp `HOME`, then run `verify-installed-pkg.ts --smoke` (constructs a `Client` and asserts `client.version()` returns a well-formed `{ version, commit }` envelope). Also asserts the postinstall placed `~/.claude/skills/install-agent-director/` with the right frontmatter version. Runs the full in-tree `bun test` suite against `pkg/ts-bun-client/` (`bun install --frozen-lockfile && bun test`). Then re-stages `dist/` binaries into `platforms/` (undoing any overwrite by `test/setup.ts`) and re-asserts the staged binary version matches `$VERSION` (b.uys anchor). |
| `tag` | **POINT OF NO RETURN** | `git tag -a $VERSION` and `git push origin $VERSION` |
| `publish` | **irreversible** | in-place stamp version onto umbrella + 2 sub-package `package.json` files + `skills/install-agent-director/SKILL.md` frontmatter `version:` (SR-4.1 lockstep); caret-pin `optionalDependencies` (umbrella); `prepublishOnly` re-checks; `npm publish` 2 per-platform optional packages, then the umbrella; same-version retries forbidden |
| `gh-release` | irreversible | `gh release create $VERSION` with 3 CLI binaries attached |
| `report` | read-only | final summary; on failure, names the failed phase + the phase-specific corrective action |

## Pre-flight checklist

Before invoking `./release.sh v<X.Y.Z> --release`, verify all five:

1. **No placeholder names.** None of the three `package.json` files
   (`pkg/ts-bun-client/package.json` + the two under
   `platforms/*/`) contain the `@CHANGEME-H3/...` or `@TBD/...`
   placeholder. H3 itself was resolved on 2026-05-24 (see
   `docs/release-blockers.md`); the publish-phase sentinel remains
   in place as a forward-going tripwire (now Guard 0 of
   `pkg/ts-bun-client/scripts/prepublish-guards.ts`) and halts before
   any `npm publish` if a placeholder is ever re-introduced.
2. **Functional gates green.** Both the Go suite (`go test ./...`)
   and the TS suite (`bun test` under `pkg/ts-bun-client/`) succeed
   locally. `make release-binaries-smoke` succeeds. The Docker
   testplans listed in the verify-before-release table (see
   `docs/architecture.md` "Release engineering") all pass.
3. **`NPM_TOKEN` set in env.** Live runs require the publishing
   token. It is read from the environment only — never bake it
   into the script — and the publish phase writes it to a
   transient `pkg/ts-bun-client/.npmrc` with `0600` perms that the
   EXIT trap cleans up.
4. **`gh auth status` authenticated** against the repo's GitHub
   remote (read+push). Required by `gh release create`
   (gh-release phase).
5. **v0.4.1+ artifacts in lockstep.** The umbrella's
   `pkg/ts-bun-client/scripts/postinstall.ts` is shipped via the
   umbrella's `files` glob (alongside `skills/install-agent-director/**`,
   staged at pack time by the `prepack` hook
   `scripts/stage-skill.ts`), and the SKILL.md frontmatter `version:`
   field is the source of truth the postinstall + release pipeline
   read. The `publish` phase keeps the umbrella `package.json`
   `version` and the SKILL.md frontmatter `version:` in lockstep at
   bump time; `prepublishOnly` (Guard 1) refuses to publish if they
   drift. The `verify` phase packs the umbrella into a tarball,
   installs it under a temp `HOME`, runs the version smoke, and
   asserts the postinstall placed the skill body with the right
   frontmatter version — so a regression in the published-shape
   tarball is caught before the tag is pushed.

The existing semver / clean-tree / branch / tag-not-exists gates
remain enforced by the preflight phase and are restated here so the
operator runs through one list, not two.

## Dry-run workflow

The script DEFAULTS to `--dry-run`. Use this five-step workflow
locally to verify a release before passing `--release`.

1. **Sync.** `git fetch --tags && git checkout main && git pull`.
2. **Check the pre-flight checklist above.** Anything not satisfied
   will surface in either the preflight or build phase.
3. **Sanity-run on a fresh checkout.** `./release.sh v<X.Y.Z>
   --dry-run` (the default; the flag is explicit for clarity).
4. **Inspect the templated release notes** at
   `dist/release-notes.md` (also dumped to stdout at the end of
   the dry run). Make sure Epic-grouped entries make sense.
5. **Confirm the report is all-green** (`[report] ✓ preflight`,
   `notes`, `build`, `verify`, `tag`, `publish`, `gh-release`,
   `[report] ==== dry-run OK ====`). The dry-run rolls back its
   in-place mutations to `package.json` version stamps and
   `optionalDependencies` pins on EXIT, so `git status --porcelain`
   should be empty afterward.

## Live release

Once the dry run is green and the pre-flight checklist is fully
satisfied:

```sh
NPM_TOKEN=... ./release.sh v<X.Y.Z> --release
```

The script will execute every phase including `git push --tags`,
`npm publish`, and `gh release create`. The report phase at the
end confirms which phases ran. If anything failed mid-run, follow
the **Failure recovery** table below.

## Failure recovery

Each phase has a phase-specific recovery path. The report phase
prints the right one automatically; this table is the
human-readable index for forward planning.

| Failed phase | What is mutated | Recovery |
|---|---|---|
| `preflight` | nothing | Fix the reported error and re-run. No state has been changed. |
| `notes` | `dist/release-notes.md` written | Inspect `git log` range; re-run. |
| `build` | `dist/` populated, CLI binaries staged | Fix the cross-compile failure (`make release-binaries`), then re-run. |
| `verify` | local builds, no remote state | Fix the regression. **Never ship a red verify** — that is the entire point of this gate. The verify phase exercises the published shape (tarball + install + version smoke), so the regression is in the umbrella's `files` glob, the `prepack` skill-staging hook (`scripts/stage-skill.ts`), `scripts/postinstall.ts`, the subprocess Client's resolveCliPath path, or the staged CLI binary itself. Iterate until verify is green. |
| `tag` | **tag pushed to remote** | Delete the remote tag and try again: `git push --delete origin v<X.Y.Z> && git tag -d v<X.Y.Z>`. Re-run from the top once the underlying cause is fixed. |
| `publish` | **npm packages published** | The published version is gone — **same-version retries are forbidden**. Increment VERSION (PATCH bump), re-run from the top. The npm registry will accept the new version; the partially-published old version stays as a record. **Never silent re-publish.** |
| `gh-release` | tag pushed + npm published; GH release not created | Do **NOT** increment VERSION — the tag and npm publish already succeeded for that version. Re-run `gh release create` manually with the assets in `./dist/`. The report phase prints the exact command. |

### Never silent re-publish

If `publish_phase` fails for any package after one or more
sub-packages already succeeded, the script halts and refuses to
retry the same version. npm registries reject re-publishing the
same `<name>@<version>`; even if they did not, a silent re-publish
would break consumers' install reproducibility. The only valid
retry path is to bump the version (PATCH for a re-publish, MINOR
or MAJOR if the recovery patch is meaningful) and re-run from the
top.

### `prepublishOnly` guards (v0.4.1+)

`pkg/ts-bun-client/scripts/prepublish-guards.ts` runs synchronously
inside `npm publish` / `bun publish` for the umbrella and blocks the
publish on any of four invariant violations (SRD §SR-3.1 + §SR-3.3 +
§SR-4.1). Each is a hard release-blocker; fix and re-run.

| Guard | Invariant | Recovery |
|---|---|---|
| Placeholder name | umbrella `name` does not contain `CHANGEME-H3` | Edit `pkg/ts-bun-client/package.json` `name`; see `docs/release-blockers.md`. |
| Version skew | `package.json` `version` == `SKILL.md` frontmatter `version:` | Re-run `publish_phase`'s lockstep stamp — both fields move together. If you edited one by hand, re-run the release. |
| `os`/`cpu` drift | umbrella `os` == `["linux","darwin"]` AND `cpu` == `["x64","arm64"]`, exact match including element order | Restore the SR-3.1 pin in `pkg/ts-bun-client/package.json`. If a future Epic legitimately reorders or expands the supported set, update the SR-3.1 pin in the SRD and in `prepublish-guards.ts` together. |
| Opt-deps range | umbrella `optionalDependencies[@agent-director/linux-x64]` and `[@agent-director/darwin-arm64]` both equal `^<umbrella.version>` | Re-run `bun run scripts/version-bump.ts --version <X.Y.Z>` from `pkg/ts-bun-client/` (the canonical pin-rewriter, invoked by `publish_phase`). |

## Flags

- `--dry-run` (default) — perform every phase except the
  irreversible publishes (`git push --tags`, `npm publish`, `gh
  release create`). Restores any in-place mutations to the
  working tree on EXIT.
- `--release` — flip to live mode; tag, publish, and gh-release
  actually execute their irreversible commands.
- `--branch <name>` — release from a non-main branch. Default
  `main`.
- `--no-build` — skip `make release-binaries` (assumes `./dist/`
  is already populated, e.g. from a CI artifact upload). The
  staging step still runs.

## Environment variables

- `NPM_TOKEN` — required for `--release`. Written to a transient
  `pkg/ts-bun-client/.npmrc` (0600) that the EXIT trap deletes.

## What this skill does NOT do

- It does NOT bump version numbers in source files. The released
  binary is stamped with the `$VERSION` argument via `VERSION_LDFLAGS`
  (passed as a `make` override to `make release-binaries`), so the
  binary reports the exact release tag regardless of when the build
  phase runs relative to `tag_phase`. Dev builds fall back to
  `git describe` output (see `Makefile` `VERSION_LDFLAGS`).
- It does NOT push to non-GitHub hosts. v1 is GitHub-only.
- It does NOT build Windows binaries. Windows is unsupported.
- It does NOT publish to a private npm registry. The `.npmrc`
  written by `publish_phase` points at `registry.npmjs.org`.

## See also

- `docs/architecture.md` "Release engineering" — the canonical
  architectural reference for the three-CLI asset list and the Go
  module tagging convention.
- `docs/release-blockers.md` — H3 npm-name resolution checklist.
