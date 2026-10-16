import fs from 'node:fs';
import crypto from 'node:crypto';
import {spawnSync} from 'node:child_process';
const base='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/';
const out=base+'native-lazy-validation/';
const read=p=>JSON.parse(fs.readFileSync(p));
const info=p=>({sha256:crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex'),mode:(fs.statSync(p).mode&0o777).toString(8)});
const tests=['internal/store/sqlite_lazy_native_test.go','internal/store/sqlite_lazy_context_test.go','internal/store/prompt_native_observer_test.go','internal/store/cursor_prompt_native_test.go'];
const current=read(out+'green-handoff-inputs-before.json');
const baseline=read(base+'native-boundaries/input-manifest-after.json');
const nativeBefore=read(base+'native-multiwaiter-fix/source-before.json').files;
const sourceBaselineMismatch=[];
for(const [p,v] of Object.entries(nativeBefore)){
  const expected=p==='internal/store/sqlite_connector.go'?{sha256:'850688eeb67b2bd9f8d71c3324c735e14c94fe63b1a48afad266b285bee36c79',mode:'664'}:v;
  if(JSON.stringify(info(p))!==JSON.stringify(expected))sourceBaselineMismatch.push({path:p,expected,current:info(p)});
}
const previousGoChanges=[];
for(const [p,v] of Object.entries(current))if(baseline[p]&&(v.sha256!==baseline[p].sha256||parseInt(v.mode,8)!==baseline[p].mode))previousGoChanges.push(p);
const modes=['green-final','green-complete','red-n1b','red-n2','vet','green-handoff','red-n1b-handoff','red-n1b-contract','vet-final','vet-confirmed'];
const validations=modes.map(mode=>{
  const r=read(out+mode+'-receipt.json');
  const stdout=fs.readFileSync(out+mode+'.stdout','utf8');
  const passed=[...stdout.matchAll(/^\s*--- PASS:\s+(\S+)/gm)].map(m=>m[1]);
  const failed=[...stdout.matchAll(/^\s*--- FAIL:\s+(\S+)/gm)].map(m=>m[1]);
  return {mode,...r,passed_nodes:passed.length,passed_leaves:passed.filter(n=>!passed.some(other=>other.startsWith(n+'/'))),failed_nodes:failed};
});
const provenanceChecks={};
for(const mode of ['red-n1b-contract','red-n2']){
  const receipt=read(out+mode+'-receipt.json');
  const before=read(out+mode+'-inputs-before.json');
  const unexpected=Object.keys(current).filter(p=>JSON.stringify(current[p])!==JSON.stringify(before[p]));
  provenanceChecks[mode]={same_physical_current_inputs_as_green:unexpected.length===0,same_other_current_production_bytes_as_green:unexpected.every(p=>p.endsWith('_test.go')),all_differences_are_explicitly_excluded_n1_tests:unexpected.every(p=>receipt.replacements['/work/'+p]?.endsWith('excluded-n1-tests.txt')),differences:unexpected,verified_exact_snapshots:receipt.verified_before,independent_test_bytes:Object.fromEntries(tests.filter(p=>!receipt.replacements['/work/'+p]).map(p=>[p,info(p)]))};
}
const format=spawnSync('gofmt',['-l',...tests],{encoding:'utf8'});
fs.writeFileSync(out+'format.stdout',format.stdout??'');fs.writeFileSync(out+'format.stderr',format.stderr??'');fs.writeFileSync(out+'format.exit',String(format.status)+'\n');
const diff=spawnSync('git',['diff','--check'],{encoding:'utf8'});
fs.writeFileSync(out+'diff-check.stdout',diff.stdout??'');fs.writeFileSync(out+'diff-check.stderr',diff.stderr??'');fs.writeFileSync(out+'diff-check.exit',String(diff.status)+'\n');
const report={
  role:'apiary_test_writer_e3_task3_lazy_connector_validate',
  result:'New production-outer-connector and late prompt COUNT regressions pass 18 targeted race leaves; store vet and formatting pass. Exact pre-N1b snapshot fails the explicit successful-background-handoff contract. End-user retry exhaustion and N2 causal RED remain unproven. No current source failure found. Full unit acceptance remains the later sole owner.',
  head:'e85a74c9803ffe1dddb80bdcb19bf4614f643839',branch:'feature/b.ftd',
  toolchain:'go version go1.22.10 linux/amd64; Linux Debian sandbox image via Makefile; modernc.org/sqlite v1.34.0 as locked module',
  tickets:{'t3.jf1.r8.a8.yj':'active; remains active pending batched docs/CLI/final acceptance review','t3.jf1.r8.a8.zj':'active; untouched, later sole full-suite owner'},statuses_set:[],
  files_changed:tests.map(p=>({path:p,...info(p),lines:fs.readFileSync(p,'utf8').trimEnd().split('\n').length})),evidence_directory:out,
  source_doc_mapping_collector_edits:[],
  coverage:{
    N1:'Actual sql.DB with the exact production callerOwnedConnector and sqliteConnector layers around the real SQLite driver. Hold a legacy lease, observe a queued bounded Store call, discard held lease, observe background handoff with zero physical I/O and original actual borrower context. Noncooperative native Open exceeds original 500ms deadline; caller returns Busy within unchanged 1.5s guard. Operation admission token and sql lease stay retained through separate native Open and Close barriers. Repeated three 20ms reads and zero-wait admission start no additional native work. Expired Open runs zero initialization SQL. Finite release leads to physical replacement, foreign_keys=1, nondefault busy_timeout=137, no open transaction and unchanged undecided durable request.',
    N1b:'One bounded and three legitimate legacy waiters queued in real database/sql behind the legacy lease. Pool handoff must succeed without synthetic ErrBadConn and without native opening. Whichever caller wins native opening retains its own value/deadline; no copied pool/queue algorithm or forced scheduling. Bounded caller expires; legacy native owner remains synchronously held until finite release; all live legacy callers finish with SELECT 1, no spurious acquisition failures. Both bounded-winner and legacy-winner orderings occurred in retained current-source development receipts; this is observed coverage, not an exhaustive scheduler proof.',
    fresh_replacements:'Ten table cases retain needReset=false fresh handoff and real driver work: direct Exec, Query, their ErrSkip/statement fallbacks, Prepare context fallback, Begin transaction fallback, Ping fallback, unused lease return/reborrow ResetSession, Raw ErrBadConn discard and unopened Close/Validator. Context values/deadlines reach actual initialization; all opened connections validate configured settings. No native I/O for unopened raw-discard/close. Real SQLite statement and transaction results retained.',
    contexts:'Explicit production caller provenance tested for unbounded default, cancellation-only default, zero fixed 1s execution after 1ms admission, and zero clipped by original 500ms parent. Initialization deliberately lasts 30ms beyond the admission allowance. Partial initialization cancellation after first successful PRAGMA starts no second initialization statement, returns canceled, invalidates the connection and recovers with a fresh initialized physical connection.',
    N2:'Actual CheckCursorPrompt COUNT(*) native Rows.Next held beyond original 500ms call deadline while separate authority until remains one hour ahead. Caller returns false/Busy while callback remains held, within original 1.5s guard. Finite release and safe lease synchronization precede zero-late-COMMIT check and successful fresh authority verification. Existing permission/plan eligibility guard owners remain unchanged. Race-enabled current run passes; no late callback eligibility assignment or original race was demonstrated.'
  },
  validation:{primary_green:'green-handoff-receipt.json',primary_green_leaves:18,final_vet:'vet-confirmed-receipt.json',all_attempts:validations,first_development_run:{stdout:'01-targeted.stdout',stderr:'01-targeted.stderr',exit:Number(fs.readFileSync(out+'01-targeted.exit','utf8'))},format:{exit:format.status,stdout:format.stdout,stderr:format.stderr},diff_check:{exit:diff.status,stdout:diff.stdout,stderr:diff.stderr}},
  integrity:{current_tested_inputs:Object.keys(current).length,before_after_manifests:'Each runner receipt has corresponding *-inputs-before.json and *-inputs-after.json; all contain Go module inputs and Go files under internal, pkg/api and cmd with modes and SHA256. Receipt changed_inputs is empty in every run.',source_against_engineer_snapshot:{checked:Object.keys(nativeBefore).length,unexpected:sourceBaselineMismatch},changes_since_native_boundaries:previousGoChanges,protected:{trail:info('pkg/api/decide_trail_deadline_test.go'),no_match_permission:info('internal/store/permission.go'),connector:info('internal/store/sqlite_connector.go')},provenance_checks:provenanceChecks},
  red_provenance:{
    N1_original:'Not executed against original pre-N1 snapshot: that source has no callerOwnedConnector type required by the new production-outer fixture. No reconstruction, guessed shim, mismatched overlay or compile failure is called RED. Current mixed-path bounded ownership is directly proven by native context, admission/lease and SQL events.',
    N1b_first_comparison:'red-n1b exact verified d2230fcedd3855ff5a10771f7ec89ee0d92b6abb8b7cbbbe4512ae1732ba5c29 snapshot PASSED the original caller-output assertions in its selected scheduling. Therefore no end-user retry-exhaustion reproduction is claimed.',
    N1b_contract:'After adding the explicit production-background successful-handoff assertion, unchanged current test bytes passed green-handoff and failed exact verified pre-N1b snapshot in red-n1b-contract: background pool handoff returned synthetic acquisition failure: driver: bad connection. No compile/setup failure. This causally distinguishes connector handoff behavior; all live caller failure/retry exhaustion was not reproduced by that run.',
    N2:'red-n2 uses all four exact original native-review before files, verified SHA256/mode before execution. The independent N2 test/helper bytes stay identical. Two N1-only new test files are excluded with a package-store-only test overlay because original source has no outer connector type. Every other current production file is unchanged. It COMPILED and PASSED with -race. N2 original race/late eligibility assignment RED remains UNPROVEN.',
    mandatory_original_trail:'Previously proven old64a causal trail RED remains retained by caller; protected 032950e86d9919e2c48ae79189751e36f8875312a78bf1410973954fdde86d88 test is byte/mode unchanged. Not re-run here.',
    earlier_optional_native:'Earlier pre-ownership native RED remains UNPROVEN; mismatched lockwait overlay was never executed and is not reused.'
  },
  deviations_and_failures:[
    'First invocation mistakenly used make test-sandbox CMD=cat/metadata; test-sandbox ignores CMD and began the unauthorized full suite. Bun install/build ran and Go ./... emitted build failures from archived before/*.go snapshot directories (undefined package-local symbols), before new tests were added. The tool stdin Ctrl-C did not stop the pipe session. Root verified owned process group and exact container, terminated only those, and this agent observed final session exit143 / make Terminated. Only the last raw output chunk is retained on disk as accidental-full-final-chunk.stdout; original full stdout/stderr are NOT complete receipts. No full-suite result/pass claim, no integration result. Later final owner must account for evidence snapshot .go directories before Go ./... discovery; this agent did not move them.',
    'Operational cancellation required two parent coordination messages despite dispatch normally limiting outbound messages. Root explicitly recorded the process-safety exception. No external-user messaging or other container action by this writer.',
    'After accidental invocation, one host rg read Makefile target definitions to identify CMD handling; no host repository code, Go/Bun tests, formatter, script, binary, dependency execution or Docker TESTPLAN group was run.',
    'The first red-n1b comparison passed. Its receipt is preserved; later handoff-contract assertion is explicitly distinguished from unproven user-visible retry exhaustion.',
    'The red-n2 comparison passed; it is preserved and not labeled causal RED.',
    'Two new runner mode aliases were initially not recognized: red-n1b-handoff and vet-final actually ran current-source targeted tests (all passed). Their receipts record the actual go test command and no overlay; neither is claimed RED or vet. Runner mode routing was fixed; red-n1b-contract and vet-confirmed are the corrected receipts. Extra green repetitions were administrative errors, not retries after failed current-source tests.',
    'The dispatch permitted .yj to remain active until later batched documentation/final acceptance. No ticket status was changed; .zj and all other statuses untouched. Bees CLI is absent inside sandbox and no host ticket CLI was used.'
  ],
  review:{guide:'docs/test-writing-guide.md, including Pre-Submit Checklist and Red Flags',result:'No additional source gaps found within test lane. Real Store/SQLite fixtures reused; one shared observer seam for actual COUNT rows; table-driven context/method cases, new main fixture/test file under 500 lines, finite native barriers, no duplicate eligibility matrix, no production algorithm copies or test relaxation. Original 100/150/300/500ms limits and parallelism files unchanged.'},
  limitations:[
    'No settled full-unit, Bun, integration, Docker TESTPLAN or live acceptance pass is claimed. The unintended partial full invocation is retained as a failure/deviation, not validation.',
    'N1 original mixed-path exact-baseline RED not run; N1b end-user retry exhaustion and N2 original race remain unproven. The current boundary guarantees above are directly observed.',
    'Historical09 root cause remains UNKNOWN; prior later read failures establish outer phases only, not inner native/scheduler causes.',
    'No production/docs/mapping/collector edits; no .claude access, real AD store/credentials/login/logout/live Cursor work, subagents, scheduler submissions, commits or pushes. No spec/tla changes or formal jobs.',
    'Authenticated O1-O6 acceptance remains pending and was not authorized for this lane.'
  ],
  incomplete_work:['Caller batched native ownership/CLI/N1/N2 documentation and SRD reconciliation; final .yj review; later .zj sole settled full suite; PM/commit/exact-SHA scheduler handoff remain pending.'],
  source_failures:[],questions:[]
};
fs.writeFileSync(out+'FINAL-report.json',JSON.stringify(report,null,2)+'\n');
const artifacts=Object.fromEntries(fs.readdirSync(out).filter(n=>fs.statSync(out+n).isFile()&&n!=='artifact-manifest.json').sort().map(n=>[n,info(out+n)]));
fs.writeFileSync(out+'artifact-manifest.json',JSON.stringify(artifacts,null,2)+'\n');
console.log(JSON.stringify({result:report.result,files:report.files_changed,integrity:report.integrity,validation:validations.map(r=>({mode:r.mode,status:r.status,passed_leaves:r.passed_leaves.length,failed:r.failed_nodes})),format:report.validation.format,diff:report.validation.diff_check},null,2));
