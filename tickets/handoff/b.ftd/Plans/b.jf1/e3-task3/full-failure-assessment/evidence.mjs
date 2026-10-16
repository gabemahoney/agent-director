import {readFile,writeFile,lstat,readlink} from 'node:fs/promises';
import {createHash} from 'node:crypto';
const base='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/';
const output=base+'full-failure-assessment/';
const hash=b=>createHash('sha256').update(b).digest('hex');
const prior=JSON.parse(await readFile(base+'settled-full/input-manifest-after.json','utf8'));
const changed=[],after={};
for(const [path,old] of Object.entries(prior)) {
  if(path.split('/').includes('.claude') || !path.startsWith('.') && path.startsWith('/')) throw Error('Forbidden path '+path);
  const stat=await lstat(path);
  const bytes=stat.isSymbolicLink()?Buffer.from(await readlink(path)):await readFile(path);
  after[path]={sha256:hash(bytes),bytes:bytes.length,mode:stat.mode&0o777};
  if(after[path].sha256!==old.sha256 || after[path].mode!==old.mode) changed.push({path,before:old,after:after[path]});
}
const addedInputs=[];
for(const path of ['internal/store/sqlite_connector.go']) {
 const bytes=await readFile(path),stat=await lstat(path);
 after[path]={sha256:hash(bytes),bytes:bytes.length,mode:stat.mode&0o777};
 addedInputs.push({path,after:after[path]});
}
const reviewed=[
 'settled-full/FINAL-report.json','settled-full/02-full-test-sandbox.stdout.log','settled-full/02-full-test-sandbox.stderr.log','settled-full/quality-review-after.json',
 'deadline-engineer/FINAL-report.json','deadline-green/FINAL-report.json','deadline-review-fix/FINAL-report.json','api-deadline-assessment/FINAL-report.json','legacy-commit-assessment/FINAL-report.json','no-match-validation/FINAL-report.json','no-match-code-review.json','docs-no-match/FINAL-report.json',
 'SRD-DEADLINE-DECISIONS.md','SRD-DEADLINE-REVIEW-DECISION.md','SRD-DEADLINE-TEST-CONTRACT-DECISION.md','SRD-NO-MATCH-TRANSACTION-DECISION.md',
];
const reviewedInputs={};
for(const path of reviewed) {
 const bytes=await readFile(base+path);
 if(path.endsWith('.json')) JSON.parse(bytes);
 reviewedInputs[path]={sha256:hash(bytes),bytes:bytes.length};
}
const receipts={};
for(const name of ['targeted-01','targeted-02-race','targeted-03-cursor-race','targeted-04-outcomes-race','targeted-05-final','targeted-07-connector','targeted-08-zero-initialization']) {
 const path=base+'full-failure-assessment-'+name+'.log';
 const bytes=await readFile(path),raw=bytes.toString();
 const passed=[...raw.matchAll(/^\s*--- PASS: ([^\n]+?) \([\d.]+s\)$/gm)].map(m=>m[1]);
 const leaves=passed.filter(p=>!passed.some(q=>q.startsWith(p+'/')));
 receipts[name]={path,sha256:hash(bytes),passed_leaves:leaves.length,leaves,
 failures:raw.split('\n').filter(l=>/FAIL|WARNING: DATA RACE|\.go:\d+:.*want|setup failure:/.test(l)),
 packages:raw.split('\n').filter(l=>/^(ok\s|FAIL\s)/.test(l))};
}
const integrity={baseline:base+'settled-full/input-manifest-after.json',baseline_inputs:Object.keys(prior).length,inputs:Object.keys(after).length,changed,added_inputs:addedInputs,
 tests_and_docs_changed:changed.filter(x=>x.path.endsWith('_test.go')||x.path.startsWith('docs/')||x.path.endsWith('README.md')),
 protected_regression:after['pkg/api/decide_trail_deadline_test.go'],
 preserved_no_match_source:after['internal/store/permission.go']};
await writeFile(output+'input-manifest-after.json',JSON.stringify(after,null,2)+'\n');
await writeFile(output+'integrity.json',JSON.stringify(integrity,null,2)+'\n');
await writeFile(output+'reviewed-inputs.json',JSON.stringify(reviewedInputs,null,2)+'\n');
await writeFile(output+'targeted-results.json',JSON.stringify(receipts,null,2)+'\n');
console.log(JSON.stringify({inputs:integrity.inputs,changed:changed.map(x=>x.path),added_inputs:addedInputs,tests_and_docs_changed:integrity.tests_and_docs_changed,protected:integrity.protected_regression,preserved_no_match:integrity.preserved_no_match_source,receipts:Object.fromEntries(Object.entries(receipts).map(([k,v])=>[k,{passed_leaves:v.passed_leaves,failures:v.failures,packages:v.packages}]))},null,2));
