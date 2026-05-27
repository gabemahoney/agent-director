#!/usr/bin/env bash
# stage-cli.sh — CLI platform staging helper.
# Source this file; it defines CLI_PLATFORMS and stage_cli_into_platforms.
# Caller must set REPO_ROOT and define log() before invoking stage_cli_into_platforms.
# No set -e / set -u — callers manage shell options.

# CLI_PLATFORMS maps cross-compile target triples to the npm
# sub-package directory name that ships the binary. The CLI cross-
# compile produces ./dist/agent-director-<os>-<arch>; we stage each
# into pkg/ts-bun-client/platforms/<npm-subdir>/bin/agent-director.
CLI_PLATFORMS=(
    "linux-amd64=linux-x64"
    "darwin-arm64=darwin-arm64"
)

# stage_cli_into_platforms copies the cross-compiled binaries into
# the corresponding pkg/ts-bun-client/platforms/<dir>/bin/agent-director.
# Called from build_phase after `make release-binaries`. The release-
# binaries-smoke target verifies the binaries themselves; this helper
# only does the staging.
stage_cli_into_platforms() {
    local entry src npm_subdir dest_dir
    for entry in "${CLI_PLATFORMS[@]}"; do
        local cross="${entry%=*}"
        npm_subdir="${entry#*=}"
        src="$REPO_ROOT/dist/agent-director-${cross}"
        dest_dir="$REPO_ROOT/pkg/ts-bun-client/platforms/$npm_subdir/bin"
        if [[ ! -f "$src" ]]; then
            log build "missing $src — was make release-binaries run?" >&2
            return 1
        fi
        mkdir -p "$dest_dir"
        cp "$src" "$dest_dir/agent-director"
        chmod 0755 "$dest_dir/agent-director"
        log build "  staged $src → $dest_dir/agent-director"
    done
}
