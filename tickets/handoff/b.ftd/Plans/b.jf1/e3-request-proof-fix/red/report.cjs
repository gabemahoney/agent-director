const fs = require('fs');
const cp = require('child_process');
const crypto = require('crypto');
const out = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-request-proof-fix/red';
const before = JSON.parse(fs.readFileSync(out+'/before.json'));
const after = JSON.parse(fs.readFileSync(out+'/after.json'));
const hash = value => crypto.createHash('sha256').update(value).digest('hex');
const summaries = {};
for (const name of ['api','store','runtime']) {
 const log = fs.readFileSync(out+'/'+name+'.log','utf8');
 summaries[name] = {
  exit: Number(fs.readFileSync(out+'/'+name+'.exit','utf8')),
  failedTests: log.split('\n').filter(line => /^\s*--- FAIL:/.test(line)),
  passedControls: log.split('\n').filter(line => /^\s*--- PASS:/.test(line)),
  exactCausalAssertions: log.split('\n').filter(line => /invented request proof|invented proof or rewrote|fabricated request proof/.test(line)),
  logSHA256:hash(log)
 };
}
const controls = ['internal/store/relay_close_test.go','tickets/testplans/b.jf1/t1.jf1.cr/t2.jf1.cr.05/t2.jf1.cr.05.md','tickets/testplans/b.jf1/t1.jf1.cr/t2.jf1.cr.09/t2.jf1.cr.09.md','tickets/testplans/b.jf1/t1.jf1.cr/t2.jf1.cr.10/t2.jf1.cr.10.md'];
const controlHashes = Object.fromEntries(controls.map(path => {
 const current = hash(fs.readFileSync(path));
 const original = hash(cp.execFileSync('git',['show','HEAD:'+path]));
 if (current !== original) throw new Error('protected control changed: '+path);
 return [path,current];
}));
const changed = cp.execFileSync('git',['diff','--name-only','--','internal','pkg','cmd','test','tickets/testplans'],{encoding:'utf8'}).trim().split('\n').filter(Boolean);
cp.execFileSync('git',['diff','--check','--',...changed]);
fs.writeFileSync(out+'/test-changes.patch',cp.execFileSync('git',['diff','--',...changed]));
const report = {
 role:'sole RED Test Writer', idea:'b.ftd', plan:'b.jf1', epic:'t1.jf1.r8', task:'t2.jf1.r8.a8',statusesChanged:[],commits:[],
 head:after.head, result:'causal RED established; Engineer repair and independent GREEN required',
 command:'make sandbox CMD=\'bash tickets/handoff/b.ftd/Plans/b.jf1/e3-request-proof-fix/red/sandbox-command.sh red\'',
 runs:summaries,
 productionGoFilesFrozen:Object.keys(after.sources).length,
 protectedCollectorDriverFilesFrozen:Object.keys(after.protectedFiles).length,
 productionSourcesUnchanged:JSON.stringify(before.sources)===JSON.stringify(after.sources),
 collectorsUnchanged:JSON.stringify(before.protectedFiles)===JSON.stringify(after.protectedFiles),
 frozenTestsAndRunners:after.tests,unchangedControlSHA256:controlHashes,
 testFilesChanged:[...changed,'pkg/api/cursor_terminal_request_proof_test.go','internal/store/cursor_terminal_request_proof_test.go'],
 oracleCorrections:[
  {file:'pkg/api/cursor_startup_liveness_test.go',old:'required requestAfter.ProvenGone()',new:'requires !requestAfter.ProvenGone() and empty ProvenGoneHow'},
  {file:'pkg/api/cursor_liveness_list_test.go',old:'required pr.ProvenGone()',new:'requires !pr.ProvenGone() and empty ProvenGoneHow'},
  {file:'pkg/api/cursor_liveness_trail_deadline_test.go',old:'required ProvenGoneHow == agent_gone',new:'requires no ProvenGoneAt and empty ProvenGoneHow'},
  {file:'internal/store/cursor_startup_liveness_test.go',old:'required requestAfter.ProvenGone()',new:'requires !requestAfter.ProvenGone() and empty ProvenGoneHow'}
 ],
 oracleCorrectionReason:'Only newly added E3 gateway-loss assertions were corrected. Row terminal truth and atomic orphan close/deny do not establish request-hook death or Cursor receipt; E2 request-proof uncertainty survives gateway/runtime death. Original missing row/version, failure, SIGKILL, deadlines/retries and cr tests remain strict and unchanged.',
 causalFindings:[
  'Public Get, Status and List reach missing exactly once and close undecided/decided requests, while wrongly stamping agent_gone on undecided, decided and delivered-unproven requests.',
  'Delivered acknowledgement timestamp remains set and unchanged; gateway loss adds the unsupported proof only. Whole PermissionRow comparison otherwise checks ack, decision, attempted-decision, hook, pane/sender and existing verified proof metadata.',
  'Previously verified tool_ran proof remains unchanged; independent real hook process remains alive before and after public reconciliation.',
  'Store MarkCursorMissingIfSameLaunch, MarkMissingIfSameLife, authenticated terminated-runtime SessionEnd and resume backstop all add unsupported proof to delivered-unproven Cursor requests.',
  'Actual malformed-update fatal Run and gateway-only SIGKILL regressions retain strict missing/owned-death assertions and newly fail only for fabricated request proof. DIE control passes.',
  'Legacy TestCloseClosesRelayRequests passes all five Claude/Codex controls, retaining agent_gone behavior and existing tool proof.'
 ],
 sourceInsight:{
  path:'MarkCursorMissingIfSameLaunch -> terminalMarkWithoutWait -> terminalMarkOnConn -> closeOrphanedRequests -> proveAgentGoneSQL',
  sharedCallers:['internal/store/find_missing_writes.go','internal/store/hook_writes.go','internal/store/resume_writes.go'],
  requiredBoundary:'Keep row terminal and atomic close/deny semantics; prevent shared unconditional proof stamp for Cursor without changing Claude/Codex proof controls. Existing correlated proof must not be erased or overwritten. Engineer chooses implementation scope.'
 },
 limitations:['No GREEN/source repair claimed.','No full suite or heavy Docker groups, collector/parser execution, formal jobs, scheduler or live/auth work run.','Public API proof fixture uses a synthetic persisted binding/process checker plus a real independent hook process; real gateway/runtime death additionally covered by unchanged strict SIGKILL fixture.'],
 deviations:[],questions:[],TLA:'not applicable: no model inputs changed'
};
fs.writeFileSync(out+'/report.json',JSON.stringify(report,null,2)+'\n');
console.log(JSON.stringify({head:report.head,result:report.result,failedTests:Object.fromEntries(Object.entries(summaries).map(([name,r])=>[name,r.failedTests.length])),sourcesUnchanged:report.productionSourcesUnchanged,collectorsUnchanged:report.collectorsUnchanged}));
