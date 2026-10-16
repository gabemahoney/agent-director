const fs=require('fs'), cp=require('child_process'), crypto=require('crypto');
const own='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/commit-closeout-cancel-barrier-full/';
const path='internal/cursorhost/session_capacity_linux_test.go';
const before=fs.readFileSync(own+'session_capacity_linux_test.go.before','utf8'), after=fs.readFileSync(path,'utf8');
const start=s=>s.indexOf('\t\t\t\tc.wait(t, "each byte reservation cancelled"');
const block=s=>s.slice(start(s),s.indexOf('\n\t\t\t\t})',start(s))+9);
if(block(before)!==block(after)) throw Error('Cancellation observation body changed');
const refill=s=>s.slice(s.indexOf('\t\t\t\t// Refill the same byte capacity'));
if(refill(before)!==refill(after)) throw Error('Refill/final assertions changed');
let patch;try{patch=cp.execFileSync('diff',['-u',own+'session_capacity_linux_test.go.before',path],{encoding:'utf8'});}catch(e){if(e.status!==1)throw e;patch=e.stdout;}
fs.writeFileSync(own+'session_capacity_linux_test.go.patch',patch,{mode:0o600});
cp.execFileSync('git',['diff','--check']);
const proof={cancellation_wait_body_unchanged:true,refill_and_final_assertions_unchanged:true,diff_check:'passed',before_sha256:crypto.createHash('sha256').update(before).digest('hex'),after_sha256:crypto.createHash('sha256').update(after).digest('hex')};
fs.writeFileSync(own+'preservation-proof.json',JSON.stringify(proof,null,2)+'\n',{mode:0o600});
console.log(JSON.stringify(proof));
