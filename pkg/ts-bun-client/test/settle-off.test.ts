/**
 * settle-off.test.ts — b.3ie: with [state] settle_ms unset or 0, the TS
 * client's status, get and list results serialize (JSON.stringify plus a
 * newline) byte for byte to the CLI's goldens in
 * cmd/agent-director/testdata/settle_off, over ts-helper's fixed store
 * (seed-read-surface-store). The case table there drives every surface; each
 * read's own process probe times are normalized as the Go surfaces do.
 */

import { test, expect } from "bun:test";
import * as fs from "fs";
import * as path from "path";
import type { Client } from "../src/index.js";
import { withTempHome } from "./internal/tempHome.js";
import { runHelper, homeStore, openClient } from "./internal/helper.js";
import { normalizeProbes } from "./internal/readSurface.js";

const fixtureDir = path.resolve(import.meta.dir, "../../../cmd/agent-director/testdata/settle_off");

interface ReadSurfaceCase {
  name: string;
  verb: "status" | "get" | "list";
  params: Record<string, unknown>;
}

const table = JSON.parse(fs.readFileSync(path.join(fixtureDir, "cases.json"), "utf8")) as {
  variants: { name: string; config: string | null }[];
  cases: ReadSurfaceCase[];
};

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

for (const variant of table.variants) {
  test(`settle-off (${variant.name}): status, get and list match the CLI goldens byte for byte`, async () => {
    await withTempHome(async (homeDir) => {
      runHelper("seed-read-surface-store", { store: homeStore(homeDir), "writer-pid": String(process.pid) });
      if (variant.config !== null) {
        fs.writeFileSync(path.join(homeDir, ".config", "agent-director", "config.toml"), variant.config);
      }
      using client = await openClient(homeStore(homeDir));
      const first = new Map<string, string>();
      for (const c of table.cases) {
        const from = Date.now();
        const result = await call(client, c);
        const got = normalizeProbes(JSON.stringify(result) + "\n", from, Date.now(), first, String(c.params.instance_id ?? ""));
        const want = fs.readFileSync(path.join(fixtureDir, "golden", `${c.name}.json`), "utf8");
        expect(`${c.name}: ${got}`).toBe(`${c.name}: ${want}`);
      }
      const gets = table.cases.filter((c) => c.verb === "get");
      const ids = [...gets.map((c) => c.params.instance_id as string), "get-many-unknown"];
      const from = Date.now();
      const result = await client.getMany({ instance_ids: ids });
      const got = normalizeProbes(JSON.stringify(result) + "\n", from, Date.now(), first, "");
      const goldens = gets.map((c) => fs.readFileSync(path.join(fixtureDir, "golden", `${c.name}.json`), "utf8").replace(/\n$/, ""));
      expect(got).toBe(`{"spawns":[${goldens.join(",")}],"not_found":["get-many-unknown"]}\n`);
    });
  }, 60_000);
}
