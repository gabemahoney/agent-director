import { test, expect } from "bun:test";
import { Database } from "bun:sqlite";
import { withTempHome } from "./internal/tempHome.js";
import { runHelper, homeStore, openClient } from "./internal/helper.js";

// b.ivj D2/A2: the client preserves explicit nulls and Cursor text through its actual CLI transport.
test("failure_reason survives get/getMany/status transport and remains absent from list", async () => {
  await withTempHome(async (home) => {
    const store = homeStore(home);
    const cases = [
      { id: "cursor-failed", agent: "cursor", recorded: true, want: "runtime internal error" },
      { id: "cursor-legacy", agent: "cursor", recorded: false, want: null },
      { id: "claude", agent: "claude", recorded: true, want: null },
      { id: "codex", agent: "codex", recorded: true, want: null },
    ];
    for (const row of cases) {
      runHelper("seed-spawn", { store, id: row.id, agent: row.agent, state: "missing", "create-store": true });
    }
    // Extend ts-helper's real seeded rows with the optional metadata under their own identities.
    using db = new Database(store);
    for (const row of cases) {
      db.query(`UPDATE spawns SET state_evidence = CASE WHEN ? THEN
        json_object('v', state, 'life', life_number, 'token', launch_token,
          'epoch', permission_epoch, 'at', 1000, 'src', 'process',
          'failure_reason', 'runtime internal error') ELSE NULL END WHERE instance_id = ?`).run(row.recorded ? 1 : 0, row.id);
    }
    using client = await openClient(store);
    for (let repeat = 0; repeat < 2; repeat++) {
      const many = await client.getMany({ instance_ids: cases.map((row) => row.id) });
      expect(many.not_found).toEqual([]);
      expect(many.spawns).toHaveLength(cases.length);
      for (const [i, row] of cases.entries()) {
        const get = await client.get({ instance_id: row.id });
        const status = await client.status({ instance_id: row.id });
        for (const read of [get, many.spawns[i]!, status]) {
          expect(Object.hasOwn(read, "failure_reason")).toBe(true);
          expect(read.failure_reason).toBe(row.want);
          expect(read.confirmations.state.reason).toBe(row.recorded ? null : "no_evidence");
        }
        expect(get.agent).toBe(row.agent);
      }
    }
    const listed = await client.list({});
    expect(listed.spawns).toHaveLength(cases.length);
    for (const row of listed.spawns) expect(row).not.toHaveProperty("failure_reason");
  });
}, 30_000);
