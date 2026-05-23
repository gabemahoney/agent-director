/**
 * bootstrapFfi — thin synchronous dlopen wrapper used by the Client
 * constructor and close().
 *
 * Only three symbols are needed here: ad_open, ad_close, ad_free_cstring.
 * The full binding spec (all 18 symbols) is used by worker.ts via loadNative().
 *
 * Library path resolution delegates to platform.ts::resolveNativePath() so
 * that all platforms (linux-x64, darwin-x64, darwin-arm64) resolve their
 * binary from the correct optional sub-package, and so this file shares the
 * same path logic as the worker.
 *
 * Internal — NOT re-exported from src/index.ts.
 */

import { dlopen, FFIType, CString, ptr, type Pointer } from "bun:ffi";
import { resolveNativePath } from "../platform.js";

// ---------------------------------------------------------------------------
// Library path resolution + dlopen
// ---------------------------------------------------------------------------
// resolveNativePath() checks the Bun version and optional sub-package on
// first import of this module (module-level eager init). If the platform
// package is missing or the binary absent it throws before dlopen is reached.
const _soPath = resolveNativePath();

// ---------------------------------------------------------------------------
// FFI symbol declarations
// ---------------------------------------------------------------------------
// ad_open(params_json: *char) → *char   (caller must free via ad_free_cstring)
// ad_close(params_json: *char) → *char  (caller must free via ad_free_cstring)
// ad_free_cstring(s: *char) → void
//
// We declare the return type as FFIType.ptr (→ Pointer | null) so we can
// pass it directly to `new CString(ptr)` and back to `ad_free_cstring(ptr)`.
const _lib = dlopen(_soPath, {
  ad_open: {
    args: [FFIType.cstring],
    returns: FFIType.ptr,
  },
  ad_close: {
    args: [FFIType.cstring],
    returns: FFIType.ptr,
  },
  ad_free_cstring: {
    args: [FFIType.ptr],
    returns: FFIType.void,
  },
} as const);

// ---------------------------------------------------------------------------
// Public helpers
// ---------------------------------------------------------------------------

/**
 * callOpen encodes params as JSON, calls ad_open, copies the returned C
 * string into a JS string, frees the C pointer, and returns the envelope
 * JSON string.
 *
 * Throws if the library call itself fails (i.e. the function pointer is
 * null) — normal Go-level errors are returned as error envelopes, not
 * thrown.
 */
export function callOpen(params: {
  store_path: string;
  config_path?: string;
  tmux_command?: string;
  create_if_missing?: boolean;
}): string {
  const jsonBytes = Buffer.from(JSON.stringify(params) + "\0");
  const rawPtr = _lib.symbols.ad_open(ptr(jsonBytes));
  return _readAndFree(rawPtr);
}

/**
 * callClose encodes params as JSON, calls ad_close, copies the returned C
 * string into a JS string, frees the C pointer, and returns the envelope
 * JSON string.
 */
export function callClose(params: { handle: string }): string {
  const jsonBytes = Buffer.from(JSON.stringify(params) + "\0");
  const rawPtr = _lib.symbols.ad_close(ptr(jsonBytes));
  return _readAndFree(rawPtr);
}

// ---------------------------------------------------------------------------
// Private helpers
// ---------------------------------------------------------------------------

function _readAndFree(rawPtr: Pointer | null): string {
  if (rawPtr === null || rawPtr === 0) {
    throw new Error("bootstrapFfi: ad_* returned a null pointer");
  }
  // rawPtr is Pointer (branded number) — pass directly to CString and free.
  const result = new CString(rawPtr).toString();
  _lib.symbols.ad_free_cstring(rawPtr);
  return result;
}
