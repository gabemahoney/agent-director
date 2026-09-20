//go:build linux

package probe

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstat"
)

// writeFakeProc and writeStatOnly live in the UNTAGGED fakeproc_test.go so the
// tag-free checker verdict-table tests compile off-linux; this file uses them.

// newFakeReader builds a linuxProcReader over a fabricated proc root with the
// given starting pid.
func newFakeReader(root string, self int) *linuxProcReader {
	return &linuxProcReader{procRoot: root, self: self}
}

// TestLinuxResolverFabricatedTopmostMatch exercises the full linuxResolver
// stack over a fabricated proc root (via linuxProcReader with an injected self
// pid). Child AND parent both carry the matching var → the TOPMOST wins, and
// its starttime is returned VERBATIM = procstarttimefix.LinuxProcStarttime. A
// nearest-match implementation fails this test.
func TestLinuxResolverFabricatedTopmostMatch(t *testing.T) {
	const id = "fab-topmost-id"
	root := t.TempDir()

	// self(100) matches; parent(200) matches and is topmost, planting the
	// fixture starttime; grandparent(300) lacks the var → terminates.
	writeFakeProc(t, root, 100, 200, "11111111", id)
	writeFakeProc(t, root, 200, 300, procstarttimefix.LinuxProcStarttime, id)
	writeFakeProc(t, root, 300, 1, "22222222", "")

	pid, start, err := walkAncestors(newFakeReader(root, 100), id)
	if err != nil {
		t.Fatalf("walkAncestors: %v", err)
	}
	if pid != 200 {
		t.Errorf("pid = %d; want 200 (topmost)", pid)
	}
	if start != procstarttimefix.LinuxProcStarttime {
		t.Errorf("starttime = %q; want %q (verbatim fixture)", start, procstarttimefix.LinuxProcStarttime)
	}
}

// TestLinuxProcReaderInstanceID exercises the file-reading layer directly:
// instanceID over a fabricated environ returns the planted value, and a pid
// with no matching var (or a missing environ) is a routine non-match.
func TestLinuxProcReaderInstanceID(t *testing.T) {
	const id = "reader-env-id"
	root := t.TempDir()
	writeFakeProc(t, root, 42, 1, "9", id) // has the var
	writeFakeProc(t, root, 43, 1, "9", "") // no matching var

	r := newFakeReader(root, 42)

	got, ok, err := r.instanceID(42)
	if err != nil {
		t.Fatalf("instanceID(42): %v", err)
	}
	if !ok || got != id {
		t.Errorf("instanceID(42) = (%q,%v); want (%q,true)", got, ok, id)
	}

	got, ok, err = r.instanceID(43)
	if err != nil {
		t.Fatalf("instanceID(43): %v", err)
	}
	if ok || got != "" {
		t.Errorf("instanceID(43) = (%q,%v); want (\"\",false)", got, ok)
	}

	// Missing environ (pid absent from the tree) → routine non-match, no error.
	got, ok, err = r.instanceID(9999)
	if err != nil || ok || got != "" {
		t.Errorf("instanceID(missing) = (%q,%v,%v); want (\"\",false,nil)", got, ok, err)
	}
}

// TestLinuxProcReaderParent exercises the file-reading layer directly: parent
// over a fabricated stat returns the ppid + verbatim starttime, and a missing
// stat is a clean end-of-chain (present=false), not an error.
func TestLinuxProcReaderParent(t *testing.T) {
	root := t.TempDir()
	writeFakeProc(t, root, 42, 7, procstarttimefix.LinuxProcStarttime, "")

	r := newFakeReader(root, 42)

	ppid, start, present, err := r.parent(42)
	if err != nil {
		t.Fatalf("parent(42): %v", err)
	}
	if !present {
		t.Fatal("parent(42) present = false; want true")
	}
	if ppid != 7 {
		t.Errorf("ppid = %d; want 7 (field 4, past a comm with ')')", ppid)
	}
	if start != procstarttimefix.LinuxProcStarttime {
		t.Errorf("starttime = %q; want %q (field 22 verbatim)", start, procstarttimefix.LinuxProcStarttime)
	}

	// Missing stat → clean end-of-chain.
	_, _, present, err = r.parent(9999)
	if err != nil {
		t.Fatalf("parent(missing): %v", err)
	}
	if present {
		t.Error("parent(missing) present = true; want false (vanished ancestor)")
	}
}

// TestLinuxProcReaderMalformedStatHardError pins that a malformed stat line is
// surfaced by parent() as the ErrLinuxStatMalformed hard error (which aborts
// the walk verbatim).
func TestLinuxProcReaderMalformedStatHardError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "42")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// No ')' at all → parseLinuxStat returns ErrLinuxStatMalformed.
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte("42 no-parens here\n"), 0o644); err != nil {
		t.Fatalf("write stat: %v", err)
	}

	r := newFakeReader(root, 42)
	_, _, _, err := r.parent(42)
	if !errors.Is(err, ErrLinuxStatMalformed) {
		t.Fatalf("parent err = %v; want ErrLinuxStatMalformed", err)
	}
}

