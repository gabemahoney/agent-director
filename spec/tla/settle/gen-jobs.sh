#!/usr/bin/env bash
# Generates cfg/<name>.cfg and the self-contained job directory job-<name>/ for every run of
# B3ieSettle.tla (b.3ie). Each job directory holds: Dockerfile (job-Dockerfile), tlcjob.sh and
# tla2tools.jar (byte-identical to the repo's spec/tla/jobs/lib/tlcjob.sh and spec/tla/tla2tools.jar),
# B3ieSettle.tla (byte-identical to the master here), one cfg and `runs`.
# It runs no TLC. Usage: bash gen-jobs.sh
set -eu
B=/home/horde/projects/agent-director-project/tickets/Bugs/b.3ie
REPO=/home/horde/projects/agent-director-project/agent-director-b.3ie
mkdir -p "$B/cfg"

KEYS="NHooks MaxT InitT MaxCfgChanges MaxTime MaxWrites InitStates FreshInsert HookVals HookFromTerminal Commands Guards ReadClock Mut"

# job NAME CAP EXPECTED COMMENT OVERRIDES(k=v;k=v) INVARIANTS
job() {
  local name=$1 cap=$2 expect=$3 comment=$4 over=$5 invs=$6
  declare -A c=(
    [NHooks]=2 [MaxT]=2 [InitT]=2 [MaxCfgChanges]=0 [MaxTime]=4 [MaxWrites]=3
    [InitStates]='{"waiting", "working"}' [FreshInsert]=FALSE
    [HookVals]='{"waiting", "working", "check_permission", "ended"}'
    [HookFromTerminal]=TRUE [Commands]=TRUE [Guards]=TRUE
    [ReadClock]='"inline"' [Mut]='"none"')
  local p
  IFS=';' read -ra kv <<< "$over"
  for p in "${kv[@]}"; do
    [ -n "$p" ] || continue
    c[${p%%=*}]=${p#*=}
  done
  local cfg="$B/cfg/$name.cfg"
  {
    printf '\\* %s: %s\n' "$name" "$comment"
    printf '\\* Expected: %s\n' "$expect"
    echo 'SPECIFICATION Spec'
    echo 'CONSTANTS'
    for k in $KEYS; do printf '  %s = %s\n' "$k" "${c[$k]}"; done
    echo 'CHECK_DEADLOCK FALSE'
    for i in $invs; do echo "INVARIANT $i"; done
  } > "$cfg"
  local d="$B/job-$name"
  mkdir -p "$d"
  cp "$B/job-Dockerfile" "$d/Dockerfile"
  cp "$REPO/spec/tla/jobs/lib/tlcjob.sh" "$REPO/spec/tla/tla2tools.jar" "$B/B3ieSettle.tla" "$cfg" "$d/"
  printf '# one run per job. Expected: %s\nB3ieSettle %s @%s\n' "$expect" "$name" "$cap" > "$d/runs"
}

ALL="TypeOK ZeroIsRaw SettledIsSpec HeldOrLastSettled ReadsLinearizable ImmediateAtOnce GuardsReadRaw NeverUnsupported WellDefined BeeFormReaderT"
TCH="TypeOK ZeroIsRaw SettledIsSpec HeldOrLastSettled ReadsLinearizable ImmediateAtOnce GuardsReadRaw NeverUnsupported WellDefined"
OK2="TypeOK ZeroIsRaw ImmediateAtOnce GuardsReadRaw NeverUnsupported"
RD="TypeOK ZeroIsRaw SettledIsSpec HeldOrLastSettled ImmediateAtOnce GuardsReadRaw NeverUnsupported"
F() { echo "FAIL(Invariant_$1_is_violated)"; }

# ---- green ----
job pass_const 2900 PASS "the design, T = 2 constant, every invariant" \
  'MaxWrites=4;FreshInsert=TRUE' "$ALL"
job pass_const_t1 2900 PASS "the design, T = 1 constant, every invariant" \
  'InitT=1;MaxWrites=4;FreshInsert=TRUE' "$ALL"
job pass_t0 2900 PASS "the design at T = 0 (off), every invariant" \
  'InitT=0;MaxWrites=4;FreshInsert=TRUE' "$ALL"
job pass_before 2900 PASS "the design, reader takes its clock before it reads the row (negative ages reachable)" \
  'FreshInsert=TRUE;ReadClock="before"' "$ALL"
job pass_noterm 2900 PASS "the design with the guard it assumed: hooks never move a terminal row" \
  'FreshInsert=TRUE;HookFromTerminal=FALSE' "$ALL"
job pass_immnostamp 2900 PASS "robustness: immediate writes leave state_changed_ms alone (a verbatim reuse restore); nothing changes" \
  'FreshInsert=TRUE;Mut="immnostamp"' "$ALL"
job pass_tchange 2900 PASS "the design with T changing (1 -> any, twice) between writes and reads: invariants 1, 3, 4, 5" \
  'InitT=1;MaxCfgChanges=2;FreshInsert=TRUE' "$TCH"
job pass_tchange_before 2900 PASS "as pass_tchange, reader clock before the row read" \
  'InitT=1;MaxCfgChanges=2;FreshInsert=TRUE;ReadClock="before"' "$TCH"

# ---- red: one seeded mutation each ----
job red_strictgt 900 "$(F ZeroIsRaw)" "inv 1: settled test age > T (no T = 0 short-cut) at T = 0" \
  'InitT=0;Mut="strictgt"' "TypeOK ImmediateAtOnce GuardsReadRaw NeverUnsupported ZeroIsRaw"
job red_prevraw 900 "$(F SettledIsSpec)" "inv 2: the base is the previous raw value, not the last settled one (A->B->C)" \
  'Mut="prevraw"' "$OK2 SettledIsSpec"
job red_samerestart 900 "$(F SettledIsSpec)" "inv 2: a same-value hook write restarts the clock" \
  'Mut="samerestart"' "$OK2 SettledIsSpec"
job red_stalebase 900 "$(F SettledIsSpec)" "inv 2: base and same-value test from a Go-side read before the UPDATE (concurrent duplicate hooks)" \
  'Mut="stalebase"' "TypeOK ZeroIsRaw GuardsReadRaw NeverUnsupported SettledIsSpec"
job red_nowoutside 900 "$(F SettledIsSpec)" "inv 2: the writer's now is taken before the write lock (a bound parameter)" \
  'Mut="nowoutside"' "$OK2 SettledIsSpec"
job red_negage 900 "$(F ReadsLinearizable)" "inv 2 (reads): a negative age counts as settled; reader clock before the row read" \
  'ReadClock="before";Mut="negagesettles"' "$RD ReadsLinearizable"
job red_readafter 900 "$(F ReadsLinearizable)" "inv 2 (reads): the reader reads the row, then takes its clock" \
  'ReadClock="after"' "$RD ReadsLinearizable"
job red_termsettles 900 "$(F ImmediateAtOnce)" "inv 3: a hook's ended goes through the settle rule" \
  'Mut="termsettles"' "TypeOK ZeroIsRaw GuardsReadRaw NeverUnsupported ImmediateAtOnce"
job red_cmdsettles 900 "$(F ImmediateAtOnce)" "inv 3: command writes go through the settle rule" \
  'Mut="cmdsettles"' "TypeOK ZeroIsRaw GuardsReadRaw NeverUnsupported ImmediateAtOnce"
job red_guardreported 900 "$(F GuardsReadRaw)" "inv 4: a guard acts on the reported state" \
  'Mut="guardreported"' "TypeOK ZeroIsRaw SettledIsSpec HeldOrLastSettled ReadsLinearizable ImmediateAtOnce NeverUnsupported WellDefined GuardsReadRaw"
job red_insertnobase 900 "$(F NeverUnsupported)" "inv 5, T changing: InsertPending leaves settled_state at its column default ''" \
  'InitT=1;MaxCfgChanges=1;InitStates={};FreshInsert=TRUE;Mut="insertnobase"' "TypeOK ZeroIsRaw GuardsReadRaw NeverUnsupported"

# ---- control and limit ----
job ctl_today 900 "$(F HeldOrLastSettled)" "control: today (reported = raw) with T = 2, checked in the bee's form" \
  'Mut="today"' "$OK2 HeldOrLastSettled"
job lim_tchange_bee 900 "$(F BeeFormReaderT)" "limit: after a T change, a base judged under the old T is reported (held < the reader's T)" \
  'InitT=1;MaxCfgChanges=1' "TypeOK ZeroIsRaw ImmediateAtOnce GuardsReadRaw NeverUnsupported WellDefined BeeFormReaderT"

# ---- probes (written to be violated) ----
probe() { job "probe_$1" 900 "$(F "$2")" "probe: $3" "$4" "TypeOK $2"; }
probe flap ProbeFlap "A -> B -> A within T is reached and a read returns A" ''
probe shortturn ProbeShortTurn "the short turn waiting -> working -> waiting within T reads waiting" ''
probe holdsold ProbeHoldsOld "a read during an unsettled hook change returns the old settled value" ''
probe chaina ProbeChainHoldsA "A -> B -> C within T reads A" ''
probe chainc ProbeChainReachesC "A -> B -> C reads C once C held T from the B -> C change" ''
probe dup ProbeDup "a duplicate hook write (same value, other hook, <= 1 ms) lands" ''
probe twohooks ProbeTwoHooks "two hook processes in flight with the same value" ''
probe tchange ProbeTChangeRead "T changes between a hook write and a read" 'MaxCfgChanges=1'
probe guarddiff ProbeGuardDiffers "a guard acts on raw while the reported state differs" ''
probe hookleavesterm ProbeHookLeavesTerminal "a hook moves a terminal row (missing/ended) to a live state" ''
probe cmdcuts ProbeCmdCutsSettle "a command write lands while a hook change is unsettled" ''
