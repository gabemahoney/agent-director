#!/bin/bash
set -eu
umask 022
unset TMPDIR TMUX TMUX_PANE
OUT=/work/tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/trail-hypothesis-measure
mkdir -p "$OUT/baseline" "$OUT/load"
touch "$OUT/gitconfig"
export HOME=/home/sandbox GIT_CONFIG_GLOBAL="$OUT/gitconfig"
cd "$OUT/_snapshot/head"
env -i PATH="$PATH" HOME=/home/sandbox GIT_CONFIG_GLOBAL="$GIT_CONFIG_GLOBAL" GOPATH=/go GOCACHE=/home/sandbox/.cache/go-build AGENT_DIRECTOR_TEST_SANDBOX=1 GOMAXPROCS=4 gofmt -w internal/trail/measure_ftd.go internal/trail/deadline.go internal/trail/trail.go internal/store/lockwait.go internal/store/connection_cleanup.go
env -i PATH="$PATH" HOME=/home/sandbox GIT_CONFIG_GLOBAL="$GIT_CONFIG_GLOBAL" GOPATH=/go GOCACHE=/home/sandbox/.cache/go-build AGENT_DIRECTOR_TEST_SANDBOX=1 GOMAXPROCS=4 FTD_MEASURE_DIR="$OUT/baseline" go test ./pkg/api -run '^(TestDecideKeepsDurableOrigin|TestRepairCheckPermission)$' -count=1 -parallel=192 -json -timeout=3m > "$OUT/baseline/results.jsonl" 2> "$OUT/baseline/stderr"
cat /sys/fs/cgroup/cpu.max /proc/pressure/cpu /proc/pressure/io > "$OUT/load/pressure-before"
pids=()
for worker in $(seq 1 32);do
 env -i PATH="$PATH" HOME=/home/sandbox GIT_CONFIG_GLOBAL="$GIT_CONFIG_GLOBAL" GOPATH=/go GOCACHE=/home/sandbox/.cache/go-build AGENT_DIRECTOR_TEST_SANDBOX=1 GOMAXPROCS=192 FTD_MEASURE_DIR="$OUT/load" go test ./pkg/api -run '^(TestDecideKeepsDurableOrigin|TestRepairCheckPermission)$' -count=1 -parallel=192 -json -timeout=3m > "$OUT/load/worker-$worker.jsonl" 2> "$OUT/load/worker-$worker.stderr" &
 pids+=("$!")
done
failed=0
for pid in "${pids[@]}";do if wait "$pid";then echo "$pid PASS";else echo "$pid FAIL";failed=1;fi;done
cat /sys/fs/cgroup/cpu.max /proc/pressure/cpu /proc/pressure/io > "$OUT/load/pressure-after"
exit "$failed"
