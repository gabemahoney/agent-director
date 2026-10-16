// Internal assembler used by diagnostic-overlay.mjs, which supplies the shared
// lifecycle/export helpers after these hooks. Canonical tests remain unchanged.
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import {execFileSync} from 'node:child_process';

if (process.env.HOME !== '/home/sandbox') throw Error('sandbox HOME required');
const [output] = process.argv.slice(2);
if (!output || output.split(/[\\/]/).includes('.claude')) throw Error('safe output required');
const root = '/work';
execFileSync('bun', ['tools/measure-decide/source-overlay.mjs', '--root', root, '--output', output], {stdio:'inherit'});
const map = JSON.parse(fs.readFileSync(path.join(output,'overlay.json')));
const pins = new Map(fs.readFileSync('tickets/handoff/b.ftd/Plans/b.jf1/e4-task1/engineer/canonical-tests.sha256','utf8').trim().split('\n').map(line => [line.slice(66),line.slice(0,64)]));
const sha = bytes => crypto.createHash('sha256').update(bytes).digest('hex');
const stamps = {};
function edit(file, replacements) {
  const bytes = fs.readFileSync(file);
  if (sha(bytes) !== pins.get(file)) throw Error('canonical test drift: '+file);
  let text = bytes.toString();
  for (const [before,after] of replacements) {
    if (text.split(before).length !== 2) throw Error('test anchor drift: '+before);
    text = text.replace(before,after);
  }
  const dest = path.join(output,file);
  fs.mkdirSync(path.dirname(dest),{recursive:true});
  fs.writeFileSync(dest,text);
  execFileSync('gofmt',['-w',dest]);
  map.Replace[path.join(root,file)] = dest;
  stamps[file] = {baseline:sha(bytes),overlay:sha(fs.readFileSync(dest))};
}
edit('internal/store/deadline_boundary_test.go', [
  ['import (','import (\n obs "github.com/gabemahoney/agent-director/internal/decideobserve"'],
  ['func (b *deadlineBoundary) Connect(ctx context.Context) (driver.Conn, error) {','func (b *deadlineBoundary) Connect(ctx context.Context) (driver.Conn, error) {\n obs.Event(ctx,"fixture.connector.enter",nil)'],
  ['func installDeadlineBoundary(t *testing.T, s *Store, path string) *deadlineBoundary {','func installDeadlineBoundary(t *testing.T, s *Store, path string) *deadlineBoundary {\n diagnosticRegister(t,path)'],
  ['return s.WithContext(ctx).DecideRelayRequest(relayID, tokenA, "allow", "", "test", time.Time{}, wait)', 'id:=diagnosticStoreBegin(s.Path(),"fixture.decision")\n root := obs.RootInvocation("fixture.decision",relayID,tokenA,id)\n ctx = obs.Bind(root,ctx)\n begin:=time.Now()\n written,err := s.WithContext(ctx).DecideRelayRequest(relayID, tokenA, "allow", "", "test", time.Time{}, wait)\n end:=time.Now()\n obs.Event(ctx,"fixture.decision.return",err)\n diagnosticReturned(id,begin,end,err,true)\n return written,err'],
]);
edit('internal/store/store_test.go', [
  ['os.Exit(m.Run())','code := m.Run()\n if err := diagnosticExport(); err != nil { fmt.Fprintln(os.Stderr,err); code=1 }; os.Exit(code)'],
]);
const virtual = 'internal/store/diagnostic_export_test.go';
const dest = path.join(output,virtual);
fs.writeFileSync(dest, fs.readFileSync('tools/measure-decide/calibration-export.go.in'));
execFileSync('gofmt',['-w',dest]);
map.Replace[path.join(root,virtual)] = dest;
fs.writeFileSync(path.join(output,'overlay.json'),JSON.stringify(map,null,2)+'\n');
fs.writeFileSync(path.join(output,'test-provenance.json'),JSON.stringify({canonical_mutations:[],tests:stamps,export_sha256:sha(fs.readFileSync(dest)),generator_sha256:sha(fs.readFileSync(import.meta.filename))},null,2)+'\n');
