#!/bin/sh
# Runs inside the job container (on gmahoney-test, via jobsched). Never run on
# the dev host. Reads /work/runs: one line per TLC run,
#   SPEC CFG [@BUDGET_S] [TLC args]
# Everything the report needs goes to stdout (jobsched keeps it as run.log;
# /out is not returned). TLC's own log stays in the container; stdout gets it
# without state dumps, and a condensed counterexample (action + changed
# variables per step) only when a property fails. Each run ends with one
# "VERDICT ..." line; the LAST line of the job is "JOB-VERDICT ...".
#
# Guards: a wall-clock budget (BUDGET s in total, below jobsched's 1 h
# timeout; @N caps one run) and a disk cap (DISKCAP_MB) on TLC's metadir,
# which lives on the container disk.
BUDGET=${BUDGET:-3240}
DISKCAP_MB=${DISKCAP_MB:-8000}
XMX=${XMX:-16g}
DIRECT=${DIRECT:-6g}
start=$(date +%s)
mkdir -p /work/states /work/logs
cd /work || exit 1

# Condensed trace: "-- N Action" then the variables that changed.
condense() {
  awk '
    /^Error: (The behavior up to this point is|The following behavior constitutes a counter-example):/ { on = 1; next }
    !on { next }
    /^State [0-9]+:/ || /^([0-9]+: )?(Back to state|Stuttering)/ {
      flush(); hdr = $0; sub(/ line [0-9].*$/, ">", hdr); n = 0; next }
    /^\/\\ / { flushvar(); cur = substr($0, 4); next }
    /^ / && cur != "" { cur = cur " " $0; gsub(/  +/, " ", cur); next }
    /^$/ { flushvar(); next }
    /states generated|^Finished|^The depth/ { flush(); done = 1; exit }
    END { if (!done) flush() }
    function flushvar(   k, v) {
      if (cur == "") return
      k = cur; sub(/ = .*/, "", k); v = cur; sub(/^[^=]*= /, "", v)
      val[k] = v; keys[++n] = k; cur = "" }
    function flush(   i, k) {
      flushvar()
      if (hdr == "") return
      print "-- " hdr
      for (i = 1; i <= n; i++) { k = keys[i]
        if (!(k in prev) || prev[k] != val[k]) print "     " k " = " val[k]
        prev[k] = val[k] }
      hdr = ""; n = 0 }
  ' "$1"
}

