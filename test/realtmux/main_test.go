// Package realtmux_test proves the production tmux client (internal/tmux)
// against real tmux (3.3a in the sandbox image). It runs only inside the
// sandbox container (sandboxguard) and skips every test when tmux is not on
// PATH. Each test gets a private, short TMUX_TMPDIR with TMUX and TMUX_PANE
// unset, and passes its private socket with -S: no test starts, kills or
// depends on a server at the default socket path, which test/reboot-recovery
// uses concurrently. The shared helpers live in the harness_*_test.go files.
package realtmux_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

// tmuxPath is the absolute path of tmux on PATH, set by TestMain; "" when
// tmux is absent, in which case every test skips.
var tmuxPath string

func TestMain(m *testing.M) {
	sandboxguard.Require()
	if p, err := exec.LookPath("tmux"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			tmuxPath = abs
		}
	}
	if tmuxPath == "" {
		fmt.Fprintln(os.Stderr, "realtmux: tmux is not on PATH; every test skips")
	} else if out, err := exec.Command(tmuxPath, "-V").Output(); err == nil {
		fmt.Fprintf(os.Stderr, "realtmux: %s at %s\n", strings.TrimSpace(string(out)), tmuxPath)
	}
	os.Exit(m.Run())
}
