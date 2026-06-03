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
#   - build_phase:     `make release-binaries` writes cross-compiled CLI
#                      binaries into dist/ (gitignored). No tracked file
#                      is touched. (Post-b.w3q: the per-platform
#                      pkg/ts-bun-client/platforms/ tree is gone, so no
#                      in-tree staging happens here either.)
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

# _sha256 <file> — portable SHA-256: uses sha256sum (Linux/coreutils)
# when present, falls back to shasum -a 256 (macOS/BSD).
_sha256() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | cut -d' ' -f1
    else
        shasum -a 256 "$1" | cut -d' ' -f1
    fi
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

    # Sentinel assertion: the umbrella package.json::version field must be
    # "0.0.0" in the live tree before any staging happens.  Catches
    # hand-edits and leftover version bumps from a prior failed run.
    # b.w3q dropped the per-platform sub-packages, so the umbrella is now
    # the only version-stamp site on disk.
    local _sentinel_path="pkg/ts-bun-client/package.json"
    local _actual
    _actual="$(jq -r '.version' "$_sentinel_path")"
    if [[ "$_actual" != "0.0.0" ]]; then
        log preflight "preflight: ${_sentinel_path}::version is \"${_actual}\"; expected \"0.0.0\" (sentinel)" >&2
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

The CLI binary and the TS client library ship separately:

