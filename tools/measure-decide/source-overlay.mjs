// Run only through make sandbox CMD. Produces Go -overlay inputs, never edits source.
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import {execFileSync} from 'node:child_process';

const baseline = '8decf0d46914fef350c1fca0ba6af9348ba43a1a';
// Read-only authority seam; preserve the original six transformation pins.
const storeIdentitySource = 'internal/store/store.go';
const storeIdentitySHA256 = 'd625550192fd4959e9042443b7b7d84d19767f0bb9735914452720501a411fbe';
const files = ['pkg/api/decide.go', 'internal/store/lockwait.go', 'internal/store/connection_cleanup.go',
  'internal/store/permission.go', 'internal/trail/deadline.go', 'internal/trail/trail.go',
  'pkg/api/list.go', 'pkg/api/cursor_liveness.go', 'internal/store/find_missing_writes.go'];
const extensionPins = {
  'pkg/api/list.go': '07f88530c1f5f19ed48748ee273a79ce67e602a0acebe14d1c43eccf47f9c714',
  'pkg/api/cursor_liveness.go': 'd1480700adf513abd03be8d1cdb68c5948b04cf565a2d6c4c2900a1d6e8a4edb',
  'internal/store/find_missing_writes.go': '88a296cf7011e2447934bcde607d9b36073c9c7113415d43ed02ddcfb20dfb09',
};
const sha = s => crypto.createHash('sha256').update(s).digest('hex');
function safe(p) {
  if (p.split(/[\\/]/).includes('.claude')) throw Error('forbidden path component');
  return path.resolve(p);
}
function rep(s, old, next, count = 1) {
  const found = s.split(old).length - 1;
  if (found !== count) throw Error(`anchor drift: expected ${count}, got ${found}: ${old.slice(0,100)}`);
  return s.split(old).join(next);
}
function transform(file, source) {
  let s = rep(source, 'import (', 'import (\n obs "github.com/gabemahoney/agent-director/internal/decideobserve"');
  const r = (a,b,n=1) => { s=rep(s,a,b,n); };
  if (file === files[0]) {
    r('type DecideParams struct {', 'type DecideParams struct {\n Observation obs.Invocation `json:"-"`');
    for (const [sig, route] of [
      ['func decide(s DecideStore, v RelayView, sleep func(time.Duration), params DecideParams) (DecideResult, error) {','helper'],
      ['func (c *Client) Decide(params DecideParams) (DecideResult, error) {','client']]) {
      r(sig, sig.replace('(DecideResult, error)', '(observedResult DecideResult, observedErr error)') + `
 observedRoot := obs.RootInvocation("${route}", params.InstanceID, params.RequestToken, params.Observation)
 defer func() { obs.Event(observedRoot, "${route}.return_after_defers", observedErr) }()`);
    }
    r('ctx, cancel := decideContext(params)\n\tdefer cancel()', `ctx, cancel := decideContext(params)
 ctx = obs.Bind(observedRoot, ctx)
 observedRoot = ctx
 obs.Event(ctx, "original_context.created", nil)
 defer cancel()`,2);
    r('func decideWithin(ctx context.Context, s DecideStore, v RelayView, sleep func(time.Duration), params DecideParams) (DecideResult, error) {', `func decideWithin(ctx context.Context, s DecideStore, v RelayView, sleep func(time.Duration), params DecideParams) (observedResult DecideResult, observedErr error) {
 ctx = obs.Child(ctx, "api.body")
 defer func() { obs.Event(ctx, "api.body.return", observedErr) }()`);
    for (const [name, type] of [['readRequest','PermissionRow'], ['getSpawn','Spawn'], ['readAllRequests','[]PermissionRow']]) {
      r(`func (c *decideCall) ${name}() (${type}, error) {`, `func (c *decideCall) ${name}() (observedRow ${type}, observedErr error) {
 observedCtx := obs.Child(c.ctx, "api.${name}")
 defer func() { obs.Event(observedCtx, "api.${name}.return", observedErr) }()`);
    }
    r('pr, verdict, err := readOneJudged(c.readRequest, c.j)', `obs.Event(ctx, "api.judgment.enter", nil)
 pr, verdict, err := readOneJudged(c.readRequest, c.j)
 obs.Event(ctx, "api.judgment.return", err)`);
    r('c.ctx = ctx','c.ctx = ctx\n obs.Value(ctx, "relay.deadline_unix_ns", c.deadline.UnixNano())');
    r('func (c *decideCall) awaitAck(pr PermissionRow) DecideResult {', `func (c *decideCall) awaitAck(pr PermissionRow) DecideResult {
 obs.Event(c.ctx, "api.ack.enter", nil)
 defer obs.Event(c.ctx, "api.ack.return", nil)`);
    r('\t\tc.sleep(wait)', '\t\tobs.Value(c.ctx, "relay.sleep_requested_ns", int64(wait))\n\t\tc.sleep(wait)\n\t\tobs.Event(c.ctx, "relay.sleep.return", nil)');
  } else if (file === files[1]) {
    s += `
// WithObservation preserves the original context and the existing per-call view.
func (s *Store) WithObservation(root context.Context) *Store {
 ctx := s.callContext
 if ctx == nil { ctx = context.Background() }
 return s.WithContext(obs.Bind(root, ctx))
}
`;
    r('ctx, cancel := s.operationContext(maxWait)\n\tdefer cancel()', 'ctx, cancel := s.operationContext(maxWait)\n obs.Event(ctx, "operation_context.created", nil)\n\tdefer cancel()',2);
    r('func (s *Store) withConnContext(ctx context.Context, maxWait time.Duration, fn func(context.Context, *sql.Conn, int, *connectionCleanup) error) error {', `func (s *Store) withConnContext(ctx context.Context, maxWait time.Duration, fn func(context.Context, *sql.Conn, int, *connectionCleanup) error) (observedErr error) {
 ctx = obs.Child(ctx, "operation")
 obs.StoreIdentity(ctx, s.Path())
 defer func() { obs.Event(ctx, "operation.caller_return", observedErr) }()`);
    r('conn, err := s.db.Conn(acquireCtx)', 'obs.Boundary(ctx, acquireCtx, "pool.acquire.enter", nil)\n conn, err := s.db.Conn(acquireCtx)\n obs.Boundary(ctx, acquireCtx, "pool.acquire.return", err)',2);
    r('\t\tdefer func() {\n\t\t\top.mu.Lock()\n\t\t\top.err, op.completed = result, true\n\t\t\top.mu.Unlock()\n\t\t}()', `		defer func() {
 obs.Event(ctx, "sql.publication.lock_wait", nil)
 op.mu.Lock()
 op.err, op.completed = result, true
 obs.Event(ctx, "sql.published_locked", result)
 op.mu.Unlock()
 obs.Event(ctx, "sql.publication.unlocked", result)
 }()`);
    r('cleanup.operation.commitStarted = true','cleanup.operation.commitStarted = true\n obs.Event(ctx, "commit.started_locked", nil)');
    r('\t\tif _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {', `		obs.Event(ctx, "commit.exec.enter", nil)
 _, observedCommitErr := conn.ExecContext(ctx, "COMMIT")
 obs.Event(ctx, "commit.exec.return", observedCommitErr)
 if err := observedCommitErr; err != nil {`);
    r('\t\t_, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE")', '\t\tobs.Event(ctx, "begin.attempt.enter", nil)\n\t\t_, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE")\n\t\tobs.Event(ctx, "begin.attempt.return", err)');
    r('\t\tif err := fn(ctx, conn); err != nil {', '\t\tobs.Event(ctx, "transaction.body.enter", nil)\n observedBodyErr := fn(ctx, conn)\n obs.Event(ctx, "transaction.body.return", observedBodyErr)\n if err := observedBodyErr; err != nil {');
    r('\t\tif _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", sqliteBusyMs)); err != nil {', `		obs.Event(ctx, "setup.busy_timeout.enter", nil)
 _, observedSetupErr := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", sqliteBusyMs))
 obs.Event(ctx, "setup.busy_timeout.return", observedSetupErr)
 if err := observedSetupErr; err != nil {`);
    r('\treturn fn(ctx, conn, busyMs, cleanup)', '\tobs.Value(ctx, "setup.effective_busy_ms", int64(busyMs))\n obs.Event(ctx, "sql.callback.enter", nil)\n defer func() { obs.Event(ctx, "sql.callback.return", result) }()\n return fn(ctx, conn, busyMs, cleanup)');
  } else if (file === files[2]) {
    r('\tselect {\n\tcase lane.operationGate <- struct{}{}:', '\tobs.Boundary(ctx, admitCtx, "gate.wait.enter", nil)\n\tselect {\n\tcase lane.operationGate <- struct{}{}:\n\t\tobs.Boundary(ctx, admitCtx, "gate.acquired", nil)');
    r('\tcase <-admitCtx.Done():', '\tcase <-admitCtx.Done():\n obs.Boundary(ctx, admitCtx, "gate.refused", admitCtx.Err())');
    r('\t\t<-lane.operationGate','\t\t<-lane.operationGate\n obs.Event(ctx, "gate.released_expired", nil)');
    r('\tgo func() {\n\t\tdefer func() { <-lane.operationGate }()', '\tobs.Event(ctx, "worker.dispatch", nil)\n\tgo func() {\n obs.Event(ctx, "worker.enter", nil)\n defer func() { <-lane.operationGate; obs.Event(ctx, "gate.released", nil) }()');
    r('\t\top.err, op.completed = err, true', '\t\top.err, op.completed = err, true\n obs.Event(ctx, "worker.final_published_locked", err)');
    r('\t\tclose(done)','\t\tobs.Event(ctx, "worker.complete_before_done", err)\n\t\tclose(done)');
    r('\tcase <-done:\n\t\treturn op.err','\tcase <-done:\n obs.Event(ctx, "guard.done", op.err)\n\t\treturn op.err');
    r('\t\tdefer op.mu.Unlock()\n\t\tif op.completed {', `		defer op.mu.Unlock()
 observedState := int64(0)
 if op.completed { observedState |= 1 }
 if op.commitStarted { observedState |= 2 }
 if op.abandoned { observedState |= 4 }
 obs.Value(ctx, "guard.state_locked", observedState)
 obs.Event(ctx, "guard.error_locked", op.err)
 if op.completed {`);
    r('\t\top.abandoned = true','\t\top.abandoned = true\n obs.Event(ctx, "guard.abandoned_locked", nil)');
    r('func (s *Store) releaseConn(ctx context.Context, cleanup *connectionCleanup) {', 'func (s *Store) releaseConn(ctx context.Context, cleanup *connectionCleanup) {\n obs.Event(ctx, "cleanup.queue.enter", nil)\n defer obs.Event(ctx, "cleanup.queue.caller_return", nil)');
    r('\t\tjob.cleanup.finish(job.ctx)','\t\tobs.Event(job.ctx, "cleanup.dequeued", nil)\n\t\tjob.cleanup.finish(job.ctx)');
    r('func (c *connectionCleanup) finish(ctx context.Context) {', `func (c *connectionCleanup) finish(ctx context.Context) {
 obs.Event(ctx, "cleanup.enter", nil)
 defer obs.Event(ctx, "cleanup.return", nil)
 observedFlags := int64(0)
 if c.rollback { observedFlags |= 1 }
 if c.restore { observedFlags |= 2 }
 if c.discard { observedFlags |= 4 }
 obs.Value(ctx, "cleanup.flags", observedFlags)`);
    for (const [phase, expr] of [['rollback','c.conn.ExecContext(ctx, "ROLLBACK")'], ['restore','c.conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", c.busyTimeoutMs))']]) {
      r(`\t\tif _, err := ${expr}; err != nil {`, `\t\tobs.Event(ctx, "cleanup.${phase}.enter", nil)\n _, observedErr := ${expr}\n obs.Event(ctx, "cleanup.${phase}.return", observedErr)\n if err := observedErr; err != nil {`);
    }
    r('\t\t_ = c.conn.Raw(func(any) error { return driver.ErrBadConn })','\t\tobs.Event(ctx, "cleanup.discard.enter", nil)\n observedErr := c.conn.Raw(func(any) error { return driver.ErrBadConn })\n obs.Event(ctx, "cleanup.discard.return", observedErr)');
    r('\t_ = c.conn.Close()','\tobs.Event(ctx, "lease.close.enter", nil)\n observedErr := c.conn.Close()\n obs.Event(ctx, "lease.close.return", observedErr)');
  } else if (file === files[3]) {
    r('\t\terr := conn.QueryRowContext(ctx, decideRelayRequestSQL, args...).Scan(&requestID, &toolName)', '\t\tobs.Event(ctx, "guarded_update.scan.enter", nil)\n\t\terr := conn.QueryRowContext(ctx, decideRelayRequestSQL, args...).Scan(&requestID, &toolName)\n obs.Event(ctx, "guarded_update.scan.return", err)\n obs.Value(ctx, "guarded_update.request_id", requestID)');
    r('\tif written {\n\t\temitDecisionCommittedContext','\tif written {\n obs.Value(s.callContext, "row_trail.request_id", requestID)\n obs.Event(s.callContext, "row_trail.enter", nil)\n defer obs.Event(s.callContext, "row_trail.return", nil)\n\t\temitDecisionCommittedContext');
  } else if (file === files[4]) {
    r('type emission struct {','type emission struct {\n observedContext context.Context');
    r('func (w *Writer) emitWithin(ctx context.Context, event string, fields map[string]any) error {', `func (w *Writer) emitWithin(ctx context.Context, event string, fields map[string]any) (observedErr error) {
 ctx = obs.Child(ctx, "trail."+event)
 defer func() { obs.Event(ctx, "trail.caller_return", observedErr) }()`);
    r('job := emission{event: event,','job := emission{observedContext: ctx, event: event,');
    r('\tw.queueMu.Lock()\n\tif w.stopping {','\tobs.Event(ctx, "trail.queue_lock.wait", nil)\n\tw.queueMu.Lock()\n obs.Event(ctx, "trail.queue_lock.acquired", nil)\n\tif w.stopping {');
    r('\tcase w.queue <- job:\n\t\tw.delivery.Pending++','\tcase w.queue <- job:\n obs.Event(ctx, "trail.accepted", nil)\n\t\tw.delivery.Pending++');
    r('\t\terr := w.emit(job.event, job.fields)','\t\tobs.Event(job.observedContext, "trail.dequeued", nil)\n\t\terr := w.emitObserved(job.observedContext, job.event, job.fields)\n obs.Event(job.observedContext, "trail.worker_result", err)');
    r('\t\tjob.done <- err','\t\tjob.done <- err\n obs.Event(job.observedContext, "trail.result_published", err)');
  } else if (file === files[5]) {
    r('func Emit(ctx context.Context, event string, fields map[string]any) error {', `func Emit(ctx context.Context, event string, fields map[string]any) (observedErr error) {
 ctx = obs.Child(ctx, "trail.submit."+event)
 defer func() { obs.Event(ctx, "trail.submit.return", observedErr) }()`);
    r('\treturn w.emit(event, fields)\n}', '\treturn w.emitObserved(ctx, event, fields)\n}');
    r('func (w *Writer) emit(event string, fields map[string]any) error {', `func (w *Writer) emit(event string, fields map[string]any) error {
 return w.emitObserved(context.Background(), event, fields)
}
func (w *Writer) emitObserved(ctx context.Context, event string, fields map[string]any) (observedErr error) {
 obs.Event(ctx, "trail.mutex.wait", nil)
 defer func() { obs.Event(ctx, "trail.emit.return_after_unlock", observedErr) }()`);
    r('\tw.mu.Lock()\n\tdefer w.mu.Unlock()\n\tif w.closed {','\tw.mu.Lock()\n obs.Event(ctx, "trail.mutex.acquired", nil)\n\tdefer w.mu.Unlock()\n\tif w.closed {');
    r('if _, err := w.f.Write(line); err != nil {','obs.Event(ctx, "trail.write.enter", nil)\n _, observedWriteErr := w.f.Write(line)\n obs.Event(ctx, "trail.write.return", observedWriteErr)\n if err := observedWriteErr; err != nil {');
    r('if err := w.f.Sync(); err != nil {','obs.Event(ctx, "trail.sync.enter", nil)\n observedSyncErr := w.f.Sync()\n obs.Event(ctx, "trail.sync.return", observedSyncErr)\n if err := observedSyncErr; err != nil {');
  } else if (file === files[6]) {
    r('type ListParams struct {', 'type ListParams struct {\n Observation obs.Invocation `json:"-"`');
    const sig = 'func (c *Client) List(params ListParams) (ListResult, error) {';
    r(sig, `func (c *Client) List(params ListParams) (observedResult ListResult, observedErr error) {
 observedRoot := obs.RootInvocation("client.list", "", "", params.Observation)
 defer func() { obs.Event(observedRoot, "client.list.return_after_defers", observedErr) }()`);
    r('\tcandidates := filters', '\tobservedStore := c.st.WithObservation(observedRoot)\n\tcandidates := filters');
    r('rows, err := c.st.ListSpawns(candidates)', 'rows, err := observedStore.ListSpawns(candidates)');
    r('c.reconcileCursorGateway(row.InstanceID)', 'c.reconcileCursorGatewayWithStore(row.InstanceID, observedStore)');
    r('return listFiltered(c.st, c.relayView(), filters)', 'return listFiltered(observedStore, c.relayView(), filters)');
  } else if (file === files[7]) {
    // This file needs no recorder import: only thread the same per-call store.
    s = rep(s, '\n obs "github.com/gabemahoney/agent-director/internal/decideobserve"', '');
    r('func (c *Client) reconcileCursorGateway(instanceID string) error {', `func (c *Client) reconcileCursorGateway(instanceID string) error {
 return c.reconcileCursorGatewayWithStore(instanceID, c.st)
}
func (c *Client) reconcileCursorGatewayWithStore(instanceID string, observedStore *store.Store) error {`);
    r('c.st.', 'observedStore.', 4);
  } else if (file === files[8]) {
    r('func (s *Store) terminalMarkWithoutWait(errPrefix, stmt string, args []any, instanceID, reason string) (string, CondResult, error) {', `func (s *Store) terminalMarkWithoutWait(errPrefix, stmt string, args []any, instanceID, reason string) (observedPrior string, observedResult CondResult, observedErr error) {
 observedCtx := obs.Child(s.callContext, "terminal_mark")
 defer func() { obs.Event(observedCtx, "terminal_mark.return_after_defers", observedErr) }()`);
    r('ctx, cancel := s.operationContext(0)\n\tdefer cancel()', `ctx, cancel := s.operationContext(0)
 ctx = obs.Bind(observedCtx, ctx)
 observedCtx = ctx
 obs.Event(ctx, "terminal_mark.operation_context.created", nil)
 defer cancel()`);
  }
  return s;
}

