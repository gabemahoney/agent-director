import {readFileSync,writeFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {execFileSync} from 'node:child_process';
import {correctFinalReport} from './correct-accounting.mjs';
const root='tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/';
const dir=root+'timing-investigation/';
const final=root+'final-tests/';
const hash=p=>createHash('sha256').update(readFileSync(p)).digest('hex');
const json=p=>JSON.parse(readFileSync(p,'utf8'));
const phase=process.argv[2];
const settled=json(final+'settled-input-manifest.json');
if(settled.some(x=>x.path==='.claude'||x.path.startsWith('.claude/'))) throw Error('Forbidden manifest path');
const current=settled.map(x=>({path:x.path,sha256:hash(x.path)}));
const mismatches=current.filter((x,i)=>x.sha256!==settled[i].sha256);
const paths=execFileSync('git',['ls-files','-z','--cached','--others','--exclude-standard','--','.',':(exclude).claude/**',':(exclude)tickets/handoff/**'],{encoding:'utf8'}).split('\0').filter(Boolean).filter(p=>p!=='.claude'&&!p.startsWith('.claude/')).sort();
const samePaths=JSON.stringify(paths)===JSON.stringify(settled.map(x=>x.path));
const historicalLogs={};
for(const [name,expected] of Object.entries({'09-full-test-sandbox.log':'b865dc2460d0e34e0cc23d8bce0b22528d558fa7fa8864d60a42dd36b8c20dae','13-full-test-sandbox.log':'00eb01f03e74cc84f13c498c769a645b5a7c4ab3fcc25a111e9cc17f23a68d90'})) {
  const raw=readFileSync(final+name,'utf8');
  historicalLogs[name]={bytes:Buffer.byteLength(raw),lines:raw.split('\n').length-1,sha256:hash(final+name),expected,hash_matches:hash(final+name)===expected,failed_leaves:raw.split('\n').filter(x=>/^\s+--- FAIL:/.test(x)),go_passed:raw.split('\n').filter(x=>/^ok\s+github.com\//.test(x)).length,bun_summary:raw.match(/\d+ pass\s*\n\s*\d+ fail\s*\n\s*\d+ expect\(\) calls\s*\nRan \d+ tests across \d+ files/)?.[0]};
}
const receipt={phase,head:execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim(),manifest_count:settled.length,settled_manifest_sha256:hash(final+'settled-input-manifest.json'),all_existing_inputs_identical:mismatches.length===0,same_paths:samePaths,mismatches,historical_logs:historicalLogs};
writeFileSync(dir+phase+'-integrity.json',JSON.stringify(receipt,null,2)+'\n');
if(mismatches.length||!samePaths||Object.values(historicalLogs).some(x=>!x.hash_matches)) throw Error('Input/log drift');
if(phase==='before') {
  const report=correctFinalReport(json(final+'FINAL-report.json'));
  writeFileSync(final+'FINAL-report.json',JSON.stringify(report,null,2)+'\n');
  const quality=json(final+'quality-findings.json');
  quality.mandatory_findings_remaining=['Historical pkg/api 09 deadline failures now fully accounted for, but causal disposition remains pending after bounded investigation.'];
  quality.historical_failures_with_unknown_cause=report.historical_failures_with_unknown_cause;
  quality.result='Final 13 suite passed; quality reopened for historical deadline failure accounting and explicit technical disposition';
  writeFileSync(final+'quality-findings.json',JSON.stringify(quality,null,2)+'\n');
}
console.log(JSON.stringify(receipt,null,2));
