import fs from 'node:fs';
import crypto from 'node:crypto';
import {execFileSync} from 'node:child_process';

const dir = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/docs';
const task = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-task3';
const prior = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-task2/docs';
const read = path => fs.readFileSync(path, 'utf8');
const json = path => JSON.parse(read(path));
const hash = value => crypto.createHash('sha256').update(value).digest('hex');
const meta = value => ({sha256: hash(value), lines: value.split('\n').length - 1, bytes: Buffer.byteLength(value)});
const assert = (ok, message) => { if (!ok) throw Error(message); };
const save = (path, value) => fs.writeFileSync(`${dir}/${path}`, JSON.stringify(value, null, 2) + '\n');
const git = args => execFileSync('git', args, {encoding: 'utf8', maxBuffer: 10 * 1024 * 1024});

// Hashes cover complete endpoints; every context and added line must also match.
function reversePatch(endpoint, patch) {
  assert(endpoint.endsWith('\n'), 'endpoint lacks newline');
  const lines = endpoint.split('\n'); lines.pop();
  const hunks = []; let h;
  for (const line of patch.split('\n')) {
    const m = line.match(/^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/);
    if (m) { h = {start: Number(m[3])-1, old: [], next: [], oc: Number(m[2] ?? 1), nc: Number(m[4] ?? 1)}; hunks.push(h); continue; }
    if (!h) continue;
    if (line.startsWith(' ')) { h.old.push(line.slice(1)); h.next.push(line.slice(1)); }
    else if (line.startsWith('-')) h.old.push(line.slice(1));
    else if (line.startsWith('+')) h.next.push(line.slice(1));
  }
  assert(hunks.length > 0, 'empty patch');
  for (const h of hunks.reverse()) {
    assert(h.old.length === h.oc && h.next.length === h.nc, 'hunk length mismatch');
    assert(JSON.stringify(lines.slice(h.start, h.start+h.next.length)) === JSON.stringify(h.next), 'hunk byte mismatch');
    lines.splice(h.start, h.next.length, ...h.old);
  }
  return lines.join('\n') + '\n';
}

const inherited = json(`${prior}/architecture-review-provenance.json`);
for (const ref of [inherited.prior_report, inherited.prior_provenance, inherited.prior_document_hashes])
  assert(hash(read(ref.path)) === ref.sha256, 'inherited reference mismatch ' + ref.path);
const base = git(['show', 'HEAD:docs/architecture.md']);
assert(hash(base) === 'b2dc75bf87725ac756abcdf596cad3e237b4714e45f2611521c5bd45c994dab0', 'unexpected Task3 base');
assert(hash(base) === inherited.final.sha256 && meta(base).lines === 22358 && meta(base).bytes === 1544650, 'Task2 endpoint mismatch');
const verified = [];
let reconstructed = base;
for (const c of [...inherited.chain].reverse()) {
  assert(hash(reconstructed) === c.to, 'chain target mismatch');
  const patch = read(c.patch);
  assert(hash(patch) === c.patch_sha256, 'inherited patch hash mismatch ' + c.patch);
  reconstructed = reversePatch(reconstructed, patch);
  assert(hash(reconstructed) === c.from, 'chain origin mismatch');
  verified.unshift({...c, independently_reverified_complete_endpoints_and_all_hunks: true});
}
assert(hash(reconstructed) === inherited.original_base.sha256 && meta(reconstructed).lines === 22213 && meta(reconstructed).bytes === 1534189, 'reviewed whole-base mismatch');
let last = 0;
const partitions = inherited.partition_reports.map(p => {
  assert(hash(read(p.path)) === p.sha256, 'partition mismatch ' + p.path);
  assert(p.lines[0] === last+1, 'partition gap'); last = p.lines[1];
  return {...p, independently_hash_verified: true};
});
assert(last === 22213, 'incomplete base coverage');
const final = read('docs/architecture.md');
const delta = git(['diff', '--no-ext-diff', '--', 'docs/architecture.md']);
assert(hash(reversePatch(final, delta)) === hash(base), 'own delta endpoint mismatch');
fs.writeFileSync(`${dir}/architecture-delta.patch`, delta);
verified.push({from: hash(base), to: hash(final), patch: `${dir}/architecture-delta.patch`, patch_sha256: hash(delta), independently_reverified_complete_endpoints_and_all_hunks: true, review: 'Every Task3 hunk and its changed context personally read against actual source and settled reports; existing unchanged whole base inherited.'});
const deferred = ['A2-A6', 'B3-B8', 'C2-C5', 'D1-D2', 'Inherited hooks/permissions guide-scope and recovery-placement normalization'];
const provenance = {
  method: 'Inherited four-partition whole-base review plus independently verified every complete patch and endpoint through Task1, Task2 and this personally reviewed Task3 delta/context. Aggregate coverage, not a fresh personal 22k-line read.',
  original_base: meta(reconstructed), task3_base: meta(base), final: meta(final), partition_reports: partitions, chain: verified,
  whole_final_aggregate_covered: true, personal_whole_file_read: false, clean_document_claim: false, deferred,
  prior_report: {path: `${prior}/FINAL-report.json`, sha256: hash(read(`${prior}/FINAL-report.json`))},
  prior_provenance: {path: `${prior}/architecture-review-provenance.json`, sha256: hash(read(`${prior}/architecture-review-provenance.json`))},
  prior_document_hashes: {path: `${prior}/document-hashes.json`, sha256: hash(read(`${prior}/document-hashes.json`))},
  inherited_reference_hashes_verified: true
};
save('architecture-review-provenance.json', provenance);

