#!/usr/bin/env python3
"""ci/mkci.py -- writes the on-demand TLA+ design check (README.md) for the LABEL design (../Phase4.tla, ../Phase4Split.tla):
the cfgs in ci/cfg/ and the manifest ci/suite.tsv. Only writes files; runs nothing.

Manifest columns (tab-separated): group, spec, cfg, cap_seconds, expect, tier, what
  expect = pass       -> the run must end PASS
  expect = violation  -> the run must end FAIL(...) (controls, probes, documented residuals)
  tier   = fast       -> in the fast tier (TIER=fast, the default; about 90 min) and in the full tier
  tier   = full       -> only in the full tier (TIER=full; about 7 h, optional)
The fast tier: every must-fail run (controls, probes, residuals; seconds each) plus the
pass runs named in FAST_PASS below (see README.md for what it gives up).
Groups run in order, one jobsched job per group (run_ci.sh). Every run is capped at 1800 s.
Scope (Gabe, 2026-09-28): accidents only; respawn and remain-on-exit out of scope."""
import os

HERE = os.path.dirname(os.path.abspath(__file__))
CFG = os.path.join(HERE, "cfg")
os.makedirs(CFG, exist_ok=True)

BASE = dict(MaxSid="3", MaxLife="3", MaxFaults="1", ResumeEnabled="TRUE", EnableExpire="TRUE",
            AllowStray="TRUE", WallInit="{FALSE, TRUE}", EnableOrch="TRUE", EnableBot="TRUE",
            EnableSweeper="TRUE", OrchNames='{"nx"}', EnableHumanResume="FALSE",
            EnableHumanKill="TRUE", HistoryByLife="TRUE", ApproverPrompt="FALSE", Probes="FALSE",
            ResumeSendOK="TRUE", AllowLeftover="FALSE", EnableCrash="TRUE",
            AllowRename="FALSE", AllowGroup="FALSE", AllowRespawn="FALSE", RemainOnExit="FALSE",
            AllowLabelFail="FALSE", AllowRestart="FALSE", WrongServerFault="FALSE",
            CallerWrongServer="FALSE", AllowLostReply="TRUE",
            A1TokenKept="TRUE", A2ByLabel="TRUE", A3DollarById="TRUE", A4LabelRecover="TRUE",
            A5PaneKill="TRUE", A6RecordedSock="TRUE", A7ProcLiveness="TRUE", ActPidCheck="TRUE",
            Ghosts="TRUE",
            HookGate="TRUE", SpawnScan="TRUE", StoreInLabel="TRUE", AllowOtherStore="FALSE",
            LeftoverLabelled="TRUE")
ALL = ("NoOrphan NoOrphanLife OneAgentPerId HandsOff NonGoneInert MarksRightLife ResumeNoEarlierLife "
       "NoWrongMemory GetNoEarlierLife SendOnlyCurrentLaunch BotNeverKillsFinished "
       "KillFinishedOnlyLeftover KillHonest VerdictSound SidBound")
ALLS = ("NoOrphanS NoOrphanLifeS OneAgentPerIdS HandsOff NonGoneInert MarksRightLife ResumeNoEarlierLife "
        "NoWrongMemoryS GetNoEarlierLife SendOnlyCurrentLaunch BotNeverKillsFinished "
        "KillFinishedOnlyLeftover KillHonest VerdictSoundS SidBound")
NOORPH = ("HandsOff NonGoneInert MarksRightLife ResumeNoEarlierLife GetNoEarlierLife SendOnlyCurrentLaunch "
          "BotNeverKillsFinished KillFinishedOnlyLeftover KillHonest VerdictSound SidBound")
AP = "NoFalseMissing KillPendingOnlyCurrent"
ORCH = dict(EnableBot="FALSE")
BOTH = dict(EnableOrch="FALSE", EnableHumanResume="TRUE")
BOTK = dict(EnableOrch="FALSE")
GL = dict(AllowLeftover="TRUE", **BOTK)
ACC = (("ren", dict(AllowRename="TRUE"), "rename"),
       ("grp", dict(AllowGroup="TRUE"), "grouped viewer"),
       ("lab", dict(AllowLabelFail="TRUE"), "failed label step"),
       ("rst", dict(AllowRestart="TRUE"), "server restart"),
       ("reb", dict(WrongServerFault="TRUE"), "different server at the socket"))
ALLACC = dict(AllowRename="TRUE", AllowGroup="TRUE", AllowLabelFail="TRUE", AllowRestart="TRUE",
              WrongServerFault="TRUE")
