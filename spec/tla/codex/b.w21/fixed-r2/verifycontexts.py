#!/usr/bin/env python3
"""Verify r2 hashes and model fidelity without parsing or running the model."""

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
SUBMITTED_COMMIT = "b9f73c45f3802feb8cc02a9b03f4873f4dd859bb"
SUBMITTED_MANIFEST_SHA256 = "b58999c89c5cc9237b46d7fa706fcb71ffebe7347bf8d3680800e50823ee7066"
RECEIPT_SHA256 = "83b4ed31740ec2ada0147623c0cd123e473c0ae5cf8dadc3196c313c995efd8d"
RERUN_JOB = "j20261015001930-15ca4e"
RERUN_CONTEXT = "/tmp/claude-1000/ad-codex/w21-r2-initial-big"
METADATA_FIELDS = ("id", "name", "context_dir", "dockerfile", "context_bytes", "context_sha256",
                   "memory", "cpus", "timeout_s", "network", "state", "exit_code", "build_exit_code")
# Job, case, observed result, distinct states, reported depth, elapsed seconds, log hash.
ATTEMPTS = (
    ("j20261015000702-bf3dab", "w21_r2_initial", "INCOMPLETE(time-budget)", 2000377, ">=26", 703,
     "0a3dc3dfed6287eb6e9b31719108a41aec88954ba935c1fe7c035c67599f376e"),
    ("j20261015000702-e3dee4", "w21_r2_modern_full_control", "PASS", 296382, "35", 57,
     "9bba1978c3b3665e8cf279b56e8745b2789b67c694a8d533f473b41fa1fea7b6"),
    ("j20261015000702-38ad71", "w21_r2_mixed_acked_live_time", "PASS", 29, "14", 7,
     "3a37fe3976996693e6527e69a57e267daec31e9e2defb9f623684cb8eb2c4039"),
    ("j20261015000702-4884e8", "w21_r2_mixed_acked_live_exact", "PASS", 27, "11", 7,
     "b10707f453c166c704dfb63e741c21218afc09a77680bd0752944315fe0491a4"),
    ("j20261015000703-bb16b3", "w21_r2_mixed_acked_live_probe",
     "FAIL(Invariant_B21MixedAckedLiveUnreachable_is_violated)", 24, "9", 7,
     "73e5c04520aa0436a37cb861c8631d9c974b79f4da1519a1b48888bb063a3fb7"),
    ("j20261015000703-99fcf3", "w21_r2_mutant_ack_shortcut", "FAIL(Invariant_NoRace_is_violated)",
     31, "12", 7, "fc3fee72dc8ad425ddfbb076a0b4fba2485311da9f8666f7933fd97f8deb3457"),
    (RERUN_JOB, "w21_r2_initial", "PASS", 6289976, "43", 2555,
     "6ab6bab52ad5c94a5bab7280faa50c7c5099a1a33c5a90a1bc3fe04f5345fa68"),
)


def text_sha(text):
    return hashlib.sha256(text.encode()).hexdigest()


def frozen_bytes(generator, path):
    return subprocess.check_output(["git", "show", f"{SUBMITTED_COMMIT}:{path}"], cwd=generator.ROOT)


def verify_text(record, source, expected_hash=None):
    assert set(record) == {"source", "text", "sha256"}
    assert record["source"] == source
    assert text_sha(record["text"]) == record["sha256"]
    if expected_hash is not None:
        assert record["sha256"] == expected_hash
    path = Path(source)
    if path.exists():
        assert sha(path) == record["sha256"], source


def verify_metadata(record, job, name, context):
    """Archive only allowlisted fields; never copy job env/secrets or container inspect."""
    assert set(record) == {"source", "sha256", "values"}
    source = f"/home/horde/.job-scheduler/jobs/{job}/job.json"
    assert record["source"] == source and re.fullmatch(r"[a-f0-9]{64}", record["sha256"])
    values = record["values"]
    assert set(values) == set(METADATA_FIELDS)
    big = job == RERUN_JOB
    assert values == {
        "id": job, "name": "b-w21-" + name.removeprefix("w21_").replace("_", "-") + ("-big" if big else ""),
        "context_dir": context, "dockerfile": "Dockerfile",
        "context_bytes": values["context_bytes"], "context_sha256": values["context_sha256"],
        "memory": 25769803776 if big else 4294967296, "cpus": 4,
        "timeout_s": 10800 if big else 900, "network": "off", "state": "succeeded",
        "exit_code": 0, "build_exit_code": 0,
    }
    assert type(values["context_bytes"]) is int and values["context_bytes"] > 0
    assert re.fullmatch(r"[a-f0-9]{64}", values["context_sha256"])
    if Path(source).exists():
        assert sha(Path(source)) == record["sha256"]
        live = json.loads(Path(source).read_text())
        assert {key: live[key] for key in METADATA_FIELDS} == values


