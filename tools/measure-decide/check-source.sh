#!/usr/bin/env bash
# Source-only compile validation. No experiments and no test suite execution.
set -euo pipefail
umask 022
unset TMPDIR TMUX TMUX_PANE
export HOME=/home/sandbox
run_dir=$(mktemp -d /tmp/measure-decide-source.XXXXXX)
touch "$run_dir/gitconfig"
exec env -i HOME=/home/sandbox PATH="$PATH" GOPATH=/go GOCACHE=/home/sandbox/.cache/go-build \
  GIT_CONFIG_GLOBAL="$run_dir/gitconfig" RUN_DIR="$run_dir" bash -euo pipefail -c '
    bun tools/measure-decide/source-overlay.mjs --root /work --output "$RUN_DIR/overlay"
    go build -overlay "$RUN_DIR/overlay/overlay.json" ./pkg/api ./internal/store ./internal/trail
    report_dir=tickets/handoff/b.ftd/Plans/b.jf1/e4-task1/engineer
    mkdir -p "$report_dir"
    cp "$RUN_DIR/overlay/provenance.json" "$report_dir/source-provenance.json"
    : > "$report_dir/observation.patch"
    for file in pkg/api/decide.go internal/store/lockwait.go internal/store/connection_cleanup.go internal/store/permission.go internal/trail/deadline.go internal/trail/trail.go; do
      diff -u --label "baseline/$file" --label "diagnostic/$file" "$file" "$RUN_DIR/overlay/$file" >> "$report_dir/observation.patch" || test "$?" -eq 1
    done
    find pkg/api internal/store internal/trail -name .claude -prune -o -type f -name "*_test.go" -print0 | sort -z | xargs -0 sha256sum > "$report_dir/canonical-tests.sha256"
    echo "SOURCE_COMPILE_OK overlay=$RUN_DIR/overlay"
  '
