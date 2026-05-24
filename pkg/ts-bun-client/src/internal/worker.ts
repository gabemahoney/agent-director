/**
 * worker.ts — worker-thread entry-point for all C-ABI FFI calls.
 *
 * This module runs INSIDE a node:worker_threads Worker. It is never imported
 * by the main thread. Its sole job is:
 *
 *  1. At startup: resolve the native library path via loadNative(), post {type:"ready"}.
 *  2. On every message: receive {id, op, handle, paramsJSON}, build the
 *     single-arg JSON for the C symbol, call it, copy the returned C string
 *     to JS, free the native pointer (try/finally so the error path also
 *     frees), and post the raw JSON string back to the main thread.
 *
 * WHY a dedicated worker?
 * ───────────────────────
 * Bun's dlopen has no per-symbol `async: true` marker. Calling a C function
 * directly on the main thread blocks the JS event loop for the duration of
 * that call. By running all FFI in a dedicated worker, verb calls are truly
 * off-main-thread — the event loop remains responsive during long-running
 * Go-side operations.
 *
 * One worker per process (singleton, not a pool): the Go runtime inside the
 * shared library carries process-global state (the handle registry). Multiple
 * parallel dlopen handles would split that state and break audit-trail
 * invariants. A single serialized worker is the correct model.
 *
 * Wire format for C calls
 * ───────────────────────
 * Every C-ABI function takes exactly ONE `char* params_json` argument and
 * returns `char* result_json` (which the caller must release via
 * ad_free_cstring). The handle field is embedded inside the JSON:
 *   - ad_open  → paramsJSON (no handle)
 *   - ad_close → {"handle":"<token>"}
 *   - ad_<verb> → {"handle":"<token>", ...verb-params}
 *   - ad_version → {} (handle-free)
 */

import { parentPort } from "node:worker_threads";
import { CString, ptr, type Pointer } from "bun:ffi";
import { kebabToUnderscore, isHandleFree } from "./verbs.js";
import { FreeGuard } from "./freeGuard.js";
import { loadNative, type NativeLib } from "../platform.js";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface WorkerRequest {
  id: number;
  op: string;           // "open" | "close" | <verb-kebab>
  handle: string | null;
  paramsJSON: string;   // verb params JSON (excluding the handle field)
}

interface WorkerResponse {
  id: number;
  type: "result" | "ffi-error" | "ready" | "startup-error";
  jsonString?: string;
  message?: string;
}

// Type for a single-arg C-ABI function (all ad_* except ad_free_cstring).
type CStringFn = (arg: Pointer) => Pointer;

// ---------------------------------------------------------------------------
// Ensure parentPort is non-null (we are always inside a Worker here)
// ---------------------------------------------------------------------------
if (parentPort === null) {
  throw new Error("worker.ts must run inside a node:worker_threads Worker");
}

const port = parentPort;

// Helper to post a startup-error and exit.
function fatalStartup(message: string): never {
  port.postMessage({ id: -1, type: "startup-error", message } satisfies WorkerResponse);
  process.exit(1);
}

// ---------------------------------------------------------------------------
// Load native library via the platform resolver
// ---------------------------------------------------------------------------

function doLoadNative(): NativeLib {
  try {
    const { lib } = loadNative();
    return lib;
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    fatalStartup(`Failed to load native library: ${msg}`);
  }
}

const lib = doLoadNative();

// Signal readiness to the main thread.
port.postMessage({ id: -1, type: "ready" } satisfies WorkerResponse);

// Convenience: get a symbol from the loaded library as a CStringFn.
function getSymbol(name: string): CStringFn {
  const sym = lib[name];
  if (typeof sym !== "function") {
    throw new Error(`symbol not found in loaded library: ${name}`);
  }
  return sym as unknown as CStringFn;
}

// ---------------------------------------------------------------------------
// Call helper
// ---------------------------------------------------------------------------

/**
 * callSymbol invokes a C-ABI function with a JSON string argument, copies
 * the returned C string to JS, and frees the native pointer inside a
 * try/finally (so the error path also frees). Returns the raw JSON string.
 */
function callSymbol(fn: CStringFn, jsonArg: string): string {
  const inputBuf = Buffer.from(jsonArg + "\0", "utf8");
  const rawPtr = fn(ptr(inputBuf));

  if (rawPtr === null || rawPtr === 0) {
    throw new Error("C-ABI function returned a null pointer");
  }

  const freeSymbol = getSymbol("ad_free_cstring");
  const guard = new FreeGuard<Pointer>(rawPtr, (p) => {
    // ad_free_cstring takes FFIType.ptr — cast through unknown to satisfy TS.
    (freeSymbol as unknown as (p: Pointer) => void)(p);
  });

  try {
    return new CString(guard.ptr!).toString();
  } finally {
    guard.free();
  }
}

// ---------------------------------------------------------------------------
// Message loop
// ---------------------------------------------------------------------------

port.on("message", (msg: WorkerRequest) => {
  const { id, op, handle, paramsJSON } = msg;

  let jsonString: string;
  try {
    if (op === "open") {
      // ad_open: paramsJSON already contains the full params; no handle.
      jsonString = callSymbol(getSymbol("ad_open"), paramsJSON);

    } else if (op === "close") {
      // ad_close: params is {"handle":"<token>"}.
      const closeJSON =
        handle !== null ? JSON.stringify({ handle }) : "{}";
      jsonString = callSymbol(getSymbol("ad_close"), closeJSON);

    } else {
      // Verb call.
      const symbolName = "ad_" + kebabToUnderscore(op);
      const verbFn = getSymbol(symbolName);

      let inputJSON: string;
      if (handle !== null && !isHandleFree(op)) {
        // Embed handle into params.
        const parsedParams = JSON.parse(paramsJSON) as Record<string, unknown>;
        parsedParams.handle = handle;
        inputJSON = JSON.stringify(parsedParams);
      } else {
        // Handle-free verb (e.g. "version") or null handle.
        inputJSON = paramsJSON;
      }

      jsonString = callSymbol(verbFn, inputJSON);
    }
  } catch (e) {
    port.postMessage({
      id,
      type: "ffi-error",
      message: e instanceof Error ? e.message : String(e),
    } satisfies WorkerResponse);
    return;
  }

  port.postMessage({
    id,
    type: "result",
    jsonString,
  } satisfies WorkerResponse);
});
