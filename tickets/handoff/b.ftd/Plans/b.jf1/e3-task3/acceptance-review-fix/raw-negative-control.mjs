// Offline assertion evidence only; no collector helper or case is executed.
import {mkdtempSync,writeFileSync,readFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {spawnSync} from 'node:child_process';
import assert from 'node:assert/strict';
const dir=mkdtempSync(join(tmpdir(),'pane-raw-negative-'));
const complete=Buffer.from('{"id":1,"jsonrpc":"2.0","method":"session/new","params":{}}\n');
const partial=Buffer.concat([complete,Buffer.from('{')]);
const trace=join(dir,'trace'),raw=join(dir,'raw');
writeFileSync(trace,complete);writeFileSync(raw,complete);
assert.equal(spawnSync('cmp',[raw,trace]).status,0);
writeFileSync(raw,partial);
assert.equal(spawnSync('cmp',[raw,trace]).status,1);
assert.ok(!readFileSync(raw).equals(readFileSync(trace)));
const decoded=b=>b.toString().split('\n').slice(0,-1).map(JSON.parse);
assert.deepEqual(decoded(partial),decoded(complete));
assert.ok(!partial.includes(Buffer.from('session/prompt')));
console.log(JSON.stringify({offline_only:true,collector_or_helper_executed:false,complete_stream:'accepted',one_partial_byte:'rejected by cmp and Buffer equality',decoded_frame_counts:'unchanged',full_forbidden_method_present:false}));
