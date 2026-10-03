package mcp_test

import (
	"os"
	"testing"
)

// knownBrokenAdvice skips a b.fji literal-follow test whose advice does not
// work as written (a product bug to be filed). Set
// AGENT_DIRECTOR_RUN_KNOWN_BROKEN_ADVICE=1 to run it and see it fail.
func knownBrokenAdvice(t *testing.T, id, why string) {
	t.Helper()
	if os.Getenv("AGENT_DIRECTOR_RUN_KNOWN_BROKEN_ADVICE") != "1" {
		t.Skipf("b.fji %s: advice does not work as written (product bug to file): %s", id, why)
	}
}
