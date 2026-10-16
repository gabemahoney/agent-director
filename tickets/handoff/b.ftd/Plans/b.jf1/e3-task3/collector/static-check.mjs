// Static authoring validation only: never import/source/execute a case or helper.
import {readFileSync,writeFileSync,readdirSync,statSync,mkdtempSync,rmSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {spawnSync} from 'node:child_process';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import assert from 'node:assert/strict';
import {Script} from 'node:vm';
const base='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3';
const out=process.argv[2]||`${base}/collector`;
const root='tickets/testplans/b.jf1/t1.jf1.pc';
const read=p=>readFileSync(p,'utf8');const json=p=>JSON.parse(read(p));
const hash=p=>createHash('sha256').update(readFileSync(p)).digest('hex');
const run=(cmd,args)=>{const r=spawnSync(cmd,args,{encoding:'utf8'});assert.equal(r.status,0,`${cmd} ${args.join(' ')}: ${r.stderr}`);return r.stdout;};
const walk=p=>readdirSync(p).flatMap(n=>{const q=join(p,n);return statSync(q).isDirectory()?walk(q):[q];});
const publicRepair=process.argv[3]?json(process.argv[3]):null;
const reports=['deadline-red/FINAL-report.json','deadline-engineer/FINAL-report.json','deadline-green/FINAL-report.json','deadline-code-review.json','deadline-review-fix/FINAL-report.json','public/FINAL-report.json'];
const inputPaths=reports.map(p=>`${base}/${p}`).concat(['CLAUDE.md','docs/test-writing-guide.md','test/Dockerfile','test/driver/run-testplan.sh','test/driver/cursor-owned-launch.sh','test/driver/sql.sh','internal/testsupport/cursorfix/runtime.js','pkg/ts-bun-client/package.json','pkg/ts-bun-client/build.ts',`${base}/start/collector-authority.json`,`${base}/start/collector-task.json`,`${base}/start/collector-epic.json`,`${base}/start/collector-subtasks.json`]);
for(const r of reports)json(`${base}/${r}`);
const authoritative=json(`${base}/start/collector-authority.json`);assert.equal(authoritative.not_found.length,0);
const subtasks=json(`${base}/start/collector-subtasks.json`);assert.equal(subtasks.tickets.length,12);assert.equal(subtasks.not_found.length,0);
const requirements=authoritative.tickets.find(t=>t.ticket_id==='t1.ftd.hj').body;
const relevant=requirements.split('\n').filter(l=>/SR-0\.[23]|SR-3\.[123]|SR-4\.[12]|SR-6\.2|SR-9\.2/.test(l));
writeFileSync(`${out}/requirement-excerpts.txt`,relevant.join('\n')+'\n');
const children=[...read(`${root}/t1.jf1.pc.md`).matchAll(/^- (t2\.[\w.]+)$/gm)].map(m=>m[1]);assert.equal(children.length,5);assert.equal(new Set(children).size,5);
const allPlans=walk('tickets/testplans').filter(p=>p.endsWith('.md'));
for(const id of ['t1.jf1.pc',...children])assert.equal(allPlans.filter(p=>new RegExp(`^id: ${id.replaceAll('.','\\.')}\r?$`,'m').test(read(p))).length,1,`unique ID ${id}`);
const slugs=read('test/docker-epics.txt').split('\n').map(l=>l.trim()).filter(l=>l&&!l.startsWith('#'));assert.equal(slugs.length,17);assert.equal(new Set(slugs).size,17);
assert.equal(slugs.filter(s=>s==='cursor-pane-controls').length,1);
const resolved=[];
for(const slug of slugs){const matching=allPlans.filter(p=>/^t1\..*\.md$/.test(p.split('/').at(-1))&&new RegExp(`^title:.*${slug}`,'im').test(read(p)));assert.equal(matching.length,1,`slug ${slug}`);const ids=[...read(matching[0]).matchAll(/^- (t2\.[\w.]+)$/gm)].map(m=>m[1]);assert.ok(ids.length);for(const id of ids)assert.ok(statSync(join(matching[0],'..',id,id+'.md')).isFile());resolved.push({slug,collector:matching[0],expected_cases:ids.length});}
const tmp=mkdtempSync(join(tmpdir(),'pc-static-'));const checked=[];
try {
 for(const id of children){const p=`${root}/${id}/${id}.md`,body=read(p);assert.ok(body.includes(`parent: t1.jf1.pc`));for(const section of ['Setup','Steps','Pass criteria'])assert.ok(body.includes(`## ${section}`));const blocks=[...body.matchAll(/^```bash\n([\s\S]*?)^```$/gm)];assert.equal(blocks.length,1);assert.ok(!/\bsqlite3\b/.test(blocks[0][1]));const script=join(tmp,id+'.sh');writeFileSync(script,blocks[0][1]);run('bash',['-n',script]);for(const m of blocks[0][1].matchAll(/\bnode -e '([^']+)'/g))new Script(m[1]);checked.push(p);}
 run('bash',['-n','test/driver/cursor-pane-controls.sh']);
 for(const p of ['test/driver/cursor-pane-public.mjs','test/driver/cursor-pane-records.cjs'])run('node',['--check',p]);
}finally{rmSync(tmp,{recursive:true,force:true});}
const pub=json(`${base}/public/source-integrity.json`);const expected=pub.expected_settled_source_sha256;
const publicTests={...pub.own_test_sha256};
if(publicRepair){
 assert.deepEqual(publicRepair.prior_public_test_sha256,pub.own_test_sha256,'repair lineage');
 assert.deepEqual(Object.keys(publicRepair.repaired_public_test_sha256).sort(),['test/realtmux/cursor_pane_controls_boundary_linux_test.go','test/realtmux/cursor_pane_controls_test.go']);
 Object.assign(publicTests,publicRepair.repaired_public_test_sha256);
}
for(const [p,digest] of Object.entries(expected))assert.equal(hash(p),digest,`settled source ${p}`);
assert.equal(hash('pkg/api/decide_trail_deadline_test.go'),json(`${base}/deadline-red/FINAL-report.json`).test_sha256);
let verifiedPriorTests=0;
for(const line of read(`${base}/deadline-review-fix/verified-tests.sha256`).trim().split('\n')) {
 const m=/^([a-f0-9]{64})\s+(.+)$/.exec(line);assert.ok(m);assert.ok(!m[2].startsWith('.claude/'));
 assert.equal(hash(m[2]),publicTests[m[2]]||m[1],`retained test ${m[2]}`);verifiedPriorTests++;
}
for(const [p,digest] of Object.entries(publicTests))assert.equal(hash(p),digest,`public test ${p}`);
const allGit=run('git',['ls-files','-co','--exclude-standard','-z']).split('\0').filter(p=>p&&!p.startsWith('.claude/')&&!p.startsWith('tickets/'));
const tests=[...new Set(allGit.filter(p=>p.endsWith('_test.go')||p.endsWith('.test.ts')))];
const integrity={settled_source_sha256:expected,all_match:true,verified_prior_go_tests:verifiedPriorTests,public_tests_match:true,causal_regression_sha256:hash('pkg/api/decide_trail_deadline_test.go'),tests_sha256:Object.fromEntries(tests.map(p=>[p,hash(p)])),existing_harness_sha256:Object.fromEntries(['test/driver/run-testplan.sh','test/driver/cursor-owned-launch.sh','test/driver/sql.sh','test/driver/cursor-pipe.c','internal/testsupport/cursorfix/runtime.js'].map(p=>[p,hash(p)]))};
writeFileSync(`${out}/source-test-integrity.json`,JSON.stringify(integrity,null,2)+'\n');
writeFileSync(`${out}/input-hashes.json`,JSON.stringify(Object.fromEntries(inputPaths.map(p=>[p,hash(p)])),null,2)+'\n');
writeFileSync(`${out}/static-checks.json`,JSON.stringify({result:'passed static authoring checks only',case_bodies_executed:false,helpers_sourced:false,bash_n_cases:checked,bash_n_helpers:['test/driver/cursor-pane-controls.sh'],node_syntax_only:['test/driver/cursor-pane-public.mjs','test/driver/cursor-pane-records.cjs'],registered_groups:resolved,expected_groups:17,expected_total_cases:resolved.reduce((n,r)=>n+r.expected_cases,0),new_expected_cases:5,unknown_live_build_protocol_platform_scheduler_provenance:'PENDING'},null,2)+'\n');
run('git',['diff','--check']);
console.log(JSON.stringify({static_checks:'passed',new_cases:5,groups:17,expected_total_cases:resolved.reduce((n,r)=>n+r.expected_cases,0),collector_execution:'NOT RUN',authenticated_acceptance:'PENDING'}));
