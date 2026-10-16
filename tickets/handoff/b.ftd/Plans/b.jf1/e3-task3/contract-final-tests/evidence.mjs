import fs from 'node:fs';
import crypto from 'node:crypto';
import {execFileSync} from 'node:child_process';

const base = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/';
const out = base + 'contract-final-tests/';
const own = ['pkg/api/export_test.go', 'pkg/api/decide_delivery_test.go', 'pkg/api/decide_policy_test.go'];
const read = p => JSON.parse(fs.readFileSync(p, 'utf8'));
const hash = bytes => crypto.createHash('sha256').update(bytes).digest('hex');
const sha = p => hash(fs.readFileSync(p));
const git = args => execFileSync('git', args, {encoding: 'utf8'});
const write = (p, value) => {
  if (fs.existsSync(out + p)) throw Error('receipt already exists: ' + p);
  fs.writeFileSync(out + p, JSON.stringify(value, null, 2) + '\n');
};
const keep = p => p && !p.split('/').includes('.claude') && !p.startsWith('tickets/handoff/');
// Filter forbidden paths before stat, content or Git blob access. No blob reads.
const paths = [...new Set(git(['ls-files', '-co', '--exclude-standard', '-z']).split('\0').filter(keep))].sort();
const tracked = new Map(git(['ls-files', '--stage', '-z']).split('\0').filter(Boolean).map(line => {
  const tab = line.indexOf('\t');
  return [line.slice(tab + 1), line.slice(0, tab).split(' ')[0]];
}).filter(([p]) => keep(p)));
const manifest = {};
for (const p of paths) {
  const stat = fs.lstatSync(p);
  if (!stat.isFile() && !stat.isSymbolicLink()) throw Error('unexpected input type: ' + p);
  const bytes = stat.isSymbolicLink() ? Buffer.from(fs.readlinkSync(p)) : fs.readFileSync(p);
  manifest[p] = {sha256: hash(bytes), bytes: bytes.length,
    git_mode: stat.isSymbolicLink() ? '120000' : (stat.mode & 0o111) ? '100755' : '100644',
    index_mode: tracked.get(p) ?? null};
}
const previous = read(base + 'final-tests/input-manifest-after.json');
const changed = [...new Set([...Object.keys(previous), ...paths])].filter(p => previous[p] !== manifest[p]?.sha256);
if (changed.length !== own.length || changed.some(p => !own.includes(p))) throw Error('unexpected input changes: ' + JSON.stringify(changed));
const sources = read(base + 'deadline-review-fix/FINAL-report.json').source_after_sha256;
for (const [p, h] of Object.entries(sources)) if (manifest[p]?.sha256 !== h) throw Error('source changed: ' + p);
if (manifest['pkg/api/decide_trail_deadline_test.go'].sha256 !== '032950e86d9919e2c48ae79189751e36f8875312a78bf1410973954fdde86d88') throw Error('protected regression changed');
const phase = process.argv[2];
if (phase !== 'pre-full' && phase !== 'after') throw Error('expected pre-full or after');
write('input-manifest-' + phase + '.json', manifest);
const result = {head: git(['rev-parse', 'HEAD']).trim(), branch: git(['branch', '--show-current']).trim(),
  files: paths.length, changed_from_prior_final: changed,
  sources, protected_regression_sha256: manifest['pkg/api/decide_trail_deadline_test.go'].sha256,
  test_files: paths.filter(p => /(?:_test\.go|\.test\.[cm]?[jt]sx?)$/.test(p)).length,
  mode: 'working bytes and executable/symlink Git mode plus index mode; tracked and nonignored untracked files',
  exclusions: '.claude filtered before stat/content/blob reads; all handoff metadata and ignored generated artifacts excluded'};
