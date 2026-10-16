import fs from 'node:fs';
import path from 'node:path';
const [dir]=process.argv.slice(2);
const safe=p=>{if(!p||p.split(/[\\/]/).includes('.claude'))throw Error('forbidden path component');return p};
safe(dir);
const out={result:'INCOMPLETE',processes:[],problems:[],started:0,completed:0,unfinished:0,counts_unknown:false,missing_exports:0,missing_export_inventory:[],source_root_joins:[],limitations:['Counts cover instrumented invocations only. Missing process/call exports imply unknown counts; interrupted calls never become success. Snapshots are reservation-prefix cutoffs, not worker quiescence.']};
const problem=m=>out.problems.push(m);
const read=p=>JSON.parse(fs.readFileSync(safe(p),'utf8'));
try {
 if(fs.existsSync(dir+'/supervisor-result.json')) {const s=read(dir+'/supervisor-result.json');if(['STOPPED','NOT_ADMITTED'].includes(s.result))problem('supervisor '+s.result+': '+s.first_trigger?.reason)}
 const expected=read(dir+'/run-inventory.json');
 if(!Array.isArray(expected.expected_processes)||expected.expected_processes.length===0)problem('no explicit expected process inventory');
 const names=fs.readdirSync(dir);
 const inventory=names.filter(n=>/^process-[a-f0-9]{32}\.json$/.test(n)).map(n=>read(dir+'/'+n));
 const lifecycles=new Set(), origins=new Map(), snapshotsSeen=new Set();
 for(const e of expected.expected_processes||[]) {
  const match=inventory.filter(p=>p.package===e.package&&p.mode===e.mode);
  if(match.length!==e.count) {problem('process coverage '+JSON.stringify(e)+' actual='+match.length);out.missing_exports+=Math.max(0,e.count-match.length);out.counts_unknown=true;if(match.length<e.count)out.missing_export_inventory.push({kind:'process',package:e.package,mode:e.mode,expected:e.count,actual:match.length,missing:e.count-match.length})}
 }
 for(const p of inventory) {
  if(!/^[a-f0-9]{32}$/.test(p.process)||!Number.isInteger(p.pid)||p.pid<=0||!['on','off'].includes(p.mode))problem('invalid process lifecycle/mode');
  if(!expected.expected_processes.some(e=>p.package===e.package&&p.mode===e.mode))problem('unexpected process inventory '+p.process);
  if(lifecycles.has(p.process))problem('reused process lifecycle '+p.process);
  lifecycles.add(p.process);
  const proc={process:p.process,pid:p.pid,mode:p.mode,package:p.package,state:p.state,trace_bytes:0,snapshots:[],calls:0};
  out.processes.push(proc);
  if(p.state!=='completed') {problem('unfinished process '+p.process);proc.counts='unknown beyond last export';out.counts_unknown=true}
  if(!Array.isArray(p.calls)) {problem('missing call export '+p.process);out.counts_unknown=true;out.missing_exports++;out.missing_export_inventory.push({kind:'calls',process:p.process,package:p.package,mode:p.mode,reason:'missing or nonarray calls'});proc.calls=null;proc.counts='unknown; missing call export';continue}
  const calls=p.calls, callIDs=new Set(), fixturePaths=new Map(), pathFixtures=new Map();
  for(const c of calls) {
   out.started++;proc.calls++;
   const tuple=[c.process,c.test,c.fixture,c.call];
   if(tuple.some(s=>typeof s!=='string'||!s||Buffer.byteLength(s)>128||s.includes('\ufffd')))problem('invalid external identity '+JSON.stringify(tuple));
   if(c.process!==p.process)problem('external process mismatch');
   if(callIDs.has(c.call))problem('reused invocation '+c.call);
   callIDs.add(c.call);
   if(typeof c.path!=='string'||!path.isAbsolute(c.path)||Buffer.byteLength(c.path)>1024||c.path.includes('\ufffd')||c.path.split('/').includes('.claude'))problem('invalid external store path');
   if(fixturePaths.has(c.fixture)&&fixturePaths.get(c.fixture)!==c.path)problem('conflicting fixture paths');
   if(pathFixtures.has(c.path)&&pathFixtures.get(c.path)!==c.fixture)problem('reused path across lifecycles');
   fixturePaths.set(c.fixture,c.path);pathFixtures.set(c.path,c.fixture);
   if(c.state==='completed'&&Number.isFinite(c.duration_ns)&&c.return_ns>=c.entry_ns){out.completed++}else{out.unfinished++;problem('unfinished invocation '+c.call)}
  }
  let finalEvents=null;
  if(p.mode==='off') {
   if(p.snapshots?.length)problem('off process claims recorder snapshots');
   continue;
  }
  if(!Array.isArray(p.snapshots)||!p.snapshots.length){problem('missing recorder inventory '+p.process);out.missing_exports++;continue}
  for(const label of ['tests-complete','final'])if(p.snapshots.filter(s=>s.label===label).length!==1)problem('missing/duplicate required snapshot '+p.process+' '+label);
  for(const s of p.snapshots) {
   if(typeof s.file!=='string'||path.basename(s.file)!==s.file||!s.file.startsWith('snapshot-'+p.process+'-')||!s.file.endsWith('.jsonl')) {problem('invalid recorder filename');continue}
   if(snapshotsSeen.has(s.file))problem('duplicate recorder file '+s.file);
   snapshotsSeen.add(s.file);
   if(s.state!=='completed'){problem('partial snapshot '+s.file);continue}
   const file=safe(dir+'/'+s.file);
   let stat;try{stat=fs.lstatSync(file)}catch(e){problem('missing snapshot '+s.file);out.missing_exports++;continue}
   if(!stat.isFile()||stat.isSymbolicLink()){problem('non-file recorder '+s.file);continue}
   proc.trace_bytes+=stat.size;
   if(proc.trace_bytes>128*1024*1024){problem('128MiB per-process trace cap '+p.process);continue}
   if(stat.size===0||stat.size!==s.bytes){problem('zero/size-conflicting snapshot '+s.file);continue}
   let rows;try{const raw=fs.readFileSync(file,'utf8');if(!raw.endsWith('\n'))throw Error('partial line');rows=raw.trim().split('\n').map(JSON.parse)}catch(e){problem('invalid snapshot '+s.file+': '+e.message);continue}
   const h=rows[0],f=rows.at(-1),events=rows.slice(1,-1);
   if(h.kind!=='snapshot'||f.kind!=='snapshot_end'||h.pid!==p.pid||h.overflow!==0||f.unpublished_reservations!==0||h.reserved!==events.length||h.reserved===0||h.reserved>h.capacity||events.some((e,i)=>e.kind!=='event'||e.slot!==i)) {problem('zero/missing/partial/overflow snapshot '+s.file);continue}
   const origin=h.pid+'|'+h.origin_wall;
   if(origins.has(p.process)&&origins.get(p.process)!==origin)problem('recorder process lifecycle conflict');
   if([...origins].some(([id,v])=>id!==p.process&&v===origin))problem('reused recorder lifecycle');
   origins.set(p.process,origin);
   proc.snapshots.push({file:s.file,reserved:h.reserved,cutoff_ns:h.cutoff_ns});
   if(s.label==='final')finalEvents=events;
  }
  if(!finalEvents){problem('no complete final recorder '+p.process);continue}
  const roots=finalEvents.filter(e=>['helper.enter','client.enter','client.list.enter','fixture.decision.enter'].includes(e.phase));
  const usedRoots=new Set();
  for(const c of calls) {
   const matching=roots.filter(e=>e.phase===c.route+'.enter'&&e.invocation?.state==='complete'&&['process','test','fixture','call'].every(k=>e.invocation[k]===c[k]));
   if(matching.length!==1){problem('nonunique/missing source root '+c.call+' candidates='+matching.length);continue}
   const root=matching[0].root;
   if(usedRoots.has(root))problem('reused source root '+root);
   usedRoots.add(root);
   const events=finalEvents.filter(e=>e.root===root);
   if(events.some(e=>!e.invocation||e.invocation.state!=='complete'||['process','test','fixture','call'].some(k=>e.invocation[k]!==c[k])))problem('conflicting descendant identity '+c.call);
   const returnPhase=c.route==='fixture.decision'?'fixture.decision.return':c.route+'.return_after_defers';
   const returns=events.filter(e=>e.phase===returnPhase);
   if(c.state==='completed'&&returns.length!==1)problem('missing/duplicate actual source return '+c.call);
   const paths=events.filter(e=>e.phase==='operation.store_identity');
   if(!paths.length&&events.some(e=>e.phase.startsWith('gate.')||e.phase==='worker.dispatch'||e.phase==='pool.acquire.enter'||e.phase==='commit.exec.enter'))problem('missing actual Store.Path identity '+c.call);
   for(const identity of paths)if(identity.store_path_state!=='complete'||identity.store_path!==c.path||identity.store_path_bytes!==Buffer.byteLength(c.path)||identity.store_path_bytes>1024)problem('invalid/conflicting actual store identity '+c.call);
   out.source_root_joins.push({process:p.process,test:c.test,fixture:c.fixture,call:c.call,route:c.route,path:c.path,source_root:root,store_identity_events:paths.length,actual_source_entry_ns:matching[0].ns,actual_source_return_ns:returns[0]?.ns,actual_external_duration_ns:c.duration_ns});
   if(c.route==='client.list') {
    const readbacks=names.filter(n=>n.startsWith('readback-')&&n.endsWith('.json')).map(n=>read(dir+'/'+n)).filter(r=>r.process===c.process&&r.test===c.test&&r.fixture===c.fixture&&r.call===c.call&&r.path===c.path);
    if(!readbacks.some(r=>r.label==='list-caller'))problem('missing explicit List caller readback '+c.call);
    if(c.error&&!readbacks.some(r=>r.label==='eventual-100ms'))problem('missing uncertain List eventual readback '+c.call);
   }
  }
  for(const root of roots)if(!usedRoots.has(root.root))problem('unattributed actual source root '+root.root+' '+root.phase);
 }
 for(const name of names.filter(n=>n.startsWith('snapshot-')&&n.endsWith('.jsonl')))if(!snapshotsSeen.has(name))problem('unlisted recorder '+name);
 if(!out.processes.length){problem('no process exports; call counts unknown');out.counts_unknown=true}
 out.result=out.problems.length?'INCOMPLETE':'PASS';
} catch(e){problem(e.stack||e.message);out.counts_unknown=true}
if(out.counts_unknown){out.known_prefix={started:out.started,completed:out.completed,unfinished:out.unfinished};out.started=null;out.completed=null;out.unfinished=null}
fs.writeFileSync(safe(dir+'/trace-quality.json'),JSON.stringify(out,null,2)+'\n');
console.log(JSON.stringify({result:out.result,started:out.started,completed:out.completed,unfinished:out.unfinished,joins:out.source_root_joins.length,problems:out.problems}));
if(out.problems.length)process.exitCode=1;