// TestLinuxResolverFabricatedNoMatch pins that a fabricated chain with no
// matching ancestor yields ErrNoMatchingAncestor and no bogus pid.
func TestLinuxResolverFabricatedNoMatch(t *testing.T) {
	root := t.TempDir()
	writeFakeProc(t, root, 100, 200, "1", "")
	writeFakeProc(t, root, 200, 1, "2", "")

	pid, start, err := walkAncestors(newFakeReader(root, 100), "nobody")
	if !errors.Is(err, ErrNoMatchingAncestor) {
		t.Fatalf("err = %v; want ErrNoMatchingAncestor", err)
	}
	if pid != 0 || start != "" {
		t.Errorf("got pid=%d start=%q; want zero identity", pid, start)
	}
}

// TestLinuxResolverFabricatedMidWalkVanish pins the mid-walk contract at the
// file layer: the parent's stat/environ are DELETED partway. Because no match
// was seen before the vanish, present=false ends the walk cleanly with the
// typed no-match error and no partial identity.
func TestLinuxResolverFabricatedMidWalkVanish(t *testing.T) {
	const id = "vanish-id"
	root := t.TempDir()
	// self(100) has no match; its parent(200) would match, but we remove
	// 200's files so 100's parent() read yields present=false first.
	writeFakeProc(t, root, 100, 200, "1", "")
	// Intentionally do NOT create pid 200 → its stat is absent.

	pid, start, err := walkAncestors(newFakeReader(root, 100), id)
	if !errors.Is(err, ErrNoMatchingAncestor) {
		t.Fatalf("err = %v; want ErrNoMatchingAncestor", err)
	}
	if pid != 0 || start != "" {
		t.Errorf("got pid=%d start=%q; want zero identity", pid, start)
	}
}

// TestLinuxResolverFabricatedUnreadableEnvTolerated simulates a foreign-uid
// ancestor whose environ is unreadable (chmod 000) — under the sandbox's
// non-root uid this is a genuine EACCES. instanceID must treat it as a
// non-match and the walk must continue to a matching ancestor above it.
func TestLinuxResolverFabricatedUnreadableEnvTolerated(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not deny reads; skipping")
	}
	const id = "past-unreadable-id"
	root := t.TempDir()

	// self(100): environ chmod 000 → unreadable → non-match, continue.
	writeFakeProc(t, root, 100, 200, "1", id) // planted, but made unreadable below
	unreadable := filepath.Join(root, "100", "environ")
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatalf("chmod 000: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o644) })

	// parent(200): matches → found despite the unreadable child.
	writeFakeProc(t, root, 200, 1, procstarttimefix.LinuxProcStarttime, id)

	pid, start, err := walkAncestors(newFakeReader(root, 100), id)
	if err != nil {
		t.Fatalf("walkAncestors: %v", err)
	}
	if pid != 200 {
		t.Errorf("pid = %d; want 200 (matched past the unreadable child)", pid)
	}
	if start != procstarttimefix.LinuxProcStarttime {
		t.Errorf("starttime = %q; want %q", start, procstarttimefix.LinuxProcStarttime)
	}
}

// TestLinuxResolverRealProcHappyPath is the ONE real-/proc happy-path test. The
// test process sets EnvKey via t.Setenv, then Resolve walks from the current
// process. Because Resolve returns the TOPMOST matching ancestor, we assert the
// returned pid is reachable by climbing real ppids from os.Getpid() and that it
// carries the var; when the match is the test process itself we further assert
// the starttime equals /proc/self/stat field 22 read directly. This is robust
// under --pid=host: foreign-uid ancestors with unreadable environ are tolerated
// by the walker and never break the walk.
func TestLinuxResolverRealProcHappyPath(t *testing.T) {
	if _, err := os.Stat("/proc"); err != nil {
		t.Skip("/proc not mounted; skipping")
	}

	const id = "real-proc-resolve-id-a1b2c3"
	t.Setenv(EnvKey, id)

	// /proc/self/environ is a snapshot taken at exec and is NOT updated by a
	// post-exec setenv, so the test process cannot reliably match itself via
	// /proc. Instead spawn a child that carries the var in its fresh exec
	// environ snapshot and resolve from the child's perspective by rooting a
	// reader at its pid. A reader rooted at the child is used for the poll.
	childEnvReader := &linuxProcReader{procRoot: "/proc", self: os.Getpid()}

	child := exec.Command("sleep", "30")
	child.Env = append(os.Environ(), EnvKey+"="+id)
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	})
	childPID := child.Process.Pid

	// Wait for the kernel to expose the child's environ.
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, ok, err := childEnvReader.instanceID(childPID)
		if err == nil && ok && got == id {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child environ never exposed %s=%s", EnvKey, id)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Resolve from the child's perspective: it is a leaf whose environ carries
	// the var; its ancestors (the test binary, go test, shell, ...) do NOT, so
	// the child itself is the TOPMOST (and only) match.
	childReader := &linuxProcReader{procRoot: "/proc", self: childPID}
	pid, start, err := walkAncestors(childReader, id)
	if err != nil {
		t.Fatalf("walkAncestors over real /proc: %v", err)
	}
	if pid != childPID {
		t.Fatalf("resolved pid = %d; want child pid %d (topmost & only match)", pid, childPID)
	}

	// Assert the returned starttime equals the child's /proc/<pid>/stat field
	// 22 read directly here — the authoritative verbatim value.
	wantStart := procstat.ReadStarttime(t, childPID)
	if start != wantStart {
		t.Errorf("starttime = %q; want %q (from /proc/%d/stat field 22)", start, wantStart, childPID)
	}
}
