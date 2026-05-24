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
# Environment escape hatches (for local rehearsal / Docker testplan):
#   AD_RELEASE_SKIP_CABI=1   skip `gh run download` of pkg-cabi-* artifacts
#                            (useful when no green cabi-matrix run exists,
#                            e.g. running release.sh on a feature branch)
#   AD_RELEASE_SIMULATE_DARWIN_ARM64_OFFLINE=1
#                            simulate the darwin/arm64 self-hosted runner
#                            being offline (covered by T9)
#
# Exit codes:
#   0  success
#   2  pre-flight failure (bad version, dirty tree, missing gh, etc.)
#   3  build failure (release-binaries, cabi-collection)
#   4  GitHub release create failure
#   5  verify-phase failure (go smoke, ts smoke, envelope-diff)
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
            log report "  → rebuild artifacts; verify the cabi-matrix run is green on the release commit"
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

cleanup_npmrc_if_any() {
    if [[ -n "${NPMRC_PATH:-}" && -f "$NPMRC_PATH" ]]; then
        rm -f "$NPMRC_PATH"
    fi
}

# restore_pkg_jsons_if_dryrun rolls back the in-place mutations the
# publish phase makes to package.json files when running in dry-run.
# Live runs intentionally leave the mutations in place — the rewritten
# versions are part of the published artifacts. Dry-run must leave the
# workspace clean so the script can be re-run without a pre-flight
# "working tree is dirty" failure.
restore_pkg_jsons_if_dryrun() {
    if [[ "${DRY_RUN:-1}" -ne 1 ]]; then
        return 0
    fi
    if [[ -z "${REPO_ROOT:-}" ]]; then
        return 0
    fi
    local f
    for f in \
        pkg/ts-bun-client/package.json \
        pkg/ts-bun-client/platforms/linux-x64/package.json \
        pkg/ts-bun-client/platforms/darwin-arm64/package.json; do
        if [[ -f "$REPO_ROOT/$f" ]] && git -C "$REPO_ROOT" diff --quiet -- "$f" 2>/dev/null; then
            continue
        fi
        git -C "$REPO_ROOT" checkout -- "$f" >/dev/null 2>&1 || true
    done
}

report_phase() {
    local rc=$?
    cleanup_npmrc_if_any
    restore_pkg_jsons_if_dryrun
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

    # gh required for live runs. Dry-run still needs it for `gh run list`
    # in the cabi-collection step, but tolerates missing gh by skipping
    # that helper (build_phase exposes a NO_CABI escape hatch — set via
    # AD_RELEASE_SKIP_CABI=1 — for local previews without auth'd gh).
    if ! command -v gh >/dev/null 2>&1; then
        if [[ "$DRY_RUN" -eq 0 ]]; then
            log preflight "'gh' (GitHub CLI) not found on PATH" >&2
            log preflight "install via your package manager and run 'gh auth login'" >&2
            exit 2
        fi
        log preflight "'gh' not on PATH — dry-run will skip cabi-artifact collection" >&2
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
- **pkg/cabi** shared libraries (two platforms in v1): linux/amd64,
  darwin/arm64. linux/arm64 cabi is deferred to v2; darwin/amd64
  was dropped on 2026-05-24.

Windows is not supported (SRD §16.1).
NOTES

    log notes "written to $NOTES_FILE"

    # Append the toolchain-pin diff section if any pins changed since
    # the previous release tag. SRD §SR-2.3 requires release notes
    # surface these changes; the diff helper is silent when there is
    # nothing to report so the appended block is a no-op for releases
    # that did not bump a pin.
    local diff_helper="$REPO_ROOT/skills/release-agent-director/toolchain-pin-diff.sh"
    if [[ -x "$diff_helper" ]]; then
        local pin_diff_section
        if pin_diff_section=$(TOOLCHAIN_DIFF_PREV_LABEL="${PREV_TAG:-previous release}" \
                "$diff_helper" "${PREV_TAG:-}" 2>/dev/null) && [[ -n "$pin_diff_section" ]]; then
            printf '%s\n' "$pin_diff_section" >> "$NOTES_FILE"
            log notes "appended toolchain-pin diff section"
        else
            log notes "no toolchain-pin changes vs ${PREV_TAG:-(none)}"
        fi
    else
        log notes "toolchain-pin-diff.sh missing/not-executable; skipping pin diff"
    fi
    phase_ok notes
}

