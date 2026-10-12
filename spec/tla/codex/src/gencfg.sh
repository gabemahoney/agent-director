#!/usr/bin/env bash
# b.ssz: writes src/cfg/<run>.cfg for every run of ../suite.tsv. Text only: it runs no TLC.
# Usage: bash gencfg.sh   (from anywhere)
set -eu
HERE=$(cd "$(dirname "$0")" && pwd)
OUT="$HERE/cfg"
mkdir -p "$OUT"

# ---------------------------------------------------------------- Phase5HookCx
# p5 <run> <comment> <SPEC> <MaxClock> <PidPool> <NullStart> <Stray/Leftover> <Probes> <knobs> <checks>
#   knobs: space-separated definition names turned on (Name <- KnobOn)
#   checks: the INVARIANT/PROPERTY lines, \n-separated
p5() {
  local run=$1 what=$2 spec=$3 mc=$4 pool=$5 ns=$6 sl=$7 probes=$8 knobs=$9 checks=${10}
  {
    printf '\\* %s: %s\n' "$run" "$what"
    printf '\\* b.ssz (tickets/Ideas/b.ssz/tla/README.md). Phase5Hook design values; b.ssz knobs at the end of CONSTANTS.\n'
    printf 'SPECIFICATION %s\n' "$spec"
    printf 'CONSTANTS\n'
    printf '  MaxClock = %s\n  MaxLife = 2\n  MaxFaults = 1\n  PidPool = %s\n' "$mc" "$pool"
    printf '  Gate = "parent"\n  AdoptBy = "adpane"\n  KillAll = TRUE\n'
    printf '  AllowNested = TRUE\n  AllowTeammate = TRUE\n  AllowStray = %s\n  AllowLeftover = %s\n' "$sl" "$sl"
    printf '  AllowRotate = TRUE\n  AllowHup = TRUE\n  AllowLostReply = TRUE\n  AllowNullStart = %s\n' "$ns"
    printf '  AllowCrash = TRUE\n  Probes = %s\n  FMNoPaneGone = TRUE\n  HookWait = TRUE\n  AllowSlowIdentity = FALSE\n' "$probes"
    for k in $knobs; do printf '  %s <- KnobOn\n' "$k"; done
    printf '%b\n' "$checks"
    printf 'CHECK_DEADLOCK FALSE\n'
  } > "$OUT/$run.cfg"
}

CX="Codex PauseOn PauseSeesGone"          # the brief's rule, pause modelled, the fix (F1)
CXB="Codex PauseOn"                       # the brief's rule as written (pause's wait polls state only)
SAFE='INVARIANT RowOnlyByAgent KillAllGone ClockBound CxNoEndedWhileAlive'

# knobs off: must equal b.zuj run 4's ci_H_Sns (PASS, 11,387,737 distinct states)
p5 hx_eq_Sns "knobs off: ci_H_Sns exactly (expect PASS with 11,387,737 distinct states, b.zuj CI run 4)" \
   Spec 4 0 TRUE TRUE FALSE "" 'INVARIANT RowOnlyByAgent KillAllGone ClockBound'
p5 hx_S "Codex rule + pause (fix): gate safety and ended-only-when-gone, whole world, unreadable start (ci_H_Sns bounds)" \
   Spec 4 0 TRUE TRUE FALSE "$CX" "$SAFE"
p5 hx_S5 "Codex rule + pause (fix): as hx_S with pid reuse and readable start (MaxClock 5, PidPool 3)" \
   Spec 5 3 FALSE TRUE FALSE "$CX" "$SAFE"
p5 hx_L "Codex rule + pause (fix): PendingResolves, ReportInResolves, CxEndResolves, PauseSeesEnd (find-missing fair)" \
   FairSpecP 4 3 FALSE TRUE FALSE "$CX" 'PROPERTY PendingResolves ReportInResolves CxEndResolves PauseSeesEnd'
p5 hx_P "the fix: pause's own wait reaches ended on a Codex row with find-missing not scheduled (PauseSpec)" \
   PauseSpec 4 3 FALSE FALSE FALSE "$CX" 'PROPERTY PauseSeesEnd'
p5 hx_C_pause "control = the brief as written: pause's wait only polls the state, find-missing not scheduled: the row stays waiting" \
   PauseSpec 4 3 FALSE FALSE FALSE "$CXB" 'PROPERTY PauseSeesEnd'
p5 hx_C_endnow "control: a Codex SessionEnd ends the row at once (Claude's rule): /new ends a row whose agent goes on running" \
   Spec 4 3 FALSE TRUE FALSE "Codex CxEndNow" 'INVARIANT CxNoEndedWhileRunning'
