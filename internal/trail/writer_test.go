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

// tsRe is the SR-A-7.9 timestamp regex used across multiple tests.
var tsRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3,}Z$`)

// newTestWriter returns a Writer pointing at a fresh temp dir, and sets
// AGENT_DIRECTOR_STATE_DIR for the test duration so stray Default() calls
// stay off the real ~/.agent-director/.
func newTestWriter(t *testing.T) (*Writer, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AGENT_DIRECTOR_STATE_DIR", dir)
	path := filepath.Join(dir, trailFilename)
	return &Writer{path: path}, path
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

// resetSingleton reinitializes the process-singleton so tests that exercise
// SetLogger / Default() / package-level Emit get a fresh writer each time.
// Cleans up after itself via t.Cleanup.
func resetSingleton(t *testing.T) {
	t.Helper()
	once = sync.Once{}
	defaultWriter = nil
	t.Cleanup(func() {
		once = sync.Once{}
		defaultWriter = nil
	})
}

// TestEmitSingleLineRoundTrip verifies a single Emit writes exactly one
// newline-framed JSON line that round-trips through encoding/json.
func TestEmitSingleLineRoundTrip(t *testing.T) {
	w, path := newTestWriter(t)
	if err := w.Emit(context.Background(), "ad.test", map[string]any{
		"claude_instance_id": "inst-1",
		"request_token":      "tok-abc",
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rows := readLines(t, path)
	if len(rows) != 1 {
		t.Fatalf("want 1 line; got %d", len(rows))
	}
	if rows[0]["event"] != "ad.test" {
		t.Errorf("event = %v; want ad.test", rows[0]["event"])
	}
}

// TestTopLevelFields verifies ts, event, claude_instance_id, request_token
// appear at the top level of the JSON object and not under data/payload/body.
func TestTopLevelFields(t *testing.T) {
	w, path := newTestWriter(t)
	if err := w.Emit(context.Background(), "ad.test", map[string]any{
		"claude_instance_id": "inst-1",
		"request_token":      "tok-abc",
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rows := readLines(t, path)
	row := rows[0]
	for _, key := range []string{"ts", "event", "claude_instance_id", "request_token"} {
		if _, ok := row[key]; !ok {
			t.Errorf("key %q missing at top level", key)
		}
	}
	for _, nested := range []string{"data", "payload", "body"} {
		if _, ok := row[nested]; ok {
			t.Errorf("unexpected nesting wrapper key %q found in emitted line", nested)
		}
	}
}

// TestPathResolution exercises path derivation for the env-set and env-empty cases.
func TestPathResolution(t *testing.T) {
	home, _ := os.UserHomeDir()

	t.Run("env_set", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("AGENT_DIRECTOR_STATE_DIR", dir)
		got := Path()
		want := filepath.Join(dir, trailFilename)
		if got != want {
			t.Errorf("Path() = %q; want %q", got, want)
		}
	})

	t.Run("env_empty", func(t *testing.T) {
		// Explicitly empty → falls back to ~/.agent-director/.
		// Path() is stateless (no file created), so reading the real home path is safe.
		t.Setenv("AGENT_DIRECTOR_STATE_DIR", "")
		got := Path()
		want := filepath.Join(home, ".agent-director", trailFilename)
		if got != want {
			t.Errorf("Path() = %q; want %q", got, want)
		}
	})
}

// TestPathReturnsResolvedPath confirms Path() returns the env-derived path.
func TestPathReturnsResolvedPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENT_DIRECTOR_STATE_DIR", dir)
	got := Path()
	want := filepath.Join(dir, trailFilename)
	if got != want {
		t.Errorf("Path() = %q; want %q", got, want)
	}
}

// TestValidTsPreserved confirms a well-formed SR-A-7.9 ts passes through unchanged.
func TestValidTsPreserved(t *testing.T) {
	w, path := newTestWriter(t)
	const ts = "2026-06-05T01:23:45.678Z"
	if err := w.Emit(context.Background(), "ad.test", map[string]any{"ts": ts}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rows := readLines(t, path)
	if got := rows[0]["ts"]; got != ts {
		t.Errorf("ts = %v; want %q", got, ts)
	}
}

// TestMalformedTsSubstitutedWithWarning confirms a malformed ts is replaced
// with a valid timestamp and a warning is written to the operational logger.
// Uses SetLogger (package-level API) with a bytes.Buffer sink.
func TestMalformedTsSubstitutedWithWarning(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENT_DIRECTOR_STATE_DIR", dir)
	resetSingleton(t)

	var buf bytes.Buffer
	SetLogger(log.New(&buf, "", 0))

	if err := Emit(context.Background(), "ad.ts.warn", map[string]any{"ts": "not-a-timestamp"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	rows := readLines(t, filepath.Join(dir, trailFilename))
	if len(rows) != 1 {
		t.Fatalf("want 1 line; got %d", len(rows))
	}
	ts, ok := rows[0]["ts"].(string)
	if !ok || !tsRe.MatchString(ts) {
		t.Errorf("substituted ts %q does not match SR-A-7.9 regex", rows[0]["ts"])
	}
	if buf.Len() == 0 {
		t.Errorf("expected ts-substitution warning in operational logger; got empty buffer")
	}
}

// TestMissingTsSubstitutedSilently confirms an absent ts is replaced without
// any log entry (SR-A-7.9: absent → silent substitution).
func TestMissingTsSubstitutedSilently(t *testing.T) {
	w, path := newTestWriter(t)
	var buf bytes.Buffer
	w.olog = log.New(&buf, "", 0)

	if err := w.Emit(context.Background(), "ad.test", nil); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rows := readLines(t, path)
	ts, ok := rows[0]["ts"].(string)
	if !ok || !tsRe.MatchString(ts) {
		t.Errorf("substituted ts %q is not a valid SR-A-7.9 timestamp", rows[0]["ts"])
	}
	// No warning expected for absent (vs malformed) ts.
	if buf.Len() != 0 {
		t.Errorf("unexpected log entry for absent ts: %q", buf.String())
	}
}

// TestToolInputDropped asserts that a tool_input key in fields never reaches
// the trail file.
func TestToolInputDropped(t *testing.T) {
	w, path := newTestWriter(t)
	if err := w.Emit(context.Background(), "ad.test", map[string]any{
		"tool_input": map[string]any{"secret": "value"},
		"safe_field": "kept",
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rows := readLines(t, path)
	if _, ok := rows[0]["tool_input"]; ok {
		t.Errorf("tool_input present in emitted line; must be silently dropped")
	}
	if rows[0]["safe_field"] != "kept" {
		t.Errorf("safe_field = %v; want \"kept\"", rows[0]["safe_field"])
	}
}

// TestMultipleEmitsShareOneFd verifies sequential Emits reuse the same file
// descriptor (lazy-open, single-fd invariant per SR-A-7.6).
func TestMultipleEmitsShareOneFd(t *testing.T) {
	w, path := newTestWriter(t)
	ctx := context.Background()

	if err := w.Emit(ctx, "ad.first", nil); err != nil {
		t.Fatalf("first Emit: %v", err)
	}
	fdAfterFirst := w.f
	if fdAfterFirst == nil {
		t.Fatal("fd is nil after first Emit; expected open")
	}

	if err := w.Emit(ctx, "ad.second", nil); err != nil {
		t.Fatalf("second Emit: %v", err)
	}
	if w.f != fdAfterFirst {
		t.Errorf("fd pointer changed between Emits; want single lazy-open descriptor")
	}

	rows := readLines(t, path)
	if len(rows) != 2 {
		t.Errorf("want 2 lines after two Emits; got %d", len(rows))
	}
}

// TestReadOnlyDirEmitReturnsError verifies that when the trail directory
// cannot be created (read-only parent), Emit returns a non-nil error AND
// a meta-event line lands in the operational logger.
func TestReadOnlyDirEmitReturnsError(t *testing.T) {
	t.Setenv("AGENT_DIRECTOR_STATE_DIR", t.TempDir())

	// Create a parent dir with no write permission so MkdirAll fails.
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	// Restore write bit on cleanup so t.TempDir can remove the dir.
	t.Cleanup(func() { os.Chmod(parent, 0o700) })

	var buf bytes.Buffer
	w := &Writer{
		path: filepath.Join(parent, "state", trailFilename),
		olog: log.New(&buf, "", 0),
	}

	err := w.Emit(context.Background(), "ad.test", nil)
	if err == nil {
		t.Errorf("Emit to read-only parent: want non-nil error; got nil")
	}
	if buf.Len() == 0 {
		t.Errorf("expected meta-event line in operational log; got empty buffer")
	}
}
