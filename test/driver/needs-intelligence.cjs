#!/usr/bin/env node
// needs_intelligence acceptance in the Docker harness (needs-intelligence
// Epic): public reads across all agents with the configured timeout,
// representative continuity and report-only command guards, each against the
// shipped binary.
//
// Expected wire values come from the shared hand-authored fixture the unit,
// CLI, MCP and TypeScript tests also use; the case adds only what differs per
// scenario (request tokens and tools). Every store write goes through
// /opt/driver/sql.sh. The v14 ownership and clock columns have no public
// writer, so they are seeded with sql.sh INSERTs, like context-fullness seeds
// its rows; the binary itself creates the schema and does every observation.
//
// Observation writes are best effort: a read that cannot commit returns the
// previously committed display. So each transition is confirmed by
// spawns.observation_version, re-reading a bounded number of times at the same
// state, and only then asserted. Reads that change nothing never bump it.
//
// Each entry must leave exactly one ad.spawn.displayed_state_transition
// record in the trail. db-reset does not reset the trail, so records are
// selected by the case's own instance ids.
//
// Stdout is one compact JSON evidence line of the observed values; the harness
// keeps only its first 200 characters, so it stays short. It holds no run-time
// timestamp: since is shown as the relative facts asserted about it, so every
// passing run prints the same line. Exit 77 with availability:unavailable
// means the binary lacks the schema (never a pass).
const assert = require('node:assert/strict');
const {spawnSync} = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

assert.equal(process.env.AGENT_DIRECTOR_TEST_HARNESS, '1', 'Docker harness required');
const started = Date.now();
const wire = JSON.parse(fs.readFileSync('/work/source/cmd/agent-director/testdata/display/needs_intelligence.json', 'utf8'));
for (const key of ['state', 'since', 'request_token', 'tool_name']) {
 assert.equal(typeof wire[key], 'string', `shared fixture lacks ${key}`);
}
const sinceShape = new RegExp('^' + [...wire.since].map(c => /\d/.test(c) ? '\\d' : c.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('') + '$');
const db = path.join(os.homedir(), '.config/agent-director/state.db');
const trailPath = path.join(path.dirname(db), 'ad-trail.jsonl');
const MAX_READS = 5;
const BUSY_RETRY_MS = 10000;
// The configured timeout is never the default 30 s, so a binary that ignores
// [state] needs_intelligence_timeout_seconds shows a different state for a
// request fallen back RECENT_MS before the case start: that request qualifies
// under 30 s and LOWERED_S, and not under TIMEOUT_S until the case has run
// for over 50 minutes.
const TIMEOUT_S = 3600;
const LOWERED_S = 60;
const RECENT_MS = 10 * 60 * 1000;
const STATUS_KEYS = ['context', 'family_state', 'idle_screen_availability', 'request_token', 'since', 'state', 'tmux_session_name', 'tool_name'];
const RECORD_KEYS = ['event', 'instance_id', 'new_state', 'prior_state', 'ts'];
// A list row's keys present on every row, and those present only on some.
const LIST_KEYS = ['agent', 'cwd', 'family_state', 'idle_screen_availability', 'instance_id', 'labels', 'last_seen_at', 'permission_requests', 'relay_mode', 'started_at', 'state', 'state_changed_at', 'tmux_session_name'];
const LIST_OPTIONAL = ['ended_at', 'launch_started_at', 'liveness_note', 'liveness_unverified_since', 'parent_id'];
const q = value => "'" + String(value).replaceAll("'", "''") + "'";
const availability = agent => agent === 'cursor' ? 'unavailable' : 'available';

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
// Runs a command that may refuse with a false ErrStoreBusy under load, having
// recorded nothing: retries only that refusal, for at most BUSY_RETRY_MS, and
// returns the first other outcome (or the last busy one once the time is up).
function callUnlessBusy(verb, ...args) {
 const deadline = Date.now() + BUSY_RETRY_MS;
 for (;;) {
  const result = call(verb, ...args);
  if (result.status === 0 || result.envelope?.err_name !== 'ErrStoreBusy' || Date.now() >= deadline) return result;
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 100);
 }
}
const version = id => Number(sql(`SELECT observation_version FROM spawns WHERE instance_id=${q(id)}`, true));
const rawState = id => sql(`SELECT state FROM spawns WHERE instance_id=${q(id)}`, true);

