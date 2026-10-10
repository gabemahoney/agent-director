#!/usr/bin/env python3
"""Writes cfg/<run>.cfg and suite.tsv for Hold36h.tla from the RUNS table below."""
import os

HERE = os.path.dirname(os.path.abspath(__file__))
ALL = '{"HookDeath", "BothEndsDie", "SlowIdle", "Claim"}'
LIVE = '{"SlowIdle", "HookDeath", "Claim"}'
SAFE = ["TypeOK", "WorkingTrue", "NoStaleHold"]

# run, turns, subagent, design, write, env, probe, invariants, properties, expected, what
RUNS = [
    ("red_split", 1, True, "fixcut", "split", "{}", "", ["WorkingTrue"], [],
     "FAIL WorkingTrue",
     "control, today's code: a subagent's working write read before the main agent's request "
     "was recorded overwrites its check_permission"),
    ("red_split_all", 2, True, "fixcut", "split", ALL, "", SAFE, [],
     "FAIL WorkingTrue",
     "control, today's code, every switch, two turns: the same violation"),
    ("ctl_split_nosub", 2, False, "fixcut", "split", ALL, "", SAFE, [],
     "PASS",
     "control: today's code with the main agent alone (its hooks run one at a time) is safe, "
     "every switch: the race needs two agents"),
    ("cond_safe", 2, True, "fixcut", "cond", ALL, "", SAFE, [],
     "PASS",
     "the fix: safety, subagent, every switch"),
    ("cond_safe3", 3, True, "fixcut", "cond", ALL, "", SAFE, [],
     "PASS",
     "the fix: safety, three turns, every switch"),
    ("cond_live1", 1, True, "fixcut", "cond", LIVE, "", SAFE, ["RowSettles"],
     "PASS",
     "the fix: every property, one turn (the local sanity run)"),
    ("cond_live", 2, True, "fixcut", "cond", '{"HookDeath", "Claim"}', "", SAFE, ["RowSettles"],
     "PASS",
     "the fix: every property, subagent, hook deaths, record-pane-answer used"),
    ("cond_live_slow", 2, True, "fixcut", "cond", LIVE, "", SAFE, ["RowSettles"],
     "PASS",
     "the fix: every property, slow idle hook"),
    ("cond_live3", 3, True, "fixcut", "cond", LIVE, "", SAFE, ["RowSettles"],
     "PASS",
     "the fix: every property, three turns"),
    ("red_today_stale", 2, False, "today", "cond", "{}", "", ["NoStaleHold"], [],
     "FAIL NoStaleHold",
     "red for NoStaleHold: b.omt's proof rule off (today), with the fix's write"),
    ("red_today_settle", 1, False, "today", "cond", "{}", "", [], ["RowSettles"],
     "FAIL RowSettles",
     "red for RowSettles: b.omt's proof rule off (today), with the fix's write"),
    ("lim_cond_sub_noclaim", 1, True, "fixcut", "cond", "{}", "", [], ["RowSettles"],
     "FAIL RowSettles",
     "b.omt's accepted residual, unchanged by the fix: a subagent's request denied at the pane "
     "holds until record-pane-answer or the row's end"),
    ("p_race", 2, True, "fixcut", "cond", "{}", "race", ["NeverHit"], [],
     "FAIL NeverHit",
     "probe: a working write read 'not held' and a request holds at its write (the fix reports it held)"),
    ("p_mainrace", 2, True, "fixcut", "cond", "{}", "mainrace", ["NeverHit"], [],
     "FAIL NeverHit",
     "probe: the same race with the main agent's write and the subagent's request"),
    ("p_txrace", 2, True, "fixcut", "cond", "{}", "txrace", ["NeverHit"], [],
     "FAIL NeverHit",
     "probe: the race through the lock: the read ran inside the open transaction, the write "
     "waited for its commit"),
    ("p_staleheld", 2, True, "fixcut", "cond", "{}", "staleheld", ["NeverHit"], [],
     "FAIL NeverHit",
     "probe: the opposite window: a read 'held' whose request no longer holds at the write "
     "(unchanged by the fix: find-missing repairs it)"),
]


def main():
    os.makedirs(os.path.join(HERE, "cfg"), exist_ok=True)
    rows = ["run\texpected\twhat"]
    for (run, turns, subagent, design, write, env, probe, invs, props, expected, what) in RUNS:
        lines = [
            f"\\* {run}: {what}. Expected: {expected}",
            "SPECIFICATION Spec",
            "CONSTANTS",
            f"  MaxTurns = {turns}",
            f"  Subagent = {'TRUE' if subagent else 'FALSE'}",
            f'  Design = "{design}"',
            f'  Write = "{write}"',
            f"  Env = {env}",
            f'  Probe = "{probe}"',
            "CHECK_DEADLOCK FALSE",
        ]
        lines += [f"INVARIANT {i}" for i in invs]
        lines += [f"PROPERTY {p}" for p in props]
        with open(os.path.join(HERE, "cfg", run + ".cfg"), "w") as f:
            f.write("\n".join(lines) + "\n")
        rows.append(f"{run}\t{expected}\t{what}")
    with open(os.path.join(HERE, "suite.tsv"), "w") as f:
        f.write("\n".join(rows) + "\n")


if __name__ == "__main__":
    main()
