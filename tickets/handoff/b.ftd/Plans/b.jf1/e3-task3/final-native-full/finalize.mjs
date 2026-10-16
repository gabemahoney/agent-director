import fs from 'node:fs';
import crypto from 'node:crypto';
const out='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/final-native-full/';
const read=p=>JSON.parse(fs.readFileSync(out+p,'utf8'));
const r=read('FINAL-report.json');
if(!r.full_suite.passed||!r.integrity.unchanged)throw Error('suite/integrity failed');
if(fs.readFileSync(out+'05-after-report.exit','utf8').trim()!=='0')throw Error('report failed');
const tickets=read('tickets-after.json');
if(tickets.status!=='success')throw Error('ticket read failed');
for(const id of ['t3.jf1.r8.a8.yj','t3.jf1.r8.a8.zj']){
 if(tickets.tickets.find(t=>t.ticket_id===id)?.ticket_status!=='done')throw Error('ticket not done '+id);
 r.tickets[id]='done';
}
if(tickets.tickets.find(t=>t.ticket_id==='t2.jf1.r8.a8')?.ticket_status!=='active')throw Error('Task unexpectedly not active');
r.ticket_receipts=['tickets-before.json','tickets-active.json','tickets-done.json','tickets-after.json'];
r.receipt_sha256={};
for(const name of fs.readdirSync(out).sort()){
 if(name==='FINAL-report.json')continue;
 const p=out+name,s=fs.lstatSync(p);
 if(s.isFile())r.receipt_sha256[name]={sha256:crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex'),bytes:s.size};
}
fs.writeFileSync(out+'FINAL-report.json',JSON.stringify(r,null,2)+'\n');
console.log(JSON.stringify({result:r.result,tickets:r.tickets,inputs:r.integrity.files,manifest:r.integrity.manifest_after_sha256,go_passed:r.full_suite.go.passed_packages,go_executed:r.full_suite.go.executed_packages,go_cached:r.full_suite.go.cached_packages,go_no_tests:r.full_suite.go.no_test_packages,bun:r.full_suite.bun,receipt_files:Object.keys(r.receipt_sha256).length}));
