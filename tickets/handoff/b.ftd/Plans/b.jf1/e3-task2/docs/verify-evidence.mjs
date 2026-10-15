import fs from 'node:fs';
import crypto from 'node:crypto';
import {execFileSync} from 'node:child_process';
const dir = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/docs';
const prior = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-task1/docs';
const read = p => fs.readFileSync(p, 'utf8');
const hash = s => crypto.createHash('sha256').update(s).digest('hex');
const meta = s => ({sha256:hash(s), lines:s.split('\n').length-1, bytes:Buffer.byteLength(s)});
const save = (p,v) => fs.writeFileSync(`${dir}/${p}`, JSON.stringify(v,null,2)+'\n');
const assert = (ok,s) => { if(!ok) throw Error(s); };
const git = args => execFileSync('git', args,{encoding:'utf8',maxBuffer:8*1024*1024});
const latest=JSON.parse(read('tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/prestart-validation/FINAL-report.json'));
const preserved={...latest.source_sha256,...latest.test_sha256,...latest.unchanged_other_test_fixture_sha256};
for(const [path,expected] of Object.entries(preserved)) assert(hash(read(path))===expected,'source/test changed '+path);
save('source-test-integrity.json',Object.entries(preserved).map(([path,sha256])=>({path,sha256,unchanged:true})));
const inherited = JSON.parse(read(`${prior}/architecture-review-provenance.json`));
const base = git(['show','HEAD:docs/architecture.md']);
assert(hash(base)===inherited.final.sha256,'Task1 base mismatch');
// Strictly reverse complete unified hunks against the complete known endpoint.
// Every context/addition byte must match; endpoint hashes cover unchanged text.
function reversePatch(text,patch){
  const lines=text.split('\n'); lines.pop();
  const hs=[]; let h;
  for(const line of patch.split('\n')){
    const m=line.match(/^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/);
    if(m){ h={start:Number(m[3])-1,old:[],next:[],oc:Number(m[2]??1),nc:Number(m[4]??1)};hs.push(h);continue; }
    if(!h) continue;
    if(line.startsWith(' ')){h.old.push(line.slice(1));h.next.push(line.slice(1));}
    else if(line.startsWith('-')) h.old.push(line.slice(1));
    else if(line.startsWith('+')) h.next.push(line.slice(1));
  }
  assert(hs.length>0,'empty patch');
  for(const h of hs.reverse()){
    assert(h.old.length===h.oc && h.next.length===h.nc,'hunk count mismatch');
    assert(JSON.stringify(lines.slice(h.start,h.start+h.next.length))===JSON.stringify(h.next),'hunk byte mismatch');
    lines.splice(h.start,h.next.length,...h.old);
  }
  return lines.join('\n')+'\n';
}
let cursor=base; const verified=[];
for(const c of [...inherited.chain].reverse()){
  assert(hash(cursor)===c.to,'chain target mismatch');
  const patch=read(c.patch); cursor=reversePatch(cursor,patch);
  assert(hash(cursor)===c.from,'chain origin mismatch');
  verified.unshift({...c,patch_sha256:hash(patch),strict_hunks_and_complete_endpoints_verified:true});
}
assert(hash(cursor)===inherited.original_base.sha256,'original reviewed base mismatch');
assert(meta(cursor).lines===22213 && meta(cursor).bytes===1534189,'base dimensions');
const partitions=inherited.partition_reports.map(p=>{
  assert(hash(read(p.path))===p.sha256,'partition hash mismatch '+p.path);
  return {...p,hash_verified:true};
});
let end=0;for(const p of partitions){assert(p.lines[0]===end+1,'partition gap');end=p.lines[1];}
assert(end===22213,'partition end');
const final=read('docs/architecture.md');
const delta=git(['diff','--no-ext-diff','--','docs/architecture.md']);
assert(hash(reversePatch(final,delta))===hash(base),'own complete delta mismatch');
fs.writeFileSync(`${dir}/architecture-delta.patch`,delta);
verified.push({from:hash(base),to:hash(final),patch:`${dir}/architecture-delta.patch`,patch_sha256:hash(delta),strict_hunks_and_complete_endpoints_verified:true,review:'All new Task2 prose and changed context personally read against production source and settled reports'});
save('reviewed-base.json',{original_base:meta(cursor),task2_base:meta(base),source:'HEAD:docs/architecture.md; complete reverse reconstruction through all three inherited patches',personal_whole_file_read:false});
save('architecture-review-provenance.json',{
  method:'Inherited whole-base four-partition review plus independently verified every exact delta through Task1 and personal Task2 delta/context review; not a fresh personal 22k-line read',
  original_base:meta(cursor),partition_reports:partitions,chain:verified,task2_base:meta(base),final:meta(final),
  whole_final_aggregate_covered:true,clean_document_claim:false,
  deferred:['A2-A6','B3-B8','C2-C5','D1-D2'],
  prior_report:{path:`${prior}/FINAL-report.json`,sha256:hash(read(`${prior}/FINAL-report.json`))},
  prior_provenance:{path:`${prior}/architecture-review-provenance.json`,sha256:hash(read(`${prior}/architecture-review-provenance.json`))},
  prior_document_hashes:{path:`${prior}/document-hashes.json`,sha256:hash(read(`${prior}/document-hashes.json`))}
});
const files=['docs/usage.md','docs/caller-contract.md','docs/permissions.md','docs/hooks.md','docs/architecture.md'];
const hashes=files.map(path=>{
 const before=git(['show',`HEAD:${path}`]);const after=read(path);
 const blocks=s=>s.match(/```[^]*?```/g)??[];
 assert(JSON.stringify(blocks(before))===JSON.stringify(blocks(after)),'changed fenced commands '+path);
 return {path,before_sha256:hash(before),...meta(after),fenced_blocks_unchanged:true};
});
save('document-hashes.json',hashes);
const caller=read('docs/caller-contract.md');
const advice=[
 ['rotation_capacity','During rotation, a provisional holding-capacity refusal likewise requires a', 'During rotation, a provisional holding-capacity refusal likewise requires a\nlater explicit submission after capacity is available; refused text will not\nbe delivered automatically. Wait for publication and remaining decision holds\nto resolve before expecting held input to be admitted. A rotation notice or\nfresh-identity confirmation is not evidence of admission or prompt completion.', 'Observe explicit held-capacity refusal, then deliberately submit later when eligible; no automatic delivery. Preserve unknown/independent holds through publication and prove no admission before remaining guards.'],
 ['failed_rotation','The first control has a fixed three-second cancellation deadline, including','The first control has a fixed three-second cancellation deadline, including\nfresh-session creation, publication and protected recording. Repeated controls\ncannot extend it. Cancellation timeout, missing, invalid or reused identity,\nrefused publication, lost authority or persistence failure stops admission and\nvisibly refuses held input to both conversations. Interrupting a pending\nrotation also stops the owned runtime. Do not send refused input to the old\nconversation or automatically replay it. Inspect `get` and available protected\nrecords, then use the existing live-row sequence above if cleanup is needed;\nstart a fresh unique instance once the failed row is reconciled.', 'Follow failure inspection and existing cleanup sequence only if needed, then fresh unique instance. Refused held text reaches neither conversation; no automatic replay, old resume, deadline extension or fabricated history. Use isolated fixtures and actual existing public owners; no live credentials.'],
 ['committed_report_failure','Publication and hook completion are distinct. A report failure or timeout can','Publication and hook completion are distinct. A report failure or timeout can\noccur after the fresh session and required outgoing history have committed.\nDo not infer rollback from that failure, or treat an incomplete record as proof\nthat publication failed. Inspect the actual current session and history before\ndeciding what happened; AD does not restore the old conversation automatically.', 'On actual commit followed by failure/stalled hook, inspect real public current session/history and available transcript; preserve committed truth and refuse held text, without rollback/replay inference.'],
 ['rotation_shutdown','Exit or eligible pause can preempt rotation, but `ended` still requires actual','Exit or eligible pause can preempt rotation, but `ended` still requires actual\nowned runtime death and a verified, durably recorded end for the authoritative\nconversation. Unknown binding or exhausted persistence cannot establish success.', 'Follow intentional shutdown with actual owned-death and authoritative ended evidence; unknown/failing persistence must remain failure. Retain reporter-only limit for pre-Start micro-window.']
].map(([id,marker,exact,follow])=>{assert(caller.includes(exact),'advice differs '+id);return{id,path:'docs/caller-contract.md',heading:'Cursor operation boundaries',marker,exact_prose:exact,literal_follow_scope:follow};});
save('postdoc-advice-inventory.json',{owner:'t3.jf1.r8.sr.78',new_or_changed_commands:[],sequences:advice,unchanged_advice:'Existing capacity/interruption/display/persistence/live-row advice remains its existing owner; changed rotation advice above is the scoped delta. No doc parsing/metatests or manufactured commands requested.',status:'awaiting literal-follow Test Writer; no advice execution claimed'});
save('FINAL-report.json',{
 role:'apiary_doc_writer_e3_task2',result:'One settled initial documentation pass complete; whole-base/all-delta aggregate architecture chain verified. Literal-follow advice, final quality and full suite remain assigned downstream.',
 ticket_statuses:{'t3.jf1.r8.sr.vh':'defined -> active -> done; changed recovery sequences explicitly assigned to .78 via exact inventory; execution not claimed','t3.jf1.r8.sr.be':'defined -> active -> done; complete aggregate chain verified'},
 statuses_limit:'Only own documentation Subtasks updated; Task/Epic and other Subtasks untouched.',
 files_changed:files,
 artifacts:['docs/FINAL-report.json','docs/reviewed-base.json','docs/architecture-review-provenance.json','docs/architecture-delta.patch','docs/document-hashes.json','docs/postdoc-advice-inventory.json','docs/source-test-integrity.json','docs/verify-evidence.mjs','docs/verification.log'],
 documentation:'Usage owns same-life aliases, individual old accepted cancellations, provisional capacity/consumption-time ownership and guarded promotion, and per-record/current-life shared transcript references. Caller contract owns fixed deadline, failure/refusal, interruption during rotation, publication versus hook completion, no automatic old delivery/rollback/replay, and truthful shutdown. Permissions owns retained request identities/independent unknown-origin holds. Hooks owns authenticated outgoing-to-fresh publication. Architecture names mandatory component and shared fixture reuse, atomic required archive, three-way bounded reconciliation, retired correlation and persistence truth.',
 whole_architecture_review:{method:'Inherited four complete partition reviews with exact hashes verified; reverse-applied every full-context patch from known 6c36 Task1 endpoint through 2738e9 and 759fb to complete b18e original base, validating every endpoint. Own exact Task2 delta reverse-reconstructs 6c36; new prose/context personally inspected against source. Aggregate coverage, not a personal 22k-line read.',chain:verified,original_base:meta(cursor),task2_base:meta(base),final:meta(final),whole_final_aggregate_covered:true,clean_document_claim:false,deferred:['A2-A6','B3-B8','C2-C5','D1-D2']},
 review_basis:'Task2, own two authoritative doc Subtasks and relevant PRD/SRD; CLAUDE documentation locations and all three doc guides; complete engineer/behavior/code-review/review-fix/revalidation/prestart-fix/prestart-validation reports and all three SRD decision records, with later final reports superseding historical pending findings. Actual controller admission/promotion/publication/shutdown, mailbox, protected recorder, reporter reconciliation, guarded transaction, hook verification and reusable fixture boundaries inspected.',
 verification:['make sandbox CMD=...verify-evidence.mjs PASS; exact complete architecture endpoints/hunks and partition hashes verified','git diff --check -- docs PASS','All fenced command blocks in five edited docs unchanged from HEAD; no new executable advice','Production and test/fixture hashes unchanged against latest prestart-validation report'],
 source_test_contradictions:[],
 operator_actions:'Unchanged: no demonstrated new human-only procedure; existing caller cleanup and operator inspection owners linked. No install, pins, skills, generated references, manuals or new guides touched.',
 srd_decisions_for_caller:'Documentation adopts existing SRD-DECISIONS plus superseding REVIEW and PRESTART decisions. No new technical/product decision. Historical pending prose in those records is superseded by final revalidation; this pass does not edit SRD tickets.',
 postdoc_handoff:{owner:'t3.jf1.r8.sr.78',inventory:'docs/postdoc-advice-inventory.json',sequences:advice.map(x=>x.id),request:'Follow exact changed advice literally with isolated real behavior/public fixtures; reuse existing owners, no doc-parsing or manufactured commands. Validate explicit capacity resubmission, remaining durable holds before admission, failed-rotation inspection/cleanup/fresh unique instance, committed report-failure truth/no replay, and authoritative intentional shutdown. Existing live-row sequence is reused, not changed.'},
 limitations:['No tests or full suite run by doc writer; .78 advice, .e9 final quality and .66 sole settled make test-sandbox remain next owner','R3 deterministic proof remains reporter-level plus controller source inspection; no actual full Run pre-Start micro-window claim','No live/authenticated Cursor, external Docker acceptance or formal proof claim. No spec/tla change; zero formal jobs','No whole clean architecture certification; inherited A2-A6/B3-B8/C2-C5/D1-D2 remain deferred'],
 execution_boundary:'All evidence script execution only make sandbox CMD; no source/test edits, host repo artifacts, .claude reads/access, real AD stores, credentials/login/logout/live Cursor, Docker groups/collectors, scheduler/TLC/Java/parser, full suite, commits/pushes/delegation/messages. Workspace-only discovery. Bees CLI used only authorized metadata reads and own status updates.',
 deviations:[],failures:[],incomplete_work:['Downstream .78 literal validation, .e9 final quality, .66 settled full suite, coordinator commit and user exact-revision scheduler gate remain outstanding; no remaining assigned documentation edits'],questions_for_caller:[]
});
console.log(JSON.stringify({result:'PASS',base:meta(base),final:meta(final),chain_steps:verified.length,partitions:partitions.length,documents:hashes.length,advice_sequences:advice.length}));
