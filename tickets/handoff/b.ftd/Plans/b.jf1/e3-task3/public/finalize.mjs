import {readFileSync,writeFileSync,readdirSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {spawnSync} from 'node:child_process';

const base='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/public';
const prior='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3';
const json=p=>JSON.parse(readFileSync(p,'utf8'));
const hash=p=>createHash('sha256').update(readFileSync(p)).digest('hex');
const run=(command,args)=>{const r=spawnSync(command,args,{encoding:'utf8'});if(r.status!==0)throw Error(`${command}: ${r.stderr}`);return r.stdout.trim();};
const repaired=json(`${prior}/deadline-review-fix/FINAL-report.json`);
const red=json(`${prior}/deadline-red/FINAL-report.json`);
const old=json(`${prior}/deadline-red/old-production-receipt.json`);
const expected=repaired.source_after_sha256;
for(const [path,digest] of Object.entries(expected))if(hash(path)!==digest)throw Error(`settled source drift: ${path}`);
const changedOld=[];
for(const [path,digest] of Object.entries(old.production_manifest)){
 if(path.startsWith('.claude/')||path.startsWith('tickets/'))throw Error('forbidden path in old manifest');
 const current=hash(path);
 if(current!==digest){changedOld.push(path);if(expected[path]!==current)throw Error(`unexpected production change: ${path}`);}
}
if(hash('pkg/api/decide_trail_deadline_test.go')!==red.test_sha256)throw Error('causal regression changed');
const own=['internal/store/deadline_boundary_test.go','test/realtmux/cursor_pane_controls_test.go','test/realtmux/cursor_pane_controls_boundary_linux_test.go'];
const tests=[];
for(const line of readFileSync(`${prior}/deadline-review-fix/verified-tests.sha256`,'utf8').trim().split('\n')){
 const m=/^([a-f0-9]{64})\s+(.+)$/.exec(line);if(!m)throw Error(`invalid baseline: ${line}`);
 if(m[2].startsWith('.claude/'))throw Error('forbidden path in test baseline');
 if(!own.includes(m[2])&&hash(m[2])!==m[1])throw Error(`other writer test changed: ${m[2]}`);
 tests.push(m[2]);
}
const receipts={};
for(const name of readdirSync(base).filter(p=>/^\d\d-.*\.log$/.test(p))){
 const raw=readFileSync(`${base}/${name}`,'utf8');
 const passed=[...raw.matchAll(/^\s*--- PASS: (\S+) /gm)].map(m=>m[1]);
 const leaves=passed.filter(n=>!passed.some(o=>o.startsWith(n+'/')));
 receipts[name]={sha256:hash(`${base}/${name}`),passed_leaves:leaves.length,leaves,packages:[...raw.matchAll(/^ok\s+(.+)$/gm)].map(m=>m[1]),failure_lines:raw.split('\n').filter(l=>/^\s*--- FAIL:|^FAIL|\[build failed\]|^test\/realtmux\/.*cannot use|timed out after|missing submission\/request\/outcome|tools\/call failed|failed input gained/.test(l))};
}
for(const name of ['01-d1-boundaries.log','06-public-mixed-attribution.log','08-public-mcp-errors-terminal-evidence.log','09-final-public-race.log'])if(receipts[name].failure_lines.length||!receipts[name].passed_leaves)throw Error(`required final receipt failed: ${name}`);
writeFileSync(`${base}/source-integrity.json`,JSON.stringify({expected_settled_source_sha256:expected,all_match:true,old_manifest_count:Object.keys(old.production_manifest).length,old_paths_changed_only_by_engineer:changedOld,new_engineer_paths:Object.keys(expected).filter(p=>!(p in old.production_manifest)),unchanged_causal_regression_sha256:hash('pkg/api/decide_trail_deadline_test.go'),other_test_baseline_verified:tests.length-own.filter(p=>tests.includes(p)).length,own_test_sha256:Object.fromEntries(own.map(p=>[p,hash(p)]))},null,2)+'\n');
const deadlinePattern='^(TestDecisionSetupPragmaFailureBeforeBegin|TestDecisionAbsoluteDeadlineAtSQLiteBoundaries|TestDecisionCleanupQuarantinesNoncancelableClose|TestDecisionExpiredContextCannotRenewAcquisitionOrZeroWait|TestDecisionLateAcquisitionRetainsOriginalSQLDeadline|TestDecisionZeroWaitAllowsSQLBeyondAcquisitionAllowance|TestDecideMaxWaitWithBlockedTrail)$';
const mixed='^TestCursorPaneControlsPublicMixedInputAndRecreation$';
const commands=[
 {command:`make sandbox CMD='gofmt -w internal/store/deadline_boundary_test.go && timeout 90s go test -race -timeout 75s -count=1 -v ./internal/store ./pkg/api -run "${deadlinePattern}"'`,log:'01-d1-boundaries.log',exit:0},
 ...[['02-public-mixed.log',2,'180s','160s'],['03-public-mixed-typed-observer.log',2,'180s','160s'],['04-public-mixed-request-correlation.log',2,'180s','160s'],['05-public-mixed-ready-boundary.log',0,'180s','160s'],['06-public-mixed-attribution.log',0,'90s','75s']].map(([log,exit,outer,inner])=>({command:`make sandbox CMD='gofmt -w test/realtmux/cursor_pane_controls_test.go && timeout ${outer} go test -timeout ${inner} -count=1 -v ./test/realtmux -run "${mixed}"'`,log,exit})),
 {command:'make sandbox CMD=\'gofmt -w test/realtmux/cursor_pane_controls_boundary_linux_test.go && timeout 150s go test -timeout 130s -count=1 -v ./test/realtmux -run "^TestCursorPaneControlsPublic(MixedQueueRetirement|IndependentHoldsAcrossRecreation|FailureInspection)$"\'',log:'07-public-adversarial.log',exit:2},
 {command:'make sandbox CMD=\'gofmt -w test/realtmux/cursor_pane_controls_test.go test/realtmux/cursor_pane_controls_boundary_linux_test.go && timeout 150s go test -timeout 130s -count=1 -v ./test/realtmux -run "^TestCursorPaneControlsPublic(MixedInputAndRecreation|MixedQueueRetirement|IndependentHoldsAcrossRecreation|FailureInspection)$"\'',log:'08-public-mcp-errors-terminal-evidence.log',exit:0},
 {command:'make sandbox CMD=\'gofmt -w test/realtmux/cursor_pane_controls_boundary_linux_test.go && timeout 120s go test -race -timeout 100s -count=1 -v ./test/realtmux -run "^TestCursorPaneControlsPublic"\'',log:'09-final-public-race.log',exit:0},
 {command:`make sandbox CMD='timeout 90s go vet ./internal/store ./test/realtmux && git diff --check && go version && bun --version && bun ${base}/finalize.mjs'`,log:'10-vet-integrity.log',purpose:'Vet and final integrity/evidence; no tests'}
];
const report={
 role:'apiary_test_writer_e3_task3_public',
 result:'D1 independently GREEN; genuine four-surface public mixed-input/recreation and representative adversarial public compositions settled GREEN under race detector.',
 head:run('git',['rev-parse','HEAD']),branch:run('git',['branch','--show-current']),
 ticket_statuses:{'t3.jf1.r8.a8.yt':'done -> active -> done (D1 revalidation)','t3.jf1.r8.a8.wn':'defined -> active -> done','t3.jf1.r8.a8.8u':'defined -> active -> done'},
 status_scope:'Only own .yt/.wn/.8u; no Task/Epic/other Subtask changes.',
 files_changed:own,
 evidence_directory:base,commands,receipts,
 d1_case_verification:{case:'TestDecisionSetupPragmaFailureBeforeBegin',boundary:'Existing deadlineBoundary/execHookConnector over the real SQLite driver; hook returns wrapped context.Canceled, context.DeadlineExceeded, or ordinary setup I/O error while context canceled at actual PRAGMA busy_timeout = 0.',assertions:'One setup, zero BEGIN/COMMIT; both context failures preserve underlying error and ErrStoreBusy. Ordinary I/O error preserves ordinary classification despite expired context. Original bounded return; independent SQLite no decision; replacement connection has restored busy_timeout and supports BEGIN/ROLLBACK; opens=2 proves failed connection discarded. Existing actual postcommit uncertainty case rerun unchanged.',race:'01-d1-boundaries.log',unchanged_causal_regression_sha256:red.test_sha256,causal_observations:readFileSync(`${base}/01-d1-boundaries.log`,'utf8').split('\n').filter(l=>l.includes('independent connection observed')||l.includes('decide finished after')),historical09_cause:'Unknown; no claim to reconstruct it.'},
 public_boundary_proof:{
 CLI:'Built production agent-director invoked with real argv and explicit --store-path for send-keys/get/read-pane/pause/find-missing. Each call exits; replacement caller uses same live gateway.',
 MCP:'Actual built agent-director serve --stdio, JSON-RPC initialize/tools/call; process stop/wait and replacement process while active/queued or rotation provisional. Reads genuine error responses without turning expected refusal into helper failure.',
 Go:'Actual api.New Client.SendKeys/Get/ReadPane/GetPermission/Pause; Client.Close then new Client with same explicit store. No fake API implementation.',
 Bun:'Bun.build of package public src/index.ts export using same target/entrypoint as package build; imported generated JavaScript in a real Bun subprocess. Actual Client.create/sendKeys/get/readPane/getPermission/pause uses production CLI subprocess serialization and default client timeouts. Process killed/waited and new Bun process creates fresh client. This is built public-export coverage, not an npm-packed/installed tarball test.',
 transport_vs_admission:'Successful send-keys only proves tmux transport. Tests separately await protected submission_accepted, verify actual ACP session/prompt, correlate unique submission/request IDs to submission_started and turn_outcome, and inspect independent SQLite/process evidence. Waiting for turn_outcome alone was correctly replaced by committed waiting + explicit completion notice before the next idle prompt.',
 recreation:'Gateway pane PID/starttime, runtime PID, launch token, session and queue survive caller replacement; held active turn not canceled by follow-ups; mixed terminal/public exact Unicode/multiline FIFO five prompts and outcomes per caller. Same-ID default-store decoy unchanged.',
 rotation:'Each caller sends /clear and /new; independent current-life history and public get agree; life/token/path/inode unchanged; old identity remains history. No load/reuse/switch/older-history migration.',
 controls:'MCP mixed accepted queue + native interrupt; Bun mixed accepted queue + /new; CLI mixed accepted queue + /exit. Every queued ID cancelled exactly once, visible via actual public read-pane, never later started/outcome/raw prompt; unrelated tmux sentinel survives. Actual eligible pause finishes the surviving launch.',
 independent_holds:'Captured permission+plan envelopes and source-derived question attempts through synthetic ACP. MCP public request identity and unknown-origin proof eligibility survive actual caller restart, denial of all questions, terminal cancel, completion, and new session; public pause refuses and guarded input has no raw prompt bytes.',
 disconnected_send:'Separate actual MCP caller writes tools/call then never consumes its response. Independent transcript proves queued admission; caller process dies, replacement inspects without retrying; original queue dispatches/completes exactly once. This proves an unobserved caller outcome, not a forced partial network write.',
 failures:'MCP rejected rotation and Bun archive write failure: process replacement during unconfirmed session/new preserves old identity; provisional inputs individually refused, no accepted/dispatched/raw bytes, no fabricated end/history. CLI terminal loss inspects then process-only find-missing reconciles dead launch; any durable end record requires real owned runtime death. Go protected-file permission failure admits no prompt and cannot claim ended. All accepting owned processes stop; unrelated sentinels survive.'
 },
 coverage_existing_vs_new:{
 retained:[
 'internal/cursorhost/session_controls_linux_test.go: FIFOExactTextAndLifecycle; ControlsRetireQueuedSubmissions; queued holds/authority loss; saturation/report barriers; partial/zero-byte retirement.',
 'internal/cursorhost/session_rotation_linux_test.go and session_rotation_boundary_linux_test.go: alias active/idle matrix; every proof/hold before promotion; failure/reused/malformed identity; stale callbacks/traffic; capacity; cancellation deadlines; publication/sync phases and retired input.',
 'test/realtmux/cursor_relay_test.go, cursor_relay_pane_test.go, cursor_relay_boundary_linux_test.go: captured schema kinds, real four-surface decisions, unknown provenance, question-policy matrix, acknowledgement-before-byte and finite answer/cancel boundaries.',
 'test/realtmux/cursor_failure_linux_test.go, cursor_blocked_linux_test.go, cursor_queue_linux_test.go: focused persistence matrix, blocked transport uncertainty, exact owned shutdown/unrelated workers, working-pause refusal.',
 'pkg/api/sendkeys_pending_test.go, pane_answer_test.go, sendkeys_hold_test.go, pause_relay_test.go and existing public CLI/MCP/Bun counterpart tests own pending/hash/dialog/answer guards.',
 'test/realtmux/cursor_launch_test.go: actual Bun default spawn timing with unfinished generation and actual client.close/recreation, startup no model text, independent pane identity guards.'
 ],
 new:'Two shared-adapter public files add only cross-boundary compositions and caller-loss/recreation, public mixed-input ordering/correlation, current-life inspection and representative failure observations; no second internal exhaustive fault matrix.',
 traceability:{'SR-0.2':'protected transcript + terminal/public interactivity and native commands','SR-0.3':'actual pane/runtime identity, exact-owned death and unrelated sentinels','SR-3.1–3.3':'mixed terminal/send-keys, exact Unicode/multiline FIFO, controls, caller continuity','SR-4.1–4.2':'independent authoritative row, process and permission identity','SR-6.2':'current-life confirmed rotation/history only'},
 corrected_mapping:'SR-0.4 does not exist and is not claimed.',
 final_public_race_leaves:receipts['09-final-public-race.log'].passed_leaves
 },
 failures_and_causes:[
 {log:'02-public-mixed.log',cause:'Test compile only: independent readRow fields are dynamically typed; explicit type assertions added.'},
 {log:'03-public-mixed-typed-observer.log',cause:'Test decoder mismatch: transcript request IDs are strings, numeric wire IDs raw; fixed record decoder. Go also demonstrated that turn_outcome precedes committed waiting/readiness.'},
 {log:'04-public-mixed-request-correlation.log',cause:'Test synchronization error: sending next idle prompt after outcome record alone permits legitimate consumption-time refusal during Stop publication. Replaced with independent committed waiting plus explicit completion notice; no retry or timeout change.'},
 {log:'07-public-adversarial.log',cause:'Test helper treated expected MCP error as fatal. Adapter now reads actual error envelope. Terminal loss assertion incorrectly forbade a durable session_end even when owned runtime gone was true; now checks actual runtime_gone/process evidence and process-only public reconciliation.'}
 ],
 source_findings:[],source_integrity:'source-integrity.json: all eight settled source hashes and exact causal regression verified; all old non-Go-test paths match old manifest except Engineer-authorized source changes; other Go tests match independent 700-test baseline except owned deadline boundary test.',
 deviations:['Two initial discovery reads used proposed filenames absent in the workspace; no changes or repository execution resulted.','Run04 began shortly before run03 process result was collected; their real-pane packages used separate container/process HOME/socket/temp namespaces. Final settled public runs were sequential; no retries of an unchanged failed case.','Bun coverage builds the public export into a temporary bundle rather than invoking full declaration/package packing; installed/shipped artifact collector evidence remains the next .wc writer.'],
 limitations:['Synthetic ACP runtime throughout; actual tmux/CLI/MCP/Go/Bun, not real authenticated Cursor or actual human attach/paste acceptance.','Terminal text is used only by explicit test read-pane inspection/notice assertions; AD does not classify screen state/identity/liveness.','Literal text and Enter remain separate operations. No arbitrary concurrent-send atomicity promise.','Unobserved MCP response is caller-side uncertainty; no claim that the response had not reached its OS pipe.','Protected pending/failure evidence does not invent completion, crash durability or replay; audit pending remains memory-resident as documented by deadline lane.','Native uninterruptible calls and descheduling limits unchanged; no timeout widening.'],
 next_collector_writer:{tickets:['t3.jf1.r8.a8.wc','t3.jf1.r8.a8.ky'],remaining:'Author/register unused cursor-pane-controls collector and exact actual-terminal operator acceptance instructions. Reuse these five public test owners and the retained Task1/2 focused owners; build/installed package evidence and actual human/authenticated acceptance remain separate. Do not execute collectors or scheduler jobs locally. No collector files written in this pass.',later:'One settled docs .48/.ro pass after collector/instructions; advice .6f; quality .yj; sole full make test-sandbox .zj; exact-revision user scheduler gate.'},
 full_suite:'Not run; .zj exclusively owns final settled make test-sandbox.',
 integration:'Not run; not dispatched for integration. No local Docker groups/collector bodies or scheduler submissions.',
 formal:'No spec/tla input changes; no TLA/TLC/Java/parser/formal jobs.',
 environment:{go:run('go',['version']),bun:run('bun',['--version']),tmux:run('tmux',['-V']),execution:'make sandbox only; isolated fixture HOME/explicit store/private tmux socket and exact owned process cleanup'},
 boundaries:'No .claude access, real AD stores, credentials/login/logout/live Cursor, source/product docs edits, host repo tests/build/scripts, commits/pushes, delegation, or outbound messages. Only assigned tests and evidence. Bees CLI only workspace ticket reads and own Subtask status changes.',
 incomplete_work:[],questions_for_caller:[]
};
writeFileSync(`${base}/FINAL-report.json`,JSON.stringify(report,null,2)+'\n');
console.log(JSON.stringify({result:report.result,source_hashes_verified:Object.keys(expected).length,old_manifest_verified:Object.keys(old.production_manifest).length,retained_regression:hash('pkg/api/decide_trail_deadline_test.go'),other_tests_preserved:tests.length-1,public_race_leaves:receipts['09-final-public-race.log'].passed_leaves,report:`${base}/FINAL-report.json`},null,2));
