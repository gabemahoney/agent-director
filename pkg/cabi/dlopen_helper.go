//go:build cabi_dlopen

package main

// dlopen_helper.go provides cgo-backed dlopen/dlsym/dlclose wrappers used
// exclusively by the dlopen wire-test suite (dlopen_test.go and friends).
// This is a NON-test file so that import "C" is legal alongside the //export
// directives in lifecycle.go: Go prohibits import "C" in _test.go files of
// packages that contain //export, but not in regular non-test files.
//
// The //go:build cabi_dlopen tag isolates this code from the production build
// path:
//   - `go build ./cmd/agent-director` (CGO_ENABLED=0, no tag): excluded.
//   - `make libagent_director` (CGO_ENABLED=1, no tag): excluded.
//   - `go test -tags cabi_dlopen ./pkg/cabi/...`: included in the test binary.
//
// All functions use unsafe.Pointer (not uintptr) for dlopen handles and dlsym
// symbols. This avoids the uintptr→unsafe.Pointer conversions that go vet flags
// as "possible misuse of unsafe.Pointer" — the handles and symbols are C-heap
// allocations, not GC-managed, so no GC movement occurs, but keeping them as
// unsafe.Pointer throughout is the idiomatic way to express this in Go.

// #cgo LDFLAGS: -ldl
// #include <dlfcn.h>
// #include <stdlib.h>
//
// // call_ad_func invokes a C-ABI function of type char*(*)(const char*)
// // via a void* function pointer obtained from dlsym. All ad_<verb> exports
// // share this signature.
// static char* call_ad_func(void *fn, const char *params) {
//     typedef char* (*ad_fn_t)(const char*);
//     return ((ad_fn_t)fn)(params);
// }
//
// // call_free_cstring invokes ad_free_cstring — void(*)(char*) — via its
// // void* function pointer. Used to release C strings returned by ad_* calls.
// static void call_free_cstring(void *fn, char *ptr) {
//     typedef void (*free_fn_t)(char*);
//     ((free_fn_t)fn)(ptr);
// }
import "C"

import (
	"fmt"
	"unsafe"
)

// soOpen opens the shared library at path with RTLD_NOW|RTLD_LOCAL.
// Returns the dlopen handle as unsafe.Pointer. The caller must call soClose.
func soOpen(path string) (unsafe.Pointer, error) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))

	C.dlerror() // clear any stale error
	h := C.dlopen(cpath, C.RTLD_NOW|C.RTLD_LOCAL)
	if h == nil {
		errmsg := C.dlerror()
		if errmsg == nil {
			return nil, fmt.Errorf("dlopen(%q): unknown error", path)
		}
		return nil, fmt.Errorf("dlopen(%q): %s", path, C.GoString(errmsg))
	}
	return h, nil
}

// soClose releases the library handle returned by soOpen.
func soClose(handle unsafe.Pointer) {
	C.dlclose(handle)
}

// soSym resolves a symbol by name from the library opened with soOpen.
// Returns (nil, error) if the symbol is not found.
func soSym(handle unsafe.Pointer, name string) (unsafe.Pointer, error) {
	C.dlerror() // clear any stale error
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))

	sym := C.dlsym(handle, cname)
	if errmsg := C.dlerror(); errmsg != nil {
		return nil, fmt.Errorf("dlsym(%q): %s", name, C.GoString(errmsg))
	}
	return sym, nil
}

// soCallAdFunc calls the C-ABI function at funcPtr with paramsJSON, copies
// the returned C string into a Go []byte, and then frees it via freePtr
// (which must be the symbol for ad_free_cstring). Returns the raw JSON bytes.
//
// Every ad_<verb> export has the signature char*(*)(const char*); they all go
// through this function.
func soCallAdFunc(funcPtr, freePtr unsafe.Pointer, paramsJSON []byte) []byte {
	cparams := C.CString(string(paramsJSON))
	defer C.free(unsafe.Pointer(cparams))

	result := C.call_ad_func(funcPtr, cparams)
	if result == nil {
		// Nil return should never happen for well-formed ad_* functions; be
		// defensive rather than crashing.
		return []byte("{}")
	}

	// Copy to Go memory before freeing the C-heap string.
	goResult := []byte(C.GoString(result))

	// Release via ad_free_cstring (the .so's allocator owns the memory).
	if freePtr != nil {
		C.call_free_cstring(freePtr, result)
	}

	return goResult
}
