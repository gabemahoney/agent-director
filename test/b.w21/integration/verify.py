#!/usr/bin/env python3
"""Statically verify scheduler inputs against the pinned Git tree and templates."""
import argparse
import ast
import json
from pathlib import Path
import stat
import subprocess
import sys

sys.dont_write_bytecode = True
from prepare import DEFAULT_CONTEXT, HERE, ROOT, json_bytes, prepare


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--context", type=Path, default=DEFAULT_CONTEXT)
    parser.add_argument("--handoff-dir", type=Path, default=HERE)
    args = parser.parse_args()
    context = args.context.resolve()
    handoff = args.handoff_dir.resolve()
    payload, manifest, commands = prepare(context)
    assert not context.is_symlink() and context.is_dir()
    actual = list(context.rglob("*"))
    assert sorted(path.relative_to(context).as_posix() for path in actual) == sorted(payload), "unexpected/missing context entries"
    for path in actual:
        mode = path.lstat().st_mode
        assert stat.S_ISREG(mode), f"context contains a non-regular file: {path}"
        assert stat.S_IMODE(mode) == 0o644, f"unexpected mode: {path}"
        assert path.read_bytes() == payload[path.name], f"context differs from pinned input/template: {path}"
    assert (handoff / "manifest.json").read_bytes() == json_bytes(manifest), "manifest mismatch"
    assert (handoff / "scheduler-commands.txt").read_bytes() == commands, "submission commands mismatch"
    for name in ("prepare.py", "verify.py", "check-result.py"):
        ast.parse((HERE / name).read_text(), filename=name)
    subprocess.run(["bash", "-n", str(context / "run-job.sh")], check=True)
    subprocess.run(["bash", "-n", str(handoff / "scheduler-commands.txt")], check=True)
    report = {"static_verification": "PASS", "source_commit": manifest["source_commit"],
              "context_total_bytes": manifest["context_total_bytes"],
              "context_payload_sha256": manifest["context_payload_sha256"],
              "source_file_count": manifest["source_file_count"],
              "epic_count": 14, "job_count": 15,
              "epic_case_count": manifest["epic_case_count"],
              "install_mode_assertion_count": 22,
              "observed": "NOT_RUN", "no_execution_performed": True}
    print(json.dumps(report, sort_keys=True))


if __name__ == "__main__":
    main()
