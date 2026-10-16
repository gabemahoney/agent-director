import fs from 'node:fs';
import crypto from 'node:crypto';
import {execFileSync} from 'node:child_process';
const base='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/';
const out=base+'no-match-validation/';
const hash=b=>crypto.createHash('sha256').update(b).digest('hex');
const read=p=>fs.readFileSync(p);
const write=(p,v)=>fs.writeFileSync(out+p,JSON.stringify(v,null,2)+'\n');
const paths=execFileSync('git',['ls-files','-co','--exclude-standard','-z'],{encoding:'utf8'}).split('\0').filter(p=>p&&!p.split('/').includes('.claude')&&!p.startsWith('tickets/handoff/')).sort();
const manifest=Object.fromEntries([...new Set(paths)].map(p=>[p,{sha256:hash(read(p)),mode:fs.statSync(p).mode&0o777}]));
const phase=process.argv[2];
write('input-manifest-'+phase+'.json',manifest);
if(phase==='before'){
 const required=['legacy-commit-assessment/FINAL-report.json','legacy-commit-assessment/input-manifest-before.json','legacy-commit-assessment/input-manifest-after.json','contract-final-tests/FINAL-report.json','api-deadline-assessment/FINAL-report.json','deadline-green/FINAL-report.json','deadline-engineer/FINAL-report.json','deadline-review-fix/FINAL-report.json','deadline-code-review.json','PM-initial-review.json','docs/FINAL-report.json','contract-final-tests/quality-review-after.json','SRD-NO-MATCH-TRANSACTION-DECISION.md'];
 write('reviewed-inputs.json',Object.fromEntries(required.map(p=>{const bytes=read(base+p);if(p.endsWith('.json'))JSON.parse(bytes);return[p,{sha256:hash(bytes),bytes:bytes.length}]})));
 const log=read(base+'contract-final-tests/04-full-test-sandbox.log');
 write('retained-full-failure.json',{sha256:hash(log),bytes:log.length,failures:log.toString().split('\n').filter(l=>/--- FAIL:|^FAIL\b|want exactly ErrStoreBusy/.test(l))});
 let old=read('internal/store/permission.go').toString();
 old=old.replace('\t\t\t// No verdict matched the guards. Roll back this transaction rather\n\t\t\t// than attempt a COMMIT whose outcome the caller need not resolve.\n\t\t\treturn sql.ErrNoRows','\t\t\treturn nil');
 old=old.replace('\tif errors.Is(err, sql.ErrNoRows) && !errors.Is(err, ErrStoreBusy) {\n\t\treturn false, nil\n\t}\n','');
 const prior=JSON.parse(read(base+'legacy-commit-assessment/input-manifest-before.json'))['internal/store/permission.go'].sha256;
 if(hash(old)!==prior||prior!=='151d6bec0905b78e24e3ce571da8a8a8017b1f1965565cc80be0f4c4adf736a9')throw Error('pre-repair reconstruction mismatch');
 fs.writeFileSync(out+'permission-before.go.txt',old);
 write('overlay.json',{Replace:{'/work/internal/store/permission.go':'/work/'+out+'permission-before.go.txt'}});
 write('baseline-provenance.json',{method:'Exact inverse of two no-match repair hunks, hash verified against contemporaneous legacy-commit-assessment before manifest. Current other production inputs unchanged; not old64a.',before:prior,after:manifest['internal/store/permission.go'].sha256,overlay:'overlay.json'});
}else if(phase==='after'){
 const before=JSON.parse(read(out+'input-manifest-before.json'));
 const delta=Object.keys(manifest).filter(p=>JSON.stringify(manifest[p])!==JSON.stringify(before[p]));
 if(delta.length!==1||delta[0]!=='internal/store/advice_follow_decision_test.go')throw Error('unexpected inputs changed during checks: '+delta);
 if(manifest['pkg/api/decide_trail_deadline_test.go'].sha256!=='032950e86d9919e2c48ae79189751e36f8875312a78bf1410973954fdde86d88')throw Error('protected test changed');
 const settled=JSON.parse(read(out+'input-manifest-pre-corrected-green.json'));
 if(JSON.stringify(settled)!==JSON.stringify(manifest))throw Error('inputs changed during corrected run');
 write('integrity.json',{unchanged_during_corrected_run:true,changed_after_first_focused_failure:delta,files:Object.keys(manifest).length,protected:manifest['pkg/api/decide_trail_deadline_test.go']});
}
console.log(JSON.stringify({phase,files:Object.keys(manifest).length,permission:manifest['internal/store/permission.go']}));
