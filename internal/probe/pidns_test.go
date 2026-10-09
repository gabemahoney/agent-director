package probe

// pidns_test.go — UNTAGGED answer-table test for the build-tag-free
// linuxSelfPIDNamespace (b.kdf), over a fabricated t.TempDir() proc root
// (never the real /proc), so it runs on any OS.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLinuxSelfPIDNamespace: the self/ns/pid link's target is the namespace,
// verbatim and known; no link, a plain file in its place or no proc root is
// unknown, never a guess.
func TestLinuxSelfPIDNamespace(t *testing.T) {
	const ns = "pid:[4026531836]"
	cases := []struct {
		name      string
		build     func(t *testing.T, root string)
		want      string
		wantKnown bool
	}{
		{"link to the namespace", func(t *testing.T, root string) {
			mkdirAll(t, filepath.Join(root, "self", "ns"))
			if err := os.Symlink(ns, filepath.Join(root, "self", "ns", "pid")); err != nil {
				t.Fatalf("symlink: %v", err)
			}
		}, ns, true},
		{"no ns directory", func(t *testing.T, root string) { mkdirAll(t, filepath.Join(root, "self")) }, "", false},
		{"a plain file, not a link", func(t *testing.T, root string) {
			mkdirAll(t, filepath.Join(root, "self", "ns"))
			if err := os.WriteFile(filepath.Join(root, "self", "ns", "pid"), []byte(ns), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
		}, "", false},
		{"no proc root", func(t *testing.T, root string) {
			if err := os.Remove(root); err != nil {
				t.Fatalf("remove: %v", err)
			}
		}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "proc")
			mkdirAll(t, root)
			tc.build(t, root)
			if got, known := linuxSelfPIDNamespace(root); got != tc.want || known != tc.wantKnown {
				t.Errorf("linuxSelfPIDNamespace = %q, %v; want %q, %v", got, known, tc.want, tc.wantKnown)
			}
		})
	}
}

// mkdirAll creates dir and its parents.
func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}
