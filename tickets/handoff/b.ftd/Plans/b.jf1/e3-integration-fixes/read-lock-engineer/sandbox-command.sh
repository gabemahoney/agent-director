set -eu
umask 022
for name in $(compgen -e); do
  case "$name" in
    AGENT_DIRECTOR_TEST_SANDBOX) ;;
    AD*|AGENT_DIRECTOR_*|TMUX|TMUX_PANE|TMPDIR|GIT_CONFIG_GLOBAL) unset "$name" ;;
  esac
done
export HOME=/home/sandbox
run_dir=$(mktemp -d /tmp/e3-read-lock-engineer.XXXXXX)
export GIT_CONFIG_GLOBAL="$run_dir/gitconfig"
git config --file "$GIT_CONFIG_GLOBAL" --add safe.directory /work
evidence=tickets/handoff/b.ftd/Plans/b.jf1/e3-integration-fixes/read-lock-engineer
node "$evidence/frozen-proof.cjs" before
gofmt -w internal/store/find_missing_writes.go internal/store/lockwait.go internal/store/request_close.go internal/store/recovery.go internal/store/cursor_runtime.go pkg/api/list.go
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
  if [ "$result" -ne 0 ]; then exit "$result"; fi
}
run_test held-lock-green go test ./pkg/api -run '^TestCursorReadsRefuseWriterContention$' -count=1 -v -timeout 60s
run_test original-green go test ./internal/cursorhost -run '^(TestCursorE3ToolUpdateBeforePlanAdmission|TestCursorE3FatalRunClosesMissing|TestCursorE3PublicReadsReconcileKilledGateway)$' -count=1 -v -timeout 90s
run_test list-green go test ./pkg/api -run '^(TestCursorListReconcilesGatewayBeforeProjectionAndFilters|TestCursorListLostGatewayErrorsAndValidation)$' -count=1 -v -timeout 90s
run_test api-guards-green go test ./pkg/api -run '^(TestCursorReadGatewayVerdicts|TestCursorReadLostGatewayRefusesUnsafeSuccess|TestCursorReadConcurrentLaunchChange)$' -count=1 -v -timeout 90s
run_test store-targeted-green go test ./internal/store -run '^(TestCursorMissingRefusesReplacedAuthority|TestAppliedWriteChangesOnlyItsColumns|TestFindMissingIfSameLifeRefused|TestMarkEndedRefusedWithoutEndReport|TestMarkEndedClosesRecordsAtOneInstant|TestCloseClosesRelayRequests|TestCloseWritesNothingUnlessItsWriteApplies|TestDecisionAbsoluteDeadlineAtSQLiteBoundaries|TestDecisionExpiredContextCannotRenewAcquisitionOrZeroWait|TestDecisionZeroWaitAllowsSQLBeyondAcquisitionAllowance)$' -count=1 -v -timeout 90s
git diff --check
git diff -- internal/store/find_missing_writes.go internal/store/lockwait.go internal/store/request_close.go internal/store/recovery.go internal/store/cursor_runtime.go pkg/api/list.go > "$evidence/source-changes.patch"
node "$evidence/frozen-proof.cjs" after
