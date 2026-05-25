// subprocess-client-1-smoke driver — Epic C / SR-10.3.
//
// Exercises every callable verb through the installed-tarball Client. Prints
// "OK <verb>" (happy) or "OK <verb>: <ErrClass>" (error) for each verb. The
// case bash script greps `OK <verb>` for every entry in VERBS, so either
// scenario satisfies the cover; both are emitted for non-allow-listed verbs
// so the test would still catch a regression on either path.
//
// Mirrors pkg/ts-bun-client/test/subprocess-smoke.test.ts but cannot use the
// ts-helper seed-spawn fixture (it ships only under the dev tree). State-
// sensitive verbs (send-keys, read-pane, kill, decide, resume, pause) cover
// via the error path only — a real spawn created via fake-tmux starts in
// state=pending, which the dispatcher's interactivity check rejects.

import * as path from "node:path";
import {
  Client,
  ErrCwdMissing,
  ErrSpawnNotFound,
  ErrInvalidDecision,
  ErrListInvalidLabel,
  ErrTemplateNameUnsafe,
} from "agent-director";

const HOME = process.env.HOME;
const STORE = path.join(HOME, ".agent-director", "state.db");
const FAKE_TMUX = process.env.FAKE_TMUX_BIN ?? "/work/source/test/fake-tmux/tmux";
const BOGUS = "subprocess-smoke-bogus-id";

function fail(msg) {
  console.error(`FAIL ${msg}`);
  process.exit(1);
}

async function expectThrow(fn, ctor, label) {
  try {
    await fn();
  } catch (e) {
    if (!(e instanceof ctor)) {
      fail(`${label}: expected ${ctor.name}, got ${e?.constructor?.name ?? typeof e}: ${e?.message ?? e}`);
    }
    return e;
  }
  fail(`${label}: expected ${ctor.name} but call resolved`);
}

const client = new Client({ storePath: STORE, createIfMissing: true, tmuxCommand: FAKE_TMUX });
try {
  // ── spawn ───────────────────────────────────────────────────────────────
  const sp = await client.spawn({ cwd: HOME });
  if (typeof sp.claude_instance_id !== "string" || sp.claude_instance_id.length === 0) {
    fail("spawn: missing claude_instance_id");
  }
  const spawnId = sp.claude_instance_id;
  console.log("OK spawn");

  await expectThrow(() => client.spawn({ cwd: "" }), ErrCwdMissing, "spawn-err");
  console.log("OK spawn: ErrCwdMissing");

  // ── status (read-only; works on any state) ──────────────────────────────
  const st = await client.status({ claude_instance_id: spawnId });
  if (typeof st.state !== "string") fail("status: missing state");
  console.log("OK status");

  await expectThrow(() => client.status({ claude_instance_id: BOGUS }), ErrSpawnNotFound, "status-err");
  console.log("OK status: ErrSpawnNotFound");

  // ── get (read-only; works on any state) ─────────────────────────────────
  const g = await client.get({ claude_instance_id: spawnId });
  if (g.claude_instance_id !== spawnId) fail("get: id mismatch");
  console.log("OK get");

  await expectThrow(() => client.get({ claude_instance_id: BOGUS }), ErrSpawnNotFound, "get-err");
  console.log("OK get: ErrSpawnNotFound");

  // ── send-keys (error only; needs an interactive state we can't seed) ────
  await expectThrow(
    () => client.sendKeys({ claude_instance_id: BOGUS, text: "x" }),
    ErrSpawnNotFound,
    "send-keys-err",
  );
  console.log("OK send-keys: ErrSpawnNotFound");

  // ── read-pane (error only; needs an interactive state we can't seed) ────
  await expectThrow(
    () => client.readPane({ claude_instance_id: BOGUS }),
    ErrSpawnNotFound,
    "read-pane-err",
  );
  console.log("OK read-pane: ErrSpawnNotFound");

  // ── kill (error only; happy path requires a settled tmux session) ───────
  await expectThrow(
    () => client.kill({ claude_instance_id: BOGUS }),
    ErrSpawnNotFound,
    "kill-err",
  );
  console.log("OK kill: ErrSpawnNotFound");

  // ── decide (error only; happy path needs a seeded check_permission row) ─
  await expectThrow(
    () => client.decide({ claude_instance_id: "any", decision: "maybe" }),
    ErrInvalidDecision,
    "decide-err",
  );
  console.log("OK decide: ErrInvalidDecision");

  // ── resume (error only; happy path needs a seeded ended row + jsonl) ────
  await expectThrow(
    () => client.resume({ claude_instance_id: BOGUS }),
    ErrSpawnNotFound,
    "resume-err",
  );
  console.log("OK resume: ErrSpawnNotFound");

  // ── pause (error only; happy path needs a pausable interactive spawn) ───
  await expectThrow(
    () => client.pause({ claude_instance_id: BOGUS }),
    ErrSpawnNotFound,
    "pause-err",
  );
  console.log("OK pause: ErrSpawnNotFound");

  // ── find-missing (linux unconditionally on the container) ───────────────
  const fm = await client.findMissing({});
  if (typeof fm.count !== "number" || !Array.isArray(fm.ids)) {
    fail("find-missing: bad result");
  }
  console.log("OK find-missing");

  // ── expire ──────────────────────────────────────────────────────────────
  const ex = await client.expire({ older_than: "0d" });
  if (ex === null || typeof ex !== "object") fail("expire: bad result");
  console.log("OK expire");

  // ── delete ──────────────────────────────────────────────────────────────
  const del = await client.delete({ claude_instance_id: ["nonexistent"] });
  if (del.results === null || typeof del.results !== "object") fail("delete: bad results");
  console.log("OK delete");

  // ── make-template ───────────────────────────────────────────────────────
  const mt = await client.makeTemplate({ name: "subsmoke-tmpl" });
  if (mt === null || typeof mt !== "object") fail("make-template: bad result");
  console.log("OK make-template");

  await expectThrow(
    () => client.makeTemplate({ name: "a/b" }),
    ErrTemplateNameUnsafe,
    "make-template-err",
  );
  console.log("OK make-template: ErrTemplateNameUnsafe");

  // ── list ────────────────────────────────────────────────────────────────
  const ls = await client.list({});
  if (!Array.isArray(ls.spawns)) fail("list: bad spawns");
  console.log("OK list");

  await expectThrow(
    () => client.list({ label: ["no-equals-sign"] }),
    ErrListInvalidLabel,
    "list-err",
  );
  console.log("OK list: ErrListInvalidLabel");

  // ── version ─────────────────────────────────────────────────────────────
  const v = await client.version({});
  if (typeof v.version !== "string" || v.version.length === 0) fail("version: missing version");
  if (typeof v.commit !== "string") fail("version: missing commit");
  console.log("OK version");
} finally {
  client[Symbol.dispose]?.();
}
