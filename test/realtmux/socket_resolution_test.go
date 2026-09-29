package realtmux_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// RN-5 on real tmux (SRD SR-3.3, SR-20.7; AC-LKP-19): tmux.ResolveSocket
// names the socket tmux itself uses without -S, and refuses the per-user
// directories tmux refuses.

// uidDir is the per-user directory's base name, tmux-<uid>.
func uidDir() string { return "tmux-" + strconv.Itoa(os.Getuid()) }

// chdirFor makes dir the working directory for the rest of the test and
// restores the old one in t.Cleanup (go 1.22 has no t.Chdir).
func chdirFor(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Errorf("restore working directory %s: %v", old, err)
		}
	})
}

// mkdirs makes each directory with mode 0700, failing the test on error.
func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
}

// mustResolve is tmux.ResolveSocket failing the test on any error.
func mustResolve(t *testing.T, create bool) string {
	t.Helper()
	got, err := tmux.ResolveSocket(create)
	if err != nil {
		t.Fatalf("ResolveSocket(%v): %s", create, describe(err))
	}
	return got
}

// TestSocketResolutionMatchesTmux: ResolveSocket, before and after tmux runs,
// equals #{socket_path} of the server tmux starts without -S in the same env.
func TestSocketResolutionMatchesTmux(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, rt *realTmux) (want string)
	}{
		{"TMUX unset uses the private TMUX_TMPDIR", func(t *testing.T, rt *realTmux) string {
			return rt.Socket
		}},
		{"TMUX with an empty first field is ignored", func(t *testing.T, rt *realTmux) string {
			t.Setenv("TMUX", ","+strconv.Itoa(os.Getpid())+",0")
			return rt.Socket
		}},
		{"relative TMUX_TMPDIR resolves against the working directory", func(t *testing.T, rt *realTmux) string {
			mkdirs(t, filepath.Join(rt.Dir, "wd"), filepath.Join(rt.Dir, "base"))
			chdirFor(t, filepath.Join(rt.Dir, "wd"))
			t.Setenv("TMUX_TMPDIR", "../base")
			return filepath.Join(rt.Dir, "base", uidDir(), "default")
		}},
		{"symlinked TMUX_TMPDIR resolves to its real path", func(t *testing.T, rt *realTmux) string {
			realDir := filepath.Join(rt.Dir, "real")
			mkdirs(t, realDir)
			if err := os.Symlink(realDir, filepath.Join(rt.Dir, "link")); err != nil {
				t.Fatalf("symlink: %v", err)
			}
			t.Setenv("TMUX_TMPDIR", filepath.Join(rt.Dir, "link"))
			return filepath.Join(realDir, uidDir(), "default")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := newRealTmux(t)
			want := tc.setup(t, rt)
			before := mustResolve(t, false) // before any tmux call: nothing created yet
			s := rt.raw().withoutS().startSession(t, "", "")
			own := strings.TrimSuffix(rt.raw().withoutS().must(t, "display-message", "-p", "-t", s.ID, "#{socket_path}"), "\n")
			after := mustResolve(t, true)
			if own != want {
				t.Errorf("tmux's own socket path = %q, want %q", own, want)
			}
			if before != own || after != own {
				t.Errorf("ResolveSocket = %q before and %q after tmux ran, tmux's own path %q", before, after, own)
			}
		})
	}
}

// TestSocketFallbackToTmpMatchesTmux (R3b): an unset TMUX_TMPDIR, or one naming
// nothing (a ':' value is one path), resolves as tmux does to real(/tmp)/tmux-<uid>/default.
func TestSocketFallbackToTmpMatchesTmux(t *testing.T) {
	cases := []struct {
		name   string
		tmpdir func(rt *realTmux) (value string, set bool)
	}{
		{"TMUX_TMPDIR names a missing directory", func(rt *realTmux) (string, bool) {
			return filepath.Join(rt.Dir, "nope"), true
		}},
		{"TMUX_TMPDIR with a colon is one missing path", func(rt *realTmux) (string, bool) {
			return filepath.Join(rt.Dir, "nope") + ":" + rt.Dir, true
		}},
		{"TMUX_TMPDIR unset", func(rt *realTmux) (string, bool) { return "", false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := newRealTmux(t)
			if v, set := tc.tmpdir(rt); set {
				t.Setenv("TMUX_TMPDIR", v)
			} else {
				unsetEnv(t, "TMUX_TMPDIR")
			}
			got, err := tmux.ResolveSocket(false) // before any raw tmux call
			if err != nil {
				t.Fatalf("ResolveSocket(false): %s", describe(err))
			}
			tmp, err := filepath.EvalSymlinks("/tmp")
			if err != nil {
				t.Fatalf("real path of /tmp: %v", err)
			}
			want := filepath.Join(tmp, uidDir(), "default")
			if got != want {
				t.Fatalf("ResolveSocket(false) = %q, want %q", got, want)
			}
			// tmux's own path, read-only: from a listing when a server is there
			// (test/reboot-recovery may run one), else from the no-server / no-socket reply.
			noServer, noSocket := tmuxfix.NoServer(want), tmuxfix.NoSocket(want)
			is := func(r rawResult, e tmuxfix.Entry) bool {
				return r.Stdout == e.Stdout && r.Stderr == e.Stderr && r.Exit == e.Exit
			}
			var last rawResult
			waitFor(t, "tmux without -S to name its default socket "+want, func() bool {
				last = rt.raw().withoutS().run(t, "list-sessions", "-F", "#{socket_path}")
				return (last.Exit == 0 && last.Stdout != "") || is(last, noServer) || is(last, noSocket)
			}, func() string {
				return strconv.Quote(last.Stdout) + " " + strconv.Quote(last.Stderr) + " exit " + strconv.Itoa(last.Exit)
			})
			if last.Exit == 0 {
				for _, line := range strings.Split(strings.TrimSuffix(last.Stdout, "\n"), "\n") {
					if line != want {
						t.Errorf("tmux's own socket path = %q, want %q", line, want)
					}
				}
			}
			if _, err := os.Lstat(filepath.Join(rt.Dir, "nope")); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("%s exists after resolution (err %v); nothing may create it", filepath.Join(rt.Dir, "nope"), err)
			}
		})
	}
}

