import { test, expect } from "bun:test";
import * as fs from "node:fs";
import * as path from "node:path";
import { ErrTooManyIds, type GetManyResult } from "../../src/index.js";
import { withTempHome } from "../internal/tempHome.js";
import { homeStore, openClient, rejection, runHelper } from "../internal/helper.js";

test("get-many: typed rows, missing ids, empty arrays and next-call config", async () => {
  await withTempHome(async (home) => {
    const store = homeStore(home);
    const ids = Array.from({ length: 21 }, (_, i) => `many-${i}`);
    runHelper("seed-spawn", { store, id: ids[0]!, state: "ended", "create-store": true });
    using client = await openClient(store);
    const mixed: GetManyResult = await client.getMany({ instance_ids: ids.slice(0, 2) });
    expect(mixed.spawns.map((row) => [row.instance_id, row.state])).toEqual([[ids[0], "ended"]]);
    expect(mixed.not_found).toEqual([ids[1]!]);
    expect((await client.getMany({ instance_ids: [ids[0]!] })).not_found).toEqual([]);
    expect(await client.getMany({ instance_ids: [ids[1]!] })).toEqual({ spawns: [], not_found: [ids[1]!] });
    const over = await rejection(client.getMany({ instance_ids: ids }));
    expect(over).toBeInstanceOf(ErrTooManyIds);
    expect((over as ErrTooManyIds).errDetails).toEqual({ id_cap: 20 });
    for (const cap of [2, 1]) {
      fs.writeFileSync(path.join(home, ".config", "agent-director", "config.toml"), `[get_many]\nid_cap = ${cap}\n`);
      const err = await rejection(client.getMany({ instance_ids: ids.slice(0, cap + 1) }));
      expect(err).toBeInstanceOf(ErrTooManyIds);
      expect((err as ErrTooManyIds).errDetails).toEqual({ id_cap: cap });
      const result = await client.getMany({ instance_ids: ids.slice(0, cap) });
      expect(result.spawns.map((row) => row.instance_id)).toEqual([ids[0]!]);
      expect(result.not_found).toEqual(ids.slice(1, cap));
    }
  });
}, 15_000);
