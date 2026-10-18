#!/usr/bin/env python3
"""Prepare immutable scheduler inputs; never build, run, or submit jobs."""
import argparse
import gzip
import hashlib
import io
import json
from pathlib import Path, PurePosixPath
import re
import shlex
import subprocess
import tarfile

SOURCE_COMMIT = "9d025f8f2caf176f3a60421f52ba94b293021bdb"
SOURCE_VERSION = "0.0.0-dev"
HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
DEFAULT_CONTEXT = ROOT / "dist/b.w21/integration/context-9d025f8f"
SCHEDULER = "/home/horde/projects/horde_admin/job-scheduler/bin/jobsched"
# All build/source/test/skill inputs, without unrelated tickets, model contexts,
# local agent settings, ignored binaries, caches, credentials or worktree .git.
SOURCE_PATHS = [".github", ".gitignore", "CLAUDE.md", "LICENSE", "Makefile",
                "README.md", "cmd", "docs", "go.mod", "go.sum", "internal",
                "pkg", "skills", "test", "tickets/testplans", "tools"]


def digest(data):
    return hashlib.sha256(data).hexdigest()


def json_bytes(obj):
    return (json.dumps(obj, indent=2, sort_keys=True) + "\n").encode()


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT)


def safe_path(name):
    path = PurePosixPath(name)
    assert name and not path.is_absolute() and ".." not in path.parts, name
    assert ".git" not in path.parts and ".claude" not in path.parts, name
    assert "\n" not in name and "\\" not in name, name


def source_archive():
    raw = git("archive", "--format=tar", SOURCE_COMMIT, *SOURCE_PATHS)
    source = {}
    modes = {}
    with tarfile.open(fileobj=io.BytesIO(raw)) as archive:
        for member in archive.getmembers():
            safe_path(member.name)
            if member.isdir():
                continue
            assert member.isfile(), f"unexpected archive entry: {member.name}"
            assert member.name not in source, member.name
            source[member.name] = archive.extractfile(member).read()
            modes[member.name] = member.mode
    tracked = git("ls-tree", "-r", "--name-only", SOURCE_COMMIT, "--", *SOURCE_PATHS).decode().splitlines()
    assert sorted(source) == sorted(tracked), "archive is not the selected tracked tree"
    compressed = io.BytesIO()
    with gzip.GzipFile(filename="", mode="wb", fileobj=compressed, compresslevel=9, mtime=0) as stream:
        stream.write(raw)
    return compressed.getvalue(), source, modes


def testplan_jobs(source):
    slugs = [line.strip() for line in source["test/docker-epics.txt"].decode().splitlines()
             if line.strip() and not line.lstrip().startswith("#")]
    assert len(slugs) == len(set(slugs)) == 14
    assert all(re.fullmatch(r"[A-Za-z0-9._-]+", slug) for slug in slugs)
    jobs = []
    for slug in slugs:
        matches = [name for name, data in source.items()
                   if name.startswith("tickets/testplans/")
                   and re.fullmatch(r"t1\..*\.md", PurePosixPath(name).name)
                   and re.search(r"^title:.*" + re.escape(slug), data.decode(), re.M | re.I)]
        assert len(matches) == 1, (slug, matches)
        collector = matches[0]
        frontmatter = source[collector].decode().split("---")[1]
        children = re.findall(r"^- (t2[^\n]+)", frontmatter, re.M)
        assert children and len(children) == len(set(children)), collector
        case_files = []
        for child in children:
            name = str(PurePosixPath(collector).parent / child / (child + ".md"))
            body = source[name].decode()
            blocks = re.findall(r"^```bash\n(.*?)^```", body, re.M | re.S)
            assert blocks and any(block.strip() for block in blocks), name
            # None of these selected scripts depends on source Git history.
            assert not re.search(r"(?m)^\s*(?:git\s|[^#\n]*\bgit\s+(?:show|clone|checkout|rev-parse|describe|worktree|archive)\b)", "\n".join(blocks)), name
            case_files.append(name)
        jobs.append({"slug": slug, "kind": "epic", "name": "w21-int-" + slug,
                     "canonical_command": "make test-docker EPIC=" + slug,
                     "runtime_command": ["/opt/driver/run-testplan.sh"],
                     "driver_mode": "shell", "collector": collector,
                     "case_ids": children, "case_files": case_files,
                     "case_count": len(children), "expected": "PASS", "observed": "NOT_RUN"})
    assertions = []
    for mask in ("022", "077", "0777"):
        name = "fresh-umask-" + mask
        assertions.extend([name + "-exit", name + "-permission-denied", name, name + "-admin"])
    assertions.extend(["keep-prior-older-build-exit", "keep-prior-older-build-permission-denied",
                       "keep-prior-upgrade-exit", "keep-prior-upgrade-permission-denied",
                       "keep-prior-canonical", "keep-prior-admin", "keep-prior-snapshot",
                       "keep-prior-admin-snapshot", "0644-source-refused", "0644-source-no-partial-write"])
    assert len(assertions) == 22
    jobs.append({"slug": "test-docker-install-mode", "kind": "install-mode",
                 "name": "w21-int-install-mode", "canonical_command": "make test-docker-install-mode",
                 "runtime_command": ["/opt/install-mode/run.sh"], "driver_mode": "shell",
                 "suite_file": "test/install-mode/run.sh", "scenario_count": 5,
                 "assertion_count": len(assertions), "assertion_names": assertions,
                 "expected": "PASS", "observed": "NOT_RUN"})
    assert all(len(job["name"]) <= 40 for job in jobs)
    return jobs


