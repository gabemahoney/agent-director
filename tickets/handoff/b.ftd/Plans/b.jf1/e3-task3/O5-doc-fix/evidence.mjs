import fs from 'node:fs';
import crypto from 'node:crypto';
import {execFileSync,spawnSync} from 'node:child_process';
const base='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/';
const out=base+'O5-doc-fix/';
const target='tickets/testplans/b.jf1/t1.jf1.pc/operator-acceptance.md';
const read=p=>fs.readFileSync(p);
const hash=b=>crypto.createHash('sha256').update(b).digest('hex');
const git=a=>execFileSync('git',a,{encoding:'utf8'});
const fail=m=>{throw Error(m);};
const keep=p=>p&&!p.split('/').includes('.claude')&&!p.startsWith('tickets/handoff/');
const oldManifestPath=base+'final-native-full/input-manifest-after.json';
const oldManifest=JSON.parse(read(oldManifestPath));
if(hash(read(oldManifestPath))!=='3216131b3a5f0b61f085bd1e8cbb85fcfc3e9ce4e54c6f445d01fc1197efc558')fail('tested manifest changed');
const paths=[...new Set(git(['ls-files','-co','--exclude-standard','-z']).split('\0').filter(keep))].sort();
const index=new Map(git(['ls-files','--stage','-z']).split('\0').filter(Boolean).map(l=>{const i=l.indexOf('\t');return[l.slice(i+1),l.slice(0,i).split(' ')[0]];}).filter(([p])=>keep(p)));
const current={};
for(const p of paths){const s=fs.lstatSync(p),b=s.isSymbolicLink()?Buffer.from(fs.readlinkSync(p)):read(p);current[p]={sha256:hash(b),bytes:b.length,mode:s.mode&0o777,git_mode:s.isSymbolicLink()?'120000':s.mode&0o111?'100755':'100644',index_mode:index.get(p)??null};}
const drift=[...new Set([...Object.keys(oldManifest),...paths])].filter(p=>JSON.stringify(current[p])!==JSON.stringify(oldManifest[p]));
if(paths.length!==2330||drift.length!==1||drift[0]!==target)fail('unexpected drift '+drift);
const before=read(out+'operator-acceptance-before.txt'),after=read(target);
if(hash(before)!==oldManifest[target].sha256)fail('old document not tested document');
const beforeText=before.toString(),afterText=after.toString();
const prefix='## O5 clear/new, history and inspection continuity\n',suffix='## O6 exit, eligible pause and death evidence\n';
if(beforeText.split(prefix)[0]!==afterText.split(prefix)[0]||beforeText.split(suffix)[1]!==afterText.split(suffix)[1])fail('outside O5 changed');
const fences=t=>t.match(/^```[^\n]*\n[\s\S]*?^```/gm)??[];
if(JSON.stringify(fences(beforeText))!==JSON.stringify(fences(afterText)))fail('fences changed');
for(const k of ['mode','git_mode','index_mode'])if(current[target][k]!==oldManifest[target][k])fail('document mode changed');
const diff=spawnSync('diff',['-u','--label',target+' (tested bytes)','--label',target+' (O5 prose correction)',out+'operator-acceptance-before.txt',target],{encoding:'utf8'});
if(diff.status!==1)fail('expected one document difference');
fs.writeFileSync(out+'operator-acceptance.patch',diff.stdout);
git(['diff','--check','--',target]);
const report={
 role:'apiary_doc_writer_e3_task3_O5_final_fix',
 result:'Mandatory PM finding O5 corrected; instructions-only outcome interpretation.',
 tickets:{'t3.jf1.r8.a8.48':'done -> active -> done (completed after evidence verification)'},
 status_scope:'Only own .48; Task/Epic remain active and other Subtasks unchanged.',
 files_changed:[target,out+' (new evidence only)'],
 correction:'Record authoritative current row/runtime binding/current-life history; distinguish prepublication failure, committed publication followed by reporting failure, and uncertain publication; no inferred rollback or forced old identity restoration; preserve held-input refusal and no automatic replay evidence.',
 old_document:{...oldManifest[target],snapshot:'operator-acceptance-before.txt'},
 new_document:current[target],
 patch:{path:'operator-acceptance.patch',sha256:hash(Buffer.from(diff.stdout))},
 input_integrity:{tested_manifest:oldManifestPath,tested_manifest_sha256:hash(read(oldManifestPath)),original_inputs:2330,unchanged_inputs:2329,changed_inputs:drift,document_mode_unchanged:true,source_tests_other_docs_unchanged:true,architecture:current['docs/architecture.md'],protected_trail:current['pkg/api/decide_trail_deadline_test.go']},
 reviewed_basis:['CLAUDE.md','docs/user-docs-guide.md','docs/readme-guide.md','docs/architecture-docs-guide.md','Task t2.jf1.r8.a8 and Subtask t3.jf1.r8.a8.48','PM-final-review.json (full)','docs-native/FINAL-report.json (full)','final-native-full/FINAL-report.json (full; narrowed rereads after combined output truncation)','docs/caller-contract.md rotation contract','internal/cursorhost/report.go publication observation and reporting failure','internal/cursorhost/session.go held_input_refused and rotation_failed recording'],
 verification:{scope:'Evidence-only byte/mode/Git-index verification through make sandbox; scoped git diff --check',fenced_blocks_unchanged:true,outside_O5_unchanged:true,command_delta:'NONE',changed_test_assertions:[],new_test_pass_claim:false},
 provenance:'Original full suite receipt and tested manifest left unchanged. Earlier full PASS belongs to original 2330-input manifest, not these new documentation bytes. Coordinator must bind approved doc-only overlay (2329 unchanged + one new document) to final commit.',
 limitations:['No tests/full/lint/build/live acceptance executed. PM explicitly waives unchanged unit rerun for this instructions-only correction.','O1-O6 actual terminal acceptance remains PENDING and separately authorized; existing isolation/provenance requirements unchanged; screens are not source authority.','Original old64a causal trail RED proven; extra native causal RED gaps retained; historical09 cause UNKNOWN.','No spec/tla changes; zero formal/scheduler jobs.','Inherited architecture whole-base plus reviewed deltas is not fresh whole-file certification.'],
 deviations:['Read-only combined output truncation resolved by narrower report reads; no source/test mutation. Initial absent-path rg returned exit1.'],
 incomplete_work:['Caller narrow read-only review, local commit and exact-SHA scheduler handoff remain.'],
 questions_for_caller:[]
};
fs.writeFileSync(out+'FINAL-report.json',JSON.stringify(report,null,2)+'\n');
console.log(JSON.stringify({result:report.result,old_document:report.old_document,new_document:report.new_document,unchanged_inputs:2329,drift,command_delta:'NONE',fenced_blocks_unchanged:true}));
