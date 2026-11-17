// Run with Bun; import the image's installed public tarball, never its source module.
import {spawn, execFileSync, type ChildProcess} from 'node:child_process';
import {createInterface} from 'node:readline';
import {performance} from 'node:perf_hooks';
import {Client} from '/opt/cursor-pane-bun/consumer/node_modules/agent-director/dist/index.js';
const [build, work, covered, uncovered, caller] = process.argv.slice(2);
const children:{p:ChildProcess,ended:Promise<void>}[]=[];
function stream(command:string,args:string[]) {
  const p=spawn(command,args,{stdio:['pipe','pipe','inherit']});
  let streamError:Error|undefined;
  const ended=new Promise<void>(resolve=>{
    p.once('exit',()=>resolve());
    p.once('error',error=>{streamError=error;resolve();});
  });
  children.push({p,ended});
  p.stdin.on('error',error=>{streamError=error;});
  const lines=createInterface({input:p.stdout})[Symbol.asyncIterator]();
  return {p,
    async read(){const {value,done}=await lines.next();if(done)throw streamError??Error(command+' closed');return JSON.parse(value);},
    async write(v:any){
      if(streamError)throw streamError;
      await new Promise<void>((resolve,reject)=>p.stdin.write(JSON.stringify(v)+'\n',error=>error?reject(error):resolve()));
    }
  };
}
let go:ReturnType<typeof stream>,mcp:ReturnType<typeof stream>,ts:Client|undefined;
let sequence=0;
async function rpc(method:string,params:any) {
  const id=++sequence;await mcp.write({jsonrpc:'2.0',id,method,params});
  for(;;){const r=await mcp.read();if(r.id===id){if(r.error)throw Error(JSON.stringify(r.error));return r.result;}}
}
async function send(surface:string,params:any) {
  if(surface==='go'){await go.write(params);const r=await go.read();return r.result;}
  if(surface==='ts')return await ts!.sendKeys(params);
  if(surface==='mcp'){
    const r=await rpc('tools/call',{name:'send-keys',arguments:params});
    if(r.isError||r.content?.length!==1)throw Error(JSON.stringify(r));
    return JSON.parse(r.content[0].text);
  }
  const args=['send-keys','--instance-id',params.instance_id,'--text',params.text];
  if(params.confirmed_delivery)args.push('--confirmed-delivery','--caller-uuid',caller,'--delivery-request-id',params.delivery_request_id);
  return JSON.parse(execFileSync('agent-director',args,{encoding:'utf8',timeout:5000}));
}
async function bounded<T>(pending:Promise<T>,message:string) {
  let timer:ReturnType<typeof setTimeout>;
  try {
    return await Promise.race([pending,new Promise<never>((_,reject)=>{
      timer=setTimeout(()=>reject(Error(message)),5000);
    })]);
  } finally { clearTimeout(timer!); }
}
let failed=false;
try {
  go=stream(build+'/callers',[]);
  mcp=stream('agent-director',['serve','--stdio']);
  const goReady=await bounded(go.read(),'Go caller initialization timed out');
  await bounded(rpc('initialize',{protocolVersion:'2024-11-05',capabilities:{},clientInfo:{name:'install-availability',version:'1'}}),'MCP initialization timed out');
  ts=await Client.create({home:process.env.HOME,callTimeoutMs:5000});
  console.log(JSON.stringify({ready:true,go_pid:goReady.pid,mcp_pid:mcp.p.pid}));
  for await (const line of createInterface({input:process.stdin})) {
    const {phase}=JSON.parse(line);const rows=[];
    for(const surface of ['cli','mcp','go','ts']) {
      for(const target of ['covered','uncovered']) {
        const name=phase+'-'+surface;
        const text=target==='covered' ? `${name}: ü 漢字 🙂\nwhole message` : `printf '%s\\n' '${name}' >> '${work}/uncovered'; : > '${work}/fence-${name}'`;
        const p:any={instance_id:target==='covered'?covered:uncovered,text};
        if(target==='covered')Object.assign(p,{confirmed_delivery:true,caller_uuid:caller,delivery_request_id:name});
        const start=performance.now(),result:any=await bounded(send(surface,p),'send exceeded unchanged 5s caller bound'),elapsed_ms=performance.now()-start;
        if(elapsed_ms>5000)throw Error('send exceeded unchanged 5s caller bound');
        if(target==='covered') {
          if(result.class!=='held'||result.final!==false||result.caller_uuid!==caller||result.request_id!==name)throw Error(JSON.stringify({surface,result}));
        } else if(JSON.stringify(result)!=='{}')throw Error(JSON.stringify({surface,result}));
        rows.push({phase,surface,target,text,request_id:name,elapsed_ms,result,store_wait_ms:50,call_bound_ms:5000});
      }
    }
    console.log(JSON.stringify({phase,rows,go_pid:go.p.pid,mcp_pid:mcp.p.pid}));
  }
} catch(error) {
  failed=true;
  throw error;
} finally {
  const cleanup=await Promise.allSettled([
    Promise.resolve().then(()=>ts?.close()),
    ...children.map(async ({p,ended})=>{
      const timer=setTimeout(()=>p.kill('SIGKILL'),3000);
      try {
        p.stdin?.end();
        await bounded(ended,'caller did not exit after bounded termination');
      } finally { clearTimeout(timer); }
    })
  ]);
  const errors=cleanup.flatMap(result=>result.status==='rejected'?[result.reason]:[]);
  if(errors.length) {
    // Cleanup diagnostics must never replace the original caller failure.
    if(failed)console.error('Caller cleanup errors:',errors);
    else throw new AggregateError(errors,'caller cleanup failed');
  }
}
