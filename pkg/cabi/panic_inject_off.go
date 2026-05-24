//go:build !cabi_panic_inject

package main

// triggerInjectedPanicIfRequested is the production-build no-op seam. When
// the cabi_panic_inject build tag is absent this function compiles to an empty
// body and is eliminated by the compiler. Exported functions call it early in
// their body with the raw params bytes; in production the call vanishes
// entirely.
//
// The deliberate-panic implementation lives in panic_inject_on.go and is
// compiled only when -tags cabi_panic_inject is supplied (test builds only).
func triggerInjectedPanicIfRequested(_ []byte) {}
