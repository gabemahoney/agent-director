// One actual public caller per invocation. Its exit/replacement cannot own the pane.
import {spawn} from 'node:child_process';
import {createInterface} from 'node:readline';
const [surface,verb,home,store,encoded]=process.argv.slice(2);
const params=JSON.parse(encoded);
if(surface==='Bun' && !process.env.PC_INSIDE_INSTALLED) {
 if(!process.env.PC_INSTALLED_CALLER)throw Error('installed consumer required');
 const p=spawn('bun',['--no-install',process.env.PC_INSTALLED_CALLER,...process.argv.slice(2)],{stdio:'inherit',env:{...process.env,PC_INSIDE_INSTALLED:'1'}});
 process.exitCode=await new Promise(r=>p.on('exit',r));
} else if(surface==='Bun') {
 const {Client}=await import('agent-director');
 const client=await Client.create({home,storePath:store,_cliPath:'/usr/local/bin/agent-director'});
 const method={'resume':'resume','list-subagents':'listSubagents','send-keys':'sendKeys','get':'get','read-pane':'readPane','pause':'pause','get-permission':'getPermission'}[verb];
 try {console.log(JSON.stringify(await client[method](params)));} finally {await client.close();}
} else if(surface==='MCP') {
 const p=spawn('agent-director',['serve','--stdio','--home',home,'--store-path',store],{stdio:['pipe','pipe','inherit']});
 const lines=createInterface({input:p.stdout});
 const iterator=lines[Symbol.asyncIterator](); let seq=0;
 async function call(method,params) {
  const id=++seq; p.stdin.write(JSON.stringify({jsonrpc:'2.0',id,method,params})+'\n');
  for(;;){const {value,done}=await iterator.next(); if(done)throw Error('MCP closed'); const r=JSON.parse(value);if(r.id===id){if(r.error)throw Error(JSON.stringify(r.error));return r.result;}}
 }
 try {
  await call('initialize',{protocolVersion:'2024-11-05',capabilities:{},clientInfo:{name:'pane-controls',version:'1'}});
  const result=await call('tools/call',{name:verb,arguments:params});
  if(result.isError)throw Error(JSON.stringify(result));
  if(result.content?.length!==1)throw Error('MCP content shape');
  console.log(result.content[0].text);
 } finally {p.stdin.end();p.kill();await new Promise(r=>p.once('exit',r));lines.close();}
} else throw Error('unsupported collector surface');
