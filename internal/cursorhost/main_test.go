package cursorhost

import (
	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
	"os"
	"testing"
)

func TestMain(m *testing.M) { sandboxguard.Require(); os.Exit(m.Run()) }
