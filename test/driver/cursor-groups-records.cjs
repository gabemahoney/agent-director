// Composed synthetic observations; no Cursor/account/config introspection.
const fs=require('fs'),assert=require('assert/strict'),path=require('path');
const [mode,dir,cwd,config,data,operation,input,pid]=process.argv.slice(2);
const read=p=>JSON.parse(fs.readFileSync(p,'utf8'));
const selection=read(path.join(dir,(mode==='worker'?'worker-pid':'runtime-pid')+'.selection.json'));
assert.deepEqual(selection,{CURSOR_CONFIG_DIR:config,CURSOR_DATA_DIR:data,CURSOR_API_KEY:null,AGENT_CLI_CREDENTIAL_STORE:null});
function verify(result,expectedInput) {
 assert.equal(result.input,expectedInput);assert.equal(result.result,'Synthetic reply: '+expectedInput);
 assert.equal(result.pid,Number(pid));assert.equal(result.cwd,cwd);assert.deepEqual(result.selection,selection);
 for(const selected of [config,data])assert.deepEqual(read(path.join(selected,pid+'-'+result.operation+'.json')),{operation:result.operation,input:expectedInput,result:result.result,pid:Number(pid),cwd,selection});
 return result;
}
let results=[];
if(mode==='worker') {
 const result=read(path.join(dir,'worker-pid.result.json'));
 assert.equal(result.operation,operation);
 if(result.hook)assert.equal(result.hook.status,0,'hook exit zero means ignored, not authority');
 results.push(verify(result,input));
} else {
 const frames=fs.readFileSync(path.join(dir,'trace.jsonl'),'utf8').trim().split('\n').map(JSON.parse);
 const sent=fs.readFileSync(path.join(dir,'sent.jsonl'),'utf8').trim().split('\n').map(JSON.parse);
 const prompts=frames.filter(f=>f.method==='session/prompt'&&(mode==='inventory'||f.params.prompt[0].text===input));
 if(mode!=='inventory')assert.equal(prompts.length,1);
 for(const p of prompts) {
 const text=p.params.prompt[0].text;
 if(mode==='inventory'&&!sent.some(f=>f.id===p.id&&f.result?.stopReason==='end_turn'))continue;
 assert.equal(sent.filter(f=>f.id===p.id&&f.result?.stopReason==='end_turn').length,1);
 const replies=sent.filter(f=>f.method==='session/update'&&f.params.sessionId===p.params.sessionId&&f.params.update.content?.text==='Synthetic reply: '+text);
 assert.equal(replies.length,1);
 const result=verify(read(path.join(config,pid+'-'+p.id+'.json')),text);
 if(mode==='inventory')for(const other of [operation,input])assert.equal(fs.existsSync(path.join(other,pid+'-'+p.id+'.json')),false,'artifact crossed selection');
 results.push(result);
 }
}
assert.ok(results.length);
console.log(JSON.stringify(results));