# --------------------------------------------------------------------
# Phase: build
# --------------------------------------------------------------------

# CABI_PLATFORMS is the canonical v1 set. linux/arm64 and darwin/amd64
# are intentionally absent — cabi v1 ships two platforms only
# (darwin/amd64 dropped 2026-05-24 per operator decision). The CLI
# binary set is independent of this restriction; that is built locally
# by `make release-binaries`.
CABI_PLATFORMS=(linux-amd64 darwin-arm64)

# cabi_lib_basename echoes the per-platform shared-library filename.
# linux uses .so, darwin uses .dylib. Used by both collect_cabi_artifacts
# and the gh-release phase (T5) when listing assets.
cabi_lib_basename() {
    local platform="$1"
    case "$platform" in
        linux-*)  echo "libagent_director.so" ;;
        darwin-*) echo "libagent_director.dylib" ;;
        *) log build "unknown cabi platform: $platform" >&2; return 1 ;;
    esac
}

# collect_cabi_artifacts: find the green cabi-matrix run on the release
# commit and `gh run download` each pkg-cabi-<platform> artifact into
# ./dist/cabi/<platform>/. Halts on zero or ambiguous matches.
collect_cabi_artifacts() {
    : "${CABI_WORKFLOW:=cabi-matrix.yml}"

    if [[ "${AD_RELEASE_SKIP_CABI:-0}" -eq 1 ]]; then
        log build "AD_RELEASE_SKIP_CABI=1 — skipping cabi artifact collection"
        return 0
    fi

    if ! command -v gh >/dev/null 2>&1; then
        # Pre-flight allowed dry-run to proceed without gh; honor that.
        if [[ "$DRY_RUN" -eq 1 ]]; then
            log build "gh not on PATH — dry-run skipping cabi artifact collection"
            return 0
        fi
        log build "gh not on PATH — cannot download cabi artifacts" >&2
        return 3
    fi

    local commit run_id
    commit="$(git rev-parse HEAD)"

    # RELEASE_FORCE_RUN_ID=<id> is a debug-only override used by tests
    # to drive collect_cabi_artifacts against a known run (e.g. a
    # known-red run to exercise the matrix-red halt). Production
    # releases must NOT set it.
    if [[ -n "${RELEASE_FORCE_RUN_ID:-}" ]]; then
        run_id="$RELEASE_FORCE_RUN_ID"
        log build "RELEASE_FORCE_RUN_ID set — using run id $run_id (debug override)"
        # Even with a forced id, re-check the conclusion: a green
        # release MUST NOT be cut against a red matrix run.
        local forced_concl
        forced_concl=$(gh run view "$run_id" --json conclusion -q .conclusion 2>/dev/null || true)
        if [[ "$forced_concl" != "success" ]]; then
            log build "CI matrix is red on \$COMMIT (run $run_id conclusion=$forced_concl); fix before releasing" >&2
            return 3
        fi
    else
        log build "looking up $CABI_WORKFLOW run for $BRANCH @ ${commit:0:12}"

        # Two lookups so we can distinguish "no run exists" from "run
        # exists but is red" — the latter gets a more actionable error
        # ("CI matrix is red on $COMMIT") than the former ("no run").
        local all_runs_json success_runs_json count_all count_ok concl
        if ! all_runs_json=$(gh run list \
                --workflow="$CABI_WORKFLOW" \
                --branch="$BRANCH" \
                --commit="$commit" \
                --json databaseId,headSha,conclusion,status \
                --limit 5 2>/dev/null); then
            log build "gh run list failed for $CABI_WORKFLOW" >&2
            return 3
        fi
        success_runs_json=$(gh run list \
                --workflow="$CABI_WORKFLOW" \
                --branch="$BRANCH" \
                --commit="$commit" \
                --status=success \
                --json databaseId,headSha,conclusion,status \
                --limit 5 2>/dev/null || true)

        count_all=$(printf '%s' "$all_runs_json" | tr -cd '{' | wc -c | tr -d ' ')
        count_ok=$(printf '%s' "$success_runs_json" | tr -cd '{' | wc -c | tr -d ' ')

        if [[ "$count_all" -eq 0 ]]; then
            log build "no $CABI_WORKFLOW run on $BRANCH @ ${commit:0:12}" >&2
            log build "wait for the matrix to run on this commit before releasing" >&2
            return 3
        fi
        if [[ "$count_ok" -eq 0 ]]; then
            concl=$(printf '%s' "$all_runs_json" | sed -n 's/.*"conclusion":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
            log build "CI matrix is red on \$COMMIT (conclusion=${concl:-unknown}); fix before releasing" >&2
            return 3
        fi
        if [[ "$count_ok" -gt 1 ]]; then
            log build "ambiguous: $count_ok successful $CABI_WORKFLOW runs on $BRANCH @ ${commit:0:12}" >&2
            log build "pin the release commit so only one matrix run is associated with it" >&2
            return 3
        fi
        run_id=$(printf '%s' "$success_runs_json" | sed -n 's/.*"databaseId":[[:space:]]*\([0-9][0-9]*\).*/\1/p' | head -n 1)
        if [[ -z "$run_id" ]]; then
            log build "could not parse run id from gh run list output" >&2
            return 3
        fi
        log build "using cabi-matrix run id: $run_id (conclusion=success)"
    fi

    # Accumulate platforms that failed to download/verify so we can
    # emit ONE summary message that distinguishes the darwin/arm64
    # self-hosted-runner case (specific operator action) from the
    # generic GitHub-hosted leg failures.
    local platform out_dir lib
    local missing_platforms=()
    for platform in "${CABI_PLATFORMS[@]}"; do
        out_dir="$REPO_ROOT/dist/cabi/$platform"
        mkdir -p "$out_dir"

        # AD_RELEASE_SIMULATE_DARWIN_ARM64_OFFLINE=1 simulates the
        # darwin/arm64 self-hosted runner being offline. The cabi-matrix
        # run might still exist (with the leg failing) or might not have
        # the artifact at all. Either way the release script must halt
        # with the specific self-hosted-runner message. This flag is
        # exercised by the Docker testplan.
        if [[ "$platform" == "darwin-arm64" \
              && "${AD_RELEASE_SIMULATE_DARWIN_ARM64_OFFLINE:-0}" -eq 1 ]]; then
            log build "AD_RELEASE_SIMULATE_DARWIN_ARM64_OFFLINE=1 — pretending darwin/arm64 artifact is absent"
            missing_platforms+=("darwin-arm64")
            continue
        fi

        log build "downloading pkg-cabi-$platform → $out_dir"
        if ! gh run download "$run_id" \
                --name "pkg-cabi-$platform" \
                --dir "$out_dir" >/dev/null 2>&1; then
            missing_platforms+=("$platform")
            continue
        fi
        lib="$out_dir/$(cabi_lib_basename "$platform")"
        if [[ ! -f "$lib" ]]; then
            log build "missing expected library $lib after download" >&2
            missing_platforms+=("$platform")
            continue
        fi
        if [[ ! -f "$out_dir/libagent_director.h" ]]; then
            log build "missing expected header $out_dir/libagent_director.h after download" >&2
            missing_platforms+=("$platform")
            continue
        fi
        log build "  OK $platform: $(basename "$lib") + libagent_director.h"
    done

    # Refuse to ship a partial 2-of-3 under any circumstance.
    if (( ${#missing_platforms[@]} > 0 )); then
        local p
        for p in "${missing_platforms[@]}"; do
            if [[ "$p" == "darwin-arm64" ]]; then
                log build "darwin/arm64 leg unavailable; bring self-hosted runner online before retrying" >&2
            else
                log build "$p leg unavailable; re-run CI matrix before retrying" >&2
            fi
        done
        log build "halting before tag/publish/gh-release — no partial 2-of-3 release" >&2
        return 3
    fi

    # Stage a canonical header copy at dist/cabi/include/libagent_director.h.
    # The header is platform-independent — any per-platform copy will do.
    mkdir -p "$REPO_ROOT/dist/cabi/include"
    cp "$REPO_ROOT/dist/cabi/${CABI_PLATFORMS[0]}/libagent_director.h" \
       "$REPO_ROOT/dist/cabi/include/libagent_director.h"
    log build "canonical header staged at dist/cabi/include/libagent_director.h"
}

build_phase() {
    phase_begin build
    if [[ "$NO_BUILD" -eq 1 ]]; then
        log build "--no-build set — assuming ./dist/ is already populated"
        # Even with --no-build, cabi collection still runs unless the
        # caller ALSO set AD_RELEASE_SKIP_CABI=1. This decouples the
        # two escape hatches so the Docker testplan can exercise the
        # cabi-collection halt paths (matrix-red, darwin/arm64 offline)
        # without paying the local `make release-binaries` cost.
        if [[ "${AD_RELEASE_SKIP_CABI:-0}" -ne 1 ]]; then
            log build "running collect_cabi_artifacts (cabi collection still active under --no-build)"
            if ! collect_cabi_artifacts; then
                phase_fail build "collect_cabi_artifacts failed"
                exit 3
            fi
        fi
        log build "OK"
        phase_ok build
        return 0
    fi
    log build "building release binaries (3 CLI platforms)"
    if ! (cd "$REPO_ROOT" && make release-binaries) > >(while IFS= read -r l; do printf '[build] %s\n' "$l"; done); then
        log build "release-binaries build failed" >&2
        phase_fail build "release-binaries failed"
        exit 3
    fi
    log build "collecting pkg/cabi artifacts (2 v1 platforms)"
    if ! collect_cabi_artifacts; then
        phase_fail build "collect_cabi_artifacts failed"
        exit 3
    fi
    log build "OK"
    phase_ok build
}

# --------------------------------------------------------------------
# Phase: verify
# --------------------------------------------------------------------

# host_cabi_platform echoes "linux-amd64" / "darwin-arm64" matching the
# host runner architecture. Verify uses it to point AD_CABI_DIR at the
# per-platform .so/.dylib the TS bindings load. darwin/amd64 hosts are
# rejected as unsupported under v1 (dropped 2026-05-24).
host_cabi_platform() {
    local os arch
    case "$(uname -s)" in
        Linux)  os=linux ;;
        Darwin) os=darwin ;;
        *) log verify "unsupported host OS: $(uname -s)" >&2; return 1 ;;
    esac
    case "$(uname -m)" in
        x86_64|amd64) arch=amd64 ;;
        arm64|aarch64) arch=arm64 ;;
        *) log verify "unsupported host arch: $(uname -m)" >&2; return 1 ;;
    esac
    if [[ "$os" == "linux" && "$arch" == "arm64" ]]; then
        log verify "host is linux/arm64 — no v1 cabi build for this host; smoke + envelope-diff will run against dev-checkout libs" >&2
        return 1
    fi
    if [[ "$os" == "darwin" && "$arch" == "amd64" ]]; then
        log verify "host is darwin/amd64 — dropped from v1 (2026-05-24); smoke + envelope-diff will run against dev-checkout libs" >&2
        return 1
    fi
    echo "${os}-${arch}"
}

