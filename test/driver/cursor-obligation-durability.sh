#!/usr/bin/env bash
# Distinct E2 process-loss cuts, using the shared synthetic ACP runtime and SQL wrapper.
set -euo pipefail
cut=$1
case_ticket=$2
source /work/source/test/driver/cursor-resume-life.sh
# Reuse its load barrier while retaining this composition's complete artifacts.
trap pc_cleanup EXIT
check_receipts="$case_dir/checks.txt"
od_finish() {
 pc_finish "$case_ticket" "$(jq -Rn '[inputs]' < "$check_receipts")" > "$case_dir/receipt.tmp"
 jq --arg cut "$cut" --argjson checks "$(jq -Rn '[inputs]' < "$check_receipts")" '.+{cut:$cut,checks:$checks}' "$case_dir/receipt.tmp" > "$case_dir/receipt.json"
 cat "$case_dir/receipt.json"
}

od_lock() {
 sql <<SQL &
BEGIN IMMEDIATE;
.shell touch '$case_dir/lock-ready'
.shell while [ ! -f '$case_dir/unlock' ]; do sleep 0.01; done
ROLLBACK;
SQL
 lock_owner=$!
 check writer.actual-lock wait_until test -f "$case_dir/lock-ready"
}
od_unlock() { touch "$case_dir/unlock"; wait "$lock_owner"; unset lock_owner; }
od_snapshot() {
 sql -readonly -json "SELECT store_id,connection_id,submission_id,record FROM cursor_inputs WHERE instance_id='$id' ORDER BY submission_id; SELECT store_id,connection_id,proof_id,record,hex(raw) AS raw_hex FROM cursor_proofs WHERE instance_id='$id' ORDER BY proof_id;"
}
od_input_is() { [[ $(sql "SELECT count(*) FROM cursor_inputs WHERE instance_id='$id' AND json_extract(record,'$.Text')='$prompt' AND json_extract(record,'$.State')='$1';") == 1 ]]; }
od_proof_count() { [[ $(sql "SELECT count(*) FROM cursor_proofs WHERE instance_id='$id';") == "$1" ]]; }
od_emit() {
 jq -cn --arg id "$1" --rawfile raw "$2" '{id:$id,raw:$raw}' > "$case_dir/events.tmp"
 mv "$case_dir/events.tmp" "$case_dir/events.json"
}
od_echoed() { ad read-pane --instance-id "$id" --n-lines 100 | jq -e --arg prompt "$prompt" '.pane|contains($prompt)' >/dev/null; }
od_load_seen() { frames | jq -se 'any(.[]; .method=="session/load")' >/dev/null; }

if [[ $cut == ipc-* ]]; then
 pc_start
 if [[ $cut == ipc-overflow ]]; then
  node - "$session" "$case_dir/completion.raw" <<'JS'
const fs=require('fs'),[session,path]=process.argv.slice(2);
fs.writeFileSync(path,Array.from({length:80},(_,n)=>JSON.stringify({jsonrpc:'2.0',method:'session/update',params:{sessionId:session,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'finite-notification-'+n}}}})+'\n').join(''));
JS
  od_emit ipc-burst "$case_dir/completion.raw"
  check ipc.bounded-channel-preserves-all wait_until pc_count_is startup_or_idle_update 80
  # The protocol unit companion holds consumption until its channel is full.
  # This shipped-process case also requires every complete payload in order;
  # a duplicate or replacement cannot hide behind an unchanged count.
  check ipc.complete-ordered-payloads node - "$case_dir/completion.raw" "$path" "$case_dir/notification-comparison.json" <<'JS'
const fs=require('fs'),assert=require('assert'),[wire,path,receipt]=process.argv.slice(2);
const expected=fs.readFileSync(wire,'utf8').trim().split('\n').map(line=>JSON.parse(line).params);
const observed=fs.readFileSync(path,'utf8').trim().split('\n').map(JSON.parse).filter(r=>r.kind==='startup_or_idle_update').map(r=>r.data.update);
assert.deepStrictEqual(observed,expected);
fs.writeFileSync(receipt,JSON.stringify({count:expected.length,expected,observed})+'\n');
JS
  check ipc.burst-no-prompt pc_prompts_is 0
  check ipc.burst-no-proof od_proof_count 0
  pc_terminal explicit-after-finite-burst
  check ipc.burst-live-positive wait_until pc_ready 1
  ad pause --instance-id "$id" >/dev/null
  od_finish
  exit 0
 fi
 wire_before=$(raw_size)
 if [[ $cut == ipc-malformed ]]; then
  printf '{"jsonrpc":"2.0",broken}\n' > "$case_dir/completion.raw"
 else
  printf '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":' > "$case_dir/completion.raw"
 fi
 od_emit ipc-first "$case_dir/completion.raw"
 ipc_sent() { tail -c "$(wc -c < "$case_dir/completion.raw")" "$case_dir/sent.jsonl" | cmp -s - "$case_dir/completion.raw"; }
 check ipc.actual-first-bytes wait_until ipc_sent
 if [[ $cut == ipc-delayed ]]; then
  check ipc.partial-no-prompt pc_prompts_is 0
  check ipc.partial-no-proof od_proof_count 0
  check ipc.partial-no-output test "$(pc_count startup_or_idle_update)" = 0
  printf '"%s","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"delayed-complete-event"}}}}\n' "$session" > "$case_dir/completion.raw"
  od_emit ipc-second "$case_dir/completion.raw"
  check ipc.complete-observed wait_until pc_count_is startup_or_idle_update 1
  check ipc.observation-no-authority pc_prompts_is 0
  check ipc.observation-no-proof od_proof_count 0
  pc_terminal explicit-after-delayed-event
  check ipc.live-positive wait_until pc_ready 1
  ad pause --instance-id "$id" >/dev/null
 else
  if [[ $cut == ipc-partial-eof ]]; then kill -KILL "$runtime"; fi
  check ipc.gateway-failed wait_gone "$gateway"
  check ipc.runtime-gone wait_gone "$runtime"
  check ipc.no-clean-end test "$(pc_count session_end)" = 0
  check ipc.no-input pc_prompts_is 0
  check ipc.no-proof od_proof_count 0
  check ipc.no-new-wire test "$(raw_size)" = "$wire_before"
 fi
 od_finish
 exit 0
