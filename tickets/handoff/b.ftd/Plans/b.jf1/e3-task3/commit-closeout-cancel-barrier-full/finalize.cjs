const fs=require('fs'), crypto=require('crypto');
const d='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/commit-closeout-cancel-barrier-full/';
const read=p=>fs.readFileSync(d+p,'utf8');
const json=(p,v)=>fs.writeFileSync(d+p,JSON.stringify(v,null,2)+'\n',{mode:0o600});
const hash=p=>crypto.createHash('sha256').update(fs.readFileSync(d+p)).digest('hex');
fs.chmodSync(d,0o700);
const log=read('full.log').replace(/\x1b\[[0-9;]*m/g,'');
const exit=Number(read('make-exit.txt').trim());
const lines=log.split('\n');
const passed=lines.filter(l=>/^ok\s+\S+\s+/.test(l));
const failed=lines.filter(l=>/^FAIL\s+\S+\s+/.test(l));
const none=lines.filter(l=>/^\?\s+\S+\s+\[no test files\]/.test(l));
const cached=passed.filter(l=>l.includes('(cached)'));
const bun={};
for(const [key,re] of Object.entries({pass:/^\s*(\d+) pass\s*$/m, fail:/^\s*(\d+) fail\s*$/m, expect_calls:/^\s*(\d+) expect\(\) calls\s*$/m, files:/Ran \d+ tests across (\d+) files/, runtime_seconds:/Ran \d+ tests across \d+ files\. \[([\d.]+)s\]/})) {
  const m=log.match(re); bun[key]=m?Number(m[1]):null;
}
const comparison=JSON.parse(read('input-comparison.json'));
const run=JSON.parse(read('run.json'));
Object.assign(run,{start:read('start.txt').trim(),end:read('end.txt').trim(),time_provenance:'UTC datetime receipts from sanitized launcher',make_exit:exit,removed_environment_names:read('removed-environment-names.txt').trim().split('\n').filter(Boolean)});
json('run.json',run);
const failures=lines.flatMap((l,i)=>/^--- FAIL:|^\s+--- FAIL:/.test(l)?[{test_line:l,following:lines.slice(i+1,i+9)}]:[]);
json('package-results.json',{passed,cached_passed:cached,fresh_passed:passed.filter(l=>!cached.includes(l)),failed,no_test_files:none});
const report={task:'t2.jf1.r8.a8',role:'sole cancellation barrier repair and full unit owner',ticket_status_updates:[],commits:[],command:['make','test-sandbox'],run_count:1,result:exit===0?'passed':'failed',make_exit:exit,go_exit:failed.length?1:(passed.length?0:null),go_exit_evidence:'Inferred from uninstrumented Go package records and canonical recipe result; no wrapper inserted.',go_packages:{total_package_records:passed.length+failed.length+none.length,passed:passed.length,failed:failed.length,no_test_files:none.length,cached_passed:cached.length,fresh_passed:passed.length-cached.length},bun_exit:bun.fail===0&&bun.pass!==null?0:(bun.fail>0?1:null),bun_summary:bun,failures,input_hashes_unchanged:comparison.unchanged,input_changes:comparison.changes,hash_proof:{input_manifest_before_sha256:hash('input-manifest-before.json'),input_manifest_after_sha256:hash('input-manifest-after.json'),full_log_sha256:hash('full.log'),frozen_historical_matches:5,changed_absolute_deadline_current_sha256:comparison.absolute_deadline_current_sha256},raw_log_mode:'0o'+(fs.statSync(d+'full.log').mode&0o777).toString(8),canonical_input_count:run.canonical_input_count,canonical_new_untracked_test_included:true,actual_go_source_count:JSON.parse(read('go-compiled-source-inventory.json')).length,historical_snapshot_go_paths_excluded_from_compiled_inventory:JSON.parse(read('snapshot-go-excluded.json')).length,files_changed:['internal/cursorhost/session_capacity_linux_test.go', 'evidence artifacts under commit-closeout-cancel-barrier-full'],targeted:{exit:0,leaves:6,package_elapsed_seconds:8.483,command:'make sandbox CMD=umask 022; unset TMPDIR; gofmt -w internal/cursorhost/session_capacity_linux_test.go; git diff --check; go test ./internal/cursorhost -run ^TestCursorWaitingMailboxByteBoundaries$ -count=1 -v -timeout 90s'},minimal_delta:'Added encoding/json import and bounded exact peer-receipt barrier for session/cancel with params.sessionId synthetic-session-1; immediately release HOLD; unchanged recovery wait; moved intact each-byte-reservation-cancelled wait after recovery. Prior real-admission refill barrier retained. No source/docs edits or deadline changes.',deviations:['Initial host source/evidence/metadata reads preceded sandbox-only inspection. No repository test or built binary ran on host. No .claude paths read/copied/hashed.'],integration:'not dispatched; not run',incomplete_work:exit===0?[]:['Full suite failed; evidence preserved without retry or fix.'],questions:[],limitations:['No historical COMMIT root cause or repair claim.','Nonverbose Go output reports package outcomes, not individual passing/skipped test counts.','Cached Go packages are identified explicitly; no cache-forcing option was used.']};
if(!comparison.unchanged) report.incomplete_work.push('Canonical inputs changed during full run.');
json('test-writer-report.json',report);
console.log(JSON.stringify(report,null,2));
