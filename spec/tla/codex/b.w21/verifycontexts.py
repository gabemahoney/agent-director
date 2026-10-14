#!/usr/bin/env python3
"""Static file checks only: no TLC, JVM, scheduler, container or repo binary."""

import hashlib
import json
from pathlib import Path
import re


HERE = Path(__file__).resolve().parent
BASELINE_COMMIT = "8c6998d4f978312cdd557a27483c2c6929f205e8"
BASELINE_MANIFEST_SHA256 = "0ab9035d9bad9b83963c666ef4f41d31f1be857d417443b3e3eca53dfb520a45"
BASELINE_MODEL_SHA256 = "3901dee2cefba073cff3ef53c9b511af16d5d84b9836d2a92d64ca15a723e43e"
BASELINE_INHERITED_SHA256 = "92c41d1e2ec59fdc0ad8b1ca489f455f517271def25a1166f844f5e8ecee4350"
RECEIPT_SHA256 = "ffa7019ce9352f236ed8ae7c09a50288786cac71664f9b4a26c5198e3e2c83ad"
RECEIPTS_ARCHIVE_SHA256 = "d39512d29e6e136f34f250ab80a6c744c022ee243ab1ff3af816b334f7620965"


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def checks(cfg, kind):
    return re.findall(rf"^{kind}\s+(\w+)", cfg, re.M)


def check_review_fixes(source, configs, inherited):
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


def read_receipts(manifest):
    """Verify archived input bytes; never read the evolving working model."""
    assert manifest["baseline"] == {
        "commit": BASELINE_COMMIT,
        "manifest_sha256": BASELINE_MANIFEST_SHA256,
        "model_context": "jobs/w21_initial_red/B146R5Cx.tla",
        "model_sha256": BASELINE_MODEL_SHA256,
        "inherited_cfg_sha256": BASELINE_INHERITED_SHA256,
    }
    assert manifest["receipts"]["file"] == "model-red-receipts.json"
    path = HERE / manifest["receipts"]["file"]
    assert path.is_file() and not path.is_symlink(), path
    data = path.read_bytes()
    assert sha256(data) == manifest["receipts"]["sha256"] == RECEIPTS_ARCHIVE_SHA256, path
    archive = json.loads(data)
    assert archive["bug"] == "b.w21" and archive["baseline_commit"] == BASELINE_COMMIT
    assert sha256(archive["baseline_manifest_text"].encode()) == BASELINE_MANIFEST_SHA256
    assert sha256(archive["inherited_cfg_text"].encode()) == BASELINE_INHERITED_SHA256
    baseline = json.loads(archive["baseline_manifest_text"])
    assert baseline["observed"] == "NOT_RUN"
    assert len(baseline["cases"]) == len(manifest["cases"]) == 9
    for case, original in zip(manifest["cases"], baseline["cases"]):
        assert set(case) == set(original) | {"evidence"}, case["name"]
        assert {key: case[key] for key in original if key != "observed"} == {
            key: value for key, value in original.items() if key != "observed"
        }, case["name"]
    receipt = archive["receipt"]
    assert receipt["sha256"] == sha256(receipt["text"].encode()) == RECEIPT_SHA256
    rows = re.findall(
        r"^b-w21-\S+ (j\d{14}-[a-f0-9]+) (\w+) \| "
        r"(JOB-VERDICT (\w+)=(\S+)/(\?|\d+))$", receipt["text"], re.M
    )
    assert len(rows) == 9 and len({row[3] for row in rows}) == 9
    assert {row[3] for row in rows} == {case["name"] for case in manifest["cases"]}
    assert {row[0] for row in rows} == set(archive["logs"])
    return archive, {row[3]: row for row in rows}


def check_observation(case, archive, receipt_rows, cfg):
    """Compare exact scheduler verdicts and TLC messages, preserving '?' counts."""
    name = case["name"]
    evidence = case["evidence"]
    job_id, status, job_line, receipt_name, result, distinct = receipt_rows[name]
    assert receipt_name == name and status == "succeeded"
    assert evidence["job_id"] == job_id and evidence["scheduler_status"] == status
    log = archive["logs"][job_id]
    assert log["source"] == f"/home/horde/.job-scheduler/jobs/{job_id}/run.log"
    assert sha256(log["text"].encode()) == log["sha256"] == evidence["log_sha256"]
    verdicts = re.findall(
        r"^VERDICT (\w+) checks=(\S+) result=(\S+) "
        r"distinct=(\?|\d+) depth=(\?|\d+) secs=(\d+)$", log["text"], re.M
    )
    assert len(verdicts) == 1, name
    logged_name, checked, logged_result, logged_distinct, depth, secs = verdicts[0]
    assert (logged_name, logged_result, logged_distinct) == (name, result, distinct)
    assert checked.split(",") == re.findall(r"^(?:INVARIANT|PROPERTY)\s+(\w+)", cfg, re.M), name
    assert re.findall(r"^VERDICT .*", log["text"], re.M) == [evidence["verdict_line"]]
    assert re.findall(r"^JOB-VERDICT .*", log["text"], re.M) == [job_line]
    assert evidence["job_verdict_line"] == job_line
    assert evidence["distinct_states"] == (None if distinct == "?" else int(distinct))
    assert evidence["depth"] == (None if depth == "?" else int(depth))
    assert evidence["seconds"] == int(secs)
    assert case["observed"] == result
    counts = re.findall(r"\d+ states generated, (\d+) distinct states found", log["text"])
    assert (not counts if distinct == "?" else counts and set(counts) == {distinct}), name
    depths = re.findall(r"The depth of the complete state graph search is (\d+)\.", log["text"])
    assert depths == ([] if depth == "?" else [depth]), name
    check_result(case, cfg, log["text"])


