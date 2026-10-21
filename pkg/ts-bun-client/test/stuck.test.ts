import { test, expect } from "bun:test";
import * as fs from "fs";
import * as path from "path";
import { withTempHome } from "./internal/tempHome.js";
import { runHelper, homeStore, openClient } from "./internal/helper.js";

const fixtures = path.resolve(import.meta.dir, "../../../cmd/agent-director/testdata/stuck");
const permission = JSON.parse(fs.readFileSync(path.join(fixtures, "permission.json"), "utf8"));
const pane = JSON.parse(fs.readFileSync(path.join(fixtures, "pane.json"), "utf8"));
const surface = JSON.parse(fs.readFileSync(path.resolve(import.meta.dir, "../../api/manifest/surface.json"), "utf8")) as {
  verbs: { name: string; params: { name: string }[]; result_fields: { name: string; allowed_values: string[] | null }[] }[];
};
const verb = (name: string) => surface.verbs.find((v) => v.name === name)!;

/** The no_screen_change_availability every row of agent carries. */
const availability = (agent: string) => (agent === "cursor" ? "unavailable" : "available");

test("stuck details of both reasons and availability agree across status/get/list and survive a fresh client", async () => {
  await withTempHome(async (home) => {
    const store = homeStore(home);
    runHelper("seed-permission-surface", { store });
    runHelper("seed-pane-surface", { store });
    // apitest.SeedPaneSurface: a failed capture's clearing keeps the pane detail under the largest settle time.
    fs.writeFileSync(path.join(home, ".config", "agent-director", "config.toml"), "[state]\nsettle_ms = 60000\n");
    const surfaces = [
      { prefix: "stuck-", agents: ["claude", "codex", "cursor"], expected: permission, absent: ["unchanged_minutes"] },
      { prefix: "pane-", agents: ["claude", "codex"], expected: pane, absent: ["request_token", "tool_name"] },
    ];
    for (const { prefix, agents, expected, absent } of surfaces) {
      for (const agent of agents) {
        using client = await openClient(store);
        for (const verb of ["status", "get"] as const) {
          const row = JSON.parse(JSON.stringify(await client[verb]({ instance_id: `${prefix}${agent}` })));
          expect(row).toMatchObject({ ...expected, tmux_session_name: `ts-${prefix}${agent}`, no_screen_change_availability: availability(agent) });
          for (const key of absent) expect(row).not.toHaveProperty(key);
          if (verb === "get") {
            expect(row.permission_requests).toBeArray();
            expect(row.unproven_requests).toBeArray();
            // b.k6i: the seed's zero stamp, shown as the Unix epoch.
            expect(row.state_changed_at).toBe("1970-01-01T00:00:00Z");
          } else {
            expect(row).not.toHaveProperty("state_changed_at");
          }
        }
      }
    }
    using client = await openClient(store);
    const { spawns } = await client.list({ state: ["stuck"], limit: 5 });
    expect(spawns).toHaveLength(5);
    for (const row of spawns) {
      const reason = row.instance_id.startsWith("pane-") ? pane.stuck_reason : permission.stuck_reason;
      expect(row).toMatchObject({ state: "stuck", stuck_reason: reason, state_changed_at: "1970-01-01T00:00:00Z", no_screen_change_availability: availability(row.agent) });
      for (const key of ["since", "request_token", "tool_name", "unchanged_minutes"]) expect(row).not.toHaveProperty(key);
    }
    for (const [id, agent] of [["ordinary", "claude"], ["pane-cursor", "cursor"]]) {
      const ordinary = await client.status({ instance_id: id });
      expect(ordinary.no_screen_change_availability).toBe(availability(agent));
      for (const key of [...Object.keys(permission), ...Object.keys(pane)].filter((key) => key !== "state")) expect(ordinary).not.toHaveProperty(key);
    }
  });
}, 60_000);

test("the manifest declares the stuck reasons, availability values and the spawn override the TypeScript types carry", () => {
  const golden = fs.readFileSync(path.resolve(import.meta.dir, "fixtures/public-surface/types.d.ts.golden"), "utf8");
  for (const name of ["status", "get"]) {
    const fields = Object.fromEntries(verb(name).result_fields.map((f) => [f.name, f.allowed_values]));
    expect(fields.stuck_reason).toEqual(["permission_unanswered", "no_screen_change"]);
    expect(fields.no_screen_change_availability).toEqual(["available", "unavailable"]);
    expect(fields).toHaveProperty("unchanged_minutes");
  }
  expect(verb("spawn").params.map((p) => p.name)).toContain("no_screen_change_timeout_seconds");
  expect(golden).toContain('export type StuckReason = "permission_unanswered" | "no_screen_change";');
  expect(golden).toContain('export type NoScreenChangeAvailability = "available" | "unavailable";');
  expect(golden).toContain("no_screen_change_timeout_seconds?: number | null;");
});
