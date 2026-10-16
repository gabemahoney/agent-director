const fs = require('fs'), crypto = require('crypto'), cp = require('child_process');
const base = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-integration-fixes/full-fixture-startup-red';
const hash = p => crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex');
const files = ['internal/cursorhost/session_controls_linux_test.go','test/realtmux/cursor_launch_test.go','test/realtmux/cursor_relay_boundary_linux_test.go','pkg/ts-bun-client/test/client-serialization.test.ts','test/smoke/ts-helper/main.go','test/smoke/ts-helper/fake_pane_owner.go','pkg/api/cursor_liveness_test.go','pkg/api/cursor_liveness_list_test.go','pkg/api/cursor_startup_liveness_test.go','internal/store/cursor_startup_liveness_test.go','test/realtmux/cursor_startup_liveness_linux_test.go'];
const newRed = ['pkg/api/cursor_liveness_test.go','pkg/api/cursor_liveness_list_test.go','pkg/api/cursor_startup_liveness_test.go','internal/store/cursor_startup_liveness_test.go','test/realtmux/cursor_startup_liveness_linux_test.go',base+'/sandbox-command.sh',base+'/targeted-command.sh',base+'/proof.cjs'];
fs.writeFileSync(base+'/new-red-frozen-sha256.json', JSON.stringify(Object.fromEntries(newRed.map(p=>[p,hash(p)])),null,2)+'\n');
fs.writeFileSync(base+'/test-fixture-sha256.json', JSON.stringify(Object.fromEntries(files.map(p=>[p,hash(p)])),null,2)+'\n');
const currentList = fs.readFileSync('pkg/api/cursor_liveness_list_test.go','utf8');
const oldList = currentList.replace(/\t\t\tif tc\.absentBinding \{\n\t\t\t\t\/\/ Exact gateway loss[\s\S]*?\n\t\t\t\}\n(?=\t\t\tgot, err := c.List\(params\))/, '');
fs.writeFileSync(base+'/list-absent-control-before.go.txt',oldList);
const expectedList = JSON.parse(fs.readFileSync(base+'/frozen-before.json'))['pkg/api/cursor_liveness_list_test.go'];
if(hash(base+'/list-absent-control-before.go.txt')!==expectedList) throw Error('List reconstruction is not original frozen file');
const currentGet = fs.readFileSync('pkg/api/cursor_liveness_test.go','utf8');
const oldGet = currentGet.replace('\t\t{"stale session",', '\t\t{"absent binding", "DELETE FROM cursor_runtimes WHERE bound_instance_id=?", false},\n\t\t{"stale session",');
fs.writeFileSync(base+'/get-status-absent-control-before.go.txt',oldGet);
const diffs = [['list-absent-control-before.go.txt','pkg/api/cursor_liveness_list_test.go'],['get-status-absent-control-before.go.txt','pkg/api/cursor_liveness_test.go']];
for (const [old,p] of diffs) {
  let diff;
  try { diff=cp.execFileSync('git',['diff','--no-index','--',base+'/'+old,p],{encoding:'utf8'}); }
  catch(e) { if(e.status!==1) throw e; diff=e.stdout; }
  fs.writeFileSync(base+'/'+old.replace('.go.txt','.patch'),diff);
}
const labels = ['fifo-attempt-1','pane-attempt-1','bun-attempt-1','startup-real-red-attempt-1','startup-api-red-attempt-1','startup-store-red-attempt-1'];
const results = Object.fromEntries(labels.map(label=>{
  const log=base+'/'+label+'.log', raw=fs.readFileSync(log,'utf8');
  return [label,{exit_code:Number(fs.readFileSync(base+'/'+label+'-exit-code.txt','utf8')),log,sha256:hash(log),mode:(fs.statSync(log).mode&0o777).toString(8),top_level_passes:(raw.match(/^--- PASS:/gm)||[]).length,top_level_failures:(raw.match(/^--- FAIL:/gm)||[]).length,subtest_passes:(raw.match(/^\s+--- PASS:/gm)||[]).length,subtest_failures:(raw.match(/^\s+--- FAIL:/gm)||[]).length}];
}));
fs.writeFileSync(base+'/results.json',JSON.stringify(results,null,2)+'\n');
const report={
  status:'Fixture targeted GREEN; pre-registration gateway death causal RED retained for Engineer',
  ticket_ids_context:['t1.ftd.y5','t1.ftd.hj','t1.jf1.r8'],ticket_status_changes:[],commits:[],
  files_changed:files,
  commands:['format','fixtures','startup','store'].map(mode=>`make sandbox CMD='bash ${base}/sandbox-command.sh ${base}/targeted-command.sh ${mode}'`),
  results,
  fixture_green:{fifo:'1 test; all three follow-ups admitted before HOLD release; separate existing 3s transition waits correlated to each accepted submission/start/request/outcome; Unicode/tab/newline, exact order, once-only and binding assertions retained; bounded diagnostic dump added',pane:'2 tests / 7 leaves; original ownership row still passed to verifier; raw full row baseline after independently exact old gateway/runtime death and terminal commit for server replacement/respawn; strict rejection/no-write preserved',cancellation:'interrupt 3.034920384s and policy 3.034731534s; original 5s observation/fixed 3s product cancellation; one cancel, no permission answer, independent worker alive. Missing allowed only with independently known exact runtime or gateway loss; denied/closed/proven-gone requests then required; identity preserved. Ordinary successful hold release still stays alive beyond cancellation bound',bun:'4 Cursor transport cases pass, 30 expects; real controlled sleep PID/start through every read; fake NewPanePID via ts-helper; recorded pane PID/start independently revalidated at end; kill and await joined in finally; pending/args/wire/subagent assertions retained'},
  startup_red:{real:'Real production CLI spawn launches production cursor-host in isolated real tmux with nonexistent synthetic runtime path; gateway identity 1117/442868107 independently gone, no cursor_runtimes row, raw pending retains model args. Get/Status/List each fail with lost gateway has no current runtime binding; pending version1 unchanged. No Cursor/live/auth invocation.',api:'3 exact-gone success cases plus 6 contention/rollback cases and the authorized absent-binding List control RED. Six no-binding alive/unknown controls GREEN. Stale-session/life and existing request-close error controls GREEN.',store:'Unchanged no-binding terminal close and downstream subagent rollback cases RED (CondChanged instead of applied/injected error). Seven insertion/snapshot/token/life/gateway guard controls GREEN. Positive case requires common request/subagent closure and no fabricated runtime binding.',engineer_contract:'Extend existing MarkCursorMissingIfSameLaunch(row,b) for an explicit examined-absence descriptor: row instance/token/life/gateway/session retained, runtime/connection/pipe/fd fields empty. The new store test uses this minimal descriptor, not a forged runtime. Its separate committed registration after absence observation leaves the spawn snapshot unchanged and requires NOT EXISTS for ANY runtime row inside the terminal write, including stale existing bindings. Existing nonempty binding branch remains strict. Public helper must invoke this branch only after exact gateway Gone and sql.ErrNoRows; never alive/unknown or stale binding errors.'},
  frozen:{original_count:11,unchanged_count:10,authorized_exception:'pkg/api/cursor_liveness_list_test.go only absent-binding control; old file reconstructed and verified byte-for-byte against historical frozen SHA; exact patch and old/new proof retained; no historical manifest or RED proof rewritten',get_status_delta:'Removed absent-binding from unsafe-binding-error table; replacement positive exact-gone tests cover Get/Status/List and conservative alive/unknown controls. Stale session/life and request-close assertions untouched.',proof:'proof-after.json',new_red_manifest:'new-red-frozen-sha256.json'},
  source_proof:{files:11,unchanged:true,before:'source-before.json',after:'source-after.json'},
  environment:'All repository scripts, tests, builds, formatting and hashing run only via literal make sandbox. Canonical sandbox HOME=/home/sandbox; umask022; unset TMPDIR, inherited AD*/AGENT_DIRECTOR_* except guard, TMUX/TMUX_PANE/GIT_CONFIG_GLOBAL; run-local git config. No recursive sandbox CMD.',
  deviations:['Named read-only shell inspection (cat/rg/git) used host before and between sandbox executions; no repository code/scripts/tests/binaries executed on host. Bees ticket reads used the external metadata CLI. No forbidden paths or host AD stores/auth/config/cache read or copied.','The new store contract represents examined no-registration through a zero-runtime descriptor on the existing conditional method to keep RED behaviorally executable without a production/API seam. This adds no product source; Engineer must implement this explicit branch with in-transaction absence guard.'],
  incomplete_work:['Production absent-binding fix intentionally not made; causal REDs must turn GREEN on unchanged frozen tests.','Fresh full suite and scheduler/live gate remain caller-owned; targeted fixture GREEN is not full acceptance.'],
  not_run:['full unit suite','integration Docker groups','collectors/testimage','TLC/java/formal/parser','scheduler jobs','live Cursor/O4'],
  questions:[],recorded_utc:new Date().toISOString()
};
fs.writeFileSync(base+'/report.json',JSON.stringify(report,null,2)+'\n');
console.log(JSON.stringify({status:report.status,results,source_unchanged:true,frozen:'10 unchanged; one explicit authorized control exception'},null,2));
