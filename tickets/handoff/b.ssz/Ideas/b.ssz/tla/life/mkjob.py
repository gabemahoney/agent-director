#!/usr/bin/env python3
"""b.ssz Epic 5: builds job-lr/, one scheduler job holding every LifeRule run.

It copies the unchanged tlcjob.sh and tla2tools.jar from agent-director-bssz/spec/tla,
the spec from src/, and writes one cfg per run plus the runs file. It runs no TLC.
"""
import pathlib
import shutil

base = pathlib.Path(__file__).resolve().parent
import sys
job = base / (sys.argv[1] if len(sys.argv) > 1 else "job-lr")
job.mkdir(parents=True, exist_ok=True)
lib = pathlib.Path("/home/horde/projects/agent-director-project/agent-director-bssz/spec/tla")
shutil.copy(lib / "jobs/lib/tlcjob.sh", job / "tlcjob.sh")
shutil.copy(lib / "tla2tools.jar", job / "tla2tools.jar")
shutil.copy(base / "src/LifeRule.tla", job / "LifeRule.tla")

knobs = ["LifePerLaunch", "NeverNewLife", "NoLifeFilter", "NoClose", "ChildFollowsLife", "NoFolderCheck", "SwitchBackReturns", "HistoryFallback", "ShortFormsPass", "SamePickPass"]
inv_all = "TypeOK SubsTruth UntrackedEmpty NoStaleRunning HistTruth ResumeOpens SwitchForgets KeyCaseNoOldSession NeverOlder KidsShown"
prop_all = "LifeIffAgent CarryInLife KidsUntouched ResumeLatest"
runs = [
    ("lr_S", "PASS", None, inv_all, prop_all, "the design: every never-false invariant and the three rule properties"),
    ("lr_C_lifeperlaunch", "FAIL LifeIffAgent", "LifePerLaunch", None, "LifeIffAgent", "control: every reuse starts a new life (B today)"),
    ("lr_C_nevernew", "FAIL HistTruth", "NeverNewLife", "HistTruth", None, "control: an agent switch keeps the life"),
    ("lr_C_nofilter", "FAIL UntrackedEmpty", "NoLifeFilter", "UntrackedEmpty", None, "control: records shown across lives (b.xjn as merged)"),
    ("lr_C_noclose", "FAIL NoStaleRunning", "NoClose", "NoStaleRunning", None, "control: no write closes a running record"),
    ("lr_C_fallback", "FAIL ResumeLatest", "HistoryFallback", None, "ResumeLatest", "control: B's history fallback resumes an older session"),
    ("lr_C_fallback_old", "FAIL NeverOlder", "HistoryFallback", "NeverOlder", None, "control: B's fallback reaches a session from before a same-agent reuse"),
    ("lr_C_nofolder", "FAIL ResumeOpens", "HistoryFallback+NoFolderCheck", "ResumeOpens", None, "control: with B's fallback and no folder check, resume opens a session from another folder"),
    ("lr_lim_b_nofolder", "PASS", "LifePerLaunch+HistoryFallback+NoFolderCheck", "ResumeOpens", None, "limit: B today (fallback, a life per reuse) needs no folder check"),
    ("lr_C_across", "FAIL SwitchForgets", "ShortFormsPass", "SwitchForgets", None, "control: a reuse that switches the agent resumes a session from before the switch (b.d6q)"),
    ("lr_C_keycase", "FAIL KeyCaseNoOldSession", "ShortFormsPass", "KeyCaseNoOldSession", None, "control: Claude, Codex, then Claude with a short-form resume picks up the old Claude session (b.d6q)"),
    ("lr_C_samepick", "FAIL ResumeLatest", "SamePickPass", None, "ResumeLatest", "control: a same-agent reuse or a replayed argument picks an older session (b.d6q)"),
    ("lr_C_switchback", "FAIL SwitchForgets", "SwitchBackReturns", "SwitchForgets", None, "control: a switch back returns to the agent's old life"),
    ("lr_C_childlife", "FAIL KidsShown", "ChildFollowsLife", "KidsShown", None, "control: list --parent filtered by the parent's life"),
    ("lr_V_hidden", "FAIL NotHidden", None, "NotHidden", None, "probe: Claude records kept but hidden on a Codex row"),
    ("lr_V_carried", "FAIL NotCarried", None, "NotCarried", None, "probe: a record from before a same-agent reuse is shown after it"),
    ("lr_V_kid", "FAIL NotKidAfterSwitch", None, "NotKidAfterSwitch", None, "probe: a child from before a switch is listed after it"),
    ("lr_V_hist", "FAIL NotHistCarried", None, "NotHistCarried", None, "probe: a session archived by a same-agent reuse is offered after it"),
    ("lr_V_keycase", "FAIL NotKeyCase", None, "NotKeyCase", None, "probe: Claude, then Codex, then Claude again, the first Claude session still stored"),
    ("lr_V_switchback", "FAIL NotSwitchedBack", None, "NotSwitchedBack", None, "probe: a switch back to an agent with earlier sessions in the store"),
    ("lr_V_resumed", "FAIL NotResumed", None, "NotResumed", None, "probe: a resume happens"),
]

lines = ["# b.ssz Epic 5 life rule: one job, one small run per line (expected verdicts in each cfg's first line)."]
for name, exp, knob, inv, prop, what in runs:
    c = [
        "\\* %s: %s. Expected: %s" % (name, what, exp),
        "SPECIFICATION Spec",
        "CONSTANTS",
        '  Sids = {"s1", "s2"}',
        '  Kids = {"k1"}',
        "  MaxLaunch = 5",
        "  MaxSess = 4",
        '  Locs = {"L1", "L2"}',
    ]
    on = (knob or "").split("+")
    for k in knobs:
        c.append("  %s = %s" % (k, "TRUE" if k in on else "FALSE"))
    if inv:
        c.append("INVARIANT " + inv)
    if prop:
        c.append("PROPERTY " + prop)
    c.append("CHECK_DEADLOCK FALSE")
    (job / (name + ".cfg")).write_text("\n".join(c) + "\n")
    lines.append("LifeRule %s @300" % name)
(job / "runs").write_text("\n".join(lines) + "\n")

(job / "Dockerfile").write_text(
    "# b.ssz Epic 5: LifeRule.tla (tickets/Ideas/b.ssz/tla/life/src), for the job scheduler only\n"
    "# (submit with --timeout 1h --memory 8g). The run script is tlcjob.sh (spec/tla/jobs/lib/tlcjob.sh,\n"
    "# unchanged); it reads /work/runs and its LAST line is the job's one-line verdict.\n"
    "FROM eclipse-temurin:21-jre\n"
    "WORKDIR /work\n"
    "COPY . /work/\n"
    "ENV XMX=4g DIRECT=1g BUDGET=3000 DISKCAP_MB=8000\n"
    "RUN chmod 0755 /work/tlcjob.sh && java -version\n"
    'CMD ["/work/tlcjob.sh"]\n'
)
print(sorted(p.name for p in job.iterdir()))
