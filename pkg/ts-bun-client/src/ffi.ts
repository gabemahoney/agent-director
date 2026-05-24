/**
 * ffi.ts — public facade for the C-ABI call recipe.
 *
 * The six-step FFI recipe is split across three files:
 *
 *   src/internal/worker.ts      — steps 1–4 (runs inside the worker thread)
 *     1. Encode params as null-terminated UTF-8 JSON
 *     2. Call the C symbol via Bun dlopen
 *     3. Copy the returned CString to a JS string
 *     4. Free the C pointer via ad_free_cstring (try/finally — error path frees too)
 *
 *   src/internal/workerProxy.ts — steps 5–6 (main thread, post-worker)
 *     5. Parse the raw JSON string
 *     6. If err_name present → throw typed error via errorFromEnvelope;
 *        else → return parsed result
 *
 *   src/ffi.ts (this file)      — thin public facade
 *     Exposes callVerb<P, R> that routes to workerProxy.dispatch and casts.
 *
 * WHY a worker?
 * Bun FFI has no per-symbol `async: true` marker. Without the worker, every
 * C call would block the JS event loop. The worker runs in a separate thread
 * so verb calls are off-main-thread and the event loop stays responsive.
 * See src/internal/workerProxy.ts for the singleton design rationale.
 *
 * Internal — NOT re-exported from src/index.ts.
 */

import { workerProxy } from "./internal/workerProxy.js";

// Re-export the verb list and symbol set for downstream consumers (tests, etc.)
export { VERBS, KEBAB_TO_CAMEL } from "./internal/verbs.js";
export type { VerbName } from "./internal/verbs.js";
export { BINDING_SYMBOL_NAMES } from "./internal/bindingSpec.js";
export { shutdown as shutdownWorker } from "./internal/workerProxy.js";

/**
 * callVerb dispatches a verb call through the dedicated worker thread and
 * returns a Promise that resolves with the typed result R or rejects with a
 * typed AgentDirectorError.
 *
 * @param verb    Kebab-case verb name (e.g. "send-keys", "make-template")
 * @param handle  Opaque Client handle from ad_open, or null for handle-free
 *                verbs (currently only "version")
 * @param params  Verb-specific params object; JSON-serialized inside dispatch
 */
export async function callVerb<P, R>(
  verb: string,
  handle: string | null,
  params: P
): Promise<R> {
  return workerProxy.dispatch(verb, handle, params) as Promise<R>;
}
