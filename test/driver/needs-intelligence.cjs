#!/usr/bin/env node
// Fallen-back permission requests in the Docker harness (needs-intelligence
// Epic), against the shipped binary. b.zpv: a known permission request never
// shows needs_intelligence, however long it waits, on every agent with the
// relay on and on Cursor with it off: public reads report check_permission
// with no displayed_state, list --state check_permission lists the row, a
// read still establishes and persists the request's fallback clock, and the
// report-only command guards still refuse.
//
// Every store write goes through /opt/driver/sql.sh. The v14 ownership and
// clock columns have no public writer, so they are seeded with sql.sh
// INSERTs, like context-fullness seeds its rows; the binary itself creates the
// schema and does every observation.
//
// Observation writes are best effort: a read that cannot commit returns the
// previously committed display. So a read that must store a fallback clock is
// confirmed by spawns.observation_version, re-reading a bounded number of
// times at the same state, and only then asserted. Reads that change nothing
// never bump it.
//
// No read may leave an ad.spawn.displayed_state_transition record in the
// trail. db-reset does not reset the trail, so records are selected by the
// case's own instance ids.
//
// Stdout is one compact JSON evidence line of the observed values; the harness
// keeps only its first 200 characters, so it stays short. It holds no run-time
// timestamp, so every passing run prints the same line. Exit 77 with
// availability:unavailable means the binary lacks the schema (never a pass).
const assert = require('node:assert/strict');
const {spawnSync} = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

assert.equal(process.env.AGENT_DIRECTOR_TEST_HARNESS, '1', 'Docker harness required');
const db = path.join(os.homedir(), '.config/agent-director/state.db');
const trailPath = path.join(path.dirname(db), 'ad-trail.jsonl');
const MAX_READS = 5;
const BUSY_RETRY_MS = 10000;
// The lowest valid timeout: the key still loads, and has no effect.
const TIMEOUT_S = 1;
const STATUS_KEYS = ['context', 'family_state', 'idle_screen_availability', 'state'];
const DETAIL_KEYS = ['displayed_state', 'since', 'request_token', 'tool_name', 'unchanged_minutes', 'stuck_reason'];
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
const clock = id => sql(`SELECT COALESCE(fallback_at,'')||'|'||COALESCE(fallback_basis,'') FROM permission_requests WHERE instance_id=${q(id)}`, true);

