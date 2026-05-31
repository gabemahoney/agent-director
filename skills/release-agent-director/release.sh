#!/usr/bin/env bash
# release.sh — cut an agent-director release.
#
# Phased pipeline (ordered most-reversible → least-reversible):
#
#   preflight → notes → build → verify → tag → publish → gh-release → report
#
# Each phase is a function. The script halts on the first failing phase
# and the report phase prints which phases succeeded + a corrective
# action for the failure.
#
# Usage:
#   VERSION=v0.1.0 ./release.sh [--dry-run|--release] [--branch main] [--no-build]
#   ./release.sh v0.1.0 [--dry-run|--release] [--branch main] [--no-build]
#
# The script DEFAULTS to --dry-run. Pass --release to enable irreversible
# steps (git push --tags, npm publish, gh release create).
#
# What --dry-run does:
#   - preflight, notes, build, verify    → run REAL
#   - tag                                → logs "(dry-run) would push $VERSION"; no git push
#   - publish                            → uses `npm publish --dry-run --ignore-scripts` per package;
#                                          skips the `npm view` duplicate check (no network)
#   - gh-release                         → logs the asset list it would attach; no `gh release create`
#   - report                             → runs REAL (summary, corrective actions)
#
# What --release does:
#   - Same phases, but tag actually pushes, publish actually invokes
#     `npm publish`, gh-release actually invokes `gh release create`.
#   - Requires NPM_TOKEN in the environment.
#   - Requires the npm package name to be resolved (no CHANGEME-H3 placeholder).
#   - Requires `gh` authenticated and on PATH.
#
# Escape-hatch knobs:
#   --no-build is the sole release-time escape-hatch. It assumes the
#   build artifacts in ./dist/ are already correct and skips `make
#   release-binaries`. Use only when rebuilding would reproduce identical
#   binaries (e.g. a same-commit retry after a transient build infra
#   failure). No env-var bypass knobs exist; any new knob requires an
#   explicit SRD change.
#
# Mode-bit audit (2026-05-27 / T4B / b.nss):
#   After v0.5.1 was published on 2026-05-27 (b.gza incident), 13 tracked
#   .sh/.js files lost their +x bit (100755→100644), clustered in
#   pkg/ts-bun-client/test/fixtures/epic-a/, skills/install-agent-director/,
#   skills/release-agent-director/, and test/driver/.
#
#   All non-publish phases were audited for live-tree mode-bit drift:
#
#   - notes_phase:     writes only dist/release-notes.md (gitignored, root
#                      .gitignore:dist/). The two "chmod +x" strings in that
#                      file are markdown prose inside the <<NOTES HEREDOC, not
#                      executed commands.
#   - build_phase /
#     stage_cli_into_platforms:
#                      chmod 0755 targets pkg/ts-bun-client/platforms/*/bin/
#                      agent-director — gitignored by pkg/ts-bun-client/
#                      .gitignore rule "platforms/*/bin/". No tracked file
#                      is touched.
#   - verify_phase:    all cp -a operations land in mktemp stage dirs
#                      (verify.XXXXXX, verify-home.XXXXXX, verify-proj.XXXXXX)
#                      cleaned up via a RETURN trap. No live-tree writes.
#   - tag_phase:       git tag + git push only. Zero file writes.
#   - gh-release phase: gh release create uploads from dist/ (gitignored).
#                      Zero file writes to the live tree.
#   - report_phase:    cleanup + stdout only. No file writes.
#   - publish_phase:   covered by T4A (db3422e); stage dir pattern confirmed.
#
#   Conclusion: no in-script cause found. The b.gza mode-bit loss is host-side
#   (e.g. filesystem, git-checkout, or editor interaction during the release
#   run) and is tracked by OTQ-3 follow-up bee if it persists.
#
# Exit codes:
#   0  success
#   2  pre-flight failure (bad version, dirty tree, missing gh, etc.)
#   3  build failure (release-binaries)
#   4  GitHub release create failure
#   5  verify-phase failure (bun pack / install / version smoke)
#   6  publish-phase failure (npm publish, H3 unresolved)
#   7  tag-phase failure (tag push)
#
# Sourcing:
#   `_RELEASE_SH_SOURCE_ONLY=1 source release.sh` loads the phase
#   functions without running the main flow — used by the bats tests
#   under skills/release-agent-director/tests/.

set -euo pipefail

# --------------------------------------------------------------------
# Phase logging
# --------------------------------------------------------------------

log() {
    # Usage: log <phase> <msg...>
    local phase="$1"; shift
    printf '[%s] %s\n' "$phase" "$*"
}

# --------------------------------------------------------------------
# Phase outcome tracking + report
# --------------------------------------------------------------------

# Phases append "<phase>:OK" or "<phase>:FAIL:<reason>" on entry/exit.
# report_phase reads this array on EXIT and prints the final summary.
PHASE_RESULTS=()
CURRENT_PHASE=""

# Script-level paths for cleanup helpers called from the EXIT trap.
# Both default to empty so cleanup is a no-op when the phase that sets
# them never ran (e.g. the script failed before publish_phase).
STAGE_DIR=""
# NPMRC_PATH is set by publish_phase when it writes the transient token file.

phase_begin() {
    CURRENT_PHASE="$1"
}

phase_ok() {
    local phase="$1"
    PHASE_RESULTS+=("${phase}:OK")
    CURRENT_PHASE=""
}

phase_fail() {
    local phase="$1" reason="${2:-unspecified}"
    PHASE_RESULTS+=("${phase}:FAIL:${reason}")
    CURRENT_PHASE=""
}

