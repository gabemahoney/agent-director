#!/usr/bin/env bash
set -euo pipefail
umask 022
unset TMPDIR TMUX TMUX_PANE
localdir=$(mktemp -d /tmp/decide-review.XXXXXX)
touch "$localdir/gitconfig"
exec env -i HOME=/home/sandbox PATH="$PATH" GOPATH=/go GOCACHE=/home/sandbox/.cache/go-build AGENT_DIRECTOR_TEST_SANDBOX=1 GIT_CONFIG_GLOBAL="$localdir/gitconfig" REVIEW_DIR="$localdir" bash -euo pipefail -c '
 bun tools/measure-decide/source-overlay.mjs --root /work --output "$REVIEW_DIR/source"
 git diff --exit-code -- internal/store pkg/api internal/trail > tickets/handoff/b.ftd/Plans/b.jf1/e4-task1/diagnostic/pass-03/canonical-diff.patch
 bash -n tools/measure-decide/diagnose.sh tools/measure-decide/calibrate-acquisition.sh
 bun tools/measure-decide/review-inputs.mjs
'
