from pathlib import Path
import runpy
import shutil


root = Path(__file__).resolve().parents[4]
runpy.run_path(str(root / 'tickets/testplans/b.vis/full-unit-job/prepare.py'))
source = root / 'bin/context-full-unit-snapshot'
target = root / 'bin/context-paired-unit-snapshot'
target.mkdir(parents=True, exist_ok=True)
for name in ('source.bundle', 'source.tar.gz', 'snapshot.json'):
    shutil.copy2(source / name, target / name)
for name in ('runner.py', 'correlations.cjs'):
    shutil.copy2(Path(__file__).parent / name, target / name)
dockerfile = (source / 'Dockerfile').read_text()
dockerfile = dockerfile.replace('ENTRYPOINT ["python3", "-u", "/opt/unit/runner.py"]', '''USER root
RUN mkdir -p /baseline /sandbox-tmp/pair-main /sandbox-tmp/pair-branch /sandbox-tmp/main /sandbox-tmp/branch \\
 && chown -R 1000:1000 /baseline /sandbox-tmp
USER sandbox
RUN git clone /opt/unit/source.bundle /baseline
RUN git -C /baseline checkout --detach 98a2283275b43535bb8487e0de05006f8b3a4c68
WORKDIR /baseline
RUN go mod download
RUN go build ./...
WORKDIR /work
COPY correlations.cjs /opt/unit/
ENTRYPOINT ["python3", "-u", "/opt/unit/runner.py"]''')
(target / 'Dockerfile').write_text(dockerfile)
print(target)
