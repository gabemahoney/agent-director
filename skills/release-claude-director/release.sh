#!/usr/bin/env bash
# release.sh — cut a claude-director release.
#
# Per SRD §16.1 + §16.4. Validates semver, ensures the working tree
# is clean, tags the commit, builds the four supported binaries via
# the Makefile, generates release notes templated from git log, and
# creates the GitHub release with the artifacts attached.
#
# Usage:
#   VERSION=v0.1.0 ./release.sh [--dry-run] [--branch main] [--no-build]
#   ./release.sh v0.1.0 [--dry-run] [--branch main] [--no-build]
#
# Exit codes:
#   0  success
#   2  pre-flight failure (bad version, dirty tree, missing gh, etc.)
#   3  build failure
#   4  GitHub release create failure

set -euo pipefail

# --------------------------------------------------------------------
# Flag parsing
# --------------------------------------------------------------------

DRY_RUN=0
BRANCH="main"
NO_BUILD=0
VERSION="${VERSION:-}"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run)  DRY_RUN=1; shift ;;
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
# Pre-flight
# --------------------------------------------------------------------

# Semver: v?MAJOR.MINOR.PATCH only. No pre-release tags in v1.
if [[ -z "$VERSION" ]]; then
    echo "release.sh: VERSION is required (e.g. v0.1.0)" >&2
    exit 2
fi
# Normalize: accept 0.1.0 and v0.1.0 both as v0.1.0.
[[ "$VERSION" == v* ]] || VERSION="v$VERSION"
if ! [[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "release.sh: $VERSION is not strict semver (MAJOR.MINOR.PATCH)" >&2
    echo "  pre-release tags (e.g. v0.1.0-rc1) are not supported in v1" >&2
    exit 2
fi

# `gh` must be on PATH (we use it for the release create).
if ! command -v gh >/dev/null 2>&1; then
    echo "release.sh: 'gh' (GitHub CLI) not found on PATH" >&2
    echo "  install via your package manager and run 'gh auth login'" >&2
    exit 2
fi

# Working tree must be clean.
if [[ -n "$(git status --porcelain)" ]]; then
    echo "release.sh: working tree is dirty — commit or stash first" >&2
    git status --short >&2
    exit 2
fi

# Tag must not exist.
if git tag --list | grep -qx "$VERSION"; then
    echo "release.sh: tag $VERSION already exists" >&2
    echo "  to retry: git push --delete origin $VERSION && git tag -d $VERSION" >&2
    exit 2
fi

# Must be on the configured branch.
current_branch="$(git rev-parse --abbrev-ref HEAD)"
if [[ "$current_branch" != "$BRANCH" ]]; then
    echo "release.sh: current branch is '$current_branch', want '$BRANCH'" >&2
    echo "  use --branch <name> to release from a different branch" >&2
    exit 2
fi

echo "release.sh: pre-flight OK"
echo "  version : $VERSION"
echo "  branch  : $BRANCH"
echo "  dry-run : $DRY_RUN"

# --------------------------------------------------------------------
# Generate release notes from git log
# --------------------------------------------------------------------

PREV_TAG=$(git tag --list "v*.*.*" --sort=-version:refname | head -n 1 || true)
if [[ -n "$PREV_TAG" ]]; then
    LOG_RANGE="${PREV_TAG}..HEAD"
    echo "  prev    : $PREV_TAG"
else
    LOG_RANGE="HEAD"
    echo "  prev    : (none — first release)"
fi

REPO_ROOT="$(git rev-parse --show-toplevel)"
mkdir -p "$REPO_ROOT/dist"
NOTES_FILE="$REPO_ROOT/dist/release-notes.md"
cat > "$NOTES_FILE" <<NOTES
# $VERSION

Released $(date -u +'%Y-%m-%d').

## What's in this release

$(git log "$LOG_RANGE" --pretty=format:'%s' \
    | awk '
        # Group commits by Epic ID parsed from "(Task N / Epic E.tX.Y)"
        # or "(Epic E.tX.Y)" or "[t1.E.Y]" patterns. Lines without a
        # match go into a default "Other" group preserving order.
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
            # Print groups by first-appearance order.
            n = 0
            for (k in order) { keys[++n] = k; ord[k] = order[k] }
            # Simple insertion sort by ord[]
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
curl -L -o claude-director https://github.com/<owner>/<repo>/releases/download/$VERSION/claude-director-darwin-arm64
chmod +x claude-director

# Linux amd64:
curl -L -o claude-director https://github.com/<owner>/<repo>/releases/download/$VERSION/claude-director-linux-amd64
chmod +x claude-director
\`\`\`

## Supported platforms

- linux/amd64, linux/arm64 (statically linked, no glibc dependency)
- darwin/amd64, darwin/arm64 (Mach-O 64)

Windows is not supported (SRD §16.1).
NOTES

echo "release.sh: notes written to $NOTES_FILE"

# --------------------------------------------------------------------
# Dry run exits here
# --------------------------------------------------------------------

if [[ "$DRY_RUN" -eq 1 ]]; then
    echo "release.sh: --dry-run — skipping tag, build, and release create"
    echo "------ release notes preview ------"
    cat "$NOTES_FILE"
    echo "------ end preview ------"
    exit 0
fi

# --------------------------------------------------------------------
# Tag + push
# --------------------------------------------------------------------

echo "release.sh: tagging $VERSION on $current_branch"
git tag -a "$VERSION" -m "Release $VERSION"
git push origin "$VERSION"

# --------------------------------------------------------------------
# Build the four binaries
# --------------------------------------------------------------------

if [[ "$NO_BUILD" -eq 0 ]]; then
    echo "release.sh: building release binaries"
    if ! (cd "$REPO_ROOT" && make release-binaries); then
        echo "release.sh: release-binaries build failed" >&2
        exit 3
    fi
fi

# --------------------------------------------------------------------
# Create the GitHub release
# --------------------------------------------------------------------

binaries=(
    "$REPO_ROOT/dist/claude-director-linux-amd64"
    "$REPO_ROOT/dist/claude-director-linux-arm64"
    "$REPO_ROOT/dist/claude-director-darwin-amd64"
    "$REPO_ROOT/dist/claude-director-darwin-arm64"
)
for b in "${binaries[@]}"; do
    if [[ ! -x "$b" ]]; then
        echo "release.sh: missing binary $b — run make release-binaries first" >&2
        exit 3
    fi
done

echo "release.sh: creating GitHub release $VERSION"
if ! gh release create "$VERSION" "${binaries[@]}" \
    --title "$VERSION" \
    --notes-file "$NOTES_FILE"; then
    echo "release.sh: gh release create failed" >&2
    echo "  the tag $VERSION is still pushed; re-run after fixing the underlying issue" >&2
    echo "  OR delete the tag with: git push --delete origin $VERSION && git tag -d $VERSION" >&2
    exit 4
fi

echo "release.sh: done — $VERSION published"
