/**
 * settle-off.test.ts — b.3ie: with [state] settle_ms unset or 0, the TS
 * client's status, get and list results serialize (JSON.stringify plus a
 * newline) byte for byte to the CLI's goldens in
 * cmd/agent-director/testdata/settle_off, over ts-helper's fixed store
 * (seed-read-surface-store). The case table there drives every surface.
 */

import { test, expect } from "bun:test";
import * as fs from "fs";
import * as path from "path";
import type { Client } from "../src/index.js";
import { withTempHome } from "./internal/tempHome.js";
import { runHelper, homeStore, openClient } from "./internal/helper.js";

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
      runHelper("seed-read-surface-store", { store: homeStore(homeDir) });
      if (variant.config !== null) {
        fs.writeFileSync(path.join(homeDir, ".config", "agent-director", "config.toml"), variant.config);
      }
      using client = await openClient(homeStore(homeDir));
      for (const c of table.cases) {
        const got = JSON.stringify(await call(client, c)) + "\n";
        const want = fs.readFileSync(path.join(fixtureDir, "golden", `${c.name}.json`), "utf8");
        expect(`${c.name}: ${got}`).toBe(`${c.name}: ${want}`);
      }
    });
  }, 60_000);
}
