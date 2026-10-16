import fs from 'node:fs';
import path from 'node:path';
const base='tickets/handoff/b.ftd/Plans/b.jf1/e4-task1/diagnostic/pass-02';
const read=(dir,file)=>fs.readFileSync(path.join(base,dir,file),'utf8');
const json=(dir,prefix)=>{const n=fs.readdirSync(path.join(base,dir)).filter(n=>n.startsWith(prefix)&&n.endsWith('.json')).sort().at(-1);return n?JSON.parse(read(dir,n)):null};
function quantiles(values){const a=values.toSorted((a,b)=>a-b),q=p=>a.length?a[Math.max(0,Math.ceil(p*a.length)-1)]:null;return {n:a.length,min:a[0]??null,p50:q(.5),p95:q(.95),p99:q(.99),max:a.at(-1)??null}}
function getTrace(dir){const name=fs.readdirSync(path.join(base,dir)).filter(n=>n.startsWith('snapshot-final')).sort().at(-1);if(!name)return null;const rows=read(dir,name).trim().split('\n').map(JSON.parse);return {header:rows[0],footer:rows.at(-1),events:rows.filter(e=>e.kind==='event').sort((a,b)=>a.ns-b.ns)}}
const names=fs.readdirSync(base).filter(n=>/^matrix[48]-p[1-5]-(on|off)$/.test(n)).sort();
const report={method:'nearest-rank ceil(p*n); units ns, wall inclusive of driver/database/sql and scheduling, not pure native/fsync service time',historical_cause:'UNKNOWN',runs:[],cells:{},overhead:[],limitations:['Only five process pairs per quota; leaves and concurrent calls are correlated; small tail samples support no causal p99 claim.','Pairs run off then on, without cache randomization; time/cache/scheduling effects can dominate signed differences.','All main samples use HEAD/current finite 5000ms semantic setups and the real driver; parked initial call keeps 100ms/1100ms bound.','Host/shared ancestor quotas and load are not independently sampled; cgroup counters describe the actual sandbox and include launcher/export activity.','Context state is sampled sequentially and may lag a crossed monotonic deadline; cancelled root at after-defers return is expected even on success.']};
const pairings={
 'helper.enter':'helper.return_after_defers','client.enter':'client.return_after_defers',
 'api.body.enter':'api.body.return','api.getSpawn.enter':'api.getSpawn.return','api.readRequest.enter':'api.readRequest.return','api.readAllRequests.enter':'api.readAllRequests.return','api.judgment.enter':'api.judgment.return','api.ack.enter':'api.ack.return',
 'operation.enter':'operation.caller_return','gate.wait.enter':['gate.acquired','gate.refused'],'worker.dispatch':'worker.enter',
 'pool.acquire.enter':'pool.acquire.return','setup.busy_timeout.enter':'setup.busy_timeout.return','begin.attempt.enter':'begin.attempt.return','transaction.body.enter':'transaction.body.return','guarded_update.scan.enter':'guarded_update.scan.return','commit.exec.enter':'commit.exec.return',
 'sql.publication.lock_wait':'sql.published_locked','cleanup.enter':'cleanup.return','cleanup.rollback.enter':'cleanup.rollback.return','cleanup.restore.enter':'cleanup.restore.return','cleanup.discard.enter':'cleanup.discard.return','lease.close.enter':'lease.close.return',
 'trail.queue_lock.wait':'trail.queue_lock.acquired','trail.accepted':'trail.dequeued','trail.mutex.wait':'trail.mutex.acquired','trail.write.enter':'trail.write.return','trail.sync.enter':'trail.sync.return',
};
const add=(map,key,value)=>{(map[key]??=[]).push(value)};
for(const name of names){
 const [,cpu,pair,mode]=name.match(/^matrix([48])-p([1-5])-(on|off)$/);
 const log=read(name,'test.log'),lines=[...log.matchAll(/--- (PASS|FAIL|SKIP): ([^ ]+) \(/g)].map(m=>({status:m[1],test:m[2]}));
 const leaves=lines.filter(x=>x.test==='TestDecideAbsoluteContextSurvivesParkedPhase'||x.test.includes('/'));
 const calls=json(name,'calls-')||[],trace=getTrace(name);
 const run={name,cpu:Number(cpu),pair:Number(pair),mode,exit:Number(read(name,'final-exit-code.txt')),leaves:{passed:leaves.filter(x=>x.status==='PASS').length,failed:leaves.filter(x=>x.status==='FAIL').length,skipped:leaves.filter(x=>x.status==='SKIP').length},calls_started:calls.length,calls_completed:calls.length,unexpected_errors:calls.filter(c=>c.error&&!c.expected_error),runtime:json(name,'runtime-'),cpu_max:read(name,'cpu.max.before').trim(),affinity:read(name,'affinity.txt').trim(),external_call_ns:quantiles(calls.filter(c=>!c.expected_error).map(c=>c.duration_ns)),parked_call_ns:quantiles(calls.filter(c=>c.expected_error).map(c=>c.duration_ns)),phase_ns:{},right_censored:[],late:[],commit_remaining_ns:null};
 if(run.exit||run.leaves.passed!==37||run.unexpected_errors.length)throw Error('matrix incomplete/failure '+name);
 const cell=report.cells[cpu+'cpu-'+mode]??={processes:0,passed_leaves:0,unexpected_errors:0,external_call_ns:[],parked_call_ns:[],phase_ns:{},commit_remaining_ns:[],right_censored:[],late:[]};
 cell.processes++;cell.passed_leaves+=run.leaves.passed;
 cell.external_call_ns.push(...calls.filter(c=>!c.expected_error).map(c=>c.duration_ns));cell.parked_call_ns.push(...calls.filter(c=>c.expected_error).map(c=>c.duration_ns));
 if(trace){
  if(trace.header.overflow||trace.footer.unpublished_reservations||trace.footer.kind!=='snapshot_end')throw Error('trace incomplete '+name);
  run.trace={reserved:trace.header.reserved,overflow:trace.header.overflow,unpublished:trace.footer.unpublished_reservations};
  const rootReturns=new Map(trace.events.filter(e=>['helper.return_after_defers','client.return_after_defers'].includes(e.phase)).map(e=>[e.root,e]));
  const pending=new Map();
  for(const e of trace.events){
   for(const [start,end] of Object.entries(pairings)){
    const key=e.span+' '+start;
    if(e.phase===start){if(!pending.has(key))pending.set(key,[]);pending.get(key).push(e)}
    if((Array.isArray(end)?end:[end]).includes(e.phase)){
     const enter=pending.get(key)?.shift();
     if(enter){const delta=e.ns-enter.ns;if(delta<0){run.right_censored.push({phase:start,reason:'acceptance marker can follow dequeue; not a negative duration sample',root:e.root,span:e.span});continue}add(run.phase_ns,start,delta);add(cell.phase_ns,start,delta)}
    }
   }
   if(e.phase==='commit.exec.enter'){(run.commit_remaining_ns??=[]).push(e.remaining_ns??null);if(e.remaining_ns!==undefined)cell.commit_remaining_ns.push(e.remaining_ns)}
   const ret=rootReturns.get(e.root);
   if(ret&&e.ns>ret.ns&&['worker.complete_before_done','cleanup.return','trail.worker_result','trail.result_published'].includes(e.phase)){const late={root:e.root,span:e.span,phase:e.phase,after_caller_ns:e.ns-ret.ns};run.late.push(late);cell.late.push({run:name,...late})}
  }
  for(const [key,es] of pending)for(const e of es){const c={root:e.root,span:e.span,phase:e.phase,ns:e.ns,reason:'no matched terminal event by final prefix snapshot'};run.right_censored.push(c);cell.right_censored.push({run:name,...c})}
  for(const key of Object.keys(run.phase_ns))run.phase_ns[key]=quantiles(run.phase_ns[key]);
  run.commit_remaining_ns=quantiles((run.commit_remaining_ns||[]).filter(v=>v!==null));
 }
 report.runs.push(run);
}
for(const cell of Object.values(report.cells)){cell.external_call_ns=quantiles(cell.external_call_ns);cell.parked_call_ns=quantiles(cell.parked_call_ns);cell.commit_remaining_ns=quantiles(cell.commit_remaining_ns);for(const p of Object.keys(cell.phase_ns))cell.phase_ns[p]=quantiles(cell.phase_ns[p])}
for(const cpu of [8,4])for(const pair of [1,2,3,4,5]){
 const off=json('matrix'+cpu+'-p'+pair+'-off','calls-'),on=json('matrix'+cpu+'-p'+pair+'-on','calls-');
 const key=c=>c.test+' '+c.expected_error,by=new Map(off.map(c=>[key(c),c]));
 const diffs=on.filter(c=>!c.expected_error).map(c=>{const other=by.get(key(c));if(!other)throw Error('pair lacks same call '+key(c));return c.duration_ns-other.duration_ns});
 report.overhead.push({cpu,pair,matched_semantic_calls:diffs.length,on_minus_off_ns:quantiles(diffs)});
}
report.denominators={processes:report.runs.length,leaf_outcomes:report.runs.reduce((n,r)=>n+r.leaves.passed,0),natural_unexpected_failures:report.runs.reduce((n,r)=>n+r.unexpected_errors.length,0),completed_observer_processes:report.runs.filter(r=>r.trace).length};
fs.writeFileSync(path.join(base,'matrix-analysis.json'),JSON.stringify(report,null,2)+'\n');
console.log(JSON.stringify({denominators:report.denominators,cells:Object.fromEntries(Object.entries(report.cells).map(([k,v])=>[k,{processes:v.processes,passed_leaves:v.passed_leaves,external_call_ns:v.external_call_ns,parked_call_ns:v.parked_call_ns,commit_remaining_ns:v.commit_remaining_ns,censored:v.right_censored.length,late:v.late.length}])),overhead:report.overhead},null,2));
