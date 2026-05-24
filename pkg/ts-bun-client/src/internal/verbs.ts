/**
 * Canonical callable verb list — source of truth for all TS/Bun client code
 * that needs to iterate, type-check, or map verb names.
 *
 * This list is hand-authored from `manifest.CallableVerbs()` in
 * pkg/api/manifest/manifest.go. It includes every verb where `Callable: true`
 * and excludes the three non-callable verbs (help, serve, hook).
 *
 * Currently 15 callable verbs: spawn, status, get, send-keys, read-pane,
 * kill, decide, resume, find-missing, expire, delete, make-template, list,
 * pause, version.
 *
 * NOTE: `help` has `Callable: false` in manifest.go (source of truth); it is
 * intentionally excluded here even though earlier epic-spec drafts assumed
 * otherwise. The .go source wins.
 */

// ---------------------------------------------------------------------------
// Verb list
// ---------------------------------------------------------------------------

/** All callable verbs in their canonical kebab form (matches manifest). */
export const VERBS = [
  "spawn",
  "status",
  "get",
  "send-keys",
  "read-pane",
  "kill",
  "decide",
  "resume",
  "find-missing",
  "expire",
  "delete",
  "make-template",
  "list",
  "pause",
  "version",
] as const;

/** Union type of every callable verb name. */
export type VerbName = (typeof VERBS)[number];

// ---------------------------------------------------------------------------
// Name conversion helpers
// ---------------------------------------------------------------------------

/**
 * kebabToUnderscore converts a kebab-case verb name to the underscore form
 * used in the C-ABI symbol name.
 *
 * Examples:
 *   "send-keys"     → "send_keys"
 *   "find-missing"  → "find_missing"
 *   "make-template" → "make_template"
 *   "read-pane"     → "read_pane"
 *   "version"       → "version"
 */
export function kebabToUnderscore(v: string): string {
  return v.replace(/-/g, "_");
}

/**
 * kebabToCamel converts a kebab-case verb name to the camelCase form used
 * as a method name on the Client class.
 *
 * Examples:
 *   "send-keys"     → "sendKeys"
 *   "find-missing"  → "findMissing"
 *   "make-template" → "makeTemplate"
 *   "read-pane"     → "readPane"
 *   "version"       → "version"
 */
export function kebabToCamel(v: string): string {
  return v.replace(/-([a-z])/g, (_, c: string) => c.toUpperCase());
}

/**
 * KEBAB_TO_CAMEL is a static map from every VerbName to its camelCase form.
 * Prefer this over calling kebabToCamel() at runtime when the input is always
 * a VerbName (avoids a string scan in hot code paths).
 */
export const KEBAB_TO_CAMEL: Record<VerbName, string> = {
  "spawn": "spawn",
  "status": "status",
  "get": "get",
  "send-keys": "sendKeys",
  "read-pane": "readPane",
  "kill": "kill",
  "decide": "decide",
  "resume": "resume",
  "find-missing": "findMissing",
  "expire": "expire",
  "delete": "delete",
  "make-template": "makeTemplate",
  "list": "list",
  "pause": "pause",
  "version": "version",
};

// ---------------------------------------------------------------------------
// Handle-free verb set
// ---------------------------------------------------------------------------

/**
 * HANDLE_FREE_VERBS is the set of verbs that do not require a Client handle.
 * Currently only "version". Mirrors pkg/cabi/dispatch.go's handleFreeVerbs map.
 */
export const HANDLE_FREE_VERBS: ReadonlySet<string> = new Set(["version"]);

/** Returns true if the given verb does not require a Client handle. */
export function isHandleFree(verb: string): boolean {
  return HANDLE_FREE_VERBS.has(verb);
}
