// Event-driven fixture wait. Observe before checking, so an early event is not lost.
const fs = require('node:fs'), cp = require('node:child_process'), path = require('node:path');
const [kind, target, query] = process.argv.slice(2);
const watch = fs.watch(path.dirname(target), check);
const timer = setTimeout(()=>{console.error('fixture wait timed out: '+target);process.exit(1);},15000);
function check() {
  let result='';
  if (kind==='file') { if(fs.existsSync(target)) result='ready'; }
  else if(kind==='sql') {
    const p=cp.spawnSync('/opt/driver/sql.sh',['-readonly',target,query],{encoding:'utf8'});
    if(p.status===0) result=p.stdout.trim();
  } else throw Error('unknown wait');
  if(result){watch.close();clearTimeout(timer);console.log(result);process.exit(0);}
}
check();
