package store

// Export-for-test shim: hands Store.hookBeforeUpdateForTest (b.3ie) to the
// external settle_writes_test.go. It sets the seam only.

// SetHookBeforeUpdateForTest makes s's ApplyHookTransitionResult call f after
// its Go-side read of the prior state and before its UPDATE; nil clears it. A
// hook f writes through s must clear it first, or f runs again on that call.
func SetHookBeforeUpdateForTest(s *Store, f func()) {
	s.hookBeforeUpdateForTest = f
}
