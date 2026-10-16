import {readFileSync,writeFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import assert from 'node:assert/strict';
const base='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3';
const json=p=>JSON.parse(readFileSync(p,'utf8'));
const hash=p=>createHash('sha256').update(readFileSync(p)).digest('hex');
const pub=json(`${base}/public/source-integrity.json`);
const old=json(`${base}/deadline-red/old-production-receipt.json`);
const collector=json(`${base}/collector/FINAL-report.json`);
for(const [p,digest] of Object.entries(old.production_manifest)){
 assert.ok(!p.startsWith('.claude/')&&!p.startsWith('tickets/'));
 assert.equal(hash(p),pub.expected_settled_source_sha256[p]||collector.file_sha256[p]||digest,p);
}
for(const [p,digest] of Object.entries(pub.expected_settled_source_sha256))assert.equal(hash(p),digest,p);
assert.equal(hash('pkg/api/decide_trail_deadline_test.go'),'032950e86d9919e2c48ae79189751e36f8875312a78bf1410973954fdde86d88');
const owned=['test/realtmux/cursor_pane_controls_test.go','test/realtmux/cursor_pane_controls_boundary_linux_test.go'];
const result={all_old_production_paths_verified:Object.keys(old.production_manifest).length,settled_source_sha256:pub.expected_settled_source_sha256,prior_public_test_sha256:pub.own_test_sha256,repaired_public_test_sha256:Object.fromEntries(owned.map(p=>[p,hash(p)])),causal_regression_sha256:hash('pkg/api/decide_trail_deadline_test.go')};
writeFileSync(`${base}/acceptance-review-fix/repair-inputs.json`,JSON.stringify(result,null,2)+'\n');
console.log(JSON.stringify({all_production_unchanged:true,old_paths:result.all_old_production_paths_verified,causal_regression_unchanged:true}));
