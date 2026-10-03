#!/usr/bin/env bash
# spec/tla/laptop/run.sh: runs the TLA+ model check on a laptop with the
# vendored TLC (../tla2tools.jar), for bee b.66h while the job scheduler is
# off. See README.md next to this file.
#
# NEVER run this on a shared dev host: TLC can take every core and many GB of
# memory and disk (a TLC run once OOM-killed the dev VM). The script refuses
# to start on a Horde dev VM.
#
#   run.sh                 parse check, then the b.66h runs, then the fast tier
#   run.sh --launch        parse check and the b.66h runs only
#   run.sh --regress       parse check and the fast tier (or TLA_TIER=full) only
#   run.sh --parse-only    the parse check only
#   run.sh CFG...          only the named cfgs (from either manifest), after the
#                          parse check of their spec modules
#
# Environment (all optional):
#   TLA_JAVA      the java binary (default: $JAVA_HOME/bin/java, else java)
#   TLA_XMX       JVM heap, e.g. 8g (default: a quarter of RAM, 2g to 8g)
#   TLA_DIRECT    JVM direct memory, TLC's fingerprint set (default: half the heap)
#   TLA_WORKERS   TLC worker threads (default: every core)
#   TLA_TIER      fast (default) or full: which ci/suite.tsv rows --regress runs
#   TLA_CAP_S     wall-clock cap per run in seconds (default 3600; 0 = none)
#   TLA_DISK_GB   cap on one run's TLC state files (default 40)
#   TLA_WORKDIR   where TLC runs and the logs stay (default: a new directory
#                 under $TMPDIR); the state files are deleted after each run
#
# Output: one line per run,
#   PASS|FAIL <cfg> <result> distinct=<n> secs=<s> -- <what>[ (note)]
# then
#   LAPTOP-VERDICT PASS|FAIL (<ok>/<total> runs ok, <minutes> min, ...)
# A pass row is ok when TLC finds no error; a b.zuj row must also reach the
# state count b.zuj CI run 4 recorded (run4.tsv): with the b.66h knobs off the
# model change must leave the state graph exactly as it was. A violation row
# is ok when TLC reports a violation; a b.66h row must violate one of the
# properties its manifest names. Exit 0 only on PASS. Needs bash 3.2+ and a
# Java 11+ runtime; works on macOS and Linux.
set -u -o pipefail

HERE=$(cd "$(dirname "$0")" && pwd)   # spec/tla/laptop
ROOT=$(dirname "$HERE")                # spec/tla
JAR="$ROOT/tla2tools.jar"
CI_SUITE="$ROOT/ci/suite.tsv"
LS_SUITE="$ROOT/launch/suite.tsv"
RUN4="$HERE/run4.tsv"
SPECS="Phase4 Phase4Split Phase5Hook"
T0=$(date +%s)
CUR_PID=

say()  { printf '%s\n' "$*"; }
note() { printf '# %s\n' "$*" >&2; }
die()  { printf 'FAIL preflight -- %s\n' "$1"; say "LAPTOP-VERDICT FAIL ($2)"; exit 1; }

# --- arguments ------------------------------------------------------------------
MODE=all; PARSE_ONLY=no; ONLY=
for a in "$@"; do
  case "$a" in
    --launch) MODE=launch ;;
    --regress) MODE=regress ;;
    --parse-only) PARSE_ONLY=yes ;;
    -h|--help) sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*) echo "unknown option $a (see --help)" >&2; exit 2 ;;
    *) ONLY="$ONLY $a" ;;
  esac
done
TIER=${TLA_TIER:-fast}
case "$TIER" in fast|full) ;; *) die "TLA_TIER must be fast or full (got '$TIER')" "bad TLA_TIER" ;; esac

# --- never on a shared dev host --------------------------------------------------
if [ -d /home/horde/startup ]; then
  die "this looks like a Horde dev VM (/home/horde/startup exists); TLC must never run here. Run it on your laptop" "refused on a dev host"
