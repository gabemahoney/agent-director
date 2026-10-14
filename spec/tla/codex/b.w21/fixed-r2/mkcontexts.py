#!/usr/bin/env python3
"""Prepare a separate model correction round by file operations only."""

import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess


HERE = Path(__file__).resolve().parent
FIXED = HERE.parent / "fixed"
CODEX = HERE.parents[1]
TLA = CODEX.parent
ROOT = CODEX.parents[2]
SOURCE_COMMIT = "9d025f8f2caf176f3a60421f52ba94b293021bdb"
MODEL = "B146R5Cx"
NEW_PREDICATE = "        ELSE JudgedAlive(r)\n"
OLD_PREDICATE = ('        ELSE CASE LiveCheck = "exact" -> HookAlive(r)\n'
                 '               [] OTHER -> ~ack[r] /\\ ph[r] < 2\n')
REPAIRS = ("PreV7Old", "PreV7ReadRepair", "PreV7CloseRepair")
CASES = (
    ("w21_r2_initial", "w21_fixed_initial", "time", "corrected", "PASS",
     "Full Spec rerun of the failed mixed-store job, retaining all submitted safety/liveness checks."),
    ("w21_r2_modern_full_control", "w21_fixed_modern_full_control", "time", "corrected", "PASS",
     "Full original modern-only Spec with historical time liveness abstraction; no legacy seed."),
    ("w21_r2_mixed_acked_live_time", None, "time", "corrected", "PASS",
     "Real lifecycle/timeout-ack/bypass actions retain NoRace and the current acknowledged live hook hold."),
    ("w21_r2_mixed_acked_live_exact", None, "exact", "corrected", "PASS",
     "The same focused mixed-store safety check with exact process liveness."),
    ("w21_r2_mixed_acked_live_probe", None, "time", "corrected",
     "FAIL(Invariant_B21MixedAckedLiveUnreachable_is_violated)",
     "Required nonvacuity counterexample reaches released legacy plus current denied/acked/live hook at phase 1."),
    ("w21_r2_mutant_ack_shortcut", None, "time", "ack-shortcut-mutant",
     "FAIL(Invariant_NoRace_is_violated)",
     "Only the old modern RelayHolds predicate is restored; the original race detector must reject the bypass send."),
)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def git_bytes(path):
    return subprocess.check_output(["git", "show", f"{SOURCE_COMMIT}:{path}"], cwd=ROOT)


def runtime_identity():
    """Bind every tracked production Go file, not just the repaired guard."""
    names = subprocess.check_output(["git", "ls-tree", "-r", "--name-only", SOURCE_COMMIT],
                                    cwd=ROOT).decode().splitlines()
    production = lambda name: (name.endswith(".go") and not name.endswith("_test.go")
                               and name.startswith(("cmd/", "internal/", "pkg/")))
    names = sorted(name for name in names if production(name))
    present = subprocess.check_output(["git", "ls-files", "--cached", "--others", "--exclude-standard",
                                       "--", "cmd", "internal", "pkg"], cwd=ROOT).decode().splitlines()
    assert sorted(name for name in present if production(name)) == names, "Production Go inventory changed"
    frozen = hashlib.sha256()
    current = hashlib.sha256()
    for name in names:
        prefix = name.encode() + b"\0"
        frozen.update(prefix + git_bytes(name))
        current.update(prefix + (ROOT / name).read_bytes())
    assert current.hexdigest() == frozen.hexdigest(), "Production Go source changed since submitted runtime"
    return {"commit": SOURCE_COMMIT, "production_go_files": len(names),
            "production_go_sha256": frozen.hexdigest(),
            "hash_encoding": "sorted git paths in cmd/internal/pkg ending .go excluding _test.go; path + NUL + bytes",
            "guard_sha256": sha(git_bytes("pkg/api/sendkeys_relay.go")),
            "delivery_sha256": sha(git_bytes("pkg/api/relay_delivery.go"))}


def model_text(variant):
    source = (CODEX / f"src/{MODEL}.tla").read_text()
    assert source.count(NEW_PREDICATE) == 1
    return source if variant == "corrected" else source.replace(NEW_PREDICATE, OLD_PREDICATE)


def cfg_text(row):
    name, original, livecheck, variant, expected, purpose = row
    header = f"\\* b.w21 r2 expected={expected}; observed=NOT_RUN.\n\\* {purpose}\n"
    if original:
        cfg = (FIXED / "cfg" / f"{original}.cfg").read_text()
        return header + "\n".join(line for line in cfg.splitlines()
                                   if not line.startswith("\\*")) + "\n"
    cfg = header + f'''SPECIFICATION B21MixedSpec
CONSTANTS
  N = 1
  NI = 1
  DVerdicts = {{"allow"}}
  Off = {{}}
  Env = {{"Stray"}}
  LiveCheck = "{livecheck}"
  Pick = "token"
  ReaderWrites = "try"
  MaxGrabs = 0
  MaxWait = 2
  AckWait = 1
  Sigma = 0
  Phi = 0
  CallerDeadline = 3
  Probe = "none"
  R5 = {{"Unproven", "Bypass"}}
  CxR5 <- KnobOn
'''
    cfg += "".join(f"  {knob} <- KnobOn\n" for knob in REPAIRS)
    cfg += "CHECK_DEADLOCK FALSE\nINVARIANT TypeOK\nINVARIANT B21SeedFidelity\nINVARIANT B21ProofFidelity\n"
    if name.endswith("probe"):
        cfg += "INVARIANT B21MixedAckedLiveUnreachable\n"
    else:
        cfg += "INVARIANT NoRace\n"
        if variant == "corrected":
            cfg += "INVARIANT B21MixedAckedLiveHeld\n"
    return cfg


