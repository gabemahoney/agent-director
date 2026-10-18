//go:build !linux && !darwin

package inputdelivery

import (
	"fmt"
	"os/exec"
)

func runVersionProbe(*exec.Cmd) error {
	return fmt.Errorf("isolated version probing unsupported on this platform")
}
