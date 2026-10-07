package tmux_test

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

// resolveCase arranges a tree and env; arrange returns the wanted socket, or
// for a refusal the refused per-user directory (its socket is dir/default).
type resolveCase struct {
	name    string
	create  bool
	arrange func(f *socketFixture) string
	reason  tmux.SocketDirReason // zero: success
	creates bool                 // success made the per-user directory of the result
}

func resolveCases() []resolveCase {
	withBase := func(rel string) func(f *socketFixture) string {
		return func(f *socketFixture) string {
			base := f.mkdir(rel, 0o755)
			f.env.Env["TMUX_TMPDIR"] = base
			return filepath.Join(f.userDir(base), "default")
		}
	}
	cases := []resolveCase{
		// Step 1: TMUX.
		{name: "TMUX first field verbatim naming nothing", create: true, arrange: func(f *socketFixture) string {
			f.env.Env["TMUX"] = f.root + "/srv/../srv//dead.sock,4321,0"
			return f.root + "/srv/../srv//dead.sock"
		}},
		{name: "TMUX wins over TMUX_TMPDIR", create: true, arrange: func(f *socketFixture) string {
			f.env.Env["TMUX_TMPDIR"] = f.mkdir("base", 0o755)
			f.env.Env["TMUX"] = f.path("s.sock") + ",1,0"
			return f.path("s.sock")
		}},
		{name: "TMUX wins over unsafe per-user dir", create: true, arrange: func(f *socketFixture) string {
			f.mkdir("defbase/tmux-"+strconv.Itoa(f.env.UID), 0o777)
			f.env.Env["TMUX"] = f.path("s.sock") + ",1,0"
			return f.path("s.sock")
		}},
		{name: "TMUX empty is ignored", arrange: func(f *socketFixture) string {
			f.env.Env["TMUX"] = ""
			return f.defaultSocket()
		}},
		{name: "TMUX empty first field is ignored R3a", create: true, creates: true, arrange: func(f *socketFixture) string {
			f.env.Env["TMUX"] = ",123,0"
			return f.defaultSocket()
		}},
		{name: "relative TMUX joined to working dir", create: true, arrange: func(f *socketFixture) string {
			f.env.Env["TMUX"] = "rel/s.sock,1,0"
			return f.env.Wd + "/rel/s.sock"
		}},

		// Step 2: the base directory.
		{name: "TMUX_TMPDIR unset uses default base", arrange: func(f *socketFixture) string {
			return f.defaultSocket()
		}},
		{name: "TMUX_TMPDIR empty uses default base", arrange: func(f *socketFixture) string {
			f.env.Env["TMUX_TMPDIR"] = ""
			return f.defaultSocket()
		}},
		{name: "TMUX_TMPDIR existing dir", create: true, creates: true, arrange: withBase("base")},
		{name: "TMUX_TMPDIR trailing slash", arrange: func(f *socketFixture) string {
			want := withBase("base")(f)
			f.env.Env["TMUX_TMPDIR"] += "/"
			return want
		}},
		{name: "TMUX_TMPDIR symlink resolved R1", create: true, creates: true, arrange: func(f *socketFixture) string {
			want := withBase("real")(f)
			f.env.Env["TMUX_TMPDIR"] = f.symlink(f.path("real"), "link")
			return want
		}},
		{name: "TMUX_TMPDIR relative resolved against wd R3c", create: true, creates: true, arrange: func(f *socketFixture) string {
			want := withBase("wd/sub")(f)
			f.env.Env["TMUX_TMPDIR"] = "sub"
			return want
		}},
		{name: "TMUX_TMPDIR missing falls back R3b", create: true, creates: true, arrange: func(f *socketFixture) string {
			f.env.Env["TMUX_TMPDIR"] = f.path("missing")
			return f.defaultSocket()
		}},
		{name: "TMUX_TMPDIR missing falls back R3b lookup", arrange: func(f *socketFixture) string {
			f.env.Env["TMUX_TMPDIR"] = f.path("missing")
			return f.defaultSocket()
		}},
		{name: "TMUX_TMPDIR colon value is one path", arrange: func(f *socketFixture) string {
			f.env.Env["TMUX_TMPDIR"] = f.mkdir("a", 0o755) + ":" + f.mkdir("b", 0o755)
			return f.defaultSocket()
		}},
		{name: "TMUX_TMPDIR dir named with colon", create: true, creates: true, arrange: withBase("x:y")},
		{name: "TMUX_TMPDIR under unsearchable dir falls back", arrange: func(f *socketFixture) string {
			f.env.Env["TMUX_TMPDIR"] = f.mkdir("locked/sub", 0o755)
			f.lockDir("locked")
			return f.defaultSocket()
		}},
		{name: "default base symlink resolved", create: true, creates: true, arrange: func(f *socketFixture) string {
			real := f.mkdir("realbase", 0o755)
			f.env.DefaultBase = f.symlink(real, "baselink")
			return filepath.Join(f.userDir(real), "default")
		}},

		// Step 3: the per-user directory.
		{name: "per-user dir missing lookup creates nothing", arrange: withBase("base")},
		{name: "creation fails in unwritable base", create: true, reason: tmux.SocketDirCreateFailed, arrange: func(f *socketFixture) string {
			skipIfRoot(f.t, "root can create in a 0555 directory")
			base := f.mkdir("ro", 0o555)
			f.env.Env["TMUX_TMPDIR"] = base
			return f.userDir(base)
		}},
	}
	for _, create := range []bool{true, false} {
		mode := "lookup"
		if create {
			mode = "create"
		}
		row := func(name string, reason tmux.SocketDirReason, arrange func(f *socketFixture) string) {
			cases = append(cases, resolveCase{name: name + " " + mode, create: create, reason: reason, arrange: arrange})
		}
		userDirRel := func(f *socketFixture) string { return "defbase/tmux-" + strconv.Itoa(f.env.UID) }
		row("TMUX_TMPDIR regular file refused R3d", tmux.SocketDirCreateFailed, func(f *socketFixture) string {
			f.env.Env["TMUX_TMPDIR"] = f.file("plain")
			return f.userDir(f.path("plain"))
		})
		for _, perm := range []fs.FileMode{0o700, 0o750, 0o700 | fs.ModeSetgid} {
			perm := perm
			name := fmt.Sprintf("%04o", perm.Perm())
			if perm&fs.ModeSetgid != 0 {
				name += " setgid"
			}
			row("existing per-user dir "+name+" accepted", 0, func(f *socketFixture) string {
				f.mkdir(userDirRel(f), perm)
				return f.defaultSocket()
			})
		}
		for _, perm := range []fs.FileMode{0o755, 0o701, 0o702, 0o704, 0o777} {
			perm := perm
			row(fmt.Sprintf("per-user dir %04o refused R4a", perm), tmux.SocketDirUnsafePermissions, func(f *socketFixture) string {
				return f.mkdir(userDirRel(f), perm)
			})
		}
		row("per-user dir symlink refused R4b", tmux.SocketDirSymlink, func(f *socketFixture) string {
			return f.symlink(f.mkdir("valid", 0o700), userDirRel(f))
		})
		row("per-user dir regular file refused", tmux.SocketDirNotDirectory, func(f *socketFixture) string {
			return f.file(userDirRel(f))
		})
		row("per-user dir foreign owner refused R4c", tmux.SocketDirNotOwned, func(f *socketFixture) string {
			f.env.UID = 4242
			return f.mkdir(userDirRel(f), 0o700)
		})
	}
	return cases
}

