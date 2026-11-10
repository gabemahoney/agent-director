/** loadErrNameCatalog — the sorted, unique public err_names in pkg/api/errnames/catalog.json. */
import path from "path";
import fs from "fs";

export function loadErrNameCatalog(): string[] {
  const catalogPath = path.resolve(import.meta.dir, "../../../../pkg/api/errnames/catalog.json");
  const entries = JSON.parse(fs.readFileSync(catalogPath, "utf-8")) as Array<{ name: string }>;
  // Only this approved administrator-only category has no public TS class.
  return [...new Set(entries.filter((e) => e.name !== "ErrJournalMigrationRefused").map((e) => e.name))].sort();
}
