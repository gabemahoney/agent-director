/**
 * subprocess-client.test.ts — the subprocess Client against fixture binaries:
 * a rejected call does not wedge the queue (SR-3.3), timeout → ErrCallTimeout
 * (SR-6.2/6.5), a signal → ErrConsumerSignal (SR-5.2), version() reports the
 * package version (b.6o1), storePath, home and tmuxCommand reach the CLI
 * verbatim (b.38a), and the busy-send verbs' methods run their verbs and return
 * the lookup payloads as printed (SR-4.1, SR-5.4). callTimeoutMs validation
 * (SR-6.1) is adviceFollow.test.ts K1.
 *
 * The Client passes process.env to each call (SR-1.4), which the fixtures read.
 */

import { test, expect, afterAll } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

import type { Client } from "../src/client.js";
import type { SweepCheck, SweepResult, Confirmation, DeliveryPair, DeliveryResult, ForceResult } from "../src/types.js";
import { ErrCallTimeout, ErrConsumerSignal, ErrInvalidFlags, ErrStoreBusy, ErrTooManyIds, ErrUnknownVerb } from "../src/errors.js";
import { PKG_VERSION, openClient, rejection, withProcessEnv } from "./internal/helper.js";

const FIXTURES = path.resolve(import.meta.dir, "fixtures/epic-a");
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "ad-sct-"));
afterAll(() => fs.rmSync(tmp, { recursive: true, force: true }));

/** A Client on a fresh temp store whose CLI is the named fixture. */
function makeClient(fixture: string, extra: Record<string, unknown> = {}) {
  return openClient(path.join(tmp, `${crypto.randomUUID()}.db`), { _cliPath: path.join(FIXTURES, fixture), callTimeoutMs: 5000, ...extra });
}

test("getMany refuses unsafe ids before starting a subprocess", async () => {
  const argvFile = path.join(tmp, `${crypto.randomUUID()}-get-many-argv`);
  await withProcessEnv({ ARGV_FILE: argvFile }, async () => {
    using client = await makeClient("argv-recorder.sh");
    const before = fs.existsSync(argvFile) ? fs.readFileSync(argvFile, "utf8") : null;
    for (const id of ["--home", "--store-path=private", "private\0id"]) {
      expect(await rejection(client.getMany({ instance_ids: ["safe", id] }))).toBeInstanceOf(ErrInvalidFlags);
      expect(fs.existsSync(argvFile) ? fs.readFileSync(argvFile, "utf8") : null).toBe(before);
    }
    // A rejected call leaves the queue usable; the recorder proves the positive path runs.
    await client.version({});
    expect(fs.readFileSync(argvFile, "utf8")).not.toBe(before);
  });
});

test.each([
  ["ErrTooManyIds", ErrTooManyIds, { id_cap: 3 }],
  ["ErrUnknownVerb", ErrUnknownVerb, undefined],
] as const)("getMany maps %s from a binary that passes the version probe", async (name, ctor, details) => {
  const cli = path.join(tmp, `${crypto.randomUUID()}-get-many.sh`);
  const envelope = { err_name: name, err_description: "fixture refusal", ...(details ? { err_details: details } : {}) };
  fs.writeFileSync(cli, `#!/bin/sh
case " $* " in
  *" version "*) printf '{"version":"0.0.0-dev","commit":"fixture"}\\n'; exit 0;;
  *" get-many "*) printf '%s\\n' '${JSON.stringify(envelope)}' >&2; exit 1;;
  *) exit 71;;
esac
`, { mode: 0o700 });
  using client = await openClient(path.join(tmp, `${crypto.randomUUID()}.db`), { _cliPath: cli });
  const err = await rejection(client.getMany({ instance_ids: ["one", "two", "three", "four"] }));
  expect(err).toBeInstanceOf(ctor);
  expect(err).toMatchObject({ verb: "get-many", errName: name, errDescription: "fixture refusal" });
  expect((err as ErrTooManyIds).errDetails).toEqual(details ?? null);
});

