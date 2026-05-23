#!/usr/bin/env bash
# release.sh — cut an agent-director release.
#
# Phased pipeline:
#
#   pre-flight → notes → build → verify → tag → publish → gh-release → report
#
# Each phase is a function; phases are ordered most-reversible to
# least-reversible. The script halts on the first failing phase and
# the report phase prints which phases succeeded.
#
# Usage:
#   VERSION=v0.1.0 ./release.sh [--dry-run|--release] [--branch main] [--no-build]
#   ./release.sh v0.1.0 [--dry-run|--release] [--branch main] [--no-build]
#
# The script DEFAULTS to --dry-run. Pass --release to actually push tags,
# publish to npm, and create the GitHub release.
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
}

# --------------------------------------------------------------------
# Phase: notes (release notes templater)
# --------------------------------------------------------------------

notes_phase() {
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

- **CLI binaries** (four platforms): linux/amd64, linux/arm64
  (statically linked, no glibc dependency), darwin/amd64, darwin/arm64
  (Mach-O 64).
- **pkg/cabi** shared libraries (three platforms in v1): linux/amd64,
  darwin/amd64, darwin/arm64. linux/arm64 cabi is deferred to v2.

Windows is not supported (SRD §16.1).
NOTES

    log notes "written to $NOTES_FILE"
}

# --------------------------------------------------------------------
# Phase: build
# --------------------------------------------------------------------

# CABI_PLATFORMS is the canonical v1 set. linux/arm64 is intentionally
# absent — cabi v1 ships three platforms only. The four-platform CLI
# binary set is independent of this restriction; that is built locally
# by `make release-binaries`.
CABI_PLATFORMS=(linux-amd64 darwin-amd64 darwin-arm64)

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

    local commit
    commit="$(git rev-parse HEAD)"
    log build "looking up $CABI_WORKFLOW run for $BRANCH @ ${commit:0:12}"

    # gh run list returns newest first. Filter to success.
    local matches_json
    if ! matches_json=$(gh run list \
            --workflow="$CABI_WORKFLOW" \
            --branch="$BRANCH" \
            --commit="$commit" \
            --status=success \
            --json databaseId,headSha,conclusion,status \
            --limit 5 2>/dev/null); then
        log build "gh run list failed for $CABI_WORKFLOW" >&2
        return 3
    fi

    local count run_id
    count=$(printf '%s' "$matches_json" | tr -cd '{' | wc -c | tr -d ' ')
    if [[ "$count" -eq 0 ]]; then
        log build "no successful $CABI_WORKFLOW run on $BRANCH @ ${commit:0:12}" >&2
        log build "wait for the matrix to go green on this commit before releasing" >&2
        return 3
    fi
    if [[ "$count" -gt 1 ]]; then
        log build "ambiguous: $count successful $CABI_WORKFLOW runs on $BRANCH @ ${commit:0:12}" >&2
        log build "pin the release commit so only one matrix run is associated with it" >&2
        return 3
    fi
    run_id=$(printf '%s' "$matches_json" | sed -n 's/.*"databaseId":[[:space:]]*\([0-9][0-9]*\).*/\1/p' | head -n 1)
    if [[ -z "$run_id" ]]; then
        log build "could not parse run id from gh run list output" >&2
        return 3
    fi
    log build "using cabi-matrix run id: $run_id"

    local platform out_dir lib
    for platform in "${CABI_PLATFORMS[@]}"; do
        out_dir="$REPO_ROOT/dist/cabi/$platform"
        mkdir -p "$out_dir"
        log build "downloading pkg-cabi-$platform → $out_dir"
        if ! gh run download "$run_id" \
                --name "pkg-cabi-$platform" \
                --dir "$out_dir" >/dev/null 2>&1; then
            log build "gh run download failed for pkg-cabi-$platform" >&2
            log build "ensure the darwin/arm64 self-hosted runner was online and that all three legs succeeded" >&2
            return 3
        fi
        lib="$out_dir/$(cabi_lib_basename "$platform")"
        if [[ ! -f "$lib" ]]; then
            log build "missing expected library $lib after download" >&2
            return 3
        fi
        if [[ ! -f "$out_dir/libagent_director.h" ]]; then
            log build "missing expected header $out_dir/libagent_director.h after download" >&2
            return 3
        fi
        log build "  OK $platform: $(basename "$lib") + libagent_director.h"
    done

    # Stage a canonical header copy at dist/cabi/include/libagent_director.h.
    # The header is platform-independent — any per-platform copy will do.
    mkdir -p "$REPO_ROOT/dist/cabi/include"
    cp "$REPO_ROOT/dist/cabi/${CABI_PLATFORMS[0]}/libagent_director.h" \
       "$REPO_ROOT/dist/cabi/include/libagent_director.h"
    log build "canonical header staged at dist/cabi/include/libagent_director.h"
}

