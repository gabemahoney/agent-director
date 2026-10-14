#!/usr/bin/env python3
"""Static file checks only: no TLC, JVM, scheduler, container or repo binary."""

import hashlib
import json
from pathlib import Path
import re


HERE = Path(__file__).resolve().parent
CODEX = HERE.parent
TLA = CODEX.parent


def checks(cfg, kind):
    return re.findall(rf"^{kind}\s+(\w+)", cfg, re.M)


def check_review_fixes(source, configs):
    """Check the reviewed spellings and wiring; this is not a TLA parser."""
    assert "    /\\ nl = (PreV7Old /\\ ~PreV7Current)\n" in source
    assert len(re.findall(r"\bnl'\s*=", source)) == 1
    relaunch = source.split("Relaunch ==\n", 1)[1].split("\nHookStep", 1)[0]
    assert '/\\ nl\' = TRUE' in relaunch
    assert '/\\ rs = "missing" /\\ proc = "gone"' in relaunch
    assert '/\\ proc\' = "alive" /\\ aph\' = "ss" /\\ rs\' = "pending"' in relaunch
    crash = source.split("AgentCrash ==\n", 1)[1].split("\n-----------------------------------------------------------------------------", 1)[0]
    missing = source.split("FMMissing ==\n", 1)[1].split("\n-----------------------------------------------------------------------------", 1)[0]
    assert '/\\ Crash /\\ proc = "alive"' in crash and '/\\ proc\' = "gone"' in crash
    assert '/\\ Sweep /\\ proc = "gone" /\\ rs \\in Live' in missing
    assert '/\\ rs\' = "missing"' in missing
    focused = source.split("B21Next ==\n", 1)[1].split("\nB21Fairness", 1)[0]
    for action in ("AgentCrash", "FMMissing", "Relaunch"):
        assert action in focused, action

    original = 'RecordsClose == <>[](\\A r \\in R : rec[r] => Closed(r))'
    assert original in source
    settlement = source.split("B21RecordsSettle ==\n", 1)[1].split("\n\n", 1)[0]
    assert '\\A r \\in R \\ Legacy : rec[r] => Closed(r)' in settlement
    assert '\\A r \\in Legacy : rec[r] => (~RelayHolds(r) /\\ ~Reported(r))' in settlement
    initial = configs["w21_initial_red"]
    inherited = (CODEX / "src/cfg/r5x_time.cfg").read_text()
    assert inherited.count("PROPERTY RecordsClose\n") == 1
    assert initial.startswith(inherited.replace("PROPERTY RecordsClose\n", "PROPERTY B21RecordsSettle\n"))
    assert "RecordsClose" not in checks(initial, "PROPERTY")
    assert "B21RecordsSettle" in checks(initial, "PROPERTY")
    assert "RelayHoldOnlyCurrentLaunch" in checks(initial, "INVARIANT")

    probe = configs["w21_relaunch_probe"]
    red = configs["w21_relaunch_red"]
    for cfg in (probe, red):
        assert checks(cfg, "SPECIFICATION") == ["B21Spec"]
        assert re.search(r'^\s*Env = \{"Stray", "Crash"\}$', cfg, re.M)
        assert re.findall(r'^\s*(PreV7\w+)\s*<-\s*KnobOn', cfg, re.M) == [
            "PreV7Old", "PreV7Current", "PreV7Relaunch"]
    assert checks(probe, "INVARIANT") == ["TypeOK", "B21SeedFidelity", "B21RelaunchPendingUnreachable"]
    assert not checks(probe, "PROPERTY")
    assert "RelayHoldOnlyCurrentLaunch" in checks(red, "INVARIANT")
    unreachable = source.split("B21RelaunchPendingUnreachable ==\n", 1)[1].split("\n\n", 1)[0]
    assert 'PreV7Old /\\ PreV7Current /\\ PreV7Relaunch /\\ nl' in unreachable
    assert '/\\ proc = "alive" /\\ rs = "pending" /\\ aph = "ss"' in unreachable
    assert "RelayHolds" not in unreachable and "Reported" not in unreachable


