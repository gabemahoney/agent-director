/**
 * workerProxy.ts — main-thread singleton proxy for the FFI worker.
 *
 * The six-step C-ABI call recipe splits across two files:
 *   worker.ts      → steps 1–4: encode JSON → call C symbol → copy CString →
 *                    free pointer (try/finally)
 *   workerProxy.ts → steps 5–6: parse raw JSON string → check err_name →
 *                    throw typed error (via errorFromEnvelope) or return result
 *
 * See src/ffi.ts for the public callVerb facade that drives this module.
 *
 * Architecture
 * ────────────
 * A single dedicated Worker is spawned lazily on the first call to dispatch.
 * The Worker lives for the lifetime of the process (or until shutdown() is
 * called). All Client instances in the process share the same worker.
 *
 * Correlation IDs
 * ───────────────
 * Each dispatch call mints a monotonically increasing id and inserts a
 * {resolve, reject} pair into `_pending`. The worker echoes the id back in
 * every response so the correct Promise can be settled.
 *
 * Error envelope decoding
 * ───────────────────────
 * The worker returns raw JSON strings only. This module parses them and, if
 * `err_name` is present, calls errorFromEnvelope() so the error-class graph
 * is loaded once (on the main thread), not inside every worker thread.
 */

import { Worker } from "node:worker_threads";
import { errorFromEnvelope } from "../errors.js";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface WorkerResponse {
  id: number;
  type: "result" | "ffi-error" | "ready" | "startup-error";
  jsonString?: string;
  message?: string;
}

interface PendingEntry {
  resolve: (value: unknown) => void;
  reject: (reason: Error) => void;
}

// Error envelope shape returned by Go C-ABI on failure.
interface ErrorEnvelope {
  err_name: string;
  err_description: string;
}

function isErrorEnvelope(v: unknown): v is ErrorEnvelope {
  return (
    typeof v === "object" &&
    v !== null &&
    typeof (v as ErrorEnvelope).err_name === "string"
  );
}

// ---------------------------------------------------------------------------
// Module-level state
// ---------------------------------------------------------------------------

let _worker: Worker | null = null;
let _nextId = 1;
const _pending = new Map<number, PendingEntry>();

// Resolves when the worker posts {type:"ready"}; rejects on startup-error.
let _readyResolve: (() => void) | null = null;
let _readyReject: ((e: Error) => void) | null = null;
let _readyPromise: Promise<void> | null = null;

// ---------------------------------------------------------------------------
// Worker lifecycle
// ---------------------------------------------------------------------------

/**
 * _rejectAll rejects every in-flight pending dispatch with the given error.
 * Used when the worker exits unexpectedly or encounters a startup error.
 */
function _rejectAll(reason: Error): void {
  for (const entry of _pending.values()) {
    entry.reject(reason);
  }
  _pending.clear();
}

/**
 * _spawnWorker creates the dedicated Worker and wires its event handlers.
 * Called at most once per process (unless shutdown() is called first).
 */
function _spawnWorker(): void {
  const worker = new Worker(new URL("./worker.ts", import.meta.url));

  _readyPromise = new Promise<void>((resolve, reject) => {
    _readyResolve = resolve;
    _readyReject = reject;
  });

  worker.on("message", (msg: WorkerResponse) => {
    if (msg.type === "ready") {
      _readyResolve?.();
      _readyResolve = null;
      _readyReject = null;
      return;
    }

    if (msg.type === "startup-error") {
      const err = new Error(
        `agent-director worker failed to start: ${msg.message ?? "(unknown)"}`
      );
      _readyReject?.(err);
      _readyResolve = null;
      _readyReject = null;
      _rejectAll(err);
      return;
    }

    const entry = _pending.get(msg.id);
    if (!entry) return; // stale/unknown id — ignore

    _pending.delete(msg.id);

    if (msg.type === "ffi-error") {
      entry.reject(
        new Error(`FFI error: ${msg.message ?? "(unknown)"}`)
      );
      return;
    }

    // msg.type === "result" — raw JSON string
    const jsonString = msg.jsonString ?? "";
    let parsed: unknown;
    try {
      parsed = JSON.parse(jsonString);
    } catch (e) {
      entry.reject(
        new Error(
          `Failed to parse C-ABI response JSON: ${e instanceof Error ? e.message : String(e)}`
        )
      );
      return;
    }

    if (isErrorEnvelope(parsed)) {
      entry.reject(errorFromEnvelope(parsed));
      return;
    }

    entry.resolve(parsed);
  });

  worker.on("error", (err: Error) => {
    const reason = new Error(`agent-director worker error: ${err.message}`);
    _readyReject?.(reason);
    _readyResolve = null;
    _readyReject = null;
    _rejectAll(reason);
  });

  worker.on("exit", (code: number) => {
    // Only report abnormal exits (code 0 = clean shutdown via shutdown()).
    const reason = new Error(
      `agent-director worker exited with code ${code}`
    );
    _readyReject?.(reason);
    _readyResolve = null;
    _readyReject = null;
    if (_pending.size > 0) {
      _rejectAll(reason);
    }
    _worker = null;
    _readyPromise = null;
  });

  _worker = worker;
}

/**
 * _ensureWorker lazily spawns the worker and waits until it is ready.
 * Subsequent calls return the same ready promise without re-spawning.
 */
async function _ensureWorker(): Promise<void> {
  if (_worker === null) {
    _spawnWorker();
  }
  await _readyPromise;
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * workerProxy is the mutable singleton used by callVerb (and tests).
 * Exporting an object (rather than a bare function) allows tests to replace
 * `workerProxy.dispatch` without module-level mocking:
 *
 *   import { workerProxy } from "../src/internal/workerProxy.js";
 *   workerProxy.dispatch = mock(async () => '{"version":"test"}');
 */
export const workerProxy: {
  dispatch: (
    op: string,
    handle: string | null,
    params: unknown
  ) => Promise<unknown>;
} = {
  /**
   * dispatch sends an operation to the dedicated worker and returns a Promise
   * that resolves with the parsed response value or rejects with a typed error.
   *
   * @param op      "open" | "close" | <verb-kebab>
   * @param handle  The opaque Client handle (null for open and handle-free verbs)
   * @param params  Verb-specific params object (JSON-stringified before send)
   */
  async dispatch(
    op: string,
    handle: string | null,
    params: unknown
  ): Promise<unknown> {
    await _ensureWorker();

    const id = _nextId++;
    const paramsJSON = JSON.stringify(params ?? {});

    return new Promise<unknown>((resolve, reject) => {
      _pending.set(id, { resolve, reject });

      _worker!.postMessage({ id, op, handle, paramsJSON });
    });
  },
};

/**
 * shutdown terminates the worker and rejects all pending dispatches. Call
 * this in tests (afterAll) to prevent lingering worker threads.
 */
export function shutdown(): void {
  if (_worker !== null) {
    const w = _worker;
    _worker = null;
    _readyPromise = null;

    _rejectAll(new Error("workerProxy: shutdown() called"));
    void w.terminate();
  }
}
