from pathlib import Path
import json,collections,hashlib,subprocess
out=Path('tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/trail-hypothesis-measure');summary=json.loads((out/'measurement-summary.json').read_text())
for pop in ['baseline','load']:
 rs=[json.loads(l) for l in (out/pop/'records.jsonl').read_text().splitlines()];ws={(r['pid'],r['id']):r for r in rs if r['kind']==2};cs=[r for r in rs if r['kind']==1];phases=collections.Counter();n=0;cuts=[];commits=collections.defaultdict(list);pubs=collections.defaultdict(list)
 for r in rs:
  if r['kind']==3:commits[r['pid'],r['deadline']].append(r)
  if r['kind']==4:pubs[r['pid'],r['deadline']].append(r)
 correlations=[]
 for c in cs:
  w=ws.get((c['pid'],c['id']));ct=c['times'];cut=ct[2];dur=cut-ct[0]
  relevant=[r for r in commits[c['pid'],c['deadline']] if r['times'][1]<=ct[0]]
  if relevant:
   cm=max(relevant,key=lambda r:r['times'][1]);ps=[r for r in pubs[c['pid'],c['deadline']] if cm['times'][1]<=r['times'][0]<=ct[0]]
   correlations.append(dict(pid=c['pid'],id=c['id'],native_commit_ms=(cm['times'][1]-cm['times'][0])/1e6,commit_end=cm['times'][1],publication=[r['times'][0] for r in ps],emit_start=ct[0],deadline=c['deadline'],caller_end=cut))
  if not w:continue
  wt=w['times'];bounds=[ct[0],ct[1],wt[0],wt[1],wt[2],wt[3],wt[4],wt[5],wt[6],wt[8],cut]
  names=['enqueue','queue','mutex','layout','encode','open','write','sync','worker_final','completion_or_scheduling']
  row=dict(pid=c['pid'],id=c['id'],caller_ms=dur/1e6,reason=c['reason'],phase_at_cutoff='completed' if wt[8]<=cut else 'unknown',overlap_ms={})
  for name,a,b in zip(names,bounds,bounds[1:]):
   overlap=max(0,min(cut,b)-max(ct[0],a))/1e6
   row['overlap_ms'][name]=overlap
   if a<=cut<b:row['phase_at_cutoff']=name
   phases[name]+=overlap
  cuts.append(row);n+=1
 totals=sum(phases.values());summary[pop]['cutoff_analysis']=dict(joined_n=n,censored_n=len(cs)-n,phase_at_cutoff=dict(collections.Counter(r['phase_at_cutoff'] for r in cuts)),aggregate_ms=dict(phases),aggregate_percent={k:v/totals*100 for k,v in phases.items()},over900ms_joined_n=sum(r['caller_ms']>=900 for r in cuts),over900ms_phase_at_cutoff=dict(collections.Counter(r['phase_at_cutoff'] for r in cuts if r['caller_ms']>=900)))
 summary[pop]['commit_publication_correlation']=dict(native_commit_before_emit_n=len(correlations),publication_between_commit_and_emit_n=sum(bool(r['publication']) for r in correlations),bounded_emit_n=len(cs))
 (out/pop/'cutoff-partitions.json').write_text(json.dumps(cuts,indent=2)+'\n');(out/pop/'correlations.json').write_text(json.dumps(correlations,indent=2)+'\n')
(out/'measurement-summary.json').write_text(json.dumps(summary,indent=2)+'\n')
for p,d in summary.items():print(p,json.dumps(d['cutoff_analysis'],indent=2),d['commit_publication_correlation'])
# Provenance: tests are unmodified from HEAD, instrumentation retained separately.
snap=out/'_snapshot/head';manifest={};bad=[]
for f in snap.rglob('*_test.go'):
 rel=f.relative_to(snap).as_posix(); orig=subprocess.check_output(['git','show','HEAD:'+rel]);data=f.read_bytes()
 manifest[rel]=hashlib.sha256(data).hexdigest()
 if orig!=data:bad.append(rel)
(out/'tests-provenance.json').write_text(json.dumps(dict(test_files=len(manifest),changed=bad,sha256=manifest),indent=2)+'\n')
print('test files',len(manifest),'changed',bad)
