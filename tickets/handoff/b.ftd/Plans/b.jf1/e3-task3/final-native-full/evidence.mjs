import fs from 'node:fs';
import crypto from 'node:crypto';
import {execFileSync} from 'node:child_process';
const base='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/';
const out=base+'final-native-full/';
const hash=b=>crypto.createHash('sha256').update(b).digest('hex');
const read=p=>fs.readFileSync(p);
const json=p=>JSON.parse(read(p));
const git=args=>execFileSync('git',args,{encoding:'utf8'});
const write=(p,v)=>fs.writeFileSync(out+p,JSON.stringify(v,null,2)+'\n',{flag:'wx'});
// Filter forbidden directory names before stat/content/blob access.
const keep=p=>p&&!p.split('/').includes('.claude')&&!p.startsWith('tickets/handoff/');
const paths=[...new Set(git(['ls-files','-co','--exclude-standard','-z']).split('\0').filter(keep))].sort();
const index=new Map(git(['ls-files','--stage','-z']).split('\0').filter(Boolean).map(l=>{const n=l.indexOf('\t');return[l.slice(n+1),l.slice(0,n).split(' ')[0]];}).filter(([p])=>keep(p)));
const manifest={};
for(const p of paths){const s=fs.lstatSync(p);if(!s.isFile()&&!s.isSymbolicLink())throw Error('unsupported '+p);const b=s.isSymbolicLink()?Buffer.from(fs.readlinkSync(p)):read(p);manifest[p]={sha256:hash(b),bytes:b.length,mode:s.mode&0o777,git_mode:s.isSymbolicLink()?'120000':s.mode&0o111?'100755':'100644',index_mode:index.get(p)??null};}
const phase=process.argv[2];
if(!['before','after'].includes(phase))throw Error('phase');
write('input-manifest-'+phase+'.json',manifest);
const packages=read(out+'01-packages.stdout.log').toString().split('\n').filter(l=>l.startsWith('github.com/gabemahoney/agent-director/'));
if(read(out+'01-packages.exit').toString().trim()!=='0'||packages.some(p=>p.includes('/tickets/handoff/'))||!packages.length)throw Error('package discovery invalid');
const expected={
 'pkg/api/decide_trail_deadline_test.go':'032950e86d9919e2c48ae79189751e36f8875312a78bf1410973954fdde86d88',
 'internal/store/permission.go':'af61fc61e2b1b4c7e73eabac6ef2710a12d2019be45b3ab4c4920080badaa194',
 'internal/store/sqlite_connector.go':'850688eeb67b2bd9f8d71c3324c735e14c94fe63b1a48afad266b285bee36c79',
 'docs/architecture.md':'027a6e33aed5b2546102a82e0c5b8946b265461528adc4302f1dfc84aa5446df'
};
for(const [p,h] of Object.entries(expected))if(manifest[p]?.sha256!==h)throw Error('protected drift '+p);
const integrity={head:git(['rev-parse','HEAD']).trim(),branch:git(['branch','--show-current']).trim(),files:paths.length,manifest_sha256:hash(read(out+'input-manifest-'+phase+'.json')),package_count:packages.length,packages,protected:Object.fromEntries(Object.keys(expected).map(p=>[p,manifest[p]])),exclusions:'.claude filtered before any stat/content/blob; handoff metadata excluded after go list confirms no handoff packages; ignored generated files excluded'};
if(phase==='before'){
 const names=['docs-native/FINAL-report.json','docs-native/postdoc-advice-inventory-delta.json','native-lazy-validation/FINAL-report.json','native-lazy-code-review.json','native-multiwaiter-fix/FINAL-report.json','native-boundaries/FINAL-report.json','full-failure-assessment/FINAL-report.json','settled-full/FINAL-report.json','settled-full/quality-review-after.json','contract-final-tests/quality-review-after.json','public/FINAL-report.json','collector/FINAL-report.json','acceptance-review-fix/FINAL-report.json','PM-initial-review.json','deadline-red/FINAL-report.json','deadline-green/FINAL-report.json','no-match-validation/FINAL-report.json','docs/postdoc-advice-inventory.json','archive-layout-correction.json','accidental-full-cancellation.json'];
 const reviewed={};for(const p of names){const b=read(base+p),r=JSON.parse(b);reviewed[p]={sha256:hash(b),bytes:b.length,result:r.result??r.verdict??r.final_result??r.status??null};}
 write('reviewed-inputs.json',reviewed);
 write('all-testing-subtasks.json',json(base+'settled-full/testing-subtasks.json'));
 write('tools.json',Object.fromEntries([['go',['go',['version']]],['bun',['bun',['--version']]],['git',['git',['--version']]],['make',['make',['--version']]]].map(([k,[cmd,args]])=>[k,execFileSync(cmd,args,{encoding:'utf8'}).trim()]).concat([['HOME',process.env.HOME],['sandbox',process.env.AGENT_DIRECTOR_TEST_SANDBOX]])));
 write('quality-advice-inventory.json',{
   guide:'docs/test-writing-guide.md Pre-Submit Checklist / Red Flags',
   method:'Inherited complete Task3 acceptance/traceability review and prior PM repair evidence reconciled with final native/lazy source/test/docs changes and current advice delta; no fresh whole unchanged architecture/test corpus claim.',
   mandatory_unresolved:[],
   inventory:[
    {delta:'Original caller deadline while native work retains ownership',owner:'internal/store/native_boundary_test.go; native_read_deadline_test.go; deadline_boundary_test.go; sqlite_lazy_native_test.go; sqlite_lazy_context_test.go; cursor_prompt_native_test.go',evidence:'native-boundaries 194 race leaves and native-lazy-validation 18 race leaves; source review no findings'},
    {delta:'CLI separate 500ms audit drain within unchanged 1s reply margin, pending may be lost at exit',owner:'cmd/agent-director/decide_shutdown_linux_test.go TestDecideProcessShutdownBoundsBlockedAudit; decide_cli_test.go TestDecideCalledEmitsTrailLine',evidence:'Actual child process full kernel FIFO; finite shutdown, original verdict error, diagnostics, no fabricated audit; ordinary exactly-once event coverage'},
    {delta:'Structured error among stderr notices',owner:'pkg/ts-bun-client/test/subprocess-client.test.ts',evidence:'One minimal new real subprocess fixture test; existing parser scans error envelope among before/after notices; targeted receipt 02'},
    {delta:'Go Client.Close closes store with audit lifetime owned by host',owner:'pkg/api/decide_absolute_deadline_test.go TestClientDecideBoundsDeferredAuditAndPreservesFIFO',evidence:'500ms call within 1.5s held-trail guard; close returns while reporting held; closed-client refusal; release observes commit/called FIFO'},
    {delta:'Canonical precommit deliberate retry / unconfirmed COMMIT inspect without replay',owner:'pkg/api/decide_delivery_test.go TestAdviceFollow_F9_DecideStoreBusyRetry; internal/store/advice_follow_decision_test.go TestAdviceFollow_F10_UnconfirmedCommitInspectBeforeRetry',evidence:'Existing F9/F10 behavioral advice; no new recovery commands or sequences'}
   ],
   advice_status:'.6f stays done; no new sequence / no boilerplate advice or matrix tests',
   preserved:'Original protected old64a trail regression hash, all original 500ms/1.5s physical guards and parallelism; no source/docs edits.',
   provenance:'Original old64a trail causal RED proven. N1b successful background handoff contract RED proven; user-visible retry exhaustion N1/N1b and N2 causal RED unproven. Optional pre-ownership reconstruction mismatch never executed. Historical09 cause UNKNOWN; later outer read phases established without inner native/scheduler cause.',
   architecture:'Aggregate inherited whole-base plus every verified delta ends 027a6e33; not a fresh personal whole-file read or clean-document certification. A2-A6/B3-B8/C2-C5/D1-D2 and inherited hooks/permissions guide/recovery normalization remain deferred.',
   pending:'Full suite result separately recorded; PM then caller commit and fresh exact-SHA 17-group/172-case scheduler handoff; writable /tmp/cursor-pane-controls-artifacts bind; installed Bun collector network prerequisite. O1-O6 pending and not authorized.',
   formal:'No spec/tla changes; zero formal jobs.'
 });
 console.log(JSON.stringify({reviewed,packages:packages.length,files:paths.length}));
}else{
 const before=json(out+'input-manifest-before.json');integrity.drift=[...new Set([...Object.keys(before),...paths])].filter(p=>JSON.stringify(before[p])!==JSON.stringify(manifest[p]));
 if(integrity.drift.length)throw Error('input drift '+integrity.drift);
}
write('integrity-'+phase+'.json',integrity);
console.log(JSON.stringify(integrity));
