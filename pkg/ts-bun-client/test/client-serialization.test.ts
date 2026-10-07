/**
 * client-serialization.test.ts — Epic C / SR-10.4: a Client runs its calls one
 * at a time through a private queue, in submission order (SR-3); two Clients
 * have independent queues and may overlap (SR-3.2).
 *
 * No overlap: the serialization-recorder fixture logs START, sleeps 50 ms, then
 * logs END, so parallel runs would leave two STARTs adjacent. Order: each spawn
 * resolves in submission order, and the store's started_at never goes backwards.
 */

import { test, expect } from "bun:test";
import * as fs from "node:fs";
import * as path from "node:path";
import { homeStore, openClient, seedOuterParent, withProcessEnv } from "./internal/helper.js";
import { withTempHome } from "./internal/tempHome.js";

const RECORDER = path.resolve(import.meta.dir, "fixtures/epic-a/serialization-recorder.js");

test("5 parallel version() calls on one Client never overlap: the CLI log alternates START, END", async () => {
  await withTempHome(async (homeDir) => {
    const logFile = path.join(homeDir, "serial.log");
    using client = await openClient(homeStore(homeDir), { _cliPath: RECORDER });
    await withProcessEnv({ LOG_FILE: logFile }, () => Promise.all(Array.from({ length: 5 }, () => client.version({}))));
    const kinds = fs.readFileSync(logFile, "utf-8").trim().split("\n").map((line) => line.split(" ")[1]);
    expect(kinds).toEqual(Array.from({ length: 10 }, (_, i) => (i % 2 === 0 ? "START" : "END")));
  });
}, 30_000);

test("5 parallel spawn() calls on one Client execute serially in submission order", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = homeStore(homeDir);
    seedOuterParent(storePath);
    using client = await openClient(storePath);

    const N = 5;
    const resolved: number[] = [];
    const results = await Promise.all(
      Array.from({ length: N }, (_, i) =>
        client.spawn({ cwd: homeDir, label: [`seq=${i}`] }).then((r) => {
          resolved.push(i);
          return r;
        })
      )
    );
    for (const r of results) expect(r.claude_instance_id.length).toBeGreaterThan(0);
    expect(resolved).toEqual([...Array(N).keys()]);

    // The store holds one row per seq label, started_at non-decreasing by seq.
    const startedAt = new Map<number, string>();
    for (const row of (await client.list({})).spawns) {
      const seq = row.labels?.["seq"];
      if (typeof seq === "string") startedAt.set(Number(seq), row.started_at);
    }
    expect(startedAt.size).toBe(N);
    for (let i = 1; i < N; i++) expect(startedAt.get(i)! >= startedAt.get(i - 1)!).toBe(true);
  });
}, 60_000);

test("two independent Clients may overlap (SR-3.2): both complete without deadlock", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = homeStore(homeDir);
    using clientA = await openClient(storePath);
    using clientB = await openClient(storePath);
    const [vA, vB] = await Promise.all([clientA.version({}), clientB.version({})]);
    expect(typeof vA.version).toBe("string");
    expect(typeof vB.version).toBe("string");
  });
}, 30_000);
