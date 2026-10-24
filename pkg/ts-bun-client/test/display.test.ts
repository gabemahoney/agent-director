import { test, expect } from "bun:test";
import * as fs from "fs";
import * as path from "path";
import { withTempHome } from "./internal/tempHome.js";
import { runHelper, homeStore, openClient } from "./internal/helper.js";

const fixtures = path.resolve(import.meta.dir, "../../../cmd/agent-director/testdata/display");
const idleScreen = JSON.parse(fs.readFileSync(path.join(fixtures, "idle_screen.json"), "utf8"));
const surface = JSON.parse(fs.readFileSync(path.resolve(import.meta.dir, "../../api/manifest/surface.json"), "utf8")) as {
  verbs: { name: string; params: { name: string }[]; result_fields: { name: string; allowed_values: string[] | null }[] }[];
};
const verb = (name: string) => surface.verbs.find((v) => v.name === name)!;

/** The idle_screen_availability every row of agent carries. */
const availability = (agent: string) => (agent === "cursor" ? "unavailable" : "available");

const idle = ["idle-screen-claude", "idle-screen-codex"];
const asking = ["needs-intelligence-claude", "needs-intelligence-codex", "needs-intelligence-cursor"];
/** Each seeded row's real state; the asking rows hold a needs_intelligence display a 0.16/0.17 read committed. */
const realState: Record<string, string> = {
  ...Object.fromEntries(idle.map((id) => [id, "working"])),
  ...Object.fromEntries(asking.map((id) => [id, "check_permission"])),
  ordinary: "working",
  "ordinary-cursor": "working",
};
const LIST_KEYS = ["agent", "cwd", "family_state", "idle_screen_availability", "instance_id", "labels", "last_seen_at",
  "permission_requests", "relay_mode", "started_at", "state", "state_changed_at", "tmux_session_name"];

// b.zpv: state is the real state on every read, and a Displayed State shows only in displayed_state.
test("idle_screen shows in displayed_state beside state working, and an open permission request reads check_permission, across status/get/list and fresh clients", async () => {
  await withTempHome(async (home) => {
    const store = homeStore(home);
    runHelper("seed-needs-intelligence-surface", { store });
    runHelper("seed-idle-screen-surface", { store });
    // apitest.SeedIdleScreenSurface: a failed capture's clearing keeps the idle_screen detail under the largest settle time.
    fs.writeFileSync(path.join(home, ".config", "agent-director", "config.toml"), "[state]\nsettle_ms = 60000\n");
    for (const id of idle) {
      using client = await openClient(store);
      const detail = { ...idleScreen, tmux_session_name: `ts-${id}`, idle_screen_availability: "available" };
      const status = JSON.parse(JSON.stringify(await client.status({ instance_id: id })));
      expect(status).toMatchObject(detail);
      // The exact key set: no request detail and no state_changed_at.
      expect(Object.keys(status).sort()).toEqual(["context", "family_state", ...Object.keys(detail)].sort());
      const get = JSON.parse(JSON.stringify(await client.get({ instance_id: id })));
      expect(get).toMatchObject(detail);
      for (const key of ["request_token", "tool_name", "stuck_reason"]) expect(get).not.toHaveProperty(key);
      expect(get.permission_requests).toBeArray();
      expect(get.unproven_requests).toBeArray();
      // b.k6i: the seed's zero stamp, shown as the Unix epoch.
      expect(get.state_changed_at).toBe("1970-01-01T00:00:00Z");
    }
    for (const id of asking) {
      using client = await openClient(store);
      const agent = id.slice("needs-intelligence-".length);
      const status = JSON.parse(JSON.stringify(await client.status({ instance_id: id })));
      expect(status).toMatchObject({ state: "check_permission", idle_screen_availability: availability(agent) });
      expect(Object.keys(status).sort()).toEqual(["context", "family_state", "idle_screen_availability", "state"]);
      const get = JSON.parse(JSON.stringify(await client.get({ instance_id: id })));
      expect(get.state).toBe("check_permission");
      for (const key of ["displayed_state", "since", "request_token", "tool_name", "unchanged_minutes"]) expect(get).not.toHaveProperty(key);
      expect(get.permission_requests).toHaveLength(1);
    }
    using client = await openClient(store);
    // A filter keeps a row whose state or displayed_state it names, before limit.
    for (const [states, ids] of [
      [["idle_screen"], idle],
      [["working"], ["idle-screen-claude", "idle-screen-codex", "ordinary", "ordinary-cursor"]],
      [["check_permission"], asking],
      [["needs_intelligence"], []],
    ] as const) {
      const { spawns } = await client.list({ state: [...states], ...(ids.length ? { limit: ids.length } : {}) });
      expect(spawns.map((row) => row.instance_id).sort()).toEqual([...ids]);
      for (const row of spawns) {
        expect(row).toMatchObject({ state: realState[row.instance_id], idle_screen_availability: availability(row.agent) });
        if (idle.includes(row.instance_id)) {
          // The exact key set: displayed_state and no detail.
          expect(row.displayed_state).toBe("idle_screen");
          expect(Object.keys(row).sort()).toEqual([...LIST_KEYS, "displayed_state"].sort());
        } else {
          expect(row).not.toHaveProperty("displayed_state");
        }
        if (asking.includes(row.instance_id)) expect(Object.keys(row).sort()).toEqual(LIST_KEYS);
      }
    }
    for (const [id, agent] of [["ordinary", "claude"], ["ordinary-cursor", "cursor"]]) {
      const ordinary = await client.status({ instance_id: id });
      expect(ordinary.idle_screen_availability).toBe(availability(agent));
      expect(Object.keys(ordinary).sort()).toEqual(["context", "family_state", "idle_screen_availability", "state"]);
    }
  });
}, 60_000);

test("the manifest declares the lifecycle states, displayed_state, the idle_screen availability values, detail field and spawn override the TypeScript types carry", () => {
  const golden = fs.readFileSync(path.resolve(import.meta.dir, "fixtures/public-surface/types.d.ts.golden"), "utf8");
  for (const name of ["status", "get"]) {
    const fields = Object.fromEntries(verb(name).result_fields.map((f) => [f.name, f.allowed_values]));
    expect(fields.state).toEqual(["pending", "waiting", "working", "ask_user", "check_permission", "ended", "missing"]);
    expect(fields.displayed_state).toEqual(["needs_intelligence", "idle_screen"]);
    expect(fields.idle_screen_availability).toEqual(["available", "unavailable"]);
    expect(fields).toHaveProperty("unchanged_minutes");
  }
  expect(verb("spawn").params.map((p) => p.name)).toContain("idle_screen_timeout_seconds");
  expect(golden).toContain('export type IdleScreenAvailability = "available" | "unavailable";');
  expect(golden).toContain('displayed_state?: "needs_intelligence" | "idle_screen";');
  expect(golden).toContain("idle_screen_timeout_seconds?: number | null;");
});
