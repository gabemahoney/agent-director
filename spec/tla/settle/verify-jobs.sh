#!/usr/bin/env bash
# Static checks of every job directory (no TLC run): byte-identity of tlcjob.sh, tla2tools.jar and
# the spec; the cfg in the directory equals cfg/<name>.cfg; every INVARIANT the cfg names is defined
# in the spec; every CONSTANT is set; `runs` names the cfg. Prints one line per job and a summary.
# Usage: bash verify-jobs.sh
set -u
B=/home/horde/projects/agent-director-project/tickets/Bugs/b.3ie
REPO=/home/horde/projects/agent-director-project/agent-director-b.3ie
KEYS="NHooks MaxT InitT MaxCfgChanges MaxTime MaxWrites InitStates FreshInsert HookVals HookFromTerminal Commands Guards ReadClock Mut"
bad=0; n=0
for d in "$B"/job-*/; do
  d=${d%/}; name=${d##*/job-}; n=$((n + 1)); err=""
  cmp -s "$d/tlcjob.sh" "$REPO/spec/tla/jobs/lib/tlcjob.sh" || err="$err tlcjob.sh"
  cmp -s "$d/tla2tools.jar" "$REPO/spec/tla/tla2tools.jar" || err="$err jar"
  cmp -s "$d/B3ieSettle.tla" "$B/B3ieSettle.tla" || err="$err spec"
  cmp -s "$d/Dockerfile" "$B/job-Dockerfile" || err="$err Dockerfile"
  cmp -s "$d/$name.cfg" "$B/cfg/$name.cfg" || err="$err cfg"
  [ "$(ls "$d" | wc -l)" = 6 ] || err="$err extra-files"
  grep -q "^B3ieSettle $name @[0-9]*$" "$d/runs" || err="$err runs"
  for k in $KEYS; do grep -q "^  $k = " "$d/$name.cfg" || err="$err const:$k"; done
  for i in $(grep '^INVARIANT ' "$d/$name.cfg" | cut -d' ' -f2); do
    grep -q "^$i ==" "$B/B3ieSettle.tla" || err="$err undefined:$i"
  done
  exp=$(grep -o 'Expected: .*' "$d/runs" | cut -d' ' -f2-)
  if [ -n "$err" ]; then bad=$((bad + 1)); echo "BAD  job-$name:$err"; else echo "OK   job-$name  expected $exp"; fi
done
echo "--- layout of sanity.cfg (run by TLC) vs pass_const.cfg, non-comment lines:"
diff <(grep -v '^\\\*' "$B/tla-logs/sanity.cfg") <(grep -v '^\\\*' "$B/cfg/pass_const.cfg")
echo "SUMMARY jobs=$n bad=$bad"