# corrective_action prints the per-phase recovery guidance.
corrective_action() {
    case "$1" in
        preflight)
            log report "  → fix the pre-flight error and re-run; no state has been mutated"
            ;;
        notes)
            log report "  → notes templater failed; inspect git log range and re-run"
            ;;
        build)
            log report "  → rebuild artifacts (make release-binaries) and re-run"
            ;;
        verify)
            log report "  → fix the regression on the release commit before retrying — never ship a red verify"
            ;;
        tag)
            log report "  → delete the remote tag, then re-run:"
            log report "      git push --delete origin $VERSION && git tag -d $VERSION"
            ;;
        publish)
            log report "  → increment VERSION and re-run; same-version retries are forbidden"
            log report "    (an already-published npm version cannot be silently re-published)"
            ;;
        gh-release)
            log report "  → the tag and npm publish already succeeded — do NOT increment VERSION"
            log report "  → re-run \`gh release create $VERSION\` manually with the assets in ./dist/"
            ;;
        *)
            log report "  → no specific recovery guidance for phase $1"
            ;;
    esac
}

cleanup_stage_dir_if_any() {
    if [[ -n "${STAGE_DIR:-}" && -d "$STAGE_DIR" ]]; then
        rm -rf "$STAGE_DIR"
    fi
}

cleanup_npmrc_if_any() {
    if [[ -n "${NPMRC_PATH:-}" && -f "$NPMRC_PATH" ]]; then
        rm -f "$NPMRC_PATH"
    fi
}

