import fs from 'node:fs';
import crypto from 'node:crypto';

const dir = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/native-review-fix';
const previous = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/native-boundaries/input-manifest-after.json';
const files = ['sqlite_connector.go', 'lockwait.go', 'cursor_prompt.go', 'cursor_rotation.go'].map(f => `internal/store/${f}`);
const digest = path => ({sha256: crypto.createHash('sha256').update(fs.readFileSync(path)).digest('hex'), mode: (fs.statSync(path).mode & 0o777).toString(8)});
const bindings = files.map(path => ({path, before_path: `${dir}/before/${path}`, before: digest(`${dir}/before/${path}`), after: digest(path)}));
const baseline = JSON.parse(fs.readFileSync(previous));
const changed = [];
let checked = 0;
for (const [path, prior] of Object.entries(baseline)) {
  if (path.split('/').includes('.claude')) throw new Error('Forbidden path in baseline');
  const current = digest(path);
  checked++;
  if (current.sha256 !== prior.sha256 || parseInt(current.mode, 8) !== prior.mode) changed.push({path, prior, current});
}
const unexpected = changed.filter(item => !files.includes(item.path));
if (unexpected.length) throw new Error(`Unexpected input drift: ${JSON.stringify(unexpected)}`);
let patch = '';
for (const path of files) {
  const result = Bun.spawnSync(['diff', '-u', '--label', `before/${path}`, '--label', `after/${path}`, `${dir}/before/${path}`, path]);
  if (result.exitCode !== 1) throw new Error(`Expected source diff: ${path}, exit=${result.exitCode}`);
  patch += result.stdout.toString();
}
fs.writeFileSync(`${dir}/minimal.patch`, patch);
const receipts = ['01-targeted-race', '02-vet', '03-api-race', '04-primary-sources'].map(name => {
  const log = `${dir}/${name}.log`;
  const exit = Number(fs.readFileSync(`${dir}/${name}.exit`, 'utf8').trim());
  const names = [...fs.readFileSync(log, 'utf8').matchAll(/(?:^|\n)\s*--- PASS: (\S+) /g)].map(m => m[1]);
  const leaves = names.filter(name => !names.some(other => other.startsWith(`${name}/`)));
  return {name, log, exit, ...digest(log), passed_nodes: names.length, passed_leaves: leaves.length, leaves};
});
if (receipts.some(r => r.exit !== 0)) throw new Error('A verification receipt failed');
const report = {
  role: 'apiary_engineer_e3_task3_native_review_fix',
  result: 'Implemented mandatory N1/N2 source fixes. Targeted existing race tests, store vet and diff checks pass. Fresh independent N1/N2 regression writer and review remain required; no full-suite run or test/document edits.',
  head: 'e85a74c9803ffe1dddb80bdcb19bf4614f643839', branch: 'feature/b.ftd',
  tickets: {'t3.jf1.r8.a8.ir': 'done -> active -> done'},
  source_files: bindings,
  minimal_patch: {path: `${dir}/minimal.patch`, ...digest(`${dir}/minimal.patch`)},
  integrity: {baseline: previous, checked_inputs: checked, changed, unexpected, protected_trail: digest('pkg/api/decide_trail_deadline_test.go'), protected_no_match: digest('internal/store/permission.go')},
  decisions: {
    N1: 'Production OpenDB wraps sqliteConnector with callerOwnedConnector. A cancelable context without the private explicit-acquisition marker is refused with driver.ErrBadConn BEFORE physical Open or initialization. database/sql openNewConnection sends that error to a waiting acquisition; DB.Conn retry executes in the actual waiting caller with its marker and original context, under its existing bounded-operation slot. The background opener is never given any caller context. No mutable latest-context state, foreign provenance, new worker or queue. Noncancelable legacy pool calls remain synchronous and can replace connections with normal initialization. All boundedConnContext acquisitions are marked, including DefaultLockWait with explicit call cancellation and zero wait; zero still separates 1ms admission from original fixed SQL budget. The separate explicit ReconcileCursorRotation pool context is also marked to preserve its legitimate cancelable acquisition.',
    N1_primary_sources: 'Sandbox Go1.22.10 database/sql: OpenDB derives the pool context from Background; finalClose schedules replacements after physical Close; maybeOpenNewConnections reserves capacity; openNewConnection uses only the pool context and delivers connection errors to waiters; DB.Conn uses retry on ErrBadConn then opens directly with its explicit context. modernc1.34.0 Driver.Open/newConn have no context and DSN pragma execution uses Background. Successful raw inspection is retained in 04-primary-sources.log.',
    N2: 'CheckCursorPrompt returns false,err on failure BEFORE reading callback-owned eligible. Its exact authority, hold count and until guards remain unchanged. A nil inWriteTx result publishes completed callback values under operation synchronization before the successful read.',
    result_slot_audit: 'Reviewed every production withConn/readWithin/inWriteTx call in permission.go, spawns.go, relay_writes.go, cursor_permission.go, cursor_rotation.go, pane_writes.go and cursor_prompt.go. Existing private locals are read only after error checks or error-first short-circuit guards (RecordPaneIntent err != nil || !written; ProveCursorToolRan err == nil && written). No other same-pattern callback result race found; no unrelated edits.'
  },
  validation: receipts,
  limitations: [
    'Current native-boundary fixtures build sql.OpenDB(sqliteConnector) directly, bypassing the new outer production callerOwnedConnector. Their passing race checks preserve existing native ownership/initialization behavior, but do not independently prove N1 mixed legacy/bounded replacement. Fresh writer must retain the outer production callerOwnedConnector when wrapping physical Driver.Open (or otherwise exercise openContextSQLite actual production pool).',
    'No new N1/N2 regression was written in the source-only lane. Exact pre-review source bytes and modes are preserved for independent RED overlays; no review-fix RED is claimed.',
    'ReconcileCursorRotation retains its existing synchronous SQL contract; this patch supplies explicit connector provenance to preserve its original cancellation behavior. It is not moved into the bounded-operation worker lane.',
    'No full suite, Docker TESTPLAN collector/group, scheduler job, TLA/parser/Java job, credential/store access, .claude access, live Cursor action, commit or push. TLA+ not applicable: no model inputs changed.',
    'Historical09 causes remain unknown; retained full-suite read failures isolate outer read methods but not native/scheduling phases. No passing focused result establishes those historical causes.'
  ],
  independent_handoff: [
    'N1: use actual production callerOwnedConnector around sqliteConnector with physical native Open boundary; hold a legacy db.Conn lease, start bounded DB.Conn acquisition, discard the held lease with ErrBadConn and block replacement native Open. Require original caller physical deadline, Busy, retained operation admission through native Open and Close, no repeated work, no initialization SQL after original expiry, then finite release and safe recovery with configured busy_timeout and foreign_keys on replacements.',
    'N1: separately retain legitimate DefaultLockWait/unbounded replacements, cancellation-only explicit owner and zero-wait original work budget; background opener must do zero physical work and must never receive a foreign caller marker.',
    'N2: block actual prompt COUNT/Rows with a finite barrier beyond original operation context but before the separate until guard; require false/error and race-safe return, no late COMMIT, safe lease/disposal and unchanged exact eligibility guards.',
    'Use before/ exact snapshots for independent review-fix RED rather than reconstructing uncommitted source. Old64a trail RED is already proven and protected test hash remains unchanged. Prior native pre-ownership RED was never executed on a mismatched overlay and is not claimed here.',
    'Then one batch documentation pass for all native-operation/CLI/review fixes, fresh code review and sole-owner settled full suite.'
  ],
  documentation_implications: 'Coordinator should transcribe N1/N2 technical decisions into SRD and final architecture ownership docs: background database/sql replacement dial refusal/retry-to-explicit-owner, private context provenance including direct rotation reconciliation, and error-first captured-result publication. No product question or contract contradiction identified.',
  deviations_and_failures: [
    'Initial sandbox dependency inspection used rg, absent in sandbox; command exit127/make exit2 after Go primary source reads succeeded. Corrected to grep/sed in sandbox; successful raw source inspection saved. No source/test/build failure.',
    'One discovery command looked for nonexistent internal/store/cursor_prompt_test.go and rg returned2; actual CheckCursorPrompt coverage is in cursor_rotation_test.go and was run.',
    'Initial no-match discovery hashed decision_no_match_test.go, which is not the protected af61fc61 source. Corrected protected integrity to internal/store/permission.go; no file changed.',
    'A metadata read attempted minimal.patch before the report generator had created it and exited1; generation below creates and hashes the patch.',
    'Documentation and unit-test creation requirements of generic engineering guidance are delegated by explicit source-only dispatch; no docs/tests changed.'
  ],
  incomplete_source_work: [], questions: []
};
fs.writeFileSync(`${dir}/FINAL-report.json`, JSON.stringify(report, null, 2) + '\n');
console.log(JSON.stringify({checked_inputs: checked, changed: changed.map(v => v.path), receipts: receipts.map(r => ({name:r.name, exit:r.exit, passed_leaves:r.passed_leaves}))}));
