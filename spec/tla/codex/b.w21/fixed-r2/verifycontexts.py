#!/usr/bin/env python3
"""Verify r2 hashes and model fidelity without parsing or running the model."""

import ast
import hashlib
import importlib.util
import json
from pathlib import Path
import re
import sys


sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent


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
    assert manifest["bug"] == "b.w21" and manifest["round"] == "fixed-r2"
    assert manifest["observed"] == "NOT_RUN"
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
    for row, case, command in zip(generator.CASES, manifest["cases"], commands):
        name, original, livecheck, variant, expected, purpose = row
        assert (case["name"], case["origin_case"], case["livecheck"], case["variant"], case["expected"], case["purpose"]) == (
            name, original, livecheck, variant, expected, purpose)
        assert case["observed"] == "NOT_RUN"
        assert case["spec"] == ("Spec" if original else "B21MixedSpec")
        context = HERE / case["context"]
        assert context == HERE / "jobs" / name and not context.is_symlink()
        assert {p.name for p in context.iterdir()} == set(case["sha256"]) == {
            "B146R5Cx.tla", f"{name}.cfg", "tla2tools.jar", "tlcjob.sh", "runs", "Dockerfile"}
        for filename, expected_hash in case["sha256"].items():
            assert sha(context / filename) == expected_hash, (name, filename)
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
        print(f"STATIC-OK {name} expected={expected} observed=NOT_RUN variant={variant}")
    print(f"STATIC-OK six r2 contexts, {total} bytes; NoRace/actions/time abstraction preserved; production Go unchanged; no model execution.")


if __name__ == "__main__":
    main()
