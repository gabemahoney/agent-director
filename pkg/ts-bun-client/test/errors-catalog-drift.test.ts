/**
 * errors-catalog-drift.test.ts — regression gate: the AgentDirectorError
 * subclasses src/errors.ts exports equal the shared err_name catalog
 * (pkg/api/errnames/catalog.json), less the TS-only allow-list. A failure names
 * each drifted class on its side (the ts-bun-5 testplan injects ErrFake).
 */
import { test, expect } from "bun:test";
import * as errors from "../src/errors.js";
import { loadErrNameCatalog } from "./internal/loadCatalog.js";

test("TS subclasses equal shared err_name catalog", () => {
  const allow = new Set<string>(errors.TS_ONLY_ERROR_NAMES);
  const ts = new Set(
    Object.entries(errors)
      .filter(([k, v]) => k.startsWith("Err") && typeof v === "function" &&
        (v as { prototype: unknown }).prototype instanceof errors.AgentDirectorError)
      .map(([k]) => k)
      .filter((k) => !allow.has(k))
  );
  const catalog = new Set(loadErrNameCatalog().filter((n) => !allow.has(n)));
  const catalogOnly = [...catalog].filter((n) => !ts.has(n)).sort();
  const tsOnly = [...ts].filter((n) => !catalog.has(n)).sort();
  const report =
    `errors-catalog-drift: TS subclass set != catalog\n` +
    `  in catalog but not in TS: [${catalogOnly.join(", ")}]\n` +
    `  in TS but not in catalog: [${tsOnly.join(", ")}]`;
  expect(catalogOnly.length + tsOnly.length, report).toBe(0);
});
