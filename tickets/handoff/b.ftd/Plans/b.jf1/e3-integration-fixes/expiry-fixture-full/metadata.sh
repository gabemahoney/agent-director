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
node "$own/metadata.cjs" "$1"
if [ "$1" = after ]; then
  node "$own/finalize.cjs"
  node "$own/finish.cjs"
fi