// Confirms one expected observation commit. A skipped best-effort write leaves
// observation_version unchanged, so re-read (same state, no re-seeding) until
// it moves, at most MAX_READS times; it must move by exactly one.
function observeCommit(verb, id) {
 const before = version(id);
 for (let reads = 1; reads <= MAX_READS; reads++) {
  read(verb, id);
  const now = version(id);
  if (now !== before) {
   assert.equal(now, before + 1, `${id}: one transition must commit exactly once (observation_version ${before} -> ${now})`);
   return {reads, version: now};
  }
 }
 assert.fail(`${id}: ${verb} never committed an observation in ${MAX_READS} reads (observation_version stayed ${before})`);
}

// The driver initializes the store with the tested binary, never a schema dump.
fs.mkdirSync(path.dirname(db), {recursive: true});
assert.equal(call('list').status, 0, 'agent-director list could not open the store');
const columns = table => sql(`SELECT name FROM pragma_table_info('${table}')`, true).split('\n');
const needed = {
 permission_requests: ['owner_launch_token', 'owner_permission_epoch', 'admitted_at', 'fallback_at', 'fallback_basis'],
 spawns: ['permission_epoch', 'observation_version', 'display_observation', 'display_exit', 'idle_screen_timeout_seconds'],
};
const missing = Object.entries(needed).flatMap(([table, names]) => names.filter(n => !columns(table).includes(n)).map(n => `${table}.${n}`));
if (missing.length) {
 console.log(JSON.stringify({availability: 'unavailable', capability: 'needs_intelligence', missing}));
 process.exit(77);
}
const configure = timeout => fs.writeFileSync(path.join(path.dirname(db), 'config.toml'), `[state]\nsettle_ms = 0\nneeds_intelligence_timeout_seconds = ${timeout}\n`);
configure(TIMEOUT_S);

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
// A request owned by the spawn's current launch and interval; at is its
// fallback clock in milliseconds, by default long before any timeout.
function request(id, token, tool, at = OLD) {
 sql(`INSERT INTO permission_requests(instance_id,request_token,tool_name,tool_input,created_at,settled_at,owner_launch_token,owner_permission_epoch,admitted_at,fallback_at,fallback_basis) VALUES(${q(id)},${q(token)},${q(tool)},'{}','2019-12-31 23:59:00',${at},${q(OWNER)},${EPOCH},${at - 1000},${at},'deadline')`);
}
// Every wire field of one read row against the shared fixture, with the
// scenario's token and tool and the agent's idle_screen_availability; since
// must be the read that committed the entry. A status row has exactly the
// caller contract's keys for needs_intelligence.
function check(verb, row, id, agent, token = A, tool = wire.tool_name) {
 const label = `${id} ${verb}`;
 const want = {...wire, request_token: token, tool_name: tool, tmux_session_name: 'ts-' + id, idle_screen_availability: availability(agent)};
 for (const [key, value] of Object.entries(want)) {
  if (key !== 'since') assert.equal(row[key], value, `${label}: ${key}`);
 }
 assert.match(row.since, sinceShape, `${label}: since ${row.since} is not the fixture's format ${wire.since}`);
 const at = Date.parse(row.since);
 assert.ok(at >= started && at <= Date.now() + 1000, `${label}: since ${row.since} is not a read made during this case`);
 if (verb === 'status') {
  assert.deepEqual(Object.keys(row).sort(), STATUS_KEYS, `${label}: status keys`);
  return;
 }
 assert.ok(!('unchanged_minutes' in row), `${label}: needs_intelligence must not report unchanged_minutes`);
 assert.ok(!('stuck_reason' in row), `${label}: row carries stuck_reason`);
}
// The id's trail records: exactly one displayed_state_transition, the entry
// at since, with exactly its own keys; no record of the old event name and no
// raw state_transition naming a Displayed State. Returns the record count.
function checkTrail(id, since) {
 const all = fs.existsSync(trailPath) ? fs.readFileSync(trailPath, 'utf8').split('\n').map(parse).filter(r => r?.instance_id === id) : [];
 const displayed = all.filter(r => r.event === 'ad.spawn.displayed_state_transition');
 assert.equal(displayed.length, 1, `${id}: ${displayed.length} displayed_state_transition records, want 1`);
 const [record] = displayed;
 assert.deepEqual(Object.keys(record).sort(), RECORD_KEYS, `${id}: record keys`);
 assert.equal(record.prior_state, 'check_permission', `${id}: record prior_state`);
 assert.equal(record.new_state, wire.state, `${id}: record new_state`);
 assert.equal(record.ts, since, `${id}: record ts is not the committing read's since`);
 assert.ok(!all.some(r => r.event === 'ad.spawn.stuck_transition'), `${id}: an ad.spawn.stuck_transition record exists`);
 const raw = all.filter(r => r.event === 'ad.spawn.state_transition' && [r.prior_state, r.new_state].some(s => s === 'needs_intelligence' || s === 'idle_screen'));
 assert.equal(raw.length, 0, `${id}: a state_transition record names a Displayed State`);
 return displayed.length;
}

