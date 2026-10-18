// Terminal-exit diagnostics for this synthetic collector only. Never emit raw
// protocol payloads, registration env, tool arguments, error text or pane text.
const fs = require('node:fs');
const cp = require('node:child_process');
const crypto = require('node:crypto');
const path = require('node:path');
const {performance} = require('node:perf_hooks');
const LIMIT = {bytes: 98304, childBytes: 16384, readBytes: 32768, records: 8, millis: 7000};
const digest = value => {
 const raw = Buffer.from(typeof value === 'string' ? value : JSON.stringify(value) ?? 'null');
 return {bytes: raw.length, sha256: crypto.createHash('sha256').update(raw).digest('hex')};
};
const atom = value => typeof value === 'number' || typeof value === 'boolean' || value === null ? value :
 typeof value === 'string' && value.length <= 192 && /^[a-zA-Z0-9_./:%@+$\[\] -]*$/.test(value) ? value : digest(value);
const pick = (value, keys) => Object.fromEntries(keys.filter(k => value?.[k] !== undefined).map(k => [k, atom(value[k])]));
const frameKeys = ['id','method','kind','event','attempt','control','pid','start','code','signal','bytes','sha256','instance_id','session_id','life_number','launch_token','connection_id','stop_reason'];
function frame(value) {
 const out = pick(value, frameKeys);
 for (const key of ['params','result','error','text','message','servers','call']) if (value?.[key] !== undefined) out[key] = digest(value[key]);
 if (value?.response) out.response = {...pick(value.response, ['id']), ...Object.fromEntries(['result','error'].filter(k=>value.response[k]!==undefined).map(k=>[k,digest(value.response[k])]))};
 if (typeof value?.response?.error?.code === 'number') out.error_code = value.response.error.code;
 if (typeof value?.response?.result?.isError === 'boolean') out.isError = value.response.result.isError;
 // These are fixed protocol discriminators, never arbitrary tool input/output.
 for (const key of ['sessionId','stopReason','sessionUpdate']) if (typeof value?.params?.[key] === 'string') out[key] = atom(value.params[key]);
 if (typeof value?.result?.stopReason === 'string') out.stopReason = atom(value.result.stopReason);
 return out;
}
function capture({dir, store, own, socket, stage, peerSocket = '', registered = ''}) {
 const started = performance.now(), lines = []; let bytes = 0, childBytes = 0, incomplete = false;
 let omitted = new Set();
 let child = own;
 function emit(category, data) {
  const line = JSON.stringify({diagnostic:'HELP_FAILURE_METADATA',case:own,child,stage,category,...data})+'\n';
  const summary = category === 'end' || category === 'child-end';
  if (bytes + Buffer.byteLength(line) > LIMIT.bytes-(summary?0:8192) || (!summary && childBytes + Buffer.byteLength(line) > LIMIT.childBytes-2048)) {incomplete=true; omitted.add(category); return;}
  bytes += Buffer.byteLength(line); childBytes += Buffer.byteLength(line); lines.push(line);
 }
 function error(category, err) {incomplete=true; emit(category,{status:'error',code:err?.code || 'INVALID_METADATA'});}
 function read(file, category) {
  let fd;
  try {
   // Exact fixture paths only; refuse symlinks and non-regular objects.
   const relative=path.relative(dir,file);
   if(relative.startsWith('..') || path.isAbsolute(relative) || relative.split(path.sep).includes('.claude')) throw {code:'OUTSIDE_FIXTURE'};
   let current=dir;
   for(const component of relative.split(path.sep)) {current=path.join(current,component);if(fs.lstatSync(current).isSymbolicLink())throw {code:'SYMLINK'};}
   fd=fs.openSync(file,fs.constants.O_RDONLY|fs.constants.O_NOFOLLOW|fs.constants.O_NONBLOCK);
   const stat=fs.fstatSync(fd); if (!stat.isFile()) throw {code:'NOT_REGULAR'};
   const size=Math.min(stat.size,LIMIT.readBytes), buf=Buffer.alloc(size);
   const n=fs.readSync(fd,buf,0,size,Math.max(0,stat.size-size));
   let text=buf.subarray(0,n).toString('utf8');
   const truncated=stat.size>size;
   if (truncated) {incomplete=true; text=text.slice(text.indexOf('\n')+1);}
   emit(category,{status:'read',file:path.basename(file),bytes:stat.size,read_bytes:n,truncated});
   return text;
  } catch(err) {error(category,err);return null;} finally {if(fd!==undefined)fs.closeSync(fd);}
 }
 function records(file, category) {
  const text=read(file,category); if(text===null)return;
  const raw=text.trim().split('\n').filter(Boolean);
  if(raw.length>LIMIT.records) {incomplete=true;emit(category,{status:'truncated',omitted_records:raw.length-LIMIT.records});}
  for(const line of raw.slice(-LIMIT.records)) {
   try {emit(category,{status:'record',metadata:frame(JSON.parse(line))});}
   catch(err) {error(category,{code:'MALFORMED_JSON'});}
  }
 }
 function command(category, exe, args) {
  const remaining=LIMIT.millis-(performance.now()-started);
  if(remaining<=0){error(category,{code:'TIME_BUDGET'});return null;}
  const r=cp.spawnSync(exe,args,{encoding:'utf8',timeout:Math.min(500,remaining),maxBuffer:LIMIT.readBytes,env:{...process.env,SQL_BUSY_TIMEOUT_MS:'100'}});
  if(r.error || r.status!==0){error(category,{code:r.error?.code || 'COMMAND_FAILED'});emit(category,{exit:r.status,signal:r.signal,stderr:digest(r.stderr || '')});return null;}
  return r.stdout;
 }
 function query(db, table, keys, predicate) {
  const raw=command(table,'/opt/driver/sql.sh',['-readonly','-json',db,`SELECT ${keys.join(',')} FROM ${table} WHERE ${predicate} LIMIT 9;`]);
  if(raw===null)return [];
  try {const rows=JSON.parse(raw.trim()||'[]');emit(table,{status:rows.length?'rows':'missing',rows:rows.slice(0,8).map(row=>pick(row,keys)),truncated:rows.length>8});if(rows.length>8)incomplete=true;return rows.slice(0,8);}
  catch(err){error(table,{code:'MALFORMED_JSON'});return [];}
 }
 function processState(pid, expected) {
  if(!Number.isInteger(Number(pid)) || Number(pid)<=0)return;
  try {const raw=fs.readFileSync(`/proc/${Number(pid)}/stat`,'utf8'),fields=raw.slice(raw.lastIndexOf(')')+2).split(' ');
   emit('pane-process',{pid:Number(pid),state:fields[0],ppid:Number(fields[1]),start:fields[19],expected_start:expected || null,identity_matches:expected?fields[19]===String(expected):null});
  } catch(err){error('pane-process',err);emit('pane-process',{pid:Number(pid),status:'missing'});}
 }
 emit('begin',{snapshot:'before-cleanup',timestamp:new Date().toISOString(),limits:LIMIT});
 const candidates=[{id:own,label:'',db:store,socket}];
 // Registered settings exist before spawn, even when no row was inserted.
 // This is a fixed case-owned list, never a filesystem/store/PID scan.
 for(const label of ['cli','configured','before','after']) {
  if(registered.split(' ').includes(label) || fs.existsSync(path.join(dir,`child-${label}-settings.json`)))candidates.push({id:`child-${label}-${own}`,label:`child-${label}-`,db:store,socket});
 }
 if(peerSocket)candidates.push({id:`peer-${own}`,label:'peer-',db:path.join(dir,'peer.db'),socket:peerSocket});
 const spawnKeys=['instance_id','parent_id','state','session_id','jsonl_path','launch_token','life_number','tmux_session_name','pane_id','pane_pid','pane_starttime','tmux_socket','tmux_server_pid','tmux_server_starttime'];
 const runtimeKeys=['bound_instance_id','bound_launch_token','bound_life_number','bound_gateway_pid','bound_gateway_start','bound_runtime_pid','bound_runtime_start','bound_connection_id','bound_input_pipe','bound_output_pipe','bound_input_fd','bound_output_fd','bound_session_id','bound_terminated'];
 for(const candidate of candidates) {
  child=candidate.id;childBytes=0;omitted=new Set();
  // IDs are synthetic; refuse any possibility of SQL/path injection.
  if(!/^[a-zA-Z0-9-]{1,160}$/.test(child)){error('child',{code:'INVALID_ID'});continue;}
  emit('child',{status:'known',discovery:candidate.label?'registered-fixture':'parent'});
  records(path.join(dir,candidate.label+'trace.jsonl'),'ACP-trace');
  records(path.join(dir,candidate.label+'mcp.jsonl'),'MCP-request-response');
  const rows=query(candidate.db,'spawns',spawnKeys,`instance_id='${child}'`);
  const runtimes=query(candidate.db,'cursor_runtimes',runtimeKeys,`bound_instance_id='${child}'`);
  query(candidate.db,'session_history',['instance_id','session_id','life_number','recorded_at'],`instance_id='${child}' ORDER BY history_id DESC`);
  emit('startup-binding',{status:runtimes.length?'bound':'missing',binding:runtimes.map(row=>pick(row,runtimeKeys))});
  const transcript=rows[0]?.jsonl_path;
  // Transcripts must resolve inside this exact synthetic HOME, with no symlink.
  if(typeof transcript==='string' && path.resolve(transcript).startsWith(path.join(dir,'home')+'/') && !transcript.split('/').includes('.claude')) records(transcript,'protected-transcript-tail');
  else emit('protected-transcript-tail',{status:'missing-or-outside-fixture'});
  for(const [category,args,keys] of [
   ['tmux-list-sessions',['list-sessions','-F','#{session_name}\t#{session_id}\t#{pid}'],['session_name','session_id','server_pid']],
   ['tmux-list-panes',['list-panes','-a','-F','#{session_name}\t#{pane_id}\t#{pane_dead}\t#{pane_pid}'],['session_name','pane_id','pane_dead','pane_pid']]
  ]) {
   const raw=command(category,'tmux',['-S',candidate.socket,...args]);
   if(raw!==null){const entries=raw.trim().split('\n').filter(Boolean).filter(line=>[child,rows[0]?.tmux_session_name].includes(line.split('\t')[0]));
    emit(category,{status:entries.length?'rows':'missing',rows:entries.slice(0,8).map(line=>Object.fromEntries(line.split('\t').map((v,i)=>[keys[i],atom(v)]))),truncated:entries.length>8});
    if(category==='tmux-list-panes')for(const line of entries.slice(0,8))processState(line.split('\t')[3]);
   }
  }
  for(const row of rows)processState(row.pane_pid,row.pane_starttime);
  for(const row of runtimes){processState(row.bound_gateway_pid,row.bound_gateway_start);processState(row.bound_runtime_pid,row.bound_runtime_start);}
  if(!rows.some(row=>row.pane_pid) && !runtimes.length)emit('pane-process',{status:'missing-known-pid'});
  emit('child-end',{incomplete,omitted_categories:[...omitted]});
 }
 child=own;childBytes=0;
 emit('end',{incomplete,elapsed_ms:Math.round(performance.now()-started),snapshot:'before-cleanup'});
 return lines.join('');
}
module.exports={capture,frame,LIMIT};
if(require.main===module){const [dir,store,own,socket,stage,peerSocket,registered]=process.argv.slice(2);process.stdout.write(capture({dir,store,own,socket,stage,peerSocket,registered}));}
