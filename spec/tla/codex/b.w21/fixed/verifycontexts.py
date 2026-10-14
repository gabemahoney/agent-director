#!/usr/bin/env python3
"""Static artifact/fidelity checks only; does not parse or execute TLA+."""

import ast
import hashlib
import importlib.util
import json
from pathlib import Path
import re
import sys


sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent
RED = HERE.parent
CODEX = RED.parent
ROOT = CODEX.parents[2]


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def load_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def check_fidelity(source):
    for knob in ("PreV7Old", "PreV7Current", "PreV7Relaunch", "PreV7Expired",
                 "PreV7CloseDeny", "PreV7TurnEnd", "PreV7ReadRepair", "PreV7CloseRepair"):
        assert re.search(rf"^{knob}\s*== FALSE", source, re.M), knob
    released = source.split("LegacyReleased(r) ==\n", 1)[1].split("(*", 1)[0]
    for term in ('PreV7ReadRepair', 'r \\in Legacy', 'dec[r] # "none"',
                 'legacyProof = "agent_gone"'):
        assert term in released, term
    assert "nl" not in released and "unp" not in released
    assert source.count("legacyProof'") == 1
    track = source.split("Track(cap, sread) ==\n", 1)[1].split("(* Whole", 1)[0]
    assert 'legacyProof # "none" THEN legacyProof' in track
    assert 'rs \\in Live /\\ rs\' = "missing"' in track
    assert 'rs = "missing" /\\ rs\' = "pending"' in track
    assert 'cap /\\ aph \\in {"stop", "notif", "notifL"}' in track
    assert 'legacyProof>>' in source and 'legacyProof = IF ~PreV7Old THEN "none"' in source
    store = source.split("StoreCloseable(r) ==\n", 1)[1].split("Open ==", 1)[0]
    for term in ("StoreAwaits(r)", "PreV7CloseRepair", "~cls[r]", '~ack[r]',
                 'dec[r] # "none"', 'pane[r] \\in {"none", "intent"}'):
        assert term in store, term
    for action in ("FMMissing", "Relaunch"):
        body = source.split(action + " ==\n", 1)[1].split("\n\n", 1)[0]
        assert "StoreCloseable(q)" in body, action
    guard = source.split("GuardClosed(r) ==", 1)[1].split("RelayHolds(r) ==", 1)[0]
    closed = source.split("Closed(r) ==\n", 1)[1].split("\n\n", 1)[0]
    assert "LegacyReleased(r)" in guard and "LegacyReleased(r)" in closed
    reported = source.split("Reported(r) ==\n", 1)[1].split("\n\n", 1)[0]
    assert "~Closed(r)" in reported
    delivery = source.split("LegacyDelivery(r) ==\n", 1)[1].split("\n\n", 1)[0]
    assert "LegacyReleased" not in delivery and "legacyProof" not in delivery
    pane = source.split("SendOn(i, a, ks, v) ==\n", 1)[1].split("\n\n", 1)[0]
    assert "StoreAwaits(a)" in pane and "StoreCloseable" not in pane
    assert 'RecordsClose == <>[](\\A r \\in R : rec[r] => Closed(r))' in source
    # New provenance is constant "none" with all old scenario knobs off;
    # every action supplies it through Track, including all fairness actions.
    assert 'IF ~PreV7Old \\/ legacyProof # "none" THEN legacyProof' in track


def main():
    # Anchor and verify every frozen RED context/receipt with its own verifier.
    load_module("red_verifier", RED / "verifycontexts.py").main()
    generator = load_module("fixed_generator", HERE / "mkcontexts.py")
    for path in (HERE / "mkcontexts.py", HERE / "verifycontexts.py"):
        ast.parse(path.read_text(), filename=str(path))
    manifest = json.loads((HERE / "manifest.json").read_text())
    assert manifest["bug"] == "b.w21" and manifest["observed"] == "NOT_RUN"
    assert manifest["rules"] == "fixed-runtime-with-explicit-mutants"
    assert manifest["red_manifest_sha256"] == digest(RED / "manifest.json")
    source_path = CODEX / "src/B146R5Cx.tla"
    assert manifest["source_sha256"] == digest(source_path)
    source = source_path.read_text()
    check_fidelity(source)
    for name, expected in manifest["runtime_sha256"].items():
        assert digest(ROOT / name) == expected, name
    definitions = set(re.findall(r"^([A-Za-z][A-Za-z0-9_]*)\s*(?:\([^\n]*?\))?\s*==", source, re.M))
    assert len(manifest["cases"]) == len(generator.CASES) == 14
    commands = (HERE / "scheduler-commands.txt").read_text().splitlines()
    assert len(commands) == len(generator.CASES)
    total = 0
    for row, case, command in zip(generator.CASES, manifest["cases"], commands):
        assert case["name"] == row["name"] and case["expected"] == row["expected"]
        assert case["observed"] == "NOT_RUN" and case["purpose"] == row["purpose"]
        context = HERE / case["context"]
        assert context.parent == HERE / "jobs" and context.name == case["name"]
        assert {p.name for p in context.iterdir()} == set(case["sha256"])
        for name, expected in case["sha256"].items():
            path = context / name
            assert path.is_file() and not path.is_symlink() and digest(path) == expected, path
            total += path.stat().st_size
        assert (context / "B146R5Cx.tla").read_bytes() == source_path.read_bytes()
        for name, path in (("tla2tools.jar", CODEX.parent / "tla2tools.jar"),
                           ("tlcjob.sh", CODEX.parent / "jobs/lib/tlcjob.sh")):
            assert digest(context / name) == digest(path), name
        assert (context / "tlcjob.sh").stat().st_mode & 0o111
        cfgname = case["name"] + ".cfg"
        cfg = (HERE / "cfg" / cfgname).read_text()
        assert cfg == generator.cfg_text(row) == (context / cfgname).read_text()
        assert re.findall(r"^SPECIFICATION\s+(\w+)", cfg, re.M) == [case["spec"]]
        for name in re.findall(r"^(?:SPECIFICATION|INVARIANT|PROPERTY)\s+(\w+)", cfg, re.M):
            assert name in definitions, (case["name"], name)
        for name, replacement in re.findall(r"^\s*(\w+)\s*<-\s*(\w+)", cfg, re.M):
            assert name in definitions and replacement in definitions, (name, replacement)
        assert re.findall(r"^\s*(PreV7\w+)\s*<-\s*KnobOn", cfg, re.M) == case["enabled_knobs"]
        assert (context / "runs").read_text().splitlines() == [
            f"# b.w21 expected={case['expected']}; observed=NOT_RUN",
            f"B146R5Cx {case['name']} @700"]
        assert f"--context '{context.resolve()}'" in command
        assert "--channel C0B3X876XSB --network off --memory 4g --cpus 4 --timeout 15m" in command
        assert "observed NOT_RUN" in (context / "Dockerfile").read_text()
        print(f"STATIC-OK {case['name']} expected={case['expected']} observed=NOT_RUN")
    expected = {case["name"]: case["expected"] for case in manifest["cases"]}
    assert sum(result == "PASS" for result in expected.values()) == 11
    assert expected["w21_fixed_relaunch_probe"] == "FAIL(Invariant_B21RelaunchPendingUnreachable_is_violated)"
    assert expected["w21_fixed_pane_write_probe"] == "PASS"
    print(f"STATIC-OK 14 fixed/mutant contexts, {total} bytes; original RED evidence unchanged; no TLC/parser/JVM/scheduler execution.")


if __name__ == "__main__":
    main()
