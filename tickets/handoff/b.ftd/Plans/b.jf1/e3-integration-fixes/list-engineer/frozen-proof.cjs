const fs = require('fs');
const crypto = require('crypto');
const base = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-integration-fixes/';
const expected = JSON.parse(fs.readFileSync(base + 'red/frozen-sha256.json', 'utf8'));
for (const line of fs.readFileSync(base + 'list-red/frozen-sha256.txt', 'utf8').trim().split('\n')) {
  const [hash, path] = line.split(/\s+/);
  expected[path] = hash;
}
const actual = {};
for (const [path, hash] of Object.entries(expected)) {
  actual[path] = crypto.createHash('sha256').update(fs.readFileSync(path)).digest('hex');
  if (actual[path] !== hash) throw new Error('Frozen artifact changed: ' + path);
}
fs.writeFileSync(base + 'list-engineer/frozen-proof.json', JSON.stringify({equal: true, count: Object.keys(actual).length, expected, actual}, null, 2) + '\n');
console.log('All ' + Object.keys(actual).length + ' explicit frozen artifacts unchanged.');
