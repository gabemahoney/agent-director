/**
 * errorMap.ts — catalog-driven static error map for the subprocess Client.
 *
 * Builds a `Map<string, ErrConstructor>` at module load time by iterating
 * `pkg/api/errnames/catalog.json` (33 entries as of 2026-05-25). The map
 * keys are the canonical `err_name` strings (e.g. "ErrSpawnNotFound");
 * values are the typed constructors already exported from `src/errors.ts`.
 *
 * Exports `throwFromEnvelope(verb, envelope)` which:
 *   - Looks up `envelope.err_name` in the static map.
 *   - Throws the typed subclass when found (carrying verb + envelope fields).
 *   - Throws `ErrUnknownErrorName` when the err_name is not in the map
 *     (SRD SR-4.3 — defensive against Go-side catalog additions that race
 *     the TS-side regen).
 *
 * Implements SRD SR-4.1 (static module-load-time map), SR-4.2 (throw by
 * constructor with envelope fields), SR-4.3 (ErrUnknownErrorName for
 * unknown names), SR-4.4 (envelope key name is err_name).
 *
 * Internal — NOT re-exported from src/index.ts.
 */

import * as errors from "../errors.js";
// Inline the catalog at bundle time so the packed tarball never needs a
// filesystem path relative to import.meta.dir (which breaks in consumers).
import catalogJson from "../../../../pkg/api/errnames/catalog.json" with { type: "json" };

// ---------------------------------------------------------------------------
// Catalog loader (module-load-time, bundler-inlined)
// ---------------------------------------------------------------------------

interface CatalogEntry {
  name: string;
  package?: string;
  description?: string;
  scope?: string;
}

const _catalog = catalogJson as CatalogEntry[];

// ---------------------------------------------------------------------------
// Constructor type alias
// ---------------------------------------------------------------------------

type ErrConstructor = new (
  verb: string,
  err_name: string,
  err_description: string
) => errors.AgentDirectorError;

// ---------------------------------------------------------------------------
// Static error map (module-load-time)
// ---------------------------------------------------------------------------

/**
 * errorMap maps every err_name string from catalog.json to its typed
 * AgentDirectorError constructor. Built once at module load.
 *
 * Entries not present in errors.ts (e.g. a future catalog addition that
 * hasn't been regenerated on the TS side yet) are silently omitted; the
 * throwFromEnvelope helper handles the miss via ErrUnknownErrorName.
 */
export const errorMap: ReadonlyMap<string, ErrConstructor> =
  new Map(
    _catalog.flatMap((entry): [string, ErrConstructor][] => {
      const errorsRecord = errors as unknown as Record<string, unknown>;
      const Ctor = errorsRecord[entry.name];
      if (typeof Ctor !== "function") {
        // This entry exists in the Go catalog but has no TS class yet.
        // throwFromEnvelope will catch it as ErrUnknownErrorName.
        return [];
      }
      return [[entry.name, Ctor as ErrConstructor]];
    })
  );

// ---------------------------------------------------------------------------
// Envelope shape guard
// ---------------------------------------------------------------------------

/** Shape of a JSON envelope that carries a typed error. */
interface ErrorEnvelope {
  err_name: string;
  err_description: string;
}

/**
 * isErrorEnvelope returns true when the parsed envelope contains an err_name
 * field (indicating a typed error, not a success result).
 */
export function isErrorEnvelope(
  envelope: unknown
): envelope is ErrorEnvelope {
  return (
    typeof envelope === "object" &&
    envelope !== null &&
    "err_name" in envelope &&
    typeof (envelope as ErrorEnvelope).err_name === "string"
  );
}

// ---------------------------------------------------------------------------
// throwFromEnvelope
// ---------------------------------------------------------------------------

/**
 * throwFromEnvelope inspects an envelope for `err_name` and throws the
 * corresponding typed AgentDirectorError subclass.
 *
 * Call this only when `isErrorEnvelope(envelope)` is true.
 *
 * @param verb     The verb name that produced the envelope (for error context).
 * @param envelope The parsed JSON envelope object.
 * @throws         The typed subclass from SUBPROCESS_ERROR_MAP, or
 *                 ErrUnknownErrorName when the name is not in the map.
 */
export function throwFromEnvelope(verb: string, envelope: unknown): never {
  const env = envelope as ErrorEnvelope;
  const errName = env.err_name;
  const envelopeRecord = envelope as unknown as Record<string, unknown>;
  const errDescription =
    typeof envelopeRecord["err_description"] === "string"
      ? (envelopeRecord["err_description"] as string)
      : "";

  const Ctor = errorMap.get(errName);
  if (Ctor !== undefined) {
    throw new Ctor(verb, errName, errDescription);
  }

  // Unknown err_name — Go catalog ahead of TS catalog regen (SRD SR-4.3).
  throw new errors.ErrUnknownErrorName(errName, envelope);
}
