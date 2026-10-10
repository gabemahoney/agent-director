// Package installsh_test runs install.sh's b.fji literal-follow tests
// (advice_follow.sh, advice inventory J1-J21) under go test, so make
// test-sandbox runs them and a change to install.sh's advice turns it red.
package installsh_test

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

func TestMain(m *testing.M) {
	sandboxguard.Require()
	os.Exit(m.Run())
}

// cli2Inputs are what advice_follow.sh runs or builds from, relative to the
// repo root (it copies pkg/ts-bun-client's package.json only).
var cli2Inputs = []string{"test/install-sh", "skills/install-agent-director", "go.mod", "go.sum", "Makefile",
	"cmd", "internal", "pkg", "pkg/ts-bun-client/package.json"}

// cli2Result matches the script's closing line for one test.
var cli2Result = regexp.MustCompile(`^--- (PASS|FAIL|SKIP): (test_\w+)$`)

// cli2SkipRC is the script's exit status when install.sh refuses the host
// (Linux/aarch64, for one): it ran no test and printed one cli2SkipLine.
const cli2SkipRC = 77

// cli2SkipLine matches that line; the group is the reason.
var cli2SkipLine = regexp.MustCompile(`(?m)^advice_follow\.sh: SKIP: (.+)$`)

// cli2TrackInputs walks cli2Inputs. go test caches a pass keyed only on the
// files the test process reads, so this makes a change to any of them rerun it.
func cli2TrackInputs(t *testing.T) {
	t.Helper()
	root := filepath.Join("..", "..")
	skip := filepath.Join(root, "pkg", "ts-bun-client")
	for _, in := range cli2Inputs {
		err := filepath.WalkDir(filepath.Join(root, in), func(path string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() && path == skip {
				return fs.SkipDir
			}
			return nil // an entry that vanishes mid-walk only changes what is tracked
		})
		if err != nil {
			t.Fatalf("walk %s: %v", in, err)
		}
	}
}

// TestAdviceFollow_J_InstallShScript: J1-J21, each test_* in advice_follow.sh a
// subtest with its own result; known-broken ones skip unless the gate is set.
// On a host install.sh refuses, the whole test skips with the script's reason.
func TestAdviceFollow_J_InstallShScript(t *testing.T) {
	if os.Getenv(sandboxguard.EnvVar) != "1" {
		t.Skipf("advice_follow.sh runs only in the sandbox (%s=1)", sandboxguard.EnvVar)
	}
	// Read here so a cached pass is keyed on the script's gate too.
	t.Logf("AGENT_DIRECTOR_RUN_KNOWN_BROKEN_ADVICE=%q", os.Getenv("AGENT_DIRECTOR_RUN_KNOWN_BROKEN_ADVICE"))
	cli2TrackInputs(t)

	out, err := exec.Command("bash", "advice_follow.sh").CombinedOutput()

	var exitErr *exec.ExitError
	skipRC := errors.As(err, &exitErr) && exitErr.ExitCode() == cli2SkipRC
	if skip := cli2SkipLine.FindSubmatch(out); skip != nil || skipRC {
		if skip == nil || !skipRC {
			t.Fatalf("bash advice_follow.sh: %v: a host skip needs both exit status %d and an \"advice_follow.sh: SKIP:\" line\n%s",
				err, cli2SkipRC, out)
		}
		t.Log(strings.TrimSpace(string(out)))
		t.Skip(string(skip[1]))
	}

	var body []string
	ran, failed := 0, 0
	for _, line := range strings.Split(string(out), "\n") {
		m := cli2Result.FindStringSubmatch(line)
		if m == nil {
			if strings.HasPrefix(line, "=== RUN") {
				body = nil
			} else {
				body = append(body, line)
			}
			continue
		}
		ran++
		if m[1] == "FAIL" {
			failed++
		}
		result, text := m[1], strings.Join(body, "\n")
		t.Run(m[2], func(t *testing.T) {
			switch result {
			case "FAIL":
				t.Error(text)
			case "SKIP":
				t.Skip(strings.TrimSpace(text))
			}
		})
	}
	switch {
	case ran == 0 || (err != nil && failed == 0):
		t.Fatalf("bash advice_follow.sh: %v (%d tests reported)\n%s", err, ran, out)
	case err != nil:
		t.Fatalf("bash advice_follow.sh: %v (%d of %d tests failed, above)", err, failed, ran)
	}
}
