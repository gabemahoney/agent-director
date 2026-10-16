import fs from 'node:fs';
import crypto from 'node:crypto';
import path from 'node:path';
import {spawnSync} from 'node:child_process';

const base='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/';
const out=base+'native-lazy-validation/';
const hash=p=>crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex');
const info=p=>({sha256:hash(p),mode:(fs.statSync(p).mode&0o777).toString(8)});
function manifest(){
  const files=['go.mod','go.sum'];
  function walk(p){for(const e of fs.readdirSync(p,{withFileTypes:true})){const f=path.join(p,e.name);if(e.isDirectory())walk(f);else if(e.isFile()&&f.endsWith('.go'))files.push(f);}}
  for(const p of ['internal','pkg/api','cmd'])walk(p);
  return Object.fromEntries(files.sort().map(p=>[p,info(p)]));
}
const mode=process.argv[2];
if(!mode || !['green','red-n1b','vet'].some(prefix=>mode.startsWith(prefix)) && mode!=='red-n2')throw Error('unknown validation mode');
const selector='TestProductionLazyReplacement|TestProductionFreshLazyBorrower|TestProductionConnectorExplicitCallerContexts|TestProductionLazyInitializationFailure|TestCursorPromptLateCount';
const args=['test','-race','-count=1','-timeout','60s'];
const replacements={};
const verified=[];
if(mode.startsWith('red-n1b')||mode==='red-n2'){
  const folder=mode.startsWith('red-n1b')?'native-multiwaiter-fix':'native-review-fix';
  const report=JSON.parse(fs.readFileSync(base+folder+'/FINAL-report.json'));
  for(const f of report.source_files){
    const actual=info(f.before_path);
    if(actual.sha256!==f.before.sha256||actual.mode!==f.before.mode)throw Error('before provenance mismatch '+f.before_path);
    replacements[path.resolve(f.path)]=path.resolve(f.before_path);
    verified.push({path:f.path,before_path:f.before_path,...actual});
  }
  if(mode==='red-n2'){
    // Only the N1 tests need the outer connector type absent in the original N2 baseline.
    const empty=out+'excluded-n1-tests.txt';fs.writeFileSync(empty,'package store\n');
    for(const p of ['internal/store/sqlite_lazy_native_test.go','internal/store/sqlite_lazy_context_test.go'])replacements[path.resolve(p)]=path.resolve(empty);
  }
  const overlay=out+mode+'-overlay.json';fs.writeFileSync(overlay,JSON.stringify({Replace:replacements},null,2));
  args.push('-overlay',overlay);
}
if(mode.startsWith('vet')){args.splice(0,args.length,'vet','./internal/store');}
else args.push('./internal/store','-run',mode.startsWith('red-n1b')?'TestProductionLazyReplacementMultipleLegitimateWaiters':mode==='red-n2'?'TestCursorPromptLateCount':selector,'-v');
const before=manifest();fs.writeFileSync(out+mode+'-inputs-before.json',JSON.stringify(before,null,2));
const taskHome=fs.mkdtempSync('/tmp/native-lazy-home-');
const env={...process.env,HOME:taskHome,GOMODCACHE:'/go/pkg/mod',GOCACHE:'/home/sandbox/.cache/go-build',XDG_CONFIG_HOME:taskHome+'/config',TMUX_TMPDIR:taskHome+'/sockets'};
fs.mkdirSync(env.TMUX_TMPDIR,{recursive:true});
const result=spawnSync('go',args,{env,encoding:'utf8',maxBuffer:16*1024*1024,timeout:90000});
fs.writeFileSync(out+mode+'.stdout',result.stdout??'');fs.writeFileSync(out+mode+'.stderr',result.stderr??'');fs.writeFileSync(out+mode+'.exit',String(result.status)+'\n');
const after=manifest();fs.writeFileSync(out+mode+'-inputs-after.json',JSON.stringify(after,null,2));
const receipt={command:['go',...args],status:result.status,signal:result.signal,error:result.error?.message,verified_before:verified,replacements,environment:{HOME:taskHome,GOMODCACHE:env.GOMODCACHE,GOCACHE:env.GOCACHE,sandbox:env.AGENT_DIRECTOR_TEST_SANDBOX},changed_inputs:Object.keys(before).filter(p=>JSON.stringify(before[p])!==JSON.stringify(after[p])),stdout:info(out+mode+'.stdout'),stderr:info(out+mode+'.stderr')};
fs.writeFileSync(out+mode+'-receipt.json',JSON.stringify(receipt,null,2));
process.stdout.write(result.stdout??'');process.stderr.write(result.stderr??'');console.log(JSON.stringify(receipt,null,2));
process.exitCode=result.status??1;
