// Static syntax validation only: never sources a collector case.
const fs=require('fs'),cp=require('child_process');
const root='tickets/testplans/b.jf1/t1.jf1.cr';let checked=0;
for(const name of fs.readdirSync(root)){const p=root+'/'+name+'/'+name+'.md';if(!fs.existsSync(p))continue;const body=fs.readFileSync(p,'utf8');for(const m of body.matchAll(/```bash\n([\s\S]*?)```/g)){const r=cp.spawnSync('bash',['-n'],{input:m[1],encoding:'utf8'});if(r.status!==0){process.stderr.write(p+' '+r.stderr);process.exit(1)}checked++;}}
if(checked!==10)throw new Error('expected ten cases, got '+checked);console.log('bash syntax: '+checked+' collector blocks passed; no case executed');
