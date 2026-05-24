/**
 * withTempHome — per-test HOME isolation for smoke tests.
 *
 * Creates a fresh temp directory, sets process.env.HOME to it, and prepends
 * test/fake-tmux/ onto PATH so every `tmux` invocation in the test body (and
 * in the Go FFI layer) hits the recording stub instead of real tmux.
 *
 * On success: removes the temp dir and restores env.
 * On failure: KEEPS the temp dir (prints its path for inspection), re-throws.
 * Always: restores HOME and PATH in the finally block.
 */

import * as os from "os";
import * as path from "path";
import * as fs from "fs";

// fake-tmux binary directory (built by `make fake-tmux`, set in setup.ts).
// Falls back to the relative path from this file so tests can be run without
// going through the preload (e.g. in direct bun calls during development).
function fakeTmuxDir(): string {
  if (process.env.FAKE_TMUX_DIR) return process.env.FAKE_TMUX_DIR;
  // test/internal/tempHome.ts → test/ → pkg/ts-bun-client/ → pkg/ → repo root
  return path.resolve(import.meta.dir, "../../../../test/fake-tmux");
}

/**
 * withTempHome creates a per-test temp HOME, runs testFn, then cleans up.
 *
 * @param testFn - async callback that receives the temp homeDir path.
 * @returns the resolved value of testFn.
 */
export async function withTempHome(
  testFn: (homeDir: string) => Promise<void>
): Promise<void> {
  // Create a fresh isolated temp dir for this test's HOME.
  const homeDir = fs.mkdtempSync(
    path.join(os.tmpdir(), "agentdirector-bun-test-")
  );

  // Save prior env values (undefined when not set).
  const priorHome = process.env.HOME;
  const priorPath = process.env.PATH;

  // Override HOME + prepend fake-tmux directory to PATH.
  process.env.HOME = homeDir;
  const fakeTmux = fakeTmuxDir();
  process.env.PATH = `${fakeTmux}:${priorPath ?? "/usr/local/bin:/usr/bin:/bin"}`;

  let failed = false;
  try {
    await testFn(homeDir);
  } catch (err) {
    failed = true;
    // Keep the temp dir for post-mortem inspection.
    console.error(
      `[withTempHome] test failed; temp HOME preserved at: ${homeDir}`
    );
    throw err;
  } finally {
    // Always restore env vars.
    if (priorHome !== undefined) {
      process.env.HOME = priorHome;
    } else {
      delete process.env.HOME;
    }
    if (priorPath !== undefined) {
      process.env.PATH = priorPath;
    } else {
      delete process.env.PATH;
    }

    // Clean up temp dir only on success.
    if (!failed) {
      try {
        fs.rmSync(homeDir, { recursive: true, force: true });
      } catch {
        // best-effort cleanup; never throw from finally
      }
    }
  }
}
