from pathlib import Path
import struct,json,collections
out=Path('tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/trail-hypothesis-measure')
summary={}
for pop in ['baseline','load']:
 records=[]
 for file in (out/pop).glob('trace-*.bin'):
  data=file.read_bytes();epoch,pid=struct.unpack_from('<QQ',data)
  for pos in range(4096,len(data),320):
   h=struct.unpack_from('<7Q',data,pos)
   if not h[0]:continue
   r=dict(pid=pid,id=h[1],kind=h[2],deadline=h[3],queue=h[5],reason=h[6],times=list(struct.unpack_from('<12q',data,pos+56)),event=data[pos+152:pos+216].split(b'\0')[0].decode(),token=data[pos+216:pos+312].split(b'\0')[0].decode())
   records.append(r)
 (out/pop/'records.jsonl').write_text(''.join(json.dumps(r)+'\n' for r in records))
 metrics=collections.defaultdict(list);reasons=collections.Counter();joined=[]
 workers={(r['pid'],r['id']):r for r in records if r['kind']==2}
 for r in records:
  t=r['times'];k=r['kind']
  if k==1:
   reasons[str(r['reason'])]+=1
   if not t[1]:continue
   metrics['caller_total_ms'].append((t[2]-t[0])/1e6);metrics['enqueue_ms'].append((t[1]-t[0])/1e6);metrics['caller_select_ms'].append((t[2]-t[1])/1e6)
   w=workers.get((r['pid'],r['id']))
   if w:
    z=w['times'];row=dict(pid=r['pid'],id=r['id'],event=r['event'],token=r['token'],reason=r['reason'],deadline=r['deadline'],queue=r['queue'],caller_total_ms=(t[2]-t[0])/1e6,queue_ms=(z[0]-t[1])/1e6,mutex_ms=(z[1]-z[0])/1e6,layout_ms=(z[2]-z[1])/1e6,encode_ms=(z[3]-z[2])/1e6,open_ms=(z[4]-z[3])/1e6,write_ms=(z[5]-z[4])/1e6,sync_ms=(z[6]-z[5])/1e6,worker_total_ms=(z[8]-z[0])/1e6,caller_end=t[2],worker_end=z[8],remaining_at_caller_end_ms=(r['deadline']-t[2])/1e6)
    joined.append(row)
    for key in ['queue_ms','mutex_ms','layout_ms','encode_ms','open_ms','write_ms','sync_ms','worker_total_ms']:metrics['bounded_'+key].append(row[key])
  if k==2:
   for name,a,b in [('mutex',0,1),('layout',1,2),('encode',2,3),('open',3,4),('write',4,5),('sync',5,6),('total',0,8)]:
    if t[b]:metrics['allworker_'+name+'_ms'].append((t[b]-t[a])/1e6)
  if k==3:metrics['native_COMMIT_ms'].append((t[1]-t[0])/1e6)
 def stats(vals):
  v=sorted(vals);return dict(n=len(v),p50=v[int((len(v)-1)*.5)],p95=v[int((len(v)-1)*.95)],p99=v[int((len(v)-1)*.99)],max=v[-1])
 outcomes=collections.Counter();leaves=collections.Counter();fails=[]
 for f in (out/pop).glob('*.jsonl'):
  if f.name=='records.jsonl':continue
  for line in f.read_text().splitlines():
   try:r=json.loads(line)
   except:continue
   if r.get('Action') in ['pass','fail']:
    if r.get('Test'):
     if '/' in r['Test']:leaves[r['Action']]+=1
    else:outcomes[r['Action']]+=1
   if r.get('Action')=='fail':fails.append(r)
 summary[pop]=dict(records=len(records),processes=len(list((out/pop).glob('trace*.bin'))),kinds=dict(collections.Counter(r['kind'] for r in records)),caller_reasons=dict(reasons),metrics={k:stats(v) for k,v in metrics.items()},package_outcomes=dict(outcomes),leaf_outcomes=dict(leaves),failures=fails,top_bounded=sorted(joined,key=lambda x:x['caller_total_ms'],reverse=True)[:20],over900ms=[j for j in joined if j['caller_total_ms']>=900])
 (out/pop/'joined.jsonl').write_text(''.join(json.dumps(r)+'\n' for r in joined))
(out/'measurement-summary.json').write_text(json.dumps(summary,indent=2)+'\n')
for pop,d in summary.items():
 print(pop,'processes',d['processes'],'outcomes',d['package_outcomes'],'leaves',d['leaf_outcomes'],'kinds',d['kinds'],'reasons',d['caller_reasons'])
 for k,v in d['metrics'].items():print(k,{a:round(b,3) if isinstance(b,float) else b for a,b in v.items()})
 print('top',d['top_bounded'][:3])
