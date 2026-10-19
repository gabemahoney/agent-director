from pathlib import Path
import runpy
import shutil
import subprocess


BASELINE = '67a0c703b14224268cf094410d12e7df9b63a652'
root = Path(__file__).resolve().parents[4]
# This gate compares committed revisions; the parent must finish the merge first.
subprocess.run(['git', 'diff', '--quiet', 'HEAD', '--'], cwd=root, check=True)
subprocess.run(['git', 'merge-base', '--is-ancestor', BASELINE, 'HEAD'], cwd=root, check=True)
runpy.run_path(str(Path(__file__).parent / 'prepare.py'))
source = root / 'bin/context-full-unit-snapshot'
target = root / 'bin/context-full-paired-unit-snapshot'
target.mkdir(parents=True, exist_ok=True)
for name in ('source.bundle', 'source.tar.gz', 'snapshot.json'):
    shutil.copy2(source / name, target / name)
shutil.copy2(Path(__file__).parent / 'paired_runner.py', target / 'runner.py')
dockerfile = (source / 'Dockerfile').read_text()
# Delete any overlay build output before preparing fresh binaries and declarations.
dockerfile = dockerfile.replace(
    'RUN go build ./...',
    "RUN python3 -c \"import pathlib,shutil; p=pathlib.Path('/work'); shutil.rmtree(p/'pkg/ts-bun-client/dist',ignore_errors=True); [(p/'bin'/n).unlink(missing_ok=True) for n in ('agent-director','agent-director-admin')]\"\nRUN make build\nRUN go build ./...")
dockerfile = dockerfile.replace('ENTRYPOINT ["python3", "-u", "/opt/unit/runner.py"]', f'''USER root
RUN mkdir -p /baseline /sandbox-tmp/pair-main /sandbox-tmp/pair-branch /sandbox-tmp/main /sandbox-tmp/branch \\
 && chown -R 1000:1000 /baseline /sandbox-tmp
USER sandbox
RUN git clone /opt/unit/source.bundle /baseline
RUN git -C /baseline checkout --detach {BASELINE}
WORKDIR /baseline
RUN go mod download
RUN make build
RUN go build ./...
WORKDIR /baseline/pkg/ts-bun-client
RUN bun install --frozen-lockfile
RUN bun run build
WORKDIR /work
RUN python3 /opt/unit/runner.py --prepare-artifacts
ENTRYPOINT ["python3", "-u", "/opt/unit/runner.py"]''')
(target / 'Dockerfile').write_text(dockerfile)
print(target)
