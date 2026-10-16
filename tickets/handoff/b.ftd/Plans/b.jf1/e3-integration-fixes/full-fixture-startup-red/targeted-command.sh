set -eu
evidence=tickets/handoff/b.ftd/Plans/b.jf1/e3-integration-fixes/full-fixture-startup-red
run_test() {
  label=$1
  shift
  if [ -e "$evidence/$label.log" ]; then
    printf 'Refusing to overwrite preserved attempt: %s\n' "$label" >&2
    exit 2
  fi
  set +e
  "$@" > "$evidence/$label.log" 2>&1
  result=$?
  set -e
  chmod 600 "$evidence/$label.log"
  printf '%s\n' "$result" > "$evidence/$label-exit-code.txt"
  cat "$evidence/$label.log"
}
case "$1" in
  format)
    gofmt -w internal/cursorhost/session_controls_linux_test.go test/realtmux/cursor_launch_test.go test/realtmux/cursor_relay_boundary_linux_test.go test/realtmux/cursor_startup_liveness_linux_test.go test/smoke/ts-helper/fake_pane_owner.go test/smoke/ts-helper/main.go pkg/api/cursor_liveness_test.go pkg/api/cursor_liveness_list_test.go pkg/api/cursor_startup_liveness_test.go
    ;;
  fixtures)
    run_test fifo-attempt-1 go test ./internal/cursorhost -run '^TestCursorFIFOExactTextAndLifecycle$' -count=1 -v -timeout 60s
    run_test pane-attempt-1 go test ./test/realtmux -run '^(TestCursorIndependentPaneVerifierRejectsChangedOwnership|TestCursorRelayHeldCancellationDeadline)$' -count=1 -v -timeout 120s
    run_test bun-attempt-1 bash -c 'cd pkg/ts-bun-client && bun test test/client-serialization.test.ts --test-name-pattern "^Cursor "'
    ;;
  startup)
    run_test startup-real-red-attempt-1 go test ./test/realtmux -run '^TestCursorStartupExecutableFailurePublicReads$' -count=1 -v -timeout 60s
    run_test startup-api-red-attempt-1 go test ./pkg/api -run '^(TestCursorStartupAbsentBindingReads|TestCursorStartupAbsentBindingFailures|TestCursorReadLostGatewayRefusesUnsafeSuccess|TestCursorListLostGatewayErrorsAndValidation)$' -count=1 -v -timeout 60s
    ;;
  store)
    gofmt -w internal/store/cursor_startup_liveness_test.go
    run_test startup-store-red-attempt-1 go test ./internal/store -run '^TestCursorStartupMissingConditionalWrite$' -count=1 -v -timeout 60s
    ;;
esac
