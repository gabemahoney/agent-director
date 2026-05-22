// Package cabi is the C-ABI shim for agent-director. It exposes the pkg/api
// verb surface as C-callable functions so foreign callers (Bun today, Python
// and others later) can drive agent-director sessions from any language that
// can load a shared library via dlopen / LoadLibrary.
//
// # Symbol-prefix convention
//
// Every exported C function name carries the "ad_" prefix uniformly. No
// exception. The full set of exported symbols is:
//
//   - ad_open, ad_close          — lifecycle (session constructor / destructor)
//   - ad_<verb>                  — one per callable entry in manifest.CallableVerbs()
//   - ad_free_cstring            — memory management (release a returned C string)
//
// Symbols without the "ad_" prefix are a build error; the doc-drift CI gate
// enforces this invariant.
//
// # JSON wire shape (SRD SR-2.2)
//
// All exported functions accept a single UTF-8 JSON string and return a
// heap-allocated UTF-8 JSON string. The caller is responsible for releasing
// the returned string via ad_free_cstring (see Lifecycle below).
//
// Success envelope — structurally equivalent to the CLI's stdout envelope for
// the same verb. Same fields, same types, same values for deterministic data.
// For ad_open the success envelope carries a single "handle" field:
//
//	{"handle": "<opaque-token>"}
//
// Documented-error envelope — two fields, both strings. The err_name is drawn
// from pkg/api/errnames.Catalog; the err_description is the corresponding
// human-readable message:
//
//	{"err_name": "ErrSpawnNotFound", "err_description": "..."}
//
// Undocumented-error envelope — used when an unexpected error (panic, I/O
// failure, logic bug) would otherwise escape the C boundary. The unsanitized
// error chain is emitted to the cabi debug logger before the sanitized string
// is returned:
//
//	{"err_name": "ErrInternal", "err_description": "<sanitized message>"}
//
// No stack trace and no internal path is ever written to err_description.
//
// # Panic-recovery contract
//
// Every exported function wraps its entire body in a recover() site. A panic
// anywhere in the call — including inside pkg/api — is caught, its detail is
// logged to the cabi debug logger, and an ErrInternal envelope is returned.
// No panic ever crosses the C boundary.
//
// # Lifecycle of returned C strings
//
// Strings returned by ad_open, ad_close, ad_free_cstring, and every ad_<verb>
// function are allocated on the C heap via C.CString. Foreign callers MUST
// release each returned pointer exactly once by passing it to ad_free_cstring.
// Passing a NULL pointer to ad_free_cstring is safe (no-op). Double-free is a
// bug in the caller.
//
// # One-directional import rule
//
// pkg/cabi imports pkg/api and pkg/api/errnames (and stdlib and the C pseudo-
// package). No other package in the module imports pkg/cabi. The dependency
// arrow points strictly into the package. Prohibited imports:
//
//   - internal/*  (not part of the public API surface)
//   - cmd/*       (the CLI is a consumer, not a dependency)
//   - sibling pkg/* other than pkg/api and pkg/api/errnames
//
// # SR-1.2 Logger carve-out
//
// pkg/api.Options.Logger accepts a *log.Logger for in-process callers, but
// that option has no JSON-encodable representation. It is therefore NOT
// accepted in the ad_open params JSON. Foreign callers cannot inject a custom
// logger over the C boundary. pkg/cabi uses its own debug logger driven by the
// AGENT_DIRECTOR_DEBUG environment variable. Wrapper authors in Bun, Python,
// and other languages MUST NOT expose a "logger" option on their Client
// constructors.
//
// # What is NOT here (yet)
//
// Actual CGO annotations and C export bodies are added in T2 and T3. This
// package skeleton establishes the package shell, documentation contract, and
// the internal handle registry only.
package cabi
