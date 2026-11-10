package spawn

import (
	"bytes"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/tmux"
	"golang.org/x/sys/unix"
)

func laneFiles(t *testing.T, token string) ComposedLaunch {
	t.Helper()
	dir, err := LaunchDirectory("store", token)
	if err != nil {
		t.Fatal(err)
	}
	return ComposedLaunch{Files: []LaunchFile{{Path: filepath.Join(dir, "droid-settings.json"), Content: []byte(`{"model":"model"}`)}}}
}

func TestPaneOnlyLaunchFiles(t *testing.T) {
	for _, agent := range []string{"droid"} {
		t.Run(agent, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			mask := unix.Umask(0)
			t.Cleanup(func() { unix.Umask(mask) })
			c := laneFiles(t, "token")
			if err := PrepareLaunchFiles(c, "store", "token", nil); err != nil {
				t.Fatal(err)
			}
			for path, mode := range map[string]os.FileMode{c.Files[0].Path: 0600, filepath.Dir(c.Files[0].Path): 0700} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != mode {
					t.Fatalf("%s: info=%v, err=%v; want %o", path, info, err, mode)
				}
			}
			got, err := os.ReadFile(c.Files[0].Path)
			if err != nil || !bytes.Equal(got, c.Files[0].Content) {
				t.Fatalf("settings=%q, %v", got, err)
			}
			if err := PrepareLaunchFiles(c, "store", "token", nil); !errors.Is(err, unix.EEXIST) {
				t.Fatalf("existing directory: %v", err)
			}
			if err := reclaimLaunchFiles("store", "token"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Dir(c.Files[0].Path)); !os.IsNotExist(err) {
				t.Fatalf("empty directory retained: %v", err)
			}
			if err := reclaimLaunchFiles("store", "token"); err != nil {
				t.Fatalf("absent directory: %v", err)
			}
		})
	}
}

func TestPaneOnlyPreparationRefusesExistingAndUnsafePaths(t *testing.T) {
	for _, kind := range []string{"directory", "directory symlink", "file", "file symlink", "unwritable", "duplicate file"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			c := laneFiles(t, "token")
			dir := filepath.Dir(c.Files[0].Path)
			outside := filepath.Join(t.TempDir(), "untouched")
			if err := os.WriteFile(outside, []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory symlink":
				if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Dir(outside), dir); err != nil {
					t.Fatal(err)
				}
			case "directory", "file", "file symlink":
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if kind == "file" {
					if err := os.WriteFile(c.Files[0].Path, []byte("existing"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "file symlink" {
					if err := os.Symlink(outside, c.Files[0].Path); err != nil {
						t.Fatal(err)
					}
				}
			case "unwritable":
				if err := os.Chmod(home, 0500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(home, 0700) })
			case "duplicate file":
				c.Files = append(c.Files, c.Files[0])
			}
			err := PrepareLaunchFiles(c, "store", "token", nil)
			if !errors.Is(err, tmux.ErrTmuxSessionCreate) {
				t.Fatalf("preparation error: %v", err)
			}
			if kind == "duplicate file" || kind == "unwritable" {
				entries, err := os.ReadDir(home)
				if err != nil || len(entries) != 0 {
					t.Fatalf("failed preparation left %v: %v", entries, err)
				}
			} else if _, err := os.Lstat(dir); err != nil {
				t.Fatalf("existing directory removed: %v", err)
			}
			got, err := os.ReadFile(outside)
			if err != nil || string(got) != "untouched" {
				t.Fatalf("outside file changed: %q, %v", got, err)
			}
		})
	}
}

func TestPaneOnlyPreparationFailureRollback(t *testing.T) {
	for _, step := range []string{"write", "short write", "sync", "close", "rollback"} {
		t.Run(step, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			c := laneFiles(t, "token")
			cause := errors.New("private injected detail")
			ops := defaultLaunchFileOps()
			want := cause
			var unlinks int
			switch step {
			case "write", "rollback":
				ops.write = func(*os.File, []byte) (int, error) { return 0, cause }
			case "short write":
				ops.write = func(*os.File, []byte) (int, error) { return 0, nil }
				want = io.ErrShortWrite
			case "sync":
				ops.sync = func(*os.File) error { return cause }
			case "close":
				ops.close = func(f *os.File) error { return errors.Join(f.Close(), cause) }
			}
			if step == "rollback" {
				ops.unlinkat = func(fd int, name string, flags int) error {
					unlinks++
					if unlinks == 1 {
						return cause
					}
					return unix.Unlinkat(fd, name, flags)
				}
				ops.closeDir = func(fd int) error { return errors.Join(unix.Close(fd), cause) }
			}
			var logs bytes.Buffer
			err := prepareLaunchFiles(c, "store", "token", log.New(&logs, "", 0), ops)
			if !errors.Is(err, tmux.ErrTmuxSessionCreate) || !errors.Is(err, want) {
				t.Fatalf("lost failure classification/cause: %v", err)
			}
			if strings.Contains(err.Error()+logs.String(), cause.Error()) {
				t.Fatalf("failure printed private cause: %v %s", err, &logs)
			}
			if step == "rollback" {
				if unlinks != 6 || strings.Count(logs.String(), "WARN:") != 1 || !strings.Contains(logs.String(), `store_id="store" launch_token="token" step="rollback preparation"`) {
					t.Fatalf("rollback stopped or lost diagnostic: unlinks=%d, %s", unlinks, &logs)
				}
			} else {
				entries, readErr := os.ReadDir(home)
				if readErr != nil || len(entries) != 0 || logs.Len() != 0 {
					t.Fatalf("rollback left entries=%v, err=%v, logs=%s", entries, readErr, &logs)
				}
			}
		})
	}
}

func TestPaneOnlyReclaimPreservesUnownedEntries(t *testing.T) {
	for _, kind := range []string{"native file", "symlink", "directory", "fifo", "other user"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "other user" && os.Geteuid() != 0 {
				t.Skip("requires the separate root-only ownership check in the test container")
			}
			t.Setenv("HOME", t.TempDir())
			c, other := laneFiles(t, "token"), laneFiles(t, "other-token")
			for token, files := range map[string]ComposedLaunch{"token": c, "other-token": other} {
				if err := PrepareLaunchFiles(files, "store", token, nil); err != nil {
					t.Fatal(err)
				}
			}
			path := c.Files[0].Path
			preserved := path
			if kind == "native file" {
				preserved = filepath.Join(filepath.Dir(path), "native-session.jsonl")
				if err := os.WriteFile(preserved, []byte("native"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if kind == "other user" {
				if err := os.Chown(path, 65534, -1); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				var err error
				switch kind {
				case "symlink":
					err = os.Symlink(other.Files[0].Path, path)
				case "directory":
					err = os.Mkdir(path, 0700)
				case "fifo":
					err = unix.Mkfifo(path, 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			ReclaimLaunchFiles("store", "token", nil)
			if _, err := os.Lstat(preserved); err != nil {
				t.Fatalf("unowned entry removed: %v", err)
			}
			if got, err := os.ReadFile(other.Files[0].Path); err != nil || !bytes.Equal(got, other.Files[0].Content) {
				t.Fatalf("other launch changed: %q, %v", got, err)
			}
			if kind == "native file" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("generated file retained: %v", err)
				}
			}
		})
	}
}
