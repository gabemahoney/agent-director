from pathlib import Path
import runpy
import shutil


root = Path(__file__).resolve().parents[4]
runpy.run_path(str(Path(__file__).parent / 'prepare.py'))
source = root / 'bin/context-full-unit-snapshot'
target = root / 'bin/context-full-paired-unit-snapshot'
target.mkdir(parents=True, exist_ok=True)
for name in ('source.bundle', 'source.tar.gz', 'snapshot.json'):
    shutil.copy2(source / name, target / name)
shutil.copy2(Path(__file__).parent / 'paired_runner.py', target / 'runner.py')
dockerfile = (source / 'Dockerfile').read_text()
dockerfile = dockerfile.replace(
    'RUN git clone /opt/unit/source.bundle /work',
    'RUN git clone /opt/unit/source.bundle /work\n'
    'RUN git -C /work update-ref refs/heads/main refs/remotes/origin/main')
dockerfile = dockerfile.replace('ENTRYPOINT ["python3", "-u", "/opt/unit/runner.py"]', '''USER root
RUN mkdir -p /baseline /sandbox-tmp/pair-main /sandbox-tmp/pair-branch /sandbox-tmp/main /sandbox-tmp/branch \\
 && chown -R 1000:1000 /baseline /sandbox-tmp
USER sandbox
RUN git clone /opt/unit/source.bundle /baseline
RUN git -C /baseline checkout --detach 98a2283275b43535bb8487e0de05006f8b3a4c68
RUN git -C /baseline update-ref refs/heads/main refs/remotes/origin/main
WORKDIR /baseline
RUN go mod download
RUN go build ./...
WORKDIR /baseline/pkg/ts-bun-client
RUN bun install --frozen-lockfile
RUN bun run build
WORKDIR /work
ENTRYPOINT ["python3", "-u", "/opt/unit/runner.py"]''')
(target / 'Dockerfile').write_text(dockerfile)
print(target)
