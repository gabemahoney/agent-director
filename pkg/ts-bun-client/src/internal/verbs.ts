/**
 * Canonical callable verb list — source of truth for all TS/Bun client code
 * that needs to iterate, type-check, or map verb names.
 *
 * This list is hand-authored from `manifest.CallableVerbs()` in
 * pkg/api/manifest/manifest.go. It includes every verb where `Callable: true`
 * and excludes the three non-callable verbs (help, serve, hook).
 *
 * Currently 16 callable verbs: spawn, status, get, send-keys, read-pane,
 * kill, decide, get-permission, resume, find-missing, expire, delete,
 * make-template, list, pause, version.
 *
 * NOTE: `help` has `Callable: false` in manifest.go (source of truth); it is
 * intentionally excluded here. The .go source wins.
 */

/** All callable verbs in their canonical kebab form (matches manifest). */
export const VERBS = [
  "spawn",
  "status",
  "get",
  "send-keys",
  "read-pane",
  "kill",
  "decide",
  "get-permission",
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
