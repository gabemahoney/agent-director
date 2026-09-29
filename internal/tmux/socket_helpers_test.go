package tmux_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// socketFixture is a private temp tree plus a SocketEnv rooted in it, so no
// case reads or writes the real /tmp/tmux-<uid> or HOME.
type socketFixture struct {
	t    *testing.T
	root string
	env  tmux.SocketEnv
}

// newSocketFixture gives a real-path temp root with a default base and a
// working directory inside it, and the process uid as the seam uid.
func newSocketFixture(t *testing.T) *socketFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &socketFixture{t: t, root: root}
	f.env = tmux.SocketEnv{
		Env:         map[string]string{},
		Wd:          f.mkdir("wd", 0o755),
		UID:         os.Getuid(),
		DefaultBase: f.mkdir("defbase", 0o755),
	}
	return f
}

func (f *socketFixture) path(rel string) string { return filepath.Join(f.root, rel) }

// mkdir makes root/rel with exactly perm (setgid included) and returns its path.
func (f *socketFixture) mkdir(rel string, perm fs.FileMode) string {
	f.t.Helper()
	p := f.path(rel)
	if err := os.MkdirAll(p, 0o700); err != nil {
		f.t.Fatal(err)
	}
	if perm&fs.ModeSetgid != 0 {
		// A non-member group would make the kernel drop the setgid bit.
		if err := os.Chown(p, -1, os.Getgid()); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := os.Chmod(p, perm); err != nil {
		f.t.Fatal(err)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode()&(fs.ModePerm|fs.ModeSetgid) != perm {
		f.t.Fatalf("mkdir %s: mode not set to %v (%v)", rel, perm, err)
	}
	return p
}

func (f *socketFixture) file(rel string) string {
	f.t.Helper()
	p := f.path(rel)
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *socketFixture) symlink(target, rel string) string {
	f.t.Helper()
	p := f.path(rel)
	if err := os.Symlink(target, p); err != nil {
		f.t.Fatal(err)
	}
	return p
}

// userDir is base/tmux-<seam uid>.
func (f *socketFixture) userDir(base string) string {
	return filepath.Join(base, "tmux-"+strconv.Itoa(f.env.UID))
}

func (f *socketFixture) defaultSocket() string {
	return filepath.Join(f.userDir(f.env.DefaultBase), "default")
}

// lockDir makes root/rel unsearchable (0000) until cleanup; skipped as root.
func (f *socketFixture) lockDir(rel string) string {
	f.t.Helper()
	skipIfRoot(f.t, "root bypasses directory permission bits")
	p := f.path(rel)
	if err := os.Chmod(p, 0); err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = os.Chmod(p, 0o755) })
	return p
}

func skipIfRoot(t *testing.T, why string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("skipped as root: " + why)
	}
}

type fsEntry struct {
	mode fs.FileMode
	uid  uint32
}

// snapshot records the type, mode and owner of every entry under root.
func snapshot(t *testing.T, root string) map[string]fsEntry {
	t.Helper()
	out := map[string]fsEntry{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable directory is still recorded by its first visit
		}
		fi, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		out[p] = fsEntry{mode: fi.Mode(), uid: fi.Sys().(*syscall.Stat_t).Uid}
		return nil
	})
	return out
}

// assertTree checks the tree under root equals before, except for created
// (when not empty): a new 0700 directory owned by the process uid.
func assertTree(t *testing.T, root string, before map[string]fsEntry, created string) {
	t.Helper()
	after := snapshot(t, root)
	if created != "" {
		e, ok := after[created]
		switch {
		case !ok:
			t.Errorf("%s was not created", created)
		case !e.mode.IsDir() || e.mode.Perm() != 0o700 || e.uid != uint32(os.Getuid()):
			t.Errorf("%s created as %v uid %d, want a 0700 directory owned by uid %d", created, e.mode, e.uid, os.Getuid())
		}
		delete(after, created)
	}
	if !reflect.DeepEqual(before, after) {
		t.Errorf("tree changed:\nbefore %v\nafter  %v", before, after)
	}
}

// assertRefusal checks err is a *SocketDirError of the given kind and paths
// that matches ErrTmuxNotAvailable only and never reads as a missing binary.
func assertRefusal(t *testing.T, err error, reason tmux.SocketDirReason, socket, dir string) {
	t.Helper()
	var sde *tmux.SocketDirError
	if !errors.As(err, &sde) {
		t.Fatalf("err = %v (%T), want *tmux.SocketDirError", err, err)
	}
	if sde.Reason != reason || sde.Socket != socket || sde.Dir != dir {
		t.Errorf("got {%v, socket %q, dir %q}, want {%v, socket %q, dir %q}",
			sde.Reason, sde.Socket, sde.Dir, reason, socket, dir)
	}
	if !errors.Is(err, tmux.ErrTmuxNotAvailable) {
		t.Error("errors.Is(err, ErrTmuxNotAvailable) = false")
	}
	for _, other := range []error{tmux.ErrTmuxSessionCreate, tmux.ErrTmuxKillFailed,
		tmux.ErrTmuxListPanesFailed, tmux.ErrTmuxSendKeys, tmux.ErrTmuxCaptureFailed} {
		if errors.Is(err, other) {
			t.Errorf("err matches %v", other)
		}
	}
	var ce *tmux.CallError
	if errors.As(err, &ce) {
		t.Error("err is a *CallError")
	}
	msg := err.Error()
	if !strings.Contains(msg, dir) {
		t.Errorf("Error() %q does not name %q", msg, dir)
	}
	if strings.Contains(msg, "binary") || strings.Contains(msg, "PATH") {
		t.Errorf("Error() %q reads as a missing binary", msg)
	}
}
