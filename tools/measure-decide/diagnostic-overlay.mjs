// Test Writer overlay: canonical test bytes are pinned and never rewritten.
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import {execFileSync} from 'node:child_process';
const [output,mode='on']=process.argv.slice(2);
if (!['on','off'].includes(mode)) throw Error('mode on/off');
execFileSync('bun',['tools/measure-decide/calibration-overlay.mjs',output],{stdio:'inherit'});
const map=JSON.parse(fs.readFileSync(path.join(output,'overlay.json')));
const sourceMap=JSON.parse(fs.readFileSync(path.join(output,'overlay.json'))).Replace;
const sourcePins=JSON.parse(fs.readFileSync(path.join(output,'provenance.json')));
for(const file of [...Object.keys(sourcePins.baseline_sha256),'internal/decideobserve/recorder.go']) {
 if(!sourceMap['/work/'+file]) throw Error('missing source/recorder mapping '+file);
}
if(mode==='off') map.Replace={};
const root='/work';
const sha=b=>crypto.createHash('sha256').update(b).digest('hex');
const pins=new Map(fs.readFileSync('tickets/handoff/b.ftd/Plans/b.jf1/e4-task1/engineer/canonical-tests.sha256','utf8').trim().split('\n').map(l=>[l.slice(66),l.slice(0,64)]));
const stamps={};
function edit(file,pairs) {
 const bytes=fs.readFileSync(file);if(sha(bytes)!==pins.get(file))throw Error('test drift: '+file);
 let text=bytes.toString();
 for(const [a,b]of pairs){if(text.split(a).length!==2)throw Error('anchor drift: '+file+' '+a);text=text.replace(a,b)}
 const dest=path.join(output,file);fs.mkdirSync(path.dirname(dest),{recursive:true});fs.writeFileSync(dest,text);execFileSync('gofmt',['-w',dest]);map.Replace[path.join(root,file)]=dest;
 stamps[file]={baseline:sha(bytes),overlay:sha(fs.readFileSync(dest))};
}
function virtual(file,text) {const dest=path.join(output,file);fs.mkdirSync(path.dirname(dest),{recursive:true});fs.writeFileSync(dest,text);execFileSync('gofmt',['-w',dest]);map.Replace[path.join(root,file)]=dest;}
const exportCode=mode==='on'?`diagnosticMu.Lock();defer diagnosticMu.Unlock()
 if len(diagnosticSnapshots)>=256 {return fmt.Errorf("diagnostic snapshot count cap exceeded")}
 dir:=os.Getenv("DECIDE_REPORT"); if dir=="" {return fmt.Errorf("DECIDE_REPORT missing")}
 base:=fmt.Sprintf("snapshot-%s-%s-%06d.jsonl",diagnosticProcess,label,diagnosticSeq.Add(1))
 item:=map[string]any{"file":base,"label":label,"state":"expected"}
 diagnosticSnapshots=append(diagnosticSnapshots,item)
 if err:=diagnosticInventory("exporting");err!=nil{return err}
 f,err:=os.OpenFile(filepath.Join(dir,base),os.O_CREATE|os.O_EXCL|os.O_WRONLY,0600);if err!=nil{return err}
 writer:=&diagnosticTraceWriter{file:f}
 err=obs.Export(writer);stat,se:=f.Stat();ce:=f.Close();if err!=nil{return err};if se!=nil{return se};if ce!=nil{return ce}
 item["state"]="completed";item["bytes"]=stat.Size()
 if err:=diagnosticInventory("exporting");err!=nil{return err}`:'';
const drain=`until:=time.Now().Add(3*time.Second)
 for trail.Default().Status().Pending>0 && time.Now().Before(until) {time.Sleep(10*time.Millisecond)}
 if err:=diagnosticWrite("drain",map[string]any{"pending":trail.Default().Status().Pending,"observation_bound_ms":3000});err!=nil{return err}`;
