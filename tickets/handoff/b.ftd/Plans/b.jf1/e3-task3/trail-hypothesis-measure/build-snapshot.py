from pathlib import Path
import subprocess,tarfile,io
root=Path.cwd(); out=root/'tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/trail-hypothesis-measure'; snap=out/'_snapshot/head';snap.mkdir(parents=True,exist_ok=True)
data=subprocess.check_output(['git','archive','HEAD','go.mod','go.sum','internal','pkg','cmd'])
with tarfile.open(fileobj=io.BytesIO(data)) as t:t.extractall(snap)
helper='''package trail
import("context";"encoding/binary";"os";"strconv";"sync/atomic";"syscall";"time")
const measureSize=320
var measureEpoch=time.Now()
var measureSeq atomic.Uint64
var measureRows atomic.Uint64
var measureMemory=[]byte(nil)
func init(){
 p:=os.Getenv("FTD_MEASURE_DIR");if p==""{return};f,e:=os.OpenFile(p+"/trace-"+strconv.Itoa(os.Getpid())+".bin",os.O_CREATE|os.O_RDWR|os.O_EXCL,0600);if e!=nil{panic(e)}
 if e=f.Truncate(4096+32768*measureSize);e!=nil{panic(e)};measureMemory,e=syscall.Mmap(int(f.Fd()),0,4096+32768*measureSize,syscall.PROT_READ|syscall.PROT_WRITE,syscall.MAP_SHARED);if e!=nil{panic(e)};f.Close()
 binary.LittleEndian.PutUint64(measureMemory[0:8],uint64(measureEpoch.UnixNano()));binary.LittleEndian.PutUint64(measureMemory[8:16],uint64(os.Getpid()))
 // Fault in pages before tests, outside measured event paths.
 for i:=4096;i<len(measureMemory);i+=4096{measureMemory[i]=0}
}
type MeasureRecord struct{ID,Kind,Deadline,Queue,Reason uint64;Times [12]int64;Event,Token string}
func MeasureNow()int64{return time.Since(measureEpoch).Nanoseconds()}
func MeasureNew(ctx context.Context,kind uint64,event string,fields map[string]any)*MeasureRecord{
 r:=&MeasureRecord{ID:measureSeq.Add(1),Kind:kind,Event:event};r.Times[0]=MeasureNow();if ctx!=nil{if d,ok:=ctx.Deadline();ok{r.Deadline=uint64(d.Sub(measureEpoch).Nanoseconds())}}
 if fields!=nil{r.Token,_=fields["request_token"].(string)};return r
}
func(r *MeasureRecord) Save(){if measureMemory==nil{return};i:=measureRows.Add(1)-1;if i>=32768{return};b:=measureMemory[4096+int(i)*measureSize:4096+int(i+1)*measureSize];for j,v:=range []uint64{r.ID,r.Kind,r.Deadline,0,r.Queue,r.Reason}{binary.LittleEndian.PutUint64(b[8+j*8:],v)};for j,v:=range r.Times{binary.LittleEndian.PutUint64(b[56+j*8:],uint64(v))};copy(b[152:216],r.Event);copy(b[216:312],r.Token);binary.LittleEndian.PutUint64(b,1)}
'''
(snap/'internal/trail/measure_ftd.go').write_text(helper)
p=snap/'internal/trail/deadline.go';s=p.read_text().replace('done   chan error','done   chan error\n measure *MeasureRecord')
s=s.replace('job := emission{event: event, fields: maps.Clone(fields), done: make(chan error, 1)}','m := MeasureNew(ctx, 1, event, fields)\n defer func(){m.Times[2]=MeasureNow();m.Save()}()\n job := emission{event: event, fields: maps.Clone(fields), done: make(chan error, 1),measure:m}')
s=s.replace('w.delivery.Pending++','w.delivery.Pending++\n m.Queue=uint64(len(w.queue));m.Times[1]=MeasureNow()',1)
s=s.replace('return ErrClosed','m.Reason=4\n return ErrClosed',1).replace('return ErrQueueFull','m.Reason=3\n return ErrQueueFull',1)
s=s.replace('case err := <-job.done:\n\t\treturn err','case err := <-job.done:\n m.Reason=1\n\t\treturn err',1).replace('case err := <-job.done:\n\t\t\treturn err','case err := <-job.done:\n m.Reason=2\n\t\t\treturn err',1)
s=s.replace('return fmt.Errorf("%w: %s: %w", ErrPending','m.Reason=5\n return fmt.Errorf("%w: %s: %w", ErrPending',1)
s=s.replace('err := w.emit(job.event, job.fields)','err := w.emitMeasured(job.event, job.fields, job.measure.ID)')
p.write_text(s)
p=snap/'internal/trail/trail.go';s=p.read_text().replace('func (w *Writer) emit(event string, fields map[string]any) error {','func (w *Writer) emit(event string, fields map[string]any) error {return w.emitMeasured(event,fields,0)}\nfunc (w *Writer) emitMeasured(event string, fields map[string]any,id uint64) error {\n m:=MeasureNew(nil,2,event,fields);if id!=0{m.ID=id};defer func(){m.Times[8]=MeasureNow();m.Save()}()')
s=s.replace('func (w *Writer) emitMeasured', 'func (w *Writer) emitMeasured',1)
start=s.index('func (w *Writer) emitMeasured');a=s[:start];b=s[start:]
b=b.replace('w.mu.Lock()','w.mu.Lock()\n m.Times[1]=MeasureNow()',1)
b=b.replace('line, err := buildEnvelope','m.Times[2]=MeasureNow()\n line, err := buildEnvelope',1)
b=b.replace('if err := w.ensureOpen(); err != nil {','m.Times[3]=MeasureNow()\n if err := w.ensureOpen(); err != nil {',1)
b=b.replace('if _, err := w.f.Write(line); err != nil {','m.Times[4]=MeasureNow()\n if _, err := w.f.Write(line); err != nil {',1)
b=b.replace('if err := w.f.Sync(); err != nil {','m.Times[5]=MeasureNow()\n if err := w.f.Sync(); err != nil {',1)
b=b.replace('\treturn nil\n}', '\tm.Times[6]=MeasureNow()\n return nil\n}',1)
p.write_text(a+b)
p=snap/'internal/store/lockwait.go';s=p.read_text().replace('"context"','"context"\n"github.com/gabemahoney/agent-director/internal/trail"',1)
s=s.replace('if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {','m:=trail.MeasureNew(ctx,3,"commit",nil)\n _, commitErr := conn.ExecContext(ctx,"COMMIT")\n m.Times[1]=trail.MeasureNow();if commitErr!=nil{m.Reason=1};m.Save()\n if err := commitErr; err != nil {',1)
s=s.replace('op.err, op.completed = result, true','op.err, op.completed = result, true\n m:=trail.MeasureNew(ctx,4,"sql_publication",nil);if result!=nil{m.Reason=1};m.Save()',1)
p.write_text(s)
p=snap/'internal/store/connection_cleanup.go';s=p.read_text().replace('"context"','"context"\n"github.com/gabemahoney/agent-director/internal/trail"',1)
s=s.replace('if op.commitStarted {','if op.commitStarted {\n m:=trail.MeasureNew(ctx,5,"guard_unconfirmed",nil);m.Save()',1)
p.write_text(s)
# Retain exact snapshot source differences for review.
for rel in ['internal/trail/deadline.go','internal/trail/trail.go','internal/store/lockwait.go','internal/store/connection_cleanup.go']:
 orig=subprocess.check_output(['git','show','HEAD:'+rel]);(out/(Path(rel).name+'.original')).write_bytes(orig)
(out/'source-head.txt').write_text(subprocess.check_output(['git','rev-parse','HEAD'],text=True))
