set -eu
umask 022
for name in $(compgen -e); do
 case "$name" in AGENT_DIRECTOR_TEST_SANDBOX) ;; AD*|AGENT_DIRECTOR_*|TMUX|TMUX_PANE|TMPDIR|GIT_CONFIG_GLOBAL) unset "$name" ;; esac
done
export HOME=/home/sandbox
own=tickets/handoff/b.ftd/Plans/b.jf1/e3-integration-fixes/api-fixture-isolation-full
export GIT_CONFIG_GLOBAL=/work/$own/gitconfig
set +e
go test -timeout 30m ./pkg/api -run '^(TestCursorReadsRefuseWriterContention|TestCursorStartupAbsentBindingReads|TestCursorStartupAbsentBindingFailures|TestUnknownOriginHoldAfterReopen|TestEveryTestDeclaresParallelOrSerial)$' -count=1 -v > "$own/targeted.log" 2>&1
result=$?
set -e
chmod 600 "$own/targeted.log"
printf "%s\n" "$result" > "$own/targeted-exit.txt"
tail -n 20 "$own/targeted.log"
exit "$result"
