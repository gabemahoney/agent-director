#!/usr/bin/env bash
# spec/tla/ci/run_ci.sh: the runner behind `make tla` and `make tla-print`
# (Epic 22). Adapted from bee b.zuj's ci/run_ci.sh (see ../PROVENANCE.txt)
# with the same suite semantics, plus repo-relative paths, a pin check in
# place of regenerating the suite, a fail-closed preflight and per-run state
# under $TMPDIR.
#
#   run_ci.sh           submit the suite to the job scheduler and wait for it
#   run_ci.sh --print   print the plan and the pin check, then exit; submits
#                       nothing and never contacts the scheduler
#
# The model is checked ON THE JOB SCHEDULER ONLY. This script never starts the
# model checker on this host: it builds job contexts, submits them, polls them
# and reads their logs. The checker runs only inside each job's container on
# the scheduler's remote host (../jobs/lib/tlcjob.sh).
#
# Environment:
#   TLA_CHANNEL   required, no default: the job scheduler's notice channel ID
#                 (must be on the scheduler's allow-list). Each job sends one
#                 notice to it.
#   TLA_JOBSCHED  the job scheduler CLI (default: jobsched on PATH). The CLI is
#                 meant to be run by its absolute path; pass that.
#   TLA_TIER      fast (default) or full: rows whose tier column is fast, or
#                 every row.
#   TLA_GROUPS    the groups to run, space-separated, in the order given
#                 (default: every manifest group, in manifest order). A group
#                 with no rows in the tier is skipped; a group the manifest
#                 does not have is refused.
#   TLA_SPEC_DIR  the directory holding Phase4.tla, Phase4Split.tla and
#                 Phase5Hook.tla (default: spec/tla/). Specs read from an
#                 override are not pinned.
#   TLA_SUITE     the manifest (default: spec/tla/ci/suite.tsv). An override is
#                 not pinned. A cfg in spec/tla/ci/cfg/ comes from there and
#                 must be pinned. An override may also name cfgs in the cfg/
#                 directory next to it (spec/tla/launch/cfg/ for
#                 launch/suite.tsv); those are not pinned. A cfg in both is
#                 refused.
#   TLA_POLL_S    seconds between status polls (default 30).
#   TMPDIR        the per-run directory is created under it (default /tmp);
#                 it must lie outside this repo.
#
# Order:
#   1. Preflight, before anything is submitted: the tier, the tools, every pin
#      in ../PROVENANCE.txt (sha256sum; the jar's size too), the manifest, the
#      channel, the scheduler CLI, and a running dispatcher (`list --json`).
#      The scheduler has no cancel operation, so a job submitted while the
#      dispatcher is down would run whenever it next starts.
#   2. A parse-only job (one cfg per spec module, simulation depth 1, budget
#      600 s) over the tier's rows of the whole manifest. It gates the rest.
#      Its submit reports the scheduler's limits: the run timeout must cover
#      the largest group budget plus 300 s and the memory must be at least
#      24 GiB, or the run ends FAIL with no group job submitted.
#   3. One job per group, strictly one at a time. A job's budget is the sum of
#      (cap_s + 20) over its runs, plus 120, at most 10200 s; each run keeps
#      its own cap. DISKCAP_MB is raised to 60000: up to 60 GB of states per
#      run on the remote's container disk.
# Every job is submitted with --network off and polled until it ends; its run
# log is read through the scheduler's `results --json` and copied into the
# per-run directory. A job's context is kept intact until the job ends (the
# scheduler reads it again when the job leaves the queue), then removed; an
# interrupt is the one exception (below). The logs are kept and the per-run
# directory is printed.
#
# Output: one line per run,
#   PASS|FAIL <cfg> <result> distinct=<n> secs=<s> -- <what>
# then
#   CI-VERDICT PASS|FAIL (<ok>/<total> runs ok, <minutes> min, tier <tier>)
# A run is ok when expect=pass and the result is PASS, or expect=violation and
# the result is FAIL(...). In a manifest with the props column, a violation
# row is ok only when the property its result names is one of its props;
# otherwise its line ends "(the expected violation is <props>)". The runner
# does not compare distinct-state counts with any earlier run. INCOMPLETE, ERROR, SKIPPED and NO-VERDICT are never ok. Exit 0
# only on PASS.
#
# Fail closed: a preflight refusal, a refused submit, a submit with no job id,
# a failed status call (an unknown job), a dispatcher that is not running, an
# unknown job state, a job past its timeout plus an hour, too-low limits or a
# failed parse check each print "FAIL <step> -- <reason>" and
# "CI-VERDICT FAIL (<reason>, tier <tier>)" and exit 1. Nothing is retried. A
# usage error exits 2.
#
# Interrupt (SIGINT, SIGTERM or SIGHUP; exit 130): the runner asks the
# scheduler (`status --json`) for the current job's state. A job still queued
# has its context removed, so the scheduler's dispatch re-check rejects it when
# it reaches the head of the queue: in effect a cancel. A job already building
# or running keeps its context and finishes on its own. When the state cannot
# be read, the context is kept (fail safe). Logs are always kept, and the
# runner prints what it did.
set -u -o pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"   # spec/tla/ci
ROOT="$(dirname "$HERE")"                                # spec/tla
PROV="$ROOT/PROVENANCE.txt"
SPECS="Phase4 Phase4Split Phase5Hook"
PARSE_BUDGET=600
MAX_BUDGET=10200
LIMIT_MARGIN_S=300
MIN_MEMORY=$((24 * 1024 * 1024 * 1024))
WAIT_MARGIN_S=3600   # after a job leaves the queue: its run timeout plus this (build and fetch)
JAR=tla2tools.jar

