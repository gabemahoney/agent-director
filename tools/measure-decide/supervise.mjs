// Outside every measured call. One supervised process group, bounded admission.
import fs from 'node:fs';
import path from 'node:path';
import {spawn} from 'node:child_process';
const [base,dir,kind,seconds,...command]=process.argv.slice(2);
const GiB=1024**3, MiB=1024**2;
const safe=p=>{if(!p||p.split(/[\\/]/).includes('.claude'))throw Error('forbidden component');return p};
safe(base);safe(dir);
const hardStop=Date.parse('2026-10-16T13:26:43Z');
// Counts is available in-process, but no bounded live consumer reports it to
// this supervisor. Snapshot headers cannot cover overflow in unfinished calls.
// Refuse target admission until that coverage exists; builds execute no calls.
const liveRecorderCoverage={result:'INCOMPLETE',available:false,policy:'target admission refused',reason:'No live Counts consumer; exported snapshot headers do not cover unfinished-call slot overflow.'};
const compileOnly=kind==='build'&&['./pkg/api','./internal/store'].some(pkg=>[[],['-race']].some(flags=>JSON.stringify(command)===JSON.stringify(['go','test','-vet=off',...flags,'-c','-overlay',dir+'/overlay/overlay.json','-o',dir+'/target.test',pkg])));
const started=Date.now(), deadline=Math.min(started+Number(seconds)*1000,hardStop);
const caps={pids:1024,retained:2*GiB,disposable:8*GiB,trace_process:128*MiB,stdout:16*MiB,drain_ms:10000,hard_stop:'2026-10-16T13:26:43Z'};
if(kind==='enforcement-fault') {
 const f=JSON.parse(fs.readFileSync(dir+'/fault-caps.json','utf8'));
 for(const key of ['pids','retained','disposable','trace_process','stdout'])if(f[key]){if(f[key]>caps[key]||f[key]<=0)throw Error('fault cap may only lower');caps[key]=f[key]}
}
const firstFile=dir+'/first-trigger.json';
const globalStop=base+'/natural-admission-stop.json';
const write=(p,v)=>fs.writeFileSync(safe(p),JSON.stringify(v,null,2)+'\n',{flag:'wx'});
let first=null,child=null,closed=false,peak={pids:0,retained:0,disposable:0},sampleCount=0;
const logOffsets=new Map(),logTails=new Map();
const resourcePattern=/resource temporarily unavailable|failed to create new OS thread|pthread_create|no space left on device|trace quota exceeded|trace overflow|diagnostic.*cap exceeded|out of memory/i;
function usage(root) {
 let total=0,count=0;
 const seen=new Set();
 const walk=p=>{
  // Prune forbidden names before lstat/stat/access; never follow links.
  if(path.basename(p)==='.claude')return;
  if(++count>200000)throw Error('accounting file-count bound exceeded');
  let s;try{s=fs.lstatSync(safe(p))}catch(e){if(e.code==='ENOENT')return;throw e}
  if(s.isSymbolicLink())return;
  if(seen.has(s.dev+':'+s.ino))return;
  seen.add(s.dev+':'+s.ino);
  if(s.isDirectory())for(const name of fs.readdirSync(p)){if(name!=='.claude')walk(path.join(p,name))}
  else if(s.isFile())total+=Math.max(s.size,s.blocks*512);
 };
 walk(root);return total;
}
const number=p=>Number(fs.readFileSync(p,'utf8').trim());
const counters=p=>Object.fromEntries(fs.readFileSync(p,'utf8').trim().split('\n').map(l=>{const[k,v]=l.split(/\s+/);return[k,Number(v)]}));
const before={pids:counters('/sys/fs/cgroup/pids.events'),memory:counters('/sys/fs/cgroup/memory.events')};
function stop(reason,details) {
 if(first)return;
 first={reason,details,wall:new Date().toISOString(),elapsed_ms:Date.now()-started,kind,peak:{...peak},samples:sampleCount,admission:'stopped; no later target admitted by this supervisor',already_running:child?{pid:child.pid,process_group:child.pid,exit_observed:closed}:null};
 write(firstFile,first);
 if(kind==='natural'&&!fs.existsSync(globalStop))write(globalStop,first);
 if(child&&!closed) {
  try{process.kill(-child.pid,'SIGTERM')}catch{}
  setTimeout(()=>{if(!closed)try{process.kill(-child.pid,'SIGKILL')}catch{}},10000).unref();
 }
}
function sample() {
 sampleCount++;
 try{
  const pid=number('/sys/fs/cgroup/pids.current'),pe=counters('/sys/fs/cgroup/pids.events'),me=counters('/sys/fs/cgroup/memory.events');
  const retained=usage(base),stat=fs.statfsSync('/tmp'),disposable=(stat.blocks-stat.bfree)*stat.bsize;
  peak.pids=Math.max(peak.pids,pid);peak.retained=Math.max(peak.retained,retained);peak.disposable=Math.max(peak.disposable,disposable);
  if(pid>=caps.pids||pe.max>before.pids.max)stop('pid-cap',{pid,events:pe,before:before.pids});
  if((me.oom||0)>(before.memory.oom||0)||(me.oom_kill||0)>(before.memory.oom_kill||0)||(me.max||0)>(before.memory.max||0))stop('memory-resource-event',{events:me,before:before.memory});
  if(retained>=caps.retained)stop('retained-artifact-cap',{bytes:retained});
  if(disposable>=caps.disposable)stop('disposable-disk-cap',{bytes:disposable});
  for(const name of ['go.jsonl','go.stderr','bootstrap.log','bun.log']) {
   const file=dir+'/'+name;if(!fs.existsSync(file))continue;
   const stat=fs.lstatSync(file);if(!stat.isFile()||stat.isSymbolicLink()){stop('invalid-live-log',{file:name});continue}
   if(stat.size>caps.stdout)stop('output-artifact-cap',{file:name,bytes:stat.size});
   let offset=logOffsets.get(name)||0;
   if(stat.size<offset){stop('truncated-live-log',{file:name});continue}
   const fd=fs.openSync(file,'r');
   while(offset<stat.size&&!first){const b=Buffer.alloc(Math.min(65536,stat.size-offset));const n=fs.readSync(fd,b,0,b.length,offset);if(!n)break;offset+=n;const tail=(logTails.get(name)||'')+b.subarray(0,n).toString();const match=resourcePattern.exec(tail);if(match)stop('resource-error-output',{file:name,match:match[0],tail:tail.slice(-4096)});logTails.set(name,tail.slice(-4096))}
   fs.closeSync(fd);logOffsets.set(name,offset);
  }
  const processes=fs.readdirSync(dir).filter(n=>/^process-[a-f0-9]{32}\.json$/.test(n));
  for(const n of processes) {
   let p;try{p=JSON.parse(fs.readFileSync(dir+'/'+n,'utf8'))}catch(e){stop('invalid-live-process-inventory',{file:n,error:e.message});continue}
   let trace=0;
   for(const s of p.snapshots||[]) {
    if(typeof s.file!=='string'||path.basename(s.file)!==s.file) {stop('invalid-recorder-path',{file:s.file});continue}
    const file=safe(dir+'/'+s.file);
    if(!fs.existsSync(file))continue;
    const st=fs.lstatSync(file);if(!st.isFile()||st.isSymbolicLink()){stop('invalid-recorder-file',{file:s.file});continue}
    trace+=st.size;
    const fd=fs.openSync(file,'r'),head=Buffer.alloc(1024),len=fs.readSync(fd,head,0,1024,0);fs.closeSync(fd);
    const line=head.subarray(0,len).toString().split('\n')[0];
    try{const h=JSON.parse(line);if(h.overflow>0)stop('recorder-overflow',{process:p.process,file:s.file,overflow:h.overflow})}catch{}
   }
   if(trace>=caps.trace_process)stop('per-process-trace-cap',{process:p.process,bytes:trace});
  }
  if(Date.now()>=deadline)stop(Date.now()>=hardStop?'aggregate-hard-stop':'process-wall-watchdog',{deadline:new Date(deadline).toISOString()});
 }catch(e){stop('accounting-unavailable',{error:e.message,scope:'cannot claim covered-path quota enforcement'})}
}
if(kind==='natural'&&fs.existsSync(globalStop))throw Error('natural admission already stopped');
if(Date.now()>=hardStop)stop('aggregate-hard-stop',{});
if(!compileOnly)stop('live-recorder-coverage-unavailable',{...liveRecorderCoverage,scope:'all target execution routes, including enforcement faults; only exact driver compile commands exempt; no target admitted'});
const mount=fs.readFileSync('/proc/self/mountinfo','utf8').split('\n').find(l=>l.split(' ')[4]==='/tmp');
const tmpstat=fs.statfsSync('/tmp');
const tmpHardBound=Boolean(mount?.includes(' - tmpfs ')&&tmpstat.blocks*tmpstat.bsize<=8*GiB);
if(!tmpHardBound&&kind!=='enforcement-fault')stop('accounting-unavailable',{path:'/tmp',reason:'8GiB filesystem hard bound cannot be proved; no target admitted'});
if(kind==='full')stop('accounting-unavailable',{path:'/work outside retained artifacts',reason:'Full arm writes outside the bounded fixture namespace are not provably quota-bounded. Full arm INCOMPLETE and unadmitted; no new full arm authorized in this pass.'});
sample();
const retainedStart=usage(base);
// Reserve the maximum bounded trace, JSON exports, stream and generated outputs.
if(kind!=='enforcement-fault'&&retainedStart+512*MiB>caps.retained)stop('insufficient-retained-reserve',{retainedStart,reserve:512*MiB});
write(dir+'/supervisor-admission.json',{kind,command,caps,deadline:new Date(deadline).toISOString(),retained_start:retainedStart,tmp_hard_bound:tmpHardBound,tmp_mount:mount,covered_paths:{disposable:'/tmp (all fixtures and isolated TestMain HOME)',retained:base},accounting:'100ms live samples plus bounded trace writer/artifact count/stream; no .claude traversal',live_recorder_coverage:liveRecorderCoverage,coverage_limitations:[liveRecorderCoverage.reason,...(tmpHardBound?[]:['No filesystem hard quota for /tmp; sampled stopping cannot prove peak bound. Evidence must remain INCOMPLETE.'])]});
if(!first) {
 if(kind==='natural') {
  const record=base+'/natural-admissions.json';const admissions=fs.existsSync(record)?JSON.parse(fs.readFileSync(record,'utf8')):[];
  if(admissions.length>=5)throw Error('five targeted-process admission cap reached');
  admissions.push({directory:dir,wall:new Date().toISOString(),command});
  fs.writeFileSync(record,JSON.stringify(admissions,null,2)+'\n');
 }
 const stream=fs.openSync(dir+'/test.log','wx');let bytes=0,tail='';
 child=spawn(command[0],command.slice(1),{detached:true,env:process.env,stdio:['ignore','pipe','pipe']});
 write(dir+'/admitted-process.json',{pid:child.pid,kind,command,start_wall:new Date().toISOString(),lifecycle:'OS process group; recorder lifecycle independently joins pid+origin'});
 const output=data=>{
  bytes+=data.length;
  const text=data.toString();tail=(tail+text).slice(-4096);
  const resource=resourcePattern.exec(tail);
  if(resource)stop('resource-error-output',{match:resource[0],tail});
  if(bytes>caps.stdout)stop('output-artifact-cap',{bytes});
  if(bytes<=caps.stdout)fs.writeSync(stream,data);
 };
 child.stdout.on('data',output);child.stderr.on('data',output);
 child.on('error',e=>stop('process-launch-error',{error:e.message}));
 const timer=setInterval(sample,100);
 const code=await new Promise(resolve=>child.on('close',(code,signal)=>{closed=true;resolve({code,signal})}));
 clearInterval(timer);fs.closeSync(stream);sample();
 const report={kind,result:first?'STOPPED':code.code===0?'COMMAND_PASSED':'COMMAND_FAILED',...code,first_trigger:first,peak,samples:sampleCount,bytes,elapsed_ms:Date.now()-started,coverage_complete:false,live_recorder_coverage:liveRecorderCoverage,started_processes:1,completed_processes:closed?1:0,unfinished_processes:closed?0:1,later_admitted:0};
 write(dir+'/supervisor-result.json',report);
 console.log(JSON.stringify(report));
 process.exitCode=first?125:code.code||0;
} else {
 write(dir+'/supervisor-result.json',{result:'NOT_ADMITTED',evidence_result:'INCOMPLETE',coverage_complete:false,live_recorder_coverage:liveRecorderCoverage,first_trigger:first,peak,samples:sampleCount,started_processes:0,later_admitted:0});
 process.exitCode=125;
}
