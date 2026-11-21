// One cached build per source/driver identity, outside the shared checkout.
const fs = require('node:fs');
const cp = require('node:child_process');
const crypto = require('node:crypto');
const path = require('node:path');
// The paired harness symlinks /work/source; Go keys overlays by physical paths.
const source = fs.realpathSync('/work/source'), driver = __dirname;
const commit = JSON.parse(cp.execFileSync('/usr/local/bin/agent-director',['version'],{encoding:'utf8'})).commit;
if (!/^[a-f0-9]{40}$/.test(commit)) throw Error('shipped binary lacks the snapshot commit stamp');
const hash = crypto.createHash('sha256');
for (const p of ['/usr/local/bin/agent-director', '/opt/bin/agent-director-admin', ...fs.readdirSync(driver).sort().map(n => path.join(driver,n))]) hash.update(fs.readFileSync(p));
const build = '/tmp/busy-send-install-build-' + hash.digest('hex').slice(0,16);
if (fs.existsSync(build+'/complete')) { console.log(build); process.exit(0); }
fs.mkdirSync(build, {recursive:true});
function run(args) { cp.execFileSync('go',args,{cwd:source,stdio:['ignore',2,2],env:{...process.env,CGO_ENABLED:'0',GOPROXY:'off',GOSUMDB:'off'}}); }
let adapter = fs.readFileSync(source+'/internal/adminapi/install_versions.go','utf8');
const needle = 'return publishVersion(ctx, installversion.Engine{Root: root}, runtimeBinary, self)';
if (adapter.split(needle).length !== 2) throw Error('PublishVersion signature/construction changed; update and review overlay');
adapter = adapter.replace(needle,'return publishVersion(ctx, installversion.Engine{Root: root, Hooks: installversion.Hooks{Observe: installTestObserve(root)}}, runtimeBinary, self)');
fs.writeFileSync(build+'/install_versions.go',adapter);
fs.copyFileSync(driver+'/observe.go.in',build+'/observe.go');
fs.writeFileSync(build+'/overlay.json',fs.readFileSync(driver+'/overlay.json','utf8').replaceAll('@SOURCE@',source).replaceAll('@BUILD@',build));
const files=cp.execFileSync('go',['list','-overlay',build+'/overlay.json','-f','{{join .GoFiles "\\n"}}','./internal/adminapi'],{cwd:source,encoding:'utf8'});
if (!files.split('\n').includes('install_test_observer.go')) throw Error('Go did not apply the observer overlay');
let coverage = fs.readFileSync(source+'/internal/store/busy_send_coverage.go','utf8');
const covered = 'var busySendCoveredAgents = [...]string{AgentClaude}';
if (coverage.split(covered).length !== 2) throw Error('coverage fixture changed; review shrinking candidate');
fs.writeFileSync(build+'/coverage.go',coverage.replace(covered,'var busySendCoveredAgents = [...]string{}'));
fs.writeFileSync(build+'/coverage.json',JSON.stringify({Replace:{[source+'/internal/store/busy_send_coverage.go']:build+'/coverage.go'}}));
for (const [name,stamp,overlay] of [['later','9.0.0-kx-instrumented',build+'/overlay.json'],['refused','9.0.0-kx-refused',build+'/coverage.json']]) {
  fs.mkdirSync(build+'/'+name,{recursive:true});
  for (const binary of ['agent-director','agent-director-admin']) {
    const args=['build','-buildvcs=false','-ldflags',`-X github.com/gabemahoney/agent-director/internal/version.Version=${stamp} -X github.com/gabemahoney/agent-director/internal/version.Commit=${commit}`];
    // The later runtime is unmodified; only its own admin has the observer.
    if (name==='refused'||binary==='agent-director-admin') args.push('-overlay',overlay);
    run([...args,'-o',`${build}/${name}/${binary}`,`./cmd/${binary}`]);
  }
  const facts=JSON.parse(cp.execFileSync(`${build}/${name}/agent-director-admin`,['version-facts'],{encoding:'utf8'}));
  const expected=name==='refused'?[]:['claude'];
  if (JSON.stringify(facts.covered_agents)!==JSON.stringify(expected)) throw Error('candidate compiled coverage does not match fixture: '+name);
}
run(['build','-buildvcs=false','-o',build+'/callers','./test/driver/busy-send-install']);
// Consume the actual image-built public TS package, verifying it belongs to this source.
cp.execFileSync('sha256sum',['--check','/opt/cursor-pane-bun/source-sha256'],{cwd:source+'/pkg/ts-bun-client',stdio:['ignore',2,2]});
fs.writeFileSync(build+'/complete','complete\n');
console.log(build);
