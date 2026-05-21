---
name: release-agent-director
description: Cut a new agent-director release. Validates semver, ensures the working tree is clean, tags the commit, builds all four supported binaries (linux/darwin × amd64/arm64), generates release notes from the commit history since the previous tag (grouped by Epic ID where commit messages reference one), and publishes a GitHub release with the four artifacts attached. Use this skill when the user says "release agent-director", "cut a release", or "publish v<X.Y.Z>".
---

## When to invoke

Trigger phrases: "release agent-director", "cut a release",
"publish v<X.Y.Z>", "tag and release".

## What this skill does

This skill runs `release.sh` from the same directory. The script is
deliberately small and uses `git`, `gh` (GitHub CLI), `make`, and
standard coreutils — no Go, no SDKs.

1. **Validate VERSION.** The `VERSION` argument (or env var) must
   match `v?MAJOR.MINOR.PATCH` semver (no pre-release tags for v1;
   `v0.1.0-rc1` would be rejected). Documented in
   `docs/architecture.md` "Semver policy".

2. **Pre-flight** (checked in this order):
   - `gh` must be authenticated against the repo's GitHub remote.
   - Working tree must be clean (`git status --porcelain` is empty).
     A release that snapshots dirty state is forbidden; the script
     aborts immediately.
   - The new tag must NOT already exist.
   - The current branch must be `main` (configurable via
     `--branch`).

3. **Tag.** `git tag -a $VERSION -m "$VERSION" && git push origin
   $VERSION`. After this point, partial failure (e.g. release upload
   fails) leaves the tag in place. The operator may need to
   `git push --delete origin $VERSION && git tag -d $VERSION` to
   retry.

4. **Build the four binaries.** Delegates to `make release-binaries`
   (Epic 13 Task 1). Outputs land in `./dist/`.

5. **Generate release notes.** `git log <prev-tag>..HEAD --oneline`
   produces the commit list. The release-notes templater groups
   entries by Epic ID where commit messages contain one (e.g.
   `Task 1 / Epic 5.t2.31`), falling back to a flat list otherwise.
   Output goes to `dist/release-notes.md`.

6. **Publish the release.** `gh release create $VERSION dist/*
   --notes-file dist/release-notes.md`. The four binaries are
   attached as assets; the notes appear in the release description.

## Flags

- `--dry-run` — skip the tag-push, build, and release-create steps;
  print what would happen. Useful for verifying the notes template
  before committing to a tag.
- `--branch <name>` — release from a non-main branch. Default `main`.
- `--no-build` — skip `make release-binaries` (assumes `./dist/` is
  already populated, e.g. from a CI artifact upload).

## What this skill does NOT do

- It does NOT bump version numbers in source files. The version is
  derived from the git tag at runtime (currently a build-time pin
  in the install script; Epic 13 may revisit if SRD §19 Q1 lands).
- It does NOT push to non-GitHub hosts. v1 is GitHub-only by SRD
  §16.4.
- It does NOT build Windows binaries. Windows is unsupported per
  SRD §16.1.

## Recovery

If `release.sh` exits non-zero mid-flight, inspect:

- `git tag --list "$VERSION"` — did the tag land?
- `gh release view "$VERSION"` — did GitHub register the release?
- `dist/` — did the build complete?

To retry cleanly: `git push --delete origin "$VERSION" && git tag
-d "$VERSION"`, then re-run.