def verify_resources(overlay, baseline):
    """The rerun changes only Docker resource limits and the per-run time budget."""
    assert set(overlay) == {"job_id", "case", "context_dir", "sha256", "files"}
    assert (overlay["job_id"], overlay["case"], overlay["context_dir"]) == (
        RERUN_JOB, "w21_r2_initial", RERUN_CONTEXT)
    original = baseline["cases"][0]
    context = HERE / original["context"]
    docker = (context / "Dockerfile").read_text().replace(
        "ENV XMX=2500m DIRECT=256m BUDGET=750 DISKCAP_MB=1500\n",
        "ENV XMX=16g DIRECT=4g BUDGET=10200 DISKCAP_MB=30000\n")
    runs = (context / "runs").read_text().replace(" @700\n", " @10000\n")
    assert docker != (context / "Dockerfile").read_text() and runs != (context / "runs").read_text()
    assert set(overlay["files"]) == {"Dockerfile", "runs"}
    for name, expected in (("Dockerfile", docker), ("runs", runs)):
        record = overlay["files"][name]
        verify_text(record, f"{RERUN_CONTEXT}/{name}", text_sha(expected))
        assert record["text"] == expected
    hashes = dict(original["sha256"])
    hashes.update({name: record["sha256"] for name, record in overlay["files"].items()})
    assert overlay["sha256"] == hashes
    rerun = Path(RERUN_CONTEXT)
    if rerun.exists():
        assert not rerun.is_symlink() and {p.name for p in rerun.iterdir()} == set(hashes)
        for name, expected in hashes.items():
            assert sha(rerun / name) == expected, name


def verify_log(log, attempt, cfg):
    job, name, result, distinct, depth, seconds, log_hash = attempt
    assert set(log) == {"source", "text", "sha256", "metadata"}
    verify_text({key: log[key] for key in ("source", "text", "sha256")},
                f"/home/horde/.job-scheduler/jobs/{job}/run.log", log_hash)
    text = log["text"]
    verdict = (f"VERDICT {name} checks={','.join(checks(cfg))} result={result} "
               f"distinct={distinct} depth={depth} secs={seconds}")
    job_line = f"JOB-VERDICT {name}={result}/{distinct}"
    assert re.findall(r"^VERDICT .*", text, re.M) == [verdict]
    assert re.findall(r"^JOB-VERDICT .*", text, re.M) == [job_line]
    budget = 10000 if job == RERUN_JOB else 700
    assert text.splitlines()[0] == f"=== RUN B146R5Cx {name} () checks={','.join(checks(cfg))} budget={budget}s"
    depths = re.findall(r"^The depth of the complete state graph search is (\d+)\.$", text, re.M)
    counts = re.findall(r"^\d+ states generated, (\d+) distinct states found, (\d+) states left on queue\.$", text, re.M)
    if result == "INCOMPLETE(time-budget)":
        assert depths == counts == []
        assert "Model checking completed." not in text and not re.search(r"^Error:", text, re.M)
        progress = re.findall(r"Progress\((\d+)\).*? ([\d,]+) distinct states found .*? ([\d,]+) states left on queue\.", text)
        assert progress and progress[-1] == ("26", "2,000,377", "472,255")
    else:
        assert depths == [depth] and counts == [(str(distinct), "0")]
        if result == "PASS":
            assert "Model checking completed. No error has been found." in text
            assert not re.search(r"^Error:", text, re.M)
            if re.search(r"^PROPERTY\s", cfg, re.M):
                assert re.search(r"Checking .* temporal properties for the complete state space.*\nFinished checking temporal properties", text)
        else:
            invariant = re.fullmatch(r"FAIL\(Invariant_(\w+)_is_violated\)", result)
            assert invariant and invariant[1] in checks(cfg)
            assert f"Error: Invariant {invariant[1]} is violated." in text
            assert "Error: The behavior up to this point is:" in text
    return verdict, job_line