rows = []


def cfg(group, name, what, spec, inv="", prop="", expect="pass", cap=1800, view=False, **kv):
    c = dict(BASE)
    for k, v in kv.items():
        if k not in c:
            raise SystemExit("unknown constant " + k)
        c[k] = v
    with open(os.path.join(CFG, name + ".cfg"), "w") as f:
        f.write(f"\\* {name}: {what}\nSPECIFICATION {spec}\nCONSTANTS\n")
        for k, v in c.items():
            f.write(f"  {k} = {v}\n")
        if inv:
            f.write("INVARIANT " + inv + "\n")
        if prop:
            f.write("PROPERTY " + prop + "\n")
        if view:
            f.write("VIEW CIView\n")
        f.write("CHECK_DEADLOCK FALSE\n")
    mod = "Phase4Split" if spec.startswith(("Split", "FairSplit")) else "Phase4"
    rows.append((group, mod, name, str(cap), expect, what))


# ---- g1: controls (each amendment removed must be violated), residual, probes ----
cfg("g1", "ci_C1", "control A1: token cleared at report-in", "SplitSpec", NOORPH, AP, "violation",
    A1TokenKept="FALSE", **BOTK)
cfg("g1", "ci_C2", "control A2: lookup by name, rename", "SplitSpec", NOORPH, AP, "violation",
    A2ByLabel="FALSE", AllowRename="TRUE", **BOTK)
cfg("g1", "ci_C3", "control A3: `$` name chained", "SplitSpec", ALL, AP, "violation",
    A3DollarById="FALSE", OrchNames='{"d"}', **ORCH)
cfg("g1", "ci_C4", "control A4: failed label step leaves an unlabelled session", "SplitSpec", NOORPH, AP,
    "violation", A4LabelRecover="FALSE", AllowLabelFail="TRUE", **BOTK)
cfg("g1", "ci_C5", "control A5: kill-session only, grouped viewer", "SplitSpec", NOORPH, AP, "violation",
    A5PaneKill="FALSE", AllowGroup="TRUE", **BOTK)
cfg("g1", "ci_C6", "control A6: caller's server, no adoption", "SplitSpec", NOORPH, AP, "violation",
    A6RecordedSock="FALSE", CallerWrongServer="TRUE", **BOTK)
cfg("g1", "ci_C7", "control A7: session presence = alive (needs remain-on-exit; out-of-scope setting)",
    "FairSpecT", "", "StuckHeals", "violation", A7ProcLiveness="FALSE", RemainOnExit="TRUE",
    EnableBot="FALSE", AllowStray="FALSE", WallInit="{FALSE}", MaxSid="2", MaxLife="2", Ghosts="FALSE")
cfg("g1", "ci_Cact", "accepted residual: listing and act as separate steps + restart (declined if-shell guard)",
    "SplitSpec", ALL, AP, "violation", ActPidCheck="FALSE", AllowRestart="TRUE", **ORCH)
# decision-0929-shutdown.md: hook gating (decision 1) and the store id (decision 2)
cfg("g1", "ci_Chook", "control decision 1: hooks from any process move the row (no gating), unlabelled leftover "
    "+ rename (the ci_G3b_ren trace)", "SplitSpec", NOORPH, AP, "violation", HookGate="FALSE",
    LeftoverLabelled="FALSE", AllowRename="TRUE", **GL)
cfg("g1", "ci_Cstore", "control decision 2: no store id in the label, another store's agent with the same id",
    "SplitSpec", ALL, AP, "violation", StoreInLabel="FALSE", AllowOtherStore="TRUE", **ORCH)
cfg("g1", "ci_V_HookIgnored", "probe: a hook from another process is ignored", "SplitSpec", "NotHookIgnored",
    "", "violation", Probes="TRUE", LeftoverLabelled="FALSE", AllowRename="TRUE", **GL)
cfg("g1", "ci_V_ScanBlocks", "probe: a plain spawn is blocked by a labelled leftover", "SplitSpec",
    "NotScanBlocks", "", "violation", Probes="TRUE", **GL)
cfg("g1", "ci_V_OtherStore", "probe: another store's agent with this id is present", "SplitSpec",
    "NotOtherStore", "", "violation", Probes="TRUE", AllowOtherStore="TRUE", **ORCH)
