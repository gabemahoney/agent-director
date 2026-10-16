import fs from 'node:fs';
import crypto from 'node:crypto';
import {execFileSync} from 'node:child_process';
const base='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/';
const out=base+'settled-full/';
const hash=b=>crypto.createHash('sha256').update(b).digest('hex');
const read=p=>fs.readFileSync(p);
const json=p=>JSON.parse(read(p));
const git=args=>execFileSync('git',args,{encoding:'utf8'});
const write=(p,v)=>fs.writeFileSync(out+p,JSON.stringify(v,null,2)+'\n',{flag:'wx'});
// Exclude forbidden paths before any stat, content or blob access.
const keep=p=>p&&!p.split('/').includes('.claude')&&!p.startsWith('tickets/handoff/');
const paths=[...new Set(git(['ls-files','-co','--exclude-standard','-z']).split('\0').filter(keep))].sort();
const index=new Map(git(['ls-files','--stage','-z']).split('\0').filter(Boolean).map(l=>{const n=l.indexOf('\t');return[l.slice(n+1),l.slice(0,n).split(' ')[0]];}).filter(([p])=>keep(p)));
const manifest={};
for(const p of paths){const s=fs.lstatSync(p);if(!s.isFile()&&!s.isSymbolicLink())throw Error('unsupported input '+p);const b=s.isSymbolicLink()?Buffer.from(fs.readlinkSync(p)):read(p);manifest[p]={sha256:hash(b),bytes:b.length,mode:s.mode&0o777,git_mode:s.isSymbolicLink()?'120000':s.mode&0o111?'100755':'100644',index_mode:index.get(p)??null};}
const phase=process.argv[2];
if(!['before','after'].includes(phase))throw Error('phase');
write('input-manifest-'+phase+'.json',manifest);
const sources=json(base+'no-match-validation/FINAL-report.json').integrity.sources;
for(const [p,v] of Object.entries(sources))if(manifest[p]?.sha256!==v.sha256||manifest[p]?.mode!==v.mode)throw Error('source drift '+p);
if(manifest['pkg/api/decide_trail_deadline_test.go'].sha256!=='032950e86d9919e2c48ae79189751e36f8875312a78bf1410973954fdde86d88')throw Error('protected regression drift');
if(manifest['docs/architecture.md'].sha256!=='b7f2ea55fda7b7f61dee2e2bfd830a269578c7c29ad1c3524252ccbc64f7f819')throw Error('architecture drift');
const result={head:git(['rev-parse','HEAD']).trim(),branch:git(['branch','--show-current']).trim(),files:paths.length,sources,protected:manifest['pkg/api/decide_trail_deadline_test.go'],exclusions:'.claude before stat/content/blob access; handoff metadata; ignored generated artifacts',manifest_sha256:hash(read(out+'input-manifest-'+phase+'.json'))};
if(phase==='before'){
 const names=['legacy-commit-assessment/FINAL-report.json','no-match-validation/FINAL-report.json','no-match-code-review.json','docs-no-match/FINAL-report.json','contract-final-tests/FINAL-report.json','contract-final-tests/quality-review-after.json','final-tests/FINAL-report.json','final-tests/quality-review.json','api-deadline-assessment/FINAL-report.json','docs/FINAL-report.json','docs/postdoc-advice-inventory.json','deadline-red/FINAL-report.json','deadline-engineer/FINAL-report.json','deadline-green/FINAL-report.json','deadline-review-fix/FINAL-report.json','public/FINAL-report.json','collector/FINAL-report.json','acceptance-review-fix/FINAL-report.json','PM-initial-review.json','SRD-NO-MATCH-TRANSACTION-DECISION.md','SRD-DEADLINE-DECISIONS.md','SRD-DEADLINE-REVIEW-DECISION.md'];
 const reviewed={};for(const p of names){const b=read(base+p);const r=p.endsWith('.json')?JSON.parse(b):null;reviewed[p]={sha256:hash(b),bytes:b.length,result:r?.result??r?.verdict??r?.final_result??r?.status??null};}
 write('reviewed-inputs.json',reviewed);
 const previous=json(base+'no-match-validation/input-manifest-after.json');
 const changed=[...new Set([...Object.keys(previous),...paths])].filter(p=>previous[p]?.sha256!==manifest[p]?.sha256||previous[p]?.mode!==manifest[p]?.mode);
 if(changed.length!==1||changed[0]!=='docs/architecture.md')throw Error('unexpected post-validation delta '+changed);
 result.delta_since_focused_validation=changed;
 const retained={};for(const lane of ['final-tests','contract-final-tests']){const p=lane+'/04-full-test-sandbox.log',b=read(base+p),r=json(base+lane+'/FINAL-report.json');if(hash(b)!==r.receipt_sha256['04-full-test-sandbox.log'])throw Error('old failure changed '+p);retained[p]={sha256:hash(b),bytes:b.length,failures:b.toString().split('\n').filter(l=>/--- FAIL:|^FAIL\b/.test(l))};}
 write('retained-full-failures.json',retained);
 const quality=json(base+'contract-final-tests/quality-review-after.json');
 quality.mandatory_unresolved=[];
 quality.final_result='Settled postdoc quality reconciled; full suite separately recorded.';
 quality.postdoc_settlement={previous_blocker:'Actual guarded no-match COMMIT defect confirmed and repaired in permission.go only; no-match rollback preserves precommit Busy and matched COMMIT uncertainty.',regression:'Independent inverse pre-repair RED; unchanged repaired GREEN; 43 focused race leaves pass after necessary F10 attempt-observer correction. All previous failed receipts retained.',docs:'Batched no-match architecture clarification complete; no new advice inventory delta. Existing .6f behavioral advice work done; historical inventory PENDING describes its earlier authoring snapshot.',review:'No-match code review has no findings. Prior comprehensive quality review and PM P1/P2/P3 repair evidence inherited and reconciled, not claimed as a fresh personal whole-test/architecture read.',architecture:'Inherited four-partition whole-base plus every verified delta, ending b7f2ea55; aggregate coverage only. A2-A6/B3-B8/C2-C5/D1-D2 and inherited hooks/permissions guide/recovery normalization debt remain deferred.',preservation:'Protected 500ms/1.5s regression unchanged; actual SQL legacy 100ms/1.1s leaf retained parallel. Real driver/SQL cleanup and public uncertain-COMMIT recovery retained; controlled policy tests do not claim SQL durability.',pending:'Full sandbox result follows. Coordinator fresh PM/local commit/exact-revision scheduler 17 groups 172 cases with writable /tmp/cursor-pane-controls-artifacts mount; authenticated O1-O6 remain pending and not authorized.',formal:'No spec/tla changes; zero formal jobs.',historical09:'Causes remain UNKNOWN.'};
 quality.doc_consistency='Final architecture b7f2ea55 no-match note matches rollback source and retained ambiguity contract. Five product-doc surfaces retain canonical recovery in caller-contract; no new advice work required.';
 write('quality-review.json',quality);
 write('tools-before.json',{go:execFileSync('go',['version'],{encoding:'utf8'}).trim(),bun:execFileSync('bun',['--version'],{encoding:'utf8'}).trim(),git:execFileSync('git',['--version'],{encoding:'utf8'}).trim(),make:execFileSync('make',['--version'],{encoding:'utf8'}).split('\n')[0],home:process.env.HOME,sandbox:process.env.AGENT_DIRECTOR_TEST_SANDBOX});
 console.log(JSON.stringify({reviewed,postdoc_quality:quality.postdoc_settlement}));
}else{
 const before=json(out+'input-manifest-before.json');
 result.drift=[...new Set([...Object.keys(before),...paths])].filter(p=>JSON.stringify(before[p])!==JSON.stringify(manifest[p]));
 if(result.drift.length)throw Error('input drift '+result.drift);
}
write('integrity-'+phase+'.json',result);
console.log(JSON.stringify(result));
