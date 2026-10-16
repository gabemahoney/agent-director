set -eu
umask 022
for name in $(compgen -e); do
 case "$name" in
 AGENT_DIRECTOR_TEST_SANDBOX) ;;
 AGENT_DIRECTOR_*|TMUX|TMUX_PANE|TMPDIR|GIT_CONFIG_GLOBAL) unset "$name" ;;
 esac
done
export HOME=/home/sandbox
run_dir=$(mktemp -d /tmp/e3-read-lock-red.XXXXXX)
export GIT_CONFIG_GLOBAL="$run_dir/gitconfig"
git config --file "$GIT_CONFIG_GLOBAL" --add safe.directory /work
evidence=tickets/handoff/b.ftd/Plans/b.jf1/e3-integration-fixes/read-lock-red
set +e
go test ./pkg/api -run '^TestCursorReadsRefuseWriterContention$' -count=1 -v -timeout 60s > "$evidence/red.log" 2>&1
result=$?
set -e
chmod 600 "$evidence/red.log"
printf '%s\n' "$result" > "$evidence/go-test-exit-code.txt"
cat "$evidence/red.log"
exit "$result"
