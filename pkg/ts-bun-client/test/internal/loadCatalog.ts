/**
 * loadCatalog.ts — reads pkg/api/errnames/catalog.json (produced by Epic 1's
 * `go generate` mechanism) and returns a sorted unique list of err_name strings.
 *
 * Used by the catalog-drift regression test (T10 / t3.qe2.ew.3h.uv).
 */
import path from "path";
import fs from "fs";

/** Shape of a single entry in catalog.json. */
interface CatalogEntry {
  name: string;
  package?: string;
  description?: string;
  scope?: string;
}

/**
 * loadErrNameCatalog reads `pkg/api/errnames/catalog.json` relative to the
 * repository root and returns a sorted, deduplicated array of err_name strings.
 *
 * Throws a descriptive error when:
 *  - The file is missing (Epic 1 not yet landed).
 *  - The file cannot be read.
 *  - The JSON is malformed or has an unexpected shape.
 *
 * The function is synchronous; catalog reads are negligible in test contexts.
 */
export function loadErrNameCatalog(): string[] {
  // test/internal/ is 4 levels below the repo root:
  //   pkg/ts-bun-client/test/internal → up 4 → repo root
  const catalogPath = path.resolve(
    import.meta.dir,
    "../../../../pkg/api/errnames/catalog.json"
  );

  if (!fs.existsSync(catalogPath)) {
    throw new Error(
      `Epic 1/2 not yet landed; expected catalog at pkg/api/errnames/catalog.json` +
        ` (resolved to: ${catalogPath})`
    );
  }

  let raw: string;
  try {
    raw = fs.readFileSync(catalogPath, "utf-8");
  } catch (e) {
    throw new Error(`Failed to read catalog at ${catalogPath}: ${e}`);
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch (e) {
    throw new Error(`Malformed catalog JSON at ${catalogPath}: ${e}`);
  }

  if (!Array.isArray(parsed)) {
    throw new Error(
      `Malformed catalog at ${catalogPath}: expected top-level array, got ${typeof parsed}`
    );
  }

  const names = new Set<string>();
  for (const entry of parsed as unknown[]) {
    if (
      typeof entry !== "object" ||
      entry === null ||
      typeof (entry as CatalogEntry).name !== "string"
    ) {
      throw new Error(
        `Malformed catalog entry at ${catalogPath}: ${JSON.stringify(entry)}`
      );
    }
    names.add((entry as CatalogEntry).name);
  }

  return Array.from(names).sort();
}
