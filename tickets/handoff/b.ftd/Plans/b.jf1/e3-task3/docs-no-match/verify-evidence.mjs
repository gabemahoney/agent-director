import fs from 'node:fs';
import crypto from 'node:crypto';
import {execFileSync} from 'node:child_process';

const dir = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/docs-no-match';
const task = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-task3';
const read = p => fs.readFileSync(p, 'utf8');
const json = p => JSON.parse(read(p));
const hash = s => crypto.createHash('sha256').update(s).digest('hex');
const assert = (v, m) => { if (!v) throw Error(m); };
const meta = s => ({sha256: hash(s), bytes: Buffer.byteLength(s), lines: s.split('\n').length - 1});
const save = (name, v) => fs.writeFileSync(`${dir}/${name}`, JSON.stringify(v, null, 2) + '\n');
const beforeHash = '9593447ca9fe0640268803d42c07f5cbe9e5865be306c2a9b91388307ca7b509';
function reversePatch(endpoint, patch) {
  assert(endpoint.endsWith('\n'), 'missing final newline');
  const lines = endpoint.slice(0,-1).split('\n'), hunks = []; let h;
  for (const line of patch.split('\n')) {
    const m = line.match(/^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/);
    if (m) { h = {start: +m[3]-1, old: [], next: [], oc: +(m[2] ?? 1), nc: +(m[4] ?? 1)}; hunks.push(h); continue; }
    if (!h) continue;
    if (line.startsWith(' ')) { h.old.push(line.slice(1)); h.next.push(line.slice(1)); }
    else if (line.startsWith('-')) h.old.push(line.slice(1));
    else if (line.startsWith('+')) h.next.push(line.slice(1));
  }
  assert(hunks.length, 'empty patch');
  for (const h of hunks.reverse()) {
    assert(h.old.length === h.oc && h.next.length === h.nc, 'invalid hunk counts');
    assert(JSON.stringify(lines.slice(h.start,h.start+h.nc)) === JSON.stringify(h.next), 'hunk endpoint/context mismatch');
    lines.splice(h.start,h.nc,...h.old);
  }
  return lines.join('\n') + '\n';
}
const initial = json(`${task}/docs/architecture-review-provenance.json`);
const initialReport = json(`${task}/docs/FINAL-report.json`);
assert(JSON.stringify(initialReport.whole_architecture_review) === JSON.stringify(initial), 'initial provenance/report mismatch');
assert(initial.final.sha256 === beforeHash, 'initial endpoint mismatch');
const refs = [initial.prior_report, initial.prior_provenance, initial.prior_document_hashes];
const prior = json(initial.prior_provenance.path);
refs.push(prior.prior_report, prior.prior_provenance, prior.prior_document_hashes);
for (const r of refs) assert(hash(read(r.path)) === r.sha256, 'reference mismatch '+r.path);
let current = read('docs/architecture.md');
if (process.argv[2] === 'after') {
  const old = [
    '   no bound meets no new error name (problem 5 (a)). A write that matched',
    '   nothing is named by `notWritten` (absent, decided or closed meanwhile,',
    '   Spawn finished meanwhile, a pane answer recorded meanwhile).'
  ];
  const next = [
    '   no bound meets no new error name (problem 5 (a)). A guarded UPDATE that',
    '   matches nothing returns `sql.ErrNoRows` through `inWriteTx`, causing',
    '   rollback without COMMIT. `DecideRelayRequest` maps that sentinel to',
    '   `(false, nil)` only when the helper has not also classified precommit',
    '   expiry as `ErrStoreBusy`; expired cleanup retains the shared quarantine',
    '   path. A timely no-match is named by `notWritten` through follow-up reads',
    '   (absent, decided or closed meanwhile, Spawn finished meanwhile, a pane',
    '   answer recorded meanwhile). Potentially written verdicts still attempt',
    '   COMMIT and retain unconfirmed-outcome handling on a COMMIT error.'
  ];
  const lines = current.slice(0,-1).split('\n');
  const start = lines.indexOf(next[0]);
  assert(start >= 3 && JSON.stringify(lines.slice(start,start+next.length)) === JSON.stringify(next), 'unexpected new delta');
  const prefix = lines.slice(start-3,start).map(s=>' '+s), suffix = lines.slice(start+next.length,start+next.length+3).map(s=>' '+s);
  const patch = ['--- a/docs/architecture.md','+++ b/docs/architecture.md',`@@ -${start-2},${old.length+6} +${start-2},${next.length+6} @@`,...prefix,...old.map(s=>'-'+s),...next.map(s=>'+'+s),...suffix].join('\n')+'\n';
  fs.writeFileSync(`${dir}/architecture-delta.patch`,patch);
}
if (process.argv[2] === 'after') current = reversePatch(current, read(`${dir}/architecture-delta.patch`));
assert(hash(current) === beforeHash, 'unexpected pass baseline');
let endpoint = current;
for (const c of [...initial.chain].reverse()) {
  assert(hash(endpoint) === c.to, 'chain target mismatch');
  const p = read(c.patch);
  assert(hash(p) === c.patch_sha256, 'patch hash mismatch');
  endpoint = reversePatch(endpoint,p);
  assert(hash(endpoint) === c.from, 'chain source mismatch');
}
assert(JSON.stringify(meta(endpoint)) === JSON.stringify({sha256: initial.original_base.sha256, bytes: initial.original_base.bytes, lines: initial.original_base.lines}), 'whole base mismatch');
let last = 0;
for (const p of initial.partition_reports) {
  assert(hash(read(p.path)) === p.sha256, 'partition report hash mismatch');
  assert(p.lines[0] === last+1, 'partition gap'); last = p.lines[1];
}
assert(last === initial.original_base.lines, 'incomplete partitions');
const baseline = json(`${task}/no-match-validation/input-manifest-pre-corrected-green.json`);
const integrity = [];
for (const [path,v] of Object.entries(baseline)) {
  assert(!path.split('/').includes('.claude'), 'forbidden path');
  if (path === 'docs/architecture.md' && process.argv[2] === 'after') continue;
  assert(hash(fs.readFileSync(path)) === v.sha256, 'input drift '+path);
  integrity.push({path, sha256: v.sha256});
}
assert(hash(read('pkg/api/decide_trail_deadline_test.go')) === '032950e86d9919e2c48ae79189751e36f8875312a78bf1410973954fdde86d88', 'protected regression drift');
const sources = json(`${task}/no-match-validation/FINAL-report.json`).integrity.sources;
assert(Object.keys(sources).length === 8, 'missing eight source owners');
for (const [p,v] of Object.entries(sources)) assert(hash(read(p)) === v.sha256,'settled source drift '+p);
const inputs = ['docs/FINAL-report.json','docs/architecture-review-provenance.json','legacy-commit-assessment/FINAL-report.json','no-match-code-review.json','no-match-validation/FINAL-report.json','contract-final-tests/FINAL-report.json','api-deadline-assessment/FINAL-report.json','SRD-NO-MATCH-TRANSACTION-DECISION.md','SRD-DEADLINE-TEST-CONTRACT-DECISION.md'].map(p => ({path: `${task}/${p}`,sha256: hash(read(`${task}/${p}`))}));
save(`${process.argv[2]}-verification.json`, {result:'PASS', architecture:meta(read('docs/architecture.md')), inherited_steps:initial.chain.length, partition_reports:initial.partition_reports, reference_hashes_verified:refs, all_hunks_and_complete_endpoints_verified:true, unchanged_inputs:integrity.length, eight_sources:sources, protected_regression_unchanged:true, input_report_hashes:inputs});
if (process.argv[2] === 'after') {
  const final = read('docs/architecture.md'), patch = read(`${dir}/architecture-delta.patch`);
  assert(JSON.stringify(final.match(/```[^]*?```/g) ?? []) === JSON.stringify(current.match(/```[^]*?```/g) ?? []), 'command fences changed');
  execFileSync('git',['diff','--check','--','docs/architecture.md']);
  save('FINAL-report.json', {
    role:'apiary_doc_writer_e3_task3_no_match_batch', result:'Narrow no-match architecture clarification complete; strict inherited whole-base/every-delta chain independently verified and extended.',
    ticket_statuses:{'t3.jf1.r8.a8.ro':'done -> active -> done'}, status_scope:'Only own architecture Subtask; no other statuses changed.',
    files_changed:['docs/architecture.md',`${dir}/verify-evidence.mjs`,`${dir}/architecture-delta.patch`,`${dir}/before-verification.json`,`${dir}/after-verification.json`,`${dir}/FINAL-report.json`],
    review_basis:'Root CLAUDE, all three doc guides, authoritative own Subtask/Task/Epic/Plan/PRD/SRD and both current SRD decisions; complete named reports read, new no-match source and architecture context personally inspected. Test-only core-clock seam/COMMIT observer changes introduce no documentation contract.',
    edit:'DecideRelayRequest guarded no-match returns sql.ErrNoRows through inWriteTx to rollback without COMMIT; timely outcome maps to false,nil for existing race-aware follow-up classification. Expired precommit preserves ErrStoreBusy and shared safe cleanup. Potentially written COMMIT ambiguity remains intact.',
    whole_architecture_review:{...initial, method:'Inherited four-partition whole-base review plus independently reverified every prior complete patch endpoint/hunk and personally reviewed new no-match delta/context; aggregate coverage, not a fresh personal whole-file read.', final:meta(final), chain:[...initial.chain,{from:beforeHash,to:hash(final),patch:`${dir}/architecture-delta.patch`,patch_sha256:hash(patch),independently_reverified_complete_endpoints_and_all_hunks:true,review:'Every new line and surrounding DecideRelayRequest/inWriteTx context personally reviewed against settled source and reports.'}],personal_whole_file_read:false,clean_document_claim:false},
    evidence:['before-verification.json','after-verification.json','verification-before.log','verification-after.log'],
    source_test_integrity:{unchanged_inputs:integrity.length,eight_sources_unchanged:true,protected_regression_unchanged:true,source_test_files_edited:[]},
    advice_inventory_delta:{new_or_changed_commands:[],new_or_changed_recovery_sequences:[],canonical_recovery_links_unchanged:true,existing_inventory:`${task}/docs/postdoc-advice-inventory.json`,reason:'Internal transaction clarification only; existing caller-contract inspection/retry advice remains authoritative. No new literal-follow work introduced; prior advice receipts retain their own scope.'},
    user_docs:'Unchanged; no demonstrated contradiction from this no-match change or test-only clock/core seam. No blanket README/permissions/hooks normalization.',
    verification:'Evidence only through make sandbox with fresh isolated HOME/stores/sockets/processes; strict chain/partition/input/source/test hashes plus host git diff --check. No tests/full suite executed by this role.',
    failures:[],deviations:[],source_test_contradictions:[],questions_for_caller:[],
    limits:['Inherited A2-A6/B3-B8/C2-C5/D1-D2 and hooks/permissions guide-scope/recovery-placement debt remains deferred; no clean whole-document claim.','Prior two full-suite failures remain retained; scoped 43 targeted race passes belong to no-match-validation, not this doc lane or a full pass. Historical09 causes UNKNOWN.','Collector 17 groups/172 cases authored/static only; authenticated O1-O6 pending and not authorized here.','No spec/tla changes; zero formal/TLC/Java/parser/scheduler jobs.'],
    boundaries:'No source/test edits, .claude access, real AD stores, credentials/login/logout/live Cursor, host repository script/test/build execution, local Docker TESTPLAN groups/collector/helper execution, scheduler submissions, commits/pushes, delegation or outbound messages.',
    incomplete_work:['No remaining assigned documentation work. Final postdoc Test Writer quality/full sandbox, PM/coordinator commit and exact-revision scheduler gate remain caller-owned.']
  });
}
console.log(JSON.stringify({result:'PASS',phase:process.argv[2],architecture:meta(read('docs/architecture.md')),verified_prior_steps:initial.chain.length,unchanged_inputs:integrity.length,eight_sources:8}));
