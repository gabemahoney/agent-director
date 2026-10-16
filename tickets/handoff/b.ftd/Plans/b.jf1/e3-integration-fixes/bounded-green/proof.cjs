const fs = require('fs');
const crypto = require('crypto');
const base = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-integration-fixes';
const out = base + '/bounded-green';
const phase = process.argv[2];
const expected = {...JSON.parse(fs.readFileSync(base+'/red/frozen-sha256.json')), ...JSON.parse(fs.readFileSync(base+'/read-lock-red/frozen-sha256.json'))};
for (const line of fs.readFileSync(base+'/list-red/frozen-sha256.txt','utf8').trim().split('\n')) {
  const match = line.match(/^([0-9a-f]{64})\s+(.+)$/);
  if (!match) throw Error('invalid manifest');
  expected[match[2]] = match[1];
}
const hash = paths => Object.fromEntries(paths.map(p => [p, crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex')]));
const frozen = hash(Object.keys(expected));
const source = hash(['internal/cursorhost/session.go','internal/store/cursor_runtime.go','internal/store/find_missing_writes.go','internal/store/lockwait.go','internal/store/request_close.go','internal/store/recovery.go','internal/store/permission.go','pkg/api/cursor_liveness.go','pkg/api/get.go','pkg/api/status.go','pkg/api/list.go']);
fs.writeFileSync(out+'/frozen-'+phase+'.json', JSON.stringify(frozen,null,2)+'\n');
fs.writeFileSync(out+'/source-'+phase+'.json', JSON.stringify(source,null,2)+'\n');
if (Object.keys(expected).length !== 11 || JSON.stringify(expected) !== JSON.stringify(frozen)) throw Error('frozen mismatch');
if (phase === 'after' && fs.readFileSync(out+'/source-before.json','utf8') !== fs.readFileSync(out+'/source-after.json','utf8')) throw Error('source changed');
fs.writeFileSync(out+'/proof.json', JSON.stringify({phase, frozen_artifacts:11, all_match_expected:true, source_files:11, source_unchanged:phase === 'after'},null,2)+'\n');
