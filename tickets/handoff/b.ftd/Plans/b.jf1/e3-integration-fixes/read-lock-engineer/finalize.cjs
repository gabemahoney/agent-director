const fs = require('fs');
const crypto = require('crypto');
const base = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-integration-fixes/read-lock-engineer';
const report = JSON.parse(fs.readFileSync(base + '/report.json'));
report.recorded_utc = new Date().toISOString();
report.actual_test_results = {};
for (const label of ['held-lock-green', 'original-green', 'list-green', 'api-guards-green', 'store-targeted-green']) {
  const exitCode = Number(fs.readFileSync(base + '/' + label + '-exit-code.txt', 'utf8').trim());
  const path = base + '/' + label + '.log';
  const log = fs.readFileSync(path, 'utf8');
  const mode = fs.statSync(path).mode & 0o777;
  if (exitCode !== 0 || !/^PASS$/m.test(log) || mode !== 0o600) throw new Error(label + ' did not pass or unsafe raw mode');
  report.actual_test_results[label] = {exit_code: exitCode, raw_log_mode: mode.toString(8), top_level_passes: (log.match(/^--- PASS:/gm) || []).length, subtest_passes: (log.match(/^\s+--- PASS:/gm) || []).length};
}
const before = JSON.parse(fs.readFileSync(base + '/frozen-before.json'));
const after = JSON.parse(fs.readFileSync(base + '/frozen-after.json'));
if (JSON.stringify(before) !== JSON.stringify(after) || Object.keys(after).length !== 11) throw new Error('frozen mismatch');
report.frozen_proof = JSON.parse(fs.readFileSync(base + '/frozen-proof.json'));
report.source_hashes = Object.fromEntries(report.files_changed.map(path => [path, crypto.createHash('sha256').update(fs.readFileSync(path)).digest('hex')]));
fs.writeFileSync(base + '/report.json', JSON.stringify(report, null, 2) + '\n');
console.log(JSON.stringify({status: report.status, tests: report.actual_test_results, frozen: report.frozen_proof}, null, 2));
