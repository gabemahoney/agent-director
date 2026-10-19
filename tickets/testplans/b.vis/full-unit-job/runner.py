import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time


def run(name, command, cwd):
    started = time.time()
    log = Path('/out') / f'{name}.log'
    print(f'BEGIN {name}: {command}', flush=True)
    with log.open('w') as output:
        process = subprocess.Popen(command, cwd=cwd, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, text=True)
        for line in process.stdout:
            output.write(line)
            output.flush()
            print(line, end='', flush=True)
        code = process.wait()
    result = {'command': command, 'exit_code': code, 'log': str(log),
              'duration_s': round(time.time() - started, 3)}
    print(f'END {name}: {json.dumps(result)}', flush=True)
    return result


manifest = json.loads(Path('/opt/unit/snapshot.json').read_text())
for entry in manifest['files']:
    path = Path('/work') / entry['path']
    content = os.readlink(path).encode() if path.is_symlink() else path.read_bytes()
    if hashlib.sha256(content).hexdigest() != entry['sha256']:
        sys.exit(f'Snapshot content mismatch: {path}')
Path('/out/snapshot.json').write_text(json.dumps(manifest, indent=2) + '\n')
print(f"Verified {len(manifest['files'])} snapshot files; base HEAD {manifest['head']}", flush=True)
# The installer recovery matrix must build the actual released v9 binary.
subprocess.run(['git', 'cat-file', '-e', manifest['released_baseline'] + '^{commit}'],
               cwd='/work', check=True)
subprocess.run(['git', 'cat-file', '-e', 'refs/tags/v0.11.1^{commit}'],
               cwd='/work', check=True)
results = {
    'go': run('go', ['go', 'test', '-json', '-count=1', '-timeout', '30m', './...'], '/work'),
    'bun': run('bun', ['bun', 'test'], '/work/pkg/ts-bun-client'),
}
disk_full = any('no space left on device' in Path(result['log']).read_text().lower()
                for result in results.values())
results['invalid_disk_full'] = disk_full
Path('/out/results.json').write_text(json.dumps(results, indent=2) + '\n')
print(json.dumps(results, indent=2), flush=True)
sys.exit(1 if disk_full or any(results[name]['exit_code'] for name in ('go', 'bun')) else 0)
