package envelope_diff

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// ── repo root resolution ─────────────────────────────────────────────────────

// findRepoRoot walks up from the process working directory until it finds the
// directory containing go.mod that declares module
// github.com/gabemahoney/agent-director. Because go test sets the working
// directory to the package under test, callers running from
// test/envelope-diff/ reach the repo root after two hops.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("findRepoRoot: getwd: %w", err)
	}
	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && bytes.Contains(data, []byte("module github.com/gabemahoney/agent-director")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("findRepoRoot: repo root not found (walked up from %s)", dir)
		}
		dir = parent
	}
}

// ── fixture-store copy ───────────────────────────────────────────────────────

// copyFixtureStore copies the fixture store directory srcDir into a fresh
// test-owned directory and returns:
//
//   - homeDir: the directory to use as HOME for CLI subprocess tests; the
//     CLI resolves its store at homeDir/.agent-director/state.db.
//   - dbPath: absolute path to the copied state.db file (homeDir/.agent-director/state.db).
//
// srcDir is expected to be a directory whose contents will be copied verbatim
// into homeDir/.agent-director/. A minimal srcDir contains a single state.db
// file; additional fixture files (e.g. WAL shards) are also copied.
//
// The copy is isolated per test: mutations to the copy never affect srcDir.
// homeDir is cleaned up automatically by t.Cleanup.
func copyFixtureStore(t *testing.T, srcDir string) (homeDir, dbPath string) {
	t.Helper()
	homeDir = t.TempDir()
	adDir := filepath.Join(homeDir, ".agent-director")
	if err := os.MkdirAll(adDir, 0o700); err != nil {
		t.Fatalf("copyFixtureStore: mkdir .agent-director: %v", err)
	}
	if err := copyDir(adDir, srcDir); err != nil {
		t.Fatalf("copyFixtureStore: copy %s → %s: %v", srcDir, adDir, err)
	}
	dbPath = filepath.Join(adDir, "state.db")
	return homeDir, dbPath
}

// copyDir copies all regular files (and nested directories) from src into dst.
// dst must already exist.
func copyDir(dst, src string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		return copyFile(target, path)
	})
}

// copyFile copies the regular file at src to dst (creating dst, overwriting if
// present). The destination file is given mode 0o600.
func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// ── CLI binary builder ───────────────────────────────────────────────────────

var (
	cliOnce          sync.Once
	cliBinPath       string
	cliBinErr        error
	cliOnceBuildCount int // incremented exactly once inside cliOnce.Do; observable by same-package tests
)

// buildCLI compiles ./cmd/agent-director into a temporary binary and returns
// its path. The build is performed exactly once per go test invocation
// regardless of sub-test count; subsequent calls return the cached result.
//
// Environment override: when AGENT_DIRECTOR_TEST_BINARY is set, buildCLI
// returns that path without invoking go build. This allows CI to build the
// binary once and reuse it across runs.
func buildCLI(t *testing.T) string {
	t.Helper()

	if env := os.Getenv("AGENT_DIRECTOR_TEST_BINARY"); env != "" {
		return env
	}

	cliOnce.Do(func() {
		root, err := findRepoRoot()
		if err != nil {
			cliBinErr = fmt.Errorf("buildCLI: %w", err)
			return
		}
		tmp, err := os.MkdirTemp("", "envelope-diff-cli-*")
		if err != nil {
			cliBinErr = fmt.Errorf("buildCLI: mkdtemp: %w", err)
			return
		}
		out := filepath.Join(tmp, "agent-director")
		cmd := exec.Command("go", "build", "-o", out, "./cmd/agent-director")
		cmd.Dir = root
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			cliBinErr = fmt.Errorf("buildCLI: go build: %w", err)
			return
		}
		cliBinPath = out
		cliOnceBuildCount++
	})

	if cliBinErr != nil {
		t.Fatalf("buildCLI: %v", cliBinErr)
	}
	return cliBinPath
}

// ── fake-tmux binary builder ─────────────────────────────────────────────────

var (
	fakeTmuxOnce   sync.Once
	fakeTmuxBinDir string
	fakeTmuxBinErr error
)

// buildFakeTmux compiles ./test/fake-tmux into a temporary directory and
// returns the directory path. The resulting binary is literally named "tmux"
// so that PATH-prepend resolution finds it ahead of any system tmux.
//
// The build is performed exactly once per go test invocation; subsequent calls
// return the cached directory.
//
// Environment override: when AGENT_DIRECTOR_FAKE_TMUX_DIR is set, buildFakeTmux
// returns that directory without invoking go build, enabling CI to build once
// and reuse. The directory must contain a binary named "tmux".
func buildFakeTmux(t *testing.T) string {
	t.Helper()

	if env := os.Getenv("AGENT_DIRECTOR_FAKE_TMUX_DIR"); env != "" {
		return env
	}

	fakeTmuxOnce.Do(func() {
		root, err := findRepoRoot()
		if err != nil {
			fakeTmuxBinErr = fmt.Errorf("buildFakeTmux: %w", err)
			return
		}
		tmp, err := os.MkdirTemp("", "envelope-diff-tmux-*")
		if err != nil {
			fakeTmuxBinErr = fmt.Errorf("buildFakeTmux: mkdtemp: %w", err)
			return
		}
		out := filepath.Join(tmp, "tmux") // named "tmux" for PATH resolution
		cmd := exec.Command("go", "build", "-o", out, "./test/fake-tmux")
		cmd.Dir = root
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fakeTmuxBinErr = fmt.Errorf("buildFakeTmux: go build: %w", err)
			return
		}
		fakeTmuxBinDir = tmp
	})

	if fakeTmuxBinErr != nil {
		t.Fatalf("buildFakeTmux: %v", fakeTmuxBinErr)
	}
	return fakeTmuxBinDir
}