# verify_phase runs:
#   1. Go smoke    — test/smoke/go (Epic 4)
#   2. TS smoke    — pkg/ts-bun-client (Epic 5)
#   3. envelope-diff — Go side + TS side (Epics 3 + 5)
#
# Each step that fails halts release with exit code 5 and a clear
# [verify] FAIL <step> message naming the failed sub-step.
verify_phase() {
    phase_begin verify
    local host_plat
    if host_plat=$(host_cabi_platform); then
        AD_CABI_DIR="$REPO_ROOT/dist/cabi/$host_plat"
        export AD_CABI_DIR
        log verify "AD_CABI_DIR=$AD_CABI_DIR (host cabi platform: $host_plat)"
    else
        log verify "no host cabi platform — TS smoke + envelope-diff will fall back to dev-checkout libs" >&2
    fi

    log verify "step 1/3: go smoke (./test/smoke/go/...)"
    if ! (cd "$REPO_ROOT" && go test -race -count=1 ./test/smoke/go/...) \
            > >(while IFS= read -r l; do printf '[verify] %s\n' "$l"; done); then
        log verify "FAIL go-smoke" >&2
        phase_fail verify "go-smoke"
        exit 5
    fi

    log verify "step 2/3: ts smoke (pkg/ts-bun-client smoke suite)"
    if ! (cd "$REPO_ROOT/pkg/ts-bun-client" && bun run smoke) \
            > >(while IFS= read -r l; do printf '[verify] %s\n' "$l"; done); then
        log verify "FAIL ts-smoke" >&2
        phase_fail verify "ts-smoke"
        exit 5
    fi

    log verify "step 3/3: envelope-diff (go side + ts side)"
    # Go side: the envelope-diff suite lives under test/envelope-diff/...
    # and is exercised via the standard `go test` invocation. We only
    # need the leaf go tests, not the whole tree.
    if ! (cd "$REPO_ROOT" && go test -count=1 ./test/envelope-diff/...) \
            > >(while IFS= read -r l; do printf '[verify] %s\n' "$l"; done); then
        log verify "FAIL envelope-diff-go" >&2
        phase_fail verify "envelope-diff-go"
        exit 5
    fi
    # TS side: the make target wires up agent-director + ts-helper +
    # fake-tmux preconditions and then runs the TS envelope-diff suite.
    if ! (cd "$REPO_ROOT" && make envelope-diff-ts) \
            > >(while IFS= read -r l; do printf '[verify] %s\n' "$l"; done); then
        log verify "FAIL envelope-diff-ts" >&2
        phase_fail verify "envelope-diff-ts"
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

# Maps cabi-matrix platform names to npm sub-package directory names
# (the optional-dependencies live under pkg/ts-bun-client/platforms/<dir>/).
# The cabi-matrix names use `amd64` while npm convention uses `x64`.
npm_subdir_for_platform() {
    case "$1" in
        linux-amd64)   echo "linux-x64" ;;
        darwin-arm64)  echo "darwin-arm64" ;;
        *) log publish "unknown cabi platform: $1" >&2; return 1 ;;
    esac
}

