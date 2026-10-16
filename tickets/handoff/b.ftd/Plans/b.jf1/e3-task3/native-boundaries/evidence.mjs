import fs from 'node:fs';
import crypto from 'node:crypto';
import {execFileSync} from 'node:child_process';
const base='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/';
const out=base+'native-boundaries/';
const hash=b=>crypto.createHash('sha256').update(b).digest('hex');
const paths=[...new Set(execFileSync('git',['ls-files','-co','--exclude-standard','-z'],{encoding:'utf8'}).split('\0').filter(p=>p&&!p.split('/').includes('.claude')&&!p.startsWith('tickets/handoff/'))) ].sort();
const manifest=Object.fromEntries(paths.filter(p=>fs.statSync(p).isFile()).map(p=>[p,{sha256:hash(fs.readFileSync(p)),mode:fs.statSync(p).mode&0o777}]));
const phase=process.argv[2];
fs.writeFileSync(out+'input-manifest-'+phase+'.json',JSON.stringify(manifest,null,2)+'\n');
if(phase==='before') {
 const required=['full-failure-assessment/FINAL-report.json','full-failure-assessment/targeted-results.json','full-failure-assessment/integrity.json','settled-full/FINAL-report.json','settled-full/testing-subtasks.json','settled-full/02-full-test-sandbox.stdout.log','settled-full/02-full-test-sandbox.stderr.log','api-deadline-assessment/FINAL-report.json','contract-final-tests/FINAL-report.json','no-match-validation/FINAL-report.json','docs-no-match/FINAL-report.json','deadline-red/FINAL-report.json','deadline-green/FINAL-report.json','deadline-engineer/FINAL-report.json','public/FINAL-report.json','collector/FINAL-report.json','PM-initial-review.json','deadline-code-review.json','no-match-code-review.json'];
 const reviewed=Object.fromEntries(required.map(p=>{const b=fs.readFileSync(base+p);if(p.endsWith('.json'))JSON.parse(b);return [p,{sha256:hash(b),bytes:b.length}];}));
 fs.writeFileSync(out+'reviewed-inputs.json',JSON.stringify(reviewed,null,2)+'\n');
 const full=fs.readFileSync(base+'settled-full/02-full-test-sandbox.stdout.log','utf8');
 fs.writeFileSync(out+'retained-failures.json',JSON.stringify(full.split('\n').filter(l=>/--- FAIL:|^FAIL\b|want |decide phase/.test(l)),null,2)+'\n');
}
console.log(JSON.stringify({phase,inputs:paths.length,protected:manifest['pkg/api/decide_trail_deadline_test.go'],permission:manifest['internal/store/permission.go']}));
