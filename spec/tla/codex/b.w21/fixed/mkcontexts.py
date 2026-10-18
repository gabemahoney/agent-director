#!/usr/bin/env python3
"""Prepare separate fixed-model contexts; file operations only, never TLC."""

import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import sys


sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent
RED = HERE.parent
CODEX = RED.parent
TLA = CODEX.parent
ROOT = CODEX.parents[2]
MODEL = "B146R5Cx"
module_spec = importlib.util.spec_from_file_location("frozen_generator", RED / "mkcontexts.py")
baseline = importlib.util.module_from_spec(module_spec)
module_spec.loader.exec_module(baseline)
ORIGINAL = {case[0]: case for case in baseline.CASES}
REPAIRS = ("PreV7ReadRepair", "PreV7CloseRepair")


def case(name, original, expected="PASS", extra=(), repairs=REPAIRS,
         checks=(), properties=(), purpose=None):
    return {
        "name": name, "original": original, "expected": expected,
        "extra_knobs": extra, "repairs": repairs, "extra_checks": checks,
        "extra_properties": properties, "purpose": purpose,
    }


# Predictions only. Every observed verdict remains NOT_RUN until the user
# submits these exact self-contained contexts and returns scheduler receipts.
CASES = (
    case("w21_fixed_initial", "w21_initial_red", checks=("B21ProofFidelity",),
         purpose="Full Spec: runtime read-time release of an already-stuck migrated row; original safety and liveness obligations including B21RecordsSettle."),
    case("w21_fixed_relaunch", "w21_relaunch_red", checks=("B21ClosePersists",),
         purpose="Crash, find-missing close, resume: durable closure survives pending before SessionStart."),
    case("w21_fixed_relaunch_probe", "w21_relaunch_probe",
         expected="FAIL(Invariant_B21RelaunchPendingUnreachable_is_violated)",
         purpose="Independent reachability probe still reaches resumed pending after repair; failure is required."),
    case("w21_fixed_expired_deny", "w21_expired_deny_red",
         purpose="Already-stuck expired close-deny seed is logically closed, retaining historical fallen_back delivery."),
    case("w21_fixed_expired_deny_live", "w21_expired_deny_live_red",
         purpose="Fair plain keys eventually succeed without a manual pane answer or lifecycle write."),
    case("w21_fixed_pane_write_probe", "w21_pane_write_probe",
         purpose="NeverHit now passes: logical closure makes the obsolete request's blocked pane-write path unreachable."),
    case("w21_fixed_expired_ordinary_control", "w21_expired_ordinary_control",
         purpose="Historical ordinary legacy delivery still derives delivered after expiry; no ack is fabricated."),
    case("w21_fixed_current_launch_control", "w21_current_launch_control",
         purpose="Current unproven legacy request remains held within its relay window."),
    case("w21_fixed_no_legacy_control", "w21_no_legacy_control",
         purpose="Focused no-request pending startup continues to accept keys."),
    case("w21_fixed_turn_end_control", "w21_current_launch_control",
         extra=("PreV7TurnEnd",), checks=("B21TurnEndHoldExists", "B21ProofFidelity"),
         purpose="Current legacy request with turn_end proof remains relay-held: clearing unp alone is insufficient."),
    case("w21_fixed_turn_end_relaunch", "w21_relaunch_red",
         extra=("PreV7TurnEnd",), checks=("B21ClosePersists", "B21ProofFidelity"),
         purpose="Prior turn_end proof is preserved by close; closed_at independently releases the resumed row."),
    case("w21_mutant_close_only", "w21_initial_red",
         repairs=("PreV7CloseRepair",),
         expected="FAIL(Invariant_RelayHoldOnlyCurrentLaunch_is_violated)",
         purpose="Close-only mutant still fails at the already-stuck initial state: read-time recovery is necessary."),
    case("w21_mutant_read_only", "w21_relaunch_red",
         extra=("PreV7TurnEnd",), repairs=("PreV7ReadRepair",),
         expected="FAIL(Invariant_RelayHoldOnlyCurrentLaunch_is_violated)",
         purpose="Read-only mutant fails after relaunch when preserved prior turn_end prevents agent_gone proof: durable closure is necessary."),
    case("w21_fixed_modern_full_control", None,
         purpose="Full original Spec and all original r5x_time properties with no legacy seed; modern live relay protection remains unchanged."),
)


