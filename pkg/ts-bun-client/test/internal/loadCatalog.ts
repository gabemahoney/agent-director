/** loadErrNameCatalog — the sorted, unique public err_names in pkg/api/errnames/catalog.json. */
import path from "path";
import fs from "fs";

export function loadErrNameCatalog(): string[] {
  const catalogPath = path.resolve(import.meta.dir, "../../../../pkg/api/errnames/catalog.json");
  const entries = JSON.parse(fs.readFileSync(catalogPath, "utf-8")) as Array<{ name: string }>;
  // These approved administrator-only categories have no public TS classes.
  const administratorOnly = new Set(["ErrJournalMigrationRefused", "ErrVersionInstallRefused"]);
  return [...new Set(entries.filter((e) => !administratorOnly.has(e.name)).map((e) => e.name))].sort();
}