# stage_cabi_into_platforms copies the downloaded dist/cabi/<platform>/
# libagent_director.{so,dylib} into the corresponding
# pkg/ts-bun-client/platforms/<npm-subdir>/ directory so `npm publish`
# picks it up. The cabi header is not part of the npm packages — only
# the binary plus the existing README-binary-source.md.
stage_cabi_into_platforms() {
    local platform npm_subdir src_lib dest_dir lib_name
    for platform in "${CABI_PLATFORMS[@]}"; do
        npm_subdir=$(npm_subdir_for_platform "$platform") || return 1
        lib_name=$(cabi_lib_basename "$platform") || return 1
        src_lib="$REPO_ROOT/dist/cabi/$platform/$lib_name"
        dest_dir="$REPO_ROOT/pkg/ts-bun-client/platforms/$npm_subdir"
        if [[ ! -f "$src_lib" ]]; then
            if [[ "${AD_RELEASE_SKIP_CABI:-0}" -eq 1 || "$NO_BUILD" -eq 1 ]]; then
                log publish "(staging) missing $src_lib — skipping under AD_RELEASE_SKIP_CABI/--no-build"
                continue
            fi
            log publish "missing cabi artifact $src_lib — was build phase run?" >&2
            return 1
        fi
        log publish "staging $src_lib → $dest_dir/$lib_name"
        cp "$src_lib" "$dest_dir/$lib_name"
    done
}

