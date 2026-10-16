set -eu
umask 022
for name in $(compgen -e); do
 case "$name" in
  AGENT_DIRECTOR_TEST_SANDBOX) ;;
  AD*|AGENT_DIRECTOR_*|TMUX|TMUX_PANE|TMPDIR|GIT_CONFIG_GLOBAL) unset "$name" ;;
 esac
done
export HOME=/home/sandbox
run_dir=$(mktemp -d /tmp/e3-startup-engineer.XXXXXX)
export GIT_CONFIG_GLOBAL="$run_dir/gitconfig"
git config --file "$GIT_CONFIG_GLOBAL" --add safe.directory /work
out=tickets/handoff/b.ftd/Plans/b.jf1/e3-integration-fixes/startup-engineer
run_test() {
 label=$1
 shift
 test ! -e "$out/$label.log"
 set +e
 "$@" > "$out/$label.log" 2>&1
 result=$?
 set -e
 chmod 600 "$out/$label.log"
 printf '%s\n' "$result" > "$out/$label.exit"
 tail -n 6 "$out/$label.log"
 test "$result" -eq 0
}
case "$1" in
 startup)
 run_test startup-real-green go test ./test/realtmux -run '^TestCursorStartupExecutableFailurePublicReads$' -count=1 -v -timeout 60s
 run_test startup-api-green go test ./pkg/api -run '^(TestCursorStartupAbsentBindingReads|TestCursorStartupAbsentBindingFailures|TestCursorReadLostGatewayRefusesUnsafeSuccess|TestCursorListLostGatewayErrorsAndValidation)$' -count=1 -v -timeout 60s
 run_test startup-store-green go test ./internal/store -run '^TestCursorStartupMissingConditionalWrite$' -count=1 -v -timeout 60s
 ;;
 guards)
 run_test original-green go test ./internal/cursorhost -run '^(TestCursorE3ToolUpdateBeforePlanAdmission|TestCursorE3FatalRunClosesMissing|TestCursorE3PublicReadsReconcileKilledGateway)$' -count=1 -v -timeout 90s
 run_test api-guards-green go test ./pkg/api -run '^(TestCursorListReconcilesGatewayBeforeProjectionAndFilters|TestCursorReadGatewayVerdicts|TestCursorReadConcurrentLaunchChange|TestCursorReadsRefuseWriterContention|TestCursorReadsWithBlockedTrail)$' -count=1 -v -timeout 90s
 run_test store-guards-green go test ./internal/store -run '^TestCursorMissingRefusesReplacedAuthority$' -count=1 -v -timeout 60s
 ;;
esac
git diff --check -- internal/store/cursor_runtime.go pkg/api/cursor_liveness.go
