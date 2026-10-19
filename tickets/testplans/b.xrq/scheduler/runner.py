#!/usr/bin/env python3
"""Run both unit suites, or a selected existing driver group in shell mode."""
import json
import os
import pathlib
import shutil
import subprocess
import sys

root = pathlib.Path("/work/source")
out = pathlib.Path("/out")
os.environ["PATH"] = str(root / "bin") + ":" + os.environ["PATH"]
shutil.copyfile("/opt/snapshot.json", out / "snapshot.json")

def run(name, command, cwd=root):
    print("RUN", name, command, flush=True)
    with (out / (name + ".log")).open("w") as log:
        process = subprocess.Popen(command, cwd=cwd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        for line in process.stdout:
            log.write(line)
            log.flush()
            print(line, end="", flush=True)
        code = process.wait()
    print("RESULT", name, code, flush=True)
    return code

if len(sys.argv) > 1 and sys.argv[1] == "integration":
    os.environ["AGENT_DIRECTOR_TEST_HARNESS"] = "1"
    os.environ["DRIVER_MODE"] = "shell"
    os.environ["TESTPLAN_ROOT"] = str(root / "tickets/testplans")
    results = {}
    for slug in sys.argv[2:]:
        os.environ["EPIC"] = slug
        results[slug] = run(slug, ["bash", "/opt/driver/run-testplan.sh"])
else:
    results = {"go": run("go", ["go", "test", "-json", "-count=1", "-timeout", "30m", "./..."])}
    results["bun"] = run("bun", ["bun", "test"], root / "pkg/ts-bun-client")
    results["typecheck"] = run("typecheck", ["bun", "run", "typecheck"], root / "pkg/ts-bun-client")
(out / "results.json").write_text(json.dumps(results, indent=2) + "\n")
sys.exit(1 if any(results.values()) else 0)
