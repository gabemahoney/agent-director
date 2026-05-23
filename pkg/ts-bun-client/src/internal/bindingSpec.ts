/**
 * bindingSpec — canonical set of C-ABI symbol names expected in the dlopen
 * binding object.
 *
 * Exported as a plain string array so the binding-coverage test can import
 * it WITHOUT spawning a worker or loading the native library.
 *
 * The set equals:
 *   ["ad_open", "ad_close", "ad_free_cstring", ...verbs.map(v => "ad_" + kebabToUnderscore(v))]
 *
 * Any drift between this list and the actual dlopen spec in worker.ts is caught
 * by test/ffi-binding-coverage.test.ts.
 */

import { VERBS, kebabToUnderscore } from "./verbs.js";

/**
 * BINDING_SYMBOL_NAMES is the exhaustive list of C symbols that the worker's
 * dlopen binding object must declare.
 */
export const BINDING_SYMBOL_NAMES: readonly string[] = [
  "ad_open",
  "ad_close",
  "ad_free_cstring",
  ...VERBS.map((v) => "ad_" + kebabToUnderscore(v)),
];