def prepare(context):
    archive, source, modes = source_archive()
    source_files = [{"path": name, "sha256": digest(data), "size": len(data), "mode": oct(modes[name])}
                    for name, data in sorted(source.items())]
    jobs = testplan_jobs(source)
    canonical = source["test/Dockerfile"].decode()
    marker = "# Pre-built binary + driver assets are produced by the host Makefile.\n"
    assert canonical.count(marker) == 1
    dockerfile = canonical.split(marker)[0] + (HERE / "Dockerfile.tail").read_text()
    payload = {
        "Dockerfile": dockerfile.encode(), "source.tar.gz": archive,
        "SOURCE_COMMIT": (SOURCE_COMMIT + "\n").encode(),
        "SOURCE_VERSION": (SOURCE_VERSION + "\n").encode(),
        "source-files.json": json_bytes({"source_commit": SOURCE_COMMIT, "files": source_files}),
        "source.sha256": "".join(f"{row['sha256']}  {row['path']}\n" for row in source_files).encode(),
        "job-cases.json": json_bytes({"source_commit": SOURCE_COMMIT, "jobs": jobs}),
        "run-job.sh": (HERE / "run-job.sh").read_bytes(),
        "check-result.py": (HERE / "check-result.py").read_bytes(),
    }
    identity = digest(json_bytes({name: digest(data) for name, data in sorted(payload.items())}))
    payload["context-identity.json"] = json_bytes({"source_commit": SOURCE_COMMIT, "payload_sha256": identity})
    commands = []
    context_relative = context.relative_to(ROOT).as_posix()
    for job in jobs:
        env = ["HANDOFF_JOB_SLUG=" + job["slug"], "DRIVER_MODE=shell"]
        if job["kind"] == "epic":
            env.append("EPIC=" + job["slug"])
        argv = [SCHEDULER, "submit", "--context", context_relative, "--name", job["name"],
                "--channel", "C0B3X876XSB", "--network", "on", "--memory", "8g",
                "--cpus", "4", "--timeout", "1h"]
        for item in env:
            argv.extend(["--env", item])
        argv.extend(["--", "/opt/b.w21/run-job.sh"])
        job["submission_command"] = shlex.join(argv)
        job["submission_env"] = env
        commands.append(job["submission_command"])
    manifest = {
        "bee": "b.w21", "source_commit": SOURCE_COMMIT,
        "source_tree": git("rev-parse", SOURCE_COMMIT + "^{tree}").decode().strip(),
        "binary_version": SOURCE_VERSION, "source_paths": SOURCE_PATHS,
        "excluded": [".claude", ".git", "spec", "tickets outside tickets/testplans", "all ignored/untracked files"],
        "source_file_count": len(source_files), "source_uncompressed_file_bytes": sum(row["size"] for row in source_files),
        "source_archive_sha256": digest(archive), "context_relative": context_relative,
        "context_payload_sha256": identity,
        "context_files": [{"path": name, "sha256": digest(data), "size": len(data), "mode": "0o644"}
                          for name, data in sorted(payload.items())],
        "context_total_bytes": sum(map(len, payload.values())),
        "scheduler_limits": {"memory": "8g", "cpus": 4, "timeout": "1h", "network": "on", "channel": "C0B3X876XSB"},
        "epic_count": 14, "job_count": 15,
        "epic_case_count": sum(job["case_count"] for job in jobs if job["kind"] == "epic"),
        "install_mode_scenario_count": 5, "install_mode_assertion_count": 22,
        "integration_result": "could not run: execution reserved to user scheduler; 15 jobs prepared",
        "jobs": jobs,
        "static_limitations": ["No image build, harness execution or scheduler submission has been performed.",
                               "Debian repositories, Node 20 setup, GitHub CLI and base-image tag retain canonical mutable network dependencies.",
                               "Scheduler must provide Linux amd64; the preflight case explicitly expects Linux/x86_64.",
                               "An archive has no source-tree Git HEAD; selected scripts need no Git history. Binary stamps and source checksums are enforced independently."]}
    command_text = "# Run from the worktree root. User submission only. Keep this context immutable until every job finishes.\n"
    command_text += "# First: python3 test/b.w21/integration/verify.py\n"
    command_text += "\n".join(commands) + "\n"
    assert len(commands) == 15 and manifest["context_total_bytes"] < 2 * 1024**3
    return payload, manifest, command_text.encode()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--context", type=Path, default=DEFAULT_CONTEXT)
    parser.add_argument("--handoff-dir", type=Path, default=HERE,
                        help="destination for manifest and command file; must not overwrite different contents")
    args = parser.parse_args()
    context = args.context.resolve()
    assert context.is_relative_to(ROOT), "context must remain inside the worktree"
    if context.exists():
        raise SystemExit(f"refusing to overwrite existing/possibly queued context: {context}")
    payload, manifest, commands = prepare(context)
    handoff = args.handoff_dir.resolve()
    assert handoff.is_relative_to(ROOT), "handoff must remain inside the worktree"
    outputs = {"manifest.json": json_bytes(manifest), "scheduler-commands.txt": commands}
    for name, data in outputs.items():
        path = handoff / name
        if path.exists() and path.read_bytes() != data:
            raise SystemExit(f"refusing to overwrite reviewed handoff: {path}; choose new destinations")
    context.mkdir(parents=True)
    for name, data in payload.items():
        path = context / name
        path.write_bytes(data)
        path.chmod(0o644)
    handoff.mkdir(parents=True, exist_ok=True)
    for name, data in outputs.items():
        path = handoff / name
        if not path.exists():
            path.write_bytes(data)
    print(json.dumps({"context": str(context), "source_commit": SOURCE_COMMIT,
                      "jobs": len(manifest["jobs"]), "bytes": manifest["context_total_bytes"],
                      "payload_sha256": manifest["context_payload_sha256"]}, sort_keys=True))


if __name__ == "__main__":
    main()
