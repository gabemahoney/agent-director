package main

// #include <stdlib.h>
import "C"

import (
	"encoding/json"
	"unsafe"

	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
)

// openParams is the JSON-decoded input for ad_open. The field set mirrors
// pkg/api.Options with one deliberate omission:
//
// Logger carve-out (SR-1.2): Options.Logger accepts a *log.Logger for
// in-process callers, but that field has no JSON-encodable representation and
// cannot be transmitted over the C boundary. pkg/cabi therefore always
// constructs the Client with Logger: nil, which causes pkg/api.New to
// substitute log.New(io.Discard, …) — a silent logger. Foreign callers (Bun,
// Python, …) MUST NOT expose a "logger" option on their own client
// constructors.
type openParams struct {
	StorePath       string `json:"store_path"`
	ConfigPath      string `json:"config_path"`
	TmuxCommand     string `json:"tmux_command"`
	CreateIfMissing bool   `json:"create_if_missing"`
}

// closeParams is the JSON-decoded input for ad_close.
type closeParams struct {
	Handle string `json:"handle"`
}

//export ad_open
// ad_open constructs a pkg/api.Client from a JSON params object, registers it
// in the handle registry, and returns a success envelope:
//
//	{"handle": "<opaque-token>"}
//
// On documented error a two-field envelope is returned:
//
//	{"err_name": "...", "err_description": "..."}
//
// On an unexpected error or panic an ErrInternal envelope is returned; the
// panic never crosses the C boundary.
//
// The returned *C.char is heap-allocated via C.CString. The caller must free
// it exactly once via ad_free_cstring.
//
// Accepted JSON params: store_path, config_path, tmux_command,
// create_if_missing. The "logger" field is NOT accepted — see Logger carve-out
// in doc.go and the type comment above.
func ad_open(params_json *C.char) *C.char {
	var result []byte

	func() {
		defer func() {
			if r := recover(); r != nil {
				debugLog("ad_open: panic recovered", "panic", r)
				result = errorEnvelope("ErrInternal", "internal error")
			}
		}()

		rawBytes := []byte(C.GoString(params_json))

		// Build-tag-gated panic seam: no-op in production builds; panics
		// when params carries "_inject_panic": true in cabi_panic_inject builds.
		triggerInjectedPanicIfRequested(rawBytes)

		var p openParams
		if err := json.Unmarshal(rawBytes, &p); err != nil {
			result = errorEnvelope("ErrInternal", "ad_open: invalid JSON params")
			return
		}

		// Construct the Client. Logger: nil → pkg/api.New substitutes
		// io.Discard (SR-1.2 Logger carve-out; see type comment above).
		client, err := pkgapi.New(pkgapi.Options{
			StorePath:       p.StorePath,
			ConfigPath:      p.ConfigPath,
			TmuxCommand:     p.TmuxCommand,
			CreateIfMissing: p.CreateIfMissing,
			Logger:          nil,
		})
		if err != nil {
			result = classifyAndEnvelope(err)
			return
		}

		handle := registry.mint(client)
		result = successEnvelope(map[string]string{"handle": handle})
	}()

	// Allocate on the C heap once, outside the closure. The caller must
	// release this pointer via ad_free_cstring.
	return C.CString(string(result))
}

//export ad_close
// ad_close releases the pkg/api.Client registered under the given handle.
// An unknown (or empty) handle is a no-op success — the desired post-condition
// (no active session) is already met.
//
// Input JSON: {"handle": "<opaque-token>"}
//
// Returns an empty success envelope "{}" on success, or an ErrInternal
// envelope if the underlying Client.Close fails. Any panic is recovered and
// returned as ErrInternal; it never crosses the C boundary.
//
// The returned *C.char is heap-allocated via C.CString. The caller must free
// it exactly once via ad_free_cstring.
func ad_close(params_json *C.char) *C.char {
	var result []byte

	func() {
		defer func() {
			if r := recover(); r != nil {
				debugLog("ad_close: panic recovered", "panic", r)
				result = errorEnvelope("ErrInternal", "internal error")
			}
		}()

		rawBytes := []byte(C.GoString(params_json))

		// Build-tag-gated panic seam.
		triggerInjectedPanicIfRequested(rawBytes)

		var p closeParams
		if err := json.Unmarshal(rawBytes, &p); err != nil {
			result = errorEnvelope("ErrInternal", "ad_close: invalid JSON params")
			return
		}

		client, found := registry.delete(p.Handle)
		if !found {
			// Unknown handle is a no-op success per the Epic spec.
			result = successEnvelope(map[string]any{})
			return
		}

		if err := client.Close(); err != nil {
			result = classifyAndEnvelope(err)
			return
		}

		result = successEnvelope(map[string]any{})
	}()

	return C.CString(string(result))
}

//export ad_free_cstring
// ad_free_cstring releases a C string previously returned by ad_open,
// ad_close, or any ad_<verb> function. Passing a NULL pointer is safe (no-op).
// The caller must invoke this exactly once per returned pointer; double-free is
// a bug in the caller.
//
// Unsafe operation: C.free releases memory on the C heap that was allocated by
// C.CString inside this package. The unsafe.Pointer cast is the standard cgo
// idiom for calling free(3) with a typed Go pointer — cgo's C.free signature
// requires void*, which Go represents as unsafe.Pointer.
func ad_free_cstring(s *C.char) {
	if s == nil {
		return
	}
	// unsafe.Pointer is required here: C.free takes void* and cgo represents
	// that as unsafe.Pointer. This is the standard, documented cgo pattern.
	C.free(unsafe.Pointer(s))
}