// TestResolveSocket covers each RN-5 resolution and check case through the seam.
func TestResolveSocket(t *testing.T) {
	for _, tc := range resolveCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := newSocketFixture(t)
			want := tc.arrange(f)
			before := snapshot(t, f.root)

			got, err := f.env.ResolveSocket(tc.create)

			if tc.reason != 0 {
				assertRefusal(t, err, tc.reason, filepath.Join(want, "default"), want)
				assertTree(t, f.root, before, "")
				return
			}
			if err != nil {
				t.Fatalf("ResolveSocket(%v) error: %v", tc.create, err)
			}
			if got != want || !filepath.IsAbs(got) {
				t.Errorf("ResolveSocket(%v) = %q, want absolute %q", tc.create, got, want)
			}
			created := ""
			if tc.creates {
				created = filepath.Dir(want)
			}
			assertTree(t, f.root, before, created)
		})
	}
}

// TestResolveSocketRelativeTMUXNoWorkingDir: an unreadable working directory
// refuses a relative TMUX field as documented.
func TestResolveSocketRelativeTMUXNoWorkingDir(t *testing.T) {
	f := newSocketFixture(t)
	f.env.Env["TMUX"] = "rel/s.sock,1,0"
	f.env.WdErr = errors.New("getwd failed")
	_, err := f.env.ResolveSocket(true)
	assertRefusal(t, err, tmux.SocketDirUnreadable, "rel/s.sock", ".")
}

// TestSocketProductionWiring runs the exported functions on paths that never
// reach /tmp: a set TMUX and an existing socket directory.
func TestSocketProductionWiring(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMUX", dir+"/x.sock,1,0")
	if got, err := tmux.ResolveSocket(true); err != nil || got != dir+"/x.sock" {
		t.Errorf("ResolveSocket(true) = %q, %v; want %q", got, err, dir+"/x.sock")
	}
	if err := tmux.EnsureSocketDir(dir + "/x.sock"); err != nil {
		t.Errorf("EnsureSocketDir: %v", err)
	}
}