publish_phase() {
    phase_begin publish
    local plain_version="${VERSION#v}"

    local pkg_root="$REPO_ROOT/pkg/ts-bun-client"
    local pkg_json="$pkg_root/package.json"
    if [[ ! -f "$pkg_json" ]]; then
        log publish "missing $pkg_json — TS package layout invariant violated" >&2
        exit 6
    fi

    # ----------------------------------------------------------------
    # H3 gate: must be the FIRST action inside publish_phase so no
    # `npm publish` ever runs against a placeholder name.
    #
    # We inspect ALL THREE package.jsons (umbrella + 2 per-platform
    # sub-packages). Any one carrying the H3 sentinel halts the live
    # run. The sentinel matches `@CHANGEME-H3/...` and the alternative
    # `@TBD/...` so that whichever placeholder convention E5 ultimately
    # settled on is caught.
    #
    # Manual verification procedure (for operator rehearsal):
    #   1. Temporarily revert one of the three package.json files to a
    #      placeholder name (e.g. `git checkout HEAD~N -- <file>`).
    #   2. Run `./release.sh v<X.Y.Z> --release`.
    #   3. Observe the [publish] H3 halt with exit code 6 BEFORE any
    #      `npm publish` is invoked.
    #   4. `git checkout -- <file>` to restore and retry.
    # ----------------------------------------------------------------
    local h3_sentinel_re='^@?(CHANGEME-H3|TBD)/'
    local p3 name3 placeholder_pkgs=()
    for p3 in \
        "$pkg_json" \
        "$pkg_root/platforms/linux-x64/package.json" \
        "$pkg_root/platforms/darwin-arm64/package.json"; do
        if [[ ! -f "$p3" ]]; then
            log publish "missing $p3 — TS package layout invariant violated" >&2
            exit 6
        fi
        name3=$(grep -E '^[[:space:]]*"name":' "$p3" | head -n 1 | sed -E 's/.*"name":[[:space:]]*"([^"]+)".*/\1/')
        if [[ "$name3" =~ $h3_sentinel_re ]]; then
            placeholder_pkgs+=("${p3#$REPO_ROOT/}=$name3")
        fi
    done

    if (( ${#placeholder_pkgs[@]} > 0 )); then
        if [[ "$DRY_RUN" -eq 0 ]]; then
            log publish "H3 unresolved: claim npm name first" >&2
            log publish "the following ${#placeholder_pkgs[@]} package.json file(s) still carry a placeholder name:" >&2
            local pp
            for pp in "${placeholder_pkgs[@]}"; do
                log publish "  ${pp%%=*}  →  ${pp##*=}" >&2
            done
            log publish "see docs/release-blockers.md for the H3 resolution checklist" >&2
            exit 6
        fi
        log publish "(dry-run) H3 unresolved (${#placeholder_pkgs[@]} package.json with placeholder names) — would halt in a live run"
    fi

    local pkg_name
    pkg_name=$(grep -E '^[[:space:]]*"name":' "$pkg_json" | head -n 1 | sed -E 's/.*"name":[[:space:]]*"([^"]+)".*/\1/')

    # Live runs require NPM_TOKEN. Dry-run does not, since
    # `npm publish --dry-run` and `npm pack` don't authenticate.
    if [[ "$DRY_RUN" -eq 0 && -z "${NPM_TOKEN:-}" ]]; then
        log publish "NPM_TOKEN not set in environment" >&2
        log publish "release runner must supply NPM_TOKEN (never bake into the script)" >&2
        exit 6
    fi

    # Stage cabi binaries into the per-platform npm directories.
    if ! stage_cabi_into_platforms; then
        exit 6
    fi

    # Rewrite version on every package.json (umbrella + 2 platforms).
    log publish "stamping version $plain_version onto umbrella + 2 platform package.jsons"
    local p target_json
    for p in "$pkg_json" \
             "$pkg_root/platforms/linux-x64/package.json" \
             "$pkg_root/platforms/darwin-arm64/package.json"; do
        target_json="$p"
        if [[ ! -f "$target_json" ]]; then
            log publish "missing $target_json" >&2
            exit 6
        fi
        # In-place rewrite the top-level "version" key. sed is sufficient
        # because version is a simple scalar; we keep formatting stable.
        if ! sed -i.bak -E "s/(^[[:space:]]*\"version\":[[:space:]]*\")[^\"]+(\")/\1${plain_version}\2/" "$target_json"; then
            log publish "failed to rewrite version in $target_json" >&2
            exit 6
        fi
        rm -f "${target_json}.bak"
    done

    # version-bump the optional-deps file: pins → ^version registry pins.
    log publish "rewriting optionalDependencies file: pins to ^$plain_version"
    if ! (cd "$pkg_root" && bun run scripts/version-bump.ts --version "$plain_version") \
            > >(while IFS= read -r l; do printf '[publish] %s\n' "$l"; done); then
        log publish "version-bump.ts failed" >&2
        exit 6
    fi

    # Write a transient .npmrc with the token, used by `npm publish`.
    # The EXIT trap below (report_phase) calls cleanup_npmrc_if_any so
    # the file (and any extracted token) is gone even on hard exits —
    # do NOT install a separate EXIT trap here; that would override the
    # report-phase trap installed at the top of the script.
    NPMRC_PATH="$pkg_root/.npmrc"
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
    local pkg_dir pkg_subname pkg_full_name view_out
    for plat_subdir in linux-x64 darwin-arm64; do
        pkg_dir="$pkg_root/platforms/$plat_subdir"
        pkg_full_name=$(grep -E '^[[:space:]]*"name":' "$pkg_dir/package.json" | head -n 1 | sed -E 's/.*"name":[[:space:]]*"([^"]+)".*/\1/')
        log publish "publishing $pkg_full_name@$plain_version"
        # npm view is a live registry lookup; skip in dry-run so the
        # script does not need network access during local rehearsal.
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
                # --ignore-scripts skips prepublishOnly. The per-package
                # check-not-placeholder.ts guard is redundant during dry-run
                # because publish_phase already executed the H3 check
                # explicitly above; running it again here would always fail
                # while H3 is unresolved and break the dry-run pipeline.
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
    pkg_full_name=$(grep -E '^[[:space:]]*"name":' "$pkg_json" | head -n 1 | sed -E 's/.*"name":[[:space:]]*"([^"]+)".*/\1/')
    log publish "publishing umbrella $pkg_full_name@$plain_version"
    if [[ "$DRY_RUN" -eq 0 ]] && command -v npm >/dev/null 2>&1; then
        view_out=$(cd "$pkg_root" && npm view "${pkg_full_name}@${plain_version}" version 2>/dev/null || true)
        if [[ -n "$view_out" ]]; then
            log publish "$pkg_full_name@$plain_version is already published" >&2
            log publish "version already published, increment version for retry" >&2
            exit 6
        fi
    fi
    if [[ "$DRY_RUN" -eq 1 ]]; then
        if command -v npm >/dev/null 2>&1; then
            if ! (cd "$pkg_root" && npm publish --dry-run --ignore-scripts) \
                    > >(while IFS= read -r l; do printf '[publish] %s\n' "$l"; done); then
                log publish "FAIL $pkg_full_name (dry-run validation)" >&2
                exit 6
            fi
        else
            log publish "(dry-run) npm not on PATH — skipping packaging validation for $pkg_full_name"
        fi
    else
        if ! (cd "$pkg_root" && npm publish) \
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

# release_assets builds the canonical asset list:
#   3 CLI binaries (darwin/amd64 dropped 2026-05-24 — see CABI_PLATFORMS)
#   2 cabi shared libraries (.so/.dylib — one per v1 cabi platform)
#   1 platform-independent C header (canonical copy from T1)
#
# Returns assets via the global RELEASE_ASSETS array so callers can
# iterate. Exits the script with code 4 if any expected asset is
# missing on disk.
release_assets() {
    RELEASE_ASSETS=(
        "$REPO_ROOT/dist/agent-director-linux-amd64"
        "$REPO_ROOT/dist/agent-director-linux-arm64"
        "$REPO_ROOT/dist/agent-director-darwin-arm64"
        "$REPO_ROOT/dist/cabi/linux-amd64/libagent_director.so"
        "$REPO_ROOT/dist/cabi/darwin-arm64/libagent_director.dylib"
        "$REPO_ROOT/dist/cabi/include/libagent_director.h"
    )
    local a missing=0
    for a in "${RELEASE_ASSETS[@]}"; do
        if [[ ! -f "$a" ]]; then
            # Dry-run can legitimately reach gh-release without every
            # artifact present (e.g. --no-build or AD_RELEASE_SKIP_CABI).
            # Live runs treat any missing asset as fatal.
            if [[ "$DRY_RUN" -eq 1 ]]; then
                log gh-release "(dry-run) missing asset $a — would be required in live run"
                missing=$((missing + 1))
            else
                log gh-release "missing asset $a — was build_phase + collect_cabi_artifacts run?" >&2
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
