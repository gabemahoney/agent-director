#!/usr/bin/env bash
set -euo pipefail
out=/work/tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/pc01-fifo-fix
texts=($'第一 🐝\nline café' $'second 🦋\n別の行' HOLD 'terminal queued café' $'public queued 🐝\nlast line')
jq --version > "$out/jq-version.txt"
printf '%s\0' "${texts[@]}" | jq -Rs 'split("\u0000")[:-1]' > "$out/old-want.json"
printf '%s\0' "${texts[@]}" | jq -Rs 'split("\u0000")' > "$out/old-unsliced.json"
jq -cn --args '$ARGS.positional' "${texts[@]}" > "$out/new-want.json"
node - "$out" <<'NODE'
const fs=require('fs'),assert=require('node:assert/strict');
const out=process.argv[2],read=n=>JSON.parse(fs.readFileSync(`${out}/${n}.json`,'utf8'));
const expected=['第一 🐝\nline café','second 🦋\n別の行','HOLD','terminal queued café','public queued 🐝\nlast line'];
assert.deepEqual(read('old-want'),expected.slice(0,4));
assert.deepEqual(read('old-unsliced'),expected);
assert.deepEqual(read('new-want'),expected);
assert.equal(read('new-want').length,5);
const result={status:'passed',old_count:4,unsliced_count:5,new_count:5,all_five_exact:true,newlines_unicode_exact:true,old_behavior:'jq split omits trailing empty element; [:-1] removes fifth real text',jq:fs.readFileSync(`${out}/jq-version.txt`,'utf8').trim()};
fs.writeFileSync(`${out}/focused-result.json`,JSON.stringify(result,null,2)+'\n');
console.log(JSON.stringify(result));
NODE
