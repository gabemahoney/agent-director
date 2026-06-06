/**
 * release-bun-test-wiring.test.ts — static-assertion anchor for b.bwn.
 *
 * Regression guarded: the coverage.bun-test gate
 * (skills/release-agent-director/gates/coverage/bun-test.sh) must invoke
 * `bun install --frozen-lockfile` and `bun test` so that the m61-surface
 * regression tests (verify-installed-pkg.test.ts) are gated on every release.
 * If the gate is gutted, this file fails — re-opening the gap b.bwn was filed
 * to close.
 *
 * NOTE (E10): release.sh was retired in Epic E10. The bun-test wiring now
 * lives in gates/coverage/bun-test.sh rather than verify_phase() in release.sh.
 * The assertions below target the new gate file.
 *
 * Also anchors the redirect: .github/workflows/ts-client-test.yml must NOT
 * exist. Re-introducing the wrong-shape CI gate while removing the release
 * hook would pass a future CI run but silently skip the release gate.
 */

import { test, expect, describe } from "bun:test";
import * as fs from "node:fs";
import * as path from "node:path";

const REPO_ROOT = path.resolve(import.meta.dir, "../../..");
const BUN_TEST_GATE = path.join(
  REPO_ROOT,
  "skills/release-agent-director/gates/coverage/bun-test.sh"
);
const WRONG_WORKFLOW = path.join(REPO_ROOT, ".github/workflows/ts-client-test.yml");

describe("coverage.bun-test gate wiring (b.bwn)", () => {
  test("bun-test.sh gate invokes bun install --frozen-lockfile and bun test", () => {
    const src = fs.readFileSync(BUN_TEST_GATE, "utf8");
    expect(src).toContain("bun install --frozen-lockfile");
    expect(src).toContain("bun test");
  });

  test(".github/workflows/ts-client-test.yml does not exist (wrong-gate redirect)", () => {
    expect(fs.existsSync(WRONG_WORKFLOW)).toBe(false);
  });
});
