---
name: release-agent-director
description: Cut a coordinated agent-director release. Builds three CLI binaries plus pkg/cabi shared libraries on two v1 platforms (linux/amd64, darwin/arm64 — darwin/amd64 dropped 2026-05-24), tags the commit, publishes the umbrella npm package plus two per-platform optional dependencies, and creates a GitHub release with all artifacts attached. Runs as a phased pipeline (preflight → notes → build → verify → tag → publish → gh-release → report) ordered most-reversible to least-reversible, with halt-on-failure semantics. Defaults to --dry-run; pass --release to execute irreversible steps. Use this skill when the user says "release agent-director", "cut a release", or "publish v<X.Y.Z>".
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
| `preflight` | none — read-only | semver normalization; gh on PATH; working tree clean; tag does not exist locally; current branch matches `--branch` |
| `notes` | local file write | template `dist/release-notes.md` from `git log <prev-tag>..HEAD` grouped by Epic ID |
| `build` | local file write | `make release-binaries` (3 CLI platforms) + `gh run download` of `pkg-cabi-<platform>` for the 2 v1 cabi platforms |
| `verify` | local file write | go smoke (`test/smoke/go/...`) + ts smoke + envelope-diff (go + ts) + **postinstall tarball verify** (SRD §SR-8; v0.4.1+): `bun pm pack` against a release-stamped staging copy → install under temp `HOME` → assert bundled `SKILL.md` frontmatter `version:` matches the release tag |
| `tag` | **POINT OF NO RETURN** | `git tag -a $VERSION` and `git push origin $VERSION` |
| `publish` | **irreversible** | in-place stamp version onto umbrella + 2 sub-package `package.json` files + **`skills/install-agent-director/SKILL.md` frontmatter `version:`** (SR-4.1 lockstep, v0.4.1+); caret-pin `optionalDependencies` (umbrella); `prepublishOnly` re-checks (placeholder name, version skew, `os`/`cpu` drift, opt-deps range — SR-3.1 + SR-3.3 + SR-4.1); `npm publish` 2 per-platform optional packages, then the umbrella; same-version retries forbidden |
| `gh-release` | irreversible | `gh release create $VERSION` with 3 CLI binaries + 2 cabi libs + 1 canonical header |
| `report` | read-only | final summary; on failure, names the failed phase + the phase-specific corrective action |

## Pre-flight checklist

Before invoking `./release.sh v<X.Y.Z> --release`, verify all seven:

1. **No placeholder names.** None of the three `package.json` files
   (`pkg/ts-bun-client/package.json` + the two under
   `platforms/*/`) contain the `@CHANGEME-H3/...` or `@TBD/...`
   placeholder. H3 itself was resolved on 2026-05-24 (see
   `docs/release-blockers.md`); the publish-phase sentinel remains
   in place as a forward-going tripwire (now Guard 0 of
   `pkg/ts-bun-client/scripts/prepublish-guards.ts`) and halts before
   any `npm publish` if a placeholder is ever re-introduced.
2. **Self-hosted darwin/arm64 runner online.** Check with
   `gh api /repos/<owner>/<repo>/actions/runners | jq '.runners[]
   | select(.labels[].name=="darwin-arm64") | .status'` — must
   report `online`. The runbook for bringing it up is
   `docs/self-hosted-runner-setup.md`.
3. **CI matrix green on the release commit.** Both legs of
   `.github/workflows/cabi-matrix.yml` (`linux-amd64`,
   `darwin-arm64`) succeeded on the exact commit you intend to
   tag. The build phase will hard-fail (`[build] CI matrix is red
   on $COMMIT`) if not.
4. **Envelope-diff green.** Both the Go suite (`go test
   ./test/envelope-diff/...`) and the TS suite (`make
   envelope-diff-ts`) succeed locally. The verify phase re-runs
   them; if they go red between local and CI you should not be
   releasing.
5. **`NPM_TOKEN` set in env.** Live runs require the publishing
   token. It is read from the environment only — never bake it
   into the script — and the publish phase writes it to a
   transient `pkg/ts-bun-client/.npmrc` with `0600` perms that the
   EXIT trap cleans up.
6. **`gh auth status` authenticated** against the repo's GitHub
   remote (read+push). Required by `gh run list`/`gh run download`
   (build phase) and `gh release create` (gh-release phase).
