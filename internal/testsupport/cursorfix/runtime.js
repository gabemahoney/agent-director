// Synthetic ACP protocol 1 stand-in; never invokes or authenticates Cursor.
const fs = require('fs');
const readline = require('readline');
if(process.argv[2]==='worker-server') {
 fs.writeFileSync(SETTINGS.workerPID,String(process.pid));
 setInterval(()=>{if(fs.existsSync(SETTINGS.workerRelease)) process.exit(0);},10);
} else {
fs.writeFileSync(SETTINGS.pid, String(process.pid));
if(SETTINGS.worker) require('child_process').spawn(process.execPath,[__filename,'worker-server'],{stdio:'ignore',detached:true}).unref();
function send(frame) {
 const line=JSON.stringify({jsonrpc:'2.0',...frame});
 if(SETTINGS.sent) fs.appendFileSync(SETTINGS.sent,line+'\n');
 process.stdout.write(line+'\n');
}
// Observe raw chunks before readline can hide a partial response. The optional
// observer is synchronous and opens its own committed store connection.
process.stdin.on('data', chunk => {
 if(SETTINGS.raw) fs.appendFileSync(SETTINGS.raw,chunk);
 if(SETTINGS.observer && fs.existsSync(SETTINGS.observer))
  require('child_process').execFileSync(SETTINGS.observer,[],{stdio:'ignore'});
});
const input = readline.createInterface({input:process.stdin});
let pendingScenario = null;
let newCount = 0;
function scenario() {
 if(!fs.existsSync(SETTINGS.scenario)) return null;
 const raw=fs.readFileSync(SETTINGS.scenario,'utf8');
 return raw.startsWith('{') ? JSON.parse(raw) : null;
}
input.on('line', async line => {
 const frame=JSON.parse(line);
 fs.appendFileSync(SETTINGS.trace,JSON.stringify(frame)+'\n');
 if(!frame.method && pendingScenario) {
  pendingScenario.ids.delete(JSON.stringify(frame.id));
  if(pendingScenario.ids.size===0) {pendingScenario.resolve();pendingScenario=null;}
  return;
 }
 if(frame.method==='initialize') send({id:frame.id,result:{protocolVersion:1,agentCapabilities:{askQuestion:true}}});
 else if(frame.method==='session/new') {
  if(fs.existsSync(SETTINGS.scenario) && fs.readFileSync(SETTINGS.scenario,'utf8')==='blocked-input') {
   input.pause(); process.stdin.pause();
   while(!fs.existsSync(SETTINGS.release)) await new Promise(r=>setTimeout(r,10));
   setInterval(()=>{},1000);
  }
  const scripted = scenario();
  const count = ++newCount;
  if(count > 1 && scripted?.waitForNew)
   while(!fs.existsSync(SETTINGS.newRelease)) await new Promise(r=>setTimeout(r,10));
  const reply = count > 1 ? scripted?.newReplies?.[count-2] : null;
  send({id:frame.id,...(reply || {result:{sessionId:count===1 ? (scripted?.sessionId || 'synthetic-session-1') : 'synthetic-session-'+count}})});
  if(count > 1 && scripted?.afterNew) for(const event of scripted.afterNew) send(event);
 }
 else if(frame.method==='session/prompt') {
  const text=frame.params.prompt[0].text;
  const scripted=scenario();
  if(scripted?.requests?.length) {
   const answers = new Promise(resolve=>{
    if(scripted.pauseAfterRequests) { input.pause(); process.stdin.pause(); }
    pendingScenario={resolve,ids:new Set(scripted.requests.map(r=>JSON.stringify(r.id)))};
    for(const request of scripted.requests) send(request);
    if(scripted.pauseAfterRequests) process.stdout.write('',()=>{
     const cell=new Int32Array(new SharedArrayBuffer(4));
     if(SETTINGS.paused) fs.writeFileSync(SETTINGS.paused,'paused');
     while(!fs.existsSync(SETTINGS.release)) Atomics.wait(cell,0,0,10);
    });
   });
   if(!scripted.completeBeforeAnswers) await answers;
  }
  if(scripted?.waitForRelease) while(!fs.existsSync(SETTINGS.release)) await new Promise(r=>setTimeout(r,10));
  if(scripted?.afterAnswers) for(const update of scripted.afterAnswers) send(update);
  if(text==='HOLD') while(!fs.existsSync(SETTINGS.release)) await new Promise(r=>setTimeout(r,10));
  if(text==='DIE') process.exit(0);
  if(text==='DENY_FOREVER') {
   let n=0;
   // Unsupported authorization requires turn cancellation; direct questions do not.
   setInterval(()=>send({id:'denial-'+(++n),method:'cursor/authorize',params:{}}),150);
   return;
  }
  if(text==='ASK') {
   send({id:'question-direct',method:'cursor/ask_question',params:{questions:[{text:'one'},{text:'two'}]}});
   send({id:'question-fallback',method:'session/request_permission',params:{toolCall:{title:'AskUserQuestion'},options:[{optionId:'yes',kind:'allow_once'}]}});
   send({id:'unknown-auth',method:'cursor/authorize',params:{}});
   send({id:'plan',method:'cursor/create_plan',params:{}});
  }

  send({method:'session/update',params:{sessionId:frame.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'Synthetic reply: '+text}}}});
  send({id:frame.id,result:{stopReason:'end_turn'}});
 }
});

}
