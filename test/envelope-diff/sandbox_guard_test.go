package envelope_diff

import (
	"os"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

// TestMain refuses to run this package's tests outside the sandbox container.
// The envelope-diff tests build and exec the agent-director binary, which can
// open (and, on a schema-bumping branch, migrate) the real ~/.agent-director on
// the host (b.8dr). See sandboxguard for the rationale; run via
// `make test-sandbox`.
func TestMain(m *testing.M) {
	sandboxguard.Require()
	os.Exit(m.Run())
}
