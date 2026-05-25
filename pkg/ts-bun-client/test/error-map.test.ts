/**
 * error-map.test.ts — unit tests for src/internal/errorMap.ts (Task A5).
 *
 * Tests SRD SR-4.1 (static map built from catalog), SR-4.2 (throw by
 * constructor with envelope fields), SR-4.3 (ErrUnknownErrorName for unknown
 * names), SR-4.4 (envelope key name is err_name).
 *
 * Cases:
 *   1. Map size equals catalog entry count (34).
 *   2. For each catalog entry: synthetic envelope → throwFromEnvelope throws
 *      the correct typed class (instanceof check).
 *   3. Synthetic envelope with err_name="ErrTotallyBogus" → throws
 *      ErrUnknownErrorName carrying the offending name and full envelope.
 *
 * IMPORT NOTE: Expected exports from src/internal/errorMap.ts:
 *   errorMap: Map<string, ErrCtor>          ← static module-load-time map
 *   throwFromEnvelope(verb: string, envelope: {err_name: string, err_description: string, ...}): never
 *
 * If the engineer uses different export names, update the imports below.
 * The function is expected to THROW, not return.
 */

import { test, expect, describe } from "bun:test";
import { loadErrNameCatalog } from "./internal/loadCatalog.js";
import { AgentDirectorError, ErrUnknownErrorName } from "../src/errors.js";
import { errorMap, throwFromEnvelope } from "../src/internal/errorMap.js";

// Load the canonical catalog (same source the drift test uses).
const catalogNames = loadErrNameCatalog(); // sorted unique array of 34 names

// ---------------------------------------------------------------------------
// Case 1: map.size === 34
// ---------------------------------------------------------------------------
describe("error map — catalog coverage", () => {
  test("errorMap.size equals catalog entry count (34)", () => {
    expect(errorMap).toBeInstanceOf(Map);
    expect(errorMap.size).toBe(catalogNames.length);
    expect(errorMap.size).toBe(34);
  });

  test("every catalog name appears as a key in errorMap", () => {
    for (const name of catalogNames) {
      expect(errorMap.has(name)).toBe(true);
    }
  });
});

// ---------------------------------------------------------------------------
// Case 2: for each catalog entry, throwFromEnvelope throws the right class
// ---------------------------------------------------------------------------
describe("error map — throwFromEnvelope per catalog entry", () => {
  for (const name of catalogNames) {
    test(`${name} → throws typed class (instanceof AgentDirectorError)`, () => {
      const envelope = {
        err_name: name,
        err_description: `synthetic description for ${name}`,
      };

      let caught: unknown;
      try {
        throwFromEnvelope("list", envelope);
      } catch (e) {
        caught = e;
      }

      // Must throw something.
      expect(caught).toBeDefined();
      expect(caught).toBeInstanceOf(Error);
      expect(caught).toBeInstanceOf(AgentDirectorError);

      const err = caught as AgentDirectorError;
      // The typed class must match the catalog name.
      expect(err.errName).toBe(name);
      // The constructor name (err.name) must match as well.
      expect(err.name).toBe(name);
      // Verb is forwarded.
      expect(err.verb).toBe("list");
    });
  }
});

// ---------------------------------------------------------------------------
// Case 3: unknown err_name → ErrUnknownErrorName
// ---------------------------------------------------------------------------
describe("error map — unknown err_name", () => {
  test("ErrTotallyBogus → throws ErrUnknownErrorName", () => {
    const envelope = {
      err_name: "ErrTotallyBogus",
      err_description: "this name is not in the catalog",
      extra_context: "preserved field",
    };

    let caught: unknown;
    try {
      throwFromEnvelope("get", envelope);
    } catch (e) {
      caught = e;
    }

    expect(caught).toBeDefined();
    expect(caught).toBeInstanceOf(ErrUnknownErrorName);
    expect(caught).toBeInstanceOf(AgentDirectorError);

    const err = caught as InstanceType<typeof ErrUnknownErrorName>;
    expect(err.name).toBe("ErrUnknownErrorName");
    expect(err.errName).toBe("ErrUnknownErrorName");

    // The offending err_name must be surfaced via .unknownName field.
    expect(err.unknownName).toBe("ErrTotallyBogus");
    expect(err.message).toContain("ErrTotallyBogus");

    // The full envelope must be preserved via .envelope field.
    expect(err.envelope).toBeDefined();
    expect((err.envelope as typeof envelope).err_name).toBe("ErrTotallyBogus");
  });

  test("throwFromEnvelope always throws — never returns", () => {
    // If throwFromEnvelope returned instead of throwing, the try/catch below
    // would produce caught === undefined.  We assert that it does throw.
    let threw = false;
    try {
      throwFromEnvelope("spawn", {
        err_name: "ErrCwdMissing",
        err_description: "cwd is required",
      });
    } catch {
      threw = true;
    }
    expect(threw).toBe(true);
  });
});