p5 hx_V_followed "probe: /new's SessionEnd records an end report, then the next SessionStart follows the new session and clears it" \
   Spec 5 3 FALSE TRUE TRUE "$CX" 'INVARIANT NotCxFollowed'
p5 hx_V_fmended "probe: the agent exits after its end report and find-missing marks the row ended" \
   Spec 5 3 FALSE TRUE TRUE "$CX" 'INVARIANT NotCxFMEnded'
p5 hx_V_pauseended "probe: pause's wait (the fix) writes ended once the agent is gone after its end report" \
   Spec 5 3 FALSE TRUE TRUE "$CX" 'INVARIANT NotCxPauseEnded'
p5 hx_V_endlive "probe: an end report while the agent keeps running (in-TUI /new)" \
   Spec 5 3 FALSE TRUE TRUE "$CX" 'INVARIANT NotCxEndLive'
p5 hx_V_missing "probe: a Codex agent gone with no end report reads missing" \
   Spec 5 3 FALSE TRUE TRUE "$CX" 'INVARIANT NotCxMissing'

# ---------------------------------------------------------------- Hold36hCx
# hx36 <run> <comment> <MaxTurns> <Subagent> <Env> <Probe> <knobs> <checks>
h36() {
  local run=$1 what=$2 turns=$3 subag=$4 env=$5 probe=$6 knobs=$7 checks=$8
  {
    printf '\\* %s: %s\n' "$run" "$what"
    printf '\\* b.ssz (tickets/Ideas/b.ssz/tla/README.md). The merged relay (Design fixcut, Write cond); b.ssz knobs at the end of CONSTANTS.\n'
    printf 'SPECIFICATION Spec\n'
    printf 'CONSTANTS\n'
    printf '  MaxTurns = %s\n  Subagent = %s\n  Design = "fixcut"\n  Write = "cond"\n' "$turns" "$subag"
    printf '  Env = %s\n  Probe = "%s"\n' "$env" "$probe"
    for k in $knobs; do printf '  %s <- KnobOn\n' "$k"; done
    printf 'CHECK_DEADLOCK FALSE\n'
    printf '%b\n' "$checks"
  } > "$OUT/$run.cfg"
}

h36 rx_eq_live "knobs off: b.36h's cond_live exactly (expect PASS with 109,518 distinct states, b.36h verdicts)" \
    2 TRUE '{"HookDeath", "Claim"}' "" "" \
    'INVARIANT TypeOK\nINVARIANT WorkingTrue\nINVARIANT NoStaleHold\nPROPERTY RowSettles'
h36 rx_safe "Codex events: the working hold's safety, subagent, two turns, every switch (b.36h's cond_safe under Codex)" \
    2 TRUE '{"HookDeath", "BothEndsDie", "SlowIdle", "Claim"}' "" "CxRelay" \
    'INVARIANT TypeOK\nINVARIANT WorkingTrue\nINVARIANT NoStaleHold'
h36 rx_keys "Codex events: plain send-keys and pause never go while a dialog is shown (step 2c), with the last-gasp window, subagent, two turns" \
    2 TRUE '{"HookDeath", "BothEndsDie", "Claim", "LastGasp"}' "" "CxRelay" \
    'INVARIANT TypeOK\nINVARIANT KeysSafe\nINVARIANT NoStaleHold'
h36 rx_live "Codex events: every property, RowSettles restated (RowSettlesCx) and the hold settles (KeysSettle), subagent, two turns" \
    2 TRUE '{"HookDeath", "Claim"}' "" "CxRelay" \
    'INVARIANT TypeOK\nINVARIANT WorkingTrue\nINVARIANT NoStaleHold\nPROPERTY RowSettlesCx\nPROPERTY KeysSettle'
h36 rx_C_intnoend "control: Codex's Interrupt not counted as turn_end: an interrupted main request holds plain send-keys for good" \
    1 FALSE '{"Claim"}' "" "CxRelay CxIntNoTurnEnd" \
    'PROPERTY KeysSettle'
h36 rx_C_subproof "control: the main turn end also proves the subagent's request: plain keys reach its shown dialog" \
    1 TRUE '{"LastGasp"}' "" "CxRelay CxTurnEndProvesSub" \
    'INVARIANT KeysSafe'
h36 rx_C_subint "control: a subagent's Interrupt is unmarked and read as the main turn end: plain keys reach the main agent's shown dialog" \
    1 TRUE '{"LastGasp"}' "" "CxRelay CxSubIntUnmarked" \
    'INVARIANT KeysSafe'
