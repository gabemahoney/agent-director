/**
 * readme-snippets.test.ts — README.md's ```ts blocks against the package:
 * every package-like symbol they mention is exported by src/index.ts, and the
 * blocks typecheck (`tsc --noEmit`) with every export in scope.
 */

import { test, expect } from "bun:test";
import * as fs from "fs";
import * as path from "path";

const PKG_DIR = path.resolve(import.meta.dir, "..");
const README = fs.readFileSync(path.join(PKG_DIR, "README.md"), "utf8");
const BLOCKS = [...README.matchAll(/```(?:ts|typescript)\n([\s\S]*?)```/g)].map((m) => m[1]!);

/** Names in src/index.ts's `export { … }` / `export type { … }` lists. */
const EXPORTS = new Set(
  [...fs.readFileSync(path.join(PKG_DIR, "src/index.ts"), "utf8").matchAll(/export\s+(?:type\s+)?{([^}]+)}/g)]
    .flatMap((m) => m[1]!.split("\n"))
    .flatMap((line) => line.replace(/\/\/.*$/, "").split(","))
    .map((piece) => piece.trim().split(/\s+/)[0]!)
    .filter((name) => /^[A-Za-z_$]/.test(name))
);

/** Uppercase tokens that are JS/TS/runtime built-ins or prose, not package exports. */
const NOT_EXPORTS = new Set(
  ("Array BigInt Boolean Buffer Console Date Error Function JSON Map Math Number Object Promise Proxy RegExp Set " +
    "String Symbol URL WeakMap WeakSet Awaited Exclude Extract InstanceType NonNullable Omit Partial Pick Parameters " +
    "Readonly Record Required ReturnType Bun NodeJS TextDecoder TextEncoder CLI FFI ID SQLite").split(" ")
);

test("README package symbols are all exported from src/index.ts", () => {
  expect(BLOCKS.length).toBeGreaterThan(0);
  const mentioned = new Set(BLOCKS.flatMap((b) => [...b.matchAll(/\b([A-Z][A-Za-z0-9_$]*)\b/g)].map((m) => m[1]!)));
  const missing = [...mentioned].filter((s) => !NOT_EXPORTS.has(s) && !EXPORTS.has(s));
  expect(missing, `README mentions symbols src/index.ts does not export: ${missing.join(", ")}`).toEqual([]);
});

test("README TS snippets typecheck", () => {
  // Each snippet runs in its own block, its imports replaced by one import of every export.
  const body = BLOCKS.map((code, i) =>
    `  // === README snippet ${i + 1} ===\n  {\n` +
    code.split("\n").filter((l) => !l.trimStart().startsWith("import ")).map((l) => (l.trim() ? `    ${l}` : "")).join("\n") +
    `\n  }`
  ).join("\n");
  const source = [
    `import { ${[...EXPORTS].join(", ")} } from "../src/index.js";`,
    "declare const binaryVersion: string; // the version-floor example's captured value",
    "async function _readme() {",
    "  const client = null as unknown as Client; // for snippets that omit construction",
    "  void client;",
    body,
    "}",
    "void _readme;",
  ].join("\n");

  // Written beside this file so ../src/index.js resolves.
  const tmpFile = path.join(import.meta.dir, `tmp-readme-check-${Date.now()}.ts`);
  try {
    fs.writeFileSync(tmpFile, source, "utf8");
    const r = Bun.spawnSync(
      [path.join(PKG_DIR, "node_modules/.bin/tsc"), "--noEmit", "--ignoreConfig", "--strict", "--target", "ES2022",
        "--module", "ESNext", "--moduleResolution", "bundler", "--lib", "ES2022,ESNext.Disposable",
        "--types", "bun-types", "--skipLibCheck", tmpFile],
      { cwd: PKG_DIR }
    );
    const out = `${new TextDecoder().decode(r.stdout)}${new TextDecoder().decode(r.stderr)}`;
    expect(r.exitCode, `README TS snippets failed typecheck:\n${out}\nsource:\n${source}`).toBe(0);
  } finally {
    fs.rmSync(tmpFile, { force: true });
  }
});