PRINT=no
case "$#:${1:-}" in
  0:) ;;
  1:--print) PRINT=yes ;;
  *)
    echo "usage: run_ci.sh [--print]   (settings come from TLA_* variables; see the header)" >&2
    echo "CI-VERDICT FAIL (usage error)"
    exit 2
    ;;
esac

TIER=${TLA_TIER:-fast}
SPEC_DIR=${TLA_SPEC_DIR:-$ROOT}
SUITE=${TLA_SUITE:-$HERE/suite.tsv}
JS=${TLA_JOBSCHED:-jobsched}
CHANNEL=${TLA_CHANNEL:-}
POLL=${TLA_POLL_S:-30}
T0=$(date +%s)
RUN_DIR=
CUR_JOB=
CUR_ID=
CUR_CTX=
CUR_PHASE=   # build (not submitted yet), submit (submit call in flight), submitted
SLEEP_PID=

# die <step> <verdict reason> <detail>: the closing lines of any run that does
# not reach the per-run verdicts.
die() {
  printf 'FAIL %s -- %s\n' "$1" "$3"
  if [ -n "$CUR_CTX" ] && [ -d "$CUR_CTX" ]; then
    echo "# job ${CUR_JOB}${CUR_ID:+ ($CUR_ID)} has not ended; it stays on the job scheduler (no cancel operation) and its context stays in $CUR_CTX" >&2
  fi
  [ -n "$RUN_DIR" ] && echo "# run dir: $RUN_DIR (logs kept)" >&2
  echo "CI-VERDICT FAIL ($2, tier $TIER)"
  exit 1
}

# drop_ctx: removes the current context; the rename first makes it vanish in
# one step, so the scheduler sees either the whole context or none of it.
# Returns 1 when the rename fails (the context is then still in place).
# shellcheck disable=SC2317  # reached through the trap below
drop_ctx() {
  local gone="$CUR_CTX.removed.$$"
  mv "$CUR_CTX" "$gone" 2>/dev/null || return 1
  rm -rf "$gone" 2>/dev/null || echo "# note: $gone could not be fully deleted" >&2
  return 0
}

# on_interrupt_job: the current job was submitted; ask the scheduler where it
# is. Queued: remove the context (the dispatch re-check then rejects the job).
# Building or running: keep the context. Ended: remove it, as after a normal
# end. Anything unreadable: keep it.
# shellcheck disable=SC2317  # reached through the trap below
on_interrupt_job() {
  local who="${CUR_JOB}${CUR_ID:+ ($CUR_ID)}" out rc st term out2 st2
  if [ -z "$CUR_ID" ]; then
    echo "# interrupted: job $who state unknown (no job id); its context was kept in $CUR_CTX in case the job still needs it" >&2
    return
  fi
  out=$("$JS" status "$CUR_ID" --json 2>/dev/null); rc=$?
  st=$(jq -r '.state // empty' <<<"$out" 2>/dev/null)
  term=$(jq -r '.terminal' <<<"$out" 2>/dev/null)
  if [ "$rc" -ne 0 ]; then st=; fi
  case "$st:$term" in
    queued:false)
      if drop_ctx; then
        echo "# interrupted: job $who was still queued; its context was removed so it will not run (the scheduler rejects it when it reaches the head of the queue)" >&2
        out2=$("$JS" status "$CUR_ID" --json 2>/dev/null) || out2=
        st2=$(jq -r '.state // empty' <<<"$out2" 2>/dev/null)
        case "$st2" in
          queued|rejected) ;;
          *) echo "# note: job $who reports '${st2:-unreadable}' after its context was removed; it may have left the queue just before, in which case it fails or runs on its own" >&2 ;;
        esac
      else
        echo "# interrupted: job $who was still queued, but its context $CUR_CTX could not be removed; it will run when it reaches the head of the queue" >&2
      fi
      ;;
    building:false|running:false)
      echo "# interrupted: job $who is already $st; it will finish on its own (the scheduler has no cancel operation); its context stays in $CUR_CTX until it ends" >&2
      ;;
    succeeded:true|failed:true|build-failed:true|infrastructure-error:true|rejected:true)
      drop_ctx || true
      echo "# interrupted: job $who had already ended ($st); its context was removed" >&2
      ;;
    *)
      echo "# interrupted: job $who state unknown (status exit $rc, state '${st:-unreadable}', terminal '${term:-unreadable}'); its context was kept in $CUR_CTX in case the job still needs it" >&2
      ;;
  esac
}