def command(context, name):
    return ("/home/horde/projects/horde_admin/job-scheduler/bin/jobsched submit "
            f"--context '{context.resolve()}' --name b-w21-{name.removeprefix('w21_').replace('_', '-')} "
            "--channel C0B3X876XSB --network off --memory 4g --cpus 4 --timeout 15m")


def main():
    for name in ("manifest.json", "scheduler-commands.txt", "cfg", "jobs"):
        path = HERE / name
        if path.exists() or path.is_symlink():
            raise SystemExit(f"Refusing to overwrite r2 artifacts: {path}")
    runtime = runtime_identity()
    receipt = Path("/home/horde/projects/agent-director-project/tickets/Bugs/b.w21/fixed-model-results.txt")
    trace = Path("/home/horde/.job-scheduler/jobs/j20261014225806-d7dc0c/run.log")
    source = (CODEX / f"src/{MODEL}.tla").read_bytes()
    inputs = {"tla2tools.jar": TLA / "tla2tools.jar", "tlcjob.sh": TLA / "jobs/lib/tlcjob.sh"}
    for path in inputs.values():
        assert path.is_file() and not path.is_symlink(), path
    manifest = {
        "bug": "b.w21", "round": "fixed-r2", "observed": "NOT_RUN",
        "rules": "model-modern-liveness-correction-with-ack-shortcut-mutant",
        "source_path": "spec/tla/codex/src/B146R5Cx.tla", "source_sha256": sha(source),
        "submitted_source": {"commit": SOURCE_COMMIT,
                             "model_sha256": sha(git_bytes("spec/tla/codex/src/B146R5Cx.tla")),
                             "manifest_sha256": sha(git_bytes("spec/tla/codex/b.w21/fixed/manifest.json"))},
        "runtime_identity": runtime,
        "diagnosis": {
            "layer": "model", "runtime_changed": False,
            "failed_job": "j20261014225806-d7dc0c",
            "failed_case": "w21_fixed_initial", "expected": "PASS",
            "observed": "FAIL(Invariant_NoRace_is_violated)",
            "distinct_states": 3835, "depth": 11,
            "receipt_source": str(receipt), "receipt_sha256": sha(receipt.read_bytes()),
            "trace_source": str(trace), "trace_sha256": sha(trace.read_bytes()),
            "cause": "Mixed-store modern RelayHolds time branch gated liveness on ~ack, unlike original JudgedAlive and runtime known-alive OR not_confirmed.",
            "correction": "Use JudgedAlive for modern rows; preserve legacy delivery/logical closure and original time abstraction.",
            "abstraction_limit": "Historical time branch conservatively holds recorded modern hooks until phase 2 even after ack; it does not separately represent runtime acked/cannot-check release. Exact checks and public API controls cover concrete identity outcomes.",
            "protected": ["NoRace", "race assignments", "KnownHookAlive", "HookAlive", "original actions", "LiveCheck time in submitted full Spec"]},
        "affected_case_selection": {
            "rerun": ["w21_fixed_initial", "w21_fixed_modern_full_control"],
            "not_rerun": "Eleven submitted B21Spec cases have no modern Ask/HX transitions. The twelfth omitted case, w21_mutant_close_only, uses full Spec but fails RelayHoldOnlyCurrentLaunch in Init, before any transition exercises the changed modern RelayHolds branch. Their prior frozen-source receipts remain historical evidence; r2 does not claim they ran against new bytes.",
            "new_checks": "time/exact focused mixed-store safety, time reachability probe, old-predicate mutant"},
        "cases": [],
    }
    (HERE / "cfg").mkdir()
    (HERE / "jobs").mkdir()
    commands = []
    for row in CASES:
        name, original, livecheck, variant, expected, purpose = row
        context = HERE / "jobs" / name
        context.mkdir()
        (context / f"{MODEL}.tla").write_text(model_text(variant))
        cfg = cfg_text(row)
        (HERE / "cfg" / f"{name}.cfg").write_text(cfg)
        (context / f"{name}.cfg").write_text(cfg)
        for filename, path in inputs.items():
            shutil.copyfile(path, context / filename)
        (context / "tlcjob.sh").chmod(0o755)
        (context / "runs").write_text(f"# b.w21 r2 expected={expected}; observed=NOT_RUN\n{MODEL} {name} @700\n")
        (context / "Dockerfile").write_text(
            "# Scheduler only; user submits.\n" + f"# Expected {expected}; observed NOT_RUN.\n"
            "FROM eclipse-temurin:21-jre\nWORKDIR /work\nCOPY . /work/\n"
            "ENV XMX=2500m DIRECT=256m BUDGET=750 DISKCAP_MB=1500\n"
            'CMD ["/work/tlcjob.sh"]\n')
        manifest["cases"].append({
            "name": name, "context": str(context.relative_to(HERE)), "variant": variant,
            "spec": "Spec" if original else "B21MixedSpec", "origin_case": original,
            "livecheck": livecheck, "expected": expected, "observed": "NOT_RUN", "purpose": purpose,
            "enabled_knobs": re.findall(r"^\s*(PreV7\w+)\s*<-\s*KnobOn", cfg, re.M),
            "sha256": {p.name: sha(p.read_bytes()) for p in sorted(context.iterdir())}})
        commands.append(command(context, name))
    (HERE / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    (HERE / "scheduler-commands.txt").write_text("\n".join(commands) + "\n")
    print(f"Prepared {len(CASES)} independent r2 contexts; observations NOT_RUN; runtime unchanged; no model execution.")


if __name__ == "__main__":
    main()