def verify_receipts(manifest, baseline):
    """Observed overlay schema (all text hashes use UTF-8 bytes):

    manifest adds receipts {file,sha256} and resource_overlay. Each case adds
    evidence {attempts:[job IDs in receipt order], final_job_id, expected_matches}
    and replaces only observed. Archive model-r2-receipts.json has bug, round,
    source_commit, baseline_manifest_text, baseline_manifest_sha256, receipt
    {source,text,sha256}, logs keyed by all seven job IDs, and resource_overlay.
    Each log has source,text,sha256,metadata {source,sha256,values}; values contains
    exactly METADATA_FIELDS from job.json. resource_overlay has job_id,case,
    context_dir,sha256 (all six rerun files), files (only Dockerfile/runs, each
    {source,text,sha256}). It is identical in manifest and archive. Original
    context/cfg/model files and their frozen hashes are never replaced.
    Local source logs/metadata/rerun files are compared when still available;
    absent scheduler files do not turn archived metadata into execution proof.
    """
    archive_path = HERE / "model-r2-receipts.json"
    if manifest["observed"] == "NOT_RUN":
        assert manifest == baseline and not archive_path.exists()
        return
    assert set(manifest) == set(baseline) | {"receipts", "resource_overlay"}
    assert {k: v for k, v in manifest.items() if k not in {"observed", "cases", "receipts", "resource_overlay"}} == {
        k: v for k, v in baseline.items() if k not in {"observed", "cases"}}
    assert set(manifest["receipts"]) == {"file", "sha256"}
    assert manifest["receipts"]["file"] == archive_path.name
    assert sha(archive_path) == manifest["receipts"]["sha256"]
    archive = json.loads(archive_path.read_text())
    assert set(archive) == {"bug", "round", "source_commit", "baseline_manifest_text",
                            "baseline_manifest_sha256", "receipt", "logs", "resource_overlay"}
    assert (archive["bug"], archive["round"], archive["source_commit"]) == ("b.w21", "fixed-r2", SUBMITTED_COMMIT)
    assert text_sha(archive["baseline_manifest_text"]) == archive["baseline_manifest_sha256"] == SUBMITTED_MANIFEST_SHA256
    assert json.loads(archive["baseline_manifest_text"]) == baseline
    verify_text(archive["receipt"],
                "/home/horde/projects/agent-director-project/tickets/Bugs/b.w21/fixed-r2-results.txt", RECEIPT_SHA256)
    rows = re.findall(r"^b-w21-\S+ (j\d{14}-[a-f0-9]+) (?:succeeded )?(JOB-VERDICT (\w+)=(\S+)/(\d+))$",
                      archive["receipt"]["text"], re.M)
    assert len(rows) == len(archive["receipt"]["text"].splitlines()) == 7
    assert [(r[0], r[2], r[3], int(r[4])) for r in rows] == [a[:4] for a in ATTEMPTS]
    assert set(archive["logs"]) == {a[0] for a in ATTEMPTS}
    assert manifest["resource_overlay"] == archive["resource_overlay"]
    verify_resources(archive["resource_overlay"], baseline)
    assert len(manifest["cases"]) == len(baseline["cases"])
    for case, original in zip(manifest["cases"], baseline["cases"]):
        assert set(case) == set(original) | {"evidence"}
        assert {k: case[k] for k in original if k != "observed"} == {k: v for k, v in original.items() if k != "observed"}
        attempts = [a for a in ATTEMPTS if a[1] == case["name"]]
        cfg = (HERE / "cfg" / f"{case['name']}.cfg").read_text()
        for attempt in attempts:
            log = archive["logs"][attempt[0]]
            verify_log(log, attempt, cfg)
            context = RERUN_CONTEXT if attempt[0] == RERUN_JOB else str((HERE / original["context"]).resolve())
            verify_metadata(log["metadata"], attempt[0], case["name"], context)
        assert case["observed"] == attempts[-1][2] == original["expected"]
        assert case["evidence"] == {"attempts": [a[0] for a in attempts],
                                    "final_job_id": attempts[-1][0], "expected_matches": True}
    assert manifest["observed"] == "ALL_EXPECTED"


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def sha(path):
    assert path.is_file() and not path.is_symlink(), path
    return hashlib.sha256(path.read_bytes()).hexdigest()


def checks(cfg):
    return re.findall(r"^(?:INVARIANT|PROPERTY)\s+(\w+)", cfg, re.M)


