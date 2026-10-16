import fs from 'node:fs';
import crypto from 'node:crypto';
const out='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/final-native-full/';
const read=p=>fs.readFileSync(out+p,'utf8');
const json=p=>JSON.parse(read(p));
const write=(p,v)=>fs.writeFileSync(out+p,JSON.stringify(v,null,2)+'\n',{flag:'wx'});
const stdout=read('04-full-test-sandbox.stdout.log'),stderr=read('04-full-test-sandbox.stderr.log');
const lines=(stdout+'\n'+stderr).split('\n');
const passed=[],failed=[],noTests=[];
for(const l of stdout.split('\n')){
 let m=l.match(/^ok[ \t]+(\S+)[ \t]+(.+)$/);if(m){passed.push({package:m[1],result:m[2]});continue;}
 m=l.match(/^FAIL[ \t]+(\S+)[ \t]+(.+)$/);if(m){failed.push({package:m[1],result:m[2]});continue;}
 m=l.match(/^\?[ \t]+(\S+)[ \t]+\[no test files\]$/);if(m)noTests.push(m[1]);
}
const bunCount=pattern=>{const m=stderr.match(pattern);return m?Number(m[1]):null;};
const failures=lines.filter(l=>/--- FAIL:|^FAIL\b|^error:|^panic:|^fatal error:/.test(l));
const goComplete=passed.length+failed.length+noTests.length===json('integrity-before.json').package_count;
const exit=Number(read('04-full-test-sandbox.exit').trim());
const integrity=json('integrity-after.json');
const pass=exit===0&&goComplete&&failed.length===0&&bunCount(/^\s*(\d+) fail$/m)===0&&integrity.drift.length===0;
const quality=json('quality-advice-inventory.json');
quality.final_result=pass?'Quality reconciled and full suite passed.':'Actual full failures remain; quality/full stay active pending assessment.';
quality.mandatory_unresolved=failures;
write('quality-review-after.json',quality);
const retained={};
for(const lane of ['final-tests','contract-final-tests','settled-full']){
 const dir=out.replace('final-native-full/',lane+'/');
 const old=JSON.parse(fs.readFileSync(dir+'FINAL-report.json','utf8'));
 for(const [p,v] of Object.entries(old.receipt_sha256??{})){
  if(!p.includes('full-test-sandbox')||!p.includes('log'))continue;
  const b=fs.readFileSync(dir+p),actual=crypto.createHash('sha256').update(b).digest('hex');
  const expected=typeof v==='string'?v:v.sha256;
  if(actual!==expected)throw Error('retained failure changed '+dir+p);
  retained[dir+p]={sha256:actual,bytes:b.length};
 }
}
write('retained-failed-full-receipts.json',retained);
const receipts={};for(const name of fs.readdirSync(out).sort()){if(name==='FINAL-report.json')continue;const p=out+name,s=fs.lstatSync(p);if(s.isFile())receipts[name]={sha256:crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex'),bytes:s.size};}
const report={
 role:'apiary_test_writer_e3_task3_final_full_native',
 result:pass?'Settled full unit suite passed; final advice/quality delta reconciled.':'Settled full unit suite failed; actual failures retained and require source/test contract assessment.',
 tickets:{'t3.jf1.r8.a8.yj':pass?'done pending administration receipt':'active','t3.jf1.r8.a8.zj':pass?'done pending administration receipt':'active'},
 status_scope:'Only own .yj/.zj; .6f unchanged done; Task/Epic active pending external gate.',
 files_changed:['pkg/ts-bun-client/test/subprocess-client.test.ts',out+' (evidence only)'],
 source_doc_edits:[],
 full_suite:{command:'make test-sandbox',configured:'CLAUDE.md / Makefile',runs_this_pass:1,exit,passed:pass,raw_stdout:'04-full-test-sandbox.stdout.log',raw_stderr:'04-full-test-sandbox.stderr.log',exit_receipt:'04-full-test-sandbox.exit',tools:json('tools.json'),go:{passed_packages:passed.length,cached_packages:passed.filter(p=>p.result==='(cached)').length,uncached_passed_packages:passed.filter(p=>p.result!=='(cached)').length,executed_packages:passed.filter(p=>p.result!=='(cached)').length+failed.length,failed_packages:failed,no_test_packages:noTests.length,no_test_package_names:noTests,package_total:passed.length+failed.length+noTests.length,complete_against_discovery:goComplete,packages:passed,individual_case_counts:'Nonverbose Go output does not report passed individual case counts; none invented.'},bun:{passed:bunCount(/^\s*(\d+) pass$/m),failed:bunCount(/^\s*(\d+) fail$/m),expect_calls:bunCount(/^\s*(\d+) expect\(\) calls$/m),summary:stderr.match(/^Ran \d+ tests across \d+ files\..*$/m)?.[0]??null},failure_lines:failures},
 targeted:{command:"make sandbox CMD='cd pkg/ts-bun-client && bun test test/subprocess-client.test.ts'",exit:Number(read('02-bun-notices.exit')),passed:8,files:1,expect_calls:16,reason:'Minimal missing structured-error-amid-notices coverage; no unchanged targeted repetition.'},
 integrity:{...integrity,unchanged:integrity.drift.length===0,manifest_before_sha256:json('integrity-before.json').manifest_sha256,manifest_after_sha256:integrity.manifest_sha256,final_tested_manifest:'input-manifest-after.json',binding:'Entire tracked/nonignored-untracked input set except forbidden .claude filtered before stat/content/blob and handoff metadata; ignored generated outputs excluded. Byte hashes, permission/Git/index modes included. Caller must bind this manifest to exact later commit SHA.'},
 quality:{receipt:'quality-review-after.json',advice_receipt:'quality-advice-inventory.json',reviewed_inputs:'reviewed-inputs.json',method:quality.method,mandatory_unresolved:failures},
 failed_attempt_dispositions:{prior_full:'Prior final-tests, contract-final-tests and settled-full complete raw failure logs verified against their original report hashes; retained-failed-full-receipts.json. Current native/CLI fixes and necessary fixture corrections precede this run. Current pass not unchanged retry.',accidental_full:'native-lazy-validation accidental make test-sandbox CMD ignored command; owned process/container canceled, exit143; incomplete final chunk is not full validation. Archive layout correction verified by one go list ./... with no handoff packages.',this_pass:'One host bees show shorthand attempt failed three read-only calls (exit2); corrected show-ticket. Read-only absent helper.js/subprocessClient.test.ts lookup errors retained in conversation; actual existing files read. Some combined outputs truncated; narrower reads and hash-bound reviewed-input inventory used. Corrected report-only F9 owner path to existing decide_delivery_test.go after direct lookup; no test inputs changed. No failed tests before full.'},
 provenance_gaps:quality.provenance,
 architecture_review:quality.architecture,
 integration:'Not run by unit owner. Fresh postcommit integration writer must prepare exact-SHA scheduler handoff: 17 groups/172 cases, writable /tmp/cursor-pane-controls-artifacts bind, installed Bun collector dependency/network prerequisite. No Makefile extra-args variable.',
 authenticated:'O1-O6 pending and not authorized; synthetic/unit/static evidence is not authenticated terminal acceptance.',
 formal:'No spec/tla changes; zero TLA/TLC/Java/parser/scheduler jobs.',
 boundaries:'All repository execution only make sandbox / bare make test-sandbox with fresh container HOME and isolated synthetic stores/sockets/owned processes. No .claude reads/stat/hash, real AD stores, credentials/login/logout/live Cursor, local Docker TESTPLAN collectors/helpers/groups, commits/pushes/subagents/outbound messages or unrelated ticket statuses.',
 deviations:['One new minimal Bun test added for a demonstrated coverage gap; no source/docs edits or changed deadlines/bounds/outcomes/parallelism.'],
 incomplete_work:pass?['Caller PM, local commit bound to tested manifest, fresh exact-SHA scheduler gate; authenticated acceptance remains pending.']:['Assess actual failure diagnostics before any necessary correction or rerun; no unchanged retry or test weakening.'],
 questions_for_caller:[],receipt_sha256:receipts
};
write('FINAL-report.json',report);
console.log(JSON.stringify({pass,exit,go:report.full_suite.go,bun:report.full_suite.bun,failures,inputs:integrity.files,drift:integrity.drift}));
