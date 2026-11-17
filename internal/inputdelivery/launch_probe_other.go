//go:build !linux && !darwin

package inputdelivery

import (
	"fmt"
	"os/exec"
)

// RunProbe refuses: isolating a probe's process group is unsupported here.
// Use RunProbe for helper process-group cleanup, including this refusal.
func RunProbe(*exec.Cmd) (runErr, cleanupErr error) {
	return fmt.Errorf("isolated probing unsupported on this platform"), nil
}
