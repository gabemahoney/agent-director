// Package signalsfix provides the reusable, immutable capture-6 fixture and
// private copy/mutation helpers shared by analyzer and exact CLI tests.
package signalsfix

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

type Fixture struct {
	Root     string
	Manifest runner.Manifest
	t        testing.TB
}

func Passing(t testing.TB) *Fixture {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "../../signals/testdata/claude/passing")
	f := &Fixture{Root: root, t: t}
	f.Decode("manifest.json", &f.Manifest)
	return f
}
func Copy(t testing.TB) *Fixture {
	t.Helper()
	src := Passing(t)
	f := &Fixture{Root: t.TempDir(), Manifest: src.Manifest, t: t}
	for name := range src.Manifest.Artifacts {
		f.Write(name, src.Read(name))
	}
	f.JSON("manifest.json", f.Manifest)
	return f
}
func (f *Fixture) Read(name string) []byte {
	f.t.Helper()
	b, e := os.ReadFile(filepath.Join(f.Root, name))
	if e != nil {
		f.t.Fatal(e)
	}
	return b
}
func (f *Fixture) Decode(name string, v any) {
	f.t.Helper()
	if e := json.Unmarshal(f.Read(name), v); e != nil {
		f.t.Fatal(e)
	}
}
func (f *Fixture) Write(name string, b []byte) {
	f.t.Helper()
	p := filepath.Join(f.Root, name)
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		f.t.Fatal(e)
	}
	if e := os.WriteFile(p, b, 0600); e != nil {
		f.t.Fatal(e)
	}
}
func (f *Fixture) JSON(name string, v any) {
	f.t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		f.t.Fatal(e)
	}
	f.Write(name, append(b, '\n'))
}
func (f *Fixture) Rows(name string) []runner.Row {
	f.t.Helper()
	var rows []runner.Row
	for _, line := range bytes.Split(bytes.TrimSpace(f.Read(name)), []byte("\n")) {
		var row runner.Row
		if e := json.Unmarshal(line, &row); e != nil {
			f.t.Fatal(e)
		}
		rows = append(rows, row)
	}
	return rows
}
func (f *Fixture) WriteRows(name string, rows []runner.Row) {
	f.t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	for _, row := range rows {
		if e := enc.Encode(row); e != nil {
			f.t.Fatal(e)
		}
	}
	f.Write(name, b.Bytes())
}
func (f *Fixture) Transcript(owner int) string {
	return "raw/" + f.Manifest.Owned[owner].InstanceID + "-transcript.jsonl"
}
func (f *Fixture) Store(owner int) string {
	return "raw/final-" + f.Manifest.Owned[owner].InstanceID + "-store.json"
}
func (f *Fixture) Hooks(event string, owner int, change func(runner.Row)) {
	f.t.Helper()
	for name := range f.Manifest.Artifacts {
		if !strings.HasPrefix(name, "raw/hooks/") || !strings.HasSuffix(name, "/input.json") {
			continue
		}
		var row runner.Row
		f.Decode(name, &row)
		if row["hook_event_name"] == event && row["session_id"] == f.Manifest.Owned[owner].SessionID {
			change(row)
			f.JSON(name, row)
		}
	}
}
func (f *Fixture) Snapshots(owner int, change func(*runner.Snapshot)) {
	f.t.Helper()
	for name := range f.Manifest.Artifacts {
		if !strings.HasSuffix(name, "-store.json") && !strings.HasPrefix(name, "raw/order-observations-") {
			continue
		}
		var s runner.Snapshot
		f.Decode(name, &s)
		if s.InstanceID == f.Manifest.Owned[owner].InstanceID {
			change(&s)
			f.JSON(name, s)
		}
	}
}

// Reindex repairs mechanical digests only. Tests keep semantic contradictions
// intact so a hash failure cannot stand in for the intended signal failure.
func (f *Fixture) Reindex() {
	f.t.Helper()
	for name := range f.Manifest.Artifacts {
		if !strings.HasSuffix(name, "/receipt.json") {
			continue
		}
		var r runner.Row
		f.Decode(name, &r)
		input := filepath.ToSlash(filepath.Join(filepath.Dir(name), runner.Text(r, "input")))
		b := f.Read(input)
		r["sha256"] = runner.Digest(b)
		r["bytes"] = len(b)
		f.JSON(name, r)
	}
	for name := range f.Manifest.Artifacts {
		b := f.Read(name)
		f.Manifest.Artifacts[name] = runner.Artifact{SHA256: runner.Digest(b), Bytes: int64(len(b))}
	}
	f.JSON("manifest.json", f.Manifest)
}
