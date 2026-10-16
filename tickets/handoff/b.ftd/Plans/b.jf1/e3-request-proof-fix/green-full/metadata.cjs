const fs=require('fs'),cp=require('child_process'),crypto=require('crypto');
const base='tickets/handoff/b.ftd/Plans/b.jf1/e3-request-proof-fix',own=base+'/green-full',phase=process.argv[2];
const allowed=p=>p&&!p.split('/').includes('.claude')&&!p.startsWith('tickets/handoff/')&&!p.split('/').some(n=>/^(_|archive|archived)/i.test(n))&&(!p.includes('/')||['cmd','internal','pkg','test','skills','scripts','docs','tools','.github','tickets'].includes(p.split('/')[0]));
const git=(...args)=>cp.execFileSync('git',args,{encoding:'utf8'});
const json=(name,data)=>fs.writeFileSync(own+'/'+name,JSON.stringify(data,null,2)+'\n',{mode:0o600});
const hash=p=>{if(!p||p.split('/').includes('.claude'))throw Error('forbidden name');const fd=fs.openSync(p,fs.constants.O_RDONLY|fs.constants.O_NOFOLLOW);try{return crypto.createHash('sha256').update(fs.readFileSync(fd)).digest('hex')}finally{fs.closeSync(fd)}};
const red=JSON.parse(fs.readFileSync(base+'/red/report.json')),eng=JSON.parse(fs.readFileSync(base+'/engineer/report.json'));
const frozen={testsAndRunners:red.frozenTestsAndRunners,protectedFiles:eng.protectedFiles,controls:red.unchangedControlSHA256};
for(const [section,entries]of Object.entries(frozen)){const rows=Object.entries(entries).map(([path,expected])=>({path,expected,actual:hash(path),matches:hash(path)===expected}));json(section+'-'+phase+'.json',rows);if(rows.some(r=>!r.matches))throw Error('frozen mismatch '+section)}
if(Object.keys(frozen.testsAndRunners).length!==9||Object.keys(frozen.protectedFiles).length!==30)throw Error('wrong frozen count');
const tracked=new Set(git('ls-files','-z').split('\0').filter(allowed));
const paths=[...new Set(git('ls-files','-z','--cached','--others','--exclude-standard').split('\0').filter(allowed))].sort();
const manifest=paths.map(path=>{let st;try{st=fs.lstatSync(path)}catch(e){if(e.code==='ENOENT')return{path,missing:true,tracked:tracked.has(path)};throw e}if(st.isSymbolicLink())return{path,type:'symlink',target_not_accessed:true,tracked:tracked.has(path)};if(!st.isFile())return{path,type:'nonregular',tracked:tracked.has(path)};return{path,sha256:hash(path),bytes:st.size,tracked:tracked.has(path)}});
json('input-manifest-'+phase+'.json',manifest);
const source=manifest.filter(r=>r.path.endsWith('.go')&&!r.path.endsWith('_test.go'));
json('source-'+phase+'.json',source);
const changed=git('diff','--name-only','-z','HEAD').split('\0').filter(allowed);
json('changed-inputs-'+phase+'.json',manifest.filter(r=>changed.includes(r.path)||!r.tracked));
json('environment-'+phase+'.json',{HOME:process.env.HOME,TMPDIR:process.env.TMPDIR??'unset',umask:process.umask().toString(8),GOFLAGS:process.env.GOFLAGS??'unset',TMUX:process.env.TMUX??'unset',TMUX_PANE:process.env.TMUX_PANE??'unset',GIT_CONFIG_GLOBAL:process.env.GIT_CONFIG_GLOBAL,agent_director_variable_names:Object.keys(process.env).filter(n=>n.startsWith('AGENT_DIRECTOR_'))});
if(phase==='before'){
 const listing=cp.execFileSync('go',['list','-f','{{.ImportPath}}|{{.Dir}}|{{join .GoFiles ","}}|{{join .CgoFiles ","}}|{{join .TestGoFiles ","}}|{{join .XTestGoFiles ","}}','./...'],{encoding:'utf8'}).trim().split('\n');
 json('package-inventory.json',listing);
 const sources=listing.flatMap(line=>{const[,dir,...parts]=line.split('|');return parts.flatMap(part=>part.split(',').filter(Boolean).map(f=>dir.replace('/work/','')+'/'+f))}).sort();
 if(sources.some(p=>!allowed(p)))throw Error('forbidden compiled path');
 json('go-compiled-source-inventory.json',sources);
 json('go-files-not-in-compiled-inventory.json',manifest.filter(r=>r.path.endsWith('.go')&&!sources.includes(r.path)).map(r=>r.path));
 json('run.json',{command:'make test-sandbox',head:git('rev-parse','HEAD').trim(),branch:git('branch','--show-current').trim(),prepared:new Date().toISOString(),canonical_input_count:manifest.length,compiled_go_source_count:sources.length,package_inventory_count:listing.length,policy:'Tracked plus standard untracked canonical inputs; .claude excluded by name before stat/content/hash; evidence excluded; symlinks not followed; no old superseded proof manifests executed; bare full command with normal cache policy, no overrides/retries.'});
}
if(phase==='after'){
 const before=JSON.parse(fs.readFileSync(own+'/input-manifest-before.json'));const old=new Map(before.map(r=>[r.path,r]));
 const changes=manifest.filter(r=>JSON.stringify(old.get(r.path))!==JSON.stringify(r)).map(r=>r.path).concat(before.filter(r=>!manifest.some(a=>a.path===r.path)).map(r=>r.path));
 const comparison={unchanged:changes.length===0,changes,before_manifest_sha256:hash(own+'/input-manifest-before.json'),after_manifest_sha256:hash(own+'/input-manifest-after.json'),source_unchanged:JSON.stringify(source)===fs.readFileSync(own+'/source-before.json','utf8').trim().replace(/\n/g,'')};
 comparison.source_unchanged=JSON.stringify(source)===JSON.stringify(JSON.parse(fs.readFileSync(own+'/source-before.json')));
 json('input-comparison.json',comparison);if(!comparison.unchanged||!comparison.source_unchanged)throw Error('canonical inputs changed');
}
console.log(JSON.stringify({phase,canonical_inputs:manifest.length,productionGoFiles:source.length,frozenTestsAndRunners:9,protectedCollectorDriverFiles:30}));
