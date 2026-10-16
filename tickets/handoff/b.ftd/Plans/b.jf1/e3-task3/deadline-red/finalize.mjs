import { readFileSync, writeFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { join } from 'node:path';

const dir = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/deadline-red';
const sha = value => createHash('sha256').update(value).digest('hex');
const receipt = JSON.parse(readFileSync(join(dir, 'old-production-receipt.json')));
const test = receipt.overlay;
const currentHash = sha(readFileSync(test));
const currentProduction = Object.fromEntries(Object.keys(receipt.production_manifest).map(p => [p, sha(readFileSync(p))]));
const productionDrift = Object.keys(currentProduction).filter(p => currentProduction[p] !== receipt.production_manifest[p]);
const git = (...args) => spawnSync('git', args, { encoding: 'utf8' }).stdout.trim();
const hashes = Object.fromEntries(['pkg/api/decide.go', 'internal/store/lockwait.go', 'internal/store/permission.go', 'internal/trail/trail.go', 'internal/trail/envelope.go'].map(p => [p, receipt.production_manifest[p]]));
const files = [test, `${dir}/run-red.mjs`, `${dir}/finalize.mjs`, `${dir}/00-sandbox-receipt.log`, `${dir}/01-old-production-red.log`, `${dir}/old-production-receipt.json`];
const report = {
  role: 'apiary_test_writer_e3_task3_deadline_red',
  result: receipt.authoritative_red ? 'expected old-production deadline RED established before source repair' : 'incomplete',
  ticket_statuses: { 't3.jf1.r8.a8.gf': 'active -> done (expected-failure milestone)' },
  status_scope: 'Only .gf; no Task, Epic or other Subtask status changes.',
  head: git('rev-parse', 'HEAD'), branch: git('branch', '--show-current'),
  pinned_old_production_revision: receipt.revision,
  tested_tree: 'Fresh /tmp copy inside make sandbox, populated from exact pinned Git blobs excluding .claude and tickets before blob access; overlay only the new _test.go. Copy removed after run.',
  production_manifest: 'old-production-receipt.json#production_manifest',
  production_manifest_count: Object.keys(receipt.production_manifest).length,
  production_manifest_sha256: receipt.production_manifest_sha256,
  critical_old_source_sha256: hashes,
  production_unchanged_in_test_copy_after: receipt.production_unchanged_after,
  workspace_production_drift_from_old: productionDrift,
  workspace_source_untouched: productionDrift.length === 0,
  test_sha256: currentHash,
  unchanged_regression_ready_for_green: currentHash === receipt.test_sha256,
  changed_test_files: [test],
  reused_fixtures: ['newRelayEnv / relayEnvHook / relayEnv.view / relayEnv.request', 'storefix.TestRequestTokenA', 'pkg/api TestMain isolated HOME and singleton'],
  commands: [
    { command: 'bees update-ticket --ids t3.jf1.r8.a8.gf --status active', result: 'success' },
    { command: "make sandbox CMD='gofmt -w pkg/api/decide_trail_deadline_test.go && bun tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/deadline-red/run-red.mjs'", outer_exit: 0, actual_go_test_exit: receipt.exit_code, explanation: 'Evidence runner requires a genuine deadline assertion failure; expected RED makes the evidence runner succeed.', log: '00-sandbox-receipt.log', inner_argv: receipt.argv },
    { command: "make sandbox CMD='bun tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/deadline-red/finalize.mjs'", purpose: 'Evidence assembly and current source integrity check; no tests.', attempts: [{ log: '02-final-integrity.log', outer_exit: 2, disposition: 'Evidence generator syntax error only; corrected bracket. No test/source change and no additional test run.' }, { log: '03-final-integrity.log', outer_exit: 0 }] },
    { command: 'bees update-ticket --ids t3.jf1.r8.a8.gf --status done', result: 'expected-failure milestone completed' }
  ],
  raw_test_receipt: '01-old-production-red.log',
  actual_assertions: receipt.actual_assertions,
  request_bound_ms: 500, elapsed_assertion_ms: 1500,
  causal_control: 'trail.Emit calls the test JSON Marshaler while holding the real process singleton mutex. The Marshaler signals entry and waits for an explicit release channel. No fake writer, reflection, unsafe mutation, copied Decide algorithm, production instrumentation or source seam. This controls real mutex contention, not filesystem Write/Sync latency.',
  public_operation: 'api.Decide delegates to actual SQLite Store.DecideRelayRequest and actual trail.Emit; an independent Store connection observes committed allow before the blocked reporter is released.',
  observed: { independent_commit_elapsed: '4.480057ms', deadline_assertion_elapsed: '1.500842204s', operation_finished_elapsed: '1.505624411s', returned_result: 'not_confirmed, nil error; persisted allow, no attempted decision', finite_cleanup: receipt.finite_cleanup_observed },
  synchronization: 'Entry channel proves actual mutex ownership before api.Decide starts. Independent SQL connection observes commit. A ticker only observes persisted state; no sleep chooses the race. Holder releases after the unchanged 1.5s assertion and every wait has a finite cleanup bound; outer timeout 60s, Go timeout 45s.',
  existing_parallel_coverage: 'Existing TestDecideMaxWaitUnderHeldLock and all helpers/parallel cases untouched. New singleton-lock test declares Serial and cannot overlap parallel tests.',
  next_writer_scope: 'Independent .yt owns unchanged regression GREEN plus distinct precommit SQL, cleanup, stale absolute deadline and connection reuse/discard cases after Engineer repair. Existing request_proof_test.go beforeEachExec/execHookConnector/execHookConn and cursor_trail_export_test.go demonstrate a test-only real SQLite driver boundary; reuse/extend narrowly if needed. The current proof deliberately isolates postcommit trail contention, and does not prove SQL/cleanup deadline behavior.',
  technical_seam_requests: [],
  evidence_limits: ['Historical 09 causes remain unknown. This controlled reachable mutex overrun does not reconstruct or identify either 5.700691482s/2.484155549s historical cause.', 'No repaired-source GREEN claim; production repair has not started in this lane.', 'No real slow-filesystem test; actual mutex ownership is controlled through JSON encoding on the real writer.'],
  deviations: [], integration: 'Not run; not dispatched for integration. No Docker TESTPLAN groups or collectors.',
  full_suite: 'Not run; .zj exclusively owns the settled full suite.',
  formal: 'No spec/tla changes and no formal/TLC/Java/parser jobs.',
  boundaries: 'Source/docs untouched. No .claude read/access, real AD stores, credentials, login/logout, live Cursor, host repo scripts/build/tests/artifacts, scheduler submissions, pushes, commits or delegation. All Go formatting, repository script and test execution used make sandbox. Bees CLI used only for own Subtask status.',
  incomplete_work: [], questions_for_caller: [],
  file_sha256: Object.fromEntries(files.map(p => [p, sha(readFileSync(p))]))
};
writeFileSync(join(dir, 'FINAL-report.json'), JSON.stringify(report, null, 2) + '\n');
console.log(JSON.stringify({ result: report.result, production_manifest_count: report.production_manifest_count, productionDrift, test_unchanged: report.unchanged_regression_ready_for_green }));
if (productionDrift.length || !report.unchanged_regression_ready_for_green || !receipt.authoritative_red) process.exitCode = 1;