# shellcheck disable=SC2317  # reached through the trap below
on_signal() {
  trap - INT TERM HUP
  [ -n "$SLEEP_PID" ] && kill "$SLEEP_PID" 2>/dev/null
  # Also every other background child: a signal that lands between
  # `sleep "$POLL" &` and `SLEEP_PID=$!` leaves that sleep untracked.
  # shellcheck disable=SC2046  # one PID per word, intended
  kill $(jobs -p) 2>/dev/null || true
  echo "# interrupted" >&2
  if [ -n "$CUR_CTX" ] && [ -d "$CUR_CTX" ]; then
    case "$CUR_PHASE" in
      build)
        rm -rf "$CUR_CTX"
        echo "# interrupted: job $CUR_JOB was not submitted; its partial context was removed" >&2
        ;;
      submit)
        echo "# interrupted: job $CUR_JOB was being submitted, so whether the scheduler queued it is unknown; its context was kept in $CUR_CTX in case the job still needs it" >&2
        ;;
      *) on_interrupt_job ;;
    esac
  fi
  [ -n "$RUN_DIR" ] && echo "# run dir: $RUN_DIR (logs kept)" >&2
  echo "CI-VERDICT FAIL (interrupted, tier $TIER)"
  exit 130
}
trap on_signal INT TERM HUP

suite_min() { echo $((($(date +%s) - T0) / 60)); }

# --- preflight: settings and tools -----------------------------------------
case "$TIER" in fast|full) ;; *) die preflight "bad TLA_TIER" "TLA_TIER must be fast or full (got '$TIER'); nothing was submitted" ;; esac
[[ "$POLL" =~ ^[0-9]+$ ]] || die preflight "bad TLA_POLL_S" "TLA_POLL_S must be a whole number of seconds (got '$POLL')"

need_tools="awk sed grep sha256sum cut tr head cp mkdir mktemp date wc"
[ "$PRINT" = yes ] || need_tools="$need_tools jq sleep"
for t in $need_tools; do
  command -v "$t" >/dev/null 2>&1 || die preflight "missing tool" "the runner needs '$t' on PATH; nothing was submitted"
done

# --- preflight: the pins ------------------------------------------------------
# Every line "<sha256>  <path>" of PROVENANCE.txt names a vendored file under
# spec/tla/. All are checked; the specs are skipped when TLA_SPEC_DIR is set and
# the manifest when TLA_SUITE is set.
[ -r "$PROV" ] || die preflight "pin check failed" "the pin list $PROV is missing; nothing was submitted"
declare -A PINNED=()
PIN_N=0
PIN_SKIPPED=0
PIN_BAD=0
while read -r sum path; do
  PINNED[$path]=1
  case "$path" in
    *.tla) if [ -n "${TLA_SPEC_DIR:-}" ]; then PIN_SKIPPED=$((PIN_SKIPPED + 1)); continue; fi ;;
    ci/suite.tsv) if [ -n "${TLA_SUITE:-}" ]; then PIN_SKIPPED=$((PIN_SKIPPED + 1)); continue; fi ;;
  esac
  PIN_N=$((PIN_N + 1))
  if [ ! -f "$ROOT/$path" ]; then
    echo "FAIL pin -- $path is missing (expected SHA-256 $sum)"
    PIN_BAD=$((PIN_BAD + 1)); continue
  fi
  actual=$(sha256sum < "$ROOT/$path" | cut -d' ' -f1)
  if [ "$actual" != "$sum" ]; then
    echo "FAIL pin -- $path: SHA-256 expected $sum, got $actual"
    PIN_BAD=$((PIN_BAD + 1))
  fi
done < <(grep -E '^[0-9a-f]{64}  [^ ]+$' "$PROV")
for f in "$JAR" jobs/lib/Dockerfile jobs/lib/tlcjob.sh; do
  if [ -z "${PINNED[$f]:-}" ]; then echo "FAIL pin -- $f has no pin line in PROVENANCE.txt"; PIN_BAD=$((PIN_BAD + 1)); fi
done
if [ -z "${TLA_SPEC_DIR:-}" ]; then
  for s in $SPECS; do
    if [ -z "${PINNED[$s.tla]:-}" ]; then echo "FAIL pin -- $s.tla has no pin line in PROVENANCE.txt"; PIN_BAD=$((PIN_BAD + 1)); fi
  done
fi
if [ -z "${TLA_SUITE:-}" ] && [ -z "${PINNED[ci/suite.tsv]:-}" ]; then
  echo "FAIL pin -- ci/suite.tsv has no pin line in PROVENANCE.txt"; PIN_BAD=$((PIN_BAD + 1))
fi
jar_bytes_pin=$(sed -n 's/^  jar\.bytes  *\([0-9][0-9]*\)$/\1/p' "$PROV" | head -1)
if [ -z "$jar_bytes_pin" ]; then
  echo "FAIL pin -- PROVENANCE.txt has no jar.bytes line"; PIN_BAD=$((PIN_BAD + 1))
elif [ -f "$ROOT/$JAR" ]; then
  jar_bytes=$(wc -c < "$ROOT/$JAR" | tr -d ' ')
  if [ "$jar_bytes" != "$jar_bytes_pin" ]; then
    echo "FAIL pin -- $JAR: size expected $jar_bytes_pin bytes, got $jar_bytes"; PIN_BAD=$((PIN_BAD + 1))
  fi