7. **v0.4.1+ artifacts in lockstep.** The umbrella's
   `pkg/ts-bun-client/scripts/postinstall.ts` is shipped via the
   umbrella's `files` glob (alongside `skills/install-agent-director/**`,
   staged at pack time by the `prepack` hook
   `scripts/stage-skill.ts`), and the SKILL.md frontmatter `version:`
   field is the source of truth the postinstall + release pipeline
   read. The `publish` phase keeps the umbrella `package.json`
   `version` and the SKILL.md frontmatter `version:` in lockstep at
   bump time; `prepublishOnly` (Guard 1) refuses to publish if they
   drift. The `verify` phase's step 4/4 packs the umbrella into a
   tarball, installs it under a temp `HOME`, and asserts the
   postinstall placed the skill body with the right frontmatter
   version — so a regression in the published-shape tarball is
   caught before the tag is pushed.

The existing semver / clean-tree / branch / tag-not-exists gates
remain enforced by the preflight phase and are restated here so the
operator runs through one list, not two.

## Dry-run workflow

The script DEFAULTS to `--dry-run`. Use this six-step workflow
locally to verify a release before passing `--release`.

1. **Sync.** `git fetch --tags && git checkout main && git pull`.
2. **Check the pre-flight checklist above.** Anything not satisfied
   will surface in either the preflight or build phase.
3. **Sanity-run on a fresh checkout.** `./release.sh v<X.Y.Z>
   --dry-run` (the default; the flag is explicit for clarity).
   - With no green `cabi-matrix.yml` run on the commit (typical on
     feature branches), export `AD_RELEASE_SKIP_CABI=1` to skip
     the artifact download step. This bypasses cabi collection
     only; every other phase still runs.
4. **Inspect the templated release notes** at
   `dist/release-notes.md` (also dumped to stdout at the end of
   the dry run). Make sure Epic-grouped entries make sense.
5. **Confirm the report is all-green** (`[report] ✓ preflight`,
   `notes`, `build`, `verify`, `tag`, `publish`, `gh-release`,
   `[report] ==== dry-run OK ====`).
6. **Confirm the working tree is clean.** `git status --porcelain`
   should be empty — the dry-run rolls back its in-place mutations
   to `package.json` version stamps and `optionalDependencies`
   pins on EXIT. If anything is left dirty, file a bug; do NOT
   proceed to `--release`.

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
| `build` | `dist/` populated | If `cabi-matrix.yml` is red: fix the regression on the release commit and wait for a green run, then re-run. If darwin/arm64 leg is unavailable: bring the self-hosted runner online (`docs/self-hosted-runner-setup.md`) and re-run. |
| `verify` | local builds, no remote state | Fix the regression. **Never ship a red verify** — that is the entire point of this gate. If step 4/4 (postinstall tarball verify) fails, the regression is in the umbrella's `files` glob, the `prepack` skill-staging hook (`scripts/stage-skill.ts`), or `scripts/postinstall.ts` itself — none of which are remote-state mutations, so you can iterate freely until verify is green. |
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
  is already populated, e.g. from a CI artifact upload). Also
  skips cabi collection.

## Environment variables

- `NPM_TOKEN` — required for `--release`. Written to a transient
  `pkg/ts-bun-client/.npmrc` (0600) that the EXIT trap deletes.
- `AD_RELEASE_SKIP_CABI=1` — skip `gh run download` of
  `pkg-cabi-*` artifacts. Used by the Docker testplan and by
  feature-branch dry runs where no `cabi-matrix.yml` run exists.
- `AD_RELEASE_SIMULATE_DARWIN_ARM64_OFFLINE=1` — pretend the
  darwin/arm64 artifact is absent. Exercises the offline halt.
- `RELEASE_FORCE_RUN_ID=<id>` — debug-only: pin
  `collect_cabi_artifacts()` to a specific cabi-matrix run id.
  The conclusion is re-checked; this cannot smuggle a red run
  through.
- `CABI_WORKFLOW=cabi-matrix.yml` — workflow filename for the
  cabi-matrix lookup. Default matches the file Epic 6 lands.

## What this skill does NOT do

- It does NOT bump version numbers in source files. The version
  is derived from the `git tag` at runtime via `-ldflags -X` (see
  `Makefile` `VERSION_LDFLAGS`).
- It does NOT push to non-GitHub hosts. v1 is GitHub-only.
- It does NOT build Windows binaries. Windows is unsupported.
- It does NOT publish to a private npm registry. The `.npmrc`
  written by `publish_phase` points at `registry.npmjs.org`.

## See also

- `docs/architecture.md` "Release engineering" — the canonical
  architectural reference for the three-CLI / two-cabi /
  one-header asset list and the Go module tagging convention.
- `docs/release-blockers.md` — H3 npm-name resolution checklist.
- `docs/self-hosted-runner-setup.md` — operator runbook for
  bringing the darwin/arm64 runner online.
