//go:build cabi_panic_inject

package main

import "encoding/json"

// triggerInjectedPanicIfRequested is the test-only deliberate-panic seam.
// It panics when the params JSON object carries "_inject_panic": true, allowing
// tests to drive a panic through the full ad_open / ad_close call path — via
// dlopen or direct in-process call — and assert that the recover() site catches
// it and returns an ErrInternal envelope instead of crashing the process.
//
// This file is compiled only when -tags cabi_panic_inject is supplied. The
// production no-op lives in panic_inject_off.go. The two files are mutually
// exclusive: exactly one compiles at a time.
//
// Usage in tests:
//
//	// Build .so with -tags cabi_panic_inject, then call ad_open with:
//	params := []byte(`{"store_path":"/tmp/x.db","_inject_panic":true}`)
//	// Expect returned envelope: {"err_name":"ErrInternal","err_description":"..."}
func triggerInjectedPanicIfRequested(params []byte) {
	var m map[string]any
	if err := json.Unmarshal(params, &m); err != nil {
		return
	}
	if v, ok := m["_inject_panic"].(bool); ok && v {
		panic("cabi: deliberate panic injected for testing")
	}
}