def verify_model(generator, source):
    frozen = generator.git_bytes("spec/tla/codex/src/B146R5Cx.tla").decode()
    old_comment = "\\* original v7 abstraction unless the b.w21 scenario is explicitly enabled.\n"
    new_comment = ("\\* original v7 liveness abstraction even in a mixed legacy/current store:\n"
                   "\\* its time fallback conservatively holds through settlement, including ack.\n")
    assert frozen.count(generator.OLD_PREDICATE) == frozen.count(old_comment) == 1
    corrected = frozen.replace(generator.OLD_PREDICATE, generator.NEW_PREDICATE).replace(old_comment, new_comment)
    marker = "\n\\* A fresh request reaches its timeout ack through the original actions;"
    assert source.count(marker) == 1
    original_part, added = source.split(marker)
    assert original_part + "=============================================================================\n" == corrected, "Unexpected canonical-model edits"
    # This comparison preserves every original action and detector byte,
    # including NoRace, KnownHookAlive, HookAlive and all race assignments.
    assert re.findall(r"^.*race' =.*$", source, re.M) == re.findall(r"^.*race' =.*$", frozen, re.M)
    assert "NoRace == ~race\n" in source
    assert "KnownHookAlive == \\E r \\in R : rec[r] /\\ HookAlive(r)\n" in source
    assert 'LiveCheck = "time"  -> h[r] # "idle" /\\ ph[r] < 2' in source
    assert 'B21MixedAckedLiveUnreachable == ~B21MixedAckedLive' in added
    assert 'B21MixedAckedLiveHeld == B21MixedAckedLive => RelayHolds(1) /\\ AnyRelayHolds' in added
    for term in ('PreV7Old /\\ nl /\\ LegacyReleased(L) /\\ ~RelayHolds(L)',
                 'proc = "alive" /\\ rs = "check_permission"',
                 'rec[1] /\\ dec[1] = "deny" /\\ ack[1] /\\ h[1] = "e" /\\ ph[1] = 1',
                 'C(SessionStart)', 'E(TurnStart \\/ HX(1) \\/ StraySend \\/ OrcBlindAct(1))',
                 'P(Ask(1) \\/ Tick(1) \\/ HP(1) \\/ HD(1) \\/ HE(1) \\/ OrcRead(1))',
                 'B21MixedSpec == Init /\\ [][B21MixedNext]_vars'):
        assert term in added, term
    assert "race'" not in added and "rec'" not in added, "Probe must not fabricate a state"


