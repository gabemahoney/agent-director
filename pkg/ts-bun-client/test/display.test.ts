import { test, expect } from "bun:test";
import * as fs from "fs";
import * as path from "path";
import { withTempHome } from "./internal/tempHome.js";
import { runHelper, homeStore, openClient } from "./internal/helper.js";

const fixtures = path.resolve(import.meta.dir, "../../../cmd/agent-director/testdata/display");
const needsIntelligence = JSON.parse(fs.readFileSync(path.join(fixtures, "needs_intelligence.json"), "utf8"));
const idleScreen = JSON.parse(fs.readFileSync(path.join(fixtures, "idle_screen.json"), "utf8"));
const surface = JSON.parse(fs.readFileSync(path.resolve(import.meta.dir, "../../api/manifest/surface.json"), "utf8")) as {
  verbs: { name: string; params: { name: string }[]; result_fields: { name: string; allowed_values: string[] | null }[] }[];
};
const verb = (name: string) => surface.verbs.find((v) => v.name === name)!;

/** The idle_screen_availability every row of agent carries. */
const availability = (agent: string) => (agent === "cursor" ? "unavailable" : "available");

test("both Displayed States' details and availability agree across status/get/list and survive a fresh client", async () => {
  await withTempHome(async (home) => {
    const store = homeStore(home);
    runHelper("seed-needs-intelligence-surface", { store });
    runHelper("seed-idle-screen-surface", { store });
    // apitest.SeedIdleScreenSurface: a failed capture's clearing keeps the idle_screen detail under the largest settle time.
    fs.writeFileSync(path.join(home, ".config", "agent-director", "config.toml"), "[state]\nsettle_ms = 60000\n");
    const surfaces = [
      { prefix: "needs-intelligence-", agents: ["claude", "codex", "cursor"], expected: needsIntelligence, absent: ["unchanged_minutes"] },
      { prefix: "idle-screen-", agents: ["claude", "codex"], expected: idleScreen, absent: ["request_token", "tool_name"] },
    ];
    for (const { prefix, agents, expected, absent } of surfaces) {
      for (const agent of agents) {
        using client = await openClient(store);
        const id = `${prefix}${agent}`;
        const detail = { ...expected, tmux_session_name: `ts-${id}`, idle_screen_availability: availability(agent) };
        const status = JSON.parse(JSON.stringify(await client.status({ instance_id: id })));
        expect(status).toMatchObject(detail);
        // The exact key set: no other state's detail and no state_changed_at.
        expect(Object.keys(status).sort()).toEqual(["context", "family_state", ...Object.keys(detail)].sort());
        const get = JSON.parse(JSON.stringify(await client.get({ instance_id: id })));
        expect(get).toMatchObject(detail);
        for (const key of [...absent, "stuck_reason"]) expect(get).not.toHaveProperty(key);
        expect(get.permission_requests).toBeArray();
        expect(get.unproven_requests).toBeArray();
        // b.k6i: the seed's zero stamp, shown as the Unix epoch.
        expect(get.state_changed_at).toBe("1970-01-01T00:00:00Z");
      }
    }
    using client = await openClient(store);
    for (const [state, ids] of [
      ["needs_intelligence", ["needs-intelligence-claude", "needs-intelligence-codex", "needs-intelligence-cursor"]],
      ["idle_screen", ["idle-screen-claude", "idle-screen-codex"]],
    ] as const) {
      // A limit of exactly the matching rows: the filter selects the displayed state before limit.
      const { spawns } = await client.list({ state: [state], limit: ids.length });
      expect(spawns.map((row) => row.instance_id).sort()).toEqual([...ids]);
      for (const row of spawns) {
        expect(row).toMatchObject({ state, state_changed_at: "1970-01-01T00:00:00Z", idle_screen_availability: availability(row.agent) });
        // The exact key set: the state and no detail.
        expect(Object.keys(row).sort()).toEqual(["agent", "cwd", "family_state", "idle_screen_availability", "instance_id", "labels", "last_seen_at",
          "permission_requests", "relay_mode", "started_at", "state", "state_changed_at", "tmux_session_name"]);
      }
    }
    for (const [id, agent] of [["ordinary", "claude"], ["ordinary-cursor", "cursor"]]) {
      const ordinary = await client.status({ instance_id: id });
      expect(ordinary.idle_screen_availability).toBe(availability(agent));
      expect(Object.keys(ordinary).sort()).toEqual(["context", "family_state", "idle_screen_availability", "state"]);
    }
  });
}, 60_000);

test("the manifest declares the idle_screen availability values, detail field and spawn override the TypeScript types carry", () => {
  const golden = fs.readFileSync(path.resolve(import.meta.dir, "fixtures/public-surface/types.d.ts.golden"), "utf8");
  for (const name of ["status", "get"]) {
    const fields = Object.fromEntries(verb(name).result_fields.map((f) => [f.name, f.allowed_values]));
    expect(fields.idle_screen_availability).toEqual(["available", "unavailable"]);
    expect(fields).toHaveProperty("unchanged_minutes");
  }
  expect(verb("spawn").params.map((p) => p.name)).toContain("idle_screen_timeout_seconds");
  expect(golden).toContain('export type IdleScreenAvailability = "available" | "unavailable";');
  expect(golden).toContain("idle_screen_timeout_seconds?: number | null;");
});
