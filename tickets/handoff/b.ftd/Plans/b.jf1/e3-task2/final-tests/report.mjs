import {readFileSync,writeFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {execFileSync} from 'node:child_process';
import {correctFinalReport} from '../timing-investigation/correct-accounting.mjs';
const dir='tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/final-tests/';
const read=p=>readFileSync(dir+p,'utf8');
const json=p=>JSON.parse(read(p));
const hash=p=>createHash('sha256').update(readFileSync(p)).digest('hex');
const commands={
 '00-integrity.log':"make sandbox CMD='bun tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/final-tests/evidence.mjs before'",
 '01-advice-focused.log':'make sandbox CMD=\'gofmt -w internal/cursorhost/session_rotation_boundary_linux_test.go internal/cursorhost/session_rotation_linux_test.go test/realtmux/cursor_failure_linux_test.go && env GORACE=atexit_sleep_ms=0 go test -race -timeout 15m -count=1 -v ./internal/cursorhost ./test/realtmux -run "TestCursor(RotationHeldCapacityIsProvisional|RotationPublicationAndEveryHoldPrecedePromotion|RotationFailureRefusesEveryHeldInput|PersistenceFailureStopsAdmission/rotation_publication)$"\'',
 '02-failed-rotation-follow.log':'make sandbox CMD=\'env GORACE=atexit_sleep_ms=0 go test -race -timeout 15m -count=1 -v ./test/realtmux -run "^TestCursorPersistenceFailureStopsAdmission$/^rotation_publication$"\'',
 '03-settled-integrity.log':"make sandbox CMD='bun tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/final-tests/evidence.mjs settled'",
 '04-full-test-sandbox.log':'make test-sandbox',
 '05-initial-failure-integrity.log':"make sandbox CMD='bun tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/final-tests/evidence.mjs initial-after && cp tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/final-tests/settled-input-manifest.json tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/final-tests/initial-settled-input-manifest.json && cmp tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/final-tests/initial-settled-input-manifest.json tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/final-tests/initial-after-input-manifest.json'",
 '06-refill-synchronization.log':'make sandbox CMD=\'gofmt -w internal/cursorhost/session_capacity_linux_test.go && env GORACE=atexit_sleep_ms=0 go test -race -timeout 15m -count=1 -v ./internal/cursorhost -run "^TestCursorWaitingMailboxByteBoundaries$"\'',
 '07-refill-consumption-boundary.log':'make sandbox CMD=\'gofmt -w internal/cursorhost/session_capacity_linux_test.go && env GORACE=atexit_sleep_ms=0 go test -race -timeout 15m -count=1 -v ./internal/cursorhost -run "^TestCursorWaitingMailboxByteBoundaries$"\'',
 '08-final-settled-integrity.log':"make sandbox CMD='bun tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/final-tests/evidence.mjs settled'",
 '09-full-test-sandbox.log':'make test-sandbox',
 '10-second-failure-integrity.log':"make sandbox CMD='bun tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/final-tests/evidence.mjs second-after && cp tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/final-tests/settled-input-manifest.json tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/final-tests/second-settled-input-manifest.json && cmp tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/final-tests/second-settled-input-manifest.json tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/final-tests/second-after-input-manifest.json'",
 '11-notice-synchronization.log':'make sandbox CMD=\'gofmt -w internal/cursorhost/session_rotation_boundary_linux_test.go && env GORACE=atexit_sleep_ms=0 go test -race -timeout 15m -count=1 -v ./internal/cursorhost -run "^TestCursorRotationHeldCapacityIsProvisional$"\'',
 '12-final-settled-integrity.log':"make sandbox CMD='bun tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/final-tests/evidence.mjs settled'",
 '13-full-test-sandbox.log':'make test-sandbox',
 '14-after-integrity.log':"make sandbox CMD='bun tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/final-tests/evidence.mjs after'"
};
const full=read('13-full-test-sandbox.log');
const go=full.split('\n').filter(x=>/^ok\s+github.com\//.test(x));
const cached=go.filter(x=>x.includes('(cached)'));
const noTests=full.split('\n').filter(x=>/^\?\s+github.com\//.test(x));
const bun=full.match(/(\d+) pass\s*\n\s*(\d+) fail\s*\n\s*(\d+) expect\(\) calls\s*\nRan (\d+) tests across (\d+) files/);
if(!bun||Number(bun[2])!==0||/^FAIL/m.test(full)||/WARNING: DATA RACE/.test(full))throw Error('Full receipt not successful');
if(read('settled-input-manifest.json')!==read('after-input-manifest.json'))throw Error('Input drift during full suite');
if(read('before-source-doc-integrity.json')!==read('after-source-doc-integrity.json'))throw Error('Source/docs drift');
const targeted={};
for(const file of ['01-advice-focused.log','02-failed-rotation-follow.log','06-refill-synchronization.log','07-refill-consumption-boundary.log','11-notice-synchronization.log']){
 const log=read(file); const outcomes=[...log.matchAll(/^\s*--- (PASS|FAIL|SKIP): (\S+)/gm)].map(x=>({status:x[1],name:x[2]}));
 const leaves=outcomes.filter(n=>!outcomes.some(other=>other.name.startsWith(n.name+'/')));
 targeted[file]={passed_leaves:leaves.filter(x=>x.status==='PASS').length,failed_leaves:leaves.filter(x=>x.status==='FAIL').length,race_warnings:[...log.matchAll(/WARNING: DATA RACE/g)].length,leaves};
}
const changed=['internal/cursorhost/session_rotation_boundary_linux_test.go','internal/cursorhost/session_rotation_linux_test.go','test/realtmux/cursor_failure_linux_test.go','internal/cursorhost/session_capacity_linux_test.go'];
const initial=read('04-full-test-sandbox.log');
const initialGo=initial.split('\n').filter(x=>/^ok\s+github.com\//.test(x));
const initialCached=initialGo.filter(x=>x.includes('(cached)'));
const report={
 role:'apiary_test_writer_e3_task2_final',result:'passed: all assigned advice/quality/full unit subtasks complete',
 head:execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim(),branch:execFileSync('git',['branch','--show-current'],{encoding:'utf8'}).trim(),
 ticket_statuses:{'t3.jf1.r8.sr.78':'defined -> active -> done','t3.jf1.r8.sr.e9':'active -> active -> done','t3.jf1.r8.sr.66':'defined -> active -> done'},
 statuses_limit:'Only these own subtasks changed; Task2/E3 remain active, Task3 unstarted, E2 Epic intentionally active.',
 files_changed:changed, test_sha256:Object.fromEntries(changed.map(p=>[p,hash(p)])),
 source_docs_integrity:{files:15,unchanged:true,manifest:'after-source-doc-integrity.json',sha256:hash(dir+'after-source-doc-integrity.json')},
 full_suite:{command:'make test-sandbox',result:'passed after fixing test-side refill and pane-notice synchronization',exit_code:0,runs:3,go_packages_passed:go.length,go_packages_cached:cached.length,go_packages_executed:go.length-cached.length,go_packages_without_tests:noTests.length,bun:{passed:Number(bun[1]),failed:Number(bun[2]),expects:Number(bun[3]),tests:Number(bun[4]),files:Number(bun[5])},unresolved_failures:[],log:'13-full-test-sandbox.log',sha256:hash(dir+'13-full-test-sandbox.log'),count_limit:'Go default output reports package totals, not Go test/leaf counts; Bun totals are exact emitted summary. Initial full run and first corrected rerun failed on distinct test-side observation boundaries; each retained and corrected with focused causal checks before necessary full rerun. No additional run after pass.'},
 initial_full_suite:{log:'04-full-test-sandbox.log',exit_code:2,go_packages_passed:initialGo.length,go_packages_cached:initialCached.length,go_packages_executed_and_passed:initialGo.length-initialCached.length,go_packages_failed:initial.split('\n').filter(x=>/^FAIL\s+github.com\//.test(x)).length,go_packages_without_tests:initial.split('\n').filter(x=>/^\?\s+github.com\//.test(x)).length,bun:'429 pass, 0 fail, 1659 expects, 57 files',input_manifest:'initial-settled-input-manifest.json',sha256:hash(dir+'initial-settled-input-manifest.json')},
 tested_inputs:{count:json('settled-input-manifest.json').length,manifest:'settled-input-manifest.json',sha256:hash(dir+'settled-input-manifest.json'),post_suite_manifest:'after-input-manifest.json',identical_after_suite:true,scope:'All tracked and nonignored untracked repository files excluding .claude before any file read/hash and tickets/handoff receipt/metadata artifacts. Includes all source/test/docs/build inputs; excludes gitignored build outputs, node_modules, receipts. All hashes generated inside sandbox.'},
 advice:'advice-inventory-mapping.json',quality:'quality-findings.json',targeted,
 receipts:Object.fromEntries(Object.entries(commands).map(([file,command])=>[file,{command,sha256:hash(dir+file),result:['04-full-test-sandbox.log','06-refill-synchronization.log','09-full-test-sandbox.log'].includes(file)?'failed; retained, see cause':'passed'}])),
 failed_receipts:[{log:'04-full-test-sandbox.log',exit_code:2,failed_test:'TestCursorWaitingMailboxByteBoundaries/aggregate/+0',cause:'First refill peer wait expired after accepting the entire 8MiB batch; observer included preceding batch protected I/O/hook setup in a single per-event 3s wait. No source error or race warning observed. Corrected real consumption/acceptance synchronization, all original byte/FIFO/cancellation/raw-byte assertions retained. This does not claim forensic CPU/disk timing certainty from an uninstrumented failure.',inputs:'initial-settled-input-manifest.json',post_inputs:'initial-after-input-manifest.json',input_drift:false,bun:'429 pass, 0 fail, 1659 expects, 57 files'}, {log:'06-refill-synchronization.log',exit_code:2,failed_tests:['TestCursorWaitingMailboxByteBoundaries/aggregate/-1','TestCursorWaitingMailboxByteBoundaries/aggregate/+0'],cause:'First test-only correction waited between insertions, so dispatch legitimately began before later real admission checks; i.submit refused consumption-time authority. Corrected test consumes the full batch through actual admission before any insertion, then inserts same values and observes acceptances. No source/assertion/deadline change.'}],operational_notes:['01 compiled realtmux but its combined Go -run expression matched no tests there; explicit correctly scoped 02 ran the required real-pane leaf successfully.','Read-only discovery encountered absent optional history/spawn-fixture paths; used actual get.go/spawn_test.go owners. No repo execution outside sandbox.'],
 inherited_failure_evidence:'All prior engineer/behavior/revalidation/prestart red receipts remain in their original directories, with causes/dispositions retained in those FULL reports. No failure deleted, no source edit/restoration, no assertion/deadline weakening in this pass.',
 integration:'NOT RUN; not dispatched as integration Test Writer. Required Docker groups/collectors belong to user scheduler exact-revision gate; not acceptance here.',
 model_assessment:'TLA+ not applicable: no spec/tla changes. Zero formal/baseline jobs; existing models do not prove new rotation/observation/admission properties.',
 proof_limits:['Exact full Run pre-Start micro-window not reproduced: reporter causal test plus source branch inspection only.','Synthetic ACP with actual real-tmux/production CLI/hook evidence remains synthetic; no authenticated Cursor or human attach acceptance.','No Task3 public collector/caller parity claim; no arbitrary concurrent SendKeys atomicity promise.'],
 execution_boundary:'All repository format/test/build/evidence scripts through make sandbox CMD or make test-sandbox. Isolated container HOME plus fixture stores/sockets/processes. No .claude reads/access, real AD stores, credentials/login/logout/live Cursor, external Docker TESTPLAN groups/collectors, scheduler/TLC/Java/parser, source/docs edits, commits/pushes, delegation or agent messages. Bees CLI only metadata read and own status updates; host read-only discovery/edit allowed.',
 deviations:[],incomplete_work:[],remaining_downstream:'Coordinator local commit and exact-commit user scheduler interface acceptance before Task3; Task/Epic closure not owned here.',questions_for_caller:[]
};
report.failed_receipts.push({log:'09-full-test-sandbox.log',exit_code:2,failed_test:'TestCursorRotationHeldCapacityIsProvisional/individual',cause:'New exact-phrase assertion sampled the asynchronous pane immediately after the protected refusal record became visible; record writing precedes pane delivery. Replaced immediate sample with existing bounded c.wait for exact same phrase. No source, expected phrase, assertion, or deadline weakened.',inputs:'second-settled-input-manifest.json',post_inputs:'second-after-input-manifest.json',input_drift:false,focused_green:'11-notice-synchronization.log'});
const second=read('09-full-test-sandbox.log');
const secondGo=second.split('\n').filter(x=>/^ok\s+github.com\//.test(x));
report.second_full_suite={log:'09-full-test-sandbox.log',exit_code:2,go_packages_passed:secondGo.length,go_packages_cached:secondGo.filter(x=>x.includes('(cached)')).length,go_packages_failed:second.split('\n').filter(x=>/^FAIL\s+github.com\//.test(x)).length,go_packages_without_tests:second.split('\n').filter(x=>/^\?\s+github.com\//.test(x)).length,bun:'429 pass, 0 fail, 1659 expects, 57 files',input_manifest:'second-settled-input-manifest.json',sha256:hash(dir+'second-settled-input-manifest.json')};
correctFinalReport(report);
writeFileSync(dir+'FINAL-report.json',JSON.stringify(report,null,2)+'\n');
console.log(JSON.stringify({full_suite:report.full_suite,tested_inputs:report.tested_inputs,targeted},null,2));
