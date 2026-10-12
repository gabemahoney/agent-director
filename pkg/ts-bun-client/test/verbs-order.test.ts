/**
 * verbs-order.test.ts — the hand-authored VERBS list equals the callable verbs of
 * the manifest's surface.json, same names in the same order (a new verb lands in both).
 */

import { test, expect } from "bun:test";
import * as fs from "node:fs";
import * as path from "node:path";

import { VERBS } from "../src/internal/verbs.js";

const surfacePath = path.resolve(import.meta.dir, "../../api/manifest/surface.json");

test("VERBS equals surface.json's callable verbs, in order", () => {
  const surface = JSON.parse(fs.readFileSync(surfacePath, "utf8")) as { verbs: { name: string; callable: boolean }[] };
  const callable = surface.verbs.filter((v) => v.callable).map((v) => v.name);

  expect(callable.length).toBeGreaterThan(0);
  expect([...VERBS] as string[]).toEqual(callable);
});