const settled = json(`${task}/acceptance-review-fix/source-test-integrity.json`);
const preserved = {...settled.settled_source_sha256, ...settled.tests_sha256, ...settled.existing_harness_sha256};
const collector = json(`${task}/collector/FINAL-report.json`);
const fixes = json(`${task}/acceptance-review-fix/FINAL-report.json`);
Object.assign(preserved, collector.file_sha256, fixes.file_sha256);
Object.assign(preserved, json(`${task}/acceptance-review-fix/input-hashes.json`));
// Current repair inputs override superseded collector-authoring hashes.
Object.assign(preserved, fixes.file_sha256);
const integrity = Object.entries(preserved).map(([path, sha256]) => {
  assert(!path.split('/').includes('.claude'), 'forbidden path');
  assert(hash(read(path)) === sha256, 'source/test/collector drift ' + path);
  return {path, sha256, unchanged: true};
});
assert(Object.keys(settled.settled_source_sha256).length === 8, 'missing source manifest');
assert(hash(read('pkg/api/decide_trail_deadline_test.go')) === '032950e86d9919e2c48ae79189751e36f8875312a78bf1410973954fdde86d88', 'causal regression changed');
save('source-test-integrity.json', {all_match: true, source_count: 8, current_test_files: Object.keys(settled.tests_sha256).length, files: integrity});

const files = ['docs/caller-contract.md', 'docs/permissions.md', 'pkg/api/README.md', 'pkg/ts-bun-client/README.md', 'docs/architecture.md'];
const documents = files.map(path => {
  const before = git(['show', `HEAD:${path}`]), after = read(path);
  assert(JSON.stringify(before.match(/```[^]*?```/g) ?? []) === JSON.stringify(after.match(/```[^]*?```/g) ?? []), 'changed fenced command block ' + path);
  return {path, before_sha256: hash(before), ...meta(after), fenced_blocks_unchanged: true};
});
save('document-hashes.json', documents);
const noChange = ['docs/usage.md', 'docs/hooks.md', 'docs/operator-actions.md'];
for (const path of noChange) assert(hash(read(path)) === hash(git(['show', `HEAD:${path}`])), 'unassigned doc change ' + path);

