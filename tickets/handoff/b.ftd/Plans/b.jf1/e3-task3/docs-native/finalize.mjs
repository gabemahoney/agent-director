import fs from 'node:fs';
import crypto from 'node:crypto';
import {execFileSync} from 'node:child_process';
const task='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3',dir=`${task}/docs-native`;
const read=p=>fs.readFileSync(p,'utf8'),json=p=>JSON.parse(read(p));
const hash=s=>crypto.createHash('sha256').update(s).digest('hex');
const save=(p,v)=>fs.writeFileSync(`${dir}/${p}`,JSON.stringify(v,null,2)+'\n');
const docs=Object.keys(json(`${dir}/document-before.json`));
for(const p of docs){
 const head=execFileSync('git',['show',`HEAD:${p}`],{encoding:'utf8',maxBuffer:4e6});
 if(JSON.stringify(head.match(/```[^]*?```/g)??[])!==JSON.stringify(read(p).match(/```[^]*?```/g)??[]))throw Error('command fence changed '+p);
}
execFileSync('git',['diff','--check','--',...docs]);
save('document-after.json',Object.fromEntries(docs.map(p=>[p,{sha256:hash(read(p)),bytes:Buffer.byteLength(read(p)),lines:read(p).split('\n').length-1}])));
const references=['full-failure-assessment/FINAL-report.json','native-boundaries/FINAL-report.json','native-review-fix/FINAL-report.json','native-ownership-code-review.json','native-multiwaiter-fix/FINAL-report.json','native-lazy-code-review.json','native-lazy-validation/FINAL-report.json','SRD-NATIVE-OWNERSHIP-DECISIONS.md','SRD-LAZY-REPLACEMENT-DECISIONS.md','docs/FINAL-report.json','docs/architecture-review-provenance.json','docs-no-match/FINAL-report.json','settled-full/FINAL-report.json','archive-layout-correction.json','accidental-full-cancellation.json'];
save('input-report-hashes.json',Object.fromEntries(references.map(p=>[`${task}/${p}`,hash(read(`${task}/${p}`))])));
const advice={
 existing_inventory:`${task}/docs/postdoc-advice-inventory.json`,
 canonical_recovery_owner:'docs/caller-contract.md#permission-relay',canonical_recovery_links_unchanged:true,
 new_or_changed_commands:[],new_recovery_sequences:[],
 clarified_sequences:[
  {id:'original_deadline_and_zero_wait',delta:'Caller may return while entered database work retains the connection; no renewed positive deadline or automatic replay. Existing deliberate fresh-call and uncertain-COMMIT inspection procedures unchanged.'},
  {id:'cli_audit_teardown',delta:'Keep existing 1 s reply margin; includes separate fixed 500 ms process audit drain. Read structured result/error among stderr notices. Pending/failed drain preserves verdict exit code and may lose pending events at normal exit.',validation_owner:'Final Test Writer .yj/.zj',existing_evidence:'cmd/agent-director/decide_shutdown_linux_test.go; native-boundaries real CLI shutdown/ordinary exactly-once called receipts; pkg/ts-bun-client/src/internal/subprocessClient.ts scans error JSON among notices. No new literal advice pass claimed.'},
  {id:'client_lifetime',delta:'Go Client.Close releases its store; host process retains audit lifetime. No new public shutdown API, command or procedure.'}
 ],
 unchanged_recovery:['precommit_exhaustion_retry','unconfirmed_commit_inspect_before_retry','delivery_not_confirmed_poll'],
 literal_follow_status:'Final writer must review this delta against existing advice/CLI/Bun receipts; this docs role ran no tests.'
};
save('postdoc-advice-inventory-delta.json',advice);
save('FINAL-report.json',{
 role:'apiary_doc_writer_e3_task3_native_batch',
 result:'Batched native operation, final lazy replacement, captured-result and CLI lifetime documentation complete. Aggregate architecture chain independently verified and extended; source/test bytes and modes unchanged.',
 ticket_statuses:{'t3.jf1.r8.a8.48':'done -> active -> done','t3.jf1.r8.a8.ro':'done -> active -> done'},
 status_scope:'Only own documentation Subtasks; no Task/Epic/other Subtask statuses changed.',
 files_changed:[...docs,`${dir}/ (evidence scripts, small architecture baseline/delta patches, hash manifests, provenance, advice inventory and this report)`],
 edit_justifications:{
  'docs/architecture.md':'Require reuse of pre-open operation admission/retained owner, synchronized abandonment versus started COMMIT and result publication before cleanup. Distinguish legacy releaseConn path. Replace DSN initialization with actual caller-owned connector/lazy successful pool handoff, all borrower initialization methods/fresh replacement/Raw interfaces, zero admission versus SQL work and per-physical settings. Add error-first captured results/immutable callback binding and separate fixed CLI audit shutdown. Correct old empty-path DSN explanation made obsolete by actual connector change.',
  'docs/caller-contract.md':'Canonical bounded caller return versus entered native work/retained connection and separate CLI audit drain, original reply margin, stderr notices, unchanged verdict exit and pending normal-exit loss.',
  'docs/permissions.md':'Only narrow deadline boundary clarification, linking canonical timing/recovery owner.',
  'pkg/api/README.md':'Only actual caller-return/retained connection and Client.Close store-versus-process audit lifetime clarification.',
  'pkg/ts-bun-client/README.md':'Only original verb deadline versus separate subprocess audit teardown clarification; preserve existing parser/error contract and canonical owner.'
 },
 unchanged_docs:['README.md','docs/usage.md','docs/hooks.md','docs/operator-actions.md','docs/install.md','all generated references'],
 review_basis:'CLAUDE and all three doc guides; authoritative own Subtasks/current Task and governing Plan/Epic/PRD/SRD boundaries; named engineering, independent validation, source-review, decision and prior doc reports. Actual current connection_cleanup, lockwait, sqlite_connector, store opening, relay/cursor result publication, prompt/rotation, captured launch check, main shutdown and Bun error-envelope parser reviewed. Historical intermediate ErrBadConn-retry design explicitly superseded by final lazy handoff.',
 whole_architecture_review:json(`${dir}/architecture-review-provenance.json`),
 source_test_integrity:{before:'source-test-before.json',after:'source-test-after.json',unchanged_inputs:json(`${dir}/after-verification.json`).source_test_inputs_unchanged,source_test_files_edited:[],protected_trail_permission_connector_unchanged:true},
 advice_inventory_delta:advice,
 verification:['Evidence scripts only via make sandbox with fresh isolated HOME/XDG_CONFIG_HOME; strict complete architecture endpoints/every hunk, all four partition-report hashes, original-base hash/bytes/lines, prior reference hashes and source/test SHA256/modes checked before/after.','All five docs fenced examples unchanged against HEAD; scoped git diff --check passes. No tests/full suite run by this role.'],
 deviations_and_failures:[
  'Initial workspace file discovery listed two .claude SKILL.md path names; no .claude file was opened/read/hashed or executed. Later discovery excluded .claude.',
  'Several combined read-only outputs were truncated; focused relevant portions were reread. A discovery rg for absent proposed ticket files returned exit1. No source/test/admin mutation resulted.',
  'First evidence-only sandbox verification failed on object-key ordering in JSON equality of original-base metadata after all chain endpoint hashes passed. Preserved verification-before-failed.log; fixed to compare each named hash/byte/line field, reran successfully. No tests or source assertions changed.',
  'First documentation apply_patch rejected an unmatched acquisition-context line. Patch was atomic and made no changes; corrected patch applied successfully.'
 ],
 retained_validation_limits:[
  'Latest settled-full report remains a FAILED full run with nine leaves; later independent 194 targeted race and configured replacement checks plus latest 18 production lazy/COUNT targeted race leaves are scoped receipts, not a full-suite pass.',
  'Original old64a trail causal RED remains proven and protected current regression unchanged. Additional native pre-ownership/N1 original RED and N2 causal race RED remain unproven. Pre-N1b successful background handoff contract RED is proven; live-caller retry exhaustion was not reproduced.',
  'Latest lazy writer accidentally invoked make test-sandbox with CMD; it ignored CMD. Root stopped only verified owned process/container, writer observed exit143. Partial output is incomplete and invalid as full validation. Root moved archived before .go snapshots under _before with compatibility symlinks; original references/hash bytes retained. This is resolved archive layout, not an unresolved product build blocker.',
  'Historical09 causes UNKNOWN. Two later real full overruns isolate outer reads only, not inner native or scheduling causes. Quarantine does not force native interruption or host scheduling.',
  'Inherited A2-A6/B3-B8/C2-C5/D1-D2 and hooks/permissions guide-scope/recovery-placement debt remains deferred. Aggregate whole-base/every-delta coverage is not a personal fresh 22k-line read or clean-document certification.',
  'Collector remains authored/static only, 17 groups/172 cases; O1-O6 authenticated acceptance pending and not authorized. No spec/tla changes; zero formal/TLC/Java/parser/scheduler jobs.'
 ],
 boundaries:'No source/test/testplan/registry edits, repository host script/test/build/artifact execution, .claude contents access, real AD stores, credentials/login/logout/live Cursor, local Docker TESTPLAN groups/collectors/helpers, commits/pushes, subagents or outbound messages. Host git/text operations and own bees administration only; all repository evidence code ran make sandbox.',
 source_test_contradictions:[],questions_for_caller:[],
 incomplete_work:['No remaining assigned documentation edits. Fresh final Test Writer advice/quality .yj and sole settled full-suite .zj, PM whole-Task review, coordinator local commit and fresh exact-revision user scheduler handoff remain caller-owned. No extra documentation resync required absent a genuine further finding.']
});
console.log(JSON.stringify({result:'PASS',docs:docs.length,finalArchitecture:json(`${dir}/architecture-review-provenance.json`).final,report:`${dir}/FINAL-report.json`}));
