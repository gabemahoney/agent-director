import * as os from "node:os";

/**
 * expandTilde resolves a leading `~` or `~/` to the user's home directory.
 *
 * Resolution order:
 *   1. `process.env.HOME` if set and non-empty.
 *   2. `os.homedir()` from node:os as fallback.
 *
 * Behaviour by input:
 *   - `""` → `""`  (empty string unchanged)
 *   - `"~"` → `"/home/user"` (bare tilde → home directory)
 *   - `"~/foo"` → `"/home/user/foo"` (tilde-prefix → home + rest)
 *   - `"/abs/path"` → `"/abs/path"` (absolute path unchanged)
 *   - `"relative/path"` → `"relative/path"` (relative path unchanged)
 *   - `"foo~bar"` → `"foo~bar"` (tilde not at start → unchanged)
 */
export function expandTilde(p: string): string {
  if (p === "") return "";
  if (p !== "~" && !p.startsWith("~/")) return p;

  const home = process.env["HOME"] ?? os.homedir();

  if (p === "~") return home;
  // p starts with "~/" — strip the leading "~" and prepend home.
  return home + p.slice(1);
}
