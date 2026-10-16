set -eu
umask 022
for name in $(compgen -e); do
  case "$name" in
    AGENT_DIRECTOR_TEST_SANDBOX) ;;
    AD*|AGENT_DIRECTOR_*|TMUX|TMUX_PANE|TMPDIR|GIT_CONFIG_GLOBAL) unset "$name" ;;
  esac
done
export HOME=/home/sandbox
run_dir=$(mktemp -d /tmp/e3-bounded-green.XXXXXX)
export GIT_CONFIG_GLOBAL="$run_dir/gitconfig"
git config --file "$GIT_CONFIG_GLOBAL" --add safe.directory /work
evidence=tickets/handoff/b.ftd/Plans/b.jf1/e3-integration-fixes/bounded-green
run_test() {
  label=$1
  shift
  set +e
  "$@" > "$evidence/$label.log" 2>&1
  result=$?
  set -e
  chmod 600 "$evidence/$label.log"
  printf '%s\n' "$result" > "$evidence/$label-exit-code.txt"
  cat "$evidence/$label.log"
  test "$result" -eq 0
}
if [ "${1:-}" = regressions ]; then
  node "$evidence/proof.cjs" before
  run_test held-lock-green go test ./pkg/api -run '^TestCursorReadsRefuseWriterContention$' -count=1 -v -timeout 60s
  run_test original-green go test ./internal/cursorhost -run '^(TestCursorE3ToolUpdateBeforePlanAdmission|TestCursorE3FatalRunClosesMissing|TestCursorE3PublicReadsReconcileKilledGateway)$' -count=1 -v -timeout 90s
  run_test list-and-guards-green go test ./pkg/api -run '^(TestCursorListReconcilesGatewayBeforeProjectionAndFilters|TestCursorListLostGatewayErrorsAndValidation|TestCursorReadGatewayVerdicts|TestCursorReadLostGatewayRefusesUnsafeSuccess|TestCursorReadConcurrentLaunchChange)$' -count=1 -v -timeout 90s
  run_test store-guards-green go test ./internal/store -run '^TestCursorMissingRefusesReplacedAuthority$' -count=1 -v -timeout 60s
else
  gofmt -w pkg/api/cursor_liveness_trail_deadline_test.go
  run_test trail-green go test ./pkg/api -run '^TestCursorReadsWithBlockedTrail$' -count=1 -v -timeout 60s
  git diff --check -- pkg/api/cursor_liveness_trail_deadline_test.go
fi
node "$evidence/proof.cjs" after
