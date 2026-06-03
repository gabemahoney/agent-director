/**
 * errors-catalog-drift.test.ts
 *
 * Regression gate: asserts that the set of AgentDirectorError subclasses
 * exported by src/errors.ts exactly equals the shared err_name catalog at
 * pkg/api/errnames/catalog.json (produced by Epic 1's `go generate` mechanism).
 *
 * TS-only subclasses (ErrClientClosed, ErrBunVersionTooOld,
 * ErrConsumerSignal, ErrCallTimeout, ErrUnknownErrorName, plus the three
 * b.ue3 system-install errors ErrSystemInstallNotFound /
 * ErrSystemInstallTooOld / ErrSystemInstallUnreachable) are excluded via
 * the centralized allow-list at src/internal/tsOnlyErrors.ts.
 *
 * Must run in < 100 ms (pure in-process, no subprocess).
 */
import { test, expect } from "bun:test";
import * as errors from "../src/errors.js";
import { loadErrNameCatalog } from "./internal/loadCatalog.js";
import { compareSets } from "./internal/driftCompare.js";

test("TS subclasses equal shared err_name catalog", () => {
  // Collect constructor names of every AgentDirectorError subclass.
  const tsNames = Object.entries(errors)
    .filter(
      ([k, v]) =>
        typeof v === "function" &&
        k.startsWith("Err") &&
        (v as { prototype: unknown }).prototype instanceof errors.AgentDirectorError
    )
    .map(([k]) => k);

  // Load catalog names (sorted, unique).
  const catalogNames = loadErrNameCatalog();

  // Allow-list: TS-only subclasses with no Go catalog equivalent.
  const allowList = Array.from(errors.TS_ONLY_ERROR_NAMES);

  const result = compareSets(tsNames, catalogNames, allowList);

  expect(
    result.catalogOnly.length === 0 && result.tsOnly.length === 0,
    result.formatted
  ).toBe(true);
});
