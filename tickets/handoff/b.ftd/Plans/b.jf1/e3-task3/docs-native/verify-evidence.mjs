import fs from 'node:fs';
import crypto from 'node:crypto';
import {execFileSync} from 'node:child_process';
const dir='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/docs-native';
const read=p=>fs.readFileSync(p,'utf8');
const json=p=>JSON.parse(read(p));
const hash=s=>crypto.createHash('sha256').update(s).digest('hex');
const assert=(v,m)=>{if(!v)throw Error(m);};
const save=(p,v)=>fs.writeFileSync(`${dir}/${p}`,JSON.stringify(v,null,2)+'\n');
const meta=s=>({sha256:hash(s),bytes:Buffer.byteLength(s),lines:s.split('\n').length-1});
function patch(input,text,reverse=false){
 const lines=input.slice(0,-1).split('\n'),hunks=[];let h;
 for(const l of text.split('\n')){
  const m=l.match(/^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/);
  if(m){h={a:+m[1],ac:+(m[2]??1),b:+m[3],bc:+(m[4]??1),old:[],next:[]};hunks.push(h);continue;}
  if(!h)continue;
  if(l.startsWith(' ')){h.old.push(l.slice(1));h.next.push(l.slice(1));}
  else if(l.startsWith('-'))h.old.push(l.slice(1));
  else if(l.startsWith('+'))h.next.push(l.slice(1));
 }
 assert(hunks.length,'empty patch');
 for(const h of hunks.reverse()){
  assert(h.old.length===h.ac&&h.next.length===h.bc,'hunk counts');
  const count=reverse?h.bc:h.ac,start=(reverse?h.b:h.a)-(count?1:0),from=reverse?h.next:h.old,to=reverse?h.old:h.next;
  assert(JSON.stringify(lines.slice(start,start+count))===JSON.stringify(from),'complete hunk/context mismatch');
  lines.splice(start,count,...to);
 }
 return lines.join('\n')+'\n';
}
const inherited=json('tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/docs-no-match/FINAL-report.json').whole_architecture_review;
const finalPath='docs/architecture.md';
const phase=process.argv[2];
if(phase==='before'){
 assert(hash(read(finalPath))===inherited.final.sha256,'baseline architecture drift');
 fs.writeFileSync(`${dir}/architecture-baseline.patch`,execFileSync('git',['diff','--',finalPath]));
 const paths=execFileSync('git',['ls-files','--cached','--others','--exclude-standard'],{encoding:'utf8'}).trim().split('\n').filter(p=>!p.split('/').includes('.claude')&&!p.startsWith('tickets/')&&(p.endsWith('.go')||p.endsWith('.ts')||p.endsWith('.sh')||['go.mod','go.sum','bun.lock','test/docker-epics.txt'].includes(p)));
 save('source-test-before.json',Object.fromEntries(paths.filter(p=>fs.existsSync(p)).map(p=>[p,{sha256:hash(fs.readFileSync(p)),mode:fs.statSync(p).mode&511}])));
 save('document-before.json',Object.fromEntries(['docs/architecture.md','docs/caller-contract.md','docs/permissions.md','pkg/api/README.md','pkg/ts-bun-client/README.md'].map(p=>[p,meta(read(p))])));
}
let base=patch(execFileSync('git',['show','HEAD:docs/architecture.md'],{encoding:'utf8',maxBuffer:4e6}),read(`${dir}/architecture-baseline.patch`));
assert(hash(base)===inherited.final.sha256,'baseline endpoint');
let endpoint=base;
for(const c of [...inherited.chain].reverse()){
 assert(hash(endpoint)===c.to,'chain target '+c.patch);
 assert(hash(read(c.patch))===c.patch_sha256,'patch hash '+c.patch);
 endpoint=patch(endpoint,read(c.patch),true);
 assert(hash(endpoint)===c.from,'chain source '+c.patch);
}
for(const [key,value] of Object.entries(meta(endpoint)))assert(inherited.original_base[key]===value,'whole original base '+key);
let last=0;
for(const p of inherited.partition_reports){assert(hash(read(p.path))===p.sha256,'partition hash');assert(p.lines[0]===last+1,'partition gap');last=p.lines[1];}
assert(last===inherited.original_base.lines,'partition coverage');
for(const p of [inherited.prior_report,inherited.prior_provenance,inherited.prior_document_hashes])assert(hash(read(p.path))===p.sha256,'inherited reference');
const inputs=json(`${dir}/source-test-before.json`);
for(const [p,v] of Object.entries(inputs)){assert(hash(fs.readFileSync(p))===v.sha256,'source/test drift '+p);assert((fs.statSync(p).mode&511)===v.mode,'mode drift '+p);}
assert(inputs['pkg/api/decide_trail_deadline_test.go'].sha256==='032950e86d9919e2c48ae79189751e36f8875312a78bf1410973954fdde86d88','trail regression');
assert(inputs['internal/store/permission.go'].sha256==='af61fc61e2b1b4c7e73eabac6ef2710a12d2019be45b3ab4c4920080badaa194','permission');
assert(inputs['internal/store/sqlite_connector.go'].sha256==='850688eeb67b2bd9f8d71c3324c735e14c94fe63b1a48afad266b285bee36c79','connector');
if(phase==='after'){
 // Build only changed-line hunks in memory; never archive a whole architecture copy.
 const old=base.slice(0,-1).split('\n'),next=read(finalPath).slice(0,-1).split('\n');let i=0,j=0;const edits=[],out=['--- a/docs/architecture.md','+++ b/docs/architecture.md'];
 while(i<old.length||j<next.length){
  if(old[i]===next[j]){i++;j++;continue;}
  let found;const oldTriples=new Map();
  for(let x=i;x<Math.min(old.length-2,i+600);x++)oldTriples.set(old.slice(x,x+3).join('\n'),x);
  for(let y=j;y<Math.min(next.length-2,j+600);y++){
   const x=oldTriples.get(next.slice(y,y+3).join('\n'));if(x!==undefined&&(!found||x-i+y-j<found.cost))found={x,y,cost:x-i+y-j};
  }
  assert(found,'diff synchronization');const a=old.slice(i,found.x),b=next.slice(j,found.y);
  const previous=edits.at(-1);
  if(previous&&i-previous.x<=6&&j-previous.y<=6){previous.x=found.x;previous.y=found.y;}
  else edits.push({i,j,x:found.x,y:found.y});
  i=found.x;j=found.y;
 }
 for(const e of edits){
  const prefix=Math.min(3,e.i,e.j),suffix=Math.min(3,old.length-e.x,next.length-e.y);
  const a=old.slice(e.i,e.x),b=next.slice(e.j,e.y);
  out.push(`@@ -${e.i-prefix+1},${a.length+prefix+suffix} +${e.j-prefix+1},${b.length+prefix+suffix} @@`,...old.slice(e.i-prefix,e.i).map(l=>' '+l),...a.map(l=>'-'+l),...b.map(l=>'+'+l),...old.slice(e.x,e.x+suffix).map(l=>' '+l));
 }
 const delta=out.join('\n')+'\n';fs.writeFileSync(`${dir}/architecture-delta.patch`,delta);
 assert(patch(base,delta)===read(finalPath),'new forward complete endpoint');assert(patch(read(finalPath),delta,true)===base,'new reverse complete endpoint');
 const provenance={...inherited,method:'Inherited four-partition whole-base review plus independently reverified all six prior complete deltas/endpoints and personally reviewed every new native-batch hunk/context. Aggregate coverage, not a fresh personal whole-file read.',final:meta(read(finalPath)),chain:[...inherited.chain,{from:hash(base),to:hash(read(finalPath)),patch:`${dir}/architecture-delta.patch`,patch_sha256:hash(delta),independently_reverified_complete_endpoints_and_all_hunks:true,review:'Every new batch hunk and surrounding context personally reviewed against final native ownership, lazy connector, publication and CLI source.'}],personal_whole_file_read:false,clean_document_claim:false};
 save('architecture-review-provenance.json',provenance);
 save('source-test-after.json',inputs);
}
save(`${phase}-verification.json`,{result:'PASS',architecture:meta(read(finalPath)),inherited_steps:inherited.chain.length,partitions:inherited.partition_reports,all_complete_endpoints_hunks_and_partition_hashes_verified:true,source_test_inputs_unchanged:Object.keys(inputs).length,protected_trail_permission_connector_unchanged:true});
console.log(JSON.stringify({result:'PASS',phase,inherited_steps:inherited.chain.length,source_test_inputs_unchanged:Object.keys(inputs).length,architecture:meta(read(finalPath))}));
