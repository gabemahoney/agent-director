#!/usr/bin/env python3
"""Generate b.w21 scheduler contexts using file operations only."""

import hashlib
import json
from pathlib import Path
import shutil


HERE = Path(__file__).resolve().parent
CODEX = HERE.parent
TLA = CODEX.parent
MODEL = "B146R5Cx"
KNOBS = (
    "PreV7Old",
    "PreV7Current",
    "PreV7Relaunch",
    "PreV7Expired",
    "PreV7CloseDeny",
)

# Expected outcomes are predictions. No model or container is executed here.
CASES = (
    ("w21_initial_red", "Spec", ("PreV7Old",),
     ("RelayHoldOnlyCurrentLaunch",), (), "none", False,
     "FAIL(Invariant_RelayHoldOnlyCurrentLaunch_is_violated)",
     "Initial migrated row: pending before SessionStart, old request proven gone, within window. Legacy settlement checks hold release, not stored closure."),
    ("w21_relaunch_red", "B21Spec", ("PreV7Old", "PreV7Current", "PreV7Relaunch"),
     ("RelayHoldOnlyCurrentLaunch",), (), "none", True,
     "FAIL(Invariant_RelayHoldOnlyCurrentLaunch_is_violated)",
     "Shortest expected trace: Init -> AgentCrash -> FMMissing -> Relaunch; request remains unclosed."),
    ("w21_relaunch_probe", "B21Spec", ("PreV7Old", "PreV7Current", "PreV7Relaunch"),
     ("B21RelaunchPendingUnreachable",), (), "none", True,
     "FAIL(Invariant_B21RelaunchPendingUnreachable_is_violated)",
     "Independent reachability probe: Init -> AgentCrash -> FMMissing -> Relaunch reaches pending before SessionStart. Retain expected failure after repair."),
    ("w21_expired_deny_red", "B21Spec", ("PreV7Old", "PreV7Expired", "PreV7CloseDeny"),
     ("RelayHoldOnlyCurrentLaunch", "OldRequestsDoNotHoldPlainKeys"), (), "none", False,
     "FAIL(Invariant_OldRequestsDoNotHoldPlainKeys_is_violated)",
     "Initial expired CloseDenied seed: relay hold ends but open fallen_back still blocks keys."),
    ("w21_expired_deny_live_red", "B21Spec", ("PreV7Old", "PreV7Expired", "PreV7CloseDeny"),
     ("RelayHoldOnlyCurrentLaunch",), ("B21KeysEventuallyAccepted",), "none", False,
     "FAIL(Temporal_properties_were_violated)",
     "Fair pane claims cannot update a decided pre-v7 row; plain keys remain refused indefinitely."),
    ("w21_pane_write_probe", "B21Spec", ("PreV7Old", "PreV7Expired", "PreV7CloseDeny"),
     ("RelayHoldOnlyCurrentLaunch", "NeverHit"), (), "legacyclaimblocked", False,
     "FAIL(Invariant_NeverHit_is_violated)",
     "Reachability probe: after MarkGone/Age, OrcAct reaches the no-matching-row pane write."),
    ("w21_expired_ordinary_control", "B21Spec", ("PreV7Old", "PreV7Expired"),
     ("RelayHoldOnlyCurrentLaunch", "OldRequestsDoNotHoldPlainKeys", "B21ExpiredOrdinaryDelivered"),
     ("B21KeysEventuallyAccepted",), "none", False, "PASS",
     "Ordinary decided legacy request derives delivered after expiry; pending keys can proceed."),
    ("w21_current_launch_control", "B21Spec", ("PreV7Old", "PreV7Current"),
     ("RelayHoldOnlyCurrentLaunch", "B21CurrentHoldExists", "B21ExpiredOrdinaryDelivered"),
     (), "none", False, "PASS",
     "Current-launch legacy request can still hold before expiry. R5={} isolates rule 1; no relaunch."),
    ("w21_no_legacy_control", "B21Spec", (),
     ("RelayHoldOnlyCurrentLaunch", "OldRequestsDoNotHoldPlainKeys"),
     ("B21KeysEventuallyAccepted",), "none", False, "PASS",
     "All b.w21 knobs off, no permission request exists; focused pending keys proceed."),
)


