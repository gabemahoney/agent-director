import {readFileSync, writeFileSync, statSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {execFileSync} from 'node:child_process';
const base='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3';
const out=base+'/legacy-commit-assessment';
const digest=p=>createHash('sha256').update(readFileSync(p)).digest('hex');
const inputs=execFileSync('git',['ls-files','--cached','--others','--exclude-standard','--','internal/store','internal/trail','pkg/api','docs','Makefile'],{encoding:'utf8'}).trim().split('\n').sort();
const manifest=Object.fromEntries(inputs.map(p=>[p,{sha256:digest(p),mode:statSync(p).mode&0o777}]));
const stage=process.argv[2];
writeFileSync(out+'/input-manifest-'+stage+'.json',JSON.stringify(manifest,null,2)+'\n');
const required=['contract-final-tests/FINAL-report.json','contract-final-tests/04-full-test-sandbox.log','api-deadline-assessment/FINAL-report.json','deadline-engineer/FINAL-report.json','deadline-review-fix/FINAL-report.json','deadline-green/FINAL-report.json','deadline-code-review.json','docs/FINAL-report.json','SRD-DEADLINE-DECISIONS.md','SRD-DEADLINE-REVIEW-DECISION.md','SRD-DEADLINE-TEST-CONTRACT-DECISION.md'];
const receipts=Object.fromEntries(required.map(p=>{const bytes=readFileSync(base+'/'+p);if(p.endsWith('.json'))JSON.parse(bytes);return [p,{sha256:digest(base+'/'+p),bytes:bytes.length}]}));
writeFileSync(out+'/reviewed-inputs-'+stage+'.json',JSON.stringify(receipts,null,2)+'\n');
console.log(JSON.stringify({stage,files:inputs.length,reviewed:required.length,protected:digest('pkg/api/decide_trail_deadline_test.go')}));
if(stage==='after'){
 const before=JSON.parse(readFileSync(out+'/input-manifest-before.json'));
 const changed=inputs.filter(p=>JSON.stringify(before[p])!==JSON.stringify(manifest[p]));
 console.log(JSON.stringify({changed}));
 if(JSON.stringify(changed)!==JSON.stringify(['internal/store/permission.go']))throw new Error('unexpected input delta');
 if(digest('pkg/api/decide_trail_deadline_test.go')!=='032950e86d9919e2c48ae79189751e36f8875312a78bf1410973954fdde86d88')throw new Error('protected regression changed');
}
