/**
 * readme-symbols.test.ts — export coverage gate for README.md TS snippets.
 *
 * Reads every ```ts / ```typescript block from README.md, extracts tokens
 * that look like package-level symbols (starts with uppercase, not a
 * well-known JS/TS built-in), then asserts every such token is actually
 * exported by src/index.ts.
 *
 * This catches the "I added an Err* to the README but forgot to export it"
 * class of drift without re-running the full type-checker.
 */

import { test, expect } from "bun:test";
import * as fs from "fs";
import * as path from "path";

const PKG_DIR = path.resolve(import.meta.dir, "..");
const README_PATH = path.join(PKG_DIR, "README.md");
const INDEX_PATH = path.join(PKG_DIR, "src/index.ts");

// ---------------------------------------------------------------------------
// Allow-list — uppercase tokens that are NOT package exports.
// Includes JS/TS built-ins, DOM/Node globals, and common type utilities.
// ---------------------------------------------------------------------------
const ALLOW_LIST = new Set([
  // JS built-in constructors / namespaces
  "Array",
  "BigInt",
  "Boolean",
  "Buffer",
  "Console",
  "Date",
  "Error",
  "Function",
  "JSON",
  "Map",
  "Math",
  "Number",
  "Object",
  "Promise",
  "Proxy",
  "RegExp",
  "Set",
  "String",
  "Symbol",
  "URL",
  "WeakMap",
  "WeakSet",
  // TypeScript utility types
  "Awaited",
  "Exclude",
  "Extract",
  "InstanceType",
  "NonNullable",
  "Omit",
  "Partial",
  "Pick",
  "Parameters",
  "Readonly",
  "Record",
  "Required",
  "ReturnType",
  // Platform / runtime globals
  "Bun",
  "NodeJS",
  "TextDecoder",
  "TextEncoder",
  // Common keywords / abbreviations that start uppercase in docs
  "CHANGEME", // placeholder scope token in "@CHANGEME-H3/agent-director"
  "CLI",
  "FFI",
  "H3",
  "ID",
  "SQLite",
]);

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Extract every ```ts / ```typescript block from markdown. */
function extractTsBlocks(markdown: string): string[] {
  const blocks: string[] = [];
  const re = /```(?:ts|typescript)\n([\s\S]*?)```/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(markdown)) !== null) {
    blocks.push(m[1]);
  }
  return blocks;
}

/**
 * Extract exported names from src/index.ts by scanning `export { ... }`
 * and `export type { ... }` blocks.  This is a text-level parse — good
 * enough for a file that is entirely explicit re-exports with no computed
 * names.
 *
 * Processes line-by-line to correctly handle comment lines inside export
 * blocks (splitting by comma would fuse comment+identifier into one token).
 */
function parseExports(indexSource: string): Set<string> {
  const exports = new Set<string>();
  const blockRe = /export\s+(?:type\s+)?{([^}]+)}/g;
  let m: RegExpExecArray | null;
  while ((m = blockRe.exec(indexSource)) !== null) {
    const block = m[1];
    for (const line of block.split("\n")) {
      // Strip inline comments, trailing commas, and whitespace.
      const stripped = line
        .replace(/\/\/.*$/, "")
        .replace(/,\s*$/, "")
        .trim();
      if (!stripped || !(/^[A-Za-z_$]/).test(stripped)) continue;
      // Handle "Name as Alias" — take only the first identifier.
      const name = stripped.split(/\s+/)[0];
      if (name) exports.add(name);
    }
  }
  return exports;
}

/**
 * Extract uppercase-starting identifier tokens from TS source text.
 * Filters out allow-listed JS/TS built-ins.
 */
function extractUppercaseTokens(code: string): Set<string> {
  const tokens = new Set<string>();
  const wordRe = /\b([A-Z][A-Za-z0-9_$]*)\b/g;
  let m: RegExpExecArray | null;
  while ((m = wordRe.exec(code)) !== null) {
    const word = m[1];
    if (!ALLOW_LIST.has(word)) {
      tokens.add(word);
    }
  }
  return tokens;
}

// ---------------------------------------------------------------------------
// Test
// ---------------------------------------------------------------------------

test("README package symbols are all exported from src/index.ts", () => {
  const readme = fs.readFileSync(README_PATH, "utf8");
  const indexSrc = fs.readFileSync(INDEX_PATH, "utf8");

  const blocks = extractTsBlocks(readme);
  expect(
    blocks.length,
    "README.md must contain at least one ```ts block"
  ).toBeGreaterThan(0);

  const exports = parseExports(indexSrc);
  expect(
    exports.size,
    "src/index.ts must export at least one symbol"
  ).toBeGreaterThan(0);

  // Collect every uppercase token mentioned in README TS blocks.
  const mentionedSymbols = new Set<string>();
  for (const block of blocks) {
    for (const tok of extractUppercaseTokens(block)) {
      mentionedSymbols.add(tok);
    }
  }

  // Assert each mentioned symbol is either exported or allow-listed.
  // (allow-listed tokens never reach mentionedSymbols — filtered above.)
  const missing: string[] = [];
  for (const sym of mentionedSymbols) {
    if (!exports.has(sym)) {
      missing.push(sym);
    }
  }

  if (missing.length > 0) {
    throw new Error(
      `README mentions ${missing.length} symbol(s) not exported by src/index.ts:\n` +
        missing.map((s) => `  - ${s}`).join("\n") +
        "\n\nAll exports: " +
        [...exports].sort().join(", ")
    );
  }

  expect(missing).toHaveLength(0);
});
