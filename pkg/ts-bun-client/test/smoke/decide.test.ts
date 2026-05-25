/**
 * Smoke test — decide verb
 *
 * Happy path: seed a spawn in check_permission state with relay_mode=on and an
 * open permission request; call decide with decision="allow".
 *
 * Error path: invalid decision string "maybe" → ErrInvalidDecision. This error
 * is validated before any store lookup, so no special seeding is required.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper } from "../internal/helper.js";
import { Client, ErrInvalidDecision, AgentDirectorError } from "../../src/index.js";

test("decide: happy path — allows an open permission request", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    const spawnId = "smoke-decide-id";

    // Seed spawn in check_permission state with relay_mode=on (required by decide).
    runHelper("seed-spawn", {
      store: storePath,
      state: "check_permission",
      id: spawnId,
      "relay-mode": "on",
      "create-store": true,
    });

    // Seed an open permission request for the spawn.
    runHelper("seed-permission-request", {
      store: storePath,
      "spawn-id": spawnId,
      tool: "Bash",
    });

    using client = new Client({ storePath, createIfMissing: true });
    const result = await client.decide({
      claude_instance_id: spawnId,
      decision: "allow",
    });
    // DecideResult is an empty object.
    expect(typeof result).toBe("object");
  });
}, 10_000);

test("decide: error — invalid decision string → ErrInvalidDecision", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    using client = new Client({ storePath, createIfMissing: true });

    let caught: unknown;
    try {
      // "maybe" is not "allow" or "deny" → triggers ErrInvalidDecision
      // before any store lookup, so no seeded row is needed.
      await client.decide({
        claude_instance_id: "any-id",
        decision: "maybe" as "allow",
      });
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(ErrInvalidDecision);
    expect(caught).toBeInstanceOf(AgentDirectorError);
    expect(caught).toBeInstanceOf(Error);
  });
}, 10_000);