def check_result(case, cfg, log_text):
    name = case["name"]
    result = case["observed"]
    expected_match = result == case["expected"] or (
        name == "w21_expired_deny_live_red"
        and case["expected"] == "FAIL(Temporal_properties_were_violated)"
        and result == "FAIL(Temporal_property_B21KeysEventuallyAccepted_was_violated)"
    )
    assert expected_match and case["evidence"]["expected_matches"] is True, name
    if result == "PASS":
        assert "Model checking completed. No error has been found." in log_text
        assert not re.search(r"^Error:", log_text, re.M), name
    elif name == "w21_expired_deny_live_red":
        assert "Error: Temporal property B21KeysEventuallyAccepted was violated." in log_text
    else:
        invariant = re.fullmatch(r"FAIL\(Invariant_(\w+)_is_violated\)", result)
        assert invariant and invariant[1] in checks(cfg, "INVARIANT"), name
        message = f"Error: Invariant {invariant[1]} is violated"
        assert message + "." in log_text or message + " by the initial state:" in log_text


def main():
    manifest = json.loads((HERE / "manifest.json").read_text())
    assert manifest["bug"] == "b.w21" and manifest["rules"] == "current-unfixed"
    assert manifest["observed"] == "ALL_EXPECTED"
    archive, receipt_rows = read_receipts(manifest)
    names = [case["name"] for case in manifest["cases"]]
    assert len(names) == 9 and len(set(names)) == 9
    assert set(names) == {
        "w21_initial_red", "w21_relaunch_red", "w21_relaunch_probe",
        "w21_expired_deny_red", "w21_expired_deny_live_red", "w21_pane_write_probe",
        "w21_expired_ordinary_control", "w21_current_launch_control", "w21_no_legacy_control",
    }
    baseline_model = HERE / manifest["baseline"]["model_context"]
    assert sha256(baseline_model.read_bytes()) == BASELINE_MODEL_SHA256
    source = baseline_model.read_text()
    definitions = set(re.findall(r"^([A-Za-z][A-Za-z0-9_]*)\s*(?:\([^\n]*?\))?\s*==", source, re.M))
    for knob in ("PreV7Old", "PreV7Current", "PreV7Relaunch", "PreV7Expired", "PreV7CloseDeny"):
        assert re.search(rf"^{knob}\s*== FALSE", source, re.M), knob
    total = 0
    configs = {}
    for case in manifest["cases"]:
        context = HERE / case["context"]
        assert {p.name for p in context.iterdir()} == set(case["sha256"]), context
        size = 0
        for name, digest in case["sha256"].items():
            path = context / name
            assert path.is_file() and not path.is_symlink(), path
            assert sha256(path.read_bytes()) == digest, path
            size += path.stat().st_size
        assert size < 2 * 1024**3, (context, size)
        total += size
        assert case["sha256"]["B146R5Cx.tla"] == BASELINE_MODEL_SHA256, case["name"]
        cfg_name = case["name"] + ".cfg"
        assert (context / cfg_name).read_bytes() == (HERE / "cfg" / cfg_name).read_bytes(), cfg_name
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
        check_observation(case, archive, receipt_rows, cfg)
        evidence = case["evidence"]
        distinct = evidence["distinct_states"]
        print(f"STATIC-OK {case['name']} bytes={size} expected={case['expected']} "
              f"observed={case['observed']} job={evidence['job_id']} "
              f"distinct={distinct if distinct is not None else '?'}")
    check_review_fixes(source, configs, archive["inherited_cfg_text"])
    expected = {case["name"]: case["expected"] for case in manifest["cases"]}
    assert expected["w21_relaunch_probe"] == "FAIL(Invariant_B21RelaunchPendingUnreachable_is_violated)"
    commands = (HERE / "scheduler-commands.txt").read_text().splitlines()
    assert len(commands) == len(manifest["cases"])
    for command, case in zip(commands, manifest["cases"]):
        assert f"--context '{(HERE / case['context']).resolve()}'" in command, command
        assert "--channel C0B3X876XSB --network off --memory 4g --cpus 4 --timeout 15m" in command, command
    print(f"STATIC-OK {len(commands)} immutable baseline contexts, {total} total context bytes; "
          "nine archived scheduler receipts verified; no model or scheduler execution.")


if __name__ == "__main__":
    main()