fi

# --- java -----------------------------------------------------------------------
if [ -n "${TLA_JAVA:-}" ]; then JAVA=$TLA_JAVA
elif [ -n "${JAVA_HOME:-}" ] && [ -x "$JAVA_HOME/bin/java" ]; then JAVA=$JAVA_HOME/bin/java
else JAVA=java
fi
case "$(uname -s)" in
  Darwin) HINT="install a Java 11+ runtime: brew install --cask temurin   (or set TLA_JAVA=/path/to/java)" ;;
  *)      HINT="install a Java 11+ runtime: sudo apt-get install -y openjdk-17-jre-headless   (Fedora: sudo dnf install -y java-17-openjdk-headless; or set TLA_JAVA=/path/to/java)" ;;
esac
JVER=$("$JAVA" -version 2>&1) || JVER=
JMAJOR=$(printf '%s\n' "$JVER" | sed -n -E 's/.*version "([0-9]+)(\.([0-9]+))?.*/\1 \3/p' | head -1)
case "$JMAJOR" in
  "") die "no working java ('$JAVA'). $HINT" "java missing" ;;
  "1 "*) JMAJOR=${JMAJOR#1 } ;;
  *) JMAJOR=${JMAJOR%% *} ;;
esac
[ "${JMAJOR:-0}" -ge 11 ] 2>/dev/null || die "java $JMAJOR is too old; TLC needs Java 11+. $HINT" "java too old"

# --- files ----------------------------------------------------------------------
for f in "$JAR" "$CI_SUITE" "$LS_SUITE" "$RUN4"; do
  [ -f "$f" ] || die "$f is missing" "missing file"
done
for s in $SPECS; do [ -f "$ROOT/$s.tla" ] || die "$ROOT/$s.tla is missing" "missing file"; done

# --- machine: cores, memory -------------------------------------------------------
CORES=$(getconf _NPROCESSORS_ONLN 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 4)
WORKERS=${TLA_WORKERS:-$CORES}
case "$(uname -s)" in
  Darwin) MEM_MB=$(( $(sysctl -n hw.memsize 2>/dev/null || echo 8589934592) / 1048576 )) ;;
  *)      MEM_MB=$(awk '/^MemTotal:/ { print int($2 / 1024) }' /proc/meminfo 2>/dev/null || echo 8192) ;;
esac
HEAP_MB=$((MEM_MB / 4))
[ "$HEAP_MB" -lt 2048 ] && HEAP_MB=2048
[ "$HEAP_MB" -gt 8192 ] && HEAP_MB=8192
DIRECT_MB=$((HEAP_MB / 2))
[ "$DIRECT_MB" -lt 1024 ] && DIRECT_MB=1024
XMX=${TLA_XMX:-${HEAP_MB}m}
DIRECT=${TLA_DIRECT:-${DIRECT_MB}m}
CAP=${TLA_CAP_S:-3600}
DISK_GB=${TLA_DISK_GB:-40}
case "$WORKERS:$CAP:$DISK_GB" in
  *[!0-9:]*|:*|*::*|*:) die "TLA_WORKERS, TLA_CAP_S and TLA_DISK_GB must be whole numbers" "bad setting" ;;
esac
DISK_KB=$((DISK_GB * 1024 * 1024))

