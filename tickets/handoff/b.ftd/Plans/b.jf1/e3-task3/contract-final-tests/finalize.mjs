import fs from 'node:fs';
import crypto from 'node:crypto';
const out = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/contract-final-tests/';
const sha = p => crypto.createHash('sha256').update(fs.readFileSync(out + p)).digest('hex');
const report = JSON.parse(fs.readFileSync(out + 'FINAL-report.json', 'utf8'));
// Preserve the prior assembly, including its hash of a still-open metadata log.
fs.renameSync(out + 'FINAL-report.json', out + '06-report-before-final-receipt-hash.json');
report.deviations.push('Report assembly initially hashed its still-open metadata transport log; prior assembly preserved as 06-report-before-final-receipt-hash.json. Final receipt hashes below are collected only after all referenced logs have closed. No test/input changes or rerun.');
report.evidence_finalization = {
  command: "make sandbox CMD='bun tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/contract-final-tests/finalize.mjs'",
  result: 'metadata only, successful when checksum file exists',
  authoritative_checksums: 'checksums.json',
  exclusions: 'checksums.json excludes itself and the currently open 07-finalize.log/exit transport receipt; all test logs/exits/manifests and complete closed evidence logs are included.'
};
const completed = fs.readdirSync(out).filter(p => p !== 'checksums.json' && p !== 'FINAL-report.json' && !p.startsWith('07-') && fs.statSync(out + p).isFile()).sort();
report.receipt_sha256 = Object.fromEntries(completed.map(p => [p, sha(p)]));
fs.writeFileSync(out + 'FINAL-report.json', JSON.stringify(report, null, 2) + '\n', {flag: 'wx'});
const entries = [...completed, 'FINAL-report.json'].sort();
fs.writeFileSync(out + 'checksums.json', JSON.stringify(Object.fromEntries(entries.map(p => [p, sha(p)])), null, 2) + '\n', {flag: 'wx'});
console.log(JSON.stringify({result: report.result, files_checksummed: entries.length, full_exit: report.full_suite.exit,
  go: {executed_passed: report.full_suite.go.executed_passed_packages, cached: report.full_suite.go.cached_passed_packages, no_test_files: report.full_suite.go.no_test_files_packages, failed_packages: report.full_suite.go.failed_packages},
  bun: report.full_suite.bun, unresolved: report.quality.mandatory_unresolved, drift: report.tested_tree.drift,
  target_passed_leaves: report.commands[1].receipt.passed_leaves, trail_passed_leaves: report.commands[2].receipt.passed_leaves}));