def cfg_text(case):
    name, spec, enabled, invariants, properties, probe, crash, expected, purpose = case
    if spec == "Spec":
        # Retain nonlegacy closure; legacy settlement allows read-time release.
        text = (CODEX / "src/cfg/r5x_time.cfg").read_text()
        if text.count("PROPERTY RecordsClose\n") != 1:
            raise ValueError("Expected one RecordsClose property in r5x_time.cfg")
        text = text.replace("PROPERTY RecordsClose\n", "PROPERTY B21RecordsSettle\n")
        text += "\n\\* b.w21 expected initial-state counterexample; scheduler observation pending.\n"
        text += "CONSTANTS\n  PreV7Old <- KnobOn\n"
        text += "INVARIANT RelayHoldOnlyCurrentLaunch\n"
        return text
    env = '{"Stray", "Crash"}' if crash else '{"Stray"}'
    r5 = "{}" if name == "w21_current_launch_control" else '{"Unproven", "Bypass"}'
    text = f"""\\* b.w21 CURRENT-rule scenario. Expected: {expected}; observed: NOT_RUN.
\\* {purpose}
SPECIFICATION {spec}
CONSTANTS
  N = 1
  NI = 1
  DVerdicts = {{"allow"}}
  Off = {{}}
  Env = {env}
  LiveCheck = "exact"
  Pick = "token"
  ReaderWrites = "try"
  MaxGrabs = 0
  MaxWait = 2
  AckWait = 1
  Sigma = 0
  Phi = 0
  CallerDeadline = 3
  Probe = "{probe}"
  R5 = {r5}
  CxR5 <- KnobOn
"""
    text += "".join(f"  {knob} <- KnobOn\n" for knob in enabled)
    text += "CHECK_DEADLOCK FALSE\nINVARIANT TypeOK\nINVARIANT B21SeedFidelity\n"
    text += "".join(f"INVARIANT {check}\n" for check in invariants)
    text += "".join(f"PROPERTY {check}\n" for check in properties)
    return text


def sha256(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    cfg_dir = HERE / "cfg"
    jobs_dir = HERE / "jobs"
    cfg_dir.mkdir(exist_ok=True)
    jobs_dir.mkdir(exist_ok=True)
    sources = {
        f"{MODEL}.tla": CODEX / f"src/{MODEL}.tla",
        "tla2tools.jar": TLA / "tla2tools.jar",
        "tlcjob.sh": TLA / "jobs/lib/tlcjob.sh",
    }
    manifest = {"bug": "b.w21", "rules": "current-unfixed", "observed": "NOT_RUN", "cases": []}
    for case in CASES:
        name, spec, enabled, invariants, properties, probe, crash, expected, purpose = case
        cfg = cfg_dir / f"{name}.cfg"
        cfg.write_text(cfg_text(case))
        context = jobs_dir / name
        context.mkdir(exist_ok=True)
        for filename, source in sources.items():
            destination = context / filename
            if destination.is_symlink():
                raise ValueError(f"Refusing symlink: {destination}")
            shutil.copyfile(source, destination)
        shutil.copyfile(cfg, context / cfg.name)
        (context / "tlcjob.sh").chmod(0o755)
        (context / "runs").write_text(
            f"# b.w21 expected={expected}; observed=NOT_RUN\n{MODEL} {name} @700\n"
        )
        (context / "Dockerfile").write_text(
            "# b.w21: scheduler only. Never build or run on the development host.\n"
            f"# Expected {expected}; no observed result yet.\n"
            "FROM eclipse-temurin:21-jre\nWORKDIR /work\nCOPY . /work/\n"
            "ENV XMX=2500m DIRECT=256m BUDGET=750 DISKCAP_MB=1500\n"
            'CMD ["/work/tlcjob.sh"]\n'
        )
        hashes = {p.name: sha256(p) for p in sorted(context.iterdir())}
        manifest["cases"].append({
            "name": name, "context": str(context.relative_to(HERE)), "spec": spec,
            "enabled_knobs": list(enabled), "expected": expected, "observed": "NOT_RUN",
            "purpose": purpose, "sha256": hashes,
        })
    (HERE / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    commands = []
    for case in CASES:
        name = case[0]
        context = (jobs_dir / name).resolve()
        commands.append(
            "/home/horde/projects/horde_admin/job-scheduler/bin/jobsched submit "
            f"--context '{context}' --name b-w21-{name.removeprefix('w21_').replace('_', '-')} "
            "--channel C0B3X876XSB --network off --memory 4g --cpus 4 --timeout 15m"
        )
    (HERE / "scheduler-commands.txt").write_text("\n".join(commands) + "\n")
    print(f"Generated {len(CASES)} self-contained contexts; no model or scheduler execution.")


if __name__ == "__main__":
    main()
