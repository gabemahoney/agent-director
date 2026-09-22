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
// short message if it is absent, so a stray host-side `go test` fails fast
// instead of touching production state.
//
// There is one other way through: BypassEnvVar
// (BYPASS_CONTAINER_FOR_AGENT_DIRECTOR_TESTS). It exists solely for CI runners
// that have no real store to protect — an ephemeral GitHub-hosted runner has no
// ~/.agent-director, so containerizing the run buys nothing. It is NOT a way to
// run these tests on a development host, where a real store exists; use
// `make test-sandbox` there. Per b.175 the bypass must be set only at the
// job/step level of the GitHub-hosted workflows (go-smoke.yml,
// integration.yml), never as a repository/organization-level Actions variable
// and never in pre-release-verify-mac.yml, whose self-hosted macOS runner is a
// persistent machine that plausibly holds a real store.
//
// This is an accident-prevention gate, not a security boundary: both variables
// are plain env vars. It is nothing in the production code graph — only test
// packages import it, from their TestMain.
package sandboxguard

import (
	"fmt"
	"os"
)

// EnvVar is the marker the sandbox make targets export into the container.
const EnvVar = "AGENT_DIRECTOR_TEST_SANDBOX"

// BypassEnvVar is the explicit opt-out for CI runners that have no real
// agent-director store to protect (b.175). Setting it asserts "there is
// nothing here to lose" — true on an ephemeral GitHub-hosted runner, false on
// a development host or the self-hosted macOS runner.
const BypassEnvVar = "BYPASS_CONTAINER_FOR_AGENT_DIRECTOR_TESTS"

// Require aborts the process with a non-zero exit and a short message unless
// the sandbox marker (EnvVar) or the CI bypass (BypassEnvVar) is set. Absence
// of both means refuse — the gate is fail-closed. Call it at the top of
// TestMain in any package whose tests touch agent-director state or exec built
// binaries:
//
//	func TestMain(m *testing.M) {
//	    sandboxguard.Require()
//	    // …existing setup…
//	    os.Exit(m.Run())
//	}
func Require() {
	if os.Getenv(EnvVar) != "" || os.Getenv(BypassEnvVar) != "" {
		return
	}
	fmt.Fprintln(os.Stderr,
		"refusing to run: neither "+EnvVar+" nor "+BypassEnvVar+" is set, so this process is running OUTSIDE the sandbox container (on the host), where it can rewrite the real ~/.agent-director.")
	fmt.Fprintln(os.Stderr,
		"On a development host, run it via `make test-sandbox` (see the run-tests skill), which sets "+EnvVar+". A broken-quoting `make sandbox CMD=\"…\"` escape is one way to end up on the host here.")
	fmt.Fprintln(os.Stderr,
		"Do NOT set "+BypassEnvVar+" here: it is only for CI runners that have no real ~/.agent-director store to damage (ephemeral GitHub-hosted runners), not a way to run these tests on a host that has one.")
	os.Exit(1)
}
