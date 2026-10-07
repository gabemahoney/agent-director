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

// TestInstallShRetry runs retry.sh (b.kym, b.vqr, b.ady, b.hk7, b.7j2, b.2io, b.onv, b.ojn):
// install.sh's --from-release download retries for both release assets end in
// a full install, a wrong --sha256 or --admin-sha256 installs nothing, the
// PATH symlink is agent-director's only, an upgrade's user_version reads wait
// out a brief store lock and ignore ~/.sqliterc, a failed sentinel mv leaves
// no temp file, an install or upgrade under umask 0777 or 0222 succeeds,
// a hooks-on install's new ~/.claude and settings.json get the owner's access
// and the umask's group/other bits, and an install or upgrade reads, migrates
// and verifies the store [store] db_path names, or stops before changing
// anything on a db_path it cannot read, and a hooks-on install merges
// inject_help_hook into [defaults] however its header is spelled, which
// uninstall.sh reverses, and a hooks-on re-install keeps the modes of the
// settings.json and config.toml it merges into and of their .bak copies. On a
// host install.sh refuses, it skips with the script's reason.
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
