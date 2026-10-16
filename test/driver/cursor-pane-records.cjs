const fs=require('fs'), assert=require('assert/strict');
const [path,trace,raw,wantPath,initial,id]=process.argv.slice(2);
const records=fs.readFileSync(path,'utf8').trim().split('\n').map(JSON.parse);
const frames=fs.readFileSync(trace,'utf8').trim().split('\n').map(JSON.parse);
const bytes=fs.readFileSync(raw,'utf8');
const want=JSON.parse(fs.readFileSync(wantPath));const row=JSON.parse(initial)[0];
const accepted=records.filter(r=>r.kind==='submission_accepted');
assert.deepEqual(accepted.map(r=>r.data.text),want,'prompt.exact-admission-FIFO');
assert.equal(new Set(accepted.map(r=>r.data.submission_id)).size,want.length,'prompt.unique-ID');
const prompts=frames.filter(r=>r.method==='session/prompt');
assert.equal(prompts.length,want.length,'prompt.no-duplicate-wire');
assert.equal(frames.filter(r=>r.method==='session/cancel').length,0,'prompt.no-followup-cancel');
for(const [i,r] of records.entries()) {
 assert.equal(r.sequence,i+1,'transcript.sequence'); assert.equal(r.instance_id,id);
 assert.equal(r.launch_token,row.launch_token); assert.equal(r.life_number,row.life_number);
 assert.ok(r.connection_id,'transcript.connection');
}
for(const [i,prompt] of prompts.entries()) {
 assert.equal(prompt.params.prompt[0].text,want[i],'prompt.exact-wire-text');
 assert.equal(prompt.params.sessionId,row.session_id,'prompt.session');
 assert.ok(bytes.includes(JSON.stringify(prompt)),'prompt.raw-frame');
 for(const kind of ['submission_started','turn_outcome']) {
  const matches=records.filter(r=>r.kind===kind&&r.data.submission_id===accepted[i].data.submission_id);
  assert.equal(matches.length,1,kind+'.once'); assert.equal(matches[0].data.request_id,String(prompt.id),kind+'.request');
  assert.equal(matches[0].session_id,row.session_id,kind+'.session');
 }
}
