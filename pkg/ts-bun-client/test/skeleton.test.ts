import { test, expect } from "bun:test";
import { readFileSync } from "fs";
import { join } from "path";

test("package.json has the expected placeholder name", () => {
  const pkgPath = join(import.meta.dir, "..", "package.json");
  const raw = readFileSync(pkgPath, "utf-8");
  const pkg = JSON.parse(raw) as { name: string };
  expect(pkg.name).toBe("@CHANGEME-H3/agent-director");
});
