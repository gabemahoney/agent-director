// Synthetic ACP protocol 1 stand-in; never invokes or authenticates Cursor.
const fs = require('fs');
const readline = require('readline');
if(process.argv[2]==='worker-server') {
 fs.writeFileSync(SETTINGS.workerPID,String(process.pid));
 setInterval(()=>{if(fs.existsSync(SETTINGS.workerRelease)) process.exit(0);},10);
} else {
fs.writeFileSync(SETTINGS.pid, String(process.pid));
if(SETTINGS.worker) require('child_process').spawn(process.execPath,[__filename,'worker-server'],{stdio:'ignore'}).unref();
function send(frame) { console.log(JSON.stringify({jsonrpc:'2.0',...frame})); }
const input = readline.createInterface({input:process.stdin});
input.on('line', async line => {
 const frame=JSON.parse(line);
 fs.appendFileSync(SETTINGS.trace,JSON.stringify(frame)+'\n');
 if(frame.method==='initialize') send({id:frame.id,result:{protocolVersion:1,agentCapabilities:{askQuestion:true}}});
 else if(frame.method==='session/new') {
  if(fs.existsSync(SETTINGS.scenario) && fs.readFileSync(SETTINGS.scenario,'utf8')==='blocked-input') {
   input.pause(); process.stdin.pause();
   while(!fs.existsSync(SETTINGS.release)) await new Promise(r=>setTimeout(r,10));
   setInterval(()=>{},1000);
  }
  send({id:frame.id,result:{sessionId:'synthetic-session-1'}});
 }
 else if(frame.method==='session/prompt') {
  const text=frame.params.prompt[0].text;
  if(text==='HOLD') while(!fs.existsSync(SETTINGS.release)) await new Promise(r=>setTimeout(r,10));
  if(text==='DIE') process.exit(0);
  if(text==='DENY_FOREVER') {
   let n=0;
   setInterval(()=>send({id:'denial-'+(++n),method:'cursor/ask_question',params:{}}),150);
   return;
  }
  if(text==='ASK') {
   send({id:'question-direct',method:'cursor/ask_question',params:{questions:[{text:'one'},{text:'two'}]}});
   send({id:'question-fallback',method:'session/request_permission',params:{toolCall:{title:'AskUserQuestion'},options:[{optionId:'yes',kind:'allow_once'}]}});
   send({id:'unknown-auth',method:'cursor/authorize',params:{}});
   send({id:'plan',method:'cursor/create_plan',params:{}});
  }

  send({method:'session/update',params:{sessionId:'synthetic-session-1',update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'Synthetic reply: '+text}}}});
  send({id:frame.id,result:{stopReason:'end_turn'}});
 }
});

}