def main():
    generator = load("r2_generator", HERE / "mkcontexts.py")
    frozen_verifier = load("submitted_fixed_verifier", generator.FIXED / "verifycontexts.py")
    frozen_verifier.main()
    for path in (HERE / "mkcontexts.py", HERE / "verifycontexts.py"):
        ast.parse(path.read_text(), filename=str(path))
    manifest = json.loads((HERE / "manifest.json").read_text())
    baseline_bytes = frozen_bytes(generator, "spec/tla/codex/b.w21/fixed-r2/manifest.json")
    assert hashlib.sha256(baseline_bytes).hexdigest() == SUBMITTED_MANIFEST_SHA256
    baseline = json.loads(baseline_bytes)
    verify_receipts(manifest, baseline)
    assert manifest["bug"] == "b.w21" and manifest["round"] == "fixed-r2"
    assert manifest["runtime_identity"] == generator.runtime_identity()
    submitted = manifest["submitted_source"]
    assert submitted["commit"] == generator.SOURCE_COMMIT
    assert submitted["manifest_sha256"] == frozen_verifier.SUBMITTED_MANIFEST_SHA256
    assert submitted["model_sha256"] == generator.sha(generator.git_bytes("spec/tla/codex/src/B146R5Cx.tla"))
    source_path = generator.ROOT / manifest["source_path"]
    assert source_path == generator.CODEX / "src/B146R5Cx.tla"
    assert sha(source_path) == manifest["source_sha256"]
    source = source_path.read_text()
    verify_model(generator, source)
    frozen_verifier.check_fidelity(source)
    diagnosis = manifest["diagnosis"]
    assert diagnosis["layer"] == "model" and diagnosis["runtime_changed"] is False
    assert diagnosis["failed_job"] == "j20261014225806-d7dc0c"
    assert (diagnosis["expected"], diagnosis["observed"], diagnosis["distinct_states"], diagnosis["depth"]) == (
        "PASS", "FAIL(Invariant_NoRace_is_violated)", 3835, 11)
    assert diagnosis["receipt_sha256"] == frozen_verifier.FIXED_RECEIPT_SHA256
    assert diagnosis["trace_sha256"] == frozen_verifier.FAILED_INITIAL_LOG_SHA256
    definitions = set(re.findall(r"^([A-Za-z][A-Za-z0-9_]*)\s*(?:\([^\n]*?\))?\s*==", source, re.M))
    commands = (HERE / "scheduler-commands.txt").read_text().splitlines()
    assert len(manifest["cases"]) == len(generator.CASES) == len(commands) == 6
    total = 0
    assert (HERE / "scheduler-commands.txt").read_bytes() == frozen_bytes(
        generator, "spec/tla/codex/b.w21/fixed-r2/scheduler-commands.txt")
    assert source_path.read_bytes() == frozen_bytes(generator, manifest["source_path"])
    for row, case, command in zip(generator.CASES, manifest["cases"], commands):
        name, original, livecheck, variant, expected, purpose = row
        assert (case["name"], case["origin_case"], case["livecheck"], case["variant"], case["expected"], case["purpose"]) == (
            name, original, livecheck, variant, expected, purpose)
        assert case["spec"] == ("Spec" if original else "B21MixedSpec")
        context = HERE / case["context"]
        assert context == HERE / "jobs" / name and not context.is_symlink()
        assert {p.name for p in context.iterdir()} == set(case["sha256"]) == {
            "B146R5Cx.tla", f"{name}.cfg", "tla2tools.jar", "tlcjob.sh", "runs", "Dockerfile"}
        for filename, expected_hash in case["sha256"].items():
            assert sha(context / filename) == expected_hash, (name, filename)
            assert (context / filename).read_bytes() == frozen_bytes(
                generator, str((context / filename).relative_to(generator.ROOT)))
            total += (context / filename).stat().st_size
        assert (context / "B146R5Cx.tla").read_text() == generator.model_text(variant)
        model = (context / "B146R5Cx.tla").read_text()
        assert re.findall(r"^.*race' =.*$", model, re.M) == re.findall(r"^.*race' =.*$", source, re.M)
        assert "NoRace == ~race\n" in model
        cfgpath = HERE / "cfg" / f"{name}.cfg"
        assert not cfgpath.is_symlink()
        cfg = cfgpath.read_text()
        assert cfg == generator.cfg_text(row) == (context / cfgpath.name).read_text()
        assert re.findall(r"^SPECIFICATION\s+(\w+)", cfg, re.M) == [case["spec"]]
        assert re.findall(r'^\s*LiveCheck = "(\w+)"', cfg, re.M) == [livecheck]
        assert re.findall(r"^\s*(PreV7\w+)\s*<-\s*KnobOn", cfg, re.M) == case["enabled_knobs"]
        for definition in re.findall(r"^(?:SPECIFICATION|INVARIANT|PROPERTY)\s+(\w+)", cfg, re.M):
            assert definition in definitions, (name, definition)
        for definition, replacement in re.findall(r"^\s*(\w+)\s*<-\s*(\w+)", cfg, re.M):
            assert definition in definitions and replacement in definitions, (name, definition)
        if original:
            baseline_cfg = (generator.FIXED / "cfg" / f"{original}.cfg").read_text()
            active = lambda text: [line for line in text.splitlines() if line and not line.startswith("\\*")]
            assert active(cfg) == active(baseline_cfg), "Full obligations or knobs changed"
        elif name.endswith("probe"):
            assert "B21MixedAckedLiveUnreachable" in checks(cfg)
            assert "B21MixedAckedLiveHeld" not in checks(cfg)
        else:
            assert "NoRace" in checks(cfg)
            assert ("B21MixedAckedLiveHeld" in checks(cfg)) == (variant == "corrected")
        for filename, canonical in (("tla2tools.jar", generator.TLA / "tla2tools.jar"),
                                    ("tlcjob.sh", generator.TLA / "jobs/lib/tlcjob.sh")):
            assert sha(context / filename) == sha(canonical)
        assert (context / "tlcjob.sh").stat().st_mode & 0o111
        assert (context / "runs").read_text().splitlines() == [
            f"# b.w21 r2 expected={expected}; observed=NOT_RUN", f"B146R5Cx {name} @700"]
        assert command == generator.command(context, name)
        assert "observed NOT_RUN" in (context / "Dockerfile").read_text()
        print(f"STATIC-OK {name} expected={expected} observed={case['observed']} variant={variant}")
    print(f"STATIC-OK six r2 contexts, {total} bytes; NoRace/actions/time abstraction preserved; production Go unchanged; no model execution.")


if __name__ == "__main__":
    main()
