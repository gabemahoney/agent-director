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
git diff -- internal/cursorhost/request_boundary_linux_test.go > "$own/fixture.patch"
chmod 600 "$own/fixture.patch"
node "$own/finish.cjs"