function io(pkg){return fs.readFileSync('tools/measure-decide/diagnostic-io.go.in','utf8').replaceAll('PACKAGE',pkg).replace('"MODE"',JSON.stringify(mode)).replace('// OBS_IMPORT',(mode==='on'?'obs "github.com/gabemahoney/agent-director/internal/decideobserve"\n':'')+'"github.com/gabemahoney/agent-director/internal/trail"').replace('// INVOCATION_TYPE',mode==='on'?`type diagnosticInvocation = obs.Invocation
type diagnosticTraceWriter struct {file *os.File}
func (w *diagnosticTraceWriter) Write(p []byte)(int,error) {
 if diagnosticArtifactBytes+int64(len(p))>128<<20 {return 0,fmt.Errorf("trace quota exceeded")}
 n,err:=w.file.Write(p);diagnosticArtifactBytes+=int64(n);return n,err
}`:'type diagnosticInvocation struct {Process,Test,Fixture,Call string}').replace('// OBS_EXPORT',exportCode).replace('// FINAL_DRAIN',drain);}
const observed=mode==='on'?'Observation: diagnosticObserved(diagnosticID), ':'';
virtual('pkg/api/diagnostic_io_test.go',io('api_test'));
virtual('pkg/api/diagnostic_calls_test.go',fs.readFileSync('tools/measure-decide/diagnostic-api.go.in','utf8'));
edit('pkg/api/example_main_test.go',[['code := m.Run()','code := m.Run()\n if err:=diagnosticExport();err!=nil {fmt.Fprintln(os.Stderr,err);code=1}']]);
const helperStart='res, err := api.DecideWithSleep(s, e.view(), func(d time.Duration) {';
const helperEnd='}, api.DecideParams{InstanceID: e.id, RequestToken: storefix.TestRequestTokenA, Decision: decision, MaxWaitMs: maxWaitMs})';
const direct='res, err := api.DecideWithSleep(s, api.RelayView{Now: func() time.Time { return now }}, func(d time.Duration) { now = now.Add(d) }, api.DecideParams{InstanceID: "id-d-1", RequestToken: storefix.TestRequestTokenA, Decision: "deny", MaxWaitMs: &setupBudget})';
edit('pkg/api/decide_delivery_test.go',[
 [helperStart,'res, err := diagnosticTimed(t,e.dbPath,func(diagnosticID diagnosticInvocation)(api.DecideResult,error){ return api.DecideWithSleep(s, e.view(), func(d time.Duration) {'],
 [helperEnd,helperEnd.replace('api.DecideParams{','api.DecideParams{'+observed)+' })'],
 [direct,'res, err := diagnosticTimed(t,path,func(diagnosticID diagnosticInvocation)(api.DecideResult,error){return '+direct.slice('res, err := '.length).replace('api.DecideParams{','api.DecideParams{'+observed)+'})']
]);
edit('pkg/api/decide_absolute_deadline_test.go',[
 ['start := time.Now()\n\t_, err := api.Decide','diagnosticID:=diagnosticBegin(t,e.dbPath,"helper")\n start := time.Now()\n _, err := api.Decide'],
 ['api.DecideParams{InstanceID: e.id, RequestToken: storefix.TestRequestTokenA, Decision: "allow", MaxWaitMs: ptr(int64(100))}','api.DecideParams{'+observed+'InstanceID: e.id, RequestToken: storefix.TestRequestTokenA, Decision: "allow", MaxWaitMs: ptr(int64(100))}'],
 ['if !errors.Is(err, api.ErrStoreBusy) || time.Since(start) > 1100*time.Millisecond {','diagnosticParkedReturn := time.Now()\n if !errors.Is(err, api.ErrStoreBusy) || time.Since(start) > 1100*time.Millisecond {'],
 ['\tselect {\n\tcase <-s.entered:','\tdiagnosticReturned(diagnosticID,start,diagnosticParkedReturn,err,true)\n select {\n case <-s.entered:'],
 ['start := time.Now()\n\tgo func() {','diagnosticClientID:=diagnosticBegin(t,e.dbPath,"client")\n start := time.Now()\n go func() {'],
 ['result, err := c.Decide(api.DecideParams{InstanceID: e.id, RequestToken: storefix.TestRequestTokenA, Decision: "allow", MaxWaitMs: ptr(int64(500))})','diagnosticID:=diagnosticClientID\n result, err := c.Decide(api.DecideParams{'+observed+'InstanceID: e.id, RequestToken: storefix.TestRequestTokenA, Decision: "allow", MaxWaitMs: ptr(int64(500))})\n diagnosticReturned(diagnosticID,start,time.Now(),err,false)'],
 ['if _, err := c.Decide(api.DecideParams{}); !errors.Is(err, api.ErrClientClosed) {','diagnosticID:=diagnosticBegin(t,e.dbPath,"client")\n diagnosticClosedStart:=time.Now()\n _,diagnosticClosedErr:=c.Decide(api.DecideParams{'+observed+'})\n diagnosticReturned(diagnosticID,diagnosticClosedStart,time.Now(),diagnosticClosedErr,true)\n if err:=diagnosticClosedErr; !errors.Is(err, api.ErrClientClosed) {'],
 ['\tif got := trail.Default().Status().Pending;', '\tdiagnosticCapture(t,e.dbPath,"held-client-caller");diagnosticReadback(t,e.dbPath,"held-client-caller")\n if got := trail.Default().Status().Pending;'],
 ['\tvar events []string','\tdiagnosticCapture(t,e.dbPath,"held-client-eventual");diagnosticReadback(t,e.dbPath,"held-client-eventual")\n var events []string']
]);
edit('pkg/api/cursor_liveness_list_test.go',[
 ['got, err := c.List(params)\n\t\t\t\tif err != nil {','diagnosticID:=diagnosticBegin(t,e.dbPath,"client.list")\n '+(mode==='on'?'params.Observation=diagnosticID\n ':'')+'diagnosticStart:=time.Now()\n got, err := c.List(params)\n diagnosticEnd:=time.Now()\n diagnosticReturned(diagnosticID,diagnosticStart,diagnosticEnd,err,false)\n diagnosticCapture(t,e.dbPath,"list-caller");diagnosticReadback(t,e.dbPath,"list-caller",diagnosticID)\n if err!=nil {diagnosticUncertain(t,e.dbPath,diagnosticID)}\n if err != nil {'],
 ['"testing"','"testing"\n "time"']
]);
edit('pkg/api/decide_trail_deadline_test.go',[
 ['start := time.Now()\n\tconst limit','diagnosticID:=diagnosticBegin(t,e.dbPath,"helper")\n start := time.Now()\n const limit'],
 ['api.DecideParams{InstanceID: e.id,\n\t\t\tRequestToken: storefix.TestRequestTokenA, Decision: "allow", MaxWaitMs: ptr(int64(500))})','api.DecideParams{'+observed+'InstanceID: e.id,\n RequestToken: storefix.TestRequestTokenA, Decision: "allow", MaxWaitMs: ptr(int64(500))})\n diagnosticReturned(diagnosticID,start,time.Now(),err,false)'],
 ['\trelease()\n\tif !returned {','\tdiagnosticCapture(t,e.dbPath,"held-trail-caller");diagnosticReadback(t,e.dbPath,"held-trail-caller")\n release()\n if !returned {']
]);
{
 virtual('internal/store/diagnostic_io_test.go',io('store'));
 edit('internal/store/commit_branch_test.go',[
 ['\t\t\tadvice :=','\t\t\tdiagnosticCapture(t,path,"commit-branch-caller");diagnosticReadback(t,path,"commit-branch-caller")\n advice :='],
 ['\t\t\t\tassertDeadlineDatabaseReusable(t, s, path, "allow")','\t\t\t\tassertDeadlineDatabaseReusable(t, s, path, "allow")\n diagnosticCapture(t,path,"commit-branch-eventual");diagnosticReadback(t,path,"commit-branch-eventual")']
 ]);
 edit('internal/store/native_boundary_test.go',[
 ['func installNativeBoundary(t *testing.T, s *Store, path string) (*deadlineBoundary, *nativeBoundaryDriver) {','func installNativeBoundary(t *testing.T, s *Store, path string) (*deadlineBoundary, *nativeBoundaryDriver) {\n diagnosticRegister(t,path)'],
 ['\t\t\tinitialStatements, initialCallbacks :=','\t\t\tdiagnosticCapture(t,path,"native-caller");diagnosticReadback(t,path,"native-caller")\n initialStatements, initialCallbacks :='],
 ['\t\t\tassertDeadlineDatabaseReusable(t, s, path, wantDecision)','\t\t\tassertDeadlineDatabaseReusable(t, s, path, wantDecision)\n diagnosticCapture(t,path,"native-eventual");diagnosticReadback(t,path,"native-eventual")']
 ]);
}
fs.writeFileSync(path.join(output,'overlay.json'),JSON.stringify(map,null,2)+'\n');
fs.writeFileSync(path.join(output,'driver-provenance.json'),JSON.stringify({mode,source:sourcePins,canonical_mutations:[],tests:stamps,generator_sha256:sha(fs.readFileSync(import.meta.filename)),templates:['diagnostic-api.go.in','diagnostic-io.go.in'].map(p=>({path:p,sha256:sha(fs.readFileSync('tools/measure-decide/'+p))})),mappings:Object.fromEntries(Object.entries(map.Replace).map(([p,dest])=>[p,{destination:dest,sha256:sha(fs.readFileSync(dest))}]))},null,2)+'\n');
