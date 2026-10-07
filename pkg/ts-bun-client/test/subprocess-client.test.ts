/**
 * subprocess-client.test.ts — the subprocess Client against fixture binaries:
 * a rejected call does not wedge the queue (SR-3.3), timeout → ErrCallTimeout
 * (SR-6.2/6.5), a signal → ErrConsumerSignal (SR-5.2), version() reports the
 * package version (b.6o1), and storePath, home and tmuxCommand reach the CLI
 * verbatim (b.38a). callTimeoutMs validation (SR-6.1) is adviceFollow.test.ts K1.
 *
 * The Client passes process.env to each call (SR-1.4), which the fixtures read.
 */

import { test, expect, afterAll } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

import { ErrCallTimeout, ErrConsumerSignal } from "../src/errors.js";
import { PKG_VERSION, openClient, rejection, withProcessEnv } from "./internal/helper.js";

const FIXTURES = path.resolve(import.meta.dir, "fixtures/epic-a");
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "ad-sct-"));
afterAll(() => fs.rmSync(tmp, { recursive: true, force: true }));

/** A Client on a fresh temp store whose CLI is the named fixture. */
function makeClient(fixture: string, extra: Record<string, unknown> = {}) {
  return openClient(path.join(tmp, `${crypto.randomUUID()}.db`), { _cliPath: path.join(FIXTURES, fixture), callTimeoutMs: 5000, ...extra });
}

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

test("b.6o1: version() reports the package version over the CLI's stamp; commit passes through", async () => {
  await withProcessEnv({ SLEEP_MS: "0" }, async () => {
    // The fixture answers {"version":"fixture-1.0.0","commit":"aabbccddeeff"}.
    expect(await (await makeClient("sleep-and-respond.js")).version({})).toEqual({ version: PKG_VERSION, commit: "aabbccddeeff" });
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