if (process.env.HOME !== '/home/sandbox') throw Error('requires sandbox HOME=/home/sandbox');
const args = process.argv.slice(2);
if (args.length !== 4 || args[0] !== '--root' || args[2] !== '--output') throw Error('usage: bun source-overlay.mjs --root ROOT --output NEW_DIRECTORY');
const root=safe(args[1]), output=safe(args[3]), here=safe(import.meta.dir);
const pins=JSON.parse(fs.readFileSync(path.join(here,'source-pins.json'),'utf8'));
const allPins = {...pins.sha256, ...extensionPins};
if (pins.revision !== baseline) throw Error('pin revision mismatch');
if (sha(fs.readFileSync(safe(path.join(root,storeIdentitySource)))) !== storeIdentitySHA256) throw Error('store identity authority source drift');
const patched={};
for (const file of files) {
  const data=fs.readFileSync(safe(path.join(root,file)));
  if (sha(data) !== allPins[file]) throw Error(`baseline drift: ${file}`);
  patched[file]=transform(file,data.toString());
}
fs.mkdirSync(output); // fail closed on reuse
const Replace={}, stamped={};
for (const [file, text] of Object.entries(patched)) {
  const dest=safe(path.join(output,file));
  fs.mkdirSync(path.dirname(dest),{recursive:true}); fs.writeFileSync(dest,text);
  execFileSync('gofmt',['-w',dest]);
  Replace[path.join(root,file)]=dest; stamped[file]=sha(fs.readFileSync(dest));
}
const virtual='internal/decideobserve/recorder.go', recorder=fs.readFileSync(path.join(here,'recorder.go.in'));
fs.mkdirSync(path.join(output,'internal/decideobserve'),{recursive:true});
fs.writeFileSync(path.join(output,virtual),recorder);
execFileSync('gofmt',['-w',path.join(output,virtual)]);
Replace[path.join(root,virtual)]=path.join(output,virtual);
fs.writeFileSync(path.join(output,'overlay.json'),JSON.stringify({Replace},null,2)+'\n');
fs.writeFileSync(path.join(output,'provenance.json'),JSON.stringify({baseline_revision:baseline, baseline_sha256:allPins,
  identity_authority_sha256:{[storeIdentitySource]:storeIdentitySHA256},
  instrumented_sha256:stamped,recorder_template_sha256:sha(recorder),recorder_sha256:sha(fs.readFileSync(path.join(output,virtual))),generator_sha256:sha(fs.readFileSync(safe(import.meta.filename))),
  observation_schema:{boundary_contexts:"v2",actual_fields:["deadline_ns","remaining_ns","context_state"],operation_fields:["operation_context_recorded","operation_deadline_ns","operation_remaining_ns","operation_context_state"],store_identity:{version:1,phase:"operation.store_identity",fields:["store_path","store_path_state","store_path_bytes"],max_path_bytes:1024,source:"Store.Path() at withConnContext entry; actual opened database path; no I/O"},invocation:{version:1,field:"invocation",fields:["process","test","fixture","call","state"],max_id_bytes:128,source:"immutable per-call Observation value in diagnostic-only ListParams/DecideParams"},list:{entry:"client.list.enter",return:"client.list.return_after_defers",context:"terminal_mark.operation_context.created",terminal_return:"terminal_mark.return_after_defers"}},
  canonical_mutations:[],driver:'existing actual driver; no replacement',historical_cause:'UNKNOWN'},null,2)+'\n');
