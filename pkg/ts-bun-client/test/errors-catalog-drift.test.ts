/**
 * errors-catalog-drift.test.ts — regression gate: the AgentDirectorError
 * subclasses src/errors.ts exports equal the shared err_name catalog
 * (pkg/api/errnames/catalog.json), less the exact administrator-only category
 * and the TS-only allow-list. A failure names
 * each drifted class on its side (the ts-bun-5 testplan injects ErrFake).
 */
import { test, expect } from "bun:test";
import fs from "fs";
import path from "path";
import * as errors from "../src/errors.js";
import * as publicExports from "../src/index.js";
import { errorMap, throwFromEnvelope } from "../src/internal/errorMap.js";
import { thrownBy } from "./internal/helper.js";
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

// The canonical Go catalog includes this existing administrator-only category;
// it does not gain a TS class, public export, or Client/MCP verb error.
test("journal refusal remains catalogued only as an administrator category", () => {
  expect(Object.hasOwn(errors, "ErrJournalMigrationRefused")).toBe(false);
  expect(Object.hasOwn(publicExports, "ErrJournalMigrationRefused")).toBe(false);
  expect(errorMap.has("ErrJournalMigrationRefused")).toBe(false);
  const envelope = { err_name: "ErrJournalMigrationRefused", err_description: "administrator refusal" };
  const unknown = thrownBy(() => throwFromEnvelope("send-keys", envelope)) as errors.ErrUnknownErrorName;
  expect(unknown).toBeInstanceOf(errors.ErrUnknownErrorName);
  expect(unknown.unknownName).toBe(envelope.err_name);
  expect(unknown.envelope).toEqual(envelope);
  const apiDir = path.resolve(import.meta.dir, "../../../pkg/api");
  const catalog = JSON.parse(fs.readFileSync(path.join(apiDir, "errnames/catalog.json"), "utf-8")) as Array<{ name: string; package: string }>;
  expect(catalog.filter((entry) => entry.name === "ErrJournalMigrationRefused").map((entry) => entry.package)).toEqual(["adminapi"]);
  const surface = JSON.parse(fs.readFileSync(path.join(apiDir, "manifest/surface.json"), "utf-8")) as {
    verbs: Array<{ name: string; callable: boolean; error_names: string[] }>;
  };
  expect(surface.verbs.length).toBeGreaterThan(0);
  for (const verb of surface.verbs.filter((verb) => verb.callable)) {
    expect(verb.error_names, verb.name).not.toContain("ErrJournalMigrationRefused");
  }
});
