#!/usr/bin/env node
const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
assert.equal(process.env.AGENT_DIRECTOR_TEST_HARNESS, '1', 'Docker harness required');
const db = path.join(os.homedir(), '.config/agent-director/state.db');
const binary = process.env.PERMISSION_TEST_BINARY || 'agent-director';
const sqlTool = process.env.PERMISSION_SQL_WRAPPER || '/opt/driver/sql.sh';
const q = value => "'" + String(value).replaceAll("'", "''") + "'";
function sql(statement) {
 const result = spawnSync(sqlTool, [db, statement], { encoding:'utf8' });
 assert.equal(result.status, 0, result.stderr); return result.stdout.trim();
}
function call(verb, ...args) {
 const result = spawnSync(binary, [verb, ...args], {encoding:'utf8'});
 return {...result, data: result.stdout.trim() ? JSON.parse(result.stdout) : null};
}
function read(verb, id) {
 const result = call(verb, '--instance-id', id); assert.equal(result.status,0,result.stderr); return result.data;
}
// The driver initializes with the tested binary, never an embedded schema dump.
fs.mkdirSync(path.dirname(db), {recursive:true});
if (!fs.existsSync(db)) assert.equal(call('list').status,0);
const columns = sql("SELECT name FROM pragma_table_info('permission_requests')").split('\n');
if (!columns.includes('fallback_at')) {
 console.log(JSON.stringify({availability:'unavailable',capability:'permission_unanswered'})); process.exit(77);
}
const A='aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa';
const B='bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb';
const C='cccccccc-cccc-4ccc-cccc-cccccccccccc';
const old=Date.parse('2020-01-01T00:00:00.000Z');
const configDir = path.dirname(db);
fs.writeFileSync(path.join(configDir,'config.toml'), '[state]\nsettle_ms = 0\npermission_unanswered_timeout_seconds = 30\n');
function seed(id,agent) {
 sql(`INSERT INTO spawns(instance_id,state,settled_state,state_changed_ms,cwd,agent,relay_mode,tmux_session_name,launch_token,permission_epoch) VALUES(${q(id)},'check_permission','check_permission',0,'/tmp',${q(agent)},'on',${q('ts-'+id)},'1111111111111111',7)`);
}
function request(id,token,tool='Bash',at=old) {
 sql(`INSERT INTO permission_requests(instance_id,request_token,tool_name,tool_input,created_at,settled_at,owner_launch_token,owner_permission_epoch,admitted_at,fallback_at,fallback_basis) VALUES(${q(id)},${q(token)},${q(tool)},'{}','2019-12-31 23:59:00',${at},'1111111111111111',7,${at-1000},${at},'deadline')`);
}
function check(row,token=A) {
 assert.equal(row.state,'stuck');assert.equal(row.stuck_reason,'permission_unanswered');
 assert.equal(row.request_token,token);assert.match(row.since,/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$/);
 assert.ok(!('unchanged_minutes' in row));
}
const mode = process.argv[2];
if (mode==='public') {
 const agents=['claude','codex','cursor'];
 for (const agent of agents) {
  const id='public-'+agent;seed(id,agent);request(id,A);
  const status=read('status',id);check(status);assert.equal(status.tmux_session_name,'ts-'+id);
  // A fresh CLI process must retain the first committed episode exactly.
  const get=read('get',id);check(get);assert.equal(get.since,status.since);
  assert.equal(get.tool_name,'Bash');assert.equal(get.permission_requests.length,1);assert.equal(get.unproven_requests.length,1);
 }
 const list=call('list','--state','stuck','--limit','3');assert.equal(list.status,0,list.stderr);assert.equal(list.data.spawns.length,3);
 for (const row of list.data.spawns) {assert.equal(row.stuck_reason,'permission_unanswered');for(const key of ['since','request_token','tool_name','unchanged_minutes']) assert.ok(!(key in row));}
 console.log(JSON.stringify({case:mode,agents,persisted:true,wire:true}));
} else if (mode==='representative') {
 seed('representative','claude');request('representative',C,'Write',old+1);request('representative',B,'Read');request('representative',A);
 const before=read('status','representative');check(before,A);
 sql(`UPDATE permission_requests SET pane_answer='outside' WHERE instance_id='representative' AND request_token=${q(A)}`);
 const next=read('get','representative');check(next,B);assert.equal(next.since,before.since);assert.equal(next.tool_name,'Read');
 sql(`UPDATE permission_requests SET superseded_at=${old+2} WHERE instance_id='representative' AND request_token=${q(B)}`);
 const last=read('status','representative');check(last,C);assert.equal(last.since,before.since);
 assert.equal(sql("SELECT state FROM spawns WHERE instance_id='representative'"),'check_permission');
 console.log(JSON.stringify({case:mode,lexicalTie:true,replacement:true,sinceRetained:true,rawUnchanged:true}));
} else if (mode==='guards') {
 for(const agent of ['claude','codex','cursor']) {
  const id='guard-'+agent;seed(id,agent);request(id,A);check(read('status',id));
  for(const [verb,args,error] of [
   ['send-keys',['--instance-id',id,'--text','next'],'ErrRelayFallenBack'],
   ['decide',['--instance-id',id,'--request-token',A,'--decision','allow','--max-wait-ms','0'],'ErrRelayFallenBack'],
   ['pause',['--instance-id',id],'ErrSpawnNotPausable']
  ]) {
   const result=call(verb,...args);assert.notEqual(result.status,0,`${agent}/${verb} unexpectedly permitted`);assert.equal(result.data.err_name,error,JSON.stringify(result.data));
  }
  assert.equal(sql(`SELECT COALESCE(decision,'')||'|'||COALESCE(proven_gone_at,'')||'|'||pane_answer FROM permission_requests WHERE instance_id=${q(id)}`),'||none');
 }
 console.log(JSON.stringify({case:mode,agents:['claude','codex','cursor'],authorityPreserved:true}));
} else throw new Error('expected public, representative or guards');
