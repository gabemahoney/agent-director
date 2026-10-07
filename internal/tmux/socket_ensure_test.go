package tmux_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// TestEnsureSocketDir covers a recorded socket whose directory exists, is
// recreated, or is refused without creating anything.
func TestEnsureSocketDir(t *testing.T) {
	cases := []struct {
		name    string
		arrange func(f *socketFixture) string // returns the recorded socket
		reason  tmux.SocketDirReason          // zero: success
		creates bool
	}{
		{name: "missing per-user dir under existing base is created", creates: true, arrange: func(f *socketFixture) string {
			return filepath.Join(f.userDir(f.mkdir("base", 0o755)), "default")
		}},
		{name: "existing valid per-user dir unchanged", arrange: func(f *socketFixture) string {
			return f.mkdir("base/tmux-"+strconv.Itoa(f.env.UID), 0o700) + "/default"
		}},
		{name: "existing unsafe per-user dir left unchecked", arrange: func(f *socketFixture) string {
			return f.mkdir("base/tmux-"+strconv.Itoa(f.env.UID), 0o777) + "/default"
		}},
		{name: "existing dir of another server's TMUX socket unchanged", arrange: func(f *socketFixture) string {
			return f.mkdir("other", 0o777) + "/x.sock"
		}},
		{name: "missing base refused", reason: tmux.SocketDirNotCreatable, arrange: func(f *socketFixture) string {
			return filepath.Join(f.userDir(f.path("nobase")), "default")
		}},
		{name: "missing dir with another name refused", reason: tmux.SocketDirNotCreatable, arrange: func(f *socketFixture) string {
			return f.mkdir("base", 0o755) + "/other/default"
		}},
		{name: "missing dir of another uid refused", reason: tmux.SocketDirNotCreatable, arrange: func(f *socketFixture) string {
			return f.mkdir("base", 0o755) + "/tmux-4242/default"
		}},
		{name: "relative socket refused", reason: tmux.SocketDirNotCreatable, arrange: func(f *socketFixture) string {
			return "tmux-" + strconv.Itoa(f.env.UID) + "/default"
		}},
		{name: "parent regular file refused", reason: tmux.SocketDirCreateFailed, arrange: func(f *socketFixture) string {
			return filepath.Join(f.userDir(f.file("plain")), "default")
		}},
		{name: "creation fails in unwritable parent", reason: tmux.SocketDirCreateFailed, arrange: func(f *socketFixture) string {
			skipIfRoot(f.t, "root can create in a 0555 directory")
			return filepath.Join(f.userDir(f.mkdir("ro", 0o555)), "default")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSocketFixture(t)
			socket := tc.arrange(f)
			before := snapshot(t, f.root)

			err := f.env.EnsureSocketDir(socket)

			if !filepath.IsAbs(socket) {
				if _, serr := os.Lstat(filepath.Dir(socket)); !errors.Is(serr, fs.ErrNotExist) {
					t.Errorf("relative %s exists after the call: %v", filepath.Dir(socket), serr)
				}
			}
			if tc.reason != 0 {
				assertRefusal(t, err, tc.reason, socket, filepath.Dir(socket))
				assertTree(t, f.root, before, "")
				return
			}
			if err != nil {
				t.Fatalf("EnsureSocketDir(%q) error: %v", socket, err)
			}
			created := ""
			if tc.creates {
				created = filepath.Dir(socket)
			}
			assertTree(t, f.root, before, created)
		})
	}
}
