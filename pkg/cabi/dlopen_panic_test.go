//go:build cabi_dlopen && cabi_panic_inject

package main

// dlopen_panic_test.go verifies that a deliberate panic, injected into the
// .so via the _inject_panic seam, is recovered by the runVerb() recover() site
// and returned as an ErrInternal envelope — without crashing the process.
//
// The .so is compiled WITH -tags cabi_panic_inject because
// buildtags_panic_inject.go's init() appended "cabi_panic_inject" to
// dlopenBuildTags before TestMain ran, which the tag-forwarding logic in
// dlopen_test.go passed to `go build -buildmode=c-shared` (with cabi_dlopen
// excluded so dlopen_helper.go stays out of the .so).
//
// Run with:
//
//	go test -tags 'cabi_dlopen cabi_panic_inject' -count=1 ./pkg/cabi/...

import "testing"

// TestDlopenPanicInjection exercises the cabi_panic_inject seam through the
// dlopened .so:
//
//  1. Calls ad_version with {"_inject_panic":true} — handle-free, so
//     triggerInjectedPanicIfRequested fires inside runVerb before any verb
//     logic runs. The recover() site must catch the panic and return ErrInternal.
//  2. Calls ad_version with {} to confirm the process is still alive and the
//     .so is still functional after the recovered panic.
func TestDlopenPanicInjection(t *testing.T) {
	// Step 1: inject panic → expect ErrInternal envelope.
	panicResult := dlopenInvoke(t, "ad_version", []byte(`{"_inject_panic":true}`))
	assertDlopenErrName(t, panicResult, "ErrInternal")

	// Step 2: valid call → expect success; process is still alive.
	normalResult := dlopenInvoke(t, "ad_version", []byte(`{}`))
	assertDlopenSuccess(t, normalResult)
}
