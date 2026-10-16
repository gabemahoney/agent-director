import fs from 'node:fs';
import crypto from 'node:crypto';
import {execFileSync} from 'node:child_process';
const base='tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/';
const out=base+'native-boundaries/';
const hash=b=>crypto.createHash('sha256').update(b).digest('hex');
const prior=JSON.parse(fs.readFileSync(base+'settled-full/input-manifest-after.json'));
const candidates={};
for(const p of ['cmd/agent-director/main.go','internal/cursorhost/requests.go','internal/store/cursor_permission.go','internal/store/relay_writes.go']) candidates[p]=execFileSync('git',['show','HEAD:'+p],{encoding:'utf8'});
let s=fs.readFileSync('internal/store/store.go','utf8');
s=s.replace('// foreign_keys is a per-connection PRAGMA. The context-aware connector\n\t// sets it before every connection is admitted to the pool.','// foreign_keys is a per-connection PRAGMA, so set it via DSN so every\n\t// connection the pool dials in starts with FKs enforced.')
.replace('// per-connection too. Connector initialization uses the acquisition\n\t// context; the driver\'s DSN PRAGMAs would use context.Background.','// per-connection too, so it is set via DSN as well.')
.replace('db, err := openContextSQLite(resolved, busyTimeoutMs)','dsn := fmt.Sprintf("%s?_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)", resolved, busyTimeoutMs)\n\n\tdb, err := sql.Open("sqlite", dsn)');
candidates['internal/store/store.go']=s;
s=fs.readFileSync('internal/store/connection_cleanup.go','utf8');
s=s.replace('\n\t"time"','').replace('\n\toperation     *connectionOperation','');
const begin=s.indexOf('type connectionCleanupLane struct {');
const end=s.indexOf('func (s *Store) releaseConn(');
s=s.slice(0,begin)+'type connectionCleanupLane struct {\n\tmu      sync.Mutex\n\trunning bool\n\tjobs    []connectionCleanupJob\n}\n\n'+s.slice(end);
candidates['internal/store/connection_cleanup.go']=s;
s=fs.readFileSync('internal/store/lockwait.go','utf8');
s=s.replace('context.WithTimeout(context.WithValue(ctx, connectionInitializationContextKey{}, ctx), connGrab)','context.WithTimeout(ctx, connGrab)');
const a=s.indexOf('\tif ctx.Done() != nil {',s.indexOf('func (s *Store) withConn('));
const b=s.indexOf('\tconn, busyMs, err := s.boundedConnContext',a);
s=s.slice(0,a)+s.slice(b);
const c=s.indexOf('\tcleanup := &connectionCleanup{');
const e=s.indexOf('\tif err := ctx.Err(); err != nil {',c);
s=s.slice(0,c)+'\tcleanup := &connectionCleanup{conn: conn, busyTimeoutMs: s.busyTimeoutMs}\n\tdefer s.releaseConn(ctx, cleanup)\n'+s.slice(e);
const f=s.indexOf('\t\tif cleanup.operation != nil {');
const g=s.indexOf('\t\tif _, err := conn.ExecContext(ctx, "COMMIT")',f);
s=s.slice(0,f)+'\t\tif err := ctx.Err(); err != nil {\n\t\t\treturn fmt.Errorf("%w: before commit: %w", ErrStoreBusy, err)\n\t\t}\n'+s.slice(g);
s=s.replace('\tif err := ctx.Err(); err != nil {\n\t\tcleanup.discard = true\n\t\treturn fmt.Errorf("%w: connection acquired after deadline: %w", ErrStoreBusy, err)\n\t}\n','')
.replace('\t\tif err := ctx.Err(); err != nil {\n\t\t\treturn fmt.Errorf("%w: before callback: %w", ErrStoreBusy, err)\n\t\t}\n','');
candidates['internal/store/lockwait.go']=s;
// Accept a reconstruction only when the retained source digest matches exactly.
const expectedLock=prior['internal/store/lockwait.go'];
const delta=Buffer.byteLength(s)-expectedLock.bytes;
if(hash(s)!==expectedLock.sha256 && delta>0) {
 for(let i=0;i<=s.length-delta;i++) {
  const candidate=s.slice(0,i)+s.slice(i+delta);
  if(hash(candidate)===expectedLock.sha256) { candidates['internal/store/lockwait.go']=candidate;break; }
 }
}
const results={};
for(const [p,bytes] of Object.entries(candidates)){
 const target=out+p.replaceAll('/','_')+'.txt';fs.writeFileSync(target,bytes);
 execFileSync('gofmt',['-w',target]);
 const actual=fs.readFileSync(target);
 results[p]={expected:prior[p].sha256,actual:hash(actual),matches:hash(actual)===prior[p].sha256,path:target,bytes:actual.length};
}
fs.writeFileSync(out+'reconstruction.json',JSON.stringify(results,null,2)+'\n');
console.log(JSON.stringify(results,null,2));
