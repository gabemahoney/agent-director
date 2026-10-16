import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync, cpSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';

const root = process.cwd();
const evidence = join(root, 'tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/deadline-red');
const revision = '64a35baf37a1c44eb576ea661f3c30b13708f6fa';
const test = 'pkg/api/decide_trail_deadline_test.go';
const hash = value => createHash('sha256').update(value).digest('hex');
function run(command, args, cwd = root) {
  const result = spawnSync(command, args, { cwd, encoding: null, maxBuffer: 256*1024*1024 });
  if (result.status !== 0) throw new Error(`${command} ${args.join(' ')}: ${result.status}: ${result.stderr}`);
  return result.stdout;
}
const listing = run('git', ['ls-tree', '-r', '--name-only', '-z', revision]).toString().split('\0').filter(Boolean);
// Exclude forbidden paths before asking git to read any blobs.
const paths = listing.filter(p => p !== '.claude' && !p.startsWith('.claude/') && !p.startsWith('tickets/'));
const copy = mkdtempSync(join(tmpdir(), 'deadline-old-'));
const receipt = { revision, overlay: test, excluded_before_blob_access: ['.claude/**', 'tickets/**'], timeout: '60s outer / 45s go test', production_manifest: {} };
try {
  const archive = run('git', ['archive', '--format=tar', revision, '--', ...paths]);
  const unpack = spawnSync('tar', ['xf', '-', '-C', copy], { input: archive, encoding: null });
  if (unpack.status !== 0) throw new Error(`tar: ${unpack.stderr}`);
  for (const path of paths.filter(p => !p.endsWith('_test.go'))) {
    receipt.production_manifest[path] = hash(readFileSync(join(copy, path)));
  }
  receipt.production_manifest_sha256 = hash(JSON.stringify(receipt.production_manifest));
  cpSync(join(root, test), join(copy, test));
  receipt.test_sha256 = hash(readFileSync(join(copy, test)));
  receipt.argv = ['timeout', '60s', 'go', 'test', '-timeout', '45s', '-count=1', '-v', './pkg/api', '-run', '^TestDecideMaxWaitWithBlockedTrail$'];
  const result = spawnSync(receipt.argv[0], receipt.argv.slice(1), { cwd: copy, encoding: 'utf8', maxBuffer: 16*1024*1024 });
  const log = result.stdout + result.stderr;
  writeFileSync(join(evidence, '01-old-production-red.log'), log);
  receipt.exit_code = result.status;
  receipt.signal = result.signal;
  receipt.log_sha256 = hash(log);
  receipt.production_unchanged_after = paths.filter(p => !p.endsWith('_test.go')).every(p => hash(readFileSync(join(copy, p))) === receipt.production_manifest[p]);
  receipt.actual_assertions = log.split('\n').filter(s => s.includes('decide has not returned after') || s.includes('decide returned after'));
  receipt.independent_commit_observed = log.includes('independent connection observed committed allow');
  receipt.finite_cleanup_observed = log.includes('decide finished after') && !log.includes('did not finish');
  receipt.authoritative_red = result.status === 1 && receipt.actual_assertions.length >= 2 && receipt.independent_commit_observed && receipt.finite_cleanup_observed && receipt.production_unchanged_after;
  writeFileSync(join(evidence, 'old-production-receipt.json'), JSON.stringify(receipt, null, 2) + '\n');
  process.stdout.write(log);
  if (!receipt.authoritative_red) process.exitCode = 1;
} finally {
  rmSync(copy, { recursive: true, force: true });
}
