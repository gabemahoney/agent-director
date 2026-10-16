import { readFileSync, writeFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
const base = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/';
const out = base + 'api-deadline-assessment/';
const digest = path => createHash('sha256').update(readFileSync(path)).digest('hex');
const json = path => JSON.parse(readFileSync(path, 'utf8'));
const report = json(out + 'FINAL-report.json');
const expectedSources = json(base + 'deadline-review-fix/FINAL-report.json').source_after_sha256;
const sources = Object.entries(expectedSources).map(([path, expected]) => ({path, expected, actual: digest(path)}));
if (sources.length !== 8 || sources.some(x => x.actual !== x.expected)) throw new Error('settled source mismatch');
const protectedPath = 'pkg/api/decide_trail_deadline_test.go';
const protectedHash = digest(protectedPath);
if (protectedHash !== '032950e86d9919e2c48ae79189751e36f8875312a78bf1410973954fdde86d88') throw new Error('protected regression changed');
const manifest = json(base + 'final-tests/input-manifest-after.json');
const drift = [];
for (const [path, expected] of Object.entries(manifest)) {
  if (path.split('/').includes('.claude')) throw new Error('forbidden manifest path');
  const actual = digest(path);
  if (actual !== expected) drift.push({path, expected, actual});
}
if (drift.length) throw new Error('prior source/test/doc input manifest drift: ' + JSON.stringify(drift));
const historicalPath = base + 'deadline-red/old-production-receipt.json';
const historical = json(historicalPath);
if (Object.keys(historical.production_manifest).length !== 1333) throw new Error('old manifest count');
const inputs = report.evidence.complete_inputs.map(path => {
  const full = base + path;
  const bytes = readFileSync(full);
  return {path, bytes: bytes.length, lines: bytes.toString('utf8').split('\n').length - 1, sha256: digest(full)};
});
const expectedLog = json(base + 'final-tests/FINAL-report.json').receipt_sha256['04-full-test-sandbox.log'];
if (digest(base + 'final-tests/04-full-test-sandbox.log') !== expectedLog) throw new Error('full failed receipt changed');
const priorHistoricalHash = json(base + 'deadline-red/FINAL-report.json');
const integrity = {
  complete_inputs: inputs, sources, all_eight_settled_sources_match: true,
  prior_current_input_manifest_count: Object.keys(manifest).length, prior_current_input_manifest_drift: drift,
  protected_regression: {path: protectedPath, sha256: protectedHash, unchanged: true},
  historical_receipt: {path: historicalPath, sha256: digest(historicalPath), production_manifest_count: 1333, revision: historical.revision, untouched_by_lane: true},
  historical_report_sha256: digest(base + 'deadline-red/FINAL-report.json'),
  report_sha256: digest(out + 'FINAL-report.json'),
  tests_run: [], full_suite_run: false
};
writeFileSync(out + 'integrity.json', JSON.stringify(integrity, null, 2) + '\n');
console.log(JSON.stringify({sources: sources.length, prior_manifest_files: Object.keys(manifest).length, drift: drift.length, old_manifest_entries: 1333, protected_regression: protectedHash, complete_inputs: inputs.length, tests_run: 0}));
