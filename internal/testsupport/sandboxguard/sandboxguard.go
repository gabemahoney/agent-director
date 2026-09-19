// Package sandboxguard is a test-support helper that refuses to let a test
// package run outside the sandbox container.
//
// Packages whose tests write agent-director state (open the store, emit trail
// events) or exec built binaries can rewrite the developer's real
// ~/.agent-director when run on the host — the store resolves the home via
// user.Current() (/etc/passwd), so a $HOME redirect does not stop it (b.8dr).
// The `make test-sandbox` / `make sandbox*` targets run those tests inside a
// container whose HOME has no .agent-director and set
// AGENT_DIRECTOR_TEST_SANDBOX=1. Require() checks that marker and aborts with a
// one-line message if it is absent, so a stray host-side `go test` fails fast
// instead of touching production state.
//
// This is an accident-prevention gate, not a security boundary: the marker is a
// plain env var. It is nothing in the production code graph — only test
// packages import it, from their TestMain.
package sandboxguard

import (
	"fmt"
	"os"
)

// EnvVar is the marker the sandbox make targets export into the container.
const EnvVar = "AGENT_DIRECTOR_TEST_SANDBOX"

// Require aborts the process with a non-zero exit and a one-line message unless
// the sandbox marker is set. Call it at the top of TestMain in any package
// whose tests touch agent-director state or exec built binaries:
//
//	func TestMain(m *testing.M) {
//	    sandboxguard.Require()
//	    // …existing setup…
//	    os.Exit(m.Run())
//	}
func Require() {
	if os.Getenv(EnvVar) == "" {
		fmt.Fprintln(os.Stderr,
			"refusing to run: this test package must run via `make test-sandbox` (see the run-tests skill) — it can rewrite the real ~/.agent-director otherwise")
		os.Exit(1)
	}
}
