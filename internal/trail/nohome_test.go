package trail

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/cwdfix"
)

// assertDirEmpty fails unless dir holds no entries.
func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, e := range entries {
		t.Errorf("%s holds %q; want nothing written there", dir, e.Name())
	}
}

// assertNoHomeEmit fails unless err wraps errNoHome, cwd is still empty and
// logged is exactly one error_class=no_home line (no meta-event attempt).
func assertNoHomeEmit(t *testing.T, err error, cwd, logged string) {
	t.Helper()
	if !errors.Is(err, errNoHome) {
		t.Errorf("Emit error = %v; want one wrapping errNoHome", err)
	}
	assertDirEmpty(t, cwd)
	if lines := strings.Split(strings.TrimRight(logged, "\n"), "\n"); len(lines) != 1 ||
		!strings.Contains(lines[0], "error_class=no_home") {
		t.Errorf("operational log = %q; want exactly one line with error_class=no_home", logged)
	}
}

// TestDefaultWithoutHomeWritesNothing: with HOME empty or relative the
// singleton writes nothing under the cwd, Path() is "", and the pathless
// writer stays pinned once HOME is set later (b.iin).
func TestDefaultWithoutHomeWritesNothing(t *testing.T) {
	for _, tc := range []struct{ name, home string }{
		{"empty", ""},
		{"relative", "rel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := cwdfix.Temp(t)
			t.Setenv("HOME", tc.home)
			resetSingleton(t)
			var buf bytes.Buffer
			SetLogger(log.New(&buf, "", 0))

			err := Emit(context.Background(), "ad.test", nil)
			assertNoHomeEmit(t, err, cwd, buf.String())
			if p := Path(); p != "" {
				t.Errorf("Path() = %q with HOME=%q; want \"\"", p, tc.home)
			}

			later := t.TempDir()
			t.Setenv("HOME", later)
			if err := Emit(context.Background(), "ad.test", nil); !errors.Is(err, errNoHome) {
				t.Errorf("Emit after HOME was set = %v; want errNoHome (path pinned at Default)", err)
			}
			assertDirEmpty(t, later)
			assertDirEmpty(t, cwd)
		})
	}
}

// TestWriterWithoutAbsolutePathWritesNothing: a Writer whose path is empty or
// relative refuses with errNoHome and writes nothing under the cwd (b.iin).
func TestWriterWithoutAbsolutePathWritesNothing(t *testing.T) {
	for _, tc := range []struct{ name, path string }{
		{"empty", ""},
		{"relative", filepath.Join(".agent-director", trailFilename)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := cwdfix.Temp(t)
			var buf bytes.Buffer
			w := &Writer{path: tc.path, olog: log.New(&buf, "", 0)}

			err := w.Emit(context.Background(), "ad.test", nil)
			assertNoHomeEmit(t, err, cwd, buf.String())
		})
	}
}