const caller = read('docs/caller-contract.md');
const sequences = [
  {id: 'precommit_exhaustion_retry', exact_prose: '- `ErrStoreBusy`: the bound ran out before the verdict could commit.\n  This can include store acquisition or SQL work; it does not establish\n  that another process or call held the store. Retry only as a new,\n  deliberate call with a fresh budget.', literal_follow_scope: 'Observe actual precommit refusal/no durable verdict, then deliberately make a new decision call with a fresh budget and inspect the real request. Preserve first-writer wins and independent holds. Reuse existing API/store boundary fixtures and public owners; no inferred competing holder or automatic retry.'},
  {id: 'original_deadline_and_zero_wait', exact_prose: 'The positive `--max-wait-ms` bound covers SQL, cleanup and reporting under\nthe same original deadline. Once it expires, no new SQL starts, even if the\nstore is free. Zero permits immediately available work within a fixed\none-second allowance, with no lock retries; it does not mean zero execution\ntime. Keep the one-second reply margin above. These bounds depend on the\nhost scheduling AD and on interruptible database and filesystem operations.', literal_follow_scope: 'Use the actual existing bounded APIs, original deadline and zero-wait cases; do not renew an expired positive budget or widen the original assertions. Separate cooperative cancellation from uninterruptible-native/descheduling limits. This adds no command or configuration.'},
  {id: 'unconfirmed_commit_inspect_before_retry', exact_prose: 'An `ErrInternal` reporting an unconfirmed commit is different from a\nprecommit refusal: the verdict may already be recorded. Inspect the durable\nrequest with `get-permission` and its `request_token` before deciding whether\nto retry. Do not infer that nothing was written, resend an uncertain approval,\nor automatically replay the call. Cancellation does not undo an already\ncommitted verdict. After a confirmed commit, later deadline exhaustion keeps\nthe verdict and returns the available delivery facts; follow `not_confirmed`\nas above. A decision or delivery response is not a receipt that its audit\nevents have reached durable storage.', literal_follow_scope: 'Create real uncertain COMMIT response through existing actual SQLite boundary, inspect the actual durable request using the existing public get-permission/GetPermission owner, and make no replay of the committed allow. Follow the retained not_confirmed polling sequence where relevant; delayed audit reporting must not undo verdict or become a durability claim. Existing standalone Store-level commit uncertainty plus invented serialized errors is not by itself literal execution of caller recovery.'}
].map(x => {
  assert(caller.includes(x.exact_prose), 'advice text mismatch ' + x.id);
  return {...x, path: 'docs/caller-contract.md', heading: 'Permission relay'};
});
save('postdoc-advice-inventory.json', {owner: 't3.jf1.r8.a8.6f', status: 'PENDING literal-follow behavior validation; documentation authoring is complete, not advice execution', new_or_changed_commands: [], sequences, unchanged_sequences: 'Existing request-token inspection, not_confirmed polling, live-row cleanup, pane fallback and Task1/2 rotation failure advice remain their existing owners. Changed permission/API summaries link the canonical caller contract.', boundary: 'Follow advice literally with isolated existing behavioral/public fixtures; no doc parsing/metatests, manufactured recovery commands, credentials/live Cursor or collector execution.'});
const reportPaths = ['deadline-red/FINAL-report.json', 'deadline-engineer/FINAL-report.json', 'deadline-code-review.json', 'deadline-green/FINAL-report.json', 'deadline-review-fix/FINAL-report.json', 'public/FINAL-report.json', 'collector/FINAL-report.json', 'PM-initial-review.json', 'acceptance-review-fix/FINAL-report.json', 'SRD-DEADLINE-DECISIONS.md', 'SRD-DEADLINE-REVIEW-DECISION.md', 'start/mapping-correction.json'];
save('review-input-hashes.json', reportPaths.map(path => ({path: `${task}/${path}`, sha256: hash(read(`${task}/${path}`))})));
git(['diff', '--check', '--', ...files]);
save('FINAL-report.json', {
  role: 'apiary_doc_writer_e3_task3', result: 'One settled documentation authoring pass complete; exact whole-base/every-delta architecture chain verified through Task3. Literal advice remains assigned downstream, not passed.',
  ticket_statuses: {'t3.jf1.r8.a8.48': 'defined -> active -> done; literal-follow .6f pending explicitly', 't3.jf1.r8.a8.ro': 'defined -> active -> done; aggregate provenance verified'},
  status_scope: 'Only own .48/.ro; no Task/Epic/other Subtask statuses changed.', files_changed: files,
  edit_justifications: {
    'docs/caller-contract.md': 'PM batch and mandatory source repair replace competing-holder-only ErrStoreBusy/retry explanation, distinguish unconfirmed COMMIT and committed outcome, explain original/zero budgets and audit receipt limits; canonical changed recovery owner.',
    'docs/permissions.md': 'Narrow demonstrated contradiction: old expiry paragraph allowed new SQL after positive deadline and stated retry recovery locally. Corrected and linked caller owner; inherited unrelated normalization deferred.',
    'pkg/api/README.md': 'Old MaxWait paragraph incorrectly limited exhaustion to competing holders. Accurate API summary and canonical error/recovery link.',
    'pkg/ts-bun-client/README.md': 'Same demonstrated competing-holder-only error table contradiction; preserve sentinel meaning and canonical recovery link.',
    'docs/architecture.md': 'Actual original-context Store/decorator/SQL/cleanup responsibilities and reuse, bounded trail ownership/status/drain versus durability, Client closed-state handoff and actual public/collector helper responsibilities.'
  },
  unchanged_docs: {'docs/usage.md': 'Public interaction and Task1/2 pane behavior unchanged by settled Task3; no new workflow.', 'docs/hooks.md': 'No demonstrated changed reporting/authority contract; existing E2 proofs retained.', 'docs/operator-actions.md': 'No new demonstrated human-only procedure; caller request inspection is automated-caller scope.'},
  whole_architecture_review: provenance,
  review_basis: 'Read authoritative Task and full own doc Subtasks, approved PRD and relevant SRD/current decisions, root CLAUDE and all three docs guides, all twelve named final/review/decision/mapping inputs. Read actual eight source owners and relevant store/trail/API/public/collector helper boundaries; inherited four partition reports and Task2 provenance/delta generator/doc hashes examined. Historical pending report statements are superseded by settled public/acceptance-review-fix reports.',
  source_test_integrity: {manifest: 'source-test-integrity.json', all_eight_sources: true, causal_regression_unchanged: true, public_collector_inputs_unchanged: true, current_tests_hashed: Object.keys(settled.tests_sha256).length},
  verification: ['make sandbox CMD=...docs/verify-evidence.mjs: complete endpoint/hunk/partition/input/source/test/document checks only', 'git diff --check scoped to all five edited docs', 'All fenced command blocks unchanged; no executable advice added'],
  commands: [{command: `make sandbox CMD='bun ${dir}/verify-evidence.mjs'`, log: 'verification.log', scope: 'Evidence only; no tests, collectors, helpers or built repo binaries executed'}],
  advice_handoff: {owner: 't3.jf1.r8.a8.6f', inventory: 'postdoc-advice-inventory.json', sequences: sequences.map(x=>x.id), status: 'PENDING; no literal-follow pass claim'},
  failures: [{command: 'bees show --help', result: 'Invalid subcommand; corrected to show-ticket --ids before own status updates. Read-only administrative error, no mutations.'}, {command: 'First sandbox verify-evidence.mjs invocation', log: '01-verification-failed.log', result: 'Evidence assembly TypeError: used source_sha256 instead of settled_source_sha256. Original failed receipt retained; corrected manifest key and explicitly verify all eight source hashes. No tests/source/docs assertions changed or tests rerun.'}],
  historical_receipts: 'All raw failed source/test/integrity receipts preserved. deadline-review-fix original empty-input integrity pipeline is INVALID, not a pass; acceptance-review-fix initial registry-baseline mismatch remains failed evidence setup superseded by corrected receipt. This doc verification uses current independent manifests.',
  srd_decisions_for_caller: 'Adopts existing SRD deadline implementation and setup-expiry decisions plus settled D1/public and P1-P3 acceptance dispositions. No new technical/product decision; no SRD ticket edits.',
  source_test_contradictions: [], deviations: ['Narrow docs/permissions.md deadline correction is necessary for the actual source change; broader inherited normalization remains deferred.'],
  limits: ['Aggregate whole-final architecture coverage, not a fresh personal 22k-line read or clean-document certification; inherited deferred findings retained.', 'Advice .6f, quality .yj and sole full-suite .zj remain after this pass; no tests/full suite executed here.', '15 SQL/cleanup/trail cases plus 119 preservation and 13 public race leaves are prior scoped receipts, not full-suite or collector pass.', 'Actual tmux/CLI/MCP/Go/built public Bun used synthetic ACP; installed Bun collector steps are authored only and require locked-dependency network access. Collector runtime is not passed; authenticated human O1-O6 remain pending.', 'Historical09 timing cause UNKNOWN; exact old64a causal RED and unchanged repaired-source GREEN do not reconstruct its cause.', 'No spec/tla change or new model input: zero formal jobs. No scheduler/collector/helper/TLC/Java/parser execution.'],
  execution_boundary: 'Docs/evidence only, workspace-scoped discovery. No .claude access, real AD stores, credentials/login/logout/live Cursor, source/test/testplan/registry edits, host repository scripts/build/tests/artifacts, full suite, Docker groups/collector helpers, scheduler submissions, commits/pushes, delegation or outbound messages. All evidence script execution via make sandbox.',
  incomplete_work: ['No remaining assigned doc edits. Literal .6f validation, final .yj quality, settled .zj full suite, coordinator commit and user exact-revision scheduler gate remain outstanding.'], questions_for_caller: []
});
console.log(JSON.stringify({result: 'PASS', source_files: 8, tests: Object.keys(settled.tests_sha256).length, preserved_inputs: integrity.length, architecture: meta(final), verified_chain_steps: verified.length, partitions: partitions.length, changed_documents: documents.length, advice_sequences_pending: sequences.length}));
