/**
 * Smoke test — list-subagents verb
 *
 * Happy path: a tracked row's records with every SubagentRecord key, nulls
 * included, and the three time encodings as Go writes them (whole second,
 * trailing zero dropped, full milliseconds), in the verb's order (first start
 * ascending, nulls last); a tracked row with no records reads an empty list.
 * Codex returns an untracked empty array. Unknown id → ErrSpawnNotFound.
 */

import { test, expect } from "bun:test";
import { expectedContext } from "../internal/contextFixture.js";

test("list-subagents: captured Claude child retains its own occupancy", async () => {
  await withTempHome(async (homeDir) => {
    const store=homeStore(homeDir);
    runHelper("seed-spawn", {store,id:"context-parent",state:"waiting","create-store":true,"context-fixture":"claude-parent","context-writer-pid":String(process.pid)});
    const child=runHelper("seed-subagent",{store,"instance-id":"context-parent","context-fixture":"claude-child"});
    using client=await openClient(store);
    const result=await client.listSubagents({instance_id:"context-parent"});
    expect(result.tracked).toBe(true);
    expect(result.subagents).toHaveLength(1);
    expect(result.subagents[0].subagent_id).toBe(child.subagent_id as string);
    expect(result.subagents[0].context).toEqual(expectedContext("claude-child"));
  });
}, 10_000);
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, homeStore, openClient } from "../internal/helper.js";
import { ErrSpawnNotFound } from "../../src/index.js";
import type { SubagentRecord } from "../../src/index.js";

/** Seeds a working row in a temp HOME's store and returns the store path. */
function seedRow(homeDir: string, id: string): string {
  const store = homeStore(homeDir);
  runHelper("seed-spawn", { store, id, state: "working", "create-store": true });
  return store;
}

test("list-subagents: happy path — every record key, nulls and the three time encodings, in order", async () => {
  await withTempHome(async (homeDir) => {
    const id = "smoke-list-subagents-id";
    const store = seedRow(homeDir, id);
    // Seeded null-first so the verb's ordering (nulls last), not insertion order, puts the full record first.
    runHelper("seed-subagent", { store, "instance-id": id, "subagent-id": "sa-nulls" });
    runHelper("seed-subagent", {
      store, "instance-id": id, "subagent-id": "sa-full", state: "lost", "close-reason": "agent_gone",
      "session-id": "sess-1", type: "general-purpose", description: "find the bug", model: "model-1",
      "parent-type": "agent", "parent-id": id, "tool-use-id": "toolu-1", "run-mode": "background",
      "first-started-at-ms": "1790000000000", "started-at-ms": "1790000000120",
      "stopped-at-ms": "1790000000456", "lost-at-ms": "1790000001007",
    });

    using client = await openClient(store);
    const result = await client.listSubagents({ instance_id: id });

    const full: SubagentRecord = {
      context: expectedContext("unknown"), subagent_id: "sa-full", instance_id: id, state: "lost", session_id: "sess-1",
      subagent_type: "general-purpose", description: "find the bug", model: "model-1",
      parent_type: "agent", parent_id: id, tool_use_id: "toolu-1", run_mode: "background",
      close_reason: "agent_gone", first_started_at: "2026-09-21T14:13:20Z",
      started_at: "2026-09-21T14:13:20.12Z", stopped_at: "2026-09-21T14:13:20.456Z",
      lost_at: "2026-09-21T14:13:21.007Z",
    };
    const nulls: SubagentRecord = {
      context: expectedContext("unknown"), subagent_id: "sa-nulls", instance_id: id, state: "running", session_id: null, subagent_type: null,
      description: null, model: null, parent_type: null, parent_id: null, tool_use_id: null, run_mode: null,
      close_reason: null, first_started_at: null, started_at: null, stopped_at: null, lost_at: null,
    };
    expect(result).toEqual({ tracked: true, subagents: [full, nulls] });
  });
}, 10_000);

test("list-subagents: a tracked row with no records reads an empty list", async () => {
  await withTempHome(async (homeDir) => {
    const store = seedRow(homeDir, "smoke-list-subagents-empty-id");
    using client = await openClient(store);
    expect(await client.listSubagents({ instance_id: "smoke-list-subagents-empty-id" }))
      .toEqual({ tracked: true, subagents: [] });
  });
}, 10_000);

test("list-subagents: a Codex row returns an untracked empty array", async () => {
  await withTempHome(async (homeDir) => {
    const id = "smoke-list-subagents-codex-id";
    const store = homeStore(homeDir);
    runHelper("seed-spawn", {
      store, id, state: "working", "create-store": true,
      agent: "codex", "no-subagent-tracking": true,
    });
    using client = await openClient(store);
    expect(await client.listSubagents({ instance_id: id }))
      .toEqual({ tracked: false, subagents: [] });
  });
}, 10_000);

test("list-subagents: error — unknown id → ErrSpawnNotFound", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    await expect(client.listSubagents({ instance_id: "smoke-bogus-id" })).rejects.toBeInstanceOf(ErrSpawnNotFound);
  });
}, 10_000);
