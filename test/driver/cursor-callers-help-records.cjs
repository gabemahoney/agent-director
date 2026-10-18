// Validate actual exchange payload and current scoped dispatcher results.
const fs = require('fs');
const [tracePath, plainPath, own, inventoryPath, mode] = process.argv.slice(2);
let events;
try { events = fs.readFileSync(tracePath, 'utf8').trimEnd().split('\n').map(JSON.parse); }
catch (err) { if (err.code === 'ENOENT' || err instanceof SyntaxError) process.exit(1); throw err; }
const plain = fs.readFileSync(plainPath, 'utf8');
const registration = events.find(e => e.event === 'registration');
const response = events.find(e => e.event === 'response');
const notification = events.find(e => e.event === 'notification');
const calls = events.filter(e => e.event === 'tool_response' && !e.control);
if (!registration || !response || !notification || calls.length !== 2) process.exit(1);
if (response.response.id !== 'cursor-mcp-1' || response.response.result.instructions !== plain) throw Error('canonical payload or correlation mismatch');
if (!(events.indexOf(response) < events.indexOf(notification) && events.indexOf(notification) < events.indexOf(calls[0]))) throw Error('exchange order mismatch');
for (const call of calls) {
 if (call.response.error || call.response.result.isError || call.response.result.content.length !== 1) throw Error('scoped tool failed');
 const result = JSON.parse(call.response.result.content[0].text);
 if (call.call.name === 'get' && result.instance_id !== own) throw Error('wrong actual store/parent');
 if (call.call.name === 'read-pane' && typeof result.pane !== 'string') throw Error('actual socket read missing');
}
const server = registration.servers[0];
const env = Object.fromEntries(server.env.map(e => [e.name, e.value]));
if (env.AGENT_DIRECTOR_INSTANCE_ID !== own || env.AGENT_DIRECTOR_AGENT !== 'cursor' || !env.TMUX || !env.TMUX_PANE) throw Error('scope route missing');
const owned = events.filter(e => e.event === 'started').map(e => ({pid:e.pid,start:e.start}));
if (!owned.length || owned.some(e => !Number.isInteger(e.pid) || !/^\d+$/.test(e.start || ''))) throw Error('owned MCP identity missing');
fs.writeFileSync(inventoryPath, JSON.stringify(owned)+'\n', {mode:0o600});
if (mode === 'wait-expiry') {
 const scope = JSON.parse(env.AGENT_DIRECTOR_CURSOR_MCP_SCOPE);
 // Date.parse truncates the serialized nanoseconds; one millisecond ensures
 // the original Go deadline has passed rather than rounding it backward.
 const remaining = Date.parse(scope.Until)+1-Date.now();
 if (!Number.isFinite(remaining) || remaining > 45000) throw Error('missing original observer bound');
 // Fixture timing only: the unchanged observer expiry precedes the restart.
 if (remaining > 0) Atomics.wait(new Int32Array(new SharedArrayBuffer(4)),0,0,remaining);
} else if (mode === 'restarted') {
 const stopped = events.find(e=>e.event==='stopped');
 const restarted = events.find(e=>e.event==='response' && e.attempt===2);
 const initialized = events.find(e=>e.event==='notification' && e.attempt===2);
 const useful = events.filter(e=>e.event==='tool_response' && e.attempt===2);
 if (!stopped || owned.length!==2 || owned[0].pid===owned[1].pid || !restarted || !initialized || !useful.length) throw Error('configured restart evidence incomplete');
 if (restarted.response.id!=='cursor-mcp-2' || restarted.response.result.instructions!==plain) throw Error('replacement canonical initialization mismatch');
 if (!(events.indexOf(stopped)<events.indexOf(restarted) && events.indexOf(restarted)<events.indexOf(initialized) && events.indexOf(initialized)<events.indexOf(useful[0]))) throw Error('restart order mismatch');
 for (const call of useful) if (call.response.error || call.response.result.isError) throw Error('replacement useful call failed');
}