# --- the plan ---------------------------------------------------------------------
# One line per run: kind spec cfg expect props what   (tab-separated)
#   kind: ci (ci/cfg, b.zuj) or ls (launch/cfg, b.66h)
PLAN=$(
  if [ "$MODE" != regress ]; then
    grep -v -e '^#' -e '^$' "$LS_SUITE" | awk -F'\t' -v OFS='\t' '{ print "ls", $2, $3, $5, $8, $7 }'
  fi
  if [ "$MODE" != launch ]; then
    grep -v -e '^#' -e '^$' "$CI_SUITE" | awk -F'\t' -v OFS='\t' -v tier="$TIER" -v named="$ONLY" '
      tier == "full" || $6 == "fast" || index(" " named " ", " " $3 " ") { print "ci", $2, $3, $5, "-", $7 }'
  fi
)
if [ -n "$ONLY" ]; then
  PLAN=$(printf '%s\n' "$PLAN" | awk -F'\t' -v named="$ONLY" 'index(" " named " ", " " $3 " ")')
  for c in $ONLY; do
    printf '%s\n' "$PLAN" | awk -F'\t' -v c="$c" '$3 == c { f = 1 } END { exit !f }' ||
      die "cfg '$c' is in neither manifest (or not in this mode)" "unknown cfg"
  done
fi
[ -n "$PLAN" ] || die "nothing to run" "no runs"
TOTAL=$(printf '%s\n' "$PLAN" | wc -l | tr -d ' ')

