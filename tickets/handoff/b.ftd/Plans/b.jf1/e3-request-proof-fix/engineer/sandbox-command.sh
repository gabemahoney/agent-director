set -eu
umask 022
for name in $(compgen -e); do
 case "$name" in
  AGENT_DIRECTOR_TEST_SANDBOX) ;;
  AD*|AGENT_DIRECTOR_*|TMUX|TMUX_PANE|TMPDIR|GIT_CONFIG_GLOBAL) unset "$name" ;;
 esac
done
export HOME=/home/sandbox
run_dir=$(mktemp -d /tmp/e3-request-proof-red.XXXXXX)
export GIT_CONFIG_GLOBAL="$run_dir/gitconfig"
git config --file "$GIT_CONFIG_GLOBAL" --add safe.directory /work
out=tickets/handoff/b.ftd/Plans/b.jf1/e3-request-proof-fix/engineer
case "$1" in
 before|after)
 node "$out/freeze.cjs" "$1"
 ;;
 red)
 result=0
 for spec in api store runtime; do
  case "$spec" in
   api) package=./pkg/api; pattern='^(TestCursorTerminalReadsPreserveRequestEvidence|TestCursorStartupAbsentBindingReads|TestCursorListLostGatewayErrorsAndValidation|TestCursorReadsWithBlockedTrail)$' ;;
   store) package=./internal/store; pattern='^(TestCursorTerminalClosePreservesRequestProof|TestCloseClosesRelayRequests|TestCursorStartupMissingConditionalWrite)$' ;;
   runtime) package=./internal/cursorhost; pattern='^(TestCursorE3FatalRunClosesMissing|TestCursorE3PublicReadsReconcileKilledGateway)$' ;;
  esac
  set +e
  go test "$package" -run "$pattern" -count=1 -v -timeout 120s > "$out/$spec.log" 2>&1
  code=$?
  set -e
  printf '%s\n' "$code" > "$out/$spec.exit"
  tail -n 8 "$out/$spec.log"
  if [ "$code" -ne 0 ]; then result=1; fi
 done
 exit "$result"
 ;;
esac
