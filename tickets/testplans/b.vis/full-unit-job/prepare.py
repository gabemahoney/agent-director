import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tarfile


BASELINE = '67a0c703b14224268cf094410d12e7df9b63a652'
root = Path(__file__).resolve().parents[4]
for revision in (BASELINE, 'ccb41e075695609b084292f52c36512f3665d210', 'refs/tags/v0.11.1'):
    subprocess.run(['git', 'cat-file', '-e', revision + '^{commit}'], cwd=root, check=True)
context = root / 'bin/context-full-unit-snapshot'
context.mkdir(parents=True, exist_ok=True)
for name in ('Dockerfile', 'runner.py'):
    shutil.copy2(Path(__file__).parent / name, context / name)
files = subprocess.check_output(
    ['git', 'ls-files', '-co', '--exclude-standard', '-z'], cwd=root,
).decode().split('\0')
entries = []
with tarfile.open(context / 'source.tar.gz', 'w:gz') as archive:
    for name in sorted(set(files)):
        if not name or name == '.git':
            continue
        path = root / name
        if not path.exists() and not path.is_symlink():
            continue
        content = path.readlink().as_posix().encode() if path.is_symlink() else path.read_bytes()
        entries.append({'path': name, 'sha256': hashlib.sha256(content).hexdigest()})
        archive.add(path, arcname=name, recursive=False)
manifest = {
    'head': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip(),
    'comparison_baseline': BASELINE,
    'tracked_tree_clean': subprocess.run(['git', 'diff', '--quiet', 'HEAD', '--'], cwd=root).returncode == 0,
    'released_baseline': 'ccb41e075695609b084292f52c36512f3665d210',
    'files': entries,
    'source_archive_sha256': hashlib.sha256((context / 'source.tar.gz').read_bytes()).hexdigest(),
}
(context / 'snapshot.json').write_text(json.dumps(manifest, indent=2) + '\n')
subprocess.run(['git', 'bundle', 'create', str(context / 'source.bundle'), '--all'],
               cwd=root, check=True)
subprocess.run(['git', 'bundle', 'verify', str(context / 'source.bundle')],
               cwd=root, check=True, stdout=subprocess.DEVNULL)
print(json.dumps({'context': str(context), 'files': len(entries),
                  'source_archive_sha256': manifest['source_archive_sha256']}))