test("Task3: structured error survives stderr notices before and after audit shutdown", async () => {
  const cli = path.join(tmp, `${crypto.randomUUID()}-audit-notices.sh`);
  fs.writeFileSync(cli, `#!/bin/sh
if [ "$1" = version ]; then
  printf '{"version":"0.0.0-dev","commit":"probe"}\\n'
  exit 0
fi
printf 'agent-director: audit notice before result\\n' >&2
printf '{"err_name":"ErrStoreBusy","err_description":"original deadline exhausted"}\\n' >&2
printf 'agent-director: audit shutdown: pending: context deadline exceeded\\n' >&2
exit 1
`, { mode: 0o700 });
  using client = await openClient(path.join(tmp, `${crypto.randomUUID()}.db`), { _cliPath: cli });
  const err = await rejection(client.list({}));
  expect(err).toBeInstanceOf(ErrStoreBusy);
  expect(err).toMatchObject({ verb: "list", errDescription: "original deadline exhausted" });
});

test("call N rejects (subprocess crash) → call N+1 still succeeds", async () => {
  await withProcessEnv({ CALL_MARKER_FILE: path.join(tmp, `${crypto.randomUUID()}-marker`) }, async () => {
    const client = await makeClient("first-call-fails.js");
    expect(await rejection(client.version({}))).toBeInstanceOf(Error);
    expect(await client.version({})).toMatchObject({ version: PKG_VERSION, commit: "abc123" });
  });
}, 15_000);

test("a fixture sleeping past callTimeoutMs → ErrCallTimeout (not ErrConsumerSignal) within the grace period", async () => {
  const start = Date.now();
  const err = await withProcessEnv({ SLEEP_MS: "10000" }, async () =>
    rejection((await makeClient("sleep-and-respond.js", { callTimeoutMs: 300 })).version({}))
  );
  expect(err).toBeInstanceOf(ErrCallTimeout);
  expect(err).not.toBeInstanceOf(ErrConsumerSignal);
  // callTimeoutMs + 2 s SIGTERM grace + 2 s margin.
  expect(Date.now() - start).toBeLessThan(300 + 2000 + 2000);
}, 10_000);

test("a fixture that SIGINTs itself → ErrConsumerSignal carrying the signal", async () => {
  const err = await rejection((await makeClient("self-sigint.sh")).version({}));
  expect(err).toBeInstanceOf(ErrConsumerSignal);
  expect((err as ErrConsumerSignal).signal).toBe("SIGINT");
}, 15_000);

test("b.6o1: version() reports the package version over the CLI's stamp; commit and config_dir pass through", async () => {
  await withProcessEnv({ SLEEP_MS: "0" }, async () => {
    // The fixture answers {"version":"fixture-1.0.0","commit":"aabbccddeeff","config_dir":"~/.config/agent-director"}.
    expect(await (await makeClient("sleep-and-respond.js")).version({})).toEqual({ version: PKG_VERSION, commit: "aabbccddeeff", config_dir: "~/.config/agent-director" });
  });
}, 10_000);

// b.38a: the CLI expands `~`; the client must not, whatever HOME holds (undefined unsets it).
test.each([
  ["HOME set", "/b-38a/home"],
  ["HOME empty", ""],
  ["HOME unset", undefined],
])("%s: `~` values reach the CLI as given, with no os.homedir() path", async (_label, home) => {
  const argvFile = path.join(tmp, `${crypto.randomUUID()}-argv`);
  await withProcessEnv({ HOME: home, ARGV_FILE: argvFile }, async () => {
    const client = await makeClient("argv-recorder.sh", { storePath: "~/x.db", home: "~", tmuxCommand: "~/bin/tmux" });
    await client.version({});
    const argv = fs.readFileSync(argvFile, "utf-8").split("\n").slice(0, -1);
    expect(argv).toEqual(["--store-path", "~/x.db", "--home", "~", "--tmux-command", "~/bin/tmux", "version"]);
    expect(argv.join("\n")).not.toContain(os.homedir());
  });
});

