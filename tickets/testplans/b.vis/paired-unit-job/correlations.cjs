const fs = require('fs');
const append = fs.appendFileSync;
fs.appendFileSync = function(path, data, ...args) {
  const result = append.call(this, path, data, ...args);
  const name = String(path);
  if (process.env.RETIREMENT_CORRELATIONS && (name.endsWith('/mcp.jsonl') || name.endsWith('/requests'))) {
    append.call(fs, process.env.RETIREMENT_CORRELATIONS, JSON.stringify({
      pid: process.pid, time: new Date().toISOString(), monotonic: String(process.hrtime.bigint()),
      path: name, data: String(data)
    }) + '\n');
  }
  return result;
};
