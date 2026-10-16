import fs from 'node:fs';
import path from 'node:path';
const base=process.argv[5]||'tickets/handoff/b.ftd/Plans/b.jf1/e4-task1/diagnostic/pass-02';
if(base.split(/[\\/]/).includes('.claude'))throw Error('forbidden path component');
const fail=m=>{throw Error(m)};
function load(dir,file){return fs.readFileSync(path.join(base,dir,file),'utf8').trim().split('\n').map(JSON.parse)}
function trace(dir){const names=fs.readdirSync(path.join(base,dir));let file;if(names.includes('trace.jsonl'))file='trace.jsonl';else{const inventories=names.filter(n=>/^process-[a-f0-9]{32}\.json$/.test(n)).map(n=>JSON.parse(fs.readFileSync(path.join(base,dir,n),'utf8')));if(inventories.length!==1)fail('one explicit calibration process required');const finals=inventories[0].snapshots.filter(s=>s.label==='final'&&s.state==='completed');if(finals.length!==1)fail('one final recorder required');file=finals[0].file}const rows=load(dir,file);if(rows[0].overflow||rows.at(-1).kind!=='snapshot_end'||rows.at(-1).unpublished_reservations)fail('incomplete trace '+dir);return rows.filter(e=>e.kind==='event').sort((a,b)=>a.ns-b.ns)}
const store=trace(process.argv[2]||'store-calibration'),api=trace(process.argv[3]||'api-calibration');
const result={kind:'existing-fault-calibration',source_gaps:[],checks:{},censoring:{},historical_cause:'UNKNOWN'};
for(const es of [store,api]){
 const pool=new Map();
 for(const e of es){
  if(e.phase==='pool.acquire.enter')pool.set(e.span,e);
  if(e.phase==='fixture.connector.enter'){const p=pool.get(e.span);if(!p||p.deadline_ns!==e.deadline_ns)fail('pool/connector context identity');result.checks.pool_actual_connector=(result.checks.pool_actual_connector||0)+1}
  if(['pool.acquire.enter','pool.acquire.return','gate.wait.enter','gate.acquired','gate.refused'].includes(e.phase)){
   if(!e.operation_context_recorded)fail('missing operation context');
   if(e.deadline_ns&&e.operation_deadline_ns&&e.deadline_ns>e.operation_deadline_ns)fail('boundary deadline after original');
  }
 }
 const pubs=es.filter(e=>e.phase==='sql.published_locked');
 for(const p of pubs){const c=es.find(e=>e.span===p.span&&e.phase==='cleanup.enter');if(c&&c.ns<p.ns)fail('cleanup preceded SQL publication');}
 result.checks.publication_before_cleanup=(result.checks.publication_before_cleanup||0)+pubs.length;
}
const short=store.filter(e=>e.phase==='gate.refused'&&e.operation_remaining_ns>500000000);
if(short.length!==8)fail('missing zero-wait gate refusal calibration: '+short.length);
for(const e of short){if(e.context_state!==2||e.operation_context_state!==0||e.operation_deadline_ns-e.deadline_ns<900000000)fail('incorrect admission context')}
result.checks.zero_wait_gate_actual_expired_original_live=short.length;
for(const branch of ['commit_exec','operation_guard']){
 const returned=store.find(e=>e.phase==='fixture.decision.return'&&e.errors?.some(c=>c.text.includes('branch='+branch)));
 if(!returned)fail('missing caller branch '+branch);
 const root=store.filter(e=>e.root===returned.root);
 const entry=root.find(e=>e.phase==='commit.exec.enter'),end=root.find(e=>e.phase==='commit.exec.return'),pub=root.find(e=>e.phase==='sql.published_locked');
 if(!entry||!end||!pub)fail('missing commit/publication '+branch);
 if(branch==='commit_exec'&&(!end.errors?.length||end.ns>pub.ns||pub.ns>returned.ns))fail('commit_exec sequence');
 if(branch==='operation_guard'){
  const guard=root.find(e=>e.phase==='guard.state_locked');
  if(!guard||(guard.value&3)!==2||returned.ns>=end.ns)fail('operation_guard sequence');
 }
 result.checks[branch]={root:returned.root,caller_return_ns:returned.ns,commit_return_ns:end.ns,sql_publication_ns:pub.ns,late_commit_return:end.ns>returned.ns};
}
result.checks.precommit_busy_without_commit=store.filter(e=>e.phase==='fixture.decision.return'&&e.errors?.some(c=>c.text==='ErrStoreBusy')&&!store.some(c=>c.root===e.root&&c.phase==='commit.exec.enter')).length;
if(!result.checks.precommit_busy_without_commit)fail('missing precommit refusal');
result.checks.late_cleanup=store.filter(e=>e.phase==='cleanup.return'&&store.some(c=>c.root===e.root&&c.phase==='fixture.decision.return'&&c.ns<e.ns)).length;
result.checks.actual_disposal=store.filter(e=>e.phase==='cleanup.discard.enter').length;
for(const route of ['helper','client']){
 const returns=api.filter(e=>e.phase===route+'.return_after_defers');
 const held=returns.find(e=>api.some(c=>c.root===e.root&&c.phase==='trail.mutex.acquired'&&c.ns>e.ns));
 if(!held)fail('held real trail late worker not observed '+route);
 const pub=api.find(e=>e.root===held.root&&e.phase==='sql.published_locked');
 if(!pub||pub.ns>=held.ns)fail('missing confirmed SQL before held reporting '+route);
 result.checks['held_trail_'+route]={root:held.root,sql_publication_ns:pub.ns,actual_return_after_defers_ns:held.ns};
}
for(const [name,es] of [['store',store],['api',api]]){
 const phases={};for(const e of es)phases[e.phase]=(phases[e.phase]||0)+1;
 result.checks[name+'_events']=es.length;result.checks[name+'_phases']=phases;
 result.censoring[name]={overflow:0,unpublished:0,unmatched:es.filter(e=>e.phase.endsWith('.enter')&&!es.some(c=>c.span===e.span&&c.ns>=e.ns&&(c.phase===e.phase.replace(/\.enter$/,'.return')||c.phase===e.phase.replace(/\.enter$/,'.return_after_defers')))).map(e=>({root:e.root,span:e.span,phase:e.phase,ns:e.ns})),note:'Unmatched inventory includes state-entry markers with different terminal names; never zero-filled.'};
}
const out=path.join(base,process.argv[4]||'calibration-review.json');fs.writeFileSync(out,JSON.stringify(result,null,2)+'\n');console.log(JSON.stringify({checks:result.checks,historical_cause:result.historical_cause},null,2));
