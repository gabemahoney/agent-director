const fs = require('fs'), cp = require('child_process'), crypto = require('crypto');
const base = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-task3';
const own = base + '/commit-closeout-cancel-barrier-full';
const phase = process.argv[2];
const json = (p, v) => fs.writeFileSync(own + '/' + p, JSON.stringify(v, null, 2) + '\n', {mode: 0o600});
const hash = p => crypto.createHash('sha256').update(fs.readFileSync(p)).digest('hex');
const git = (...args) => cp.execFileSync('git', args, {encoding:'utf8'});
const allowed = p => !p.split('/').includes('.claude') && !p.startsWith(own + '/');
fs.mkdirSync(own, {recursive:true, mode:0o700});
if (phase === 'before') {
  fs.writeFileSync(own + '/gitconfig', '[safe]\n\tdirectory = /work\n', {mode:0o600});
  fs.writeFileSync(own + '/container.env', 'HOME=/home/sandbox\nGIT_CONFIG_GLOBAL=/work/' + own + '/gitconfig\n', {mode:0o600});
}
const tracked = new Set(git('ls-files', '-z').split('\0').filter(allowed));
const paths = [...new Set(git('ls-files', '-z', '--cached', '--others', '--exclude-standard').split('\0').filter(p => p && allowed(p)))].sort();
const manifest = paths.filter(p => fs.existsSync(p) && fs.statSync(p).isFile()).map(p => ({path:p, sha256:hash(p), bytes:fs.statSync(p).size, tracked:tracked.has(p)}));
json('input-manifest-' + phase + '.json', manifest);
const previous = JSON.parse(fs.readFileSync(base + '/commit-closeout-full/frozen-before.json'));
const frozen = Object.values(previous).flat().map(r => ({...r, actual:hash(r.path), matches:hash(r.path) === r.expected}));
json('frozen-' + phase + '.json', frozen);
const changed = frozen.find(r => r.path === 'pkg/api/decide_absolute_deadline_test.go');
if (frozen.filter(r => r !== changed).some(r => !r.matches)) throw new Error('Frozen file mismatch');
const newTest = manifest.find(r => r.path === 'internal/store/commit_branch_test.go');
if (!newTest || newTest.tracked) throw new Error('Expected untracked new branch test missing');
if (phase === 'before') {
  const listing = cp.execFileSync('go', ['list', '-f', '{{.ImportPath}}|{{.Dir}}|{{join .GoFiles ","}}|{{join .CgoFiles ","}}|{{join .TestGoFiles ","}}|{{join .XTestGoFiles ","}}', './...'], {encoding:'utf8', env:{...process.env, HOME:'/home/sandbox'}}).trim().split('\n');
  json('package-inventory.json', listing);
  const sources = listing.flatMap(line => {const [,dir,...parts]=line.split('|'); return parts.flatMap(part=>part.split(',').filter(Boolean).map(f=>dir.replace('/work/','')+'/'+f));}).sort();
  json('go-compiled-source-inventory.json', sources);
  json('snapshot-go-excluded.json', manifest.filter(r=>r.path.startsWith('tickets/') && r.path.endsWith('.go')).map(r=>r.path));
  json('run.json', {command:['make','test-sandbox'], head:git('rev-parse','HEAD').trim(), prepared:new Date().toISOString(), SANDBOX_FLAGS:'--env-file /home/horde/ad-codex/project/agent-director-ftd/'+own+'/container.env', container_HOME:'/home/sandbox', container_TMPDIR:'unset', umask:'022', canonical_input_count:manifest.length, compiled_go_source_count:sources.length, package_count:listing.length, new_untracked_test_included:true, note:'One bare make test-sandbox; no CMD override, GOFLAGS, instrumentation, overlays or retries.'});
} else {
  const before = JSON.parse(fs.readFileSync(own+'/input-manifest-before.json'));
  const old = new Map(before.map(r=>[r.path,r]));
  const changes = manifest.filter(r=>JSON.stringify(old.get(r.path))!==JSON.stringify(r)).map(r=>r.path).concat(before.filter(r=>!manifest.some(a=>a.path===r.path)).map(r=>r.path));
  json('input-comparison.json', {unchanged:changes.length===0, changes, before_sha256:hash(own+'/input-manifest-before.json'), after_sha256:hash(own+'/input-manifest-after.json'), absolute_deadline_current_sha256:changed.actual, frozen_unchanged_count:5});
}
console.log(JSON.stringify({phase, inputs:manifest.length, frozen_unchanged:5, absolute_deadline_current_sha256:changed.actual}));