PB = dict(Probes="TRUE", **GL, **ALLACC)
for p in ("Ours Leftover Gone CantDiff Cant OursRenamed Renamed Grouped KillWithViewer LabfailKilled "
          "LostReply Adopted Restarted RestartGone P1Conflict HeldEnd SquatGoneMark KillFailed "
          "HumanKillFinished").split():
    if p in ("Leftover", "P1Conflict"):
        # with the plain-spawn scan on, a labelled leftover of an id blocks that id's spawn, so no row
        # ever meets it (unreached in 30 min, CI run 2); the lookup's Leftover branch is probed with
        # the scan off (resume, reuse, find-missing and kill still classify it)
        cfg("g1", f"ci_V_{p}", f"probe: {p} reachable (spawn scan off)", "SplitSpec", "Not" + p, "",
            "violation", **dict(PB, SpawnScan="FALSE"))
        continue
    cfg("g1", f"ci_V_{p}", f"probe: {p} reachable", "SplitSpec", "Not" + p, "", "violation", **PB)
cfg("g1", "ci_V_DollarById", "probe: a `$` session carries its own current label", "SplitSpec",
    "NotDollarById", "", "violation", Probes="TRUE", OrchNames='{"d"}', **ORCH)
cfg("g1", "ci_V_ApproveResumed", "probe: send-keys reaches a resumed launch's pane", "SplitSpec",
    "NotApproveResumed", "", "violation", Probes="TRUE", **BOTH)

# ---- g2: liveness (Ghosts pinned; no VIEW) ----
LV = dict(Ghosts="FALSE", MaxSid="2", MaxLife="2")
cfg("g2", "ci_L5o", "PendingResolves, orchestrator + human kill, no stray, 2/2, all in-scope accidents",
    "FairSplitT", "", "PendingResolves", AllowStray="FALSE", **ORCH, **LV, **ALLACC)
for g, gk, gt in ACC:
    cfg("g2", f"ci_L5b_{g}", f"PendingResolves, bot + human resume + human kill, no stray, 2 sessions / 1 life; {gt}",
        "FairSplitT", "", "PendingResolves", EnableOrch="FALSE", EnableHumanResume="TRUE",
        AllowStray="FALSE", Ghosts="FALSE", MaxSid="2", MaxLife="1", **gk)
cfg("g2", "ci_L1", "StuckHeals, orchestrator, no stray, 2/2", "FairSpecT", "", "StuckHeals",
    EnableBot="FALSE", AllowStray="FALSE", **LV)

# ---- g3: safety, orchestrator side (3/3), one accident per run ----
for g, gk, gt in ACC:
    cfg("g3", f"ci_S1o_{g}", f"safety, orchestrator + human kill, 3/3; {gt}", "SplitSpec", ALL, AP,
        view=True, **ORCH, **gk)
cfg("g3", "ci_S1o_D", "safety, orchestrator with a `$` name, 3/3", "SplitSpec", ALL, AP, view=True,
    OrchNames='{"d"}', **ORCH)
cfg("g3", "ci_S3o", "safety, orchestrator on {n, nx} with bot sessions on n, 3/3", "SplitSpecF", ALLS, AP,
    view=True, EnableBot="FALSE", OrchNames='{"n","nx"}', EnableHumanKill="FALSE")

# ---- g4a/g4b: safety, bot side, one accident per run. Resized after CI run 1 (2026-09-29): bot + human
# resume + human kill at 3/3 did not finish in 30 min (75-86 M states, still growing). Now 3 sessions /
# 2 lives, and the two human actors in separate runs (as v3 did). ----
B2 = dict(MaxLife="2", EnableOrch="FALSE")
for g, gk, gt in ACC:
    cfg("g4a", f"ci_S1b_{g}", f"safety, bot + human kill (+unlatch), 3 sessions / 2 lives; {gt}", "SplitSpec",
        ALL, AP, view=True, **B2, **gk)
for g, gk, gt in ACC:
    cfg("g4b", f"ci_S1h_{g}", f"safety, bot + human resume, 3 sessions / 2 lives; {gt}", "SplitSpec",
        ALL, AP, view=True, EnableHumanResume="TRUE", EnableHumanKill="FALSE", **B2, **gk)
# ---- g5: safety, leftover, second store, shared names, unsplit cross-check ----
# (resized: G3b with a restart (CI run 1) or a rename (targeted run 2026-09-29) at 3/3 did not finish in
# 30 min -> 3 sessions / 2 lives for those)
SMALLG = ("rst", "ren")
for g, gk, gt in ACC:
    cfg("g5a", f"ci_G3b_{g}", f"safety, bot + human kill + leftover, 3/{'2' if g in SMALLG else '3'}; {gt}",
        "SplitSpec", NOORPH, AP, view=True, **GL, **gk, **(dict(MaxLife="2") if g in SMALLG else {}))