fi
[ "$PIN_BAD" -eq 0 ] || die preflight "pin check failed" "$PIN_BAD pin check(s) failed against PROVENANCE.txt; nothing was submitted"

# --- preflight: the specs and the manifest --------------------------------------
[ -d "$SPEC_DIR" ] || die preflight "bad TLA_SPEC_DIR" "spec directory '$SPEC_DIR' does not exist; nothing was submitted"
SPEC_DIR=$(cd "$SPEC_DIR" && pwd)
for s in $SPECS; do
  [ -f "$SPEC_DIR/$s.tla" ] || die preflight "spec missing" "$SPEC_DIR/$s.tla does not exist; nothing was submitted"
done
if [ ! -f "$SUITE" ] || [ ! -r "$SUITE" ]; then
  die preflight "bad TLA_SUITE" "manifest '$SUITE' is not a readable file; nothing was submitted"
fi

# Each data row: group, spec, cfg, cap_s, expect, tier, what; seven non-empty
# tab-separated fields. Lines starting with # and empty lines are skipped. A
# manifest may add an eighth, props (as launch/suite.tsv): on a violation row
# the comma-separated properties whose violation is the expected result, on a
# pass row "-". The first data row sets the width; every row must have it.
bad_rows=$(awk -F'\t' -v specs=" $SPECS " '
  /^#/ || /^$/ { next }
  {
    why = ""
    if (!w) w = (NF == 8 ? 8 : 7)
    if (NF != w) why = "has " NF " fields, not " w
    else if ($1 !~ /^[A-Za-z0-9][A-Za-z0-9_.-]*$/ || length($1) > 36) why = "bad group \"" $1 "\""
    else if (index(specs, " " $2 " ") == 0) why = "unknown spec \"" $2 "\""
    else if ($3 !~ /^[A-Za-z0-9_]+$/) why = "bad cfg \"" $3 "\""
    else if ($4 !~ /^[0-9]+$/ || $4 + 0 < 1) why = "bad cap_s \"" $4 "\""
    else if ($5 != "pass" && $5 != "violation") why = "bad expect \"" $5 "\""
    else if ($6 != "fast" && $6 != "full") why = "bad tier \"" $6 "\""
    else if ($7 == "") why = "empty what"
    else if (w == 8 && $5 == "pass" && $8 != "-") why = "props \"" $8 "\" on a pass row, not \"-\""
    else if (w == 8 && $5 == "violation" && $8 !~ /^[A-Za-z0-9_]+(,[A-Za-z0-9_]+)*$/) why = "bad props \"" $8 "\""
    else if (seen[$3]++) why = "duplicate cfg \"" $3 "\""
    if (why != "") print "line " NR ": " why
  }' "$SUITE")
[ -z "$bad_rows" ] || die preflight "bad manifest" "manifest '$SUITE': $(echo "$bad_rows" | head -5 | tr '\n' ';' | sed 's/;$//'); nothing was submitted"

# The tier's rows, in manifest order.
R_GRP=(); R_SPEC=(); R_CFG=(); R_CAP=(); R_EXP=(); R_WHAT=(); R_PROPS=()
ALL_GROUPS=()
declare -A HAS_GROUP=()
while IFS= read -r line; do
  props=
  IFS=$'\t' read -r grp spec c cap expect tier what props <<<"$line"
  if [ -z "${HAS_GROUP[$grp]:-}" ]; then HAS_GROUP[$grp]=1; ALL_GROUPS+=("$grp"); fi
  [ "$TIER" = full ] || [ "$tier" = fast ] || continue
  [ "$expect" = violation ] || props=
  R_GRP+=("$grp"); R_SPEC+=("$spec"); R_CFG+=("$c"); R_CAP+=("$cap"); R_EXP+=("$expect"); R_WHAT+=("$what"); R_PROPS+=("$props")
done < <(grep -v -e '^#' -e '^$' "$SUITE")

# Where each cfg comes from: ci/cfg/ (pinned), else the cfg/ directory next to
# an overriding manifest (not pinned).
CI_CFG=$(cd "$HERE/cfg" && pwd -P)
OWN_CFG=$(cd "$(dirname "$SUITE")/cfg" 2>/dev/null && pwd -P) || OWN_CFG=
[ "$OWN_CFG" != "$CI_CFG" ] || OWN_CFG=
declare -A CFG_PATH=()
OWN_N=0
for i in "${!R_CFG[@]}"; do
  c=${R_CFG[$i]}
  if [ -n "$OWN_CFG" ] && [ -f "$OWN_CFG/$c.cfg" ]; then
    [ ! -e "$CI_CFG/$c.cfg" ] || die preflight "cfg ambiguous" "$c.cfg is in both ci/cfg/ and $OWN_CFG/; nothing was submitted"
    CFG_PATH[$c]="$OWN_CFG/$c.cfg"; OWN_N=$((OWN_N + 1))
    continue
  fi
  if [ ! -f "$HERE/cfg/$c.cfg" ]; then
    [ -z "$OWN_CFG" ] || die preflight "cfg missing" "$c.cfg (manifest row for $c) is in neither ci/cfg/ nor $OWN_CFG/; nothing was submitted"
    die preflight "cfg missing" "ci/cfg/$c.cfg (manifest row for $c) does not exist; nothing was submitted"
  fi
  [ -n "${PINNED[ci/cfg/$c.cfg]:-}" ] || die preflight "pin check failed" "ci/cfg/$c.cfg has no pin line in PROVENANCE.txt; nothing was submitted"
  CFG_PATH[$c]="$HERE/cfg/$c.cfg"
done

if [ -n "${TLA_GROUPS:-}" ]; then
  read -r -a GROUPS_WANTED <<<"$TLA_GROUPS"
else
  GROUPS_WANTED=("${ALL_GROUPS[@]}")
fi
declare -A WANTED=()
for g in "${GROUPS_WANTED[@]}"; do
  [ -n "${HAS_GROUP[$g]:-}" ] || die preflight "unknown group" "group '$g' is not in the manifest (its groups: ${ALL_GROUPS[*]}); nothing was submitted"
  [ -z "${WANTED[$g]:-}" ] || die preflight "duplicate group" "group '$g' is listed twice in TLA_GROUPS; nothing was submitted"
  WANTED[$g]=1
done

# group_rows <group>: the indices of the tier's rows in that group.
group_rows() {
  local i
  for i in "${!R_GRP[@]}"; do [ "${R_GRP[$i]}" = "$1" ] && echo "$i"; done
}
# budget <cap> ...: the sum of (cap + 20), plus 120, at most MAX_BUDGET.
budget() {
  local s=0 c
  for c in "$@"; do s=$((s + c + 20)); done
  s=$((s + 120))
  [ "$s" -gt "$MAX_BUDGET" ] && s=$MAX_BUDGET
  echo "$s"
}

# The parse job: the first tier row of each spec module, over the whole manifest.
PARSE_IDX=()
declare -A PARSE_SEEN=()
for i in "${!R_SPEC[@]}"; do
  s=${R_SPEC[$i]}
  [ -n "${PARSE_SEEN[$s]:-}" ] && continue
  PARSE_SEEN[$s]=1; PARSE_IDX+=("$i")
done

PLAN_GROUPS=(); PLAN_BUDGET=(); PLAN_COUNT=(); SKIPPED_GROUPS=()
TOTAL_RUNS=0; BIGGEST=0
for g in "${GROUPS_WANTED[@]}"; do
  mapfile -t idx < <(group_rows "$g")
  if [ "${#idx[@]}" -eq 0 ]; then SKIPPED_GROUPS+=("$g"); continue; fi
  caps=(); for i in "${idx[@]}"; do caps+=("${R_CAP[$i]}"); done
  b=$(budget "${caps[@]}")
  PLAN_GROUPS+=("$g"); PLAN_BUDGET+=("$b"); PLAN_COUNT+=("${#idx[@]}")
  TOTAL_RUNS=$((TOTAL_RUNS + ${#idx[@]}))
  [ "$b" -gt "$BIGGEST" ] && BIGGEST=$b
done
[ "$TOTAL_RUNS" -gt 0 ] || die preflight "no runs" "the chosen groups have no rows in tier $TIER; nothing was submitted"
NEED_TIMEOUT=$((BIGGEST + LIMIT_MARGIN_S))

# --- print-only mode --------------------------------------------------------
if [ "$PRINT" = yes ]; then
  echo "tla plan (print only: nothing is submitted and the job scheduler is not contacted)"
  echo "  tier:       $TIER"
  if [ -n "${TLA_SPEC_DIR:-}" ]; then echo "  specs:      $SPEC_DIR (TLA_SPEC_DIR; not pinned)"; else echo "  specs:      $SPEC_DIR (pinned)"; fi
  if [ -n "${TLA_SUITE:-}" ]; then echo "  manifest:   $SUITE (TLA_SUITE; not pinned)"; else echo "  manifest:   $SUITE (pinned)"; fi
  echo "  pin check:  OK ($PIN_N files match PROVENANCE.txt; $PIN_SKIPPED overridden, not checked)"
  [ "$OWN_N" -eq 0 ] || echo "  cfgs:       $OWN_N from $OWN_CFG (next to TLA_SUITE; not pinned)"
  echo "  jobs:       $((1 + ${#PLAN_GROUPS[@]})), one at a time, each with --network off and one notice to TLA_CHANNEL"
  echo "  runs:       $TOTAL_RUNS"
  echo "  job parse:  ${#PARSE_IDX[@]} runs (one cfg per spec module, simulation depth 1), budget $PARSE_BUDGET s"
  for k in "${!PLAN_GROUPS[@]}"; do
    printf '  job %-6s %3d runs, budget %5d s\n' "${PLAN_GROUPS[$k]}:" "${PLAN_COUNT[$k]}" "${PLAN_BUDGET[$k]}"
  done
  for g in "${SKIPPED_GROUPS[@]}"; do echo "  group $g:   no rows in tier $TIER, skipped"; done
  echo "  limits:     the scheduler must give each job a run timeout of at least $NEED_TIMEOUT s and at least 24 GiB of memory (checked on the parse job's submit)"
  if [ -n "$CHANNEL" ]; then echo "  TLA_CHANNEL: set"; else echo "  TLA_CHANNEL: not set (make tla refuses without it)"; fi
  if command -v "$JS" >/dev/null 2>&1 && [ -x "$(command -v "$JS")" ]; then
    echo "  TLA_JOBSCHED: $JS (found)"
  else
    echo "  TLA_JOBSCHED: $JS (not found or not executable; make tla refuses until it names the CLI's absolute path)"
  fi
  exit 0
fi

# --- preflight: channel, CLI, run directory, dispatcher ----------------------------
[ -n "$CHANNEL" ] || die preflight "TLA_CHANNEL not set" "TLA_CHANNEL is required: the job scheduler's notice channel ID (must be on the scheduler's allow-list); nothing was submitted"
JS_PATH=$(command -v "$JS" 2>/dev/null)
if [ -z "$JS_PATH" ] || [ ! -f "$JS_PATH" ] || [ ! -x "$JS_PATH" ]; then
  die preflight "scheduler CLI not found" "the job scheduler CLI '$JS' was not found or is not executable; pass its absolute path as TLA_JOBSCHED=<path>; nothing was submitted"
fi
JS=$JS_PATH

REPO=$(cd "$ROOT/../.." && pwd -P)
TBASE=${TMPDIR:-/tmp}
TBASE_REAL=$(cd "$TBASE" 2>/dev/null && pwd -P) || die preflight "bad TMPDIR" "TMPDIR '$TBASE' is not a directory; nothing was submitted"
case "$TBASE_REAL/" in
  "$REPO"/*) die preflight "bad TMPDIR" "TMPDIR '$TBASE' lies inside the repo; job contexts must stay outside the tracked tree; nothing was submitted" ;;
esac

out=$("$JS" list --json); rc=$?
[ "$rc" -eq 0 ] || die preflight "scheduler list failed" "'list --json' exited $rc: $(jq -r '.error // empty' <<<"$out" 2>/dev/null | head -c 300); nothing was submitted"
running=$(jq -r '.summary.dispatcher_running' <<<"$out" 2>/dev/null)
[ "$running" = true ] || die preflight "dispatcher not running" "the job scheduler's dispatcher is not running (list --json: dispatcher_running=${running:-unreadable}); a job submitted now would wait and run whenever it next starts, and there is no cancel operation; nothing was submitted"

RUN_DIR=$(mktemp -d "$TBASE_REAL/tla-run.XXXXXX") || die preflight "bad TMPDIR" "cannot create a run directory under '$TBASE'; nothing was submitted"
mkdir -p "$RUN_DIR/ctx" "$RUN_DIR/logs" "$RUN_DIR/runs" || die preflight "bad TMPDIR" "cannot populate $RUN_DIR; nothing was submitted"
echo "# run dir: $RUN_DIR (job contexts are removed as their jobs end; logs are kept)" >&2
echo "# tier $TIER: $TOTAL_RUNS runs in $((1 + ${#PLAN_GROUPS[@]})) jobs" >&2
[ "$OWN_N" -eq 0 ] || echo "# $OWN_N cfgs from $OWN_CFG (next to TLA_SUITE; not pinned)" >&2

# --- one job ----------------------------------------------------------------
# build_ctx <job> <runs-file> <budget>
build_ctx() {
  local d="$RUN_DIR/ctx/$1" s c
  mkdir -p "$d" || return 1
  cp "$ROOT/jobs/lib/Dockerfile" "$ROOT/jobs/lib/tlcjob.sh" "$ROOT/$JAR" "$d/" || return 1
  for s in $SPECS; do cp "$SPEC_DIR/$s.tla" "$d/" || return 1; done
  sed -i "s/^BUDGET=\${BUDGET:-[0-9]*}/BUDGET=\${BUDGET:-$3}/; s/^DISKCAP_MB=\${DISKCAP_MB:-[0-9]*}/DISKCAP_MB=\${DISKCAP_MB:-60000}/" "$d/tlcjob.sh" || return 1
  grep -qxF "BUDGET=\${BUDGET:-$3}" "$d/tlcjob.sh" || return 1
  # shellcheck disable=SC2016  # the literal line, not an expansion
  grep -qxF 'DISKCAP_MB=${DISKCAP_MB:-60000}' "$d/tlcjob.sh" || return 1
  cp "$2" "$d/runs" || return 1
  while read -r s c _; do cp "${CFG_PATH[$c]}" "$d/" || return 1; done < "$2"
}

# submit_job <job>: sets CUR_ID and SUB_JSON.
submit_job() {
  local rc err running
  CUR_PHASE=submit
  SUB_JSON=$("$JS" submit --context "$CUR_CTX" --name "tla-$1" --channel "$CHANNEL" --network off --json); rc=$?
  if [ "$rc" -ne 0 ]; then
    err=$(jq -r '.error // empty' <<<"$SUB_JSON" 2>/dev/null | head -c 300)
    rm -rf "$CUR_CTX"; CUR_CTX=
    die scheduler "submit refused" "the job scheduler refused job $1 (submit exit $rc${err:+: $err}); nothing more was submitted"
  fi
  CUR_ID=$(jq -r '.id // empty' <<<"$SUB_JSON" 2>/dev/null)
  [[ "$CUR_ID" =~ ^[A-Za-z0-9._-]+$ ]] || { CUR_ID=; die scheduler "no job id" "submit of job $1 exited 0 but returned no job id; nothing more was submitted"; }
  CUR_PHASE=submitted
  echo "# job $1: $CUR_ID" >&2
  running=$(jq -r '.dispatcher_running' <<<"$SUB_JSON" 2>/dev/null)
  [ "$running" = true ] || die scheduler "dispatcher not running" "job $1 ($CUR_ID) was queued but submit reports the dispatcher is not running; the job runs whenever the dispatcher next starts; nothing more was submitted"
}

# check_limits <needed timeout>: 0 when the submitted job's limits suffice, else
# 1 with LIMIT_ERR set.
check_limits() {
  local t m
  t=$(jq -r '.timeout_s // empty' <<<"$SUB_JSON" 2>/dev/null)
  m=$(jq -r '.memory // empty' <<<"$SUB_JSON" 2>/dev/null)
  if ! [[ "$t" =~ ^[0-9]+$ && "$m" =~ ^[0-9]+$ ]]; then
    LIMIT_ERR="submit reported no whole-number timeout_s and memory"; return 1
  fi
  if [ "$t" -lt "$1" ]; then
    LIMIT_ERR="a run timeout of $t s, under the $1 s needed (a group budget plus $LIMIT_MARGIN_S s)"; return 1
  fi
  if [ "$m" -lt "$MIN_MEMORY" ]; then
    LIMIT_ERR="$m bytes of memory, under the 24 GiB ($MIN_MEMORY bytes) needed"; return 1
  fi
  return 0
}

# wait_job <job>: polls until the job ends; sets JOB_STATE.
wait_job() {
  local out rc st term disp deadline=0 tmo
  tmo=$(jq -r '.timeout_s // 0' <<<"$SUB_JSON" 2>/dev/null)
  [[ "$tmo" =~ ^[0-9]+$ ]] || tmo=0
  while :; do
    out=$("$JS" status "$CUR_ID" --json); rc=$?
    if [ "$rc" -ne 0 ]; then
      [ "$rc" -eq 3 ] && die scheduler "status failed" "the job scheduler does not know job $1 ($CUR_ID) (status exit 3); nothing more was submitted"
      die scheduler "status failed" "status of job $1 ($CUR_ID) exited $rc; nothing more was submitted"
    fi
    st=$(jq -r '.state // empty' <<<"$out" 2>/dev/null)
    term=$(jq -r '.terminal' <<<"$out" 2>/dev/null)
    disp=$(jq -r '.dispatcher_running' <<<"$out" 2>/dev/null)
    case "$st" in
      succeeded|failed|build-failed|infrastructure-error|rejected) JOB_STATE=$st; return 0 ;;
      queued|building|running) [ "$term" = true ] && die scheduler "unknown job state" "job $1 ($CUR_ID) is '$st' but marked ended; nothing more was submitted" ;;
      *) die scheduler "unknown job state" "job $1 ($CUR_ID) reports state '${st:-unreadable}'; nothing more was submitted" ;;
    esac
    [ "$disp" = true ] || die scheduler "dispatcher stopped" "the dispatcher stopped while job $1 ($CUR_ID) was $st; nothing more was submitted"
    if [ "$st" != queued ] && [ "$deadline" -eq 0 ]; then deadline=$(($(date +%s) + tmo + WAIT_MARGIN_S)); fi
    if [ "$deadline" -gt 0 ] && [ "$(date +%s)" -ge "$deadline" ]; then
      die scheduler "job overran" "job $1 ($CUR_ID) is still $st more than $WAIT_MARGIN_S s past its $tmo s run timeout; nothing more was submitted"
    fi
    sleep "$POLL" & SLEEP_PID=$!
    wait "$SLEEP_PID"; SLEEP_PID=
  done
}

# fetch_log <job>: copies the job's run log through `results --json`; sets LOG
# (empty when there is none).
fetch_log() {
  local out rc p present
  LOG=
  out=$("$JS" results "$CUR_ID" --json); rc=$?
  if [ "$rc" -ne 0 ]; then echo "# job $1: results exited $rc; no run log" >&2; return 0; fi
  p=$(jq -r '.paths.run_log // empty' <<<"$out" 2>/dev/null)
  present=$(jq -r '.present.run_log' <<<"$out" 2>/dev/null)
  if [ "$present" = true ] && [ -n "$p" ] && [ -f "$p" ] && cp "$p" "$RUN_DIR/logs/$1.run.log"; then
    LOG="$RUN_DIR/logs/$1.run.log"
  else
    echo "# job $1: no run log" >&2
  fi
}

# run_job <job> <runs-file> <budget> <needed timeout>: build, submit, wait,
# remove the context, fetch the log; sets LOG. Too-low limits end the run once
# the job has ended.
run_job() {
  local limits_ok=yes
  CUR_PHASE=build; CUR_JOB=$1; CUR_ID=; CUR_CTX="$RUN_DIR/ctx/$1"
  if ! build_ctx "$1" "$2" "$3"; then
    rm -rf "$CUR_CTX"; CUR_CTX=
    die context "context build failed" "cannot build the context for job $1 under $RUN_DIR/ctx; nothing more was submitted"
  fi
  submit_job "$1"
  check_limits "$4" || limits_ok=no
  [ "$limits_ok" = yes ] || echo "# job $1: the scheduler gives it $LIMIT_ERR; waiting for it to end, then stopping" >&2
  wait_job "$1"
  rm -rf "$CUR_CTX"
  echo "# job $1: $JOB_STATE after $(suite_min) min (suite clock)" >&2
  fetch_log "$1"
  CUR_JOB=; CUR_ID=; CUR_CTX=; CUR_PHASE=
  [ "$limits_ok" = yes ] || die limits "scheduler limits too low" "the job scheduler gave job $1 $LIMIT_ERR; its limits must be raised first; nothing more was submitted"
}

# --- the parse-only job first; it gates the rest ------------------------------
P="$RUN_DIR/runs/parse"
for i in "${PARSE_IDX[@]}"; do echo "${R_SPEC[$i]} ${R_CFG[$i]} -simulate num=1 -depth 1"; done > "$P"
run_job parse "$P" "$PARSE_BUDGET" "$NEED_TIMEOUT"
parse_ok=yes
[ -n "$LOG" ] || parse_ok=no
for i in "${PARSE_IDX[@]}"; do
  [ "$parse_ok" = yes ] || break
  awk -v c="${R_CFG[$i]}" '$1 == "VERDICT" && $2 == c && (/ result=PARSE-OK / || / result=PARSE-OK$/) { f = 1 } END { exit !f }' "$LOG" || parse_ok=no
done
[ "$parse_ok" = yes ] || die parse-check "parse check failed" "the specs do not parse or evaluate (see ${LOG:-no log})"
echo "# parse check: OK" >&2

# --- one job per group, one at a time ---------------------------------------
OK=0; TOTAL=0
for g in "${SKIPPED_GROUPS[@]}"; do echo "# group $g: no rows in tier $TIER, skipped" >&2; done
for k in "${!PLAN_GROUPS[@]}"; do
  g=${PLAN_GROUPS[$k]}
  mapfile -t idx < <(group_rows "$g")
  R="$RUN_DIR/runs/$g"
  for i in "${idx[@]}"; do echo "${R_SPEC[$i]} ${R_CFG[$i]} @${R_CAP[$i]}"; done > "$R"
  run_job "$g" "$R" "${PLAN_BUDGET[$k]}" "$((PLAN_BUDGET[k] + LIMIT_MARGIN_S))"
  for i in "${idx[@]}"; do
    c=${R_CFG[$i]}
    TOTAL=$((TOTAL + 1))
    v=
    [ -n "$LOG" ] && v=$(awk -v c="$c" '$1 == "VERDICT" && $2 == c' "$LOG" | tail -1)
    res=$(sed -n 's/.* result=\([^ ]*\).*/\1/p' <<<"$v"); res=${res:-NO-VERDICT}
    dist=$(sed -n 's/.* distinct=\([^ ]*\).*/\1/p' <<<"$v")
    secs=$(sed -n 's/.* secs=\([^ ]*\).*/\1/p' <<<"$v")
    good=no; why=
    case "${R_EXP[$i]}:$res" in pass:PASS) good=yes ;; violation:FAIL\(*) good=yes ;; esac
    # A props row needs its result to name one of its props; a result naming
    # no single property (empty name) matches none.
    props=${R_PROPS[$i]}
    if [ "$good" = yes ] && [ -n "$props" ]; then
      name=$(sed -n -E 's/^FAIL\((Invariant|Action_property|Temporal_property)_([A-Za-z0-9_]+)_(is|was)_violated\)$/\2/p' <<<"$res")
      case ",$props," in
        *",$name,"*) ;;
        *) good=no; why=" (the expected violation is $props)" ;;
      esac
    fi
    if [ "$good" = yes ]; then OK=$((OK + 1)); tag=PASS; else tag=FAIL; fi
    printf '%s %-24s %-58s distinct=%s secs=%s -- %s%s\n' "$tag" "$c" "$res" "${dist:-?}" "${secs:-?}" "${R_WHAT[$i]}" "$why"
  done
done

rmdir "$RUN_DIR/ctx" 2>/dev/null
echo "# run dir: $RUN_DIR (logs kept)" >&2
MIN=$(suite_min)
if [ "$OK" -eq "$TOTAL" ] && [ "$TOTAL" -gt 0 ]; then
  echo "CI-VERDICT PASS ($OK/$TOTAL runs ok, $MIN min, tier $TIER)"; exit 0
else
  echo "CI-VERDICT FAIL ($OK/$TOTAL runs ok, $MIN min, tier $TIER)"; exit 1
fi
