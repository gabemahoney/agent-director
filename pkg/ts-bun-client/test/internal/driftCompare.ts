/**
 * driftCompare.ts — pure comparison core for the catalog-drift regression test.
 *
 * Extracted so the failure-message format can be unit-tested with synthetic
 * input without touching the real errors module or catalog file.
 *
 * Used by:
 *   test/errors-catalog-drift.test.ts        (real comparison)
 *   test/errors-catalog-drift-failures.test.ts (synthetic input tests)
 */

/** Result of a catalog vs TS-class set comparison. */
export interface DriftResult {
  /** Names present in the catalog but absent from the TS class set (after allow-list removal). */
  catalogOnly: string[];
  /** Names present in the TS class set but absent from the catalog (after allow-list removal). */
  tsOnly: string[];
  /**
   * Multi-line failure message, empty string when both sides are equal.
   * Format when non-empty:
   *   "errors-catalog-drift: TS subclass set != catalog\n"
   *   "  in catalog but not in TS: [ErrFoo, ErrBar]\n"   (omitted when empty)
   *   "  in TS but not in catalog: [ErrBaz]"              (omitted when empty)
   */
  formatted: string;
}

/**
 * compareSets compares two name sets (TS class names vs. catalog names) after
 * removing allow-listed names from both sides, and returns the diff.
 *
 * @param tsNames     Constructor names of all AgentDirectorError subclasses
 *                    found in the TS errors module.
 * @param catalogNames Sorted unique err_name strings from the Go catalog.
 * @param allowList   Names to exclude from both sides before comparing
 *                    (the TS-only subclasses that have no Go equivalent).
 */
export function compareSets(
  tsNames: string[],
  catalogNames: string[],
  allowList: string[]
): DriftResult {
  const allowSet = new Set(allowList);

  const tsSet = new Set(tsNames.filter((n) => !allowSet.has(n)));
  const catalogSet = new Set(catalogNames.filter((n) => !allowSet.has(n)));

  const catalogOnly = Array.from(catalogSet)
    .filter((n) => !tsSet.has(n))
    .sort();
  const tsOnly = Array.from(tsSet)
    .filter((n) => !catalogSet.has(n))
    .sort();

  let formatted = "";
  if (catalogOnly.length > 0 || tsOnly.length > 0) {
    formatted = "errors-catalog-drift: TS subclass set != catalog";
    if (catalogOnly.length > 0) {
      formatted += `\n  in catalog but not in TS: [${catalogOnly.join(", ")}]`;
    }
    if (tsOnly.length > 0) {
      formatted += `\n  in TS but not in catalog: [${tsOnly.join(", ")}]`;
    }
  }

  return { catalogOnly, tsOnly, formatted };
}
