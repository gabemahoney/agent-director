/**
 * Asserts that the fake-tmux stub has the executable bit set after setup.ts runs.
 *
 * Regression guard for b.or3: if the stub lands at mode 644, Go's exec.LookPath
 * silently falls through to /usr/bin/tmux, leaking real tmux sessions that hold
 * fixed session names. Later test runs then find those names held: resume refuses
 * with a classified error (ErrTmuxSessionConflict for a holder with no valid
 * instance id), and spawn and kill see a name held by a session that is not theirs.
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