# report_phase is registered as the EXIT trap. It is responsible for
# cleanup and the final release summary. Because publish_phase now
# operates exclusively on a temporary stage directory (never mutating
# the live working tree), no in-tree rollback is required here.
# Cleanup responsibilities:
#   1. cleanup_stage_dir_if_any — remove the publish stage temp dir
#   2. cleanup_npmrc_if_any     — remove the transient .npmrc token file
# Both are no-ops if the respective paths were never set (e.g. the
# script failed before publish_phase ran).
report_phase() {
    local rc=$?
    cleanup_stage_dir_if_any
    cleanup_npmrc_if_any
    log report "==== release summary for $VERSION ===="

    # If we died mid-phase the current-phase didn't append its own
    # FAIL entry; synthesize one so the user sees the broken phase.
    if [[ -n "$CURRENT_PHASE" ]]; then
        PHASE_RESULTS+=("${CURRENT_PHASE}:FAIL:exit=$rc")
        CURRENT_PHASE=""
    fi

    local entry phase status reason
    local -a succeeded=() failed=()
    for entry in "${PHASE_RESULTS[@]}"; do
        phase="${entry%%:*}"
        status="${entry#*:}"
        if [[ "$status" == OK ]]; then
            succeeded+=("$phase")
        else
            reason="${status#FAIL:}"
            failed+=("${phase}:${reason}")
        fi
    done

    if (( ${#succeeded[@]} > 0 )); then
        log report "succeeded phases:"
        for phase in "${succeeded[@]}"; do
            log report "  ✓ ${phase}"
        done
    fi
    if (( ${#failed[@]} > 0 )); then
        log report "failed phases:"
        for entry in "${failed[@]}"; do
            phase="${entry%%:*}"
            reason="${entry#*:}"
            log report "  ✗ ${phase} — ${reason}"
            corrective_action "$phase"
        done
        log report "==== release FAILED (exit $rc) ===="
    elif (( ${#succeeded[@]} > 0 )); then
        if [[ "$DRY_RUN" -eq 1 ]]; then
            log report "==== dry-run OK ===="
        else
            log report "==== release OK ===="
        fi
    fi
}

trap report_phase EXIT

# --------------------------------------------------------------------
# Flag parsing
# --------------------------------------------------------------------

# Surviving escape-hatch knob: --no-build (see "Escape-hatch knobs" in
# the file header). No env-var bypass knobs exist in this script; any
# new escape-hatch flag requires explicit SRD approval to prevent
# accidental silent-skip releases.
DRY_RUN=1
EXPLICIT_RELEASE=0
BRANCH="main"
NO_BUILD=0
VERSION="${VERSION:-}"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run)  DRY_RUN=1; shift ;;
        --release)  DRY_RUN=0; EXPLICIT_RELEASE=1; shift ;;
        --branch)   BRANCH="$2"; shift 2 ;;
        --no-build) NO_BUILD=1; shift ;;
        -h|--help)
            sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
            exit 0 ;;
        v*|[0-9]*)  VERSION="$1"; shift ;;
        *)
            echo "release.sh: unknown flag: $1" >&2
            exit 2 ;;
    esac
done

# --------------------------------------------------------------------
# Phase: pre-flight
# --------------------------------------------------------------------

preflight_phase() {
    phase_begin preflight
    # Semver: v?MAJOR.MINOR.PATCH only. No pre-release tags in v1.
    if [[ -z "$VERSION" ]]; then
        log preflight "VERSION is required (e.g. v0.1.0)" >&2
        exit 2
    fi
    [[ "$VERSION" == v* ]] || VERSION="v$VERSION"
    if ! [[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        log preflight "$VERSION is not strict semver (MAJOR.MINOR.PATCH)" >&2
        log preflight "pre-release tags (e.g. v0.1.0-rc1) are not supported in v1" >&2
        exit 2
    fi

    # gh required for live runs only — used for the gh-release phase.
    if ! command -v gh >/dev/null 2>&1; then
        if [[ "$DRY_RUN" -eq 0 ]]; then
            log preflight "'gh' (GitHub CLI) not found on PATH" >&2
            log preflight "install via your package manager and run 'gh auth login'" >&2
            exit 2
        fi
        log preflight "'gh' not on PATH — dry-run gh-release will be a logged no-op"
    fi

    # Working tree must be clean.
    if [[ -n "$(git status --porcelain)" ]]; then
        log preflight "working tree is dirty — commit or stash first" >&2
        git status --short >&2
        exit 2
    fi

    # Tag must not exist locally OR on remote (the latter only checked
    # if gh and a configured origin exist — otherwise rely on the
    # tag-phase remote push to surface conflicts).
    if git tag --list | grep -qx "$VERSION"; then
        log preflight "tag $VERSION already exists locally" >&2
        log preflight "to retry: git push --delete origin $VERSION && git tag -d $VERSION" >&2
        exit 2
    fi

    # Must be on the configured branch.
    local current_branch
    current_branch="$(git rev-parse --abbrev-ref HEAD)"
    if [[ "$current_branch" != "$BRANCH" ]]; then
        log preflight "current branch is '$current_branch', want '$BRANCH'" >&2
        log preflight "use --branch <name> to release from a different branch" >&2
        exit 2
    fi

    log preflight "OK"
    log preflight "version : $VERSION"
    log preflight "branch  : $BRANCH"
    if [[ "$DRY_RUN" -eq 1 ]]; then
        log preflight "mode    : dry-run (pass --release to publish)"
    else
        log preflight "mode    : LIVE — irreversible steps WILL execute"
    fi
    phase_ok preflight
}

# --------------------------------------------------------------------
# Phase: notes (release notes templater)
# --------------------------------------------------------------------

notes_phase() {
    phase_begin notes
    PREV_TAG=$(git tag --list "v*.*.*" --sort=-version:refname | head -n 1 || true)
    if [[ -n "$PREV_TAG" ]]; then
        LOG_RANGE="${PREV_TAG}..HEAD"
        log notes "prev tag: $PREV_TAG"
    else
        LOG_RANGE="HEAD"
        log notes "prev tag: (none — first release)"
    fi

    REPO_ROOT="$(git rev-parse --show-toplevel)"
    mkdir -p "$REPO_ROOT/dist"
    NOTES_FILE="$REPO_ROOT/dist/release-notes.md"

    REPO_SLUG="<owner>/<repo>"
    if command -v gh >/dev/null 2>&1; then
        if slug=$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null) && [[ -n "$slug" ]]; then
            REPO_SLUG="$slug"
        fi
    fi
    if [[ "$REPO_SLUG" == "<owner>/<repo>" ]]; then
        if origin=$(git remote get-url origin 2>/dev/null); then
            slug=$(printf '%s' "$origin" | sed -E 's#^(https://github\.com/|git@github\.com:)##; s#\.git$##')
            if [[ "$slug" != "$origin" && -n "$slug" ]]; then
                REPO_SLUG="$slug"
            fi
        fi
    fi

    cat > "$NOTES_FILE" <<NOTES
# $VERSION

Released $(date -u +'%Y-%m-%d').

## What's in this release

$(git log "$LOG_RANGE" --pretty=format:'%s' \
    | awk '
        BEGIN { ungrouped_count = 0 }
        match($0, /Epic[ ]+[0-9]+\.[a-zA-Z0-9_-]+/) {
            key = substr($0, RSTART, RLENGTH);
            groups[key] = groups[key] "- " $0 "\n";
            order[key] = (order[key] == "" ? NR : order[key]);
            next
        }
        {
            ungrouped[++ungrouped_count] = $0
        }
        END {
            n = 0
            for (k in order) { keys[++n] = k; ord[k] = order[k] }
            for (i = 1; i <= n; i++) {
                for (j = i+1; j <= n; j++) {
                    if (ord[keys[i]] > ord[keys[j]]) {
                        t = keys[i]; keys[i] = keys[j]; keys[j] = t
                    }
                }
            }
            for (i = 1; i <= n; i++) {
                printf "### %s\n%s\n", keys[i], groups[keys[i]]
            }
            if (ungrouped_count > 0) {
                printf "### Other\n"
                for (i = 1; i <= ungrouped_count; i++) {
                    printf "- %s\n", ungrouped[i]
                }
            }
        }
    ')

## Install

Download the binary for your platform and place it on PATH. See
[README.md](README.md) for the post-download install script.

\`\`\`sh
# macOS arm64 (Apple Silicon):
curl -L -o agent-director https://github.com/${REPO_SLUG}/releases/download/$VERSION/agent-director-darwin-arm64
chmod +x agent-director

# Linux amd64:
curl -L -o agent-director https://github.com/${REPO_SLUG}/releases/download/$VERSION/agent-director-linux-amd64
chmod +x agent-director
\`\`\`

## Supported platforms

- **CLI binaries** (three platforms): linux/amd64, linux/arm64
  (statically linked, no glibc dependency), darwin/arm64
  (Mach-O 64). darwin/amd64 was dropped from v1 on 2026-05-24.
- **TS Client** (\`agent-director\` npm umbrella + per-platform
  optional sub-packages \`@agent-director/linux-x64\` and
  \`@agent-director/darwin-arm64\`) spawns the bundled CLI as a
  subprocess; no FFI / native library.

Windows is not supported (SRD §16.1).
NOTES

    log notes "written to $NOTES_FILE"
    phase_ok notes
}

# --------------------------------------------------------------------
# Phase: build
# --------------------------------------------------------------------

# shellcheck source=lib/stage-cli.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/stage-cli.sh"

build_phase() {
    phase_begin build
    if [[ "$NO_BUILD" -eq 1 ]]; then
        log build "--no-build set — assuming ./dist/ is already populated"
        if ! stage_cli_into_platforms; then
            phase_fail build "stage_cli_into_platforms failed"
            exit 3
        fi
        log build "OK"
        phase_ok build
        return 0
    fi
    log build "building release binaries (3 CLI platforms)"
    local _BUILD_COMMIT
    _BUILD_COMMIT=$(git -C "$REPO_ROOT" rev-parse HEAD)
    local _BUILD_LDFLAGS="-X github.com/gabemahoney/agent-director/internal/version.Version=$VERSION -X github.com/gabemahoney/agent-director/internal/version.Commit=$_BUILD_COMMIT"
    if ! (cd "$REPO_ROOT" && make release-binaries VERSION_LDFLAGS="$_BUILD_LDFLAGS") > >(while IFS= read -r l; do printf '[build] %s\n' "$l"; done); then
        log build "release-binaries build failed" >&2
        phase_fail build "release-binaries failed"
        exit 3
    fi
    log build "staging CLI binaries into per-platform npm sub-packages"
    if ! stage_cli_into_platforms; then
        phase_fail build "stage_cli_into_platforms failed"
        exit 3
    fi
    log build "OK"
    phase_ok build
}

# --------------------------------------------------------------------
# Phase: verify
# --------------------------------------------------------------------

# verify_phase packs the umbrella with `bun pm pack` against a staged
# copy whose `package.json` `version` and `SKILL.md` frontmatter
# `version:` have been stamped to the release tag — i.e. the shape
# consumers will see on npm. Installs the tarball into a temp HOME
# (with the host's matching platform sub-package wired in via
# `file:`) and runs verify-installed-pkg.ts --smoke against the
# installed package: constructs a Client and asserts `client.version()`
# returns a well-formed { version, commit } envelope.
#
# This catches a class of regression a unit-test pass does not:
# files-glob omissions, postinstall-path-resolution issues, prepack
# staging failures, optional-deps wiring, CLI-binary resolution from
# require.resolve. Anything mid-flight halts the release at exit 5
# before the tag is pushed.
verify_phase() {
    phase_begin verify
    local plain_v="${VERSION#v}"

    # Map host → npm sub-package name (must match SUPPORTED_TUPLES in
    # pkg/ts-bun-client/src/internal/platformResolve.ts).
    local host_os host_arch host_subpkg_dir
    case "$(uname -s)" in
        Linux)  host_os=linux ;;
        Darwin) host_os=darwin ;;
        *) log verify "unsupported host OS: $(uname -s)" >&2
           phase_fail verify "unsupported host OS"; exit 5 ;;
    esac
    case "$(uname -m)" in
        x86_64|amd64) host_arch=x64 ;;
        arm64|aarch64) host_arch=arm64 ;;
        *) log verify "unsupported host arch: $(uname -m)" >&2
           phase_fail verify "unsupported host arch"; exit 5 ;;
    esac
    host_subpkg_dir="${host_os}-${host_arch}"
    if [[ ! -d "$REPO_ROOT/pkg/ts-bun-client/platforms/$host_subpkg_dir" ]]; then
        log verify "host has no matching platform sub-package: $host_subpkg_dir" >&2
        log verify "(supported: linux-x64, darwin-arm64)" >&2
        phase_fail verify "no host platform sub-package"
        exit 5
    fi

    # ----------------------------------------------------------------
    # Step 0/4: regression-anchor for b.b3h — assert the host binary
    # baked into dist/ is stamped with the exact release VERSION, not
    # a `git describe` decoration like "v0.6.0-1-g74ce955".
    #
    # Mapping: verify_phase uses npm tuple (linux-x64); the dist/
    # binary uses the cross-compile tuple (linux-amd64 / darwin-arm64).
    # ----------------------------------------------------------------
    log verify "step 0/4: assert dist/ binary is stamped with VERSION=$VERSION (b.b3h anchor)"
    local host_bin_arch
    case "$(uname -m)" in
        x86_64|amd64) host_bin_arch=amd64 ;;
        arm64|aarch64) host_bin_arch=arm64 ;;
        *) log verify "unsupported host arch for binary check: $(uname -m)" >&2
           phase_fail verify "unsupported host arch"; exit 5 ;;
    esac
    local host_bin_os
    case "$(uname -s)" in
        Linux)  host_bin_os=linux ;;
        Darwin) host_bin_os=darwin ;;
        *) log verify "unsupported host OS for binary check: $(uname -s)" >&2
           phase_fail verify "unsupported host OS"; exit 5 ;;
    esac
    local host_bin="$REPO_ROOT/dist/agent-director-${host_bin_os}-${host_bin_arch}"
    if [[ ! -x "$host_bin" ]]; then
        log verify "FAIL b.b3h anchor: host binary not found or not executable: $host_bin" >&2
        phase_fail verify "b.b3h: host binary missing"; exit 5
    fi
    local bin_version_json bin_stamped_version
    bin_version_json="$("$host_bin" version 2>/dev/null)" || {
        log verify "FAIL b.b3h anchor: \`$host_bin version\` exited non-zero" >&2
        phase_fail verify "b.b3h: binary version failed"; exit 5
    }
    bin_stamped_version="$(printf '%s' "$bin_version_json" | jq -r '.version // empty')"
    if [[ "$bin_stamped_version" != "$VERSION" ]]; then
        log verify "FAIL b.b3h anchor: binary .version=\"$bin_stamped_version\"; expected \"$VERSION\"" >&2
        log verify "  This means VERSION_LDFLAGS was not passed to make — the build_phase ldflags override is missing." >&2
        phase_fail verify "b.b3h: version stamp mismatch"; exit 5
    fi
    log verify "  binary version stamp OK: .version=$bin_stamped_version"

    local stage_dir tmp_home tmp_workdir
    stage_dir="$(mktemp -d "${TMPDIR:-/tmp}/verify.XXXXXX")"
    tmp_home="$(mktemp -d "${TMPDIR:-/tmp}/verify-home.XXXXXX")"
    tmp_workdir="$(mktemp -d "${TMPDIR:-/tmp}/verify-proj.XXXXXX")"
    # shellcheck disable=SC2064  # we want the variables resolved now
    trap "rm -rf '$stage_dir' '$tmp_home' '$tmp_workdir'" RETURN

    log verify "step 1/4: bun pack umbrella + host platform sub-package"

    # Stage the umbrella + platforms + skill source into a writable
    # working tree, then stamp them to the release tag.
    mkdir -p "$stage_dir/pkg/ts-bun-client"
    mkdir -p "$stage_dir/skills"
    cp -a "$REPO_ROOT/pkg/ts-bun-client/." "$stage_dir/pkg/ts-bun-client/"
    cp -a "$REPO_ROOT/skills/install-agent-director" "$stage_dir/skills/"
    # src/internal/errorMap.ts imports catalog.json via a cross-pkg relative path.
    mkdir -p "$stage_dir/pkg/api/errnames"
    cp "$REPO_ROOT/pkg/api/errnames/catalog.json" "$stage_dir/pkg/api/errnames/catalog.json"

    # Wipe any stray dev artifacts the cp -a dragged in.
    rm -rf "$stage_dir/pkg/ts-bun-client/node_modules"
    rm -rf "$stage_dir/pkg/ts-bun-client/skills"

    # Stamp version sites via version-bump.ts.
    # Skip opt-deps: verify_phase tests the packed tarball via local bun
    # install, which needs file: paths intact for sub-package resolution.
    if ! (cd "$stage_dir/pkg/ts-bun-client" && bun run scripts/version-bump.ts \
            --version "$plain_v" \
            --target umbrella-version \
            --target platform-version \
            --target skill-frontmatter) \
            > >(while IFS= read -r l; do printf '[verify] %s\n' "$l"; done); then
        log verify "version-bump.ts failed" >&2
        phase_fail verify "version-bump.ts"
        exit 5
    fi

    # Install dev deps + bun-build so the files glob has dist/* to pack.
    if ! (cd "$stage_dir/pkg/ts-bun-client" \
            && bun install --no-progress >/dev/null 2>&1 \
            && bun run build >/dev/null 2>&1 \
            && bun pm pack >/dev/null 2>&1); then
        log verify "FAIL bun-pack" >&2
        phase_fail verify "bun-pack"
        exit 5
    fi

    local tgz
    tgz="$(ls "$stage_dir/pkg/ts-bun-client/"agent-director-*.tgz 2>/dev/null | head -n 1)"
    if [[ -z "$tgz" || ! -f "$tgz" ]]; then
        log verify "FAIL bun-pack: no tarball produced" >&2
        phase_fail verify "bun-pack: no tarball"
        exit 5
    fi

    log verify "step 2/4: install tarball + run client.version() smoke"

    # Consumer fixture: trust the package so the postinstall actually
    # fires (bun's untrusted-package default blocks it). We pin the
    # umbrella to the local tarball and the host's platform sub-package
    # to the staged copy so require.resolve('@agent-director/<host>/package.json')
    # finds a real CLI binary at bin/agent-director.
    cat > "$tmp_workdir/package.json" <<CONSUMER_PKG
{
  "name": "release-verify-consumer",
  "version": "0.0.0",
  "trustedDependencies": ["agent-director", "@agent-director/${host_subpkg_dir}"]
}
CONSUMER_PKG

    if ! (cd "$tmp_workdir" && HOME="$tmp_home" \
            bun add "file:$tgz" "@agent-director/${host_subpkg_dir}@file:$stage_dir/pkg/ts-bun-client/platforms/$host_subpkg_dir" \
            >/dev/null 2>&1); then
        log verify "FAIL bun-add (tarball + platform sub-package)" >&2
        phase_fail verify "bun-add"
        exit 5
    fi

    # Sanity: the postinstall should have landed the install skill
    # under the temp HOME with the release-stamped frontmatter.
    local landed="$tmp_home/.claude/skills/install-agent-director/SKILL.md"
    if [[ ! -f "$landed" ]]; then
        log verify "FAIL postinstall: SKILL.md not landed at $landed" >&2
        phase_fail verify "postinstall: SKILL.md not landed"
        exit 5
    fi
    local landed_v
    landed_v="$(awk '/^version:/ {print $2; exit}' "$landed" | tr -d '\r' | sed 's/^"//;s/"$//;s/^'\''//;s/'\''$//')"
    if [[ "$landed_v" != "$plain_v" ]]; then
        log verify "FAIL postinstall: SKILL.md frontmatter version=$landed_v; expected $plain_v" >&2
        phase_fail verify "postinstall: SKILL.md version mismatch"
        exit 5
    fi

    # Smoke: construct a Client against the installed package and
    # assert client.version() returns a well-formed envelope.
    local smoke_script="$REPO_ROOT/pkg/ts-bun-client/scripts/verify-installed-pkg.ts"
    if [[ ! -f "$smoke_script" ]]; then
        log verify "FAIL: verify-installed-pkg.ts missing at $smoke_script" >&2
        phase_fail verify "smoke script missing"
        exit 5
    fi
    if ! (cd "$tmp_workdir" && HOME="$tmp_home" bun "$smoke_script" --smoke) \
            > >(while IFS= read -r l; do printf '[verify] %s\n' "$l"; done); then
        log verify "FAIL client.version() smoke against installed tarball" >&2
        phase_fail verify "version() smoke"
        exit 5
    fi

    log verify "step 3/4: bun test pkg/ts-bun-client (in-tree)"

    if ! (cd "$REPO_ROOT/pkg/ts-bun-client" && bun install --frozen-lockfile && bun test) \
            > >(while IFS= read -r l; do printf '[verify] %s\n' "$l"; done); then
        log verify "FAIL bun test (in-tree pkg/ts-bun-client)" >&2
        phase_fail verify "bun test"
        exit 5
    fi

    log verify "  postinstall verify OK: SKILL.md frontmatter version=$plain_v under $tmp_home/.claude/skills/install-agent-director/"
    log verify "OK"
    phase_ok verify
}

# --------------------------------------------------------------------
# Phase: tag
# --------------------------------------------------------------------

# tag_phase pushes the agent-director release tag. The Go module at
# pkg/api/ is in-repo and shares the same go.mod as the root (no
# separate pkg/api/go.mod). Module resolution therefore relies on the
# single root tag — `go list -m github.com/gabemahoney/agent-director/
# pkg/api@$VERSION` resolves via the root tag. If pkg/api/ is ever
# split into its own module, this phase must additionally push a
# sub-path tag (pkg/api/$VERSION) to satisfy Go's module tag protocol;
# the conditional below detects that case automatically.
tag_phase() {
    phase_begin tag
    if [[ "$DRY_RUN" -eq 1 ]]; then
        log tag "(dry-run) would push $VERSION"
        if [[ -f "$REPO_ROOT/pkg/api/go.mod" ]]; then
            log tag "(dry-run) would also push pkg/api/$VERSION (separate Go module detected)"
        fi
        phase_ok tag
        return 0
    fi
    log tag "pushing $VERSION"
    if ! git tag -a "$VERSION" -m "Release $VERSION"; then
        log tag "git tag failed" >&2
        phase_fail tag "git tag failed"
        exit 7
    fi
    if ! git push origin "$VERSION"; then
        log tag "git push of $VERSION failed" >&2
        phase_fail tag "git push failed"
        exit 7
    fi
    if [[ -f "$REPO_ROOT/pkg/api/go.mod" ]]; then
        local submod_tag="pkg/api/$VERSION"
        log tag "pkg/api has separate go.mod — also pushing $submod_tag"
        if ! git tag -a "$submod_tag" -m "Release $submod_tag"; then
            log tag "git tag $submod_tag failed" >&2
            phase_fail tag "git tag $submod_tag failed"
            exit 7
        fi
        if ! git push origin "$submod_tag"; then
            log tag "git push of $submod_tag failed" >&2
            phase_fail tag "git push of $submod_tag failed"
            exit 7
        fi
    fi
    log tag "pushed $VERSION"
    phase_ok tag
}

# --------------------------------------------------------------------
# Phase: publish (npm)
# --------------------------------------------------------------------

publish_phase() {
    phase_begin publish
    local plain_version="${VERSION#v}"

    local pkg_root="$REPO_ROOT/pkg/ts-bun-client"
    local pkg_json="$pkg_root/package.json"
    if [[ ! -f "$pkg_json" ]]; then
        log publish "missing $pkg_json — TS package layout invariant violated" >&2
        exit 6
    fi

    # Live runs require NPM_TOKEN. Dry-run does not, since
    # `npm publish --dry-run` and `npm pack` don't authenticate.
    # Check before creating the stage dir so we fail fast on bad credentials.
    if [[ "$DRY_RUN" -eq 0 && -z "${NPM_TOKEN:-}" ]]; then
        log publish "NPM_TOKEN not set in environment" >&2
        log publish "release runner must supply NPM_TOKEN (never bake into the script)" >&2
        exit 6
    fi

    # Create a temporary publish stage directory — all mutations (version
    # stamps, SKILL.md rewrite, version-bump.ts, .npmrc) operate inside
    # this tree; the live working tree is never written.
    local stage_dir
    stage_dir="$(mktemp -d "${TMPDIR:-/tmp}/publish.XXXXXX")"
    # Publish the global so the EXIT trap's cleanup_stage_dir_if_any can
    # remove it even if publish_phase exits early.
    STAGE_DIR="$stage_dir"
    mkdir -p "$stage_dir/pkg/ts-bun-client"
    mkdir -p "$stage_dir/skills"
    cp -a "$REPO_ROOT/pkg/ts-bun-client/." "$stage_dir/pkg/ts-bun-client/"
    cp -a "$REPO_ROOT/skills/install-agent-director" "$stage_dir/skills/"
    # catalog.json: src/internal/errorMap.ts imports it at Bun.build time
    # (prepack triggers 'bun run build', which re-bundles from source and
    # needs this file). Mirror it — same reason verify_phase does.
    mkdir -p "$stage_dir/pkg/api/errnames"
    cp "$REPO_ROOT/pkg/api/errnames/catalog.json" "$stage_dir/pkg/api/errnames/catalog.json"
    # Strip dev artifacts the cp -a dragged in (mirrors verify_phase's
    # strip block).
    rm -rf "$stage_dir/pkg/ts-bun-client/node_modules"
    rm -rf "$stage_dir/pkg/ts-bun-client/skills"
    # Install deps so prepack's `bun run build` chain (bun bundle + tsc
    # --emitDeclarationOnly) can resolve bun-types and other devDependencies.
    # mirrors verify_phase's bun install before bun pm pack.
    if ! (cd "$stage_dir/pkg/ts-bun-client" && bun install --no-progress >/dev/null 2>&1); then
        log publish "FAIL bun-install" >&2
        exit 6
    fi

    # ----------------------------------------------------------------
    # H3 gate: first action AFTER stage population so the gate scans
    # staged copies pre-mutation (SR-2.3). No `npm publish` ever runs
    # against a placeholder name. Uses the unified prepublish-guards.ts
    # (subpackage mode) against all three staged package.jsons.
    # ----------------------------------------------------------------
    local p3 p3_rc
    for p3 in \
        "$stage_dir/pkg/ts-bun-client/package.json" \
        "$stage_dir/pkg/ts-bun-client/platforms/linux-x64/package.json" \
        "$stage_dir/pkg/ts-bun-client/platforms/darwin-arm64/package.json"; do
        if [[ ! -f "$p3" ]]; then
            log publish "missing $p3 — TS package layout invariant violated" >&2
            exit 6
        fi
        (cd "$(dirname "$p3")" && PREPUBLISH_GUARD_MODE=subpackage bun run "$REPO_ROOT/pkg/ts-bun-client/scripts/prepublish-guards.ts")
        p3_rc=$?
        if (( p3_rc != 0 )); then
            if [[ "$DRY_RUN" -eq 0 ]]; then
                log publish "H3 placeholder gate failed for ${p3#"$stage_dir/"} (exit $p3_rc); see docs/release-blockers.md" >&2
                exit 6
            fi
            log publish "(dry-run) H3 placeholder gate would fail for ${p3#"$stage_dir/"} — would halt in a live run"
        fi
    done

    # Stamp all five version-stamp sites (umbrella + platforms + opt-deps +
    # SKILL.md frontmatter) via version-bump.ts — single source of truth.
    # The H3 placeholder gate above (prepublish-guards.ts in subpackage mode)
    # already covers the placeholder-check responsibility that the old inline
    # `placeholder_pkgs` loop served.
    log publish "stamping all version sites to $plain_version via version-bump.ts"
    if ! (cd "$stage_dir/pkg/ts-bun-client" && bun run scripts/version-bump.ts --version "$plain_version") \
            > >(while IFS= read -r l; do printf '[publish] %s\n' "$l"; done); then
        log publish "version-bump.ts failed" >&2
        exit 6
    fi

    # Write a transient .npmrc with the token, used by `npm publish`.
    # Lands inside the stage dir so the live tree is never written.
    # The EXIT trap below (report_phase) calls cleanup_npmrc_if_any so
    # the file (and any extracted token) is gone even on hard exits.
    NPMRC_PATH="$stage_dir/pkg/ts-bun-client/.npmrc"
    if [[ -n "${NPM_TOKEN:-}" ]]; then
        printf '//registry.npmjs.org/:_authToken=%s\nalways-auth=true\n' "$NPM_TOKEN" > "$NPMRC_PATH"
        chmod 600 "$NPMRC_PATH"
    else
        : > "$NPMRC_PATH"
    fi

    # Publish order: platform sub-packages first so the umbrella's
    # ^version pins resolve. Each step uses npm view to detect a
    # prior publish at the same version — that path errors out so the
    # operator must increment the version for the retry.
    local pkg_dir pkg_full_name view_out plat_subdir
    for plat_subdir in linux-x64 darwin-arm64; do
        pkg_dir="$stage_dir/pkg/ts-bun-client/platforms/$plat_subdir"
        pkg_full_name=$(grep -E '^[[:space:]]*"name":' "$pkg_dir/package.json" | head -n 1 | sed -E 's/.*"name":[[:space:]]*"([^"]+)".*/\1/')
        log publish "publishing $pkg_full_name@$plain_version"
        if [[ "$DRY_RUN" -eq 0 ]] && command -v npm >/dev/null 2>&1; then
            view_out=$(cd "$pkg_dir" && npm view "${pkg_full_name}@${plain_version}" version 2>/dev/null || true)
            if [[ -n "$view_out" ]]; then
                log publish "$pkg_full_name@$plain_version is already published" >&2
                log publish "version already published, increment version for retry" >&2
                exit 6
            fi
        fi
        if [[ "$DRY_RUN" -eq 1 ]]; then
            if command -v npm >/dev/null 2>&1; then
                if ! (cd "$pkg_dir" && npm publish --dry-run --ignore-scripts) \
                        > >(while IFS= read -r l; do printf '[publish] %s\n' "$l"; done); then
                    log publish "FAIL $pkg_full_name (dry-run validation)" >&2
                    exit 6
                fi
            else
                log publish "(dry-run) npm not on PATH — skipping packaging validation for $pkg_full_name"
            fi
        else
            if ! (cd "$pkg_dir" && npm publish) \
                    > >(while IFS= read -r l; do printf '[publish] %s\n' "$l"; done); then
                log publish "FAIL $pkg_full_name (npm publish)" >&2
                log publish "corrective action: increment VERSION and re-run; same-version retries are forbidden" >&2
                exit 6
            fi
        fi
    done

    # Umbrella package last.
    pkg_full_name=$(grep -E '^[[:space:]]*"name":' "$stage_dir/pkg/ts-bun-client/package.json" | head -n 1 | sed -E 's/.*"name":[[:space:]]*"([^"]+)".*/\1/')
    log publish "publishing umbrella $pkg_full_name@$plain_version"
    if [[ "$DRY_RUN" -eq 0 ]] && command -v npm >/dev/null 2>&1; then
        view_out=$(cd "$stage_dir/pkg/ts-bun-client" && npm view "${pkg_full_name}@${plain_version}" version 2>/dev/null || true)
        if [[ -n "$view_out" ]]; then
            log publish "$pkg_full_name@$plain_version is already published" >&2
            log publish "version already published, increment version for retry" >&2
            exit 6
        fi
    fi
    if [[ "$DRY_RUN" -eq 1 ]]; then
        if command -v npm >/dev/null 2>&1; then
            if ! (cd "$stage_dir/pkg/ts-bun-client" && npm publish --dry-run --ignore-scripts) \
                    > >(while IFS= read -r l; do printf '[publish] %s\n' "$l"; done); then
                log publish "FAIL $pkg_full_name (dry-run validation)" >&2
                exit 6
            fi
        else
            log publish "(dry-run) npm not on PATH — skipping packaging validation for $pkg_full_name"
        fi
    else
        if ! (cd "$stage_dir/pkg/ts-bun-client" && npm publish) \
                > >(while IFS= read -r l; do printf '[publish] %s\n' "$l"; done); then
            log publish "FAIL $pkg_full_name (npm publish)" >&2
            log publish "corrective action: increment VERSION and re-run; same-version retries are forbidden" >&2
            exit 6
        fi
    fi

    cleanup_npmrc_if_any
    log publish "OK"
    phase_ok publish
}

# --------------------------------------------------------------------
# Phase: gh-release
# --------------------------------------------------------------------

# release_assets builds the canonical asset list: 3 CLI binaries.
# Returns assets via the global RELEASE_ASSETS array so callers can
# iterate. Exits the script with code 4 if any expected asset is
# missing on disk.
release_assets() {
    RELEASE_ASSETS=(
        "$REPO_ROOT/dist/agent-director-linux-amd64"
        "$REPO_ROOT/dist/agent-director-linux-arm64"
        "$REPO_ROOT/dist/agent-director-darwin-arm64"
    )
    local a missing=0
    for a in "${RELEASE_ASSETS[@]}"; do
        if [[ ! -f "$a" ]]; then
            if [[ "$DRY_RUN" -eq 1 ]]; then
                log gh-release "(dry-run) missing asset $a — would be required in live run"
                missing=$((missing + 1))
            else
                log gh-release "missing asset $a — was build_phase run?" >&2
                exit 4
            fi
        fi
    done
    if [[ "$DRY_RUN" -eq 1 && $missing -gt 0 ]]; then
        log gh-release "(dry-run) $missing of ${#RELEASE_ASSETS[@]} assets absent; live runs require all present"
    fi
}

ghrelease_phase() {
    phase_begin gh-release
    release_assets

    if [[ "$DRY_RUN" -eq 1 ]]; then
        log gh-release "(dry-run) would attach ${#RELEASE_ASSETS[@]} assets:"
        local a
        for a in "${RELEASE_ASSETS[@]}"; do
            log gh-release "  $(basename "$a") ($(printf '%s' "$a" | sed "s|$REPO_ROOT/||"))"
        done
        log gh-release "(dry-run) would run: gh release create $VERSION --title $VERSION --notes-file $NOTES_FILE <assets>"
        phase_ok gh-release
        return 0
    fi

    log gh-release "creating GitHub release $VERSION with ${#RELEASE_ASSETS[@]} assets"
    if ! gh release create "$VERSION" "${RELEASE_ASSETS[@]}" \
            --title "$VERSION" \
            --notes-file "$NOTES_FILE"; then
        log gh-release "FAIL gh release create" >&2
        log gh-release "the tag $VERSION is pushed AND the npm packages are published" >&2
        log gh-release "do NOT increment version — re-run 'gh release create' manually with the assets in ./dist/" >&2
        log gh-release "  gh release create $VERSION ${RELEASE_ASSETS[*]} --title $VERSION --notes-file $NOTES_FILE" >&2
        phase_fail gh-release "gh release create failed"
        exit 4
    fi
    log gh-release "OK — $VERSION published with ${#RELEASE_ASSETS[@]} assets"
    phase_ok gh-release
}

# --------------------------------------------------------------------
# main
# --------------------------------------------------------------------

main() {
    preflight_phase
    notes_phase
    build_phase
    verify_phase
    tag_phase
    publish_phase
    ghrelease_phase

    if [[ "$DRY_RUN" -eq 1 ]]; then
        echo "------ release notes preview ------"
        cat "$NOTES_FILE"
        echo "------ end preview ------"
    else
        log release "done — $VERSION published"
    fi
}

# Allow tests to source this file without running main.
if [[ "${_RELEASE_SH_SOURCE_ONLY:-0}" -eq 1 ]]; then
    return 0 2>/dev/null || true
fi

main "$@"
