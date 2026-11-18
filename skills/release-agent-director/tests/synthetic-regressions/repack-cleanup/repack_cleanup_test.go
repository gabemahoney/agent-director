package repackcleanup_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

func TestMain(m *testing.M) {
	sandboxguard.Require()
	os.Exit(m.Run())
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repository go.mod")
		}
		dir = parent
	}
}

// Regression b.mq1: a failed second pack must not clean up caller-owned
// EXTRACT_DIR1/2 inherited before the gate creates its own extraction dirs.
func TestFailedRepackPreservesInheritedExtractDirs(t *testing.T) {
	script := filepath.Join(repoRoot(t), "skills", "release-agent-director", "gates", "pack", "repack-and-verify.sh")
	shells := map[string]string{"bash": "bash"}
	if bash32 := os.Getenv("AD_TEST_BASH32"); bash32 != "" {
		shells["bash32"] = bash32
	}
	for name, shell := range shells {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			worktree := filepath.Join(base, "worktree")
			bin := filepath.Join(base, "bin")
			tmp := filepath.Join(base, "tmp")
			callerDirs := []string{filepath.Join(base, "caller-extract-1"), filepath.Join(base, "caller-extract-2")}
			for _, dir := range append([]string{filepath.Join(worktree, "pkg", "ts-bun-client"), bin, tmp}, callerDirs...) {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path string, data []byte, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(path, data, mode); err != nil {
					t.Fatal(err)
				}
			}
			sentinels := [][]byte{[]byte("caller one\x00\nkeep these bytes\n"), []byte("caller two\xff\nkeep these bytes\n")}
			for i, dir := range callerDirs {
				write(filepath.Join(dir, "sentinel"), sentinels[i], 0o600)
			}
			// The first tarball only needs to exist: the failed second pack
			// must exit before either tarball is extracted.
			first := filepath.Join(base, "first.tgz")
			write(first, []byte("never extracted\n"), 0o600)
			attempt := filepath.Join(base, "pack-attempt")
			write(filepath.Join(bin, "bun"), []byte("#!/bin/sh\n"+
				"[ \"$#\" -eq 4 ] && [ \"$1\" = pm ] && [ \"$2\" = pack ] && [ \"$3\" = --destination ] || exit 2\n"+
				": > \"$PACK_ATTEMPT\"\nexit 1\n"), 0o700)

			cmd := exec.Command(shell, script, "--first", first, "--worktree-root", worktree)
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TMPDIR="+tmp, "PACK_ATTEMPT="+attempt, "EXTRACT_DIR1="+callerDirs[0], "EXTRACT_DIR2="+callerDirs[1])
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
				t.Fatalf("gate exit = %v, want exit 1; stderr: %s", err, stderr.String())
			}
			if _, err := os.Stat(attempt); err != nil {
				t.Fatalf("gate did not reach the bun pack stand-in: %v; stderr: %s", err, stderr.String())
			}
			var diagnostic struct {
				Gate        string `json:"gate"`
				Description string `json:"description"`
			}
			if err := json.Unmarshal(stderr.Bytes(), &diagnostic); err != nil {
				t.Fatalf("invalid gate diagnostic: %v; stderr: %s", err, stderr.String())
			}
			if diagnostic.Gate != "pack.byte-identical-normalized" || diagnostic.Description != "second bun pm pack produced no .tgz file in staging directory" {
				t.Fatalf("unexpected gate diagnostic: %s", stderr.String())
			}
			t.Logf("gate exited 1 with diagnostic: %s", stderr.String())
			for i, dir := range callerDirs {
				if info, err := os.Stat(dir); err != nil || !info.IsDir() {
					t.Errorf("caller-owned EXTRACT_DIR%d directory %s was deleted or replaced: %v", i+1, dir, err)
				}
				got, err := os.ReadFile(filepath.Join(dir, "sentinel"))
				if err != nil || !bytes.Equal(got, sentinels[i]) {
					t.Errorf("caller-owned EXTRACT_DIR%d sentinel in %s changed: got %q, want %q; error: %v", i+1, dir, got, sentinels[i], err)
				}
			}
		})
	}
}
