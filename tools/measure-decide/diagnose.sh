#!/usr/bin/env bash
set -euo pipefail
umask 022
unset TMPDIR TMUX TMUX_PANE
export HOME=/home/sandbox
base=/work/tickets/handoff/b.ftd/Plans/b.jf1/e4-task1/diagnostic/pass-03
mkdir -p "$base"
localdir=$(mktemp -d /tmp/measure-decide.XXXXXX)
touch "$localdir/gitconfig"
exec env -i HOME=/home/sandbox PATH="$PATH" GOPATH=/go GOCACHE=/home/sandbox/.cache/go-build AGENT_DIRECTOR_TEST_SANDBOX=1 GIT_CONFIG_GLOBAL="$localdir/gitconfig" BASE="$base" bash -euo pipefail -c '
 action=$1; shift
 cap=$(date -u -d "2026-10-16 13:26:43Z" +%s)
 now=$(date -u +%s)
 if ((now>=cap));then echo "aggregate diagnostic cap reached";exit 124;fi
 # Refuse accidental uncapped target execution.
 test "$(cat /sys/fs/cgroup/memory.swap.max)" = 0 || { echo "no-swap cap required";exit 2;}
 memory=$(cat /sys/fs/cgroup/memory.max); pids=$(cat /sys/fs/cgroup/pids.max)
 [[ "$memory" != max && "$memory" -le 17179869184 && "$pids" != max && "$pids" -le 1024 ]] || { echo "memory/pid caps required";exit 2;}
 read quota period < /sys/fs/cgroup/cpu.max
 [[ "$quota" != max && "$quota" -le $((8*period)) ]] || { echo "at most 8 CPU quota required";exit 2;}
 case "$action" in
 build)
  mode=$1; package=$2; race=$3
  dir="$BASE/_build-$package-$mode-$race"
  test ! -e "$dir";mkdir -p "$dir"
  bun tools/measure-decide/diagnostic-overlay.mjs "$dir/overlay" "$mode" > "$dir/generate.log" 2>&1
  go version > "$dir/go-version.txt"
  flags=();if [[ "$race" = race* ]];then flags=(-race);fi
  pkg=./pkg/api;if test "$package" = store;then pkg=./internal/store;fi
  budget=$((cap-$(date -u +%s)));if ((budget>1200));then budget=1200;fi
  ulimit -f 524288
  bun tools/measure-decide/supervise.mjs "${BASE%/pass-03}" "$dir" build "$budget" go test -vet=off "${flags[@]}" -c -overlay "$dir/overlay/overlay.json" -o "$dir/target.test" "$pkg" > "$dir/build.log" 2>&1
  sha256sum "$dir/target.test" > "$dir/build.sha256"
  ;;
 run)
  name=$1; package=$2; mode=$3; race=$4; selection=$5
  kind=${6:-calibration}
  identity_fault=${7:-}
  case "$identity_fault" in ""|missing|truncated|invalid_utf8|duplicate|wrong_process) ;; *)exit 2;;esac
  if test "$kind" = natural && test -n "$identity_fault";then exit 2;fi
  if test "$kind" = natural;then
   test "$package" = api && test "$mode" = on && test "$selection" = "^TestCursorListReconcilesGatewayBeforeProjectionAndFilters$/^gone_working$" || exit 2
  fi
  [[ "$kind" = calibration || "$kind" = natural ]] || exit 2
  test ! -e "${BASE%/pass-03}/natural-admission-stop.json" || { echo "natural admission stopped";exit 2;}
  case "$name" in *[!a-zA-Z0-9_-]*) exit 2;;esac
  dir="$BASE/$name";test ! -e "$dir";mkdir -p "$dir"
  build="$BASE/_build-$package-$mode-$race"
  cp "$build/build.sha256" "$dir/build.sha256"
  cp "$build/overlay/driver-provenance.json" "$dir/driver-provenance.json"
  date -u +%FT%TZ > "$dir/start.txt"
  for resource in cpu.max cpu.stat memory.max memory.swap.max memory.events pids.max cpu.pressure io.pressure memory.pressure;do
   if test -r "/sys/fs/cgroup/$resource";then cat "/sys/fs/cgroup/$resource" > "$dir/$resource.before";fi
  done
  sed -n "/Cpus_allowed_list/p" /proc/self/status > "$dir/affinity.txt"
  stat -f -c "%T" /work > "$dir/filesystem.txt"
  package_name=api_test;if test "$package" = store;then package_name=store;fi
  printf "{\"expected_processes\":[{\"package\":\"%s\",\"mode\":\"%s\",\"count\":1}]}\n" "$package_name" "$mode" > "$dir/run-inventory.json"
  budget=$((cap-$(date -u +%s)));if ((budget>90));then budget=90;fi
  set +e
  DECIDE_REPORT="$dir" DECIDE_TRACE="$dir/trace.jsonl" DECIDE_IDENTITY_FAULT="$identity_fault" bun tools/measure-decide/supervise.mjs "${BASE%/pass-03}" "$dir" "$kind" "$budget" "$build/target.test" -test.timeout=90s -test.failfast -test.run "$selection" -test.count=1 -test.v > "$dir/supervisor.log" 2>&1
  code=$?
  set -e
  printf "%s\n" "$code" > "$dir/exit-code.txt"
  date -u +%FT%TZ > "$dir/end.txt"
  for resource in cpu.stat memory.events cpu.pressure io.pressure memory.pressure;do
   if test -r "/sys/fs/cgroup/$resource";then cat "/sys/fs/cgroup/$resource" > "$dir/$resource.after";fi
  done
  jq -r ".peak.retained" "$dir/supervisor-result.json" > "$dir/retained-aggregate-bytes.txt"
  bun tools/measure-decide/check-traces.mjs "$dir" > "$dir/trace-check.log" 2>&1 || code=1
  if test "$code" != 0 && test "$kind" = natural;then
   test -e "${BASE%/pass-03}/natural-admission-stop.json" || printf "{\"reason\":\"natural failure or invalid trace\",\"directory\":\"%s\"}\n" "$dir" > "${BASE%/pass-03}/natural-admission-stop.json"
  fi
  printf "%s\n" "$code" > "$dir/final-exit-code.txt"
  cat "$dir/test.log"
  exit "$code"
  ;;
 full)
  mode=$1
  dir="$BASE/fullcontext-$mode";test ! -e "$dir";mkdir -p "$dir"
  # Hidden overlay directory is excluded from go package discovery.
  bun tools/measure-decide/diagnostic-overlay.mjs "$dir/_overlay" "$mode" > "$dir/generate.log" 2>&1
  cp "$dir/_overlay/driver-provenance.json" "$dir/driver-provenance.json"
  date -u +%FT%TZ > "$dir/start.txt"
  go version > "$dir/go-version.txt";bun --version > "$dir/bun-version.txt"
  for resource in cpu.max cpu.stat memory.max memory.swap.max memory.events pids.max cpu.pressure io.pressure memory.pressure;do
   if test -r "/sys/fs/cgroup/$resource";then cat "/sys/fs/cgroup/$resource" > "$dir/$resource.before";fi
  done
  sed -n "/Cpus_allowed_list/p" /proc/self/status > "$dir/affinity.txt"
  stat -f -c "%T" /work > "$dir/filesystem.txt"
  go list -overlay "$dir/_overlay/overlay.json" ./... > "$dir/packages.txt"
  if grep -q /tickets/handoff/ "$dir/packages.txt";then echo "diagnostic artifact became Go package";exit 2;fi
  budget=$((cap-$(date -u +%s)));if ((budget<2700));then echo "insufficient aggregate time for full arm";exit 124;fi
  set +e
  printf "{\"expected_processes\":[{\"package\":\"api_test\",\"mode\":\"%s\",\"count\":1},{\"package\":\"store\",\"mode\":\"%s\",\"count\":1}]}\n" "$mode" "$mode" > "$dir/run-inventory.json"
  DECIDE_REPORT="$dir" DECIDE_TRACE="$dir/trace.jsonl" FULL_DIR="$dir" bun tools/measure-decide/supervise.mjs "${BASE%/pass-03}" "$dir" full 2700 bash -c "
   cd /work/pkg/ts-bun-client || exit 2
   bun install --frozen-lockfile > \"$dir/bootstrap.log\" 2>&1 && bun run build >> \"$dir/bootstrap.log\" 2>&1 || exit 2
   cd /work
   go test -json -vet=off -overlay \"$dir/_overlay/overlay.json\" -timeout 30m -count=1 ./... > \"$dir/go.jsonl\" 2> \"$dir/go.stderr\"
   go_code=\$?
   printf \"%s\\n\" \"\$go_code\" > \"$dir/go-exit-code.txt\"
   # Preserve a first failure; no next test arm after a failed Go context.
   if test \"\$go_code\" != 0;then exit \"\$go_code\";fi
   cd /work/pkg/ts-bun-client
   bun test > \"$dir/bun.log\" 2>&1
   bun_code=\$?
   printf \"%s\\n\" \"\$bun_code\" > \"$dir/bun-exit-code.txt\"
   exit \"\$bun_code\"
  "
  code=$?
  set -e
  printf "%s\n" "$code" > "$dir/exit-code.txt"
  date -u +%FT%TZ > "$dir/end.txt"
  for resource in cpu.stat memory.events cpu.pressure io.pressure memory.pressure;do
   if test -r "/sys/fs/cgroup/$resource";then cat "/sys/fs/cgroup/$resource" > "$dir/$resource.after";fi
  done
  # The same live aggregate retained/disposable checks cover full arms. No arm
  # is authorized by this pass; this branch is retained for a future plan.
  bun tools/measure-decide/check-traces.mjs "$dir" > "$dir/trace-check.log" 2>&1 || code=1
  printf "%s\n" "$code" > "$dir/final-exit-code.txt"
  jq -r ".peak.retained" "$dir/supervisor-result.json" > "$dir/retained-aggregate-bytes.txt"
  printf "Full-context %s exit %s\n" "$mode" "$code"
  exit "$code"
  ;;
 *)exit 2;;
 esac
' diagnostic "$@"
