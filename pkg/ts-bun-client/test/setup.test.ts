/**
 * Asserts that the fake-tmux stub has the executable bit set after setup.ts runs.
 *
 * Regression guard for b.or3: if the stub lands at mode 644, Go's exec.LookPath
 * silently falls through to /usr/bin/tmux, leaking real tmux sessions and causing
 * spurious ErrTmuxSessionCreate failures in subsequent resume / kill test runs.
 */

import { test, expect } from "bun:test";
import { statSync } from "fs";
import { resolve } from "path";

test("fake-tmux stub is executable after setup", () => {
  const fakeTmuxDir = process.env.FAKE_TMUX_DIR;
  if (!fakeTmuxDir) {
    throw new Error("FAKE_TMUX_DIR env var not set — is setup.ts loaded as a bun preload via bunfig.toml?");
  }

  const stubPath = resolve(fakeTmuxDir!, "tmux");
  const mode = statSync(stubPath).mode;

  // At least one execute bit (owner, group, or other) must be set.
  expect(mode & 0o111).toBeGreaterThan(0);
});