// The CLI stand-in holds a configured pause past the default launch budget.
test("an explicitly configured long pause keeps its larger client budget", async () => {
  const cli = path.join(tmp, `${crypto.randomUUID()}-long-pause.sh`);
  fs.writeFileSync(cli, `#!/bin/sh
case " $* " in
  *" version "*) printf '{"version":"0.0.0-dev","commit":"pause-fixture"}\\n';;
  *" pause "*) sleep 31; printf '{}\\n';;
  *) exit 71;;
esac
`, { mode: 0o700 });
  using client = await openClient(path.join(tmp, `${crypto.randomUUID()}.db`), { _cliPath: cli, callTimeoutMs: 45_000 });
  const started = performance.now();
  expect(await client.pause({ instance_id: "synthetic-long-pause" })).toEqual({});
  expect(performance.now() - started).toBeGreaterThanOrEqual(30_000);
  expect(performance.now() - started).toBeLessThan(45_000);
}, 50_000);

// SR-4.1, SR-5.4: each busy-send verb's method runs its own CLI verb with its identifiers as given.
test.each([
  ["forceDelivery", (c: Client) => c.forceDelivery({ instance_id: "id 多 🦋" }), ["force-delivery", "--instance-id", "id 多 🦋"]],
  ["getForce", (c: Client) => c.getForce({ force_id: "ABCDEF01-2345-6789-ABCD-EF0123456789" }),
    ["get-force", "--force-id", "ABCDEF01-2345-6789-ABCD-EF0123456789"]],
  ["getDelivery", (c: Client) => c.getDelivery({ caller_uuid: "abcdef01-2345-6789-abcd-ef0123456789", delivery_request_id: "-h 多 🦋" }),
    ["get-delivery", "--caller-uuid", "abcdef01-2345-6789-abcd-ef0123456789", "--delivery-request-id", "-h 多 🦋"]],
] as const)("%s runs its verb with its identifiers unchanged", async (_method, call, want) => {
  const argvFile = path.join(tmp, `${crypto.randomUUID()}-argv`);
  await withProcessEnv({ ARGV_FILE: argvFile }, async () => {
    using client = await makeClient("argv-recorder.sh");
    await call(client);
  });
  const argv = fs.readFileSync(argvFile, "utf-8").split("\n").slice(0, -1);
  expect(argv.slice(argv.indexOf(want[0]))).toEqual([...want]);
});

