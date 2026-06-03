/**
 * dev-stamp.test.ts — SR-8.3a: the live in-repo CLI binary built by setup.ts
 * must stamp the dev sentinel literal `0.0.0-dev` in its `version --json`
 * output's `version` field (byte-exact, no normalization).
 *
 * This exercises the dev-side mandate of SR-2.6: every non-tagged-release
 * build (developer machine, CI on branches, contributor checkout) stamps
 * the sentinel, not git-describe output and not a synthesized version.
 *
 * The test runs only when CLI_PATH is set (setup.ts preload ran) and only
 * on linux-x64 / darwin-arm64.
 */

import { test, expect } from "bun:test";
import { existsSync } from "node:fs";

const platformTuple = (() => {
  if (process.platform === "linux" && process.arch === "x64") return "linux-x64";
  if (process.platform === "darwin" && process.arch === "arm64") return "darwin-arm64";
  return null;
})();

const cliPath = process.env["CLI_PATH"];

test("CLI dev build stamps version='0.0.0-dev' byte-exact", () => {
  if (platformTuple === null) {
    console.log(
      `dev-stamp.test.ts: unsupported platform ${process.platform}/${process.arch} — skipping`,
    );
    return;
  }
  if (!cliPath || !existsSync(cliPath)) {
    console.log(
      "dev-stamp.test.ts: CLI_PATH not set or binary absent — skipping",
    );
    return;
  }

  const proc = Bun.spawnSync([cliPath, "version", "--json"]);
  expect(proc.exitCode).toBe(0);

  const stdout = new TextDecoder().decode(proc.stdout).trim();
  const parsed = JSON.parse(stdout) as Record<string, unknown>;

  expect(typeof parsed.version).toBe("string");
  // Byte-exact equality — no normalization, no leading "v", no
  // git-describe suffix.
  expect(parsed.version).toBe("0.0.0-dev");
});
