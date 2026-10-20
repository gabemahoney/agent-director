import { test, expect } from "bun:test";
import * as fs from "fs";
import * as path from "path";
import { withTempHome } from "./internal/tempHome.js";
import { runHelper, homeStore, openClient } from "./internal/helper.js";

const expected = JSON.parse(fs.readFileSync(path.resolve(import.meta.dir, "../../../cmd/agent-director/testdata/stuck/permission.json"), "utf8"));

test("permission stuck details agree across status/get/list and survive a fresh client", async () => {
  await withTempHome(async (home) => {
    const store = homeStore(home);
    runHelper("seed-permission-surface", { store });
    for (const agent of ["claude", "codex", "cursor"]) {
      using client = await openClient(store);
      for (const verb of ["status", "get"] as const) {
        const row = JSON.parse(JSON.stringify(await client[verb]({ instance_id: `stuck-${agent}` })));
        expect(row).toMatchObject({ ...expected, tmux_session_name: `ts-stuck-${agent}` });
        expect(row).not.toHaveProperty("unchanged_minutes");
        if (verb === "get") {
          expect(row.permission_requests).toBeArray();
          expect(row.unproven_requests).toBeArray();
        }
      }
    }
    using client = await openClient(store);
    const { spawns } = await client.list({ state: ["stuck"], limit: 3 });
    expect(spawns).toHaveLength(3);
    for (const row of spawns) {
      expect(row).toMatchObject({ state: "stuck", stuck_reason: "permission_unanswered" });
      for (const key of ["since", "request_token", "tool_name", "unchanged_minutes"]) expect(row).not.toHaveProperty(key);
    }
    const ordinary = await client.status({ instance_id: "ordinary" });
    for (const key of Object.keys(expected).filter((key) => key !== "state")) expect(ordinary).not.toHaveProperty(key);
  });
}, 60_000);
