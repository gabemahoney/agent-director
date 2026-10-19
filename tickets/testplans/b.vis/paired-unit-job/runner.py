import hashlib
from collections import Counter
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import time


FUNCTION = 'TestCursorGuidanceToolRetryDeadlineAndRetirement'
SELECTED = FUNCTION + '/retired'
TESTS = {'github.com/gabemahoney/agent-director/internal/cursorhost': [FUNCTION, SELECTED]}
PACKAGES = ['./internal/cursorhost']
SELECTION = '^' + FUNCTION + '$/^retired$'
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


def processes():
    result = set()
    for path in Path('/proc').iterdir():
        try:
            if path.name.isdigit() and path.stat().st_uid == os.getuid():
                result.add(int(path.name))
        except FileNotFoundError:
            pass
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
               XDG_CONFIG_HOME=str(home / '.config'),
               XDG_DATA_HOME=str(home / '.local/share'),
               GIT_CONFIG_GLOBAL='/dev/null',
               NODE_OPTIONS='--require=/opt/unit/correlations.cjs',
               RETIREMENT_CORRELATIONS=f'/out/{side}-correlations.jsonl')
    for path in (home / '.cache', home / '.config', home / '.local/share', temporary):
        path.mkdir(parents=True, exist_ok=True)
    for ref in (BASELINE, manifest['released_baseline'], 'refs/tags/v0.11.1'):
        subprocess.run(['git', 'cat-file', '-e', ref + '^{commit}'], cwd=cwd, check=True)
    command = ['go', 'test', '-json', '-count=20', '-timeout', '3h',
               '-run', SELECTION, *PACKAGES]
    log = Path('/out') / f'{side}.log'
    before = processes()
    started = time.time()
    print(f'BEGIN {side}: {command}', flush=True)
    with log.open('w') as output:
        process = subprocess.Popen(command, cwd=cwd, env=env, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, text=True,
                                   start_new_session=True)
        for line in process.stdout:
            output.write(line)
            output.flush()
            print(f'{side}: {line}', end='', flush=True)
    code = process.wait()
    # tmux and fake agent grandchildren can outlive go test; remove them before
    # the other side starts. Both sides use their own socket/temp namespace.
    remaining = processes() - before
    for pid in remaining:
        try:
            os.kill(pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
    shutil.rmtree(temporary, ignore_errors=True)
    shutil.rmtree(home, ignore_errors=True)
    assert hashes == {name: hashlib.sha256((Path(cwd) / name).read_bytes()).hexdigest()
                      for name in hashes}
    failures = []
    packages = {}
    disk_full = False
    occurrences = {package: {test: Counter() for test in tests}
                   for package, tests in TESTS.items()}
    unexpected = []
    failure_counts = Counter()
    for line in log.read_text().splitlines():
        disk_full |= 'no space left on device' in line.lower()
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        test = event.get('Test')
        if test:
            counter = occurrences.get(event.get('Package'), {}).get(test)
            if counter is None:
                unexpected.append(event)
            elif event.get('Action') in ('run', 'pass', 'fail', 'skip'):
                counter[event['Action']] += 1
        if event.get('Action') in ('pass', 'fail', 'skip'):
            if event.get('Test') and event['Action'] == 'fail':
                failures.append({'package': event['Package'], 'test': event['Test']})
                failure_counts[(event['Package'], event['Test'])] += 1
            elif not event.get('Test'):
                packages[event['Package']] = event['Action']
    result = dict(command=command, cwd=cwd, exit_code=code, log=str(log),
                  source_hashes=hashes,
                  duration_s=round(time.time() - started, 3), failures=failures,
                  packages=packages, invalid_disk_full=disk_full,
                  cleaned_processes=sorted(remaining), occurrences=occurrences,
                  unexpected_test_events=unexpected,
                  selected_subtest=SELECTED,
                  correlation_log=f'/out/{side}-correlations.jsonl',
                  repetitions_complete=not unexpected and all(
                      counter['run'] == 20
                      and counter['pass'] + counter['fail'] == 20
                      and counter['skip'] == 0
                      for tests in occurrences.values() for counter in tests.values()),
                  failure_counts=[dict(package=p, test=t, count=n)
                                  for (p, t), n in sorted(failure_counts.items())])
    Path(f'/out/{side}-result.json').write_text(json.dumps(result, indent=2) + '\n')
    correlations = Path(f'/out/{side}-correlations.jsonl')
    if correlations.exists():
        raw = correlations.read_bytes()
        result['correlation_records'] = len(raw.splitlines())
        result['correlation_sha256'] = hashlib.sha256(raw).hexdigest()
        for line in raw.decode().splitlines():
            print(f'{side}-correlation: {line}', flush=True)
    Path(f'/out/{side}-result.json').write_text(json.dumps(result, indent=2) + '\n')
    print(f'END {side}: {json.dumps(result)}', flush=True)
    return result


results = {'main': run('main', '/baseline'), 'branch': run('branch', '/work')}
main = {(item['package'], item['test']) for item in results['main']['failures']}
branch = {(item['package'], item['test']) for item in results['branch']['failures']}
results['comparison'] = {
    'reproduced_on_both': sorted(main & branch),
    'main_only': sorted(main - branch),
    'branch_only': sorted(branch - main),
}
results['invalid_disk_full'] = any(results[side]['invalid_disk_full'] for side in ('main', 'branch'))
results['comparison_valid'] = not results['invalid_disk_full'] and all(
    results[side]['repetitions_complete'] for side in ('main', 'branch'))
def leaves(failures):
    return {(package, test) for package, test in failures
            if not any(other_package == package and other.startswith(test + '/')
                       for other_package, other in failures)}


main_leaves, branch_leaves = leaves(main), leaves(branch)
results['leaf_comparison'] = {
    'reproduced_on_both': sorted(main_leaves & branch_leaves),
    'main_only': sorted(main_leaves - branch_leaves),
    'branch_only': sorted(branch_leaves - main_leaves),
}
results['excluded_branch_only'] = []  # The selected retirement subtest is not excluded.
results['counted_branch_only'] = results['leaf_comparison']['branch_only']
Path('/out/results.json').write_text(json.dumps(results, indent=2) + '\n')
print(json.dumps(results, indent=2), flush=True)
sys.exit(1 if not results['comparison_valid'] or any(results[side]['exit_code'] for side in ('main', 'branch')) else 0)
