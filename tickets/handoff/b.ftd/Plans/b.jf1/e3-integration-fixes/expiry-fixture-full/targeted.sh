set -eu
umask 022
for name in $(compgen -e); do
  case "$name" in
    AGENT_DIRECTOR_TEST_SANDBOX) ;;
    AD*|AGENT_DIRECTOR_*|TMUX|TMUX_PANE|TMPDIR|GIT_CONFIG_GLOBAL) unset "$name" ;;
  esac
done
export HOME=/home/sandbox
own=tickets/handoff/b.ftd/Plans/b.jf1/e3-integration-fixes/expiry-fixture-full
export GIT_CONFIG_GLOBAL=/work/$own/gitconfig
node "$own/prepare.cjs"
gofmt -w internal/cursorhost/request_boundary_linux_test.go
git diff --check -- internal/cursorhost/request_boundary_linux_test.go
set +e
go test -timeout 30m ./internal/cursorhost -run '^TestCursorAcknowledgedAnswerSurvivesFiniteStoreContention$' -count=1 -v > "$own/targeted.log" 2>&1
result=$?
set -e
chmod 600 "$own/targeted.log"
printf '%s\n' "$result" > "$own/targeted-exit.txt"
tail -n 12 "$own/targeted.log"
exit "$result"