h36 rx_lim_rowsettles "documented difference: Claude's RowSettles under Codex events fails (no automatic repair of a row left working)" \
    1 TRUE '{"Claim"}' "" "CxRelay" \
    'PROPERTY RowSettles'
h36 rx_V_subheld "probe: a subagent's request allowed through the relay still holds plain send-keys at rest (until the agent ends)" \
    1 TRUE '{}' "" "CxRelay" \
    'INVARIANT NeverSubAllowHeld'
h36 rx_V_intproof "probe: Codex's Interrupt hook proves a main request cancelled at its dialog" \
    1 FALSE '{}' "intproof" "CxRelay" \
    'INVARIANT NeverHit'
h36 rx_claude_keys "baseline, knobs off (Claude): KeysSafe with the last-gasp window, every switch" \
    2 TRUE '{"HookDeath", "BothEndsDie", "SlowIdle", "Claim", "LastGasp"}' "" "" \
    'INVARIANT TypeOK\nINVARIANT KeysSafe\nINVARIANT NoStaleHold'

# ---------------------------------------------------------------- B146R5Cx
# b.146 round 5's fin1_time_ub and fin1_t_ub (r5/cfg on design/b146-timeouts) with CxR5 on.
R5CFG=/home/horde/projects/agent-director-project/agent-director-lane-timeouts/spec/tla/r5/cfg
r5() {
  local run=$1 base=$2 what=$3
  {
    printf '\\* %s: %s\n' "$run" "$what"
    printf '\\* b.ssz (tickets/Ideas/b.ssz/tla/README.md): b.146 r5/cfg/%s.cfg with CxR5 <- KnobOn added.\n' "$base"
    sed -e '/^\\\*/d' -e 's/^  R5 = \(.*\)$/  R5 = \1\n  CxR5 <- KnobOn/' "$R5CFG/$base.cfg"
  } > "$OUT/$run.cfg"
}
r5 r5x_time fin1_time_ub "Codex events on round 5's integrated relay model (Unproven + Bypass): every property, the timing fallback (fin1_time_ub)"
r5 r5x_t fin1_t_ub "Codex events on round 5's integrated relay model (Unproven + Bypass): safety, 0.5 s ticks (fin1_t_ub)"

# ---------------------------------------------------------------- the Q2 gate (the /review thread)
# New cfgs only; every cfg above is unchanged (the md5 of each was checked before and after).
# Knobs-off equivalence, level 1: the knobs-off cfgs again, on the final model text.
h36 rx_eq_live_g "knobs off, on the Q2-gate model text: b.36h's cond_live exactly (expect PASS with 109,518 distinct states)" \
    2 TRUE '{"HookDeath", "Claim"}' "" "" \
    'INVARIANT TypeOK\nINVARIANT WorkingTrue\nINVARIANT NoStaleHold\nPROPERTY RowSettles'
# Level 2: rx_safe, rx_keys, rx_live, r5x_time, r5x_t re-run, new knobs off (cfg = the original run's, new name).
h36 rx_safe_k0 "rx_safe's cfg again on the Q2-gate model text, CxReview off (expect PASS with 64,027 distinct states)" \
    2 TRUE '{"HookDeath", "BothEndsDie", "SlowIdle", "Claim"}' "" "CxRelay" \
    'INVARIANT TypeOK\nINVARIANT WorkingTrue\nINVARIANT NoStaleHold'
h36 rx_keys_k0 "rx_keys's cfg again on the Q2-gate model text, CxReview off (expect PASS with 67,511 distinct states)" \
    2 TRUE '{"HookDeath", "BothEndsDie", "Claim", "LastGasp"}' "" "CxRelay" \
    'INVARIANT TypeOK\nINVARIANT KeysSafe\nINVARIANT NoStaleHold'
h36 rx_live_k0 "rx_live's cfg again on the Q2-gate model text, CxReview off (expect PASS with 35,589 distinct states)" \
    2 TRUE '{"HookDeath", "Claim"}' "" "CxRelay" \
    'INVARIANT TypeOK\nINVARIANT WorkingTrue\nINVARIANT NoStaleHold\nPROPERTY RowSettlesCx\nPROPERTY KeysSettle'
