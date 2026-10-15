import {readFileSync,writeFileSync,readdirSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {correctFinalReport} from './correct-accounting.mjs';
const root='tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/';
const dir=root+'timing-investigation/';
const hash=p=>createHash('sha256').update(readFileSync(p)).digest('hex');
const json=p=>JSON.parse(readFileSync(p,'utf8'));
const before=json(dir+'before-integrity.json'),after=json(dir+'after-integrity.json');
const log=readFileSync(dir+'01-focused-deadline.log','utf8');
const outcomes=[...log.matchAll(/^\s*--- (PASS|FAIL|SKIP): (\S+)/gm)].map(x=>({status:x[1],name:x[2]}));
const leaves=outcomes.filter(n=>!outcomes.some(x=>x.name.startsWith(n.name+'/')));
if(leaves.length!==23||leaves.some(x=>x.status!=='PASS')||!/^PASS$/m.test(log)||/^FAIL/m.test(log)||log.includes('(cached)'))throw Error('Unexpected focused result; preserve evidence and route to Engineer');
if(!before.all_existing_inputs_identical||!after.all_existing_inputs_identical||!before.same_paths||!after.same_paths)throw Error('Input drift');
const final=json(root+'final-tests/FINAL-report.json');
const historical=final.historical_failures_with_unknown_cause;
if(historical.length!==1||historical[0].failures.length!==2)throw Error('Historical accounting incomplete');
if(JSON.stringify(correctFinalReport(structuredClone(final)))!==JSON.stringify(final))throw Error('Accounting correction not idempotent');
const quality=json(root+'final-tests/quality-findings.json');
quality.bounded_followup={command:'make sandbox CMD=\'timeout 180s go test -timeout 2m -count=1 -v ./pkg/api -run "^TestDecideMaxWaitUnderHeldLock$"\'',result:'passed: 23 leaves, uncached, original parallel subcases and bounds unchanged',causality:'Still unknown; focused success does not explain historical 09 violations',report:'../timing-investigation/FINAL-report.json'};
writeFileSync(root+'final-tests/quality-findings.json',JSON.stringify(quality,null,2)+'\n');
const commands=[
 {command:"bees update-ticket --ids t3.jf1.r8.sr.e9 --status active",result:'success; only own quality Subtask reopened'},
 {command:"make sandbox CMD='bun tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/timing-investigation/evidence.mjs before'",exit_code:0,log:'00-before-integrity.log'},
 {command:'make sandbox CMD=\'timeout 180s go test -timeout 2m -count=1 -v ./pkg/api -run "^TestDecideMaxWaitUnderHeldLock$"\'',exit_code:0,log:'01-focused-deadline.log'},
 {command:"make sandbox CMD='bun tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/timing-investigation/evidence.mjs after'",exit_code:0,log:'02-after-integrity.log'},
 {command:"make sandbox CMD='bun tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/timing-investigation/report.mjs'",exit_code:0,log:'03-report-generation.log'}
];
const inspected=['pkg/api/decide_delivery_test.go','pkg/api/decide.go','pkg/api/codex_reporting_test.go','internal/store/lockwait.go','internal/store/permission.go','internal/trail/trail.go'];
const report={
 role:'apiary_test_writer_e3_task2_timing_investigation',
 result:'Bounded focused check passed; historical failure accounting corrected; causal disposition remains unresolved',
 head:after.head,branch:'feature/b.ftd',
 ticket_statuses:{'t3.jf1.r8.sr.e9':'done -> active; remains active pending coordinator technical disposition','t3.jf1.r8.sr.66':'done retained; no full rerun assigned or performed'},
 status_scope:'Only e9 changed. Task2/Epic remain active and Task3 unstarted; no other ticket statuses changed.',
 commands,
 current_check:{scope:'Only TestDecideMaxWaitUnderHeldLock, all original subcases; no other package tests or full suite',runs:1,uncached:true,go_timeout:'2m',outer_timeout:'180s',max_wait_ms:500,asserted_call_bound:'1.5s',parallelism:'Original t.Parallel calls, agent dimensions and busy timeout dimensions retained; no -parallel/-p/GOMAXPROCS override',race:false,passed_leaves:leaves.length,failed_leaves:0,package_result:log.match(/^ok\s+github.com\/gabemahoney\/agent-director\/pkg\/api\s+[^\n]+/m)?.[0],leaves,phase_timing_limit:'Existing test emits detailed phases only on failure; successful leaf durations include setup/cleanup and are not measured Decide call durations. No successful per-phase timing or reached-lock count is claimed.'},
 historical_accounting:historical,
 historical_vs_current_scope:'09 and 13 were whole-repository make test-sandbox runs with cross-package concurrency and all pkg/api top-level tests. This one focused invocation retains every selected test subcase and its concurrency, but does not reproduce the full-suite execution context. Its pass is not a causal experiment that clears 09.',
 source_inspection:{files_sha256:Object.fromEntries(inspected.map(p=>[p,hash(p)])),observations:[
  'deadlineStore times whole delegated methods. Each interval includes acquisition, SQLite work, timeout restoration, connection return and time not scheduled; it cannot separate these components.',
  'boundedConn gives database/sql connection acquisition a context deadline (at least 1ms connGrab) and computes remaining SQLite busy_timeout. These bounds cover waits for connection/SQLite busy, not a guaranteed wall-clock bound on every statement or goroutine scheduling.',
  'withConn uses context.Background for statements and deferred busy_timeout restoration. inWriteTx executes BEGIN IMMEDIATE, query, COMMIT and any rollback on that context; neither SQL work nor restoration/connection cleanup has a call-deadline context here.',
  'DecideRelayRequest calls emitDecisionCommitted synchronously after inWriteTx returns. trail.Emit takes the singleton Writer mutex and writes and Syncs; Writer.Emit ignores its context. The whole DecideRelayRequest observation includes this postcommit work.',
  'Both historical failures have total real sleep zero and no callback-enter/holder-ready phase. In these lockLater cases the only intended competing lock acquisition is inside that callback, so it was not reached. Neither historical deadline violation can be attributed to that intended fixture lock.',
  'Historical max busy leaf: GetPermissionRequestWithin took 1.196699792s for requested 297.557663ms, then 1.1007649s for requested zero; DecideRelayRequest took 3.100305205s for requested zero. Busy 10s leaf: DecideRelayRequest took 2.306268377s for requested 495.206843ms. These are observed method durations, not SQL busy durations.',
  'No connection-acquisition/SQL/COMMIT/cleanup/trail/scheduler split exists in the historical receipt. Source inspection establishes possible contributors, not which contributor caused either failure.'
 ],candidate_causes:'SQL execution/COMMIT or filesystem latency, cleanup/timeout restoration, synchronous shared-trail mutex/write/Sync, scheduling delays, and connection acquisition overhead remain candidates only. No load-only, trail-only, SQLite-only or new rotation-defect conclusion is supported.'},
 final_full_suite_retained:{command:'make test-sandbox',log:'../final-tests/13-full-test-sandbox.log',sha256:after.historical_logs['13-full-test-sandbox.log'].sha256,result:'passed',go_packages_passed:68,go_packages_cached:62,go_packages_without_tests:13,bun_passed:429,bun_failed:0,new_full_runs:0},
 integrity:{count:after.manifest_count,manifest:'../final-tests/settled-input-manifest.json',manifest_sha256:after.settled_manifest_sha256,before:'before-integrity.json',after:'after-integrity.json',all_2300_existing_inputs_identical_before_and_after:true,identical_path_inventory:true,scope:'All original tracked/nonignored source/tests/docs/build inputs. Excludes .claude before any read/hash and tickets/handoff metadata. No persistent source/test/doc changes; new files are handoff evidence metadata only.',historical_logs:after.historical_logs},
 corrected_files:['../final-tests/FINAL-report.json','../final-tests/quality-findings.json','../final-tests/report.mjs'],
 generator_preservation:'Original final-tests/report.mjs now calls correctFinalReport from this directory, preserving explicit historical unknown causes on regeneration; correction is idempotent.',
 receipt_sha256:Object.fromEntries(readdirSync(dir).filter(p=>p.endsWith('.log')&&p!=='03-report-generation.log').map(p=>[p,hash(dir+p)])),
 deviations:[],
 integration:'NOT RUN; not dispatched as integration Test Writer. No Docker groups/collectors or scheduler execution.',
 formal:'No spec/tla changes; no formal jobs, Java, TLC or parser execution.',
 investigation_limits:'No runtime trace, ephemeral diagnostic test copy, source instrumentation, store-test expansion, host load probe, artificial stress, sleep, deadline widening or retries. Existing failure phases suffice to bound the evidence without changing execution inputs.',
 recommended_disposition:'Coordinator must explicitly decide technical treatment of the two historical deadline violations before quality closure. Retain final 13 pass as full-suite evidence and keep the failure cause unknown. If root requires a proven cause before acceptance, route scoped Engineer work to separate connection/SQL/COMMIT/cleanup/trail/scheduling timing; do not silently clear failures, relax assertions or rerun broadly.',
 incomplete_work:['Historical timing cause is unresolved; e9 remains active pending coordinator technical disposition.'],
 genuine_blockers:[],questions_for_caller:[]
};
writeFileSync(dir+'FINAL-report.json',JSON.stringify(report,null,2)+'\n');
console.log(JSON.stringify({result:report.result,current_check:report.current_check,integrity:report.integrity,ticket_statuses:report.ticket_statuses},null,2));
