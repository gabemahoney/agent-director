// Package fixture is a synthetic test fixture for TestScanHandlerSentinels.
// This file is NOT a _test.go file so the scanner includes it when walking
// the testdata directory; _test.go files are excluded by the scanner.
package fixture

import "fmt"

// bareIdent wraps a bare-identifier sentinel via fmt.Errorf %w — should be
// collected as "ErrHandlerSentinelA".
func bareIdent() error {
	return fmt.Errorf("%w: spawn is not interactive", ErrHandlerSentinelA)
}

// selectorExpr wraps a selector-expression sentinel via fmt.Errorf %w — the
// terminal name "ErrHandlerSentinelB" should be collected (package prefix dropped).
func selectorExpr() error {
	return fmt.Errorf("%w: relay mode off for spawn %s", pkg.ErrHandlerSentinelB, "id")
}

// nonErrIdent wraps a non-Err-prefixed identifier — should be ignored because
// the second argument's name does not start with "Err".
func nonErrIdent() error {
	return fmt.Errorf("%w: some wrapping", someOtherVar)
}

// noPercentW uses a format string without the %w verb — should be ignored
// because the format does not start with "%w".
func noPercentW() error {
	return fmt.Errorf("no verb: %v", anErr)
}
