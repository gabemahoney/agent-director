// Syntax inspection only: never evaluates a case body or scenario generator.
const fs=require('fs'), cp=require('child_process'), vm=require('vm');
const root='tickets/testplans/b.jf1/t1.jf1.cr';let count=0;
for(const d of fs.readdirSync(root)) {
 const p=`${root}/${d}/${d}.md`;if(!fs.existsSync(p))continue;
 for(const m of fs.readFileSync(p,'utf8').matchAll(/```bash\n([\s\S]*?)```/g)) {
  const r=cp.spawnSync('bash',['-n'],{input:m[1],encoding:'utf8'});
  if(r.status!==0)throw Error(p+': '+r.stderr);count++;
 }
}
const shared=fs.readFileSync('test/driver/cursor-owned-launch.sh','utf8');
const scenario=shared.split("<<'SCENARIO'\n")[1].split('\nSCENARIO')[0];
new vm.Script(scenario);
console.log(`Syntax passed: ${count} collector blocks and embedded scenario JavaScript; no collector execution`);
