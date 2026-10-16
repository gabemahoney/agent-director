import {readFileSync,writeFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
const dir='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/legacy-commit-assessment';
const read=p=>readFileSync(dir+'/'+p,'utf8');
const hash=p=>createHash('sha256').update(readFileSync(dir+'/'+p)).digest('hex');
const logs=['02-targeted-race.log','03-boundaries-vet-integrity.log'];
const results=Object.fromEntries(logs.map(p=>{const text=read(p);const all=[...text.matchAll(/^\s*--- PASS: (\S+)/gm)].map(m=>m[1]);const leaves=all.filter(n=>!all.some(other=>other.startsWith(n+'/')));const failures=text.split('\n').filter(s=>/--- FAIL:|^FAIL|WARNING: DATA RACE/.test(s));if(failures.length)throw new Error('failed receipt '+p);return[p,{sha256:hash(p),passed_leaves:leaves.length,leaves,packages:text.split('\n').filter(s=>s.startsWith('ok  ')),failures}]}));
const before=JSON.parse(read('input-manifest-before.json'));const after=JSON.parse(read('input-manifest-after.json'));
const report={
 role:'apiary_engineer_e3_task3_legacy_commit_assessment',
 result:'Real no-match transaction defect repaired with one minimal source change; existing targeted race checks pass. Independent deterministic no-match regression and later settled full-suite run remain caller-owned.',
 tickets:{'t3.jf1.r8.a8.ir':'done -> active -> done'},status_scope:'Only own source Subtask .ir changed; no other ticket status changed.',
 files_changed:['internal/store/permission.go'],
 source_before:before['internal/store/permission.go'],source_after:after['internal/store/permission.go'],
 assessment:{
  retained_failure:'contract-final-tests/04-full-test-sandbox.log: unchanged legacy actual SQLite case expected ErrStoreBusy, observed commit outcome unconfirmed wrapping context deadline exceeded. Subsequent no-sleep, original 100ms+1s physical and independent no-decision/no-attempt assertions reported no failures. Subtest 1.53s includes setup; no phase trace proves actual COMMIT duration or historical09 cause.',
  defect:'DecideRelayRequest converted sql.ErrNoRows from guarded UPDATE RETURNING to nil inside inWriteTx. That instructed the helper to attempt COMMIT for a transaction with no matched verdict. Deadline expiry during that unnecessary COMMIT was correctly classified unconfirmed by the helper but prevented the caller from naming the known guarded refusal.',
  fix:'Return sql.ErrNoRows from the callback, invoking existing precommit rollback/quarantine cleanup. After the helper, convert the no-match sentinel to (false,nil) only if the helper did not also classify precommit context expiry as ErrStoreBusy. There is no COMMIT attempt for a successful no-match observation. The API performs its existing follow-up reads and legacy settling refusal.',
  safety:'No earlier API refusal based on a stale read. Guarded UPDATE, BEGIN IMMEDIATE and first-writer atomicity are unchanged. The zero-row path never sets written or emits a committed mutation. Existing rollback/restoration and expired-disposal quarantine remain authoritative. A matched UPDATE still reaches the same COMMIT; every possibly written COMMIT error remains internal unconfirmed. A precommit expired callback keeps ErrStoreBusy, with the original underlying error.',
  scope:'No helper-wide speculative error reclassification, deadline enlargement, reduced parallelism, assertion weakening, SQL guard change, public signature/schema/model change or fixture distortion.'
 },
 technical_decision_for_srd:'A guarded decision UPDATE that returns no row is a precommit no-match outcome: roll its transaction back rather than COMMIT. Return false,nil to allow existing race-aware refusal classification when the callback/operation is still valid; retain helper ErrStoreBusy on precommit expiry. Only potentially applied verdicts attempt COMMIT and retain unconfirmed-outcome handling on COMMIT error.',
 independent_test_handoff:{
  seam:'Use existing internal/store/deadline_boundary_test.go installDeadlineBoundary/ObserveDecisionBoundaryForTest around actual SQLite. Arrange a legacy cutoff mismatch (or other explicit guarded nonmatch), count BEGIN/UPDATE/ROLLBACK/COMMIT; a COMMIT hook should fail or cancel if reached. Assert (false,nil), zero COMMIT, one guarded UPDATE and rollback, no durable decision/attempt/mutation event, restored PRAGMA and reusable connection after awaiting cleanup through the existing pool-lease helper. Run a separate controlled cancellation during no-match SQL or rollback to exercise original deadline/quarantine, retaining original physical bounds.',
  red_reason:'On source before this patch, no-match callback returns nil and reaches the fail-on-COMMIT hook; repaired source never reaches it. This adds deterministic coverage for the exact no-match branch without relying on 100ms host-load timing.',
  preserve:'Keep original parallel actual-SQL TestDecideMaxWaitUnderHeldLock legacy case with 100ms/no-sleep/no-decision/no-attempt and 1.1s physical guard unchanged. Keep protected blocked-trail test bytes, 500ms and 1.5s. Keep real committed-response boundary case proving durable allow plus unconfirmed error, never Busy.'
 },
 doc_implications:{owner:'One later batched DocWriter pass',required:'Add narrow architecture note near bounded inWriteTx users / DecideRelayRequest step: guarded no-match rolls back without COMMIT, then existing follow-up refusal classification applies. Existing COMMIT ambiguity and caller retry advice remain intact. No caller-visible contract change; user docs need no new error or budget.'},
 commands:[
  {command:'make sandbox CMD=\'bun tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/legacy-commit-assessment/evidence.mjs before\'',log:'01-before.log',exit_code:0},
  {command:'make sandbox CMD=\'gofmt -w internal/store/permission.go && go test -race -timeout 150s -count=1 -v ./internal/store ./pkg/api -run "^(TestDecideRelayRequestGuards|TestRelayWritePrecondition|TestDecision|TestAdviceFollow_F10_UnconfirmedCommitInspectBeforeRetry|TestDecideMaxWaitUnderHeldLock|TestDecideMaxWaitWithBlockedTrail|TestDecideAbsoluteContextSurvivesParkedPhase|TestClientDecideBoundsDeferredAuditAndPreservesFIFO)$"\'',log:logs[0],exit_code:0},
  {command:'make sandbox CMD=\'go test -race -timeout 90s -count=1 -v ./internal/store -run "^TestDecision" && go vet ./internal/store && git diff --check && bun tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/legacy-commit-assessment/evidence.mjs after\'',log:logs[1],exit_code:0}
 ],
 targeted_results:results,
 integrity:{inputs:484,manifests:['input-manifest-before.json','input-manifest-after.json'],only_changed_input:'internal/store/permission.go',reviewed_complete_reports_and_raw_full_log:'reviewed-inputs-before.json / reviewed-inputs-after.json (11 complete files read and hash-bound)',protected_regression_sha256:'032950e86d9919e2c48ae79189751e36f8875312a78bf1410973954fdde86d88',tests_and_docs_changed:[]},
 deviations:['First targeted selector accidentally anchored TestDecision as a whole literal, so Store TestDecision* boundary cases were not selected there. Corrected with a separate prefix-only Store run; no unchanged failed test rerun.'],
 failures:[],
 limits:['This lane did not reproduce the full-suite scheduling condition or claim its exact native COMMIT timing; static control flow establishes the unnecessary no-match COMMIT and the existing unchanged legacy test passes after actual source repair.','No full suite run here. Earlier failed receipts/manifests remain unmodified. Historical09 causes remain unknown.','Cancellation remains cooperative; process descheduling/native uninterruptible I/O limits unchanged. Accepted audit pending remains memory resident, not crash durable.'],
 boundaries:'All repository execution, formatting, tests, vet and evidence scripts through make sandbox with fresh container HOME and per-test isolated stores/sockets/owned processes. No tests/docs edits, .claude access, real AD stores, credentials/login/logout/live Cursor, host repository execution, local Docker groups/collectors/helpers, scheduler jobs, commits/pushes, subagents or outbound messages. Parent-only technical progress reports sent.',
 formal:'No spec/tla changes; zero TLA/TLC/Java/parser/scheduler jobs.',
 incomplete_work:['Independent deterministic no-match regression, source review and one later batched documentation note; necessary settled full run by sole full-suite owner.'],
 questions_for_caller:[]
};
writeFileSync(dir+'/FINAL-report.json',JSON.stringify(report,null,2)+'\n');
console.log(JSON.stringify({report:dir+'/FINAL-report.json',results:Object.fromEntries(Object.entries(results).map(([p,r])=>[p,r.passed_leaves]))}));
