#!/usr/bin/env bash
# Shared sandbox/Docker generation builder. SOURCE is an immutable snapshot;
# output and any Go overlays belong to BUILD, never to the shared checkout.
set -euo pipefail
[[ ${AGENT_DIRECTOR_TEST_SANDBOX:-} == 1 ]] || { echo 'sandbox required' >&2; exit 1; }
source_dir=$1
build_dir=$2
mkdir -p "$build_dir"
overlay=$(bash "$source_dir/test/adapters/mixed-hook/prepare-overlay.sh" "$source_dir" "$build_dir/overlay")
for generation in old new; do
    case "$generation" in
        old) stamp=9.0.0-mixed-old; commit=1111111111111111111111111111111111111111 ;;
        new) stamp=9.0.0-mixed-new; commit=2222222222222222222222222222222222222222 ;;
    esac
    mkdir -p "$build_dir/$generation"
    flags="-X github.com/gabemahoney/agent-director/internal/version.Version=$stamp -X github.com/gabemahoney/agent-director/internal/version.Commit=$commit"
    go -C "$source_dir" build -overlay "$overlay" -buildvcs=false -ldflags "$flags" -o "$build_dir/$generation/agent-director" ./cmd/agent-director
    go -C "$source_dir" build -buildvcs=false -ldflags "$flags" -o "$build_dir/$generation/agent-director-admin" ./cmd/agent-director-admin
    go -C "$source_dir" build -buildvcs=false -ldflags "$flags" -o "$build_dir/$generation/driver" ./test/driver/busy-send-mixed
    go -C "$source_dir" test -c -ldflags "$flags" -o "$build_dir/$generation/store-driver" ./internal/store
    "$build_dir/$generation/agent-director-admin" --home "$build_dir/retained-home" publish-version --binary "$build_dir/$generation/agent-director" > "$build_dir/$generation/publication.json"
done
