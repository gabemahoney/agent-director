/**
 * settle-on.test.ts — b.3ie: with [state] settle_ms set, the TS client's
 * status, get and list report the settled state and nothing else changes: over
 * ts-helper's read-surface store (rows scrambled to read as `settled[state]`)
 * every result is its settle-off golden with only `state` read as settled (no
 * new field, no new state value), and list's state filter and limit apply to
 * the settled state. A row's settle columns placed by ts-helper set-settle read
 * the settled state until the raw one has held settle_ms.
 */

import { test, expect } from "bun:test";
import * as fs from "fs";
import * as path from "path";
import type { Client } from "../src/index.js";
import { withTempHome } from "./internal/tempHome.js";
import { runHelper, homeStore, openClient } from "./internal/helper.js";

const fixtureDir = path.resolve(import.meta.dir, "../../../cmd/agent-director/testdata/settle_off");
const SETTLE_MS = 60_000; // MaxStateSettleMs

interface ReadSurfaceCase {
  name: string;
  verb: "status" | "get" | "list";
  params: { state?: string[]; limit?: number } & Record<string, unknown>;
}

type Row = Record<string, unknown> & { state: string };

const table = JSON.parse(fs.readFileSync(path.join(fixtureDir, "cases.json"), "utf8")) as { cases: ReadSurfaceCase[] };

const golden = (name: string): unknown => JSON.parse(fs.readFileSync(path.join(fixtureDir, "golden", `${name}.json`), "utf8"));

/** Writes homeDir's config.toml with the settle time. */
function writeSettleConfig(homeDir: string): void {
  fs.writeFileSync(path.join(homeDir, ".agent-director", "config.toml"), `[state]\nsettle_ms = ${SETTLE_MS}\n`);
}

/** Runs one case's verb on client. */
function call(client: Client, c: ReadSurfaceCase): Promise<unknown> {
  switch (c.verb) {
    case "status":
      return client.status(c.params as never);
    case "get":
      return client.get(c.params as never);
    case "list":
      return client.list(c.params as never);
  }
}

/** What the settle time makes of case c's golden, given the scramble's settled state per raw state. */
function expected(c: ReadSurfaceCase, settled: Record<string, string>): unknown {
  const asSettled = (row: Row): Row => {
    expect(Object.keys(settled)).toContain(row.state);
    return { ...row, state: settled[row.state]! };
  };
  if (c.verb !== "list") return asSettled(golden(c.name) as Row);
  const all = (golden("list") as { spawns: Row[] }).spawns.map(asSettled);
  const matching = all.filter((r) => c.params.state === undefined || c.params.state.includes(r.state));
  return { spawns: matching.slice(0, c.params.limit ?? matching.length) };
}

test("settle-on: status, get and list give each golden with only the state read as settled", async () => {
  await withTempHome(async (homeDir) => {
    const seeded = runHelper("seed-read-surface-store", { store: homeStore(homeDir) });
    const settled = seeded.settled as Record<string, string>;
    writeSettleConfig(homeDir);
    using client = await openClient(homeStore(homeDir));
    for (const c of table.cases) {
      const got = JSON.parse(JSON.stringify(await call(client, c)));
      expect({ case: c.name, result: got }).toEqual({ case: c.name, result: expected(c, settled) });
    }
  });
}, 60_000);

test("settle-on: a raw change younger than settle_ms reads the settled state, then the raw one", async () => {
  await withTempHome(async (homeDir) => {
    const store = homeStore(homeDir);
    const id = runHelper("seed-spawn", { store, state: "waiting", "create-store": true }).instance_id as string;
    writeSettleConfig(homeDir);
    using client = await openClient(store);
    const listIDs = async (state: string): Promise<string[]> =>
      (await client.list({ state: [state] } as never)).spawns.map((s) => s.instance_id);

    runHelper("set-settle", { store, id, settled: "working", "stamp-ms": String(Date.now()) });
    expect((await client.status({ instance_id: id } as never)).state).toBe("working");
    expect((await client.get({ instance_id: id } as never)).state).toBe("working");
    expect(await listIDs("working")).toEqual([id]);
    expect(await listIDs("waiting")).toEqual([]);

    runHelper("set-settle", { store, id, settled: "working", "stamp-ms": String(Date.now() - SETTLE_MS) });
    expect((await client.status({ instance_id: id } as never)).state).toBe("waiting");
    expect(await listIDs("waiting")).toEqual([id]);
  });
}, 60_000);
