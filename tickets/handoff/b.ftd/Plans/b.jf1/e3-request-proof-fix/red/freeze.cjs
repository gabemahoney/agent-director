const fs = require('fs');
const cp = require('child_process');
const crypto = require('crypto');
const out = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-request-proof-fix/red';
const tracked = cp.execFileSync('git', ['ls-files', '-z', '--', 'internal', 'pkg', 'cmd', 'test', 'tickets/testplans/b.jf1/t1.jf1.cr', 'Makefile', 'go.mod', 'go.sum'], {encoding:'utf8'}).split('\0').filter(Boolean);
const hash = p => crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex');
const collect = paths => Object.fromEntries(paths.sort().map(p => [p,hash(p)]));
const sources = tracked.filter(p => p.endsWith('.go') && !p.endsWith('_test.go'));
const protectedFiles = tracked.filter(p => p.startsWith('tickets/testplans/b.jf1/t1.jf1.cr/') || p.startsWith('test/driver/'));
const tests = ['pkg/api/cursor_terminal_request_proof_test.go','internal/store/cursor_terminal_request_proof_test.go','pkg/api/cursor_startup_liveness_test.go','pkg/api/cursor_liveness_list_test.go','pkg/api/cursor_liveness_trail_deadline_test.go','internal/store/cursor_startup_liveness_test.go','internal/cursorhost/session_e3_regression_linux_test.go',out+'/sandbox-command.sh',out+'/freeze.cjs'].filter(p => fs.existsSync(p));
const snapshot = {head:cp.execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim(),sources:collect(sources),protectedFiles:collect(protectedFiles),tests:collect(tests)};
fs.writeFileSync(out+'/'+process.argv[2]+'.json',JSON.stringify(snapshot,null,2)+'\n');
if (process.argv[2] === 'after') {
 const before = JSON.parse(fs.readFileSync(out+'/before.json'));
 for (const section of ['sources','protectedFiles']) {
  if (JSON.stringify(before[section]) !== JSON.stringify(snapshot[section])) throw new Error(section+' changed');
 }
}
console.log(process.argv[2]+': '+sources.length+' source hashes, '+protectedFiles.length+' protected collector/driver hashes, '+tests.length+' test/runner hashes');
