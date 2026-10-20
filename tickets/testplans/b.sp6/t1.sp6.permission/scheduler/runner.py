"""Run main then branch against identical independent acceptance cases."""
import hashlib
import json
import os
from pathlib import Path
import subprocess

root = Path('/opt/permission')
out = Path('/out')
identity = json.loads((root / 'snapshot.json').read_text())
# Main may lack the new cases, so both binaries consume the branch collector.
collector = root / 'branch/tickets/testplans/b.sp6/t1.sp6.permission/permission.cjs'
results = {}
for revision in ('main', 'branch'):
    binary = root / revision / 'bin/agent-director'
    record = {'identity': identity[revision], 'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest(), 'cases': {}}
    for case in ('public', 'representative', 'guards'):
        home = Path('/work') / revision / case
        home.mkdir(parents=True)
        env = dict(os.environ, HOME=str(home), PERMISSION_TEST_BINARY=str(binary))
        # The collector bootstraps an isolated store with this binary's list.
        try:
            run = subprocess.run(['node', str(collector), case], env=env, text=True, capture_output=True, timeout=120)
        except subprocess.TimeoutExpired as error:
            run = subprocess.CompletedProcess(error.cmd, 124, str(error.stdout or ''), str(error.stderr or '') + '\ncollector timed out')
        (out / f'{revision}-{case}.stdout').write_text(run.stdout)
        (out / f'{revision}-{case}.stderr').write_text(run.stderr)
        record['cases'][case] = {'exit': run.returncode, 'result': 'unavailable' if run.returncode == 77 else 'passed' if run.returncode == 0 else 'failed'}
        print(json.dumps({'revision': revision, 'case': case, **record['cases'][case]}), flush=True)
    results[revision] = record
    (out / f'{revision}.json').write_text(json.dumps(record, indent=2) + '\n')
(out / 'paired-results.json').write_text(json.dumps(results, indent=2) + '\n')
# Preserve the baseline even when it fails; availability is distinct from pass.
raise SystemExit(0 if all(case['result'] == 'passed' for case in results['branch']['cases'].values()) else 1)
