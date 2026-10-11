/**
 * old-keys.test.ts — SR-4.3 (TS row): a params object carrying an old key
 * (`claude_instance_id`, `claude_args`) is refused by the client with
 * ErrInvalidFlags naming the new key, never dropped or mapped to it, and no
 * process starts for the refused call. The stand-in CLI records every start
 * to a log whose path is written into its script (the probe scrubs the env),
 * so the one start Client.create() makes is the whole log after a refusal.
 */

import { test, expect, afterAll, beforeAll } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

import type { Client } from "../src/client.js";
import { ErrInvalidFlags } from "../src/errors.js";
import { openClient, rejection } from "./internal/helper.js";

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "ad-oldkeys-"));
const startLog = path.join(tmp, "starts.log");
const standIn = path.join(tmp, "agent-director");

beforeAll(() => {
  fs.writeFileSync(
    standIn,
    `#!/bin/sh\nprintf 'start\\n' >> '${startLog}'\nprintf '{"version":"0.0.0-dev","commit":"deadbeef"}\\n'\n`,
    { mode: 0o755 },
  );
});
afterAll(() => fs.rmSync(tmp, { recursive: true, force: true }));

const starts = () => (fs.existsSync(startLog) ? fs.readFileSync(startLog, "utf8").split("\n").filter(Boolean).length : 0);

type Call = (c: Client, params: Record<string, unknown>) => Promise<unknown>;

// Every verb that takes an id, by the client method that sends it.
const idVerbs: [verb: string, call: Call][] = [
  ["status", (c, p) => c.status(p as never)],
  ["get", (c, p) => c.get(p as never)],
  ["kill", (c, p) => c.kill(p as never)],
  ["resume", (c, p) => c.resume(p as never)],
  ["pause", (c, p) => c.pause(p as never)],
  ["send-keys", (c, p) => c.sendKeys(p as never)],
  ["read-pane", (c, p) => c.readPane(p as never)],
  ["decide", (c, p) => c.decide(p as never)],
];

const refusedCalls: [verb: string, call: Call, params: Record<string, unknown>, oldKey: string, newKey: string][] = [
  ...idVerbs.map(
    ([verb, call]): (typeof refusedCalls)[number] => [verb, call, { claude_instance_id: "id-1" }, "claude_instance_id", "instance_id"],
  ),
  ["spawn", (c, p) => c.spawn(p as never), { cwd: "/ws", claude_args: ["--model", "x"] }, "claude_args", "agent_args"],
  ["spawn", (c, p) => c.spawn(p as never), { cwd: "/ws", claude_instance_id: "id-1" }, "claude_instance_id", "instance_id"],
  ["make-template", (c, p) => c.makeTemplate(p as never), { name: "t", claude_args: ["--x"] }, "claude_args", "agent_args"],
];

test.each(refusedCalls)("%s with %s → ErrInvalidFlags naming the new key; no process starts", async (verb, call, params, oldKey, newKey) => {
  using client = await openClient(path.join(tmp, `${crypto.randomUUID()}.db`), { _cliPath: standIn });
  const before = starts();

  const err = await rejection(call(client, params));

  expect(err).toBeInstanceOf(ErrInvalidFlags);
  const e = err as ErrInvalidFlags;
  expect([e.verb, e.errName]).toEqual([verb, "ErrInvalidFlags"]);
  expect(e.errDescription).toBe(`"${oldKey}" is no longer accepted; use "${newKey}"`);
  expect(e.message).toBe(`ErrInvalidFlags: "${oldKey}" is no longer accepted; use "${newKey}"`);
  expect(starts()).toBe(before);
});

test("an old key is refused on a verb that does not take it, and next to its new key", async () => {
  using client = await openClient(path.join(tmp, `${crypto.randomUUID()}.db`), { _cliPath: standIn });
  const before = starts();

  const onList = await rejection(client.list({ claude_instance_id: "id-1" } as never));
  const withNew = await rejection(client.status({ claude_instance_id: "id-1", instance_id: "id-1" } as never));

  expect(onList).toBeInstanceOf(ErrInvalidFlags);
  expect([(onList as ErrInvalidFlags).verb, (onList as ErrInvalidFlags).errDescription]).toEqual([
    "list",
    '"claude_instance_id" is no longer accepted; use "instance_id"',
  ]);
  expect(withNew).toBeInstanceOf(ErrInvalidFlags);
  expect((withNew as ErrInvalidFlags).errDescription).toBe('"claude_instance_id" is no longer accepted; use "instance_id"');
  expect(starts()).toBe(before);
});

test("two old keys in one call → one ErrInvalidFlags naming both new keys, joined by '; '", async () => {
  using client = await openClient(path.join(tmp, `${crypto.randomUUID()}.db`), { _cliPath: standIn });
  const before = starts();

  const err = await rejection(client.spawn({ cwd: "/ws", claude_instance_id: "id-1", claude_args: ["--x"] } as never));

  expect(err).toBeInstanceOf(ErrInvalidFlags);
  expect((err as ErrInvalidFlags).errDescription).toBe(
    '"claude_instance_id" is no longer accepted; use "instance_id"; "claude_args" is no longer accepted; use "agent_args"',
  );
  expect(starts()).toBe(before);
});

test("the refusal leaves the Client usable: the same call with the new key starts the CLI", async () => {
  using client = await openClient(path.join(tmp, `${crypto.randomUUID()}.db`), { _cliPath: standIn });
  const before = starts();
  await rejection(client.status({ claude_instance_id: "id-1" } as never));
  expect(starts()).toBe(before);

  // The stand-in answers with its version envelope; the call is sent, which is the point.
  await client.status({ instance_id: "id-1" });

  expect(starts()).toBe(before + 1);
});