summary=""
while read -r spec cfg args; do
  case "$spec" in ''|\#*) continue ;; esac
  cap=""
  case "$args" in @*) cap=${args%% *}; cap=${cap#@}; args=${args#@"$cap"}; args=${args# } ;; esac
  left=$(( BUDGET - ($(date +%s) - start) ))
  [ -n "$cap" ] && [ "$cap" -lt "$left" ] && left=$cap
  checks=$(grep -E '^(INVARIANT|PROPERTY)' "$cfg.cfg" | cut -d' ' -f2- | tr '\n' ' ' | sed 's/ *$//; s/ /,/g')
  log="/work/logs/$cfg.log"
  echo "=== RUN $spec $cfg ($args) checks=$checks budget=${left}s"
  grep -E '^  (MaxSid|MaxLife|MaxFaults|Enable|Allow|Wall|OrchNames|SendBound|Probes|ApproverPrompt|HistoryByLife|FinishedSendOK|ClaimClearsPid|GraceFromLaunch|CreationCheck|ResumeSendOK|KillNeedsReported|AllowLeftover|DupEndsLaunch|EnableCrash|SquatMarks|KillRowRule|LaunchTag|KillSparesOld|ForgeMode|WrongServerFault|ServerCheck|LabelOverRecord|AdoptPidCheck|RemainOnExit|CallerWrongServer|A1TokenKept|A2ByLabel|A3DollarById|A4LabelRecover|A5PaneKill|A6RecordedSock|A7ProcLiveness|ActPidCheck)' "$cfg.cfg" | tr -s ' ' | tr '\n' ';'; echo
  if [ "$left" -lt 30 ]; then
    v="VERDICT $cfg checks=$checks result=SKIPPED(no-budget)"
    echo "$v"; summary="$summary $cfg=SKIPPED"; continue
  fi
  rm -rf "/work/states/$cfg"; rm -f /work/diskcap
  t0=$(date +%s)
  # shellcheck disable=SC2086
  timeout -s KILL "$left" java -XX:+UseParallelGC -Xmx"$XMX" -XX:MaxDirectMemorySize="$DIRECT" \
    -cp tla2tools.jar tlc2.TLC -workers 4 -noGenerateSpecTE \
    -metadir "/work/states/$cfg" -config "$cfg.cfg" $args "$spec.tla" > "$log" 2>&1 < /dev/null &
  jpid=$!
  # stream TLC's log minus state dumps (lines of a trace start with "/\" or a space)
  tail -n +1 -f "$log" --pid=$jpid < /dev/null | grep --line-buffered -vE '^(/\\| )' &
  while kill -0 $jpid 2>/dev/null; do
    used=$(du -sm "/work/states/$cfg" 2>/dev/null | cut -f1)
    if [ "${used:-0}" -gt "$DISKCAP_MB" ]; then
      echo "disk cap: states dir ${used} MB > ${DISKCAP_MB} MB" > /work/diskcap
      kill -TERM $jpid   # timeout(1) passes the signal on to java
    fi
    sleep 5
  done
  wait $jpid; rc=$?
  sleep 2
  secs=$(( $(date +%s) - t0 ))
  distinct=$(grep -oE '[0-9,]+ distinct states found' "$log" | tail -1 | cut -d' ' -f1 | tr -d ,)
  depth=$(grep -oE 'depth of the complete state graph search is [0-9]+' "$log" | tail -1 | grep -oE '[0-9]+$')
  viol=$(grep -m1 -oE 'Error: (Invariant [A-Za-z0-9_]+ is violated|Temporal propert(y [A-Za-z0-9_]+ was|ies were) violated|Action property [A-Za-z0-9_]+ is violated)' "$log" | sed 's/^Error: //')
  if [ -n "$viol" ]; then
    res="FAIL($(echo "$viol" | tr ' ' '_'))"
    echo "--- condensed counterexample ($cfg) ---"
    condense "$log"
    echo "--- end counterexample ---"
  elif grep -q 'Model checking completed. No error has been found' "$log"; then
    res=PASS
  elif echo "$args" | grep -q simulate && grep -q 'The number of states generated' "$log" && ! grep -qE '^Error' "$log"; then
    res=PARSE-OK
  elif [ -f /work/diskcap ]; then
    res="INCOMPLETE(disk-cap)"; cat /work/diskcap
  elif { [ "$rc" = 137 ] || [ "$rc" = 124 ]; } && [ "$secs" -ge "$((left - 5))" ]; then
    res="INCOMPLETE(time-budget)"
  else
    err=$(grep -m1 -E '^Error|Exception|error' "$log" | cut -c1-80 | tr ' ' '_')
    res="ERROR(rc=$rc:$err)"
  fi
  if [ -z "$distinct" ]; then
    distinct=$(grep -oE 'The number of states generated: [0-9]+' "$log" | grep -oE '[0-9]+$')
  fi
  if [ -z "$distinct" ]; then   # incomplete: last progress line
    distinct=$(grep -oE '[0-9,]+ distinct states found' "$log" | tail -1 | cut -d' ' -f1 | tr -d ,)
    [ -z "$distinct" ] && distinct=$(grep -E '^Progress' "$log" | tail -1 | grep -oE '[0-9,]+ distinct' | tr -d , | cut -d' ' -f1)
    [ -n "$distinct" ] && distinct=">=$distinct"
  fi
  [ -z "$depth" ] && depth=$(grep -E '^Progress\(' "$log" | tail -1 | sed -E 's/^Progress\(([0-9]+)\).*/>=\1/')
  rm -rf "/work/states/$cfg"
  v="VERDICT $cfg checks=$checks result=$res distinct=${distinct:-?} depth=${depth:-?} secs=$secs"
  echo "$v"
  summary="$summary $cfg=$res/${distinct:-?}"
done < /work/runs
echo "JOB-VERDICT$summary"