# The gate runs: the /review thread's unmarked hooks (CxReview).
h36 rx_safe_rv "Q2 gate: rx_safe + CxReview: the review's unmarked working writes keep WorkingTrue and NoStaleHold" \
    2 TRUE '{"HookDeath", "BothEndsDie", "SlowIdle", "Claim"}' "" "CxRelay CxReview" \
    'INVARIANT TypeOK\nINVARIANT WorkingTrue\nINVARIANT NoStaleHold'
h36 rx_keys_rv "Q2 gate: rx_keys + CxReview, with the last-gasp window: KeysSafe and NoStaleHold" \
    2 TRUE '{"HookDeath", "BothEndsDie", "Claim", "LastGasp"}' "" "CxRelay CxReview" \
    'INVARIANT TypeOK\nINVARIANT KeysSafe\nINVARIANT NoStaleHold'
h36 rx_live_rv "Q2 gate: rx_live + CxReview: WorkingTrue, NoStaleHold, RowSettlesCx (no new exception), KeysSettle" \
    2 TRUE '{"HookDeath", "Claim"}' "" "CxRelay CxReview" \
    'INVARIANT TypeOK\nINVARIANT WorkingTrue\nINVARIANT NoStaleHold\nPROPERTY RowSettlesCx\nPROPERTY KeysSettle'
h36 rx_V_review "Q2 gate probe: a review write leaves the row working at rest (must be reached)" \
    1 TRUE '{}' "review" "CxRelay CxReview" \
    'INVARIANT NeverHit'

# B146R5Cx. r5v <run> <base cfg> <what> <mode> <knobs>
#   mode: keep (same checks), rowsv (RowSettles replaced by RowSettlesRv), lim (RowSettles only),
#         probe (NeverHit, Probe = "review")
r5v() {
  local run=$1 base=$2 what=$3 mode=$4 knobs=$5
  {
    printf '\\* %s: %s\n' "$run" "$what"
    printf '\\* b.ssz (tickets/Ideas/b.ssz/tla/README.md): b.146 r5/cfg/%s.cfg with %s added.\n' "$base" "$knobs"
    sed -e '/^\\\*/d' "$R5CFG/$base.cfg" | awk -v knobs="$knobs" -v mode="$mode" '
      /^  R5 = / { print; n = split(knobs, k, " "); for (i = 1; i <= n; i++) print "  " k[i] " <- KnobOn"; next }
      /^  Probe = / { if (mode == "probe") print "  Probe = \"review\""; else print; next }
      /^(INVARIANT|PROPERTY) / {
        if (mode == "keep") print
        else if (mode == "rowsv") { if ($0 == "PROPERTY RowSettles") print "PROPERTY RowSettlesRv"; else print }
        next }
      { print }
      END { if (mode == "lim") print "PROPERTY RowSettles"; if (mode == "probe") print "INVARIANT NeverHit" }'
  } > "$OUT/$run.cfg"
}
# Level 1 and 2 (knobs off): r5x_eq's cfg again; r5x_time's and r5x_t's cfgs again under new names.
{ sed -e '1s/^\\\* r5x_eq:/\\* r5x_eq_g:/' -e '2s/.*/\\* Expected: PASS with exactly 833,740 distinct states (the same cfg as r5x_eq, on the Q2-gate model text)./' "$OUT/r5x_eq.cfg"; } > "$OUT/r5x_eq_g.cfg"
{ sed -e '1s/^\\\* r5x_time:/\\* r5x_time_k0 (r5x_time'"'"'s cfg on the Q2-gate model text, CxRv off; expect 296,382):/' "$OUT/r5x_time.cfg"; } > "$OUT/r5x_time_k0.cfg"
{ sed -e '1s/^\\\* r5x_t:/\\* r5x_t_k0 (r5x_t'"'"'s cfg on the Q2-gate model text, CxRv off; expect 2,122,224):/' "$OUT/r5x_t.cfg"; } > "$OUT/r5x_t_k0.cfg"
r5v r5x_time_rv fin1_time_ub "Q2 gate: Codex events + the /review thread on round 5's integrated model (Unproven + Bypass): every property, RowSettlesRv in place of RowSettles" rowsv "CxR5 CxRv"
r5v r5x_t_rv fin1_t_ub "Q2 gate: Codex events + the /review thread, safety, 0.5 s ticks (the 15 invariants)" keep "CxR5 CxRv"
r5v r5x_lim_rowsettles_rv fin1_time_ub "Q2 gate limit: RowSettles unrestated fails with the /review thread's writes (the documented residual)" lim "CxR5 CxRv"
r5v r5x_V_review fin1_time_ub "Q2 gate probe: a review write leaves the row working at rest (must be reached)" probe "CxR5 CxRv"
