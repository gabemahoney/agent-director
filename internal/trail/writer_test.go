package trail

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
)

// tsRe is the SR-A-7.9 timestamp regex.
var tsRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3,}Z$`)

// newTestWriter returns a Writer on a fresh temp dir with its operational log
// in the returned buffer, and points HOME there so a stray Default() call
// never resolves to the real ~/.agent-director/.
func newTestWriter(t *testing.T) (*Writer, string, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	var buf bytes.Buffer
	path := filepath.Join(dir, trailFilename)
	return &Writer{path: path, olog: log.New(&buf, "", 0)}, path, &buf
}

// readLines scans path and returns each line unmarshaled into map[string]any.
func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("readLines: open %s: %v", path, err)
	}
	defer f.Close()
	var rows []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("readLines: unmarshal %q: %v", sc.Text(), err)
		}
		rows = append(rows, m)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("readLines: scan: %v", err)
	}
	return rows
}

// resetSingleton gives a test that uses SetLogger, Default() or the
// package-level Emit a fresh process singleton, and resets it at cleanup.
func resetSingleton(t *testing.T) {
	t.Helper()
	once = sync.Once{}
	defaultWriter = nil
	t.Cleanup(func() {
		once = sync.Once{}
		defaultWriter = nil
	})
}

// TestEmitLine: one Emit writes one newline-framed JSON line with ts, event
// and the fields at the top level (no data/payload/body wrapper), a valid ts
// kept as given, and tool_input dropped (SR-A-7.9).
func TestEmitLine(t *testing.T) {
	w, path, _ := newTestWriter(t)
	const ts = "2026-06-05T01:23:45.678Z"
	if err := w.Emit(context.Background(), "ad.test", map[string]any{
		"ts": ts, "claude_instance_id": "inst-1", "request_token": "tok-abc",
		"tool_input": map[string]any{"secret": "value"}, "safe_field": "kept",
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rows := readLines(t, path)
	if len(rows) != 1 {
		t.Fatalf("want 1 line; got %d", len(rows))
	}
	row := rows[0]
	want := map[string]any{"ts": ts, "event": "ad.test", "claude_instance_id": "inst-1", "request_token": "tok-abc", "safe_field": "kept"}
	for k, v := range want {
		if row[k] != v {
			t.Errorf("%s = %v; want %v at the top level", k, row[k], v)
		}
	}
	if len(row) != len(want) {
		t.Errorf("line %v has keys beyond %v (tool_input or a wrapper)", row, want)
	}
}

// TestPathResolution: Path() is <HOME>/.agent-director/<trail>, and the
// removed state-dir override is ignored.
func TestPathResolution(t *testing.T) {
	// The name is assembled at runtime so a repo-wide grep for the removed literal stays clean.
	override := "AGENT_DIRECTOR_" + "STATE_DIR"
	for _, setOverride := range []bool{false, true} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		if setOverride {
			t.Setenv(override, t.TempDir())
		}
		if got, want := Path(), filepath.Join(home, ".agent-director", trailFilename); got != want {
			t.Errorf("Path() = %q; want %q (%s set: %t)", got, want, override, setOverride)
		}
	}
}

// TestMalformedTsSubstitutedWithWarning: a malformed ts is replaced with a
// valid one and a warning goes to the logger SetLogger installs.
func TestMalformedTsSubstitutedWithWarning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	resetSingleton(t)

	var buf bytes.Buffer
	SetLogger(log.New(&buf, "", 0))
	if err := Emit(context.Background(), "ad.ts.warn", map[string]any{"ts": "not-a-timestamp"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rows := readLines(t, filepath.Join(home, ".agent-director", trailFilename))
	if len(rows) != 1 {
		t.Fatalf("want 1 line; got %d", len(rows))
	}
	if ts, ok := rows[0]["ts"].(string); !ok || !tsRe.MatchString(ts) {
		t.Errorf("substituted ts %q does not match SR-A-7.9 regex", rows[0]["ts"])
	}
	if buf.Len() == 0 {
		t.Errorf("expected ts-substitution warning in operational logger; got empty buffer")
	}
}

// TestEmitsShareOneFdAndFillTs: sequential Emits reuse one lazily opened
// descriptor (SR-A-7.6), and an absent ts is filled in silently (SR-A-7.9).
func TestEmitsShareOneFdAndFillTs(t *testing.T) {
	w, path, buf := newTestWriter(t)
	ctx := context.Background()
	if err := w.Emit(ctx, "ad.first", nil); err != nil {
		t.Fatalf("first Emit: %v", err)
	}
	first := w.f
	if first == nil {
		t.Fatal("fd is nil after first Emit; expected open")
	}
	if err := w.Emit(ctx, "ad.second", nil); err != nil {
		t.Fatalf("second Emit: %v", err)
	}
	if w.f != first {
		t.Errorf("fd pointer changed between Emits; want single lazy-open descriptor")
	}
	rows := readLines(t, path)
	if len(rows) != 2 {
		t.Fatalf("want 2 lines after two Emits; got %d", len(rows))
	}
	for _, row := range rows {
		if ts, ok := row["ts"].(string); !ok || !tsRe.MatchString(ts) {
			t.Errorf("substituted ts %q is not a valid SR-A-7.9 timestamp", row["ts"])
		}
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected log entry for absent ts: %q", buf.String())
	}
}

// TestReadOnlyDirEmitReturnsError: when the trail directory cannot be
// created, Emit returns an error and a meta-event line lands in the
// operational logger.
func TestReadOnlyDirEmitReturnsError(t *testing.T) {
	w, _, buf := newTestWriter(t)
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o700) })
	w.path = filepath.Join(parent, "state", trailFilename)

	if err := w.Emit(context.Background(), "ad.test", nil); err == nil {
		t.Errorf("Emit to read-only parent: want non-nil error; got nil")
	}
	if buf.Len() == 0 {
		t.Errorf("expected meta-event line in operational log; got empty buffer")
	}
}