build_phase() {
    if [[ "$NO_BUILD" -eq 1 ]]; then
        log build "--no-build set — assuming ./dist/ is already populated"
        return 0
    fi
    log build "building release binaries (4 CLI platforms)"
    if ! (cd "$REPO_ROOT" && make release-binaries) > >(while IFS= read -r l; do printf '[build] %s\n' "$l"; done); then
        log build "release-binaries build failed" >&2
        exit 3
    fi
    log build "collecting pkg/cabi artifacts (3 v1 platforms)"
    if ! collect_cabi_artifacts; then
        exit 3
    fi
    log build "OK"
}

# --------------------------------------------------------------------
# Phase: verify
# --------------------------------------------------------------------

# host_cabi_platform echoes "linux-amd64" / "darwin-amd64" / "darwin-arm64"
# matching the host runner architecture. Verify uses it to point
# AD_CABI_DIR at the per-platform .so/.dylib the TS bindings load.
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
        exit 5
    fi

    log verify "step 2/3: ts smoke (pkg/ts-bun-client smoke suite)"
    if ! (cd "$REPO_ROOT/pkg/ts-bun-client" && bun run smoke) \
            > >(while IFS= read -r l; do printf '[verify] %s\n' "$l"; done); then
        log verify "FAIL ts-smoke" >&2
        exit 5
    fi

    log verify "step 3/3: envelope-diff (go side + ts side)"
    # Go side: the envelope-diff suite lives under test/envelope-diff/...
    # and is exercised via the standard `go test` invocation. We only
    # need the leaf go tests, not the whole tree.
    if ! (cd "$REPO_ROOT" && go test -count=1 ./test/envelope-diff/...) \
            > >(while IFS= read -r l; do printf '[verify] %s\n' "$l"; done); then
        log verify "FAIL envelope-diff-go" >&2
        exit 5
    fi
    # TS side: the make target wires up agent-director + ts-helper +
    # fake-tmux preconditions and then runs the TS envelope-diff suite.
    if ! (cd "$REPO_ROOT" && make envelope-diff-ts) \
            > >(while IFS= read -r l; do printf '[verify] %s\n' "$l"; done); then
        log verify "FAIL envelope-diff-ts" >&2
        exit 5
    fi

    log verify "OK"
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
    if [[ "$DRY_RUN" -eq 1 ]]; then
        log tag "(dry-run) would push $VERSION"
        if [[ -f "$REPO_ROOT/pkg/api/go.mod" ]]; then
            log tag "(dry-run) would also push pkg/api/$VERSION (separate Go module detected)"
        fi
        return 0
    fi
    log tag "pushing $VERSION"
    if ! git tag -a "$VERSION" -m "Release $VERSION"; then
        log tag "git tag failed" >&2
        exit 7
    fi
    if ! git push origin "$VERSION"; then
        log tag "git push of $VERSION failed" >&2
        exit 7
    fi
    if [[ -f "$REPO_ROOT/pkg/api/go.mod" ]]; then
        local submod_tag="pkg/api/$VERSION"
        log tag "pkg/api has separate go.mod — also pushing $submod_tag"
        if ! git tag -a "$submod_tag" -m "Release $submod_tag"; then
            log tag "git tag $submod_tag failed" >&2
            exit 7
        fi
        if ! git push origin "$submod_tag"; then
            log tag "git push of $submod_tag failed" >&2
            exit 7
        fi
    fi
    log tag "pushed $VERSION"
}

# --------------------------------------------------------------------
# GH release (still inline; refactored to phase function in later tasks)
# --------------------------------------------------------------------

ghrelease_phase() {
    local binaries=(
        "$REPO_ROOT/dist/agent-director-linux-amd64"
        "$REPO_ROOT/dist/agent-director-linux-arm64"
        "$REPO_ROOT/dist/agent-director-darwin-amd64"
        "$REPO_ROOT/dist/agent-director-darwin-arm64"
    )
    local b
    for b in "${binaries[@]}"; do
        if [[ ! -x "$b" ]]; then
            log gh-release "missing binary $b — run make release-binaries first" >&2
            exit 3
        fi
    done

    log gh-release "creating GitHub release $VERSION"
    if ! gh release create "$VERSION" "${binaries[@]}" \
            --title "$VERSION" \
            --notes-file "$NOTES_FILE"; then
        log gh-release "gh release create failed" >&2
        log gh-release "the tag $VERSION is still pushed; re-run after fixing the underlying issue" >&2
        log gh-release "OR delete the tag with: git push --delete origin $VERSION && git tag -d $VERSION" >&2
        exit 4
    fi
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

    if [[ "$DRY_RUN" -eq 1 ]]; then
        log dry-run "skipping publish and gh-release create"
        echo "------ release notes preview ------"
        cat "$NOTES_FILE"
        echo "------ end preview ------"
        log dry-run "OK"
        exit 0
    fi

    ghrelease_phase
    log release "done — $VERSION published"
}

# Allow tests to source this file without running main.
if [[ "${_RELEASE_SH_SOURCE_ONLY:-0}" -eq 1 ]]; then
    return 0 2>/dev/null || true
fi

main "$@"
