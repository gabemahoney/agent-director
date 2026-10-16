const fs=require('fs'),crypto=require('crypto');
const own='tickets/handoff/b.ftd/Plans/b.jf1/e3-integration-fixes/startup-final-full';
const read=p=>JSON.parse(fs.readFileSync(own+'/'+p,'utf8'));
const report=read('report.json'),run=read('run.json');
const policy='Git tracked plus standard untracked root inputs and cmd/internal/pkg/test/skills/scripts/docs/tools/.github/tickets inputs; tickets/handoff and archive/underscore path components excluded by name; .claude excluded by name before any filesystem access; no symlink target reads. All go list selected source files checked against this policy.';
run.manifest_policy=policy;
report.manifest_policy=policy;
report.canonical_inventory_count=run.canonical_input_count;
report.package_inventory_count=run.package_inventory_count;
report.justification='Settled source, tests and docs changed since the immutable first failed full run. Independent final unit validation, exactly one bare make test-sandbox; no targeted repetitions.';
report.environment={container_HOME:'/home/sandbox',container_TMPDIR:'unset',umask:'022',git_config:own+'/gitconfig',host_inherited_variables_removed:run.removed_environment_names,recursive_sandbox_CMD:false};
report.deviations=[
 'Named read-only host inspections of repository docs/source/tests/reports preceded sandbox execution. No host repo scripts/tests/builds/binaries or protected paths accessed.',
 'Two preparatory metadata attempts failed before any tests: canonical-inventory policy initially also rejected explicitly authorized frozen handoff proof paths, then omitted tools from compiled-source policy. Corrected proof policy before the sole full run. No product, test, docs, deadline or assertion changes.',
 'Evidence bootstrap initialized scripts in sandbox before canonical container HOME/git-config flags were installed; all inventory/hashing and the full suite used the canonical sandbox environment.'
];
report.frozen_proof={original_files:11,original_unchanged:10,authorized_list_control_exception:true,new_red_files:8,before_all_match:read('frozen-before.json').files.every(f=>f.matches)&&read('new-red-frozen-before.json').every(f=>f.matches),after_all_match:read('frozen-after.json').files.every(f=>f.matches)&&read('new-red-frozen-after.json').every(f=>f.matches)};
report.test_source_doc_files_modified=[];
report.integration_run='Not dispatched; integration suites and heavy Docker groups not run.';
report.live_or_future_gate_claim=false;
report.failure_classification=run.make_exit===0?'No failures observed in this full run.':'Actual captured failures; stop after original full run, no unchanged retry or automatic fix.';
report.not_run=['targeted test repetitions','integration suites','heavy Docker testplan/collector/testimage','TLC/java/formal/parser/scheduler commands','live Cursor/O4'];
report.recorded_utc=new Date().toISOString();
for(const [p,v] of [['run.json',run],['report.json',report]])fs.writeFileSync(own+'/'+p,JSON.stringify(v,null,2)+'\n',{mode:0o600});
fs.writeFileSync(own+'/report.sha256',crypto.createHash('sha256').update(fs.readFileSync(own+'/report.json')).digest('hex')+'\n',{mode:0o600});
console.log(JSON.stringify({status:report.status,make_exit:report.make_exit,go:report.go,bun:report.bun,canonical_inventory_count:report.canonical_inventory_count,proof:report.proof,frozen_proof:report.frozen_proof,failure_assertions:report.failure_assertions,report:own+'/report.json'},null,2));