# --- the work directory -----------------------------------------------------------
if [ -n "${TLA_WORKDIR:-}" ]; then WORK=$TLA_WORKDIR; mkdir -p "$WORK" || die "cannot create $WORK" "bad TLA_WORKDIR"
else WORK=$(mktemp -d "${TMPDIR:-/tmp}/tla-laptop.XXXXXX") || die "cannot create a work directory" "bad TMPDIR"
fi
WORK=$(cd "$WORK" && pwd)
mkdir -p "$WORK/logs" "$WORK/states" || die "cannot populate $WORK" "bad work directory"
for s in $SPECS; do cp "$ROOT/$s.tla" "$WORK/" || die "cannot copy $s.tla" "copy failed"; done
for c in "$ROOT"/ci/cfg/*.cfg "$ROOT"/launch/cfg/*.cfg; do cp "$c" "$WORK/" || die "cannot copy $c" "copy failed"; done
FREE_KB=$(df -Pk "$WORK" 2>/dev/null | awk 'NR == 2 { print $4 }')

note "java $JMAJOR ($JAVA); $WORKERS workers on $CORES cores; heap $XMX, direct $DIRECT of ${MEM_MB} MB RAM"
note "work dir $WORK (logs kept); per-run cap ${CAP}s; state files capped at ${DISK_GB} GB per run"
if [ -n "$FREE_KB" ] && [ "$FREE_KB" -lt "$DISK_KB" ]; then
  note "warning: only $((FREE_KB / 1048576)) GB free under $WORK; a large run may fill the disk (set TLA_WORKDIR or TLA_DISK_GB)"
fi
note "plan: $TOTAL runs (mode $MODE, tier $TIER)$([ "$PARSE_ONLY" = yes ] && echo ', parse check only')"

# --- stop cleanly on ^C -----------------------------------------------------------
on_signal() {
  trap - INT TERM HUP
  if [ -n "$CUR_PID" ]; then kill "$CUR_PID" 2>/dev/null; sleep 1; kill -9 "$CUR_PID" 2>/dev/null; fi
  rm -rf "$WORK/states"
  note "interrupted; logs kept in $WORK/logs"
  say "LAPTOP-VERDICT FAIL (interrupted)"
  exit 130
}
trap on_signal INT TERM HUP

# --- one TLC run ------------------------------------------------------------------
# tlc <spec> <cfg> <log> [TLC args...]: sets RC, SECS, STOPPED (cap|disk|"").
tlc() {
  local spec=$1 cfg=$2 log=$3 t0 now used last
  shift 3
  rm -rf "$WORK/states/$cfg"
  t0=$(date +%s); last=$t0; STOPPED=
  # ${1+"$@"}: bash 3.2 treats an empty "$@" as unset under set -u.
  (cd "$WORK" && exec "$JAVA" -XX:+UseParallelGC -Xmx"$XMX" -XX:MaxDirectMemorySize="$DIRECT" \
     -cp "$JAR" tlc2.TLC -workers "$WORKERS" -noGenerateSpecTE \
     -metadir "$WORK/states/$cfg" -config "$cfg.cfg" ${1+"$@"} "$spec.tla") > "$log" 2>&1 < /dev/null &
  CUR_PID=$!
  while kill -0 "$CUR_PID" 2>/dev/null; do
    sleep 0.2
    now=$(date +%s)
    if [ "$CAP" -gt 0 ] && [ $((now - t0)) -ge "$CAP" ]; then STOPPED=cap; break; fi
    if [ $((now - last)) -ge 10 ]; then
      last=$now
      used=$(du -sk "$WORK/states/$cfg" 2>/dev/null | awk '{ print $1 }')
      if [ "${used:-0}" -gt "$DISK_KB" ]; then STOPPED=disk; break; fi
    fi
  done
  if [ -n "$STOPPED" ]; then
    kill "$CUR_PID" 2>/dev/null; sleep 2; kill -9 "$CUR_PID" 2>/dev/null
  fi
  wait "$CUR_PID"; RC=$?
  CUR_PID=
  SECS=$(( $(date +%s) - t0 ))
  rm -rf "$WORK/states/$cfg"
}

# result <log> [parse]: TLC's verdict, as in ../jobs/lib/tlcjob.sh.
result() {
  local log=$1 viol err
  viol=$(grep -m1 -oE 'Error: (Invariant [A-Za-z0-9_]+ is violated|Temporal propert(y [A-Za-z0-9_]+ was|ies were) violated|Action property [A-Za-z0-9_]+ is violated)' "$log" | sed 's/^Error: //')
  if [ -n "$viol" ]; then echo "FAIL($(echo "$viol" | tr ' ' '_'))"
  elif grep -q 'Model checking completed. No error has been found' "$log"; then echo PASS
  elif [ "${2:-}" = parse ] && grep -q 'The number of states generated' "$log" && ! grep -qE '^Error' "$log"; then echo PARSE-OK
  elif [ "$STOPPED" = cap ]; then echo "INCOMPLETE(cap-${CAP}s)"
  elif [ "$STOPPED" = disk ]; then echo "INCOMPLETE(disk-cap)"
  else
    err=$(grep -m1 -E '^Error|Exception' "$log" | cut -c1-70 | tr ' ' '_')
    echo "ERROR(rc=$RC:${err:-no_verdict})"
  fi
}

distinct_of() {
  grep -oE '[0-9,]+ distinct states found' "$1" | tail -1 | cut -d' ' -f1 | tr -d ,
}

# condense <log>: the counterexample, one "-- <step>" line per state and the
# variables that changed (as tlcjob.sh prints it).
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
      print "   -- " hdr
      for (i = 1; i <= n; i++) { k = keys[i]
        if (!(k in prev) || prev[k] != val[k]) print "        " k " = " val[k]
        prev[k] = val[k] }
      hdr = ""; n = 0 }
  ' "$1"
}

# --- 1. the parse check -----------------------------------------------------------
# Every b.66h cfg, and the first planned cfg of each spec module: TLC parses
# the specs and the cfg, then simulates one step (as make tla's parse job).
PARSE_OK=yes
PARSE_LIST=$(printf '%s\n' "$PLAN" | awk -F'\t' '{ first = !seen[$2]++ } $1 == "ls" || first { print $2 "\t" $3 }')
while IFS=$'\t' read -r spec cfg; do
  log="$WORK/logs/parse-$cfg.log"
  tlc "$spec" "$cfg" "$log" -simulate num=1 -depth 1
  res=$(result "$log" parse)
  if [ "$res" = PARSE-OK ]; then
    say "PASS parse $cfg ($spec) secs=$SECS"
  else
    PARSE_OK=no
    say "FAIL parse $cfg ($spec) $res -- log $log"
    grep -E -m5 '^(Error|\*\*\*|Line|line)|Exception|was not|not defined|Unknown' "$log" | sed 's/^/     /'
  fi
done <<EOF
$PARSE_LIST
EOF
if [ "$PARSE_OK" = no ]; then
  note "logs kept in $WORK/logs"
  say "LAPTOP-VERDICT FAIL (parse check failed; nothing else was run)"
  exit 1
fi
if [ "$PARSE_ONLY" = yes ]; then
  say "LAPTOP-VERDICT PASS (parse check only, $(( ($(date +%s) - T0 + 59) / 60 )) min)"
  exit 0
fi

# --- 2. the runs ------------------------------------------------------------------
OK=0; N=0
while IFS=$'\t' read -r kind spec cfg expect props what; do
  N=$((N + 1))
  log="$WORK/logs/$cfg.log"
  note "[$N/$TOTAL] $cfg ..."
  tlc "$spec" "$cfg" "$log"
  res=$(result "$log")
  dist=$(distinct_of "$log")
  good=no; why=
  base=$(awk -F'\t' -v c="$cfg" '$1 == c { print $2 "\t" $3 "\t" $4 }' "$RUN4")
  base_res=$(printf '%s' "$base" | cut -f1); base_dist=$(printf '%s' "$base" | cut -f2)
  base_secs=$(printf '%s' "$base" | cut -f3)
  case "$expect:$res" in
    pass:PASS)
      good=yes
      if [ "$kind" = ci ]; then
        if [ -z "$base_dist" ]; then why="no run-4 state count to compare"
        elif [ "$dist" != "$base_dist" ]; then good=no; why="state count differs from b.zuj run 4 ($base_dist): the model change is not inert here"
        else why="same state count as b.zuj run 4"
        fi
      fi ;;
    violation:FAIL\(*)
      good=yes
      name=$(printf '%s' "$res" | sed -E 's/^FAIL\((Invariant|Action_property|Temporal_property)_([A-Za-z0-9_]+)_(is|was)_violated\)$/\2/')
      if [ "$kind" = ls ]; then
        case ",$props," in
          *",$name,"*) why="violates $name, as expected" ;;
          *) good=no; why="violates $name, but the expected violation is $props" ;;
        esac
      elif [ -n "$base_res" ] && [ "$res" != "$base_res" ]; then
        why="note: b.zuj run 4 reported $base_res"
      fi ;;
  esac
  if [ "$good" = yes ]; then OK=$((OK + 1)); tag=PASS; else tag=FAIL; fi
  printf '%s %-20s %-58s distinct=%s secs=%s -- %s%s\n' "$tag" "$cfg" "$res" "${dist:-?}" "$SECS" "$what" \
    "${why:+ ($why)}"
  [ -n "$base_secs" ] && [ "$kind" = ci ] && note "    b.zuj run 4 took ${base_secs}s on 4 workers"
  # Show the hazard trace of the b.66h controls, and any unexpected failure.
  if { [ "$kind" = ls ] && [ "$expect" = violation ] && [ "$good" = yes ]; } || [ "$tag" = FAIL ]; then
    case "$res" in
      FAIL\(*) condense "$log" ;;
      *) grep -E -m5 '^Error|Exception|was not|not defined' "$log" | sed 's/^/     /' ;;
    esac
    [ "$tag" = FAIL ] && say "     log: $log"
  fi
done <<EOF
$PLAN
EOF

rm -rf "$WORK/states"
note "logs kept in $WORK/logs"
MIN=$(( ($(date +%s) - T0 + 59) / 60 ))
if [ "$OK" -eq "$N" ] && [ "$N" -gt 0 ]; then
  say "LAPTOP-VERDICT PASS ($OK/$N runs ok, $MIN min, mode $MODE, tier $TIER, $WORKERS workers)"; exit 0
fi
say "LAPTOP-VERDICT FAIL ($OK/$N runs ok, $MIN min, mode $MODE, tier $TIER, $WORKERS workers)"
exit 1
