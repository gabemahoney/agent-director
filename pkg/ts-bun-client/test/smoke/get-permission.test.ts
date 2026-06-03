/**
 * Smoke test — get-permission verb
 *
 * Happy path: seed a spawn in check_permission state with an open permission
 * request; call getPermission with the seeded request_token, assert that the
 * result fields are populated and the decision fields are null (open row).
 *
 * Error path: unknown request_token → ErrPermissionRequestNotFound. No DB
 * seeding is required since the token is simply absent.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper } from "../internal/helper.js";
import { Client, ErrPermissionRequestNotFound, AgentDirectorError } from "../../src/index.js";

const MISSING_TOKEN = "00000000-0000-0000-0000-000000000000";

test("get-permission: happy path — returns open permission request row", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    const spawnId = "smoke-gp-id";

    // Seed spawn in check_permission state with relay_mode=on.
    runHelper("seed-spawn", {
      store: storePath,
      state: "check_permission",
      id: spawnId,
      "relay-mode": "on",
      "create-store": true,
    });

    // Seed an open permission request; capture the request_token.
    const seed = runHelper("seed-permission-request", {
      store: storePath,
      "spawn-id": spawnId,
      tool: "Bash",
    });
    const requestToken = seed["request_token"] as string;

    using client = await Client.create({ storePath, createIfMissing: true , _cliPath: process.env.CLI_PATH } as any);
    const result = await client.getPermission({ request_token: requestToken });

    // Token echoed back.
    expect(result.request_token).toBe(requestToken);
    // Tool name matches what was seeded.
    expect(result.tool_name).toBe("Bash");
    // Open row: decision fields are null.
    expect(result.decision ?? null).toBeNull();
    expect(result.decision_reason ?? null).toBeNull();
    expect(result.decided_at ?? null).toBeNull();
    // requested_at is an RFC3339 timestamp string.
    expect(typeof result.requested_at).toBe("string");
    expect(result.requested_at.length).toBeGreaterThan(0);
  });
}, 10_000);

test("get-permission: error — unknown token → ErrPermissionRequestNotFound", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    using client = await Client.create({ storePath, createIfMissing: true , _cliPath: process.env.CLI_PATH } as any);

    let caught: unknown;
    try {
      await client.getPermission({ request_token: MISSING_TOKEN });
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(ErrPermissionRequestNotFound);
    expect(caught).toBeInstanceOf(AgentDirectorError);
    expect(caught).toBeInstanceOf(Error);
  });
}, 10_000);