def cfg_text(row):
    if row["original"] is None:
        cfg = (CODEX / "src/cfg/r5x_time.cfg").read_text()
    else:
        cfg = baseline.cfg_text(ORIGINAL[row["original"]])
        # Replace prediction comments inherited from frozen RED templates.
        cfg = "\n".join(line for line in cfg.splitlines()
                        if not line.startswith("\\*")) + "\n"
    cfg = (f"\\* b.w21 selected repaired rules; expected={row['expected']}; observed=NOT_RUN.\n"
           f"\\* {row['purpose']}\n" + cfg)
    cfg += "CONSTANTS\n"
    cfg += "".join(f"  {knob} <- KnobOn\n" for knob in row["extra_knobs"] + row["repairs"])
    cfg += "".join(f"INVARIANT {check}\n" for check in row["extra_checks"])
    cfg += "".join(f"PROPERTY {prop}\n" for prop in row["extra_properties"])
    return cfg


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    outputs = [HERE / "manifest.json", HERE / "scheduler-commands.txt",
               HERE / "cfg", HERE / "jobs"]
    existing = [path for path in outputs if path.exists() or path.is_symlink()]
    if existing:
        raise SystemExit("Refusing to overwrite fixed artifacts; first existing path: " + str(existing[0]))
    sources = {
        f"{MODEL}.tla": CODEX / f"src/{MODEL}.tla",
        "tla2tools.jar": TLA / "tla2tools.jar",
        "tlcjob.sh": TLA / "jobs/lib/tlcjob.sh",
    }
    # Validate all inputs before creating any context.
    for source in sources.values():
        if not source.is_file():
            raise SystemExit("Missing input: " + str(source))
    (HERE / "cfg").mkdir()
    (HERE / "jobs").mkdir()
    manifest = {
        "bug": "b.w21", "rules": "fixed-runtime-with-explicit-mutants",
        "observed": "NOT_RUN", "source_sha256": digest(sources[f"{MODEL}.tla"]),
        "red_manifest_sha256": digest(RED / "manifest.json"),
        "runtime_sha256": {name: digest(ROOT / name) for name in (
            "pkg/api/sendkeys_relay.go", "internal/store/request_close.go",
            "internal/store/schema.go")},
        "cases": [],
    }
    commands = []
    for row in CASES:
        name = row["name"]
        cfg = HERE / "cfg" / f"{name}.cfg"
        cfg.write_text(cfg_text(row))
        context = HERE / "jobs" / name
        context.mkdir()
        for filename, source in sources.items():
            shutil.copyfile(source, context / filename)
        shutil.copyfile(cfg, context / cfg.name)
        (context / "tlcjob.sh").chmod(0o755)
        (context / "runs").write_text(
            f"# b.w21 expected={row['expected']}; observed=NOT_RUN\n{MODEL} {name} @700\n")
        (context / "Dockerfile").write_text(
            "# Scheduler only: user submits this context; never execute on the dev host.\n"
            f"# Expected {row['expected']}; observed NOT_RUN.\n"
            "FROM eclipse-temurin:21-jre\nWORKDIR /work\nCOPY . /work/\n"
            "ENV XMX=2500m DIRECT=256m BUDGET=750 DISKCAP_MB=1500\n"
            'CMD ["/work/tlcjob.sh"]\n')
        enabled = (() if row["original"] is None else ORIGINAL[row["original"]][2])
        enabled += row["extra_knobs"] + row["repairs"]
        manifest["cases"].append({
            "name": name, "context": str(context.relative_to(HERE)),
            "spec": "Spec" if row["original"] is None else ORIGINAL[row["original"]][1],
            "enabled_knobs": enabled, "expected": row["expected"], "observed": "NOT_RUN",
            "purpose": row["purpose"], "sha256": {p.name: digest(p) for p in sorted(context.iterdir())},
        })
        commands.append(
            "/home/horde/projects/horde_admin/job-scheduler/bin/jobsched submit "
            f"--context '{context.resolve()}' --name b-w21-{name.removeprefix('w21_').replace('_', '-')} "
            "--channel C0B3X876XSB --network off --memory 4g --cpus 4 --timeout 15m")
    (HERE / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    (HERE / "scheduler-commands.txt").write_text("\n".join(commands) + "\n")
    print(f"Prepared {len(CASES)} separate contexts; every observation NOT_RUN; no model execution.")


if __name__ == "__main__":
    main()
