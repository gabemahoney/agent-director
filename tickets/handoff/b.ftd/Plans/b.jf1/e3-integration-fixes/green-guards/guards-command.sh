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
gofmt -w pkg/api/cursor_liveness_test.go internal/store/cursor_runtime_liveness_test.go
go test ./pkg/api -run '^(TestCursorReadGatewayVerdicts|TestCursorReadLostGatewayRefusesUnsafeSuccess|TestCursorReadConcurrentLaunchChange)$' -count=1 -v -timeout 90s
go test ./internal/store -run '^TestCursorMissingRefusesReplacedAuthority$' -count=1 -v -timeout 90s
