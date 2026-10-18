// Package clibuild shares the actual CLI build used by subprocess test fixtures.
package clibuild

import (
	"fmt"
	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
	"os/exec"
)

func Build(path string) error {
	sandboxguard.Require()
	out, err := exec.Command("go", "build", "-o", path, "github.com/gabemahoney/agent-director/cmd/agent-director").CombinedOutput()
	if err != nil {
		return fmt.Errorf("build CLI: %w\n%s", err, out)
	}
	return nil
}
