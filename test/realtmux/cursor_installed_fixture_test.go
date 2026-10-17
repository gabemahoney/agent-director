package realtmux_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Install the built public tarball without a registry or dependency download.
func installedCursorClient(t *testing.T) string {
	t.Helper()
	source, err := filepath.Abs("../../pkg/ts-bun-client")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.js", "index.d.ts"} {
		if _, err := os.Stat(filepath.Join(source, "dist", name)); err != nil {
			t.Fatalf("built package missing %s: run make sandbox CMD='cd pkg/ts-bun-client && bun run build' before this focused test: %v", name, err)
		}
	}
	dir := t.TempDir()
	cmd := exec.Command("bun", "pm", "pack", "--destination", dir)
	cmd.Dir = source
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pack installed client: %v %s", err, out)
	}
	archives, err := filepath.Glob(filepath.Join(dir, "*.tgz"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("tarballs=%v err=%v", archives, err)
	}
	target := filepath.Join(dir, "node_modules", "agent-director")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("tar", "-xzf", archives[0], "--strip-components=1", "-C", target).CombinedOutput(); err != nil {
		t.Fatalf("install tarball: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(target, "dist", "index.d.ts")); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(target, "dist", "index.js")
}