def main():
    manifest = json.loads((HERE / "manifest.json").read_text())
    assert manifest["bug"] == "b.w21" and manifest["rules"] == "current-unfixed"
    assert manifest["observed"] == "NOT_RUN"
    names = [case["name"] for case in manifest["cases"]]
    assert len(names) == 9 and len(set(names)) == 9
    assert set(names) == {
        "w21_initial_red", "w21_relaunch_red", "w21_relaunch_probe",
        "w21_expired_deny_red", "w21_expired_deny_live_red", "w21_pane_write_probe",
        "w21_expired_ordinary_control", "w21_current_launch_control", "w21_no_legacy_control",
    }
    source = (CODEX / "src/B146R5Cx.tla").read_text()
    definitions = set(re.findall(r"^([A-Za-z][A-Za-z0-9_]*)\s*(?:\([^\n]*?\))?\s*==", source, re.M))
    for knob in ("PreV7Old", "PreV7Current", "PreV7Relaunch", "PreV7Expired", "PreV7CloseDeny"):
        assert re.search(rf"^{knob}\s*== FALSE", source, re.M), knob
    total = 0
    configs = {}
    for case in manifest["cases"]:
        assert case["observed"] == "NOT_RUN", case["name"]
        context = HERE / case["context"]
        assert {p.name for p in context.iterdir()} == set(case["sha256"]), context
        size = 0
        for name, digest in case["sha256"].items():
            path = context / name
            assert path.is_file() and not path.is_symlink(), path
            assert hashlib.sha256(path.read_bytes()).hexdigest() == digest, path
            size += path.stat().st_size
        assert size < 2 * 1024**3, (context, size)
        total += size
        for filename, source_path in (
            ("B146R5Cx.tla", CODEX / "src/B146R5Cx.tla"),
            ("tla2tools.jar", TLA / "tla2tools.jar"),
            ("tlcjob.sh", TLA / "jobs/lib/tlcjob.sh"),
            (case["name"] + ".cfg", HERE / "cfg" / (case["name"] + ".cfg")),
        ):
            assert (context / filename).read_bytes() == source_path.read_bytes(), filename
        cfg = (context / (case["name"] + ".cfg")).read_text()
        configs[case["name"]] = cfg
        assert checks(cfg, "SPECIFICATION") == [case["spec"]], case["name"]
        for check in re.findall(r"^(?:SPECIFICATION|INVARIANT|PROPERTY)\s+(\w+)", cfg, re.M):
            assert check in definitions, (case["name"], check)
        for knob, replacement in re.findall(r"^\s*(\w+)\s*<-\s*(\w+)", cfg, re.M):
            assert knob in definitions and replacement in definitions, (knob, replacement)
        actual_knobs = re.findall(r"^\s*(PreV7\w+)\s*<-\s*KnobOn", cfg, re.M)
        assert actual_knobs == case["enabled_knobs"], case["name"]
        run_lines = [s for s in (context / "runs").read_text().splitlines() if not s.startswith("#")]
        assert run_lines == [f"B146R5Cx {case['name']} @700"], case["name"]
        assert (context / "tlcjob.sh").stat().st_mode & 0o111, context
        print(f"STATIC-OK {case['name']} bytes={size} expected={case['expected']} observed=NOT_RUN")
    check_review_fixes(source, configs)
    expected = {case["name"]: case["expected"] for case in manifest["cases"]}
    assert expected["w21_relaunch_probe"] == "FAIL(Invariant_B21RelaunchPendingUnreachable_is_violated)"
    commands = (HERE / "scheduler-commands.txt").read_text().splitlines()
    assert len(commands) == len(manifest["cases"])
    for command, case in zip(commands, manifest["cases"]):
        assert f"--context '{(HERE / case['context']).resolve()}'" in command, command
        assert "--channel C0B3X876XSB --network off --memory 4g --cpus 4 --timeout 15m" in command, command
    print(f"STATIC-OK {len(commands)} scheduler commands, {total} total context bytes; no execution evidence.")


if __name__ == "__main__":
    main()
