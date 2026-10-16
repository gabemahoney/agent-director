const fs = require('fs');
const crypto = require('crypto');
const base = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-integration-fixes';
const target = base + '/read-lock-engineer';
const expected = {...JSON.parse(fs.readFileSync(base + '/red/frozen-sha256.json')), ...JSON.parse(fs.readFileSync(base + '/read-lock-red/frozen-sha256.json'))};
for (const line of fs.readFileSync(base + '/list-red/frozen-sha256.txt', 'utf8').trim().split('\n')) {
  const match = line.match(/^([0-9a-f]{64})\s+(.+)$/);
  if (!match) throw new Error('invalid explicit frozen manifest');
  expected[match[2]] = match[1];
}
const actual = Object.fromEntries(Object.keys(expected).map(path => [path, crypto.createHash('sha256').update(fs.readFileSync(path)).digest('hex')]));
fs.writeFileSync(target + '/frozen-' + process.argv[2] + '.json', JSON.stringify(actual, null, 2) + '\n');
const equal = Object.keys(expected).every(path => actual[path] === expected[path]);
fs.writeFileSync(target + '/frozen-proof.json', JSON.stringify({explicit_artifact_count: Object.keys(expected).length, all_match_expected: equal, phase: process.argv[2]}, null, 2) + '\n');
if (!equal) throw new Error('frozen artifact changed');
