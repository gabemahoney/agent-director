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
import type { SpawnParams, MakeTemplateParams, GetResult, ListRow } from "../src/index.js";
import { homeStore, openClient, runHelper, seedOuterParent, spawnClaude, withProcessEnv } from "./internal/helper.js";
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

// Fake tmux does not execute Cursor; a controlled real process owns its pending pane identity.
test.each([
  ["direct, empty args", false, []],
  ["direct, model args", false, ["--model", "auto"]],
  ["direct, mode and literal prompt", false, ["--plan", "-m=account/模型", "--", "--resume\n多行 🦋"]],
  ["template, absent args", true, undefined],
  ["template, empty args", true, []],
  ["template, replacement args", true, ["--mode=ask", "--", "--resume\n多行 🦋"]],
] as [string, boolean, string[] | undefined][])("Cursor %s uses existing spawn/get/list/status/subagent methods", async (_label, template, agentArgs) => {
  await withTempHome(async (homeDir) => {
    await withProcessEnv({ TMUX_TMPDIR: homeDir, FAKE_TMUX_TABLES: path.join(homeDir, "fake-tmux-tables") }, async () => {
    const owner = Bun.spawn(["sleep", "3600"], { stdin: "ignore", stdout: "ignore", stderr: "ignore" });
    try {
    const identity = runHelper("fake-pane-owner", { pid: String(owner.pid) });
    const storePath = homeStore(homeDir);
    seedOuterParent(storePath);
    using client = await openClient(storePath);
    const params: SpawnParams = {
      cwd: homeDir,
      instance_id: `cursor-${crypto.randomUUID()}`,
      agent_args: agentArgs,
      ...(template ? { template: "cursor" } : { agent: "cursor" }),
    };
    if (template) {
      const templateParams: MakeTemplateParams = { name: "cursor", agent: "cursor", agent_args: ["--model", "auto"] };
      const result = await client.makeTemplate(templateParams);
      expect(fs.existsSync(result.path)).toBe(true);
    }
    const spawned = await client.spawn(params);
    expect(spawned.instance_id).toBe(params.instance_id!);
    const row: GetResult = await client.get({ instance_id: spawned.instance_id });
    expect(row).toMatchObject({
      instance_id: spawned.instance_id, agent: "cursor", state: "pending",
      agent_args: template && !agentArgs?.length ? ["--model", "auto"] : agentArgs,
    });
    const listed: ListRow | undefined = (await client.list({})).spawns.find((r) => r.instance_id === spawned.instance_id);
    expect(listed).toMatchObject({ instance_id: spawned.instance_id, agent: "cursor", state: "pending" });
    const status = await client.status({ instance_id: spawned.instance_id });
    expect(status.state).toBe("pending");
    expect(status.family_state).toBe("working");
    expect(Object.keys(status).sort()).toEqual(["family_state", "launch_started_at", "state"]);
    expect(await client.listSubagents({ instance_id: spawned.instance_id })).toEqual({ tracked: false, subagents: [] });
    expect(runHelper("fake-pane-owner", { pid: String(owner.pid), store: storePath, id: spawned.instance_id })).toEqual(identity);
    } finally {
      owner.kill();
      await owner.exited;
    }
    });
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
        spawnClaude(client, { cwd: homeDir, label: [`seq=${i}`] }).then((r) => {
          resolved.push(i);
          return r;
        })
      )
    );
    for (const r of results) expect(r.instance_id.length).toBeGreaterThan(0);
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
