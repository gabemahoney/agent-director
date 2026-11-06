package gorun

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
)

// MirrorIndent starts every line of test output mirrored to the job log, so
// no test output line can start with the model's log marker.
const MirrorIndent = "    "

// Hooks receive a run's cases as they happen, in stream order. Any of them
// may be nil.
type Hooks struct {
	// CaseStart is called when a case starts, and for the package's own
	// result when the binary starts.
	CaseStart func(model.CaseID)
	// CaseEnd is called when a case ends, with that run of it, and for the
	// package's own result at the binary's end.
	CaseEnd func(model.CaseResult)
	// Mirror receives every line of test output, each line indented by
	// MirrorIndent. Hooks are called only at a line start of Mirror.
	Mirror io.Writer
}

// Collected is what one run of a test binary reported.
type Collected struct {
	// Cases are the cases that ended, the package's own result among them,
	// in the order they first ended, one per case ID, each with its own
	// output.
	Cases []model.CaseRecord
	// WithoutResult are the cases that started and never ended, in the order
	// they first started.
	WithoutResult []model.CaseID
}

// event is one test2json event.
type event struct {
	Action  string  `json:"Action"`
	Test    string  `json:"Test"`
	Elapsed float64 `json:"Elapsed"`
	Output  string  `json:"Output"`
}

// The test2json actions Collect reads.
const (
	actionStart  = "start"
	actionRun    = "run"
	actionOutput = "output"
	actionPass   = "pass"
	actionFail   = "fail"
	actionSkip   = "skip"
)

// Collect reads a test2json stream of the package at dir and returns its
// cases. Each top-level test, subtest and Example that starts is a case
// (model.GoTestCase); a pass, skip or fail ends it, a fail with reason
// failed, and its running time is Go's Elapsed. A name that runs twice, as a
// name the package's in-package and external test files both define does, is
// one case (model.MergeCase). Output is each case's own, by the event's Test;
// output with no Test is the package's. The package's own result
// (model.GoPackageCase) comes from its terminal event: pass and skip as
// reported, and fail only when no case failed in the run, because a case's
// failure accounts for the package's; it is pass otherwise. A stream that
// stops before an event leaves its case without a result. A line that isn't
// JSON is kept as the package's output and the collection goes on; err is
// only for a fault reading r or writing the mirror.
func Collect(r io.Reader, dir string, hooks Hooks) (Collected, error) {
	mirror := newLineWriter(hooks.Mirror)
	collected, err := collect(r, dir, hooks, mirror)
	return collected, errors.Join(err, mirror.err)
}

// collect is Collect over a mirror the caller shares, returning only a fault
// reading r.
func collect(r io.Reader, dir string, hooks Hooks, mirror *lineWriter) (Collected, error) {
	c := collector{
		dir:    dir,
		hooks:  hooks,
		mirror: mirror,
		index:  map[model.CaseID]int{},
		open:   map[model.CaseID]int{},
		output: map[model.CaseID]*capped{},
	}
	br := bufio.NewReader(r)
	var readErr error
	for {
		line, err := br.ReadBytes('\n')
		if trimmed := bytes.TrimRight(line, "\r\n"); len(trimmed) > 0 {
			c.line(trimmed)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErr = fmt.Errorf("read test2json stream: %w", err)
			}
			break
		}
	}
	mirror.endLine()
	return c.finish(), readErr
}

// collector holds one stream's cases while Collect reads it.
type collector struct {
	dir    string
	hooks  Hooks
	mirror *lineWriter
	cases  []model.CaseRecord
	// index maps an ended case to its place in cases.
	index map[model.CaseID]int
	// open counts each case's starts that have not ended.
	open map[model.CaseID]int
	// started lists the cases in the order they first started.
	started []model.CaseID
	// output holds each case's output since its last end.
	output map[model.CaseID]*capped
	// caseFailed reports that a case failed in this run.
	caseFailed bool
}

func (c *collector) line(line []byte) {
	var e event
	if err := json.Unmarshal(line, &e); err != nil {
		c.addOutput(model.GoPackageCase(c.dir), string(line)+"\n")
		return
	}
	id := model.GoPackageCase(c.dir)
	if e.Test != "" {
		id = model.GoTestCase(c.dir, e.Test)
	}
	switch {
	case e.Action == actionOutput:
		c.addOutput(id, e.Output)
	case e.Action == actionStart && e.Test == "", e.Action == actionRun && e.Test != "":
		c.start(id)
	case e.Action == actionPass, e.Action == actionFail, e.Action == actionSkip:
		c.end(id, e)
	}
}

