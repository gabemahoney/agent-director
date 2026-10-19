#!/usr/bin/env python3
"""Build a scheduler context from the worktree without creating a commit."""
import datetime
import hashlib
import json
import pathlib
import shutil
import subprocess
import tarfile

root = pathlib.Path(__file__).resolve().parents[4]
stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%S%fZ")
context = root / "tickets" / "b.xrq" / ("unit-job-" + stamp)
context.mkdir(parents=True, exist_ok=False)
subprocess.run(["git", "bundle", "create", str(context / "source.bundle"), "--all"], cwd=root, check=True)
paths = subprocess.check_output(["git", "diff", "--name-only", "-z", "HEAD"], cwd=root).split(b"\0")
paths += subprocess.check_output(["git", "ls-files", "-z", "--others", "--exclude-standard"], cwd=root).split(b"\0")
files = {}
with tarfile.open(context / "snapshot.tar.gz", "w:gz") as archive:
    for item in sorted(set(paths)):
        if not item:
            continue
        relative = item.decode()
        source = root / relative
        if source.is_file():
            archive.add(source, arcname=relative, recursive=False)
            files[relative] = hashlib.sha256(source.read_bytes()).hexdigest()
head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
(context / "snapshot.json").write_text(json.dumps({
    "created_at": stamp, "base_revision": head, "files": files,
    "snapshot_sha256": hashlib.sha256((context / "snapshot.tar.gz").read_bytes()).hexdigest(),
}, indent=2) + "\n")
base = (root / "test/sandbox/Dockerfile").read_text()
extra = f'''
USER root
RUN apt-get update && apt-get install -y --no-install-recommends python3 tini
RUN mkdir -p /work/source /out /go/pkg/mod /sandbox-tmp /opt/driver /opt/bin \\
 && chown -R 1000:1000 /work /out /go /sandbox-tmp /home/sandbox /opt/driver /opt/bin \\
 && chmod -R u+rwX /work /out /go /sandbox-tmp /home/sandbox \\
 && chmod 0700 /sandbox-tmp
COPY source.bundle /opt/source.bundle
COPY snapshot.tar.gz /opt/snapshot.tar.gz
COPY runner.py /opt/runner.py
COPY snapshot.json /opt/snapshot.json
USER sandbox
ENV AGENT_DIRECTOR_TEST_SANDBOX=1 TMPDIR=/sandbox-tmp BUN_TMPDIR=/sandbox-tmp \\
 XDG_CACHE_HOME=/home/sandbox/.cache XDG_CONFIG_HOME=/home/sandbox/.config \\
 XDG_DATA_HOME=/home/sandbox/.local/share GOTOOLCHAIN=local
RUN git clone /opt/source.bundle /work/source
WORKDIR /work/source
RUN git checkout --detach {head}
RUN tar xzf /opt/snapshot.tar.gz
RUN go mod download
WORKDIR /work/source/pkg/ts-bun-client
RUN bun install --frozen-lockfile
RUN bun run build
WORKDIR /work/source
RUN make build ts-helper fake-tmux
RUN go build ./...
RUN cp -R test/driver/. /opt/driver/
RUN cp bin/agent-director-admin /opt/bin/agent-director-admin
ENV GOFLAGS=-mod=mod GOPROXY=off GOSUMDB=off GONOPROXY=none
# Match a normal host's orphan reaping inside the scheduler's PID namespace.
ENTRYPOINT ["/usr/bin/tini", "--", "python3", "-u", "/opt/runner.py"]
CMD ["unit"]
'''
(context / "Dockerfile").write_text(base + extra)
shutil.copyfile(pathlib.Path(__file__).with_name("runner.py"), context / "runner.py")
print(context)
print("base revision:", head)
print("snapshot includes tracked edits and nonignored new files")