cfg("g5b", "ci_G3b_ren_u", "safety, as G3b_ren (3/2) with an UNLABELLED leftover (the scan cannot see it; hook "
    "gating must hold)", "SplitSpec", NOORPH, AP, view=True, LeftoverLabelled="FALSE", AllowRename="TRUE",
    MaxLife="2", **GL)
cfg("g5b", "ci_G3b_ren_noscan", "safety, as G3b_ren (3/2) with the pre-spawn scan off (hook gating alone)",
    "SplitSpec", NOORPH, AP, view=True, SpawnScan="FALSE", AllowRename="TRUE", MaxLife="2", **GL)
cfg("g5b", "ci_S_store", "safety, orchestrator + human kill, 3/3, another store's agent with the same id",
    "SplitSpec", ALL, AP, view=True, AllowOtherStore="TRUE", **ORCH)
cfg("g5b", "ci_S3b", "safety, bot with orchestrator sessions on n/nx, 3/3", "SplitSpecF", ALLS, AP, view=True,
    EnableOrch="FALSE", OrchNames='{"n","nx"}', EnableHumanKill="FALSE")
# unsplit cross-check, split three ways by accident (all accidents at once did not finish in 30 min)
for g, gk, gt in (("win", dict(AllowRename="TRUE", AllowGroup="TRUE"), "rename + grouped viewer"),
                  ("lab", dict(AllowLabelFail="TRUE"), "failed label step"),
                  ("srv", dict(AllowRestart="TRUE", WrongServerFault="TRUE"), "restart + different server")):
    # the fast tier (<10 min): one lifetime, no /proc wall (the split runs cover both at larger bounds);
    # 2/2 did not finish in 30 min (CI run 2: 63-68 M states)
    cfg("g5c", f"ci_smoke_{g}", f"safety, UNSPLIT bot + orchestrator + human kill, 2 sessions / 1 life, "
        f"no /proc wall; {gt}", "Spec", ALL, AP, cap=600, view=True, MaxSid="2", MaxLife="1",
        WallInit="{FALSE}", **gk)

# ---- g6: decision-0929c hook identity (../Phase5Hook.tla; SRD rev 17 SR-22.9, SR-3.6, SR-6.1) ----
BASE5 = dict(MaxClock="7", MaxLife="2", MaxFaults="1", PidPool="3", Gate='"parent"', AdoptBy='"adpane"',
             KillAll="TRUE", AllowNested="TRUE", AllowTeammate="TRUE", AllowStray="TRUE",
             AllowLeftover="TRUE", AllowRotate="TRUE", AllowHup="TRUE", AllowLostReply="TRUE",
             AllowNullStart="FALSE", AllowCrash="TRUE", Probes="FALSE", FMNoPaneGone="TRUE",
             HookWait="TRUE", AllowSlowIdentity="FALSE")
INV5 = "RowOnlyByAgent KillAllGone ClockBound"


def cfg5(group, name, what, spec, inv="", prop="", expect="pass", cap=1800, **kv):
    c = dict(BASE5)
    for k, v in kv.items():
        if k not in c:
            raise SystemExit("unknown constant " + k)
        c[k] = v
    with open(os.path.join(CFG, name + ".cfg"), "w") as f:
        f.write(f"\\* {name}: {what}\nSPECIFICATION {spec}\nCONSTANTS\n")
        for k, v in c.items():
            f.write(f"  {k} = {v}\n")
        if inv:
            f.write("INVARIANT " + inv + "\n")
        if prop:
            f.write("PROPERTY " + prop + "\n")
        f.write("CHECK_DEADLOCK FALSE\n")
    rows.append((group, "Phase5Hook", name, str(cap), expect, what))


cfg5("g6", "ci_H_S", "hook gate by parent pid+start, nested + teammates + leftover + stray + /clear + lost reply "
     "+ SIGHUP survivors + pid reuse", "Spec", INV5, MaxClock="6")
cfg5("g6", "ci_H_Sns", "as ci_H_S with an unreadable pane start time (pid alone decides), no pid reuse (unbounded processes: MaxClock 4)", "Spec",
     INV5, cap=600, AllowNullStart="TRUE", PidPool="0", MaxClock="4")
