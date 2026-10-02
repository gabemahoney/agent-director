package faketmuxfix

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

// The fake is built once per test binary.
var (
	buildOnce sync.Once
	buildPath string
	buildErr  error
)

// Binary compiles test/fake-tmux once per test binary and returns the path
// of the result, a file named "tmux" (so its directory can go first on PATH;
// see Dir). Pass the path to tmux.New or api.Options.TmuxCommand. The build
// runs `go build`, so call it only in the sandbox, as every test runs.
// cmd/agent-director's buildFakeTmux wraps Dir; test/envelope-diff's own
// builder stays. New tests use this one.
func Binary(t TB) string {
	t.Helper()
	buildOnce.Do(func() { buildPath, buildErr = build() })
	if buildErr != nil {
		t.Fatalf("faketmuxfix: build test/fake-tmux: %v", buildErr)
	}
	return buildPath
}

// Dir returns the directory holding Binary's "tmux", for a PATH prepend.
func Dir(t TB) string {
	t.Helper()
	return filepath.Dir(Binary(t))
}

// build compiles ./test/fake-tmux from the repository root into a fresh
// temporary directory (left for the OS to clean, like the existing
// builders: the binary must outlive any one test).
func build() (string, error) {
	root, err := repoRoot()
	if err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp("", "fake-tmux-")
	if err != nil {
		return "", err
	}
	out := filepath.Join(tmp, "tmux")
	cmd := exec.Command("go", "build", "-o", out, "./test/fake-tmux")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if msg, err := cmd.CombinedOutput(); err != nil {
		return "", errors.New(err.Error() + ": " + string(msg))
	}
	return out, nil
}

// repoRoot finds the module root: from this source file's location when the
// build kept it, else by walking up from the working directory to go.mod.
func repoRoot() (string, error) {
	if _, file, _, ok := runtime.Caller(0); ok && filepath.IsAbs(file) {
		root := filepath.Join(filepath.Dir(file), "..", "..", "..")
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return root, nil
		}
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod above the working directory")
		}
		dir = parent
	}
}
