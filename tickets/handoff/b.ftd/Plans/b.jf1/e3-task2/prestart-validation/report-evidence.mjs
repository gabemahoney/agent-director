import { readFileSync, writeFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import assert from 'node:assert/strict';

const base = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-task2';
const out = `${base}/prestart-validation`;
const read = path => readFileSync(path, 'utf8');
const parse = path => JSON.parse(read(path));
const sha = path => createHash('sha256').update(readFileSync(path)).digest('hex');
const repair = parse(`${base}/prestart-fix/FINAL-report.json`);
const behavior = parse(`${base}/behavior/FINAL-report.json`);
const previous = parse(`${base}/revalidation/FINAL-report.json`);
const expected = {...repair.source_sha256, ...repair.unchanged_source_sha256};
const sources = Object.fromEntries(Object.keys(expected).map(name => [name, sha(name)]));
assert.deepEqual(sources, expected, 'Production source differs from engineer repair');
const changed = ['internal/cursorhost/report_linux_test.go', 'internal/store/cursor_rotation_test.go'];
const fixtures = Object.fromEntries(Object.keys(behavior.file_sha256).filter(name => !changed.includes(name)).map(name => [name, sha(name)]));
for (const [name, hash] of Object.entries(fixtures)) {
  assert.equal(hash, previous.test_sha256[name] ?? behavior.file_sha256[name], `Unassigned fixture changed: ${name}`);
}
const commands = {
  '01-new-reconciliation-race.log': `make sandbox CMD='gofmt -w internal/store/cursor_rotation_test.go internal/cursorhost/report_linux_test.go && env GORACE=atexit_sleep_ms=0 go test -race -timeout 15m -count=1 -v ./internal/store ./internal/cursorhost -run "Test(CursorRotationAtomicPublicationAndSuccessiveHistory|CursorRotationRejectsChangedExaminedAuthority|CursorRotationReconciliation|CursorRotationObservation|ReporterObservesCommittedRotationBeforeStalledHookReturns)"'`,
  '02-controller-affected-race.log': `make sandbox CMD='env GORACE=atexit_sleep_ms=0 go test -race -timeout 15m -count=1 -v ./internal/cursorhost -run "Test(CursorRotation|CursorDelayedOldAdmission|CursorEligiblePublicPause|CursorInterruptAndRotation|CursorControlDeadline|CursorRecoveryCompletion)"'`,
  '03-real-pane-affected-race.log': `make sandbox CMD='env GORACE=atexit_sleep_ms=0 go test -race -timeout 15m -count=1 -v ./test/realtmux -run "TestCursor(FreshRealPanePromptsRefusalsAndPause|PersistenceFailureStopsAdmission)$"'`,
  '04-outgoing-full-snapshot-race.log': `make sandbox CMD='gofmt -w internal/store/cursor_rotation_test.go && env GORACE=atexit_sleep_ms=0 go test -race -timeout 15m -count=1 -v ./internal/store -run "TestCursorRotationRejectsChangedExaminedAuthority/snapshot_process$"'`,
};
const count = outcomes => Object.fromEntries(['PASS', 'FAIL', 'SKIP'].map(status => [status, Object.values(outcomes).filter(value => value === status).length]));
const receipts = {}, latest = {};
for (const [name, command] of Object.entries(commands)) {
  const path = `${out}/${name}`, raw = read(path);
  const outcomes = Object.fromEntries([...raw.matchAll(/^\s*--- (PASS|FAIL|SKIP): (\S+)/gm)].map(match => [match[2], match[1]]));
  const leaves = Object.fromEntries(Object.entries(outcomes).filter(([name]) => !Object.keys(outcomes).some(other => other.startsWith(name + '/'))));
  assert(Object.keys(leaves).length && Object.values(leaves).every(status => status === 'PASS'), name);
  assert(!/^FAIL|WARNING: DATA RACE/m.test(raw), name);
  const packages = [...raw.matchAll(/^ok\s+github[^\n]+/gm)].map(match => match[0]);
  assert(packages.length, name);
  Object.assign(latest, leaves);
  receipts[name] = {command, result: 'passed', sha256: sha(path), pass_lines_including_parent_subtest_overlap: count(outcomes).PASS, leaf_outcomes: count(leaves), data_race_warnings: 0, packages};
}
const report = {
  role: 'apiary_test_writer_e3_task2_prestart_validate',
  result: 'R3 independently revalidated; unchanged retained cancel-before-start leaf passes. Three-way real-store reconciliation and negative reporter handoff coverage pass. Initial behavioral scope complete; final docs/advice/quality/full suite remain downstream.',
  head: '8fabf73e473630e2902f37d6060a5b84bd4daab0',
  ticket_statuses: {'t3.jf1.r8.sr.pp': 'active -> active -> done', 't3.jf1.r8.sr.eg': 'done -> active -> done; reopened only for three-way reconciliation proof'},
  untouched_statuses: '.8z done; .e9 active; .78/.66 defined; Task2/E3 active; Task3 unstarted; E2 intentionally active. No statuses beyond owned subtasks changed.',
  files_changed_this_pass: changed,
  evidence_files: ['prestart-validation/FINAL-report.json', 'prestart-validation/report-evidence.mjs', 'prestart-validation/latest-leaf-observations.json', 'prestart-validation/05-report-generator-unavailable.log', 'prestart-validation/06-report-generation.log', ...Object.keys(receipts).map(name => `prestart-validation/${name}`)],
  test_sha256: Object.fromEntries(changed.map(name => [name, sha(name)])),
  source_sha256: sources,
  source_integrity: 'All ten assigned production sources match engineer prestart-fix report; no source or documentation changes in this pass.',
  evidence_generation: "make sandbox CMD='bun tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/prestart-validation/report-evidence.mjs' passed (06-report-generation.log); source/fixture hashes and named receipt outcomes independently verified. git diff --check passed. Go formatting ran only in make sandbox.",
  unchanged_other_test_fixture_sha256: fixtures,
  coverage: {
    unchanged_and_committed: 'Extended existing successive real-transaction owner: exact outgoing snapshot is unchanged-only and ObserveCursorRotation remains false; after actual atomic fresh publication reconciliation is committed-only. Existing independent before-COMMIT visibility and committed-only observer checks retained.',
    unknown_outgoing: 'Added reconciliation assertions to existing changed-authority matrix, retaining original publisher refusal and replacement-preservation assertions: row snapshot version, row process without version change, old row session, launch/life/socket/server/pane, path, retirement, runtime pid/start/connection/pipes/fds and deletion all yield neither state. One inconsistent outgoing current-life archive case also yields neither. No copied authority matrix.',
    unknown_fresh_and_failed_reads: 'Existing committed-publication negative owner checks both results for missing/wrong-life/wrong-path archive, replaced runtime/launch/pane/path. Explicit cancelled/expired context and closed-store errors; real sole-connection contention expires within caller budget and yields neither. Positive baseline retained before every corruption.',
    reporter: 'Retained cancel-before-start leaf body/assertions/deadline unchanged. Added actual after-COMMIT runtime-connection replacement followed by cancellation: final unknown proof cannot call rotationReconciled. Existing deadline stop additionally requires no final reconciliation callback; independently proven prior committed observation is retained.',
    controller: 'Affected rotation/intentional shutdown, original control deadline, blocked Sync/recovery, public eligible pause and unknown holds pass, including unreleased committed-hook exit/pause/interrupt/timeout/death; R2 parked provisional admission and S1-S3 causal regressions remain green.',
    physical_pane: 'Existing actual built CLI/direct-parent hook/real tmux success, pause and persistence faults pass with synthetic ACP. No authenticated acceptance or Docker collector execution claimed.',
  },
  receipts,
  counts: {latest_distinct_leaves: count(latest), data_race_warnings: 0, limit: 'Distinct named leaves across this pass receipts; parent/subtest overlap excluded. Not a full-suite total. Added snapshot-process leaf alone was run after addition; no unrelated rerun.'},
  causal_R3: {red: '../prestart-fix/01-retained-red.log', green: '01-new-reconciliation-race.log: TestReporterObservesCommittedRotationBeforeStalledHookReturns/cancel_before_start', limit: 'Retained red is engineer pre-repair receipt, independently read; this pass did not restore old source. Reporter-level regression unchanged. No deterministic full Run pre-Start micro-window reproduction claimed. Source inspection confirms intentional-and-reconciled consumes positive handoff; controller tests cover other boundaries.'},
  review: 'Reuses existing factories, negative matrices, subprocess barrier and real SQL. No source seam, copied controller, deadline extension, fake publication, assertion weakening or retry-until-green. Representative row-process mutation supplements row-version negative to check full snapshot participation without duplicating shared per-field snapshot suite.',
  failed_receipts: [{'05-report-generator-unavailable.log': 'Evidence generator only: python3 absent from sandbox; make exit 2/underlying 127. No tests ran. Replaced generator with existing Bun runtime; no install or test rerun.'}],
  remaining_findings: [],
  remaining_work: ['One settled documentation/SRD pass', 'Fresh final Test Writer: postdoc advice .78, final quality .e9 and sole settled make test-sandbox .66', 'Coordinator commit and user exact-revision scheduler Docker gate before Task3'],
  full_suite: 'NOT RUN; explicitly not assigned this pass.',
  integration: 'NOT RUN; not dispatched as integration Test Writer. Docker groups/collectors remain user scheduler only.',
  model_assessment: 'TLA+ not applicable: no spec/tla inputs changed; zero formal/baseline jobs.',
  execution_boundary: 'All repository execution/format/tests/evidence scripts through make sandbox CMD, isolated sandbox HOME and existing per-test stores/sockets/owned processes. No .claude access, outside-workspace discovery, real AD stores, credentials/login/logout, live Cursor, Docker groups/collectors, scheduler/TLC/Java/parser, source/docs edits, commit/push, delegation or midflight messages. CLAUDE.md used only for permitted documentation locations.',
  deviations: [], questions_for_caller: [],
};
writeFileSync(`${out}/latest-leaf-observations.json`, JSON.stringify(latest, null, 2) + '\n');
writeFileSync(`${out}/FINAL-report.json`, JSON.stringify(report, null, 2) + '\n');
console.log(JSON.stringify({source_integrity: true, receipts: Object.fromEntries(Object.entries(receipts).map(([name, value]) => [name, value.leaf_outcomes])), counts: report.counts}));
