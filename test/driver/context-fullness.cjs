#!/usr/bin/env node
// Public context acceptance over shared sanitized captures in the Docker harness.
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {execFileSync} = require('node:child_process');
const assert = require('node:assert/strict');

assert.equal(process.env.AGENT_DIRECTOR_TEST_HARNESS, '1', 'Docker harness required');
const root = '/work/source/internal/testsupport/contextfix/testdata';
const db = path.join(os.homedir(), '.config/agent-director/state.db');
const caseDir = fs.mkdtempSync('/tmp/context-fullness-');
const load = relative => JSON.parse(fs.readFileSync(path.join(root, relative), 'utf8'));
const unknown = load('unknown.expected.json');
const sql = statement => execFileSync('/opt/driver/sql.sh', [db, statement], {encoding:'utf8'}).trim();
const quote = value => "'" + String(value).replaceAll("'", "''") + "'";

function hook(name, session, transcript, source, agent = 'codex') {
 execFileSync('agent-director', ['hook', '--agent', agent], {
  input:JSON.stringify({hook_event_name:'SessionStart', source, session_id:session, transcript_path:transcript}),
  env:{...process.env, AGENT_DIRECTOR_INSTANCE_ID:name}, encoding:'utf8'
 });
}
function seed(name, agent, session, data, fresh = false) {
 const transcript = agent === 'claude' ? path.join(caseDir, name, session + '.jsonl') : path.join(caseDir, name + '.jsonl');
 fs.mkdirSync(path.dirname(transcript), {recursive:true});
 fs.writeFileSync(transcript, data);
 const stat = fs.readFileSync('/proc/self/stat', 'utf8');
 const start = stat.slice(stat.lastIndexOf(')') + 2).trim().split(/\s+/)[19];
 const values = [name, fresh ? 'pending' : 'ended', caseDir, name, 'off', agent, fresh ? '' : session, transcript, 'a1a1a1a1a1a1a1a1', agent === 'claude' ? 1 : 0, process.pid, start];
 sql('INSERT INTO spawns (instance_id,state,cwd,tmux_session_name,relay_mode,agent,session_id,jsonl_path,launch_token,subagent_tracking,pane_pid,pane_starttime) VALUES (' + values.map(quote).join(',') + ')');
 if (fresh) {
  hook(name, session, transcript, 'startup', agent);
  if (agent === 'codex') assert.notEqual(sql('SELECT codex_startup FROM spawns WHERE instance_id=' + quote(name)), '', 'startup was not admitted');
 }
 return transcript;
}
function read(verb, name) {
 return JSON.parse(execFileSync('agent-director', [verb, '--instance-id', name], {encoding:'utf8'}));
}
function check(name, expected, state = 'ended') {
 for (const verb of ['status', 'get']) {
  const result = read(verb, name);
  assert.deepEqual(result.context, expected, `${verb} ${name}`);
  assert.equal(result.state, state);
 }
}
if (process.argv[2] === 'numeric') {
 for (const name of ['writer', 'reviewer']) {
  const stem = `codex/0.159.2/${name}`;
  const data = fs.readFileSync(path.join(root, stem + '.jsonl'), 'utf8');
  const meta = load(stem + '.provenance.json');
  const expected = load(stem + '.expected.json');
  const transcript = seed(name, 'codex', meta.session_id, data, true);
  check(name, expected, 'waiting');
  const lines = data.trimEnd().split('\n');
  const compacted = lines.findLastIndex(line => JSON.parse(line).type === 'compacted');
  assert.ok(compacted > 0, 'capture lacks compaction');
  fs.writeFileSync(transcript, lines.slice(0, compacted + 1).join('\n') + '\n');
  check(name, unknown, 'waiting');
  fs.writeFileSync(transcript, data);
  check(name, expected, 'waiting');
  hook(name, meta.session_id, transcript, 'resume');
  check(name, unknown, 'waiting');
  assert.equal(fs.readFileSync(transcript, 'utf8'), data, 'resume must retain tested creation metadata');
  assert.deepEqual(read('list-subagents', name), {tracked:false, subagents:[]});
 }
 const claudeRoot = 'claude/2.1.280/';
 const provenance = load(claudeRoot + 'live-provenance.json');
 const callbacks = fs.readFileSync(path.join(root, claudeRoot + 'live-statuslines.jsonl'), 'utf8').trim().split('\n').map(JSON.parse);
 const parent = provenance.pairs.find(pair => pair.agent === 'parent');
 const child = provenance.pairs.find(pair => pair.agent === 'child');
 const expectedClaude = (pair, source) => ({used_tokens:pair.used_tokens, window_tokens:pair.window_tokens, fraction:pair.fraction, source, as_of:pair.usage_as_of});
 const parentExpected = expectedClaude(parent, 'claude_jsonl_usage_statusline_capacity');
 const childExpected = expectedClaude(child, 'claude_jsonl_usage_subagent_statusline_capacity');
 const parentPath = seed('claude-live', 'claude', parent.session_id, fs.readFileSync(path.join(root, claudeRoot + parent.usage_source)), true);
 check('claude-live', unknown, 'waiting');
 const callback = source => execFileSync('agent-director', ['hook', '--context-source', source], {input:JSON.stringify(callbacks.findLast(record => record.source === source)), env:{...process.env, AGENT_DIRECTOR_INSTANCE_ID:'claude-live'}, encoding:'utf8'});
 callback('statusLine');
 check('claude-live', parentExpected, 'waiting');
 const childPath = path.join(path.dirname(parentPath), parent.session_id, 'subagents', 'agent-' + child.agent_id + '.jsonl');
 fs.mkdirSync(path.dirname(childPath), {recursive:true});
 const childData = fs.readFileSync(path.join(root, claudeRoot + child.usage_source));
 fs.writeFileSync(childPath, childData);
 sql("INSERT INTO subagents(instance_id,subagent_id,launch_token,confirmed,state,opened_at,activity_at,life_number,session_id) SELECT instance_id," + quote(child.agent_id) + ",launch_token,1,'running',1,1,life_number," + quote(child.session_id) + " FROM spawns WHERE instance_id='claude-live'");
 callback('subagentStatusLine');
 const children = read('list-subagents', 'claude-live');
 assert.equal(children.tracked, true);
 assert.equal(children.subagents.length, 1);
 assert.deepEqual(children.subagents[0].context, childExpected);
 fs.appendFileSync(childPath, '{partial');
 assert.deepEqual(read('list-subagents', 'claude-live').subagents[0].context, unknown);
 check('claude-live', parentExpected, 'waiting');
 console.log('PASS context numeric: live Codex startup/compaction/resume and captured Claude parent/own-child capacity, usage, native callback admission and on-demand partial-tail invalidation');
} else if (process.argv[2] === 'unknown') {
 seed('claude', 'claude', 'uncaptured-capacity', fs.readFileSync(path.join(root, 'claude/2.1.280/missing-capacity.jsonl')));
 check('claude', unknown);
 sql("INSERT INTO subagents (instance_id,subagent_id,launch_token,confirmed,state,opened_at,activity_at,life_number,session_id) SELECT instance_id,'visible-child',launch_token,1,'running',1,1,life_number,'child-session' FROM spawns WHERE instance_id='claude'");
 const children = read('list-subagents', 'claude');
 assert.equal(children.tracked, true);
 assert.equal(children.subagents.length, 1);
 assert.deepEqual(children.subagents[0].context, unknown);
 for (const version of ['2026.10.01-e373342', '2026.10.12-d47ca18']) {
  assert.ok(load(`cursor/${version}/managed-attempt.json`), 'missing bounded qualification evidence');
  const name = 'cursor-' + version;
  seed(name, 'cursor', 'unqualified-session', '');
  check(name, unknown);
  assert.deepEqual(read('list-subagents', name), {tracked:false, subagents:[]});
 }
 console.log('PASS context unknown: Claude without capacity, both unqualified Cursor versions and visible child');
} else throw new Error('expected numeric or unknown');
