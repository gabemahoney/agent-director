set -eu
umask 022
for name in $(compgen -e); do
  case "$name" in
    AGENT_DIRECTOR_TEST_SANDBOX) ;;
    AGENT_DIRECTOR_*|TMUX|TMUX_PANE|TMPDIR|GIT_CONFIG_GLOBAL) unset "$name" ;;
  esac
done
export HOME=/home/sandbox
run_dir=$(mktemp -d /tmp/commit-branch-green.XXXXXX)
export GIT_CONFIG_GLOBAL="$run_dir/gitconfig"
git config --file "$GIT_CONFIG_GLOBAL" --add safe.directory /work
go test ./internal/store -run '^(TestDecisionUnconfirmedCommitBranch|TestDecisionAbsoluteDeadlineAtSQLiteBoundaries|TestAdviceFollow_F10_UnconfirmedCommitInspectBeforeRetry)$' -count=1 -v -timeout 60s
go test ./pkg/api -run '^(TestRepairCheckPermission|TestDecideKeepsDurableOrigin)$' -count=1 -v -timeout 90s