- **CLI binary**: download from this GitHub release and run the
  bundled \`install.sh\` to land it at
  \`~/.agent-director/bin/agent-director\` (the standard install path
  the TS client's discovery pipeline looks at first). See
  [README.md](README.md) for the post-download workflow.
- **TS client library**: \`bun add agent-director\` (or
  \`npm i agent-director\`). The library spawns the installed CLI as
  a subprocess; it does not ship the binary itself.

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
- **TS Client** (\`agent-director\` npm umbrella, single package —
  per-platform sub-packages were removed in b.w3q) spawns the
  separately-installed CLI as a subprocess; no FFI / native library.

Windows is not supported (SRD §16.1).
NOTES

    log notes "written to $NOTES_FILE"
    phase_ok notes
}

# --------------------------------------------------------------------
# Phase: build
# --------------------------------------------------------------------

# b.w3q dropped the per-platform npm sub-packages and the
# pkg/ts-bun-client/platforms/ tree they lived in. The CLI binaries now
# ship only via the GitHub release tarball — no staging into the npm
# package surface is required.

build_phase() {
    phase_begin build
    if [[ "$NO_BUILD" -eq 1 ]]; then
        log build "--no-build set — assuming ./dist/ is already populated"
        log build "OK"
        phase_ok build
        return 0
    fi
    log build "building release binaries (3 CLI platforms)"
    local _BUILD_COMMIT _BUILD_PLAIN_V
    _BUILD_COMMIT=$(git -C "$REPO_ROOT" rev-parse HEAD)
    # SR-2.6 (b.ue3 / Epic 1): tagged release builds stamp clean strict
    # SemVer with no leading "v". The Makefile's default is the dev
    # sentinel; release.sh is the only caller that overrides to a real
    # release version.
    _BUILD_PLAIN_V="${VERSION#v}"
    local _BUILD_LDFLAGS="-X github.com/gabemahoney/agent-director/internal/version.Version=$_BUILD_PLAIN_V -X github.com/gabemahoney/agent-director/internal/version.Commit=$_BUILD_COMMIT"
    if ! (cd "$REPO_ROOT" && make release-binaries VERSION_LDFLAGS="$_BUILD_LDFLAGS") > >(while IFS= read -r l; do printf '[build] %s\n' "$l"; done); then
        log build "release-binaries build failed" >&2
        phase_fail build "release-binaries failed"
        exit 3
    fi
    log build "OK"
    phase_ok build
}

# --------------------------------------------------------------------
# Phase: verify
# --------------------------------------------------------------------

# verify_phase stages the umbrella package into a temp dir, stamps its
# version sites (umbrella package.json + install-skill frontmatter), and
# packs it with `bun pm pack --ignore-scripts`.  SHA-256 of the tarball
# is recorded to $stage_dir/tarball-shasums.txt (two-space coreutils
# format) and the paths are exported via AGENT_DIRECTOR_RELEASE_SHASUMS
# and AGENT_DIRECTOR_RELEASE_STAGE_DIR for publish_phase to consume.
#
# b.w3q dropped the per-platform npm sub-packages, so the release ships
# one umbrella tarball — period.  The CLI binary travels separately via
# the GitHub release tarball + install.sh, landing at
# $HOME/.agent-director/bin/agent-director (the SR-1.1 standard install
# path the TS client's discovery pipeline looks at first).
#
# The stage dir survives verify_phase return (cleanup moves to the EXIT
# trap via STAGE_DIR global) so publish_phase can publish the exact same
# tarball without re-staging or re-stamping (SR-1.6).
#
# The umbrella tarball is additionally installed into a temp HOME and
# verified via verify-installed-pkg.ts --smoke: stages a CLI binary at
# $HOME/.agent-director/bin/agent-director (the standard install path),
# constructs a Client via Client.create() which walks the discovery
# pipeline (HOME/.agent-director/bin → PATH), and asserts
# client.version() returns a well-formed { version, commit } envelope.
# Catches files-glob omissions and CLI-binary discovery failures.
# Anything mid-flight halts the release at exit 5 before the tag is pushed.
verify_phase() {
    phase_begin verify
    local plain_v="${VERSION#v}"

    # ----------------------------------------------------------------
    # Step 0/3: regression-anchor for b.b3h — assert the host binary
    # baked into dist/ is stamped with the exact release version
    # (SR-2.6: plain X.Y.Z, no leading "v"), not a `git describe`
    # decoration like "v0.6.0-1-g74ce955" and not the dev sentinel.
    # ----------------------------------------------------------------
    log verify "step 0/3: assert dist/ binary is stamped with version=$plain_v (b.b3h anchor)"
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
    if [[ "$bin_stamped_version" != "$plain_v" ]]; then
        log verify "FAIL b.b3h anchor: binary .version=\"$bin_stamped_version\"; expected \"$plain_v\"" >&2
        log verify "  This means VERSION_LDFLAGS was not passed to make — the build_phase ldflags override is missing." >&2
        phase_fail verify "b.b3h: version stamp mismatch"; exit 5
    fi
    log verify "  binary version stamp OK: .version=$bin_stamped_version"

    local stage_dir tmp_home tmp_workdir
    stage_dir="$(mktemp -d "${TMPDIR:-/tmp}/verify.XXXXXX")"
    # Assign to STAGE_DIR global immediately so the EXIT trap's
    # cleanup_stage_dir_if_any deletes it on script exit.  stage_dir must
    # survive verify_phase return so publish_phase can consume the tarball.
    STAGE_DIR="$stage_dir"
    tmp_home="$(mktemp -d "${TMPDIR:-/tmp}/verify-home.XXXXXX")"
    tmp_workdir="$(mktemp -d "${TMPDIR:-/tmp}/verify-proj.XXXXXX")"
    # tmp_home and tmp_workdir are consumer-fixture scratch dirs; safe to
    # delete when verify_phase returns.  stage_dir cleanup is deferred to EXIT.
    # shellcheck disable=SC2064  # we want the variables resolved now
    trap "rm -rf '$tmp_home' '$tmp_workdir'" RETURN

    log verify "step 1/3: bun pack umbrella + SHA-256 manifest"

    # Stage the umbrella into a writable working tree, then stamp it to
    # the release tag.
    mkdir -p "$stage_dir/pkg/ts-bun-client"
    cp -a "$REPO_ROOT/pkg/ts-bun-client/." "$stage_dir/pkg/ts-bun-client/"
    # src/internal/errorMap.ts imports catalog.json via a cross-pkg relative path.
    mkdir -p "$stage_dir/pkg/api/errnames"
    cp "$REPO_ROOT/pkg/api/errnames/catalog.json" "$stage_dir/pkg/api/errnames/catalog.json"
    # version-bump.ts (--target skill-frontmatter) resolves the SKILL.md
    # via "../../../skills/install-agent-director/SKILL.md" relative to
    # pkg/ts-bun-client/scripts/, so the install skill source must be
    # staged alongside the umbrella for stamping (read+write, not shipped
    # in the tarball — the install skill has its own delivery channel).
    mkdir -p "$stage_dir/skills"
    cp -a "$REPO_ROOT/skills/install-agent-director" "$stage_dir/skills/"

    # Wipe any stray dev artifacts the cp -a dragged in.
    rm -rf "$stage_dir/pkg/ts-bun-client/node_modules"

    # Stamp umbrella version + install-skill frontmatter. After b.w3q
    # these are the only version-stamp sites (per-platform sub-packages
    # and optionalDependencies are gone).
    if ! (cd "$stage_dir/pkg/ts-bun-client" && bun run scripts/version-bump.ts \
            --version "$plain_v" \
            --target umbrella-version \
            --target skill-frontmatter) \
            > >(while IFS= read -r l; do printf '[verify] %s\n' "$l"; done); then
        log verify "version-bump.ts failed" >&2
        phase_fail verify "version-bump.ts"
        exit 5
    fi

    # Install dev deps + build dist/* so the coherence gate can inspect
    # dist/index.js (site-dist-no-inline) and dist/version-floor.json
    # (site-floor-lockstep), and bun pm pack ships the compiled JS
    # surface listed in the umbrella's files glob.
    if ! (cd "$stage_dir/pkg/ts-bun-client" \
            && bun install --no-progress >/dev/null 2>&1 \
            && bun run build >/dev/null 2>&1); then
        log verify "FAIL bun-install/build" >&2
        phase_fail verify "bun-pack prep"
        exit 5
    fi

    # Gate: verify all version-stamp sites agree before packing.  Runs
    # after bun build because some sites (site-dist-no-inline,
    # site-floor-lockstep) inspect compiled dist/ artifacts.
    if ! (cd "$stage_dir/pkg/ts-bun-client" && bun run scripts/check-version-coherence.ts \
            --scope verify \
            --expected-version "$plain_v") \
            > >(while IFS= read -r l; do printf '[verify] %s\n' "$l"; done); then
        log verify "check-version-coherence.ts --scope verify failed" >&2
        phase_fail verify "check-version-coherence.ts"
        exit 5
    fi

    # Pack umbrella with --ignore-scripts (SR-1.1).
    if ! (cd "$stage_dir/pkg/ts-bun-client" \
            && bun pm pack --ignore-scripts >/dev/null 2>&1); then
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

    # Write SHA-256 manifest (two-space coreutils format: <sha256>  <abs-path>).
    # Export env vars for publish_phase and check-version-coherence.ts --scope publish.
    local shasums_file="$stage_dir/tarball-shasums.txt"
    printf '%s  %s\n' "$(_sha256 "$tgz")" "$tgz" > "$shasums_file"
    export AGENT_DIRECTOR_RELEASE_SHASUMS="$shasums_file"
    export AGENT_DIRECTOR_RELEASE_STAGE_DIR="$stage_dir"
    log verify "SHA-256 manifest written: $shasums_file (1 entry)"
    log verify "  umbrella: $tgz"

    log verify "step 2/3: install tarball + run client.version() smoke"

    # Consumer fixture: install the umbrella tarball only. The CLI binary
    # is staged separately at $tmp_home/.agent-director/bin/agent-director
    # (the standard install path the discovery pipeline checks first per
    # SR-1.1); Client.create() walks that path before falling through to
    # PATH lookup.
    cat > "$tmp_workdir/package.json" <<CONSUMER_PKG
{
  "name": "release-verify-consumer",
  "version": "0.0.0"
}
CONSUMER_PKG

    if ! (cd "$tmp_workdir" && HOME="$tmp_home" \
            bun add "file:$tgz" \
            >/dev/null 2>&1); then
        log verify "FAIL bun-add (umbrella tarball)" >&2
        phase_fail verify "bun-add"
        exit 5
    fi

    # Stage the release-stamped CLI binary at the SR-1.1 standard install
    # path so Client.create() discovery finds it without consulting PATH.
    local staged_cli_dir="$tmp_home/.agent-director/bin"
    mkdir -p "$staged_cli_dir"
    cp "$host_bin" "$staged_cli_dir/agent-director"
    chmod 0755 "$staged_cli_dir/agent-director"

    # Smoke: construct a Client against the installed package and
    # assert client.version() returns a well-formed envelope.
    local smoke_script="$REPO_ROOT/pkg/ts-bun-client/scripts/verify-installed-pkg.ts"
    if [[ ! -f "$smoke_script" ]]; then
        log verify "FAIL: verify-installed-pkg.ts missing at $smoke_script" >&2
        phase_fail verify "smoke script missing"
        exit 5
    fi
    # b.6zq: copy the smoke script INTO $tmp_workdir before running it. The
    # runtime version loader in subprocessClient.ts (b.xsh Epic 3) uses
    # import.meta.resolve("agent-director/package.json"), which Bun resolves
    # relative to the *calling script's* location. If we run the script from
    # $REPO_ROOT/pkg/ts-bun-client/scripts/, Bun walks up and finds the
    # repo's own pkg/ts-bun-client/package.json (name="agent-director",
    # version="0.0.0") *before* $tmp_workdir/node_modules/agent-director,
    # and the smoke reports "0.0.0" against any EXPECTED_VERSION. Running
    # the script from inside $tmp_workdir makes the consumer's installed
    # (release-stamped) package.json win resolution.
    local smoke_script_in_workdir="$tmp_workdir/verify-installed-pkg.ts"
    cp "$smoke_script" "$smoke_script_in_workdir"
    # EXPECTED_VERSION tells verify-installed-pkg.ts --smoke to assert the
    # value returned by client.version() matches the release tag, catching
    # b.b3h ldflags regressions before publish.  Use $plain_v (no leading
    # "v") because subprocessClient.ts overrides the CLI's git-describe
    # stamp with the npm package.json version (b.6o1), which version-bump.ts
    # stamped from $plain_v above. (b.6oj anchor)
    if ! (cd "$tmp_workdir" && HOME="$tmp_home" EXPECTED_VERSION="$plain_v" bun "$smoke_script_in_workdir" --smoke) \
            > >(while IFS= read -r l; do printf '[verify] %s\n' "$l"; done); then
        log verify "FAIL client.version() smoke against installed tarball" >&2
        phase_fail verify "version() smoke"
        exit 5
    fi

    log verify "step 3/3: bun test pkg/ts-bun-client (in-tree)"

    # --parallel=1 because the suite deadlocks in default parallel mode (b.w7e);
    # bunfig.toml's `parallel = 1` is forward-looking and not honored by bun 1.3.13.
    if ! (cd "$REPO_ROOT/pkg/ts-bun-client" && bun install --frozen-lockfile && bun test --parallel=1) \
            > >(while IFS= read -r l; do printf '[verify] %s\n' "$l"; done); then
        log verify "FAIL bun test (in-tree pkg/ts-bun-client)" >&2
        phase_fail verify "bun test"
        exit 5
    fi

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

# publish_phase consumes the umbrella tarball and SHA-256 manifest
# produced by verify_phase — no re-stage, no re-stamp, no re-pack
# (SR-1.6).  The tarball exported via AGENT_DIRECTOR_RELEASE_SHASUMS and
# AGENT_DIRECTOR_RELEASE_STAGE_DIR is passed verbatim to `npm publish`,
# preserving byte-for-byte identity with what verify validated.
#
# b.w3q dropped the per-platform npm sub-packages, so the release ships
# one umbrella tarball — period.  The CLI binary is published separately
# as a GitHub release asset by ghrelease_phase.
#
# Phase order:
#   1. preconditions: NPM_TOKEN (live runs), manifest env vars present
#   2. gate: check-version-coherence.ts --scope publish (SHA-256 round-trip)
#   3. .npmrc write into verify stage dir
#   4. umbrella npm publish <tarball>
#   5. cleanup_npmrc_if_any
publish_phase() {
    phase_begin publish
    local plain_version="${VERSION#v}"

    # Live runs require NPM_TOKEN.  Dry-run does not, since
    # `npm publish --dry-run` does not authenticate.
    if [[ "$DRY_RUN" -eq 0 && -z "${NPM_TOKEN:-}" ]]; then
        log publish "NPM_TOKEN not set in environment" >&2
        log publish "release runner must supply NPM_TOKEN (never bake into the script)" >&2
        exit 6
    fi

    # Preconditions: verify_phase must have run and exported its artifacts.
    if [[ -z "${AGENT_DIRECTOR_RELEASE_SHASUMS:-}" ]]; then
        log publish "publish_phase requires verify_phase artifacts (AGENT_DIRECTOR_RELEASE_SHASUMS unset)" >&2
        exit 6
    fi
    if [[ -z "${AGENT_DIRECTOR_RELEASE_STAGE_DIR:-}" ]]; then
        log publish "publish_phase requires verify_phase artifacts (AGENT_DIRECTOR_RELEASE_STAGE_DIR unset)" >&2
        exit 6
    fi
    local verify_stage_dir="$AGENT_DIRECTOR_RELEASE_STAGE_DIR"
    local shasums_file="$AGENT_DIRECTOR_RELEASE_SHASUMS"
    if [[ ! -f "$shasums_file" ]]; then
        log publish "SHA-256 manifest not found: $shasums_file" >&2
        exit 6
    fi

    # Parse umbrella tarball path from the manifest (single entry post-b.w3q).
    local tgz
    tgz="$(awk 'NR==1{print $NF}' "$shasums_file")"
    if [[ -z "$tgz" || ! -f "$tgz" ]]; then
        log publish "tarball missing or unresolvable from manifest: $tgz" >&2
        exit 6
    fi
    log publish "consuming verify_phase tarball:"
    log publish "  umbrella: $tgz"

    # Gate: verify all version-stamp sites agree AND SHA-256 values match
    # the verify_phase manifest (SR-1.3 / SR-1.5 round-trip check).
    if ! (cd "$verify_stage_dir/pkg/ts-bun-client" && bun run scripts/check-version-coherence.ts \
            --scope publish \
            --expected-version "$plain_version") \
            > >(while IFS= read -r l; do printf '[publish] %s\n' "$l"; done); then
        log publish "check-version-coherence.ts --scope publish failed" >&2
        log publish "round-trip SHA-256 mismatch — see check-version-coherence output" >&2
        exit 6
    fi

    # Write a transient .npmrc with the token into the verify stage dir.
    # The EXIT trap (report_phase) calls cleanup_npmrc_if_any so the
    # file (and any extracted token) is gone even on hard exits.
    NPMRC_PATH="$verify_stage_dir/pkg/ts-bun-client/.npmrc"
    if [[ -n "${NPM_TOKEN:-}" ]]; then
        printf '//registry.npmjs.org/:_authToken=%s\nalways-auth=true\n' "$NPM_TOKEN" > "$NPMRC_PATH"
        chmod 600 "$NPMRC_PATH"
    else
        : > "$NPMRC_PATH"
    fi

    # Publish the umbrella tarball verbatim (no re-pack).  npm view detects
    # a prior publish at the same version — that path errors out so the
    # operator must increment the version for the retry.
    local pkg_dir pkg_full_name view_out
    pkg_dir="$verify_stage_dir/pkg/ts-bun-client"
    pkg_full_name=$(grep -E '^[[:space:]]*"name":' "$pkg_dir/package.json" | head -n 1 | sed -E 's/.*"name":[[:space:]]*"([^"]+)".*/\1/')
    log publish "publishing umbrella $pkg_full_name@$plain_version from $tgz"
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
            if ! (cd "$pkg_dir" && npm publish --dry-run --ignore-scripts "$tgz") \
                    > >(while IFS= read -r l; do printf '[publish] %s\n' "$l"; done); then
                log publish "FAIL $pkg_full_name (dry-run validation)" >&2
                exit 6
            fi
        else
            log publish "(dry-run) npm not on PATH — skipping packaging validation for $pkg_full_name"
        fi
    else
        if ! (cd "$pkg_dir" && npm publish "$tgz") \
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
