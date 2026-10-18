#!/usr/bin/env python3
"""Reject zero-case, missing-case, or unsuccessful harness receipts."""
import json
from pathlib import Path
import re
import sys


def check(job, text, exitcode):
    errors = []
    if exitcode != 0:
        errors.append(f"harness exit={exitcode}")
    if job["kind"] == "epic":
        objects = []
        for line in text.splitlines():
            try:
                obj = json.loads(line)
            except ValueError:
                continue
            if isinstance(obj, dict):
                objects.append(obj)
        cases = [obj for obj in objects if "case" in obj]
        summaries = [obj["summary"] for obj in objects if "summary" in obj]
        if [obj["case"] for obj in cases] != job["case_ids"]:
            errors.append("case IDs/order differ from the pinned testplan")
        if any(obj.get("status") != "pass" for obj in cases):
            errors.append("one or more cases did not pass")
        expected = {"total": job["case_count"], "pass": job["case_count"], "fail": 0}
        if summaries != [expected]:
            errors.append(f"expected exactly one summary {expected}; got {summaries}")
        observed = len(cases)
    else:
        names = re.findall(r"^  PASS  (\S+)\s", text, re.M)
        if names != job["assertion_names"]:
            errors.append("install-mode assertion names/order differ from the pinned suite")
        summary = re.findall(r"^\[b\.r3j install-mode\] summary: (\d+) passed, (\d+) failed$", text, re.M)
        if summary != [(str(job["assertion_count"]), "0")]:
            errors.append(f"unexpected install-mode summary: {summary}")
        if re.search(r"^\s*FAIL\b", text, re.M):
            errors.append("install-mode reported FAIL")
        observed = len(names)
    return errors, observed


def main():
    manifest_path, slug, log_path, raw_exit = sys.argv[1:]
    manifest = json.loads(Path(manifest_path).read_text())
    job = next(job for job in manifest["jobs"] if job["slug"] == slug)
    errors, observed = check(job, Path(log_path).read_text(errors="replace"), int(raw_exit))
    context = json.loads((Path(manifest_path).parent / "context-identity.json").read_text())
    receipt = {"job": slug, "source_commit": manifest["source_commit"],
               "context_payload_sha256": context["payload_sha256"],
               "observed_count": observed, "harness_exitcode": int(raw_exit),
               "verdict": "FAIL" if errors else "PASS", "errors": errors}
    print("HANDOFF-RECEIPT " + json.dumps(receipt, sort_keys=True))
    print("VERDICT " + receipt["verdict"])
    print("JOB-VERDICT " + receipt["verdict"])
    return int(bool(errors))


if __name__ == "__main__":
    sys.exit(main())
