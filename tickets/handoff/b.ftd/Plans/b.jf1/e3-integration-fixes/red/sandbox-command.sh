set -eu
umask 022
for name in $(compgen -e); do
  case "$name" in
    AGENT_DIRECTOR_TEST_SANDBOX) ;;
    AGENT_DIRECTOR_*|TMUX|TMUX_PANE|TMPDIR|GIT_CONFIG_GLOBAL) unset "$name" ;;
  esac
done
export HOME=/home/sandbox
run_dir=$(mktemp -d /tmp/e3-regression-red.XXXXXX)
export GIT_CONFIG_GLOBAL="$run_dir/gitconfig"
git config --file "$GIT_CONFIG_GLOBAL" --add safe.directory /work
gofmt -w internal/cursorhost/session_e3_regression_linux_test.go internal/testsupport/cursorfix/cursorfix.go
go test ./internal/cursorhost -run '^(TestCursorE3ToolUpdateBeforePlanAdmission|TestCursorE3FatalRunClosesMissing|TestCursorE3PublicReadsReconcileKilledGateway)$' -count=1 -v -timeout 90s
