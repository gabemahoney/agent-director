import fs from 'node:fs';
import crypto from 'node:crypto';
import {execFileSync} from 'node:child_process';
const base = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/';
const out = base + 'contract-final-tests/';
const read = p => JSON.parse(fs.readFileSync(p, 'utf8'));
const sha = p => crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex');
const write = (p, obj) => { if (fs.existsSync(out + p)) throw Error('refusing receipt overwrite: ' + p); fs.writeFileSync(out + p, JSON.stringify(obj, null, 2) + '\n'); };
const clean = s => s.replace(/\x1b\[[0-9;]*m/g, '');
const full = clean(fs.readFileSync(out + '04-full-test-sandbox.log', 'utf8'));
const exit = Number(fs.readFileSync(out + '04-full-test-sandbox.exit', 'utf8').trim());
const goLines = full.split('\n').filter(l => /^(ok\s|\?\s|FAIL\s)/.test(l));
const failures = full.split('\n').filter(l => /^\s*(--- FAIL:|FAIL(?:\s|$)|\(fail\))/.test(l) || /panic:|fatal error:/.test(l));
const bun = full.match(/\n\s*(\d+) pass\n\s*(\d+) fail\n\s*(\d+) expect\(\) calls\nRan (\d+) tests across (\d+) files\. \[([^\]]+)\]/);
const before = read(out + 'input-manifest-pre-full.json');
const after = read(out + 'input-manifest-after.json');
const integrity = read(out + 'integrity-after.json');
const drift = [...new Set([...Object.keys(before), ...Object.keys(after)])].filter(p => JSON.stringify(before[p]) !== JSON.stringify(after[p]));
if (drift.length) throw Error('tested input changed: ' + drift);
const passed = exit === 0 && failures.length === 0 && bun && Number(bun[2]) === 0;
const targeted = name => {
  const log = clean(fs.readFileSync(out + name + '.log', 'utf8'));
  const allPassed = [...log.matchAll(/^\s*--- PASS: (\S+) \(/gm)].map(m => m[1]);
  const leaves = allPassed.filter(name => !allPassed.some(other => other.startsWith(name + '/')));
  return {log: name + '.log', exit: Number(fs.readFileSync(out + name + '.exit', 'utf8').trim()),
    passed_leaves: leaves.length, leaves,
    failed: log.split('\n').filter(l => /^\s*--- FAIL:|^FAIL\b/.test(l)),
    package_lines: log.split('\n').filter(l => /^(ok\s|\?\s|FAIL\s)/.test(l))};
};
const pre = read(out + 'integrity-pre-full.json');
const previous = read(base + 'final-tests/FINAL-report.json');
const firstTarget = targeted('01-targeted');
const raceTarget = targeted('02-targeted-race-quality');
const trailTarget = targeted('03-pre-full-integrity-trail');
const unresolved = passed ? [] : [{
  test: 'TestDecideMaxWaitUnderHeldLock/a_request_recorded_before_v7,_its_relay_hook_settling_after_the_bound',
  location: 'pkg/api/decide_delivery_test.go:315',
  assertion: 'err store: commit outcome unconfirmed; read the request before retrying: context deadline exceeded matches []; want exactly ErrStoreBusy',
  facts: 'Unchanged original real-SQL legacy leaf, still parallel, MaxWaitMs 100ms and original no-sleep/1.1s physical assertion. Full subtest reported 1.53s including setup. assertOneSentinel uses t.Errorf, so later original elapsed/no-sleep/no-write assertions executed and reported no failures. Only sentinel classification failed; no phase trace locates COMMIT elapsed time.',
  assessment: 'New actual SQL/legacy error-contract failure, distinct from corrected three policy leaves; fresh Engineer source/test-contract assessment required. Existing source intentionally distinguishes unconfirmed COMMIT from precommit ErrStoreBusy. Real legacy path attempts guarded UPDATE and COMMIT before refusal-by-settle-time. Independent row assertion saw no decision/attempt, and physical/no-sleep assertions passed. This does not establish a source defect or deadline overrun.',
  route: 'Do not relax sentinel/physical assertions, enlarge 100ms, lower parallelism, distort source or rerun unchanged. Preserve receipt and original protected 500ms/1.5s guards. Calling agent routes fresh assessment before necessary correction/new full run.'
}];
const qualityAfter = read(out + 'quality-review.json');
qualityAfter.mandatory_unresolved = unresolved;
qualityAfter.final_result = passed ? 'Quality and full suite passed.' : 'One new real-SQL legacy error-contract failure remains; quality/full subtasks stay active.';
write('quality-review-after.json', qualityAfter);
const report = {
  role: 'apiary_test_writer_e3_task3_deadline_contract_final',
  result: passed ? 'Test-only contract correction, targeted race/quality and settled full make test-sandbox passed.' : 'Full sandbox failed; preserve this receipt and route the actual failures before further full execution.',
  head: execFileSync('git', ['rev-parse', 'HEAD'], {encoding: 'utf8'}).trim(),
  branch: execFileSync('git', ['branch', '--show-current'], {encoding: 'utf8'}).trim(),
  ticket_statuses: {'t3.jf1.r8.a8.yj': passed ? 'active -> done' : 'active', 't3.jf1.r8.a8.zj': passed ? 'active -> done' : 'active'},
  status_scope: 'Only own .yj/.zj; .6f remains done and all other Subtask/Task/Epic statuses untouched. Status receipts are separate.',
  files_changed: ['pkg/api/export_test.go', 'pkg/api/decide_delivery_test.go', 'pkg/api/decide_policy_test.go'],
  evidence_directory: out,
  contract_correction: {
    assessment: 'api-deadline-assessment/FINAL-report.json read completely. No source fix justified: real fresh 100ms cannot unconditionally guarantee SQL success, and exact 300ms fake sleep is not a real overall SQL budget.',
    seam: 'Explicit _test.go DecideWithinForTest=decideWithin only for selected controlled policies. DecideWithSleep=decide remains unchanged for physical deadline callers.',
    fixture: 'Small stateful DecideStore snapshots seeded through existing relayEnv/real seed helpers; held/free availability and fake policy time explicit. Held write consumes its passed wait and returns ErrStoreBusy before mutation. Free conditional write records one allow, refuses duplicates; unexpected unused methods fail. No copied SQL, retries, cleanup, queue or Decide algorithm.',
    exact_delivery: 'Both parallel agent cases keep exact original 300ms total, six 50ms polls, not_confirmed, hook_alive, confirm_by and stored allow/one commit. Precommit fixture time is zero.',
    f9: 'Two held refusals preserve no decision/no attempt/zero commits, each fresh exact 100ms. Deliberate free third call with same 100ms records one allow, independent snapshot read confirms it; duplicate call ErrAlreadyDecided adds no commit.',
    cancellation: 'Both agents cancel explicit no-timer context immediately after fixture commit, retain allow/not_confirmed/hook_alive/confirm_by and assert no extra sleep or read after cancellation before independent snapshot inspection.',
    observer: 'deadlineStore copied wrapper forwards optional WithDecideContext to underlying concrete Store.WithContext or cooperating decorator. Same phase pointer and captured original deadline survive; every observed bounded phase is checked against that exact deadline. No source unwrapping.',
    limits: 'Controlled policy fixture is not evidence of real SQLite locks, SQL success within 100ms, physical deadlines or durability. Context has no fabricated wall Deadline or timer. Real operations retain public Decide/DecideWithSleep and actual Store deadline owners.'
  },
  physical_guards: {
    protected_regression: {path: 'pkg/api/decide_trail_deadline_test.go', sha256: integrity.protected_regression_sha256, unchanged: true, request_ms: 500, wall_bound_ms: 1500},
    held_lock: 'All original TestDecideMaxWaitUnderHeldLock agent/busy-timeout/phase parallel leaves and original 500ms/1.5s guards retained; observer forwarding now carries original absolute SQL context.',
    client: 'Original TestClientDecideBoundsDeferredAuditAndPreservesFIFO 500ms/1.5s audit bound and FIFO preserved.',
    recovery: 'Original TestDecideAbsoluteContextSurvivesParkedPhase real precommit refusal/no write followed by deliberate public fresh-budget success and actual GetPermission retained.',
    sql_cleanup: 'Actual SQLite driver/PRAGMA/BEGIN/SQL/COMMIT/rollback/restore/disposal quarantine/late acquisition/zero-wait/first-writer tests retained. F10 actual SQLite uncertain COMMIT -> actual public GetPermission and exactly one UPDATE/COMMIT retained.',
    original_old_red: pre.old_red,
    historical09: 'Causes remain UNKNOWN; this correction and controlled old64a RED/GREEN do not reconstruct either historical failure.'
  },
  quality: {report: 'quality-review-after.json', pre_full_report: 'quality-review.json', mandatory_unresolved: unresolved, vet_and_diff_check: '02-targeted-race-quality.log, exit 0; all existing parallel declarations and assertions retained; Bun parallelism unchanged',
    method: 'Current test correction and changed production diff/new source checked against actual Store/context/trail owners and inherited whole Task3 quality/advice/acceptance inventories. Complete named reports/full failed log parsed and hash-bound. No new personal whole-file architecture or unchanged-test-suite review claim.',
    docs: 'Eight source files and five product docs byte-identical to prior settled manifests. Test-only seam correction changes no product contract; no docs resync.'},
  advice: previous.advice_inventory_mapping.map(s => ({id: s.id, primary: s.primary, behavior: s.behavior,
    current_disposition: s.id === 'precommit_exhaustion_retry' ? 'Real original-context recovery retained and full-run covered; F9 exact policy-time retry corrected as documented above.' : 'Existing literal behavioral owner retained; targeted/full run covers actual owner.'})),
  tested_tree: {files: Object.keys(before).length, manifest_before: 'input-manifest-pre-full.json', manifest_after: 'input-manifest-after.json', drift,
    sha256_before: sha(out + 'input-manifest-pre-full.json'), sha256_after: sha(out + 'input-manifest-after.json'),
    changed_from_prior_final: integrity.changed_from_prior_final,
    unchanged_eight_sources: integrity.sources,
    exclusions: integrity.exclusions,
    identity: 'File bytes plus working Git mode. index_mode is provenance only: newly added files have null until staged. Coordinator commit comparison must use bytes/git_mode, not index_mode nullness.',
    current_manifest_test_files: integrity.test_files,
    previous_docs_test_hash_provenance: 'Docs 759-test snapshot is prior provenance; current differences are justified prior final advice/boundary changes and the three files in this pass. No claim that its historical hash total is current.'},
  commands: [
    {command: 'make sandbox CMD=<gofmt three own tests; targeted API decisions>', receipt: firstTarget,
      disposition: 'New observer-only test assertion defect: external pre-call timestamp predates context creation by hundreds of ns. Corrected to received exact context; all original physical bounds unchanged. Three corrected policy leaves/cancellation already passed.'},
    {command: 'make sandbox CMD=<gofmt observer; go test -race targeted API/Store; go vet; git diff --check; versions>', receipt: raceTarget,
      note: 'Trail selector TestWriterDeadline matched no tests; output reports no tests to run, never counted as trail coverage. Correct trail names run next.'},
    {command: 'make sandbox CMD=<go test -race ./internal/trail -run ^TestBoundedWriter; bun evidence.mjs pre-full>', receipt: trailTarget},
    {command: 'make test-sandbox', log: '04-full-test-sandbox.log', exit_file: '04-full-test-sandbox.exit', exit,
      configured: 'CLAUDE.md and Makefile', runs_this_pass: 1, total_Task3_full_attempts: 2, prior_failed: '../final-tests/04-full-test-sandbox.log',
      phases: ['locked Bun dependencies', 'canonical package build', 'go test -timeout 30m ./...', 'bun test'],
      parallelism: 'Default unchanged, no -p/-parallel reductions; Bun unchanged sequential configuration.'},
    {command: 'make sandbox CMD=<bun evidence.mjs after; bun report.mjs; git diff --check>', purpose: 'Post-run input integrity and report only; no test rerun.'}
  ],
  exact_commands: read(out + 'commands.json'),
  full_suite: {result: passed ? 'passed' : 'failed', exit, runs_this_correction_pass: 1, repeated_after_pass: false,
    go: {executed_passed_packages: goLines.filter(l => /^ok\s/.test(l) && !l.includes('(cached)')).length,
      cached_passed_packages: goLines.filter(l => /^ok\s/.test(l) && l.includes('(cached)')).length,
      no_test_files_packages: goLines.filter(l => /^\?\s/.test(l) && l.includes('[no test files]')).length,
      failed_packages: goLines.filter(l => /^FAIL\s/.test(l)), package_lines: goLines,
      counting: 'Actual package output. No inferred Go leaf total from nonverbose full-suite output.'},
    bun: bun ? {passed: Number(bun[1]), failed: Number(bun[2]), expect_calls: Number(bun[3]), tests: Number(bun[4]), files: Number(bun[5]), elapsed: bun[6]} : {summary_missing: true},
    failure_lines: failures,
    unresolved,
    prior_full: {log: '../final-tests/04-full-test-sandbox.log', sha256: pre.complete_prior_failed_log.sha256, failed_leaves: 5,
      dispositions: 'Two Store raw observation cleanup races corrected in previous pass and retained; three API mixed-clock failures corrected here after fresh Engineer assessment. No unchanged full retry.'}},
  receipt_provenance: {
    first_targeted_inputs: '01-input-manifest-reconstructed.json and 01-decide_delivery_test.go.txt: explicitly retrospective exact inverse reconstruction of subsequent own test edits, not a contemporaneous snapshot. This administrative limitation does not affect the contemporaneous full-run before/after manifest.',
    second_targeted_inputs: 'Same settled source/test/doc/build/registry/testplan bytes/modes as input-manifest-pre-full.json; no tested inputs edited between targeted race and pre-full manifest.',
    earlier_receipts: 'All previous failed/successful receipts remain in their original directories, never overwritten. Complete named reports and original failed full log byte-read/parsed/hash-bound in integrity-pre-full.json.',
    no_retry_until_green: 'One failed targeted assertion correction, then targeted race pass and one full run. No full rerun on unchanged inputs.'
  },
  environment: {go: 'go1.22.10 linux/amd64', bun: '1.3.13', tmux: '3.3a', execution: 'make sandbox/make test-sandbox, fresh isolated HOME with per-test owned stores/sockets/processes'},
  deviations: ['One administrative bees show-ticket call used comma-separated IDs, rejected before mutation; corrected to space-separated IDs.',
    'First targeted manifest not captured contemporaneously; transparently reconstructed after known test-only edits. Full before/after manifests are contemporaneous.',
    'Targeted trail selector initially matched zero tests; actual two trail cases explicitly run with correct names before full.',
    'Initial evidence report incorrectly called assertOneSentinel fatal. Read actual helper, corrected to t.Errorf and successful later physical/no-write assertions. Initial report/quality preserved as 05-initial-*-before-fact-correction.json; no test/input change or rerun.'],
  limits: ['Controlled policy fixture makes no real SQL/durability/physical deadline claim.',
    'No local Docker TESTPLAN groups/collector/helper body execution. Installed Bun collector steps authored/static only, require locked dependency network access.',
    'O1-O6 actual human/authenticated terminal acceptance remains pending. Synthetic ACP with actual tmux/public callers is not authenticated terminal evidence.',
    'No arbitrary concurrent SendKeys text+Enter atomicity promise. Audit pending is memory resident, not fsync/crash durable; cooperative native I/O/scheduling limits retained.',
    'Whole architecture coverage remains inherited verified base partitions plus deltas; no fresh whole-file or clean-document certification. Inherited doc debt deferred.'],
  next_handoff: {owner: passed ? 'Root final PM/coordinator commit/fresh Integration Test Writer' : 'Root routes new legacy SQL/COMMIT error-contract failure for fresh Engineer assessment',
    required: passed ? 'Bind local commit full SHA to identical bytes/Git modes. User scheduler owns 17 Docker groups/172 cases, with concrete writable /tmp/cursor-pane-controls-artifacts raw-artifact mount plus standard fixed Docker mounts, image/binary/package/source stamps and installed dependency prerequisites. Makefile has no extra-arguments variable. No unchanged unit rerun after passing.' : 'Assess the new unchanged real-SQL legacy leaf before any necessary correction or full rerun. Preserve exact original budgets/assertions/parallelism and both prior full receipts. No final PM/commit/gate-ready claim. Later postcommit scheduler still needs 17 groups/172 cases and concrete writable /tmp/cursor-pane-controls-artifacts mount, no invented Makefile extra args variable.',
    integration_executed_here: false, task_or_epic_done_here: false},
  formal: 'No spec/tla input change, zero TLA/TLC/Java/parser/scheduler jobs.',
  boundaries: 'No source/docs/registry/testplan edits, .claude access, real AD stores, credentials/login/logout/live Cursor, host repo build/format/tests/scripts, local groups/collectors/helpers, scheduler submissions, commits/pushes, subagents or outbound messages. Only own tests/evidence and own Subtask statuses.',
  incomplete_work: passed ? [] : ['Fresh Engineer assessment of new legacy real-SQL deadline/COMMIT error contract failure.', 'Complete .yj/.zj only after necessary correction and settled full pass; no unchanged retry.', 'No final PM/coordinator commit/scheduler gate closure authorized by this failed receipt.'],
  questions_for_caller: []
};
const receipts = fs.readdirSync(out).filter(p => p !== 'FINAL-report.json' && fs.statSync(out + p).isFile() && !p.startsWith('05-'));
report.receipt_sha256 = Object.fromEntries(receipts.sort().map(p => [p, sha(out + p)]));
write('FINAL-report.json', report);
console.log(JSON.stringify({result: report.result, exit, go: report.full_suite.go, bun: report.full_suite.bun, drift}));
