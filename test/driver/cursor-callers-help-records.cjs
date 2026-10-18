// Validate actual exchange payload and current scoped dispatcher results.
const fs = require('fs');
const [tracePath, plainPath, own, inventoryPath] = process.argv.slice(2);
let events;
try { events = fs.readFileSync(tracePath, 'utf8').trimEnd().split('\n').map(JSON.parse); }
catch (err) { if (err.code === 'ENOENT' || err instanceof SyntaxError) process.exit(1); throw err; }
const plain = fs.readFileSync(plainPath, 'utf8');
const registration = events.find(e => e.event === 'registration');
const response = events.find(e => e.event === 'response');
const notification = events.find(e => e.event === 'notification');
const calls = events.filter(e => e.event === 'tool_response');
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
