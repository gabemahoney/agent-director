import {readFileSync, writeFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {spawnSync} from 'node:child_process';
const base = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-task3';
const out = `${base}/deadline-review-fix`;
const json = p => JSON.parse(readFileSync(p, 'utf8'));
const hash = p => createHash('sha256').update(readFileSync(p)).digest('hex');
const old = json(`${base}/deadline-red/old-production-receipt.json`);
const green = json(`${base}/deadline-green/FINAL-report.json`);
const engineer = json(`${base}/deadline-engineer/FINAL-report.json`);
const tests = Object.fromEntries(Object.entries(old.production_manifest).filter(([p]) => p.endsWith('_test.go')));
const tracked = spawnSync('git', ['ls-files', '-z', '--', '*_test.go'], {encoding: 'utf8'});
if (tracked.status !== 0) throw Error(tracked.stderr);
for (const p of tracked.stdout.split('\0').filter(p => p && !p.split('/').includes('.claude'))) {
  const blob = spawnSync('git', ['show', `HEAD:${p}`]);
  if (blob.status !== 0) throw Error(`Cannot read baseline test ${p}`);
  tests[p] = createHash('sha256').update(blob.stdout).digest('hex');
}
Object.assign(tests, green.integrity.new_test_sha256);
tests['pkg/api/decide_trail_deadline_test.go'] = '032950e86d9919e2c48ae79189751e36f8875312a78bf1410973954fdde86d88';
for (const [p, expected] of Object.entries(tests)) {
  if (hash(p) !== expected) throw Error(`Test drift: ${p}`);
}
writeFileSync(`${out}/verified-tests.sha256`, Object.entries(tests).sort(([a], [b]) => a.localeCompare(b)).map(([p, h]) => `${h}  ${p}\n`).join(''));
const source = Object.fromEntries(engineer.source_files_changed.map(p => [p, hash(p)]));
for (const [p, h] of Object.entries(source)) {
  if (p !== 'internal/store/lockwait.go' && h !== engineer.final_source_sha256[p]) throw Error(`Unrelated source drift: ${p}`);
}
const raw = readFileSync(`${out}/01-targeted-regression.log`, 'utf8');
if (/^FAIL/m.test(raw) || !raw.includes('--- PASS: TestDecideMaxWaitWithBlockedTrail')) throw Error('Targeted checks not green');
const names = [...raw.matchAll(/^\s*--- PASS: (\S+) /gm)].map(m => m[1]);
const leaves = names.filter(n => !names.some(other => other.startsWith(`${n}/`)));
const report = {
  role: 'apiary_engineer_e3_task3_deadline_fix',
  result: 'D1 repaired; retained regression and affected existing SQL/cleanup boundary cases GREEN. Fresh independent PRAGMA-context boundary validation remains required.',
  ticket_statuses: {'t3.jf1.r8.a8.ir': 'done -> active -> done'},
  status_scope: 'Only own .ir; no parent Task/Epic/.yt status changes.',
  head: 'e85a74c9803ffe1dddb80bdcb19bf4614f643839',
  branch: 'feature/b.ftd',
  source_files_changed_this_lane: ['internal/store/lockwait.go'],
  precise_repair: 'At the failed setup PRAGMA busy_timeout only, errors wrapping context.Canceled or context.DeadlineExceeded additionally wrap ErrStoreBusy. This branch precedes callback/BEGIN and cannot hide a commit. Real non-context SQL errors retain their ordinary wrapper. Existing discard=true executes first; releaseConn continues quarantine/disposal under the original context. COMMIT and cleanup paths are unchanged.',
  technical_decisions_for_srd: ['Setup cancellation/deadline before BEGIN is a precommit ErrStoreBusy, preserving the underlying context error. Classification tests the returned error with errors.Is rather than ctx.Err(), so an unrelated I/O error coinciding with deadline expiry is not converted into busy. No new budget, worker, public signature, model or schema change.'],
  commands: [
    {command: 'bees update-ticket --ids t3.jf1.r8.a8.ir --status active', exit: 0},
    {command: 'make sandbox CMD=\'gofmt -w internal/store/lockwait.go && timeout 90s go test -timeout 75s -count=1 -v ./internal/store ./pkg/api -run "^(TestDecisionAbsoluteDeadlineAtSQLiteBoundaries|TestDecisionCleanupQuarantinesNoncancelableClose|TestDecisionExpiredContextCannotRenewAcquisitionOrZeroWait|TestDecisionLateAcquisitionRetainsOriginalSQLDeadline|TestDecisionZeroWaitAllowsSQLBeyondAcquisitionAllowance|TestBoundedCallsUnderAHeldLockOrConnection|TestDecideMaxWaitWithBlockedTrail)$"\'', log: '01-targeted-regression.log', exit: 0},
    {command: 'bees update-ticket --ids t3.jf1.r8.a8.ir --status done', exit: 0},
    {command: `make sandbox CMD='bun ${out}/finalize.mjs && git diff --check'`, logs: ['03-final-integrity.log', '04-complete-test-integrity.log'], purpose: 'Evidence assembly: 03 verified four new tests; 04 extends verification to all 700 tracked and new Go tests without rerunning tests.'}
  ],
  test_results: {passed_leaves: leaves.length, leaves, observations: raw.split('\n').filter(l => l.includes('observed committed allow') || l.includes('decide finished after')), log_sha256: hash(`${out}/01-targeted-regression.log`)},
  test_integrity: {verified_count: Object.keys(tests).length, baseline: 'Tracked Go tests match HEAD blobs; four new tests match independent GREEN new_test_sha256 and exact retained regression hash. .claude paths excluded before blob/file access.', manifest: 'verified-tests.sha256', all_match: true, retained_regression_sha256: tests['pkg/api/decide_trail_deadline_test.go'], test_edits: []},
  source_before_sha256: Object.fromEntries(Object.keys(source).map(p => [p, engineer.final_source_sha256[p]])),
  source_after_sha256: source,
  administrative_failures: ['Initial sandbox test-manifest pipeline used unavailable rg, and lacked pipefail, so setup and 02 integrity commands exited zero while hashing empty stdin. Their tests-before.sha256/tests-after.sha256 files are INVALID evidence and are not used. Retained raw logs show the error. Corrected finalize.mjs verifies actual test bytes against independent retained authoritative manifests; no test rerun or assertion change.'],
  deviations: ['Initial integrity pipeline invalid; corrected against independent retained manifests. No source-scope deviation.'],
  followup_work: ['Fresh Test Writer should add PRAGMA setup cancellation/deadline driver boundary, assert no BEGIN/COMMIT, ErrStoreBusy, safe subsequent reuse/discard; preserve ordinary setup I/O errors and postcommit uncertainty; revalidate affected cases.'],
  evidence_limits: ['This source lane did not add or execute a new PRAGMA setup cancellation case; that is explicitly assigned to the fresh independent Test Writer.', 'Historical09 deadline causes remain unknown.', 'Existing cooperative SQL/context and uninterruptible-native execution limits, and pending audit crash-durability limits, are unchanged.'],
  full_suite: 'Not run; .zj retains ownership.',
  integration: 'No local Docker TESTPLAN groups, collectors, scheduler submissions.',
  formal: 'No spec/tla changes; no TLA/TLC/Java/parser execution.',
  boundaries: 'No .claude access, real AD stores, credentials/login/logout/live Cursor, host repository tests/scripts/builds, test/doc/fixture edits, commits, pushes, delegation or outbound messages. Repository format/test/evidence scripts only make sandbox. Workspace-scoped discovery.',
  incomplete_work: [],
  questions_for_caller: []
};
writeFileSync(`${out}/FINAL-report.json`, JSON.stringify(report, null, 2) + '\n');
console.log(JSON.stringify({verified_tests: Object.keys(tests).length, passed_leaves: leaves.length, source_after_sha256: source}));