// Confirms one expected observation commit. A skipped best-effort write leaves
// observation_version unchanged, so re-read (same state, no re-seeding) until
// it moves, at most MAX_READS times; it must move by exactly one.
function observeCommit(verb, id) {
 const before = version(id);
 for (let reads = 1; reads <= MAX_READS; reads++) {
  read(verb, id);
  const now = version(id);
  if (now !== before) {
   assert.equal(now, before + 1, `${id}: one commit must land exactly once (observation_version ${before} -> ${now})`);
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
fs.writeFileSync(path.join(path.dirname(db), 'config.toml'), `[state]\nsettle_ms = 0\nneeds_intelligence_timeout_seconds = ${TIMEOUT_S}\n`);

const A = 'aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa';
const TOOL = 'Bash';
const OLD = Date.parse('2020-01-01T00:00:00.000Z');
const OWNER = '1111111111111111';
const EPOCH = 7;

function seed(id, agent, relay = 'on') {
 sql(`INSERT INTO spawns(instance_id,state,settled_state,state_changed_ms,cwd,agent,relay_mode,tmux_session_name,launch_token,permission_epoch) VALUES(${q(id)},'check_permission','check_permission',0,'/tmp',${q(agent)},${q(relay)},${q('ts-' + id)},${q(OWNER)},${EPOCH})`);
}
// A request owned by the spawn's current launch and interval, its confirm_by
// at in milliseconds, long past; stored, the fallback clock is at too, else
// only a read stores it.
function request(id, at, stored) {
 const fallback = stored ? `${at},'deadline'` : 'NULL,NULL';
 sql(`INSERT INTO permission_requests(instance_id,request_token,tool_name,tool_input,created_at,settled_at,owner_launch_token,owner_permission_epoch,admitted_at,fallback_at,fallback_basis) VALUES(${q(id)},${q(A)},${q(TOOL)},'{}','2019-12-31 23:59:00',${at},${q(OWNER)},${EPOCH},${at - 1000},${fallback})`);
}
// One status or get row of a fallen-back request: state check_permission, no
// Displayed State nor its detail, the agent's idle_screen_availability; a
// status row has exactly the ordinary keys, a get row the open request.
function check(verb, row, id, agent) {
 const label = `${id} ${verb}`;
 assert.equal(row.state, 'check_permission', `${label}: state`);
 assert.equal(row.idle_screen_availability, availability(agent), `${label}: idle_screen_availability`);
 for (const key of DETAIL_KEYS) assert.ok(!(key in row), `${label}: row carries ${key}`);
 if (verb === 'status') {
  assert.deepEqual(Object.keys(row).sort(), STATUS_KEYS, `${label}: status keys`);
  return;
 }
 assert.deepEqual(row.permission_requests.map(r => [r.request_token, r.delivery]), [[A, 'fallen_back']], `${label}: open requests`);
}
// The id's trail holds no displayed_state_transition record, no record of the
// old event name and no raw state_transition naming a Displayed State.
function checkTrail(id) {
 const all = fs.existsSync(trailPath) ? fs.readFileSync(trailPath, 'utf8').split('\n').map(parse).filter(r => r?.instance_id === id) : [];
 const displayed = all.filter(r => r.event === 'ad.spawn.displayed_state_transition');
 assert.equal(displayed.length, 0, `${id}: displayed_state_transition records ${JSON.stringify(displayed)}`);
 assert.ok(!all.some(r => r.event === 'ad.spawn.stuck_transition'), `${id}: an ad.spawn.stuck_transition record exists`);
 const raw = all.filter(r => r.event === 'ad.spawn.state_transition' && [r.prior_state, r.new_state].some(s => s === 'needs_intelligence' || s === 'idle_screen'));
 assert.equal(raw.length, 0, `${id}: a state_transition record names a Displayed State`);
 return displayed.length;
}

const mode = process.argv[2];
if (mode === 'public') {
 // Each row's expected stored clock: confirm_by on the deadline basis with
 // the relay on, admission with Cursor's relay off.
 const rows = [
  {id: 'public-claude', agent: 'claude', relay: 'on', want: `${OLD}|deadline`},
  {id: 'public-codex', agent: 'codex', relay: 'on', want: `${OLD}|deadline`},
  {id: 'public-cursor', agent: 'cursor', relay: 'on', want: `${OLD}|deadline`},
  {id: 'public-cursor-off', agent: 'cursor', relay: 'off', want: `${OLD - 1000}|admission`},
 ];
 const commits = [];
 for (const {id, agent, relay, want} of rows) {
  seed(id, agent, relay);
  request(id, OLD, false);
  assert.equal(clock(id), '|', `${id}: seeding must not store a fallback clock`);
  // The first committing read stores the clock, as before b.zpv.
  const committed = observeCommit('status', id);
  assert.equal(clock(id), want, `${id}: stored fallback clock`);
  check('status', read('status', id), id, agent);
  check('get', read('get', id), id, agent);
  assert.equal(version(id), committed.version, `${id}: repeated reads must not bump observation_version`);
  assert.equal(rawState(id), 'check_permission', `${id}: raw state must stay check_permission`);
  commits.push(committed.version);
  checkTrail(id);
 }
 const ids = rows.map(r => r.id).sort();
 const list = call('list', '--state', 'check_permission', '--limit', String(rows.length));
 assert.equal(list.status, 0, list.stderr);
 assert.deepEqual(list.data.spawns.map(r => r.instance_id).sort(), ids, 'list --state check_permission');
 for (const row of list.data.spawns) {
  assert.equal(row.state, 'check_permission', `${row.instance_id} list: state`);
  assert.equal(row.idle_screen_availability, availability(row.agent), `${row.instance_id} list: idle_screen_availability`);
  assert.deepEqual(Object.keys(row).filter(key => !LIST_OPTIONAL.includes(key)).sort(), LIST_KEYS, `${row.instance_id} list: keys`);
 }
 const shown = call('list', '--state', 'needs_intelligence');
 assert.equal(shown.status, 0, shown.stderr);
 assert.deepEqual(shown.data.spawns, [], 'list --state needs_intelligence');
 console.log(JSON.stringify({case: mode, timeout: TIMEOUT_S, shown: 'check_permission', clocks: rows.map(r => r.want.split('|')[1]), v: commits, list: list.data.spawns.length, needs_intelligence: 0, records: 0}));
} else if (mode === 'guards') {
 const expected = {'send-keys': 'ErrRelayFallenBack', decide: 'ErrRelayFallenBack', pause: 'ErrSpawnNotPausable'};
 const observed = {'send-keys': [], decide: [], pause: []};
 const facts = [];
 for (const agent of ['claude', 'codex', 'cursor']) {
  const id = 'guard-' + agent;
  seed(id, agent);
  request(id, OLD, true);
  check('status', read('status', id), id, agent);
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
  check('status', read('status', id), id, agent);
  assert.equal(rawState(id), 'check_permission', `${agent}: raw state must stay check_permission`);
  checkTrail(id);
 }
 const errors = Object.fromEntries(Object.entries(observed).map(([verb, names]) => [verb, [...new Set(names)].join('/')]));
 console.log(JSON.stringify({case: mode, errors, facts: [...new Set(facts)].join('/'), shown: 'check_permission', records: 0}));
} else throw new Error('expected public or guards');
