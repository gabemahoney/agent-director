/**
 * release-bun-test-wiring.test.ts — b.bwn: the coverage.bun-test release gate
 * (skills/release-agent-director/gates/coverage/bun-test.sh) installs with
 * `--frozen-lockfile` and runs `bun test`, so this suite gates every release;
 * the wrong-shape CI workflow .github/workflows/ts-client-test.yml stays absent.
 */

import { test, expect } from "bun:test";
import * as fs from "node:fs";
import * as path from "node:path";

const REPO_ROOT = path.resolve(import.meta.dir, "../../..");

test("bun-test.sh gate invokes bun install --frozen-lockfile and bun test", () => {
  const src = fs.readFileSync(path.join(REPO_ROOT, "skills/release-agent-director/gates/coverage/bun-test.sh"), "utf8");
  expect(src).toContain("bun install --frozen-lockfile");
  expect(src).toContain("bun test");
});

test(".github/workflows/ts-client-test.yml does not exist (wrong-gate redirect)", () => {
  expect(fs.existsSync(path.join(REPO_ROOT, ".github/workflows/ts-client-test.yml"))).toBe(false);
});