fi

if [[ $cut == proof-* ]]; then
 relay_scenario permission
 jq '.waitForRelease=true' "$case_dir/scenario" > "$case_dir/scenario.tmp"
 mv "$case_dir/scenario.tmp" "$case_dir/scenario"
fi
pc_start
store_id=$(sql "SELECT value FROM store_meta WHERE key='store_id';")
prompt="durable-$cut"
if [[ $cut == input-before ]]; then
 od_lock
 pc_terminal "$prompt"
 check input.observed-before-crash wait_until od_echoed
 check input.not-persisted test "$(sql "SELECT count(*) FROM cursor_inputs WHERE instance_id='$id';")" = 0
 check input.no-wire pc_prompts_is 0
elif [[ $cut == input-issued || $cut == input-completed ]]; then
 if [[ $cut == input-issued ]]; then printf '{"waitForRelease":true}\n' > "$case_dir/scenario"; fi
 pc_terminal "$prompt"
 check input.actual-receipt wait_until pc_prompts_is 1
 state=issued
 if [[ $cut == input-completed ]]; then state=completed; fi
 check input.committed-state wait_until od_input_is "$state"
elif [[ $cut == proof-before || $cut == proof-committed ]]; then
 pc_terminal "$prompt"
 check proof.request-admitted wait_until request_count_is 1
 token=$(sql "SELECT request_token FROM permission_requests WHERE instance_id='$id';")
 relay_decide "$token" allow >/dev/null
 check proof.actual-answer wait_until reply_count_is 1
 # Keep the exact params value, including whitespace, escaping and null, separate
 # from the envelope so comparing the stored BLOB does not normalize the source.
 node - "$case_dir/scenario" "$case_dir/proof.raw" "$case_dir/completion.raw" <<'JS'
const fs=require('fs'),[src,proof,wire]=process.argv.slice(2),s=JSON.parse(fs.readFileSync(src));
const raw='{ "sessionId" : '+JSON.stringify(s.sessionId)+', "update" : {"sessionUpdate":"tool_call_update", "toolCallId":'+JSON.stringify(s.requests[0].params.toolCall.toolCallId)+',"status":"completed","rawOutput":{"exitCode":0, "stdout":"exact\\u0020bytes", "stderr":"", "optional":null}}}';
const barrier={jsonrpc:'2.0',method:'session/update',params:{sessionId:s.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'proof-processing-barrier'}}}};
fs.writeFileSync(proof,raw);fs.writeFileSync(wire,'{"jsonrpc":"2.0","method":"session/update","params":'+raw+'}\n'+JSON.stringify(barrier)+'\n');
JS
 if [[ $cut == proof-before ]]; then od_lock; fi
 od_emit observed-proof "$case_dir/completion.raw"
 proof_sent() { grep -q 'exact\\u0020bytes' "$case_dir/sent.jsonl"; }
 check proof.actual-source wait_until proof_sent
 # The protected proof record establishes gateway consumption. The following
 # exact frame cannot be recorded until execution/prove returned for that
 # proof. For proof-before, the external writer still holds BEGIN IMMEDIATE
 # through both observations and the subsequent SIGKILL.
 proof_processed() {
  node - "$case_dir/proof.raw" "$path" "$case_dir/proof-processing.json" <<'JS'
const fs=require('fs'),assert=require('assert'),[proof,path,receipt]=process.argv.slice(2);
const expected=JSON.parse(fs.readFileSync(proof,'utf8'));
const text=fs.readFileSync(path,'utf8'),records=text.slice(0,text.lastIndexOf('\n')).split('\n').filter(Boolean).map(JSON.parse);
const observed=records.filter(r=>r.kind==='update'&&r.data.update?.update?.toolCallId===expected.update.toolCallId&&r.data.update?.update?.status==='completed');
const barriers=records.filter(r=>r.kind==='update'&&r.data.update?.update?.content?.text==='proof-processing-barrier');
if(observed.length!==1||barriers.length!==1) process.exit(1);
assert.deepStrictEqual(observed[0].data.update,expected);
assert.deepStrictEqual(barriers[0].data.update,{sessionId:expected.sessionId,update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'proof-processing-barrier'}}});
assert(records.indexOf(observed[0])<records.indexOf(barriers[0]));
assert.strictEqual(observed[0].connection_id,barriers[0].connection_id);
fs.writeFileSync(receipt,JSON.stringify({proof:observed[0],processing_barrier:barriers[0]})+'\n');
JS
 }
 check proof.gateway-consumed-and-processed wait_until proof_processed
 if [[ $cut == proof-committed ]]; then
  check proof.committed wait_until od_proof_count 1
  expected_hex=$(node -e 'process.stdout.write(require("fs").readFileSync(process.argv[1]).toString("hex").toUpperCase())' "$case_dir/proof.raw")
  check proof.exact-raw test "$(sql "SELECT hex(raw) FROM cursor_proofs WHERE instance_id='$id';")" = "$expected_hex"
  check proof.atomic-request test "$(sql "SELECT proven_gone_how='tool_ran' AND delivered_at IS NOT NULL AND decision_origin='caller' FROM permission_requests WHERE request_token='$token';")" = 1
 else
  check proof.uncommitted od_proof_count 0
 fi
