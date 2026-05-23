/**
 * bootstrapFfi — thin synchronous dlopen wrapper used by the Client
 * constructor and close() BEFORE the T3 worker-proxy is wired.
 *
 * TODO (T3): Replace direct dlopen here with worker-proxy dispatch so
 * that verb calls run off the main thread. The "open" and "close" ops
 * can keep their synchronous, main-thread semantics (they are brief and
 * handle-less / handle-acquisition ops respectively); T3's dispatcher
 * will provide a compatible callSync("open", ...) / callSync("close", ...)
 * API that this file can delegate to once it lands.
 *
 * Internal — NOT re-exported from src/index.ts.
 */

import { dlopen, FFIType, suffix, CString, ptr, type Pointer } from "bun:ffi";
import * as path from "node:path";

// ---------------------------------------------------------------------------
// Library path resolution
// ---------------------------------------------------------------------------
// Hard-coded relative path: dist/libagent_director.{so|dylib} at repo root.
// T5 (platform resolver) will expose a proper resolver once it ships; until
// then this hard-code is expected and intentional per the T2 scope comment.
const _soPath = path.resolve(
  // import.meta.dir = .../pkg/ts-bun-client/src/internal
  import.meta.dir,
  "../../../../dist/libagent_director." + suffix
);

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
