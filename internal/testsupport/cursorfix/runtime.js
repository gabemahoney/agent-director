// Synthetic ACP protocol 1 stand-in; never invokes or authenticates Cursor.
const fs = require('fs');
const readline = require('readline');
const selection = Object.fromEntries(['CURSOR_CONFIG_DIR','CURSOR_DATA_DIR','CURSOR_API_KEY','AGENT_CLI_CREDENTIAL_STORE'].map(key=>[key,process.env[key] ?? null]));
function artifact(operation, input) {
 const result={operation,input,result:'Synthetic reply: '+input,pid:process.pid,cwd:process.cwd(),selection};
 for(const key of ['CURSOR_CONFIG_DIR','CURSOR_DATA_DIR']) if(selection[key]) {
  fs.mkdirSync(selection[key],{recursive:true});
  fs.writeFileSync(require('path').join(selection[key],process.pid+'-'+operation+'.json'),JSON.stringify(result));
 }
 return result;
}
if(process.argv[2]==='worker-server') {
 fs.writeFileSync(SETTINGS.workerPID,String(process.pid));
 fs.writeFileSync(SETTINGS.workerPID+'.selection.json',JSON.stringify(selection));
 let last='';
 setInterval(()=>{
  if(fs.existsSync(SETTINGS.workerRelease)) process.exit(0);
  const request=SETTINGS.workerPID+'.request.json';
  if(!fs.existsSync(request)) return;
  const op=JSON.parse(fs.readFileSync(request,'utf8'));
  if(typeof op.operation!=='string' || !/^[a-z0-9-]{1,64}$/.test(op.operation) || typeof op.input!=='string' || op.input.length>256) throw Error('invalid synthetic worker operation');
  if(op.operation===last) return;
  last=op.operation;
  let result=artifact(op.operation,op.input);
  if(op.hook) {
   const child=require('child_process').spawnSync(op.hook.program,op.hook.args,{input:JSON.stringify(op.hook.payload),encoding:'utf8',env:{...process.env,...op.hook.env}});
   result.hook={status:child.status,stdout:child.stdout,stderr:child.stderr};
  }
  fs.writeFileSync(SETTINGS.workerPID+'.result.json.tmp',JSON.stringify(result));
  fs.renameSync(SETTINGS.workerPID+'.result.json.tmp',SETTINGS.workerPID+'.result.json');
 },10);
} else {
fs.writeFileSync(SETTINGS.pid, String(process.pid));
fs.writeFileSync(SETTINGS.pid+'.selection.json',JSON.stringify(Object.fromEntries(
 ['CURSOR_CONFIG_DIR','CURSOR_DATA_DIR','CURSOR_API_KEY','AGENT_CLI_CREDENTIAL_STORE'].map(key=>[key,process.env[key] ?? null]))));
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
 if(frame.method==='initialize') send({id:frame.id,...(scenario()?.initializeReply || {result:{protocolVersion:1,agentCapabilities:{askQuestion:true,...(scenario()?.loadSupport ? {loadSession:true} : {})}}})});
 else if(frame.method==='session/load') {
  const scripted=scenario();
  if(scripted?.beforeLoadReply) for(const event of scripted.beforeLoadReply) send(event);
  if(scripted?.waitForLoad)
   while(!fs.existsSync(SETTINGS.newRelease)) await new Promise(r=>setTimeout(r,10));
  // Source-derived synthetic agent-store.ts existence check; success remains scripted ACP, never authenticated recall.
  const nativeMissing=scripted?.nativeConfigRoot && (process.env.CURSOR_CONFIG_DIR!==scripted.nativeConfigRoot || !fs.existsSync(require('path').join(process.env.CURSOR_CONFIG_DIR,'acp-sessions',frame.params.sessionId,'store.db')));
  send({id:frame.id,...(nativeMissing ? {error:{code:-32603,message:'Session "'+frame.params.sessionId+'" not found'}} : (scripted?.loadReply || {result:{...(scripted?.modes ? {modes:scripted.modes} : {})}}))});
  if(scripted?.afterLoadReply) for(const event of scripted.afterLoadReply) send(event);
 }
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
  const reply = count > 1 ? scripted?.newReplies?.[count-2] : scripted?.firstNewReply;
  send({id:frame.id,...(reply || {result:{sessionId:count===1 ? (scripted?.sessionId || 'synthetic-session-1') : 'synthetic-session-'+count,...(scripted?.modes ? {modes:scripted.modes} : {})}})});
  if(count > 1 && scripted?.afterNew) for(const event of scripted.afterNew) send(event);
 }
 else if(frame.method==='session/set_mode') {
  const scripted=scenario();
  if(scripted?.beforeModeReply) for(const event of scripted.beforeModeReply) send(event);
  if(scripted?.waitForMode)
   while(!fs.existsSync(SETTINGS.release)) await new Promise(r=>setTimeout(r,10));
  send({id:frame.id,...(scripted?.modeReply || {result:{}})});
  if(scripted?.afterModeReply) for(const event of scripted.afterModeReply) send(event);
 }
 else if(frame.method==='session/prompt') {
  const text=frame.params.prompt[0].text;
  const scripted=scenario();
  if(scripted?.promptReply) { send({id:frame.id,...scripted.promptReply}); return; }
  if(scripted?.beforeRequests) for(const update of scripted.beforeRequests) send(update);
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

  if(scripted?.recordArtifacts) artifact(String(frame.id),text);
  send({method:'session/update',params:{sessionId:frame.params.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'Synthetic reply: '+text}}}});
  send({id:frame.id,result:{stopReason:'end_turn'}});
 }
});

}
