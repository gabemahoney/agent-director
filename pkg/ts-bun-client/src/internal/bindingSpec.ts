/**
 * bindingSpec — canonical set of C-ABI symbol names expected in the dlopen
 * binding object, plus a factory that builds the runtime FFI binding spec.
 *
 * BINDING_SYMBOL_NAMES is exported as a plain string array so the
 * binding-coverage test can import it WITHOUT spawning a worker or loading
 * the native library.
 *
 * buildBindingSpec() returns the full FFI binding object suitable for passing
 * directly to Bun's dlopen(). It is the single source of truth consumed by
 * platform.ts::loadNative() so that worker.ts and bootstrapFfi.ts both
 * resolve the same symbols without duplication.
 *
 * Any drift between BINDING_SYMBOL_NAMES and buildBindingSpec() is caught
 * by test/ffi-binding-coverage.test.ts.
 */

import { FFIType } from "bun:ffi";
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

/** A single FFI function declaration (args + return type). */
export interface FFISymbolDef {
  args: FFIType[];
  returns: FFIType;
}

/** The full dlopen binding spec as a plain object — pass to Bun's dlopen(). */
export type BindingSpec = Record<string, FFISymbolDef>;

/**
 * buildBindingSpec constructs the full FFI binding map for the native library.
 *
 * Includes lifecycle symbols (ad_open, ad_close, ad_free_cstring) plus one
 * entry per callable verb. All verb symbols take a single cstring argument and
 * return a ptr (raw pointer, freed via ad_free_cstring).
 */
export function buildBindingSpec(): BindingSpec {
  const spec: BindingSpec = {
    ad_open: { args: [FFIType.cstring], returns: FFIType.ptr },
    ad_close: { args: [FFIType.cstring], returns: FFIType.ptr },
    ad_free_cstring: { args: [FFIType.ptr], returns: FFIType.void },
  };
  for (const verb of VERBS) {
    spec["ad_" + kebabToUnderscore(verb)] = {
      args: [FFIType.cstring],
      returns: FFIType.ptr,
    };
  }
  return spec;
}