// TestSocketUnsafeUserDirRefused: ResolveSocket (both create values) and tmux
// without -S refuse an unsafe per-user directory; tmux creates no socket.
func TestSocketUnsafeUserDirRefused(t *testing.T) {
	cases := []struct {
		name     string
		reason   tmux.SocketDirReason
		rootOnly bool
		setup    func(t *testing.T, rt *realTmux)
	}{
		{"mode 0755", tmux.SocketDirUnsafePermissions, false, func(t *testing.T, rt *realTmux) {
			if err := os.Chmod(rt.UserDir, 0o755); err != nil {
				t.Fatalf("chmod: %v", err)
			}
		}},
		{"symlink to a 0700 directory", tmux.SocketDirSymlink, false, func(t *testing.T, rt *realTmux) {
			target := filepath.Join(rt.Dir, "target")
			mkdirs(t, target)
			if err := os.Remove(rt.UserDir); err != nil {
				t.Fatalf("remove per-user directory: %v", err)
			}
			if err := os.Symlink(target, rt.UserDir); err != nil {
				t.Fatalf("symlink: %v", err)
			}
		}},
		{"owned by another user", tmux.SocketDirNotOwned, true, func(t *testing.T, rt *realTmux) {
			if err := os.Chown(rt.UserDir, 4242, 4242); err != nil {
				t.Fatalf("chown: %v", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.rootOnly && os.Getuid() != 0 {
				t.Skipf("a foreign-owned per-user directory needs root to chown (SR-20.7); running as uid %d", os.Getuid())
			}
			rt := newRealTmux(t)
			tc.setup(t, rt)
			for _, create := range []bool{false, true} {
				got, err := tmux.ResolveSocket(create)
				var sde *tmux.SocketDirError
				if !errors.As(err, &sde) {
					t.Fatalf("ResolveSocket(%v) = %q, %s; want a *tmux.SocketDirError", create, got, describe(err))
				}
				if !errors.Is(err, tmux.ErrTmuxNotAvailable) {
					t.Errorf("ResolveSocket(%v): errors.Is(err, ErrTmuxNotAvailable) = false", create)
				}
				if sde.Reason != tc.reason || sde.Dir != rt.UserDir || sde.Socket != rt.Socket {
					t.Errorf("ResolveSocket(%v): {Reason %s, Dir %q, Socket %q}, want {Reason %s, Dir %q, Socket %q}",
						create, sde.Reason, sde.Dir, sde.Socket, tc.reason, rt.UserDir, rt.Socket)
				}
			}
			res := rt.raw().withoutS().run(t, append([]string{"new-session", "-d", "--"}, stubCommand()...)...)
			if res.Exit == 0 {
				t.Errorf("tmux without -S accepted the per-user directory: stdout %q", res.Stdout)
			}
			if s := socketsUnder(rt.Dir); len(s) != 0 {
				t.Errorf("tmux created sockets %q despite the refusal", s)
			}
		})
	}
}

// TestSocketCreatedDirReachedWithoutS (R5): ResolveSocket(true) makes a missing
// per-user directory owner-only; a -S create there is reached later without -S.
func TestSocketCreatedDirReachedWithoutS(t *testing.T) {
	rt := newRealTmux(t)
	if err := os.Remove(rt.UserDir); err != nil {
		t.Fatalf("remove per-user directory: %v", err)
	}
	got := mustResolve(t, true)
	if got != rt.Socket {
		t.Fatalf("ResolveSocket(true) = %q, want %q", got, rt.Socket)
	}
	fi, err := os.Lstat(rt.UserDir)
	if err != nil {
		t.Fatalf("per-user directory not created: %v", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok || int(st.Uid) != os.Getuid() || fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("per-user directory mode %s, owner ok %v; want an owner-only directory owned by uid %d",
			fi.Mode(), ok && int(st.Uid) == os.Getuid(), os.Getuid())
	}
	c := rt.at(t, got).mustCreate(t, createSpec{})
	out := rt.raw().withoutS().must(t, "display-message", "-p", "-t", c.Reply.SessionID, "#{pid}\t#{socket_path}")
	wantOut := strconv.Itoa(c.Reply.ServerPID) + "\t" + got + "\n"
	if out != wantOut {
		t.Errorf("without -S tmux reached pid and socket %q, want %q (the -S create's server)", out, wantOut)
	}
}
