package qualification

import (
	"os"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

func TestMain(m *testing.M) { sandboxguard.Require(); os.Exit(m.Run()) }
