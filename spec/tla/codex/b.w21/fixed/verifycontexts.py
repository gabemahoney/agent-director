#!/usr/bin/env python3
"""Static artifact/fidelity checks only; does not parse or execute TLA+."""

import ast
import hashlib
import importlib.util
import json
from pathlib import Path
import re
import subprocess
import sys


sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent
RED = HERE.parent
CODEX = RED.parent
ROOT = CODEX.parents[2]
SOURCE_COMMIT = "9d025f8f2caf176f3a60421f52ba94b293021bdb"
SUBMITTED_MANIFEST_SHA256 = "dac59bfb5e7d03a3f6a8881981526fca22a3c260af76cb58007caa5b10bc555f"
FIXED_RECEIPT_SHA256 = "7aa2cfb6456d26e45df3ab85b47f88e28d794723452f6e6fe7ff3c5358e8cc5e"
FAILED_INITIAL_LOG_SHA256 = "03163d3ce191129ba172edb10af5577a9cc5c592951acb714794280a81df5b25"


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def frozen_bytes(path):
    return subprocess.check_output(["git", "show", f"{SOURCE_COMMIT}:{path}"], cwd=ROOT)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def verify_receipts(manifest, submitted):
    """Optional observational overlay; predictions and submitted bytes are immutable.

    model-fixed-receipts.json has bug, source_commit, baseline_manifest_text,
    baseline_manifest_sha256, receipt {text, sha256}, and logs keyed by job ID
    with {source, text, sha256}. Each manifest case gains evidence with job_id,
    log_sha256, verdict_line, job_verdict_line, expected_matches. The top-level
    receipts points to {file, sha256}. A real mismatch is valid evidence.
    """
    if manifest["observed"] == "NOT_RUN":
        assert manifest == submitted, "NOT_RUN manifest differs from submitted predictions"
        assert not (HERE / "model-fixed-receipts.json").exists(), "Unbound fixed receipts"
        return
    assert manifest["observed"] in {"ALL_EXPECTED", "MISMATCH"}
    assert set(manifest) == set(submitted) | {"receipts"}
    assert {key: value for key, value in manifest.items() if key not in {"observed", "cases", "receipts"}} == {
        key: value for key, value in submitted.items() if key not in {"observed", "cases"}}
    assert manifest["receipts"]["file"] == "model-fixed-receipts.json"
    archive_path = HERE / manifest["receipts"]["file"]
    assert archive_path.is_file() and not archive_path.is_symlink()
    assert digest(archive_path) == manifest["receipts"]["sha256"]
    archive = json.loads(archive_path.read_text())
    assert archive["bug"] == "b.w21" and archive["source_commit"] == SOURCE_COMMIT
    assert sha(archive["baseline_manifest_text"].encode()) == archive["baseline_manifest_sha256"] == SUBMITTED_MANIFEST_SHA256
    assert json.loads(archive["baseline_manifest_text"]) == submitted
    receipt = archive["receipt"]
    assert sha(receipt["text"].encode()) == receipt["sha256"] == FIXED_RECEIPT_SHA256
    rows = re.findall(r"^b-w21-\S+ (j\d{14}-[a-f0-9]+) \| (JOB-VERDICT (\w+)=(\S+)/(\?|\d+))$",
                      receipt["text"], re.M)
    assert len(rows) == 14 and len({row[2] for row in rows}) == 14
    assert len({row[0] for row in rows}) == 14
    assert set(archive["logs"]) == {row[0] for row in rows}
    by_name = {row[2]: row for row in rows}
    assert set(by_name) == {case["name"] for case in submitted["cases"]}
    matches = []
    assert len(manifest["cases"]) == len(submitted["cases"])
    for case, original in zip(manifest["cases"], submitted["cases"]):
        assert set(case) == set(original) | {"evidence"}
        assert {key: case[key] for key in original if key != "observed"} == {
            key: value for key, value in original.items() if key != "observed"}
        name = case["name"]
        job, job_line, logged_name, result, distinct = by_name[name]
        assert logged_name == name and case["observed"] == result
        evidence = case["evidence"]
        assert evidence["job_id"] == job and evidence["job_verdict_line"] == job_line
        log = archive["logs"][job]
        assert log["source"] == f"/home/horde/.job-scheduler/jobs/{job}/run.log"
        assert sha(log["text"].encode()) == log["sha256"] == evidence["log_sha256"]
        if name == "w21_fixed_initial":
            assert log["sha256"] == FAILED_INITIAL_LOG_SHA256
        verdicts = re.findall(r"^VERDICT (\w+) checks=(\S+) result=(\S+) distinct=(\?|\d+) depth=(\?|\d+) secs=(\d+)$",
                              log["text"], re.M)
        assert len(verdicts) == 1
        logname, checks, logresult, logdistinct, depth, seconds = verdicts[0]
        assert (logname, logresult, logdistinct) == (name, result, distinct)
        cfg = (HERE / "cfg" / f"{name}.cfg").read_text()
        assert checks.split(",") == re.findall(r"^(?:INVARIANT|PROPERTY)\s+(\w+)", cfg, re.M)
        assert re.findall(r"^VERDICT .*", log["text"], re.M) == [evidence["verdict_line"]]
        assert re.findall(r"^JOB-VERDICT .*", log["text"], re.M) == [job_line]
        counts = re.findall(r"\d+ states generated, (\d+) distinct states found", log["text"])
        assert (not counts if distinct == "?" else counts and set(counts) == {distinct}), name
        depths = re.findall(r"The depth of the complete state graph search is (\d+)\.", log["text"])
        assert depths == ([] if depth == "?" else [depth]), name
        if result == "PASS":
            assert "Model checking completed. No error has been found." in log["text"]
            assert not re.search(r"^Error:", log["text"], re.M)
        else:
            invariant = re.fullmatch(r"FAIL\(Invariant_(\w+)_is_violated\)", result)
            assert invariant and invariant[1] in checks.split(",")
            message = f"Error: Invariant {invariant[1]} is violated"
            assert message + "." in log["text"] or message + " by the initial state:" in log["text"]
        match = result == original["expected"]
        assert evidence["expected_matches"] is match
        matches.append(match)
    assert manifest["observed"] == ("ALL_EXPECTED" if all(matches) else "MISMATCH")


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
    submitted_bytes = frozen_bytes("spec/tla/codex/b.w21/fixed/manifest.json")
    assert sha(submitted_bytes) == SUBMITTED_MANIFEST_SHA256
    submitted = json.loads(submitted_bytes)
    verify_receipts(manifest, submitted)
    assert manifest["bug"] == "b.w21"
    assert manifest["rules"] == "fixed-runtime-with-explicit-mutants"
    assert manifest["red_manifest_sha256"] == digest(RED / "manifest.json")
    # Submitted source is 9d025f8f, never the evolving canonical model.
    source_bytes = frozen_bytes("spec/tla/codex/src/B146R5Cx.tla")
    assert manifest["source_sha256"] == sha(source_bytes)
    source = source_bytes.decode()
    check_fidelity(source)
    for name, expected in manifest["runtime_sha256"].items():
        assert sha(frozen_bytes(name)) == expected, name
        assert digest(ROOT / name) == expected, name
    definitions = set(re.findall(r"^([A-Za-z][A-Za-z0-9_]*)\s*(?:\([^\n]*?\))?\s*==", source, re.M))
    assert len(manifest["cases"]) == len(generator.CASES) == 14
    commands = (HERE / "scheduler-commands.txt").read_text().splitlines()
    assert len(commands) == len(generator.CASES)
    total = 0
    for row, case, command in zip(generator.CASES, manifest["cases"], commands):
        assert case["name"] == row["name"] and case["expected"] == row["expected"]
        assert case["purpose"] == row["purpose"]
        context = HERE / case["context"]
        assert context.parent == HERE / "jobs" and context.name == case["name"]
        assert {p.name for p in context.iterdir()} == set(case["sha256"])
        for name, expected in case["sha256"].items():
            path = context / name
            assert path.is_file() and not path.is_symlink() and digest(path) == expected, path
            total += path.stat().st_size
        assert (context / "B146R5Cx.tla").read_bytes() == source_bytes
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
        print(f"STATIC-OK {case['name']} expected={case['expected']} observed={case['observed']}")
    expected = {case["name"]: case["expected"] for case in manifest["cases"]}
    assert sum(result == "PASS" for result in expected.values()) == 11
    assert expected["w21_fixed_relaunch_probe"] == "FAIL(Invariant_B21RelaunchPendingUnreachable_is_violated)"
    assert expected["w21_fixed_pane_write_probe"] == "PASS"
    print(f"STATIC-OK 14 fixed/mutant contexts, {total} bytes; original RED evidence unchanged; no TLC/parser/JVM/scheduler execution.")


if __name__ == "__main__":
    main()
