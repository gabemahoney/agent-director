import argparse
from collections import Counter, defaultdict, deque
import json
from pathlib import Path
import re


parser = argparse.ArgumentParser()
parser.add_argument('job_id')
parser.add_argument('--paired', action='store_true')
args = parser.parse_args()
directory = Path('/home/horde/.job-scheduler/jobs') / args.job_id
run = (directory / 'run.log').read_text()
build = (directory / 'build.log').read_text()
sides = ('main', 'branch') if args.paired else ('go',)
failures = defaultdict(set)
packages = defaultdict(dict)
outputs = defaultdict(lambda: deque(maxlen=30))
signals = defaultdict(list)
counts = defaultdict(Counter)
streams = defaultdict(list)
metadata = {}
occurrences = defaultdict(Counter)
failed_occurrences = defaultdict(list)
for line in run.splitlines():
    for name in sides:
        prefix = f'END {name}: '
        if line.startswith(prefix):
            summary = json.loads(line[len(prefix):])
            metadata[name] = summary
    side = 'go'
    if args.paired:
        prefix, separator, line = line.partition(': ')
        if not separator or prefix not in sides:
            continue
        side = prefix
        streams[side].append(line)
    try:
        event = json.loads(line)
    except json.JSONDecodeError:
        continue
    if not isinstance(event, dict) or 'Action' not in event:
        continue
    key = (side, event.get('Package'), event.get('Test'))
    if event.get('Output'):
        outputs[key].append(event['Output'].rstrip())
        if re.search(r'\w+\.go:\d+:|DRY RUN CHECK FAILED|\bFAIL:', event['Output']):
            signals[key].append(event['Output'].rstrip())
    action = event['Action']
    if event.get('Test') and action in ('run', 'pass', 'fail', 'skip'):
        occurrences[key][action] += 1
        if action == 'run':
            outputs[key].clear()
            signals[key].clear()
    if action not in ('pass', 'fail', 'skip'):
        continue
    if event.get('Test'):
        counts[side][action] += 1
        if action == 'fail':
            failures[side].add((event['Package'], event['Test']))
            failed_occurrences[key].append({
                'run': occurrences[key]['run'],
                'elapsed_s': event.get('Elapsed'),
                'diagnostics': list(dict.fromkeys(signals[key] + list(outputs[key]))),
            })
    else:
        packages[side][event['Package']] = action

result = {'job_id': args.job_id,
          'invalid_disk_full': 'no space left on device' in (build + run).lower(),
          'run_log': str(directory / 'run.log')}
for side in sides:
    failed = failures[side]
    leaves = [(package, test) for package, test in sorted(failed)
              if not any(p == package and t.startswith(test + '/') for p, t in failed)]
    result[side] = {
        'run': metadata.get(side),
        'package_counts': dict(Counter(packages[side].values())),
        'package_results': packages[side],
        'test_events_including_parents': dict(counts[side]),
        'top_level_occurrences': [dict(package=p, test=t, **dict(c))
                                  for (s, p, t), c in sorted(occurrences.items())
                                  if s == side and '/' not in t],
        'failed_leaf_tests': [dict(package=p, test=t, diagnostics=list(dict.fromkeys(
            diagnostic for occurrence in failed_occurrences[(side, p, t)]
            for diagnostic in occurrence['diagnostics'])),
            failed_count=occurrences[(side, p, t)]['fail'],
            run_count=occurrences[(side, p, t)]['run'],
            failed_occurrences=failed_occurrences[(side, p, t)])
                              for p, t in leaves],
    }
if args.paired:
    for side in sides:
        side_text = '\n'.join(streams[side])
        result[side]['bun'] = {
            'passed': [int(x) for x in re.findall(r'^\s*(\d+) pass\s*$', side_text, re.M)],
            'failed': [int(x) for x in re.findall(r'^\s*(\d+) fail\s*$', side_text, re.M)],
            'failed_tests': re.findall(r'^\(fail\) (.+)$', side_text, re.M),
        }
    result['paired_full'] = all('go' in metadata.get(side, {}) for side in sides)
    main = failures['main']
    branch = failures['branch']
    result['complete_sides'] = [side for side in sides if f'END {side}:' in run]
    result['comparison_valid'] = len(result['complete_sides']) == 2 and not result['invalid_disk_full']
    result['harness_failure'] = 'Traceback (most recent call last):' in run
    result['comparison_valid'] &= not result['harness_failure']
    if result['paired_full']:
        result['comparison_valid'] &= all(metadata[side]['suites_complete'] for side in sides)
        main_bun, branch_bun = (set(result[side]['bun']['failed_tests']) for side in sides)
        result['bun_comparison'] = {
            'reproduced_on_both': sorted(main_bun & branch_bun),
            'main_only': sorted(main_bun - branch_bun),
            'branch_only': sorted(branch_bun - main_bun),
        }
    if any('-count=20' in (metadata.get(side, {}).get('command') or []) for side in sides):
        result['repetitions_complete'] = all(metadata.get(side, {}).get('repetitions_complete')
                                             for side in sides)
        result['comparison_valid'] &= result['repetitions_complete']
    result['comparison'] = {
        'reproduced_on_both': sorted(main & branch),
        'main_only': sorted(main - branch),
        'branch_only': sorted(branch - main),
    } if result['comparison_valid'] else None
    if result['comparison_valid']:
        result['comparison']['branch_only_leaf_tests'] = [
            [item['package'], item['test']]
            for item in result['branch']['failed_leaf_tests']
            if (item['package'], item['test']) not in main]
        result['comparison']['main_only_leaf_tests'] = [
            [item['package'], item['test']]
            for item in result['main']['failed_leaf_tests']
            if (item['package'], item['test']) not in branch]
        result['comparison']['shared_leaf_tests'] = [
            [item['package'], item['test']]
            for item in result['branch']['failed_leaf_tests']
            if (item['package'], item['test']) in main]
else:
    result['bun'] = {
        'passed': [int(x) for x in re.findall(r'^\s*(\d+) pass\s*$', run, re.M)],
        'failed': [int(x) for x in re.findall(r'^\s*(\d+) fail\s*$', run, re.M)],
        'failed_tests': re.findall(r'^\(fail\) (.+)$', run, re.M),
    }
root = Path(__file__).resolve().parents[4]
if args.paired:
    result['side_logs'] = {}
    for side in sides:
        path = root / 'bin' / f'paired-{args.job_id}-{side}.log'
        path.write_text('\n'.join(streams[side]) + '\n')
        result['side_logs'][side] = str(path)
destination = root / 'bin' / f'{"paired" if args.paired else "full-unit"}-audit-{args.job_id}.json'
destination.write_text(json.dumps(result, indent=2) + '\n')
print(json.dumps(result, indent=2))
print(destination)
