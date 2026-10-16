const fs = require('fs'), cp = require('child_process'), crypto = require('crypto');
const base = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-integration-fixes';
const own = base + '/startup-final-full';
const phase = process.argv[2];
const allowed = p => p && !p.split('/').includes('.claude') && !p.startsWith('tickets/handoff/') && !p.split('/').some(n=>/^(_|archive|archived)/i.test(n)) && (!p.includes('/') || ['cmd','internal','pkg','test','skills','scripts','docs','tools','.github','tickets'].includes(p.split('/')[0]));
const git = (...args) => cp.execFileSync('git', args, {encoding:'utf8'});
const json = (p,v) => fs.writeFileSync(own+'/'+p,JSON.stringify(v,null,2)+'\n',{mode:0o600});
const hash = p => {
  if (!p || p.split('/').includes('.claude') || p.split('/').some(n=>/^(_|archive|archived)/i.test(n))) throw Error('forbidden path name');
  const fd=fs.openSync(p,fs.constants.O_RDONLY|fs.constants.O_NOFOLLOW);
  try {return crypto.createHash('sha256').update(fs.readFileSync(fd)).digest('hex');} finally {fs.closeSync(fd);}
};
const tracked = new Set(git('ls-files','-z').split('\0').filter(allowed));
const paths = [...new Set(git('ls-files','-z','--cached','--others','--exclude-standard').split('\0').filter(allowed))].sort();
const manifest=paths.map(p=>{
  let st;try{st=fs.lstatSync(p);}catch(e){if(e.code==='ENOENT')return {path:p,missing:true,tracked:tracked.has(p)};throw e;}
  if(st.isSymbolicLink())return {path:p,type:'symlink',target_not_accessed:true,tracked:tracked.has(p)};
  if(!st.isFile())return {path:p,type:'nonregular',tracked:tracked.has(p)};
  return {path:p,sha256:hash(p),bytes:st.size,tracked:tracked.has(p)};
});
json('input-manifest-'+phase+'.json',manifest);
const refs=['red/frozen-sha256.json','list-red/frozen-sha256.txt','read-lock-red/frozen-sha256.json'];
const expected={...JSON.parse(fs.readFileSync(base+'/'+refs[0])),...JSON.parse(fs.readFileSync(base+'/'+refs[2]))};
for(const line of fs.readFileSync(base+'/'+refs[1],'utf8').trim().split('\n')){
 const m=line.match(/^([0-9a-f]{64})\s+(.+)$/);if(!m)throw Error('invalid frozen line');expected[m[2]]=m[1];
}
const newExpected=JSON.parse(fs.readFileSync(base+'/full-fixture-startup-red/new-red-frozen-sha256.json'));
const originalListExpected=expected['pkg/api/cursor_liveness_list_test.go'];
expected['pkg/api/cursor_liveness_list_test.go']=newExpected['pkg/api/cursor_liveness_list_test.go'];
const newFrozen=Object.entries(newExpected).map(([path,expected])=>({path,expected,actual:hash(path),matches:hash(path)===expected}));
json('new-red-frozen-'+phase+'.json',newFrozen);
if(newFrozen.length!==8||newFrozen.some(r=>!r.matches))throw Error('new RED mismatch');
const frozen=Object.entries(expected).map(([path,expected])=>({path,expected,actual:hash(path),matches:hash(path)===expected}));
json('frozen-'+phase+'.json',{references:refs.map(p=>base+'/'+p),files:frozen});
if(frozen.length!==11||frozen.some(r=>!r.matches))throw Error('frozen mismatch');
const sourceExpected=JSON.parse(fs.readFileSync(base+'/bounded-green/source-after.json'));
Object.assign(sourceExpected,JSON.parse(fs.readFileSync(base+'/startup-engineer/report.json')).source_hashes);
const source=Object.entries(sourceExpected).map(([path,expected])=>({path,expected,actual:hash(path),matches:hash(path)===expected}));
json('source-'+phase+'.json',{reference:base+'/bounded-green/source-after.json',files:source});
if(source.length!==11||source.some(r=>!r.matches))throw Error('source mismatch');
const trail='pkg/api/cursor_liveness_trail_deadline_test.go';
json('trail-'+phase+'.json',{path:trail,sha256:hash(trail)});
const required=['internal/cursorhost/session_e3_regression_linux_test.go','internal/store/cursor_runtime_liveness_test.go','pkg/api/cursor_liveness.go','pkg/api/cursor_liveness_test.go','pkg/api/cursor_liveness_list_test.go','pkg/api/cursor_liveness_lock_test.go',trail];
required.push('pkg/api/cursor_startup_liveness_test.go','internal/store/cursor_startup_liveness_test.go','test/realtmux/cursor_startup_liveness_linux_test.go','test/smoke/ts-helper/fake_pane_owner.go');
for(const p of required)if(!manifest.some(r=>r.path===p&&r.sha256&&!r.tracked))throw Error('untracked input absent '+p);
if(phase==='before'){
 const listing=cp.execFileSync('go',['list','-f','{{.ImportPath}}|{{.Dir}}|{{join .GoFiles ","}}|{{join .CgoFiles ","}}|{{join .TestGoFiles ","}}|{{join .XTestGoFiles ","}}','./...'],{encoding:'utf8'}).trim().split('\n');
 json('package-inventory.json',listing);
 const sources=listing.flatMap(line=>{const [,dir,...parts]=line.split('|');return parts.flatMap(part=>part.split(',').filter(Boolean).map(f=>dir.replace('/work/','')+'/'+f));}).sort();
 if(sources.some(p=>!allowed(p)))throw Error('forbidden compiled path');
 json('go-compiled-source-inventory.json',sources);
 json('go-files-not-in-compiled-inventory.json',manifest.filter(r=>r.path.endsWith('.go')&&!sources.includes(r.path)).map(r=>r.path));
 json('run.json',{command:['make','test-sandbox'],head:git('rev-parse','HEAD').trim(),prepared:new Date().toISOString(),canonical_input_count:manifest.length,compiled_go_source_count:sources.length,package_inventory_count:listing.length,required_untracked_inputs:required,environment:{container_HOME:process.env.HOME,container_TMPDIR:process.env.TMPDIR??'unset',umask:'022'},manifest_policy:'Git tracked plus standard untracked; .claude names filtered before any file access; symlink targets not accessed; own ignored evidence excluded',note:'One bare make test-sandbox, normal Go/Bun cache behavior, no CMD override/GOFLAGS/overlay/filter/retry/instrumentation'});
}else{
 const before=JSON.parse(fs.readFileSync(own+'/input-manifest-before.json'));
 const old=new Map(before.map(r=>[r.path,r]));
 const changes=manifest.filter(r=>JSON.stringify(old.get(r.path))!==JSON.stringify(r)).map(r=>r.path).concat(before.filter(r=>!manifest.some(a=>a.path===r.path)).map(r=>r.path));
 const digest=p=>crypto.createHash('sha256').update(fs.readFileSync(own+'/'+p)).digest('hex');
 const proof={unchanged:changes.length===0,changes,before_manifest_sha256:digest('input-manifest-before.json'),after_manifest_sha256:digest('input-manifest-after.json'),frozen_matches:11,new_red_matches:8,original_frozen_unchanged:10,authorized_list_exception:true,source_matches:11,trail_unchanged:fs.readFileSync(own+'/trail-before.json','utf8')===fs.readFileSync(own+'/trail-after.json','utf8')};
 json('input-comparison.json',proof);
 if(!proof.unchanged||!proof.trail_unchanged)throw Error('canonical inputs changed');
}
console.log(JSON.stringify({phase,canonical_inputs:manifest.length,frozen_matches:frozen.length,source_matches:source.length}));