if (phase === 'pre-full') {
  const priorNames = ['api-deadline-assessment/FINAL-report.json', 'final-tests/FINAL-report.json', 'final-tests/quality-review.json',
    'deadline-red/FINAL-report.json', 'deadline-engineer/FINAL-report.json', 'deadline-green/FINAL-report.json',
    'deadline-review-fix/FINAL-report.json', 'public/FINAL-report.json', 'collector/FINAL-report.json', 'PM-initial-review.json',
    'acceptance-review-fix/FINAL-report.json', 'docs/FINAL-report.json', 'docs/postdoc-advice-inventory.json'];
  result.prior_reports = priorNames.map(p => {
    const bytes = fs.readFileSync(base + p);
    const report = JSON.parse(bytes.toString('utf8'));
    return {path: p, bytes: bytes.length, sha256: hash(bytes), keys: Object.keys(report),
      result: report.result ?? report.final_result ?? report.status ?? null};
  });
  const failedLog = fs.readFileSync(base + 'final-tests/04-full-test-sandbox.log');
  const priorReport = read(base + 'final-tests/FINAL-report.json');
  if (hash(failedLog) !== priorReport.receipt_sha256['04-full-test-sandbox.log']) throw Error('previous failed log changed');
  result.complete_prior_failed_log = {bytes: failedLog.length, sha256: hash(failedLog), lines: failedLog.toString('utf8').split('\n').length - 1,
    failures: failedLog.toString('utf8').split('\n').filter(l => /--- FAIL:|^FAIL\b/.test(l))};
  const oldReceipt = fs.readFileSync(base + 'deadline-red/old-production-receipt.json');
  result.old_red = {sha256: hash(oldReceipt), production_manifest_count: Object.keys(JSON.parse(oldReceipt).production_manifest).length,
    verification: 'retained old exact64a production receipt; no attempt to infer historical09 causes'};
  const quality = read(base + 'final-tests/quality-review.json');
  quality.mandatory_unresolved = [];
  quality.final_result = 'Mandatory test contract findings corrected; targeted race and full-suite results recorded separately.';
  quality.current_pass = {
    changes: own,
    assessment: 'Fresh Engineer assessed three mixed-real/fake-clock failures; no source defect demonstrated. Exact 100ms/300ms policy calls use explicit cancelable context and stateful snapshots with existing decideWithin core. DecideWithSleep=decide and all physical deadline callers remain.',
    assertions: 'Both agents retain exact 300ms, six 50ms polls, liveness, confirm_by and recorded allow. F9 retains two held refusals/no write/attempt, fresh 100ms on all calls, third success/one allow and later duplicate no additional commit.',
    cancellation: 'Both agents cancel immediately after the fixture commit; committed allow/not_confirmed/liveness/confirm_by survive with zero sleep and zero subsequent reads before independent inspection.',
    observer: 'Copied deadlineStore forwards WithDecideContext to a real WithContext delegate or cooperating decorator, retaining observer and same phase/deadline pointers; every bounded observed phase keeps exact original context deadline.',
    fixture_limits: 'Policy fixture proves no real SQL locking, durability or physical deadlines. Real held-lock/precommit no-write guards, original-context public fresh recovery/GetPermission, F10 actual SQLite uncertain COMMIT, SQL cleanup and trail owners retained.',
    docs: 'Source and five product docs unchanged. Test-only seam correction changes no product contract and requires no doc resync.',
    retained_previous_failure: 'Prior full five failures preserved. Two Store cleanup observation repairs from previous pass retained; three API leaves corrected here.',
    new_targeted_failure: '01-targeted: newly added observer assertion incorrectly used external pre-call timestamp as the API-created original deadline; context is created slightly later. Fixed assertion captures exact received context instead. Original elapsed guards never altered.'
  };
  write('quality-review.json', quality);
  // Reconstruct the first targeted attempt from current bytes plus the complete,
  // known inverse of this pass's post-failure edits; label it noncontemporaneous.
  let first = fs.readFileSync('pkg/api/decide_delivery_test.go', 'utf8');
  const replace = (a, b) => { if (!first.includes(a)) throw Error('cannot reconstruct first input: ' + a); first = first.replace(a, b); };
  replace('var pr api.PermissionRow', 'pr := e.request(t)');
  replace('\n\t\t\t\t} else {\n\t\t\t\t\tpr = e.request(t)\n\t\t\t\t}', '\n\t\t\t\t}');
  replace(', originalDeadline: new(time.Time)', '');
  replace('!phase.deadline.Equal(*observed.originalDeadline)', 'phase.deadline.After(start.Add(bound*time.Millisecond))');
  first = first.replace(/^\s*originalDeadline \*time.Time\n/m, '').replace(/^\s*\*s.originalDeadline, _ = ctx.Deadline\(\)\n/m, '');
  replace('// recorded, so retry". Controlled policy availability preserves two refusals\n// then a fresh-budget success; real SQLite locking has its deadline owner above.',
    '// recorded, so retry". Followed literally, the same decide retried once the\n// lock is free records the verdict; while it is held the retry is refused\n// alike, nothing recorded.');
  first = execFileSync('gofmt', [], {input: first, encoding: 'utf8'});
  fs.writeFileSync(out + '01-decide_delivery_test.go.txt', first, {flag: 'wx'});
  const reconstructed = structuredClone(manifest);
  reconstructed['pkg/api/decide_delivery_test.go'] = {...manifest['pkg/api/decide_delivery_test.go'], sha256: hash(first), bytes: Buffer.byteLength(first)};
  write('01-input-manifest-reconstructed.json', {provenance: 'Reconstructed after failure using exact inverse of subsequent test-only edits, gofmt stdin; not a contemporaneous snapshot. Other tested input files unchanged.', files: reconstructed});
} else {
  const before = read(out + 'input-manifest-pre-full.json');
  result.drift_during_full = [...new Set([...Object.keys(before), ...paths])].filter(p => JSON.stringify(before[p]) !== JSON.stringify(manifest[p]));
  if (result.drift_during_full.length) throw Error('full input drift: ' + JSON.stringify(result.drift_during_full));
}
write('integrity-' + phase + '.json', result);
console.log(JSON.stringify(result));