const mode = process.argv[2];
if (mode === 'public') {
 const agents = ['claude', 'codex', 'cursor'];
 const versions = [];
 const records = [];
 for (const agent of agents) {
  const id = 'public-' + agent;
  seed(id, agent);
  request(id, A, wire.tool_name);
  assert.equal(sql(`SELECT COALESCE(display_observation,'') FROM spawns WHERE instance_id=${q(id)}`, true), '', `${id}: seeding must not pre-commit an entry`);
  const committed = observeCommit('status', id);
  const status = read('status', id);
  check('status', status, id, agent);
  // A fresh CLI process must retain the first committed entry exactly.
  const get = read('get', id);
  check('get', get, id, agent);
  assert.equal(get.since, status.since, `${agent}: get since differs from status`);
  assert.equal(get.permission_requests.length, 1, `${agent}: open requests`);
  assert.equal(get.unproven_requests.length, 1, `${agent}: unproven requests`);
  assert.equal(version(id), committed.version, `${agent}: repeated reads must not bump observation_version`);
  assert.equal(rawState(id), 'check_permission', `${agent}: raw state must stay check_permission`);
  versions.push(committed.version);
  records.push(checkTrail(id, status.since));
 }
 const list = call('list', '--state', wire.state, '--limit', '3');
 assert.equal(list.status, 0, list.stderr);
 const rows = list.data.spawns;
 assert.deepEqual(rows.map(r => r.instance_id).sort(), agents.map(a => 'public-' + a));
 for (const row of rows) {
  assert.equal(row.state, wire.state, `${row.instance_id} list: state`);
  assert.equal(row.idle_screen_availability, availability(row.agent), `${row.instance_id} list: idle_screen_availability`);
  assert.deepEqual(Object.keys(row).filter(key => !LIST_OPTIONAL.includes(key)).sort(), LIST_KEYS, `${row.instance_id} list: keys`);
 }
 // The configured timeout decides, not the default: under TIMEOUT_S the
 // recent request leaves the row in check_permission over every read; once
 // the key is lowered to LOWERED_S the same request enters needs_intelligence.
 const recent = 'public-recent';
 seed(recent, 'claude');
 request(recent, A, wire.tool_name, started - RECENT_MS);
 const unentered = version(recent);
 for (let reads = 1; reads <= MAX_READS; reads++) {
  assert.equal(read('status', recent).state, 'check_permission', `${recent}: read ${reads} entered needs_intelligence under a ${TIMEOUT_S} s timeout`);
 }
 assert.equal(version(recent), unentered, `${recent}: no read may commit an entry under a ${TIMEOUT_S} s timeout`);
 configure(LOWERED_S);
 observeCommit('status', recent);
 const lowered = read('status', recent);
 check('status', lowered, recent, 'claude');
 records.push(checkTrail(recent, lowered.since));
 // Each agent's since was asserted at or after the case start, equal across
 // status and get, and the ts of its one trail record.
 console.log(JSON.stringify({case: mode, token: A.slice(0, 8), tool: wire.tool_name, list: rows.length, v: versions, since: '>=start,get=status=ts', records, timeout: {[TIMEOUT_S]: 'check_permission', [LOWERED_S]: lowered.state}}));
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
 check('status', s1, id, 'claude', A, wire.tool_name);
 steps.push([A.slice(0, 8), s1.tool_name, first.version]);
 // A completed pane answer closes A; B becomes representative in the same entry.
 sql(`UPDATE permission_requests SET pane_answer='outside' WHERE instance_id=${q(id)} AND request_token=${q(A)}`);
 const second = observeCommit('get', id);
 const s2 = read('get', id);
 check('get', s2, id, 'claude', B, 'Read');
 assert.equal(s2.since, s1.since, 'a representative change must keep since');
 steps.push([B.slice(0, 8), s2.tool_name, second.version]);
 // A lifecycle close of B leaves only C.
 sql(`UPDATE permission_requests SET closed_at=${OLD + 2} WHERE instance_id=${q(id)} AND request_token=${q(B)}`);
 const third = observeCommit('status', id);
 const s3 = read('status', id);
 check('status', s3, id, 'claude', C, 'Write');
 assert.equal(s3.since, s1.since, 'a representative change must keep since');
 assert.equal(version(id), third.version, 'repeated reads must not bump observation_version');
 steps.push([C.slice(0, 8), s3.tool_name, third.version]);
 assert.equal(rawState(id), 'check_permission', 'raw state must stay check_permission');
 // Both representative changes add no record: the entry's stays the only one.
 const records = checkTrail(id, s1.since);
 // S (s1.since) was asserted at or after the case start, kept by both
 // representative changes and the ts of the one trail record.
 console.log(JSON.stringify({case: mode, steps, since: '>=start,kept=ts', records, raw: rawState(id)}));
} else if (mode === 'guards') {
 const expected = {'send-keys': 'ErrRelayFallenBack', decide: 'ErrRelayFallenBack', pause: 'ErrSpawnNotPausable'};
 const observed = {'send-keys': [], decide: [], pause: []};
 const facts = [];
 const records = [];
 for (const agent of ['claude', 'codex', 'cursor']) {
  const id = 'guard-' + agent;
  seed(id, agent);
  request(id, A, wire.tool_name);
  observeCommit('status', id);
  const before = read('status', id);
  check('status', before, id, agent);
  const commands = {
   'send-keys': ['--instance-id', id, '--text', 'next'],
   decide: ['--instance-id', id, '--request-token', A, '--decision', 'allow', '--max-wait-ms', '0'],
   pause: ['--instance-id', id],
  };
  for (const [verb, args] of Object.entries(commands)) {
   // decide --max-wait-ms 0 cannot wait for a store lock, so load can make it
   // refuse ErrStoreBusy, recording nothing; the other verbs wait up to the
   // store's busy timeout.
   const result = verb === 'decide' ? callUnlessBusy(verb, ...args) : call(verb, ...args);
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
  check('status', after, id, agent);
  assert.equal(after.since, before.since, `${agent}: refusals must not restart the entry`);
  assert.equal(rawState(id), 'check_permission', `${agent}: raw state must stay check_permission`);
  // The refusals add no record: the entry's stays the only one.
  records.push(checkTrail(id, before.since));
 }
 const errors = Object.fromEntries(Object.entries(observed).map(([verb, names]) => [verb, [...new Set(names)].join('/')]));
 console.log(JSON.stringify({case: mode, errors, facts: [...new Set(facts)].join('/'), shown: wire.state, records}));
} else throw new Error('expected public, representative or guards');
