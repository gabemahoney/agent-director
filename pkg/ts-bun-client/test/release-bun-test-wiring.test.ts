/**
 * release-bun-test-wiring.test.ts — static-assertion anchor for b.bwn.
 *
 * Regression guarded: verify_phase() in release.sh must invoke `bun test`
 * so that the m61-surface regression tests (verify-installed-pkg.test.ts)
 * are gated on every release. If the step is removed, this file fails —
 * re-opening the gap b.bwn was filed to close.
 *
 * Also anchors the redirect: .github/workflows/ts-client-test.yml must NOT
 * exist. Re-introducing the wrong-shape CI gate while removing the release
 * hook would pass a future CI run but silently skip the release gate.
 */

import { test, expect, describe } from "bun:test";
import * as fs from "node:fs";
import * as path from "node:path";

const REPO_ROOT = path.resolve(import.meta.dir, "../../..");
const RELEASE_SH = path.join(REPO_ROOT, "skills/release-agent-director/release.sh");
const WRONG_WORKFLOW = path.join(REPO_ROOT, ".github/workflows/ts-client-test.yml");

describe("release.sh bun-test wiring (b.bwn)", () => {
  test("verify_phase contains bun install --frozen-lockfile and bun test", () => {
    const src = fs.readFileSync(RELEASE_SH, "utf8");

    // Extract the body of verify_phase() — from its opening line to phase_ok verify.
    // The function body contains nested braces so we can't match on `}` alone;
    // `phase_ok verify` is the canonical end-of-success sentinel.
    const match = src.match(/verify_phase\(\)\s*\{([\s\S]*?)phase_ok verify/);
    expect(match).not.toBeNull();
    const body = match![1];

    expect(body).toContain("bun install --frozen-lockfile");
    expect(body).toContain("bun test");
  });

  test(".github/workflows/ts-client-test.yml does not exist (wrong-gate redirect)", () => {
    expect(fs.existsSync(WRONG_WORKFLOW)).toBe(false);
  });
});