else
 echo "unknown durability cut: $cut" >&2
 exit 2
fi

od_snapshot > "$case_dir/obligations-before.json"
before_prompts=$(frames | jq -s '[.[]|select(.method=="session/prompt")]|length')
before_replies=$(frames | jq -s '[.[]|select(has("method")|not)]|length')
old_gateway=$gateway
old_runtime=$runtime
old_connection=$(sql "SELECT bound_connection_id FROM cursor_runtimes WHERE bound_instance_id='$id';")
# SIGKILL cannot run the controller's finalizers or flush deferred observations.
kill -KILL "$gateway"
kill -KILL "$runtime" 2>/dev/null || proc_gone "$runtime"
check crash.gateway-gone wait_gone "$gateway"
check crash.runtime-gone wait_gone "$runtime"
if [[ -n ${lock_owner:-} ]]; then od_unlock; fi
check crash.no-clean-end test "$(pc_count session_end)" = 0
od_snapshot > "$case_dir/obligations-after.json"
check reopen.exact-records cmp "$case_dir/obligations-before.json" "$case_dir/obligations-after.json"
check reopen.same-store test "$(sql "SELECT value FROM store_meta WHERE key='store_id';")" = "$store_id"
for observation in 1 2; do
 ad get --instance-id "$id" >/dev/null
 ad read-pane --instance-id "$id" --n-lines 100 > "$case_dir/inspection.json" 2>/dev/null || true
 check observe.no-prompt pc_prompts_is "$before_prompts"
 check observe.no-response reply_count_is "$before_replies"
done
ad find-missing >/dev/null
check crash.missing wait_state missing
# Fresh process/connection loads only the persisted conversation. There is no
# configured initial prompt in this launch and no synthetic replay file reader.
printf '{"loadSupport":true}\n' > "$case_dir/scenario"
rm -f "$case_dir/events.json"
ad resume --instance-id "$id" > "$case_dir/public.json"
check replacement.load wait_until od_load_seen
check replacement.loaded wait_until rl_loaded
check replacement.pending test "$(row_state)" = pending
load_identity
check replacement.new-helper test "$gateway" != "$old_gateway"
check replacement.new-connection test "$(sql "SELECT bound_connection_id FROM cursor_runtimes WHERE bound_instance_id='$id';")" != "$old_connection"
check replacement.no-prompt pc_prompts_is "$before_prompts"
check replacement.no-response reply_count_is "$before_replies"
od_snapshot > "$case_dir/obligations-after.json"
check replacement.preserves-records cmp "$case_dir/obligations-before.json" "$case_dir/obligations-after.json"
old_outcomes=$(pc_count turn_outcome)
prompt=fresh-explicit-after-crash
pc_terminal "$prompt"
check replacement.positive-new-input wait_until pc_prompts_is "$((before_prompts+1))"
check replacement.current-completion wait_until pc_count_is turn_outcome "$((old_outcomes+1))"
check replacement.input-completed wait_until od_input_is completed
check replacement.waiting wait_state waiting
ad pause --instance-id "$id" >/dev/null
check replacement.stopped wait_gone "$gateway"
check bytes.complete pc_raw_complete
jq -cn --arg cut "$cut" --arg connection "$old_connection" --arg store "$store_id" --argjson gateway "$old_gateway" --argjson runtime "$old_runtime" '{cut:$cut,old_connection:$connection,store_id:$store,crashed_gateway:$gateway,crashed_runtime:$runtime}' > "$case_dir/crash.json"
od_finish
