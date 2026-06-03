---
name: release-agent-director
description: Cut a coordinated agent-director release. Cross-compiles three CLI binaries (linux/amd64, linux/arm64, darwin/arm64 — darwin/amd64 dropped 2026-05-24), tags the commit, publishes the single umbrella npm package (agent-director), and creates a GitHub release with the CLI binaries attached. The TS client and the CLI ship via separate channels post-b.w3q: npm carries the library only; the CLI binary is downloaded from the GitHub release and installed via the bundled install.sh. Runs as a phased pipeline (preflight → notes → build → verify → tag → publish → gh-release → report) ordered most-reversible to least-reversible, with halt-on-failure semantics. Defaults to --dry-run; pass --release to execute irreversible steps. Use this skill when the user says "release agent-director", "cut a release", or "publish v<X.Y.Z>".
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
| `build` | local file write | `make release-binaries` (3 CLI cross-compiles into `dist/`). No in-tree staging — the CLI ships via the GitHub release tarball, not the npm package. |
| `verify` | local file write | stage a release-stamped copy of the umbrella, `bun pm pack`, install the tarball into a temp `HOME`, stage the release-stamped CLI binary at `$HOME/.agent-director/bin/agent-director` (the SR-1.1 standard install path), then run `verify-installed-pkg.ts --smoke` (constructs a `Client` via `Client.create()` — discovery walks the standard install path before PATH — and asserts `client.version()` returns a well-formed `{ version, commit }` envelope). Runs the full in-tree `bun test` suite against `pkg/ts-bun-client/` (`bun install --frozen-lockfile && bun test`). |
| `tag` | **POINT OF NO RETURN** | `git tag -a $VERSION` and `git push origin $VERSION` |
| `publish` | **irreversible** | in-place stamp version onto the umbrella `package.json` + `skills/install-agent-director/SKILL.md` frontmatter `version:` (SR-4.1 lockstep); `npm publish` the umbrella tarball; same-version retries forbidden |
| `gh-release` | irreversible | `gh release create $VERSION` with 3 CLI binaries attached |
| `report` | read-only | final summary; on failure, names the failed phase + the phase-specific corrective action |

## Pre-flight checklist

Before invoking `./release.sh v<X.Y.Z> --release`, verify all five:

1. **No placeholder names.** The umbrella `pkg/ts-bun-client/package.json`
   does not contain the `@CHANGEME-H3/...` or `@TBD/...` placeholder.
   H3 itself was resolved on 2026-05-24 (see
   `docs/release-blockers.md`). (Post-b.w3q the per-platform
   sub-packages under `platforms/*/` are gone, so this is the only
   `package.json` to check.)
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
5. **Umbrella + install-skill artifacts in lockstep.** The
   `skills/install-agent-director/SKILL.md` frontmatter `version:`
   field and the umbrella `pkg/ts-bun-client/package.json` `version`
   field are bumped in lockstep by `scripts/version-bump.ts` at the
   start of the verify phase; `check-version-coherence.ts --scope
   publish` refuses to publish if they drift. The `verify` phase
   packs the umbrella into a tarball, installs it under a temp
   `HOME`, stages the release-stamped CLI binary at the SR-1.1
   standard install path (`$HOME/.agent-director/bin/agent-director`),
   and runs the version smoke against `client.version()` — so a
   regression in the published umbrella surface or the CLI binary
   stamp is caught before the tag is pushed.

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
   `[report] ==== dry-run OK ====`). All version-stamp mutations
   happen inside a temp stage dir owned by verify_phase (cleaned up
   by the EXIT trap), so `git status --porcelain` should be empty
   afterward.

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
| `build` | `dist/` populated with cross-compiled CLI binaries | Fix the cross-compile failure (`make release-binaries`), then re-run. |
| `verify` | local builds, no remote state | Fix the regression. **Never ship a red verify** — that is the entire point of this gate. The verify phase exercises the published shape (umbrella tarball + install + version smoke against a CLI binary staged at `$HOME/.agent-director/bin/agent-director`), so the regression is in the umbrella's `files` glob, the subprocess Client's discovery pipeline, or the staged CLI binary's version stamp. Iterate until verify is green. |
| `tag` | **tag pushed to remote** | Delete the remote tag and try again: `git push --delete origin v<X.Y.Z> && git tag -d v<X.Y.Z>`. Re-run from the top once the underlying cause is fixed. |
| `publish` | **umbrella npm package published** | The published version is gone — **same-version retries are forbidden**. Increment VERSION (PATCH bump), re-run from the top. The npm registry will accept the new version; the published old version stays as a record. **Never silent re-publish.** |
| `gh-release` | tag pushed + npm published; GH release not created | Do **NOT** increment VERSION — the tag and npm publish already succeeded for that version. Re-run `gh release create` manually with the assets in `./dist/`. The report phase prints the exact command. |

### Never silent re-publish

If `publish_phase` fails after the umbrella has already been
published to npm, the script halts and refuses to retry the same
version. npm registries reject re-publishing the same
`<name>@<version>`; even if they did not, a silent re-publish
would break consumers' install reproducibility. The only valid
retry path is to bump the version (PATCH for a re-publish, MINOR
or MAJOR if the recovery patch is meaningful) and re-run from the
top.

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
