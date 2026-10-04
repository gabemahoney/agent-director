package installsh_test

import (
	"errors"
	"os"
	"os/exec"
	"regexp"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

// retrySkipLine matches retry.sh's host-skip line; the group is the reason.
var retrySkipLine = regexp.MustCompile(`(?m)^retry\.sh: SKIP: (.+)$`)

// TestInstallShRetry runs retry.sh (b.kym, b.vqr, b.ady): install.sh's
// --from-release download retries for both release assets end in a full
// install, a wrong --sha256 or --admin-sha256 installs nothing, the PATH
// symlink is agent-director's only, and an upgrade's user_version reads wait
// out a brief store lock. On a host install.sh refuses, it skips with the
// script's reason.
func TestInstallShRetry(t *testing.T) {
	if os.Getenv(sandboxguard.EnvVar) != "1" {
		t.Skipf("retry.sh runs only in the sandbox (%s=1)", sandboxguard.EnvVar)
	}
	cli2TrackInputs(t)

	out, err := exec.Command("bash", "retry.sh").CombinedOutput()

	var exitErr *exec.ExitError
	if skip := retrySkipLine.FindSubmatch(out); skip != nil && errors.As(err, &exitErr) && exitErr.ExitCode() == cli2SkipRC {
		t.Skip(string(skip[1]))
	}
	if err != nil {
		t.Fatalf("bash retry.sh: %v\n%s", err, out)
	}
	t.Log(string(out))
}
