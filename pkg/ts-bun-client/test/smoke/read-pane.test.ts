/**
 * Smoke test — read-pane verb
 *
 * Happy path: seed a working spawn, call readPane. The fake-tmux stub's
 * capture-pane handler writes "fake pane line one\nfake pane line two\n"
 * (or $FAKE_TMUX_PANE_OUTPUT if set) to stdout, so result.pane is non-empty.
 *
 * Error path: unknown id → ErrSpawnNotFound.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper } from "../internal/helper.js";
import { Client, ErrSpawnNotFound, AgentDirectorError } from "../../src/index.js";
import type { ReadPaneResult } from "../../src/index.js";

// Pass tmuxCommand explicitly — the FFI worker's PATH snapshot does not reflect
// changes made by withTempHome in the main thread after worker spawn.
const fakeTmuxBin = path.join(
  process.env.FAKE_TMUX_DIR ?? path.resolve(import.meta.dir, "../../../../test/fake-tmux"),
  "tmux"
);

const BOGUS_ID = "smoke-bogus-id-does-not-exist";

test("read-pane: happy path — returns pane text from fake-tmux stub", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, "state.db");
    const spawnId = "smoke-read-pane-id";

    runHelper("seed-spawn", {
      store: storePath,
      state: "working",
      id: spawnId,
      "create-store": true,
    });

    using client = new Client({ storePath, createIfMissing: true, tmuxCommand: fakeTmuxBin });
    const result: ReadPaneResult = await client.readPane({
      claude_instance_id: spawnId,
      n_lines: 5,
    });
    expect(typeof result.pane).toBe("string");
    // fake-tmux returns deterministic stub text; just assert non-empty.
    expect(result.pane.length).toBeGreaterThan(0);
  });
}, 10_000);

test("read-pane: error — unknown id → ErrSpawnNotFound", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, "state.db");
    using client = new Client({ storePath, createIfMissing: true });

    let caught: unknown;
    try {
      await client.readPane({ claude_instance_id: BOGUS_ID });
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(ErrSpawnNotFound);
    expect(caught).toBeInstanceOf(AgentDirectorError);
    expect(caught).toBeInstanceOf(Error);
  });
}, 10_000);
