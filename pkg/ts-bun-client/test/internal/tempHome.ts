/**
 * withTempHome — per-test HOME isolation. Runs testFn with HOME a fresh temp
 * dir and test/fake-tmux first on PATH (restored afterwards). Removes the dir
 * on success; on failure keeps it and prints its path for inspection.
 */

import * as os from "os";
import * as path from "path";
import * as fs from "fs";
import { withProcessEnv } from "./helper.js";

export async function withTempHome(testFn: (homeDir: string) => Promise<void>): Promise<void> {
  const homeDir = fs.mkdtempSync(path.join(os.tmpdir(), "agentdirector-bun-test-"));
  // FAKE_TMUX_DIR is set by setup.ts; the fallback serves direct runs without the preload.
  const fakeTmuxDir = process.env.FAKE_TMUX_DIR ?? path.resolve(import.meta.dir, "../../../../test/fake-tmux");
  const env = { HOME: homeDir, PATH: `${fakeTmuxDir}:${process.env.PATH ?? "/usr/local/bin:/usr/bin:/bin"}` };
  try {
    await withProcessEnv(env, () => testFn(homeDir));
  } catch (err) {
    console.error(`[withTempHome] test failed; temp HOME preserved at: ${homeDir}`);
    throw err;
  }
  try {
    fs.rmSync(homeDir, { recursive: true, force: true });
  } catch {
    // best-effort: a passing test never fails on cleanup
  }
}
