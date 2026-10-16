const fs = require('fs');
const dir = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-integration-fixes/bounded-green';
const labels = ['held-lock-green','original-green','list-and-guards-green','store-guards-green','trail-green'];
const runs = Object.fromEntries(labels.map(label => {
  const log = fs.readFileSync(dir+'/'+label+'.log','utf8');
  const exit = Number(fs.readFileSync(dir+'/'+label+'-exit-code.txt','utf8'));
  const mode = (fs.statSync(dir+'/'+label+'.log').mode & 511).toString(8);
  if (exit !== 0 || mode !== '600') throw Error('invalid result or permissions: '+label);
  return [label,{exit_code:exit,raw_log_mode:mode,top_level_passes:(log.match(/^--- PASS:/gm)||[]).length,subtest_passes:(log.match(/^    --- PASS:/gm)||[]).length}];
}));
fs.chmodSync(dir+'/initial-invalid-cache-attempt.log',384);
const report = {
  status:'independent targeted GREEN; new held-real-trail-mutex regression GREEN on its first test execution',
  ticket_ids_context:['t1.jf1.r8','3t2.jf1.r8.a8'], ticket_status_changes:[], commits:[],
  files_changed:['pkg/api/cursor_liveness_trail_deadline_test.go'],
  evidence_directory:dir,
  commands:["make sandbox CMD='bash "+dir+"/sandbox-command.sh regressions'","make sandbox CMD='bash "+dir+"/sandbox-command.sh trail'"],
  environment:'Each repository execution in make sandbox; sandbox runner strips AD*/AGENT_DIRECTOR_* except sandbox guard, TMUX, TMUX_PANE, TMPDIR, GIT_CONFIG_GLOBAL; canonical fresh container HOME=/home/sandbox, umask022, run-local git config. No recursive AGENT_DIRECTOR_SANDBOX_CMD.',
  results:runs,
  total_top_level_passes:Object.values(runs).reduce((sum,r)=>sum+r.top_level_passes,0),
  total_leaf_passes:Object.values(runs).reduce((sum,r)=>sum+r.subtest_passes,0),
  held_sql_writer_return_microseconds:{Get:303.489,Status:251.780,List:354.570},
  real_trail_mutex:{
    Get:{commit_observed_ms:20.386967,public_return_ms:1001.313608,still_held_ms:1002.133478},
    Status:{commit_observed_ms:11.681533,public_return_ms:1000.769008,still_held_ms:1001.529838},
    List:{commit_observed_ms:3.492248,public_return_ms:1001.011998,still_held_ms:1001.761148},
    assertions:['actual trailEncodingGate entered while singleton writer owns its mutex before each public call','separately reopened isolated store sees missing, exactly one version advance, denied/closed/proven-gone request while mutex held','public read returns success/missing within unchanged 1s immediate-work allowance plus 500ms scheduling slack, before release','exactly two pending audits and no failed/rejected delta while mutex remains held after public return','release then join holder; FIFO sentinel drains accepted audit events without closing global singleton','pending zero after drain; deny fields and orphan tick fields checked; exact deny-before-tick order once each; new inspection does not change terminal row'],
    fixture_reuse:['cursorReadLaunch','trailEncodingGate','readAPITrailLines','TestMain shared isolated trail fixture'],
    fake_emit_or_sleep:false, product_budget_changes:false, automatic_read_or_decision_retries:false
  },
  proof:JSON.parse(fs.readFileSync(dir+'/proof.json','utf8')),
  deviations:[
    'Initial host read-only inspection used named repo docs/tests/reports and rg before sandbox execution setup; no tests/builds/repository scripts ran on host, no .claude or host agent-director data path accessed.',
    'Initial invalid cache harness attempt stopped before any tests executed because host HOME=/home/sandbox selected an unwritable cache mount. Corrected only launcher environment; successful Go execution still used canonical sandbox HOME. Fatal error excerpt retained separately; it is explicitly not a full raw transcript.',
    'No type named TrailFixture exists in the checkout; reused the existing TestMain isolated singleton fixture and shared readAPITrailLines helper rather than adding a parallel fixture.',
    'Optional legacy-routing overlay omitted; requested causal RED already exists and the new behavior regression passed without overlays.'
  ],
  incomplete_work:[], remaining_source_findings:[], questions:[],
  deferred_to_caller:['fresh full make after documentation rework','local b.jf1 fix commit and user gate','O4 only after user gate'],
  not_run:['full unit suite','integration suites','live Cursor/O4','E4','heavy Docker epic groups','formal/model jobs'],
  recorded_utc:new Date().toISOString()
};
fs.writeFileSync(dir+'/report.json',JSON.stringify(report,null,2)+'\n');
console.log(JSON.stringify({runs,total_leaf_passes:report.total_leaf_passes,proof:report.proof},null,2));
