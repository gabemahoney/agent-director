import fs from 'node:fs';
import path from 'node:path';
const base='/work/tickets/handoff/b.ftd/Plans/b.jf1/e4-task1/diagnostic/pass-03';
const safe=p=>{if(!p||p.split(/[\\/]/).includes('.claude'))throw Error('forbidden component');return p};
const read=p=>JSON.parse(fs.readFileSync(safe(p),'utf8'));
for(const name of process.argv.slice(2)){
 if(!/^[a-zA-Z0-9_-]+$/.test(name))throw Error('explicit safe run name required');
 const dir=base+'/'+name,names=fs.readdirSync(dir);
 const pfiles=names.filter(n=>/^process-[a-f0-9]{32}\.json$/.test(n));
 if(pfiles.length!==1)throw Error('one process inventory required');
 const p=read(dir+'/'+pfiles[0]),quality=read(dir+'/trace-quality.json'),supervisor=read(dir+'/supervisor-result.json');
 if(quality.result!=='PASS'||supervisor.result!=='COMMAND_PASSED'||!supervisor.coverage_complete)throw Error('trace/supervisor invalid '+name);
 if(p.calls.length!==2||p.calls.some(c=>c.route!=='client.list'||c.state!=='completed'||c.error))throw Error('original List two-call success unavailable '+name);
 const last=p.snapshots.find(s=>s.label==='final');
 const events=fs.readFileSync(safe(dir+'/'+last.file),'utf8').trim().split('\n').map(JSON.parse).filter(e=>e.kind==='event');
 const readbacks=names.filter(n=>n.startsWith('readback-')&&n.endsWith('.json')).map(n=>({file:n,...read(dir+'/'+n)}));
 const calls=p.calls.map((c,index)=>{
  const join=quality.source_root_joins.find(j=>j.process===c.process&&j.test===c.test&&j.fixture===c.fixture&&j.call===c.call);
  if(!join)throw Error('missing exact source-root join');
  const es=events.filter(e=>e.root===join.source_root),actual=es.find(e=>e.phase==='client.list.return_after_defers');
  const rbs=readbacks.filter(r=>r.process===c.process&&r.test===c.test&&r.fixture===c.fixture&&r.call===c.call&&r.path===c.path);
  if(!rbs.length)throw Error('missing explicit invocation readback');
  for(const rb of rbs){
   if(rb.cutoff_ns<c.return_ns)throw Error('readback preceded actual external return');
   if(p.calls[index+1]&&rb.completed_ns>=p.calls[index+1].entry_ns)throw Error('readback was not complete before next original loop call');
  }
  const phase=phase=>es.filter(e=>e.phase===phase);
  const facts=rbs.map(rb=>({file:rb.file,label:rb.label,coherent:rb.coherent,availability:rb.availability,error:rb.error||null,cutoff_ns:rb.cutoff_ns,completed_ns:rb.completed_ns,requests:(rb.permission_requests||[]).map(r=>Object.fromEntries(['instance_id','request_token','decision','attempted_decision','delivered_at','closed_at','proven_gone_at','proven_gone_how'].map(k=>[k,r[k]??null]))),spawns:(rb.spawns||[]).map(s=>Object.fromEntries(['instance_id','state','row_version','launch_token','life_number'].map(k=>[k,s[k]??null]))),cursor_runtimes:rb.cursor_runtimes||null,subagents:rb.subagents||null,subagent_launches:rb.subagent_launches||null}));
  return {...join,external_elapsed_ns:c.duration_ns,actual_source_elapsed_ns:join.actual_source_return_ns-join.actual_source_entry_ns,actual_error:actual?.errors||[],terminal_context:phase('terminal_mark.operation_context.created').map(e=>({ns:e.ns,deadline_ns:e.deadline_ns,remaining_ns:e.remaining_ns,context_state:e.context_state,original_budget_ns:1000000000,budget_authority:'unchanged operationContext(0), exact source pin; remaining is sampled after creation'})),terminal_returns:phase('terminal_mark.return_after_defers'),commit_driver_entries:phase('commit.exec.enter'),commit_driver_returns:phase('commit.exec.return'),sql_publications:phase('sql.published_locked'),boundary_timeline:es.filter(e=>/worker|guard|cleanup|gate|commit|terminal_mark|trail\./.test(e.phase)),readbacks:facts,late_commit_returns:phase('commit.exec.return').filter(e=>e.ns>join.actual_source_return_ns).length,notes:phase('terminal_mark.enter').length?[]:['No terminal transaction observed on this original loop call; no synthetic transaction or zero-filled phase durations inferred.']};
 });
 const report={result:'PASS',name,process:p.process,mode:p.mode,race:name.includes('race'),original_leaf_outcomes:1,actual_calls_started:2,actual_calls_completed:2,unfinished:0,source_root_joins:2,calls,resources:supervisor,limitations:['Only one original leaf/two correlated loop calls in this process.','8GiB tmpfs temporary storage differs from earlier canonical full-context storage.','Natural absence of failure does not establish historical cause or a fix.'],historical_cause:'UNKNOWN'};
 fs.writeFileSync(safe(dir+'/list-analysis.json'),JSON.stringify(report,null,2)+'\n',{flag:'wx'});
 console.log(JSON.stringify({name,result:report.result,calls:calls.map(c=>({call:c.call,source_root:c.source_root,external_ms:c.external_elapsed_ns/1e6,source_ms:c.actual_source_elapsed_ns/1e6,terminal_contexts:c.terminal_context.length,commit_driver_returns:c.commit_driver_returns.length,sql_publications:c.sql_publications.length,readbacks:c.readbacks.map(r=>({coherent:r.coherent,availability:r.availability}))}))}));
}
