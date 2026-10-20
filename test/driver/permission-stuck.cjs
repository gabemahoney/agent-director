#!/usr/bin/env node
// Permission-unanswered stuck acceptance in the Docker harness (permission-stuck
// Epic): public reads across all agents, representative continuity and
// report-only command guards, each against the shipped binary.
//
// Expected wire values come from the shared hand-authored fixture the unit,
// CLI, MCP and TypeScript tests also use; the case adds only what differs per
// scenario (request tokens and tools). Every store write goes through
// /opt/driver/sql.sh. The new v14 ownership and clock columns have no public
// writer, so they are seeded with sql.sh INSERTs, like context-fullness seeds
// its rows; the binary itself creates the schema and does every observation.
//
// Observation writes are best effort: a read that cannot commit returns the
// previously committed display. So each transition is confirmed by
// spawns.stuck_version, re-reading a bounded number of times at the same
// state, and only then asserted. Reads that change nothing never bump it.
//
// Stdout is one compact JSON evidence line of the observed values; the harness
// keeps only its first 200 characters, so it stays short. Exit 77 with
// availability:unavailable means the binary lacks the schema (never a pass).
const assert = require('node:assert/strict');
const {spawnSync} = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

assert.equal(process.env.AGENT_DIRECTOR_TEST_HARNESS, '1', 'Docker harness required');
const started = Date.now();
const wire = JSON.parse(fs.readFileSync('/work/source/cmd/agent-director/testdata/stuck/permission.json', 'utf8'));
for (const key of ['state', 'stuck_reason', 'since', 'request_token', 'tool_name']) {
 assert.equal(typeof wire[key], 'string', `shared fixture lacks ${key}`);
}
const sinceShape = new RegExp('^' + [...wire.since].map(c => /\d/.test(c) ? '\\d' : c.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('') + '$');
const db = path.join(os.homedir(), '.config/agent-director/state.db');
const MAX_READS = 5;
const q = value => "'" + String(value).replaceAll("'", "''") + "'";

function sql(statement, readonly = false) {
 const result = spawnSync('/opt/driver/sql.sh', readonly ? ['-readonly', db, statement] : [db, statement], {encoding: 'utf8'});
 assert.equal(result.status, 0, `sql.sh: ${result.stderr}`);
 return result.stdout.trim();
}
const parse = text => { try { return JSON.parse(text); } catch { return null; } };
function call(verb, ...args) {
 const result = spawnSync('agent-director', [verb, ...args], {encoding: 'utf8'});
 const lines = result.stderr.trim().split('\n').filter(Boolean);
 // A refusal is the JSON envelope on stderr; a result is the JSON on stdout.
 return {status: result.status, stdout: result.stdout, stderr: result.stderr, data: parse(result.stdout.trim()), envelope: lines.length ? parse(lines[lines.length - 1]) : null};
}
function read(verb, id) {
 const result = call(verb, '--instance-id', id);
 assert.equal(result.status, 0, `${verb} ${id}: ${result.stderr}`);
 assert.ok(result.data, `${verb} ${id}: no JSON result`);
 return result.data;
}
const version = id => Number(sql(`SELECT stuck_version FROM spawns WHERE instance_id=${q(id)}`, true));
const rawState = id => sql(`SELECT state FROM spawns WHERE instance_id=${q(id)}`, true);

// Confirms one expected observation commit. A skipped best-effort write leaves
// stuck_version unchanged, so re-read (same state, no re-seeding) until it
// moves, at most MAX_READS times; it must move by exactly one.
function observeCommit(verb, id) {
 const before = version(id);
 for (let reads = 1; reads <= MAX_READS; reads++) {
  read(verb, id);
  const now = version(id);
  if (now !== before) {
   assert.equal(now, before + 1, `${id}: one transition must commit exactly once (stuck_version ${before} -> ${now})`);
   return {reads, version: now};
  }
 }
 assert.fail(`${id}: ${verb} never committed an observation in ${MAX_READS} reads (stuck_version stayed ${before})`);
}

// The driver initializes the store with the tested binary, never a schema dump.
fs.mkdirSync(path.dirname(db), {recursive: true});
assert.equal(call('list').status, 0, 'agent-director list could not open the store');
const columns = table => sql(`SELECT name FROM pragma_table_info('${table}')`, true).split('\n');
const needed = {permission_requests: ['owner_launch_token', 'owner_permission_epoch', 'admitted_at', 'fallback_at', 'fallback_basis'], spawns: ['permission_epoch', 'stuck_version', 'stuck_observation']};
const missing = Object.entries(needed).flatMap(([table, names]) => names.filter(n => !columns(table).includes(n)).map(n => `${table}.${n}`));
if (missing.length) {
 console.log(JSON.stringify({availability: 'unavailable', capability: 'permission_unanswered', missing}));
 process.exit(77);
}
fs.writeFileSync(path.join(path.dirname(db), 'config.toml'), '[state]\nsettle_ms = 0\npermission_unanswered_timeout_seconds = 30\n');

const A = wire.request_token;
const B = 'bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb';
const C = 'cccccccc-cccc-4ccc-cccc-cccccccccccc';
assert.ok(A < B && B < C, 'tokens must sort A < B < C for the lexical tie-break');
const OLD = Date.parse('2020-01-01T00:00:00.000Z');
const OWNER = '1111111111111111';
const EPOCH = 7;

function seed(id, agent) {
 sql(`INSERT INTO spawns(instance_id,state,settled_state,state_changed_ms,cwd,agent,relay_mode,tmux_session_name,launch_token,permission_epoch) VALUES(${q(id)},'check_permission','check_permission',0,'/tmp',${q(agent)},'on',${q('ts-' + id)},${q(OWNER)},${EPOCH})`);
}
// A request owned by the spawn's current launch and interval whose fallback
// clock started long before any timeout; at is that clock in milliseconds.
function request(id, token, tool, at = OLD) {
 sql(`INSERT INTO permission_requests(instance_id,request_token,tool_name,tool_input,created_at,settled_at,owner_launch_token,owner_permission_epoch,admitted_at,fallback_at,fallback_basis) VALUES(${q(id)},${q(token)},${q(tool)},'{}','2019-12-31 23:59:00',${at},${q(OWNER)},${EPOCH},${at - 1000},${at},'deadline')`);
}
// Every wire field of one read row against the shared fixture, with the
// scenario's token and tool; since must be the read that committed the episode.
function check(row, label, token = A, tool = wire.tool_name) {
 for (const [key, value] of Object.entries(wire)) {
  if (key === 'since') continue;
  const want = key === 'request_token' ? token : key === 'tool_name' ? tool : value;
  assert.equal(row[key], want, `${label}: ${key}`);
 }
 assert.match(row.since, sinceShape, `${label}: since ${row.since} is not the fixture's format ${wire.since}`);
 const at = Date.parse(row.since);
 assert.ok(at >= started && at <= Date.now() + 1000, `${label}: since ${row.since} is not a read made during this case`);
 assert.ok(!('unchanged_minutes' in row), `${label}: stuck must not report unchanged_minutes`);
}

const mode = process.argv[2];
if (mode === 'public') {
 const agents = ['claude', 'codex', 'cursor'];
 const since = [], versions = [];
 for (const agent of agents) {
  const id = 'public-' + agent;
  seed(id, agent);
  request(id, A, wire.tool_name);
  assert.equal(sql(`SELECT COALESCE(stuck_observation,'') FROM spawns WHERE instance_id=${q(id)}`, true), '', `${id}: seeding must not pre-commit an episode`);
  const committed = observeCommit('status', id);
  const status = read('status', id);
  check(status, `${agent} status`);
  assert.equal(status.tmux_session_name, 'ts-' + id);
  // A fresh CLI process must retain the first committed episode exactly.
  const get = read('get', id);
  check(get, `${agent} get`);
  assert.equal(get.since, status.since, `${agent}: get since differs from status`);
  assert.equal(get.permission_requests.length, 1, `${agent}: open requests`);
  assert.equal(get.unproven_requests.length, 1, `${agent}: unproven requests`);
  assert.equal(version(id), committed.version, `${agent}: repeated reads must not bump stuck_version`);
  assert.equal(rawState(id), 'check_permission', `${agent}: raw state must stay check_permission`);
  since.push(status.since);
  versions.push(committed.version);
 }
 const list = call('list', '--state', 'stuck', '--limit', '3');
 assert.equal(list.status, 0, list.stderr);
 const rows = list.data.spawns;
 assert.deepEqual(rows.map(r => r.instance_id).sort(), agents.map(a => 'public-' + a));
 for (const row of rows) {
  assert.equal(row.state, wire.state);
  assert.equal(row.stuck_reason, wire.stuck_reason);
  for (const key of ['since', 'request_token', 'tool_name', 'unchanged_minutes']) assert.ok(!(key in row), `list row must not carry ${key}`);
 }
 console.log(JSON.stringify({case: mode, token: A.slice(0, 8), tool: wire.tool_name, list: rows.length, v: versions, since}));
} else if (mode === 'representative') {
 const id = 'representative';
 seed(id, 'claude');
 // C has the latest clock; A and B tie on the earliest, so A wins lexically.
 request(id, C, 'Write', OLD + 1);
 request(id, B, 'Read');
 request(id, A, wire.tool_name);
 const steps = [];
 const first = observeCommit('status', id);
 const s1 = read('status', id);
 check(s1, 'tie-break', A, wire.tool_name);
 steps.push([A.slice(0, 8), s1.tool_name, first.version]);
 // A completed pane answer closes A; B becomes representative in the same episode.
 sql(`UPDATE permission_requests SET pane_answer='outside' WHERE instance_id=${q(id)} AND request_token=${q(A)}`);
 const second = observeCommit('get', id);
 const s2 = read('get', id);
 check(s2, 'after answer', B, 'Read');
 assert.equal(s2.since, s1.since, 'a representative change must keep since');
 steps.push([B.slice(0, 8), s2.tool_name, second.version]);
 // A lifecycle close of B leaves only C.
 sql(`UPDATE permission_requests SET closed_at=${OLD + 2} WHERE instance_id=${q(id)} AND request_token=${q(B)}`);
 const third = observeCommit('status', id);
 const s3 = read('status', id);
 check(s3, 'after close', C, 'Write');
 assert.equal(s3.since, s1.since, 'a representative change must keep since');
 assert.equal(version(id), third.version, 'repeated reads must not bump stuck_version');
 steps.push([C.slice(0, 8), s3.tool_name, third.version]);
 assert.equal(rawState(id), 'check_permission', 'raw state must stay check_permission');
 console.log(JSON.stringify({case: mode, steps, since: s1.since, raw: rawState(id)}));
} else if (mode === 'guards') {
 const expected = {'send-keys': 'ErrRelayFallenBack', decide: 'ErrRelayFallenBack', pause: 'ErrSpawnNotPausable'};
 const observed = {'send-keys': [], decide: [], pause: []};
 const facts = [];
 for (const agent of ['claude', 'codex', 'cursor']) {
  const id = 'guard-' + agent;
  seed(id, agent);
  request(id, A, wire.tool_name);
  observeCommit('status', id);
  const before = read('status', id);
  check(before, `${agent} before`);
  const commands = {
   'send-keys': ['--instance-id', id, '--text', 'next'],
   decide: ['--instance-id', id, '--request-token', A, '--decision', 'allow', '--max-wait-ms', '0'],
   pause: ['--instance-id', id],
  };
  for (const [verb, args] of Object.entries(commands)) {
   const result = call(verb, ...args);
   assert.notEqual(result.status, 0, `${agent}/${verb} unexpectedly permitted: ${result.stdout}`);
   assert.ok(result.envelope, `${agent}/${verb}: no error envelope on stderr: ${result.stderr}`);
   assert.equal(result.envelope.err_name, expected[verb], `${agent}/${verb}: ${result.stderr}`);
   observed[verb].push(result.envelope.err_name);
  }
  // The refusals recorded nothing: no verdict, no proof, no pane answer.
  const fact = sql(`SELECT COALESCE(decision,'')||'|'||COALESCE(proven_gone_at,'')||'|'||pane_answer FROM permission_requests WHERE instance_id=${q(id)}`, true);
  assert.equal(fact, '||none', `${agent}: refused commands must leave the request untouched`);
  facts.push(fact);
  const after = read('status', id);
  check(after, `${agent} after`);
  assert.equal(after.since, before.since, `${agent}: refusals must not restart the episode`);
  assert.equal(rawState(id), 'check_permission', `${agent}: raw state must stay check_permission`);
 }
 const errors = Object.fromEntries(Object.entries(observed).map(([verb, names]) => [verb, [...new Set(names)].join('/')]));
 console.log(JSON.stringify({case: mode, n: 3, errors, facts: [...new Set(facts)].join('/'), shown: wire.state, raw: 'check_permission'}));
} else throw new Error('expected public, representative or guards');
