package busysend_test

import (
	"os"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

// TestMain refuses to run outside the sandbox and redirects HOME to a temp
// dir before any test runs, as store, hook and pkg/api do. The store calls
// here emit trail events, and the trail singleton fixes its file under HOME
// on first use, so a per-test t.Setenv would come too late; without this the
// events land in the container's real ~/.config/agent-director, which
// cmd/agent-director's TestHookWithoutHOMEOpensNoStore then finds when it
// runs later in the same container.
func TestMain(m *testing.M) {
	sandboxguard.Require()
	home, err := os.MkdirTemp("", "ad-busysend-home-*")
	if err != nil {
		panic("TestMain: MkdirTemp: " + err.Error())
	}
	if err := os.Setenv("HOME", home); err != nil {
		panic("TestMain: Setenv HOME: " + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