// SR-4.1, SR-5.4: a lookup's payload, null outside found, reaches the caller as the CLI printed it.
const PAIR: DeliveryPair = { caller_uuid: "0f8e2a34-5b6c-4d7e-8f90-a1b2c3d4e5f6", request_id: "msg 2 多" };
const EARLIER: DeliveryPair = { caller_uuid: "0f8e2a34-5b6c-4d7e-8f90-a1b2c3d4e5f6", request_id: "msg 1" };
const HELD: DeliveryResult = {
  ...PAIR, class: "held", final: false, reason: "earlier_message", resend_safe: false, retry_with: "none",
  wait: { reason: "earlier_message", since: "2026-11-13T10:00:00.123Z", earlier: EARLIER }, forced: false,
  instance_id: "id-1", launch_token: null, evidence_kind: null, outcome: "submission_unconfirmed",
};
const DELIVERED: DeliveryResult = {
  ...PAIR, class: "delivered_confirmed", final: true, reason: "exact_receipt", resend_safe: false, retry_with: "none", wait: null,
  forced: true, instance_id: "id-1", launch_token: "tok-1", session_id: "sess-1", evidence_kind: "codex_item_started",
  record_id: "rec-1", record_sha256: "ab".repeat(32), outcome: "submitted",
};
const PENDING: ForceResult = {
  force_id: "6f1c0d2e-3a4b-4c5d-8e6f-7a8b9c0d1e2f", outcome: "pending", reason: "awaiting_admission",
  started_at: "2026-11-13T10:00:00Z", ended_at: null, carried_pairs: [EARLIER, PAIR],
};
test.each([
  ["getForce found", (c: Client) => c.getForce({ force_id: PENDING.force_id }), { lookup: "found", force_result: PENDING }],
  ["getForce retention_expired", (c: Client) => c.getForce({ force_id: PENDING.force_id }), { lookup: "retention_expired", force_result: null }],
  ["getDelivery found, waiting", (c: Client) => c.getDelivery({ caller_uuid: PAIR.caller_uuid, delivery_request_id: PAIR.request_id }),
    { lookup: "found", result: HELD }],
  ["getDelivery found, Final with its receipt", (c: Client) => c.getDelivery({ caller_uuid: PAIR.caller_uuid, delivery_request_id: PAIR.request_id }),
    { lookup: "found", result: DELIVERED }],
  ["getDelivery retention_expired", (c: Client) => c.getDelivery({ caller_uuid: PAIR.caller_uuid, delivery_request_id: PAIR.request_id }),
    { lookup: "retention_expired", result: null }],
] as const)("%s: the payload reaches the caller intact", async (_label, call, payload) => {
  const cli = path.join(tmp, `${crypto.randomUUID()}-lookup.sh`);
  fs.writeFileSync(cli, `#!/bin/sh
case " $* " in
  *" version "*) printf '{"version":"0.0.0-dev","commit":"lookup-fixture"}\\n';;
  *" get-force "*|*" get-delivery "*) printf '%s\\n' '${JSON.stringify(payload)}';;
  *) exit 71;;
esac
`, { mode: 0o700 });
  using client = await openClient(path.join(tmp, `${crypto.randomUUID()}.db`), { _cliPath: cli });
  expect(await call(client)).toEqual(payload);
});

// b.x9o cb: both queued methods preserve partial results and the three records;
// timeout_ms remains an ignored legacy parameter, never an argv/timeout override.
test("sweep and findMissing preserve incomplete checks and queue order", async () => {
  const unknown: Confirmation = {status: "unknown", confirmed_at: null, source: null, checked_at: null, reason: "no_evidence"};
  const check = (completion: "complete" | "incomplete", reason: SweepCheck["reason"]): SweepCheck => ({
    instance_id: completion, state: "working", family_state: "working", idle_screen_availability: "available",
    process_liveness: "unknown", completion, reason,
    confirmations: {state: unknown, process_liveness: unknown, displayed_state: unknown},
  });
  const result: SweepResult = {checks: [check("complete", null), check("incomplete", "deadline")], count: 0, ids: [], unverified: 0, unverified_ids: []};
  const cli = path.join(tmp, `${crypto.randomUUID()}-sweep.sh`);
  const argvFile = path.join(tmp, `${crypto.randomUUID()}-sweep-argv`);
  fs.writeFileSync(cli, `#!/bin/sh
case " $* " in
  *" version "*) printf '{"version":"0.0.0-dev","commit":"sweep-fixture"}\\n'; exit 0;;
esac
printf '%s\\n' "$*" >> '${argvFile}'
printf '%s\\n' '${JSON.stringify(result)}'
`, {mode: 0o700});
  const storePath = path.join(tmp, `${crypto.randomUUID()}.db`);
  using client = await openClient(storePath, {_cliPath: cli, callTimeoutMs: 5000, tmuxCommand: "/sweep-fixture/tmux"});
  const results = await Promise.all([client.sweep({timeout_ms: 1}), client.findMissing({timeout_ms: -1}), client.sweep()]);
  expect(results).toEqual([result, result, result]);
  expect(fs.readFileSync(argvFile, "utf8").trim().split("\n")).toEqual(
    ["sweep", "find-missing", "sweep"].map((verb) => `--store-path ${storePath} --tmux-command /sweep-fixture/tmux ${verb}`));
});
