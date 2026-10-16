const fs = require('fs'), crypto = require('crypto');
const base = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-integration-fixes';
const out = base + '/full-fixture-startup-red';
const phase = process.argv[2];
const expected = {...JSON.parse(fs.readFileSync(base+'/red/frozen-sha256.json')), ...JSON.parse(fs.readFileSync(base+'/read-lock-red/frozen-sha256.json'))};
for (const line of fs.readFileSync(base+'/list-red/frozen-sha256.txt','utf8').trim().split('\n')) {
  const [,sum,path] = line.match(/^([0-9a-f]{64})\s+(.+)$/); expected[path] = sum;
}
const hash = paths => Object.fromEntries(paths.map(p => [p, crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex')]));
const frozen = hash(Object.keys(expected));
const source = hash(['internal/cursorhost/session.go','internal/store/cursor_runtime.go','internal/store/find_missing_writes.go','internal/store/lockwait.go','internal/store/request_close.go','internal/store/recovery.go','internal/store/permission.go','pkg/api/cursor_liveness.go','pkg/api/get.go','pkg/api/status.go','pkg/api/list.go']);
fs.writeFileSync(out+'/frozen-'+phase+'.json',JSON.stringify(frozen,null,2)+'\n');
fs.writeFileSync(out+'/source-'+phase+'.json',JSON.stringify(source,null,2)+'\n');
const exceptions = [];
for(const p of Object.keys(expected)) if(frozen[p]!==expected[p]) {
  if(phase==='before' || p!=='pkg/api/cursor_liveness_list_test.go') throw Error('unexpected frozen change '+p);
  exceptions.push({path:p,old:expected[p],new:frozen[p],authorization:'Only absent-binding control changed from error/no mutation to truthful missing/atomic close/idempotence; other controls and causal List regression unchanged.'});
}
if(phase==='after' && fs.readFileSync(out+'/source-before.json','utf8')!==fs.readFileSync(out+'/source-after.json','utf8')) throw Error('source changed');
fs.writeFileSync(out+'/proof-'+phase+'.json',JSON.stringify({frozen_count:Object.keys(expected).length,exceptions,source_unchanged:phase==='after'},null,2)+'\n');
