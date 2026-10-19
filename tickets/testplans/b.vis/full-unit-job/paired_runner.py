import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import time
from collections import Counter


BASELINE = '98a2283275b43535bb8487e0de05006f8b3a4c68'
STRICT_TEST = 'internal/cursorhost/session_guidance_tools_linux_test.go'
FIXTURE = 'internal/testsupport/cursorfix/runtime.js'
STRICT_HASH = '7759ba927ca52fa706c553564cc4872c1d7fbdfe59a6b87f9edea4bb4a346ad5'
FIXTURE_HASHES = {
    'main': 'ecd731dad2b0ba99ccb4875fcb293116ade3061724d997cef9e85940ef4ddd89',
    'branch': '51313bb0b1fa3ae7d8982645b8266f899d24e77d5a3038fadd5245663e4d9c95',
}
manifest = json.loads(Path('/opt/unit/snapshot.json').read_text())
for entry in manifest['files']:
    path = Path('/work') / entry['path']
    content = os.readlink(path).encode() if path.is_symlink() else path.read_bytes()
    if hashlib.sha256(content).hexdigest() != entry['sha256']:
        sys.exit(f'Snapshot content mismatch: {path}')
Path('/out/snapshot.json').write_text(json.dumps(manifest, indent=2) + '\n')
print(json.dumps({'verified_snapshot_files': len(manifest['files']),
                  'source_archive_sha256': manifest['source_archive_sha256'], 'base_head': manifest['head'], 'cpu_max': Path('/sys/fs/cgroup/cpu.max').read_text().strip(),
                  'memory_max': Path('/sys/fs/cgroup/memory.max').read_text().strip()}), flush=True)


def processes():
    result = set()
    for path in Path('/proc').iterdir():
        try:
            if path.name.isdigit() and path.stat().st_uid == os.getuid():
                result.add(int(path.name))
        except FileNotFoundError:
            pass
    return result


def run_suite(side, suite, command, cwd, env):
    before = processes()
    started = time.time()
    log = Path('/out') / f'{side}-{suite}.log'
    print(f'BEGIN {side}-{suite}: {command}', flush=True)
    with log.open('w') as output:
        process = subprocess.Popen(command, cwd=cwd, env=env, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, text=True, start_new_session=True)
        for line in process.stdout:
            output.write(line)
            output.flush()
            print(f'{side}: {line}', end='', flush=True)
        code = process.wait()
    remaining = processes() - before
    for pid in remaining:
        try:
            os.kill(pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
    raw = log.read_text()
    result = dict(command=command, cwd=cwd, exit_code=code, log=str(log),
                  started_at=time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime(started)),
                  duration_s=round(time.time() - started, 3), cleaned_processes=sorted(remaining),
                  invalid_disk_full='no space left on device' in raw.lower())
    if suite == 'go':
        packages = {}
        occurrences = {}
        failures = []
        for line in raw.splitlines():
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue
            package, test, action = event.get('Package'), event.get('Test'), event.get('Action')
            if test and action in ('run', 'pass', 'fail', 'skip'):
                occurrences.setdefault((package, test), Counter())[action] += 1
            if action in ('pass', 'fail', 'skip'):
                if not test:
                    packages[package] = action
                elif action == 'fail':
                    failures.append(dict(package=package, test=test))
        result.update(packages=packages, failures=failures,
                      test_occurrences=[dict(package=p, test=t, **dict(c))
                                        for (p, t), c in sorted(occurrences.items())],
                      incomplete_tests=[dict(package=p, test=t, **dict(c))
                                        for (p, t), c in sorted(occurrences.items())
                                        if c['run'] != c['pass'] + c['fail'] + c['skip']],
                      protocol_abort=bool(re.search(r'panic: test timed out|fatal error:', raw)))
    else:
        result.update(pass_counts=[int(n) for n in re.findall(r'^\s*(\d+) pass\s*$', raw, re.M)],
                      fail_counts=[int(n) for n in re.findall(r'^\s*(\d+) fail\s*$', raw, re.M)],
                      failed_tests=re.findall(r'^\(fail\) (.+)$', raw, re.M))
    print(f'END {side}-{suite}: {json.dumps(result)}', flush=True)
    return result


def run(side, cwd):
    hashes = {name: hashlib.sha256((Path(cwd) / name).read_bytes()).hexdigest()
              for name in (STRICT_TEST, FIXTURE)}
    assert hashes == {STRICT_TEST: STRICT_HASH, FIXTURE: FIXTURE_HASHES[side]}, hashes
    env = os.environ.copy()
    home = Path('/sandbox-tmp') / f'pair-{side}'
    temporary = Path('/sandbox-tmp') / side
    env.update(HOME=str(home), TMPDIR=str(temporary), BUN_TMPDIR=str(temporary),
               TMUX_TMPDIR=str(temporary), XDG_CACHE_HOME=str(home / '.cache'),
               XDG_CONFIG_HOME=str(home / '.config'), XDG_DATA_HOME=str(home / '.local/share'),
               GIT_CONFIG_GLOBAL='/dev/null')
    for path in (home / '.cache', home / '.config', home / '.local/share', temporary):
        path.mkdir(parents=True, exist_ok=True)
    for ref in (BASELINE, manifest['released_baseline'], 'refs/tags/v0.11.1', 'refs/heads/main'):
        subprocess.run(['git', 'cat-file', '-e', ref + '^{commit}'], cwd=cwd, check=True)
    head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=cwd, text=True).strip()
    if side == 'main' and head != BASELINE:
        sys.exit(f'Incorrect main revision: {head}')
    expected = subprocess.check_output(['go', 'list', './...'], cwd=cwd, env=env, text=True).splitlines()
    started = time.time()
    go = run_suite(side, 'go', ['go', 'test', '-json', '-count=1', '-timeout', '30m', './...'], cwd, env)
    bun = run_suite(side, 'bun', ['bun', 'test'], cwd + '/pkg/ts-bun-client', env)
    assert hashes == {name: hashlib.sha256((Path(cwd) / name).read_bytes()).hexdigest()
                      for name in hashes}
    shutil.rmtree(temporary, ignore_errors=True)
    shutil.rmtree(home, ignore_errors=True)
    result = dict(cwd=cwd, head=head, go=go, bun=bun, source_hashes=hashes,
                  duration_s=round(time.time() - started, 3),
                  missing_packages=sorted(set(expected) - set(go['packages'])),
                  unexpected_packages=sorted(set(go['packages']) - set(expected)))
    result['suites_complete'] = (not result['missing_packages'] and not result['unexpected_packages']
                                 and not go['incomplete_tests'] and not go['protocol_abort']
                                 and bool(bun['pass_counts']) and bool(bun['fail_counts']))
    result['invalid_disk_full'] = go['invalid_disk_full'] or bun['invalid_disk_full']
    Path(f'/out/{side}-result.json').write_text(json.dumps(result, indent=2) + '\n')
    print(f'END {side}: {json.dumps(result)}', flush=True)
    return result


results = {'main': run('main', '/baseline'), 'branch': run('branch', '/work')}
results['comparison_valid'] = all(results[side]['suites_complete'] and not results[side]['invalid_disk_full']
                                  for side in ('main', 'branch'))
Path('/out/results.json').write_text(json.dumps(results, indent=2) + '\n')
print(json.dumps(results, indent=2), flush=True)
sys.exit(1 if not results['comparison_valid'] or any(results[side][suite]['exit_code']
         for side in ('main', 'branch') for suite in ('go', 'bun')) else 0)