cfg5("g6", "ci_H_Rns", "residual: unreadable start time + pid reuse (the agent dies before its first hook and "
     "another Claude gets its pid)", "Spec", INV5, "", "violation", AllowNullStart="TRUE")
cfg5("g6", "ci_H_L", "PendingResolves, find-missing reads a gone @ad_pane pane as Gone", "FairSpec", "",
     "PendingResolves", MaxClock="5")
cfg5("g6", "ci_H_Lsrd", "PendingResolves as SRD rev 17 (no pane recorded + labelled session kept by a teammate)",
     "FairSpec", "", "PendingResolves", "violation", MaxClock="5", FMNoPaneGone="FALSE")
# decision-0930b Q1: the identity write after the create; the SessionStart hook waits for it
cfg5("g6", "ci_H_Lrep_nowait", "control: SessionStart does not wait for the identity write (ReportInResolves)",
     "FairSpec", "", "ReportInResolves", "violation", MaxClock="5", HookWait="FALSE")
cfg5("g6", "ci_H_Lrep", "ReportInResolves: SessionStart waits (bounded by the grace period) for the identity write",
     "FairSpec", "", "ReportInResolves", MaxClock="5")
cfg5("g6", "ci_H_Lrep_slow", "residual (ii): the identity write comes after the grace period (late not excused)",
     "FairSpec", "", "ReportInResolvesT", "violation", MaxClock="5", AllowSlowIdentity="TRUE")
cfg5("g6", "ci_H_Ctop", "control: gate by the topmost ancestor carrying the id (the old walk)", "Spec", INV5, "",
     "violation", Gate='"topmost"')
cfg5("g6", "ci_H_Cpane", "control: gate by the hook's pane only", "Spec", INV5, "", "violation", Gate='"pane"')
cfg5("g6", "ci_H_Ckill", "control: kill waits for the agent process only", "Spec", INV5, "", "violation",
     KillAll="FALSE")
cfg5("g6", "ci_H_Cadopt", "control: a lost reply adopts any pane of the labelled session", "Spec", INV5, "",
     "violation", AdoptBy='"anypane"')
for pr, kv in (("NestedIgnored", {}), ("RotationApplied", {}), ("Adopted", {}), ("KillFailed", {}),
               ("ChildOutlives", {}), ("TeammateLive", {}), ("PidReused", {}),
               ("NullStart", dict(AllowNullStart="TRUE")), ("WaitedApplied", {}), ("WaitedIgnored", {})):
    cfg5("g6", f"ci_H_V_{pr}", f"probe: {pr} reachable", "Spec", "Not" + pr, "", "violation",
         Probes="TRUE", **kv)


# ---- tiers (measured times from CI run 3 and t7 in brackets) ----
FAST_PASS = {
    "ci_L5o",          # PendingResolves, orchestrator, all accidents [2 min]
    "ci_L5b_lab",      # PendingResolves, bot, failed label step [3 min]
    "ci_L1",           # StuckHeals [12 s]
    "ci_S1o_ren", "ci_S1o_grp", "ci_S1o_lab", "ci_S1o_rst", "ci_S1o_reb", "ci_S1o_D",
    "ci_S3o",          # orchestrator safety, every accident, and shared names [11 min in all]
    "ci_S1b_lab",      # bot + human kill safety [4 min]
    "ci_S1h_lab",      # bot + human resume safety [7 min]
    "ci_G3b_lab",      # kill --include-finished with a leftover [9 min]
    "ci_S_store",      # another store's agent [1 min]
    "ci_smoke_win", "ci_smoke_lab", "ci_smoke_srv",   # unsplit cross-check [1 min]
    "ci_H_Sns",        # hook gate: RowOnlyByAgent, KillAllGone [3 min]
    "ci_H_Lrep",       # hook gate: ReportInResolves (decision-0930b Q1) [21 min]
}
unknown = FAST_PASS - {r[2] for r in rows}
if unknown:
    raise SystemExit("FAST_PASS names unknown runs: " + " ".join(sorted(unknown)))
with open(os.path.join(HERE, "suite.tsv"), "w") as f:
    f.write("# group\tspec\tcfg\tcap_s\texpect\ttier\twhat\n")
    for g, mod, name, cap, expect, what in rows:
        tier = "fast" if expect == "violation" or name in FAST_PASS else "full"
        f.write("\t".join((g, mod, name, cap, expect, tier, what)) + "\n")
print(len(rows), "runs")
