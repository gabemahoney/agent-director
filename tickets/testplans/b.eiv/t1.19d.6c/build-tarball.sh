#!/usr/bin/env bash
# Source this from each t2 case script to populate $TARBALL with a path to
# a locally-packed agent-director-*.tgz built from /work/source.
#
# /work/source is mounted read-only, so the package is staged into a fresh
# /tmp tree that preserves the repo's relative layout (the build hooks
# import `../../../../pkg/api/errnames/catalog.json` and stage-skill.ts
# reads `${REPO_ROOT}/skills/install-agent-director/`).
#
# Also exports $PLATFORM_PKG_DIR — a path to the @agent-director/linux-x64
# sub-package directory. The umbrella's optionalDependencies entry for the
# platform package uses `file:./platforms/linux-x64`, which is a dev-tree
# path that doesn't exist after the published tarball is unpacked. Cases
# bind-mount this dir into their consumer node_modules so the Client's
# `import.meta.resolve('@agent-director/linux-x64/package.json')` step in
# resolveCliPath() finds the bundled binary.
#
# The result is cached at /tmp/ad-tarball-cache so the cost is paid once
# per docker run (4 cases × 1 cache miss + 3 hits).

TARBALL_CACHE=/tmp/ad-tarball-cache
TARBALL=$(ls "$TARBALL_CACHE"/agent-director-*.tgz 2>/dev/null | head -1 || true)
if [ -z "$TARBALL" ]; then
    BUILD_ROOT=$(mktemp -d)
    mkdir -p "$BUILD_ROOT/pkg"
    cp -a /work/source/pkg/ts-bun-client "$BUILD_ROOT/pkg/"
    cp -a /work/source/pkg/api          "$BUILD_ROOT/pkg/"
    cp -a /work/source/skills           "$BUILD_ROOT/"
    PKG_BUILD="$BUILD_ROOT/pkg/ts-bun-client"
    rm -rf "$PKG_BUILD/node_modules" "$PKG_BUILD/dist" "$PKG_BUILD/skills"
    ( cd "$PKG_BUILD" && bun install --silent >/dev/null 2>&1 )
    mkdir -p "$TARBALL_CACHE"
    ( cd "$PKG_BUILD" && npm pack --pack-destination "$TARBALL_CACHE" >/dev/null )
    TARBALL=$(ls "$TARBALL_CACHE"/agent-director-*.tgz | head -1)
    [ -n "$TARBALL" ] || { echo "FAIL: tarball build produced no .tgz"; exit 1; }
fi
export TARBALL
export PLATFORM_PKG_DIR=/work/source/pkg/ts-bun-client/platforms/linux-x64

# stage_platform_pkg copies the linux-x64 platform sub-package into the
# given consumer project's node_modules/@agent-director/linux-x64/. Call
# this AFTER `bun install "file://${TARBALL}"` so the umbrella has already
# created node_modules/.
stage_platform_pkg() {
    local proj="$1"
    local dest="$proj/node_modules/@agent-director/linux-x64"
    mkdir -p "$dest"
    cp -a "$PLATFORM_PKG_DIR"/. "$dest/"
    chmod +x "$dest/bin/agent-director"
}
export -f stage_platform_pkg