// start starts one run of a case, or the package's own result when the
// binary starts.
func (c *collector) start(id model.CaseID) {
	if _, seen := c.open[id]; !seen {
		c.started = append(c.started, id)
	}
	c.open[id]++
	c.mirror.endLine()
	if c.hooks.CaseStart != nil {
		c.hooks.CaseStart(id)
	}
}

// end ends one run of a case, or the package's own result when e has no
// Test.
func (c *collector) end(id model.CaseID, e event) {
	// test2json's terminal actions are the model's results by name.
	result := model.CaseResult{Case: id, Result: model.Result(e.Action), Elapsed: e.Elapsed}
	if e.Test == "" && result.Result == model.ResultFail && c.caseFailed {
		result.Result = model.ResultPass
	}
	if result.Result == model.ResultFail {
		result.Reason = model.ReasonFailed
		if e.Test != "" {
			c.caseFailed = true
		}
	}
	if c.open[id] > 0 {
		c.open[id]--
	}
	rec := model.CaseRecord{CaseResult: result}
	if out := c.output[id]; out != nil {
		rec.Output, rec.OutputCut = out.text()
		delete(c.output, id)
	}
	if i, seen := c.index[id]; seen {
		c.cases[i] = model.MergeCase(c.cases[i], rec)
	} else {
		c.index[id] = len(c.cases)
		c.cases = append(c.cases, rec)
	}
	c.mirror.endLine()
	if c.hooks.CaseEnd != nil {
		c.hooks.CaseEnd(result)
	}
}

func (c *collector) addOutput(id model.CaseID, text string) {
	c.mirror.write(text)
	out := c.output[id]
	if out == nil {
		out = &capped{}
		c.output[id] = out
	}
	out.add(text)
}

// finish lists the cases still open as without a result.
func (c *collector) finish() Collected {
	col := Collected{Cases: c.cases}
	for _, id := range c.started {
		if c.open[id] > 0 {
			col.WithoutResult = append(col.WithoutResult, id)
		}
	}
	return col
}

// capped holds a case's output, keeping no more than twice the model's cap
// while it grows.
type capped struct {
	buf []byte
	cut bool
}

func (o *capped) add(text string) {
	o.buf = append(o.buf, text...)
	if len(o.buf) > 2*model.MaxCaseOutputBytes {
		kept, cut := model.CapOutput(string(o.buf))
		o.buf = append(o.buf[:0], kept...)
		o.cut = o.cut || cut
	}
}

func (o *capped) text() (string, bool) {
	kept, cut := model.CapOutput(string(o.buf))
	return kept, o.cut || cut
}

// lineWriter mirrors text to a writer, starting each line with MirrorIndent.
// It keeps the first write error.
type lineWriter struct {
	w       io.Writer
	midLine bool
	err     error
}

// newLineWriter returns a lineWriter over w, which discards text when w is
// nil.
func newLineWriter(w io.Writer) *lineWriter {
	return &lineWriter{w: w}
}

func (l *lineWriter) write(text string) {
	if l.w == nil || text == "" {
		return
	}
	var b bytes.Buffer
	for text != "" {
		if !l.midLine {
			b.WriteString(MirrorIndent)
		}
		line, rest, found := cutLine(text)
		b.WriteString(line)
		l.midLine = !found
		text = rest
	}
	l.put(b.Bytes())
}

// endLine ends a line the mirror left open, so what follows starts a line.
func (l *lineWriter) endLine() {
	if l.w != nil && l.midLine {
		l.midLine = false
		l.put([]byte("\n"))
	}
}

func (l *lineWriter) put(p []byte) {
	if _, err := l.w.Write(p); err != nil && l.err == nil {
		l.err = fmt.Errorf("mirror test output: %w", err)
	}
}

// cutLine returns text's first line with its \n, the rest, and whether the
// line ended in \n.
func cutLine(text string) (line, rest string, found bool) {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[:i+1], text[i+1:], true
	}
	return text, "", false
}
