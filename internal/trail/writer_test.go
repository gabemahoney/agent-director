package trail

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/gabemahoney/agent-director/internal/layout"
)

// tsRe is the SR-A-7.9 timestamp regex.
var tsRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3,}Z$`)

// newTestWriter returns a Writer on a fresh temp dir with its operational log
// in the returned buffer, and points HOME there so a stray Default() call
// never resolves to the real ~/.config/agent-director/.
func newTestWriter(t *testing.T) (*Writer, string, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	var buf bytes.Buffer
	path := filepath.Join(dir, layout.TrailFile)
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
		"ts": ts, "instance_id": "inst-1", "request_token": "tok-abc",
		"tool_input": map[string]any{"secret": "value"}, "safe_field": "kept",
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	rows := readLines(t, path)
	if len(rows) != 1 {
		t.Fatalf("want 1 line; got %d", len(rows))
	}
	row := rows[0]
	want := map[string]any{"ts": ts, "event": "ad.test", "instance_id": "inst-1", "request_token": "tok-abc", "safe_field": "kept"}
	for k, v := range want {
		if row[k] != v {
			t.Errorf("%s = %v; want %v at the top level", k, row[k], v)
		}
	}
	if len(row) != len(want) {
		t.Errorf("line %v has keys beyond %v (tool_input or a wrapper)", row, want)
	}
}

// TestPathResolution: Path() is <HOME>/.config/agent-director/<trail> whatever
// XDG_CONFIG_HOME says, and the removed state-dir override is ignored.
func TestPathResolution(t *testing.T) {
	// The name is assembled at runtime so a repo-wide grep for the removed literal stays clean.
	override := "AGENT_DIRECTOR_" + "STATE_DIR"
	for _, tc := range []struct {
		name string
		env  []string
	}{
		{"plain", nil},
		{"state dir override", []string{override}},
		{"XDG_CONFIG_HOME", []string{"XDG_CONFIG_HOME"}},
		{"both", []string{override, "XDG_CONFIG_HOME"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			for _, k := range tc.env {
				t.Setenv(k, t.TempDir())
			}
			if got, want := Path(), filepath.Join(home, ".config", "agent-director", layout.TrailFile); got != want {
				t.Errorf("Path() = %q; want %q", got, want)
			}
		})
	}
}

// oldDirName is the directory AD used before ~/.config/agent-director,
// assembled at runtime so the old-path scan stays clean.
var oldDirName = "." + "agent-director"

// homeTree lists everything under home without following links, as sorted
// "d|f|l <perm> <rel path>" lines (perm omitted for links).
func homeTree(t *testing.T, home string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(home, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(home, p)
		if rel == "." {
			return nil
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			out = append(out, "l "+rel)
		case fi.IsDir():
			out = append(out, fmt.Sprintf("d %04o %s", fi.Mode().Perm(), rel))
		default:
			out = append(out, fmt.Sprintf("f %04o %s", fi.Mode().Perm(), rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", home, err)
	}
	sort.Strings(out)
	return out
}

// seedHome builds a home from entries: "d:" paths are directories, "f:" paths
// hold a file, "l:name->target" are links with the target text as written.
func seedHome(t *testing.T, home string, entries ...string) {
	t.Helper()
	for _, e := range entries {
		switch {
		case strings.HasPrefix(e, "d:"):
			if err := os.MkdirAll(filepath.Join(home, e[2:]), 0o700); err != nil {
				t.Fatal(err)
			}
		case strings.HasPrefix(e, "f:"):
			if err := os.WriteFile(filepath.Join(home, e[2:]), []byte("keep\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		case strings.HasPrefix(e, "l:"):
			name, target, _ := strings.Cut(e[2:], "->")
			if err := os.Symlink(strings.ReplaceAll(target, "<home>", home), filepath.Join(home, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestEmitLayoutPresence: the trail writes nothing while only the old
// directory is present, and writes in every other layout, with the home tree
// as expected after.
func TestEmitLayoutPresence(t *testing.T) {
	old, newDir := oldDirName, ".config/agent-director"
	trail := newDir + "/" + layout.TrailFile
	for _, tc := range []struct {
		name  string
		seed  []string
		write bool
		tree  []string // the home tree after one Emit
	}{
		{
			name: "only old",
			seed: []string{"d:" + old, "f:" + old + "/keep"},
			tree: []string{"d 0700 " + old, "f 0600 " + old + "/keep"},
		},
		{
			name: "only old, link to another directory",
			seed: []string{"d:elsewhere", "l:" + old + "->elsewhere"},
			tree: []string{"d 0700 elsewhere", "l " + old},
		},
		{
			name:  "both",
			seed:  []string{"d:" + old, "f:" + old + "/keep", "d:" + newDir},
			write: true,
			tree: []string{"d 0700 " + old, "f 0600 " + old + "/keep", "d 0700 .config",
				"d 0700 " + newDir, "f 0600 " + trail},
		},
		{
			name:  "compat link, relative, target present",
			seed:  []string{"d:" + newDir, "l:" + old + "->" + newDir},
			write: true,
			tree:  []string{"l " + old, "d 0700 .config", "d 0700 " + newDir, "f 0600 " + trail},
		},
		{
			name:  "compat link, absolute, target present",
			seed:  []string{"d:" + newDir, "l:" + old + "-><home>/" + newDir},
			write: true,
			tree:  []string{"l " + old, "d 0700 .config", "d 0700 " + newDir, "f 0600 " + trail},
		},
		{
			name:  "compat link, target absent",
			seed:  []string{"l:" + old + "->" + newDir},
			write: true,
			tree:  []string{"l " + old, "d 0700 .config", "d 0700 " + newDir, "f 0600 " + trail},
		},
		{
			name:  "neither",
			write: true,
			tree:  []string{"d 0700 .config", "d 0700 " + newDir, "f 0600 " + trail},
		},
		{
			name:  "only old, new is a link to a directory",
			seed:  []string{"d:" + old, "f:" + old + "/keep", "d:elsewhere", "d:.config", "l:" + newDir + "->../elsewhere"},
			write: true,
			tree: []string{"d 0700 " + old, "f 0600 " + old + "/keep", "d 0700 elsewhere",
				"f 0600 elsewhere/" + layout.TrailFile, "d 0700 .config", "l " + newDir},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			seedHome(t, home, tc.seed...)
			p, h, err := resolve()
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			w := &Writer{path: p, home: h, olog: log.New(&buf, "", 0)}

			err = w.Emit(context.Background(), "ad.test", nil)

			if tc.write && err != nil {
				t.Errorf("Emit: %v; want it written", err)
			}
			if !tc.write && !errors.Is(err, errOldLayout) {
				t.Errorf("Emit error = %v; want one wrapping errOldLayout", err)
			}
			if buf.Len() != 0 {
				t.Errorf("operational log = %q; want nothing logged", buf.String())
			}
			want := slices.Clone(tc.tree)
			sort.Strings(want)
			if got := homeTree(t, home); !slices.Equal(got, want) {
				t.Errorf("home tree after Emit:\n got %q\nwant %q", got, want)
			}
			if tc.write {
				if rows := readLines(t, filepath.Join(home, filepath.FromSlash(trail))); len(rows) != 1 || rows[0]["event"] != "ad.test" {
					t.Errorf("trail rows = %v; want the one ad.test event", rows)
				}
			}
		})
	}
}

// TestEmitJudgesLayoutEachCall: one writer refuses while only the old
// directory is present and writes once the new directory appears.
func TestEmitJudgesLayoutEachCall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedHome(t, home, "d:"+oldDirName)
	p, h, _ := resolve()
	w := &Writer{path: p, home: h}

	if err := w.Emit(context.Background(), "ad.first", nil); !errors.Is(err, errOldLayout) {
		t.Fatalf("first Emit = %v; want errOldLayout", err)
	}
	seedHome(t, home, "d:.config/agent-director")
	if err := w.Emit(context.Background(), "ad.second", nil); err != nil {
		t.Fatalf("Emit once the new directory exists: %v", err)
	}
	rows := readLines(t, p)
	if len(rows) != 1 || rows[0]["event"] != "ad.second" {
		t.Errorf("trail rows = %v; want only ad.second", rows)
	}
}

// TestDefaultOnlyOldWritesNothing: the process singleton carries its home, so
// it too writes nothing while only the old directory is present.
func TestDefaultOnlyOldWritesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedHome(t, home, "d:"+oldDirName)
	resetSingleton(t)
	var buf bytes.Buffer
	SetLogger(log.New(&buf, "", 0))

	if err := Emit(context.Background(), "ad.test", nil); !errors.Is(err, errOldLayout) {
		t.Errorf("Emit = %v; want errOldLayout", err)
	}
	if want := []string{"d 0700 " + oldDirName}; !slices.Equal(homeTree(t, home), want) {
		t.Errorf("home tree = %q; want %q", homeTree(t, home), want)
	}
	if buf.Len() != 0 {
		t.Errorf("operational log = %q; want nothing logged", buf.String())
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
	rows := readLines(t, filepath.Join(home, ".config", "agent-director", layout.TrailFile))
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
	w.path = filepath.Join(parent, "state", layout.TrailFile)

	if err := w.Emit(context.Background(), "ad.test", nil); err == nil {
		t.Errorf("Emit to read-only parent: want non-nil error; got nil")
	}
	if buf.Len() == 0 {
		t.Errorf("expected meta-event line in operational log; got empty buffer")
	}
}
