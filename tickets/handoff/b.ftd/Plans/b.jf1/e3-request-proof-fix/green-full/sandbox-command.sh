set -eu
umask 022
for name in $(compgen -e); do
 case "$name" in AGENT_DIRECTOR_TEST_SANDBOX) ;; AD*|AGENT_DIRECTOR_*|TMUX|TMUX_PANE|TMPDIR|GIT_CONFIG_GLOBAL) unset "$name" ;; esac
done
export HOME=/home/sandbox
out=tickets/handoff/b.ftd/Plans/b.jf1/e3-request-proof-fix/green-full
export GIT_CONFIG_GLOBAL=/work/$out/gitconfig
case "$1" in
 initial|before|after) node "$out/metadata.cjs" "$1" ;;
 focused)
  gofmt -w test/realtmux/cursor_relay_boundary_linux_test.go
  git diff --check
  result=0
  for spec in api store runtime realtmux; do
   case "$spec" in
    api) package=./pkg/api; pattern='^(TestCursorTerminalReadsPreserveRequestEvidence|TestCursorStartupAbsentBindingReads|TestCursorListLostGatewayErrorsAndValidation|TestCursorReadsWithBlockedTrail)$' ;;
    store) package=./internal/store; pattern='^(TestCursorTerminalClosePreservesRequestProof|TestCloseClosesRelayRequests|TestCursorStartupMissingConditionalWrite|TestCursorTrailProofFinalValuesAndDuplicate|TestCursorTrailProofRefusalIsSilent|TestCloseToolRanRequests|TestProveRequestsGone|TestProvenGoneRequestHoldsNothing)$' ;;
    runtime) package=./internal/cursorhost; pattern='^(TestCursorE3FatalRunClosesMissing|TestCursorE3PublicReadsReconcileKilledGateway|TestCursorExecutionProofRequiresMatchingShellResult|TestCursorProofAndAdmissionRejectStaleAuthority|TestCursorUnknownRequestRejectsGenericLifecycleProof|TestCursorGenericToolProofCannotBypassControllerValidation)$' ;;
    realtmux) package=./test/realtmux; pattern='^TestCursorRelayHeldCancellationDeadline$' ;;
   esac
   set +e
   go test "$package" -run "$pattern" -count=1 -v -timeout 120s > "$out/$spec.log" 2>&1
   code=$?
   set -e
   chmod 600 "$out/$spec.log"
   printf '%s\n' "$code" > "$out/$spec.exit"
   tail -n 8 "$out/$spec.log"
   if [ "$code" -ne 0 ]; then result=1; fi
  done
  exit "$result"
 ;;
esac
