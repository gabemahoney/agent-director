/**
 * adviceFollow.test.ts — b.fji literal-follow tests for the TS client's own
 * advice: K1 triggers the callTimeoutMs refusal, asserts its advice, then omits
 * the field as told and asserts the client constructs and works; K2 (b.vpb,
 * b.66q) does the same for a malformed extra_env key given to spawn, and to
 * makeTemplate, whose saved template's spawn then pre-trusts as advised; K3 and K4
 * (SR-4.3) do the same for the old keys `claude_instance_id` and `claude_args`:
 * the call renamed as the refusal says succeeds; K5-K7 (SR-1.1, SR-1.6) do the same
 * for the agent refusals: the agent passed, or the template's agent_args fixed, as told.
 */

import { test, expect, afterAll } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

import { Client } from "../src/client.js";
import { ErrAgentArgsMismatch, ErrAgentMissing, ErrInvalidFlags, ErrReservedEnvKey, ErrTemplateAgentMissing } from "../src/errors.js";
import type { SpawnResult } from "../src/types.js";
import {
  CLAUDE_JSON, homeStore, makeTemplateClaude, openClient, rejection, seedOuterParent, spawnClaude, trustEntry,
} from "./internal/helper.js";
import { withTempHome } from "./internal/tempHome.js";

const fixturePath = path.resolve(import.meta.dir, "fixtures/epic-a/success.sh");
const tmpDirs: string[] = [];

afterAll(() => {
  for (const d of tmpDirs.splice(0)) fs.rmSync(d, { recursive: true, force: true });
});

type CreateOpts = Parameters<typeof Client.create>[0];

// K1: "omit the field to use the default (30000 ms)"
for (const bad of [0, -1]) {
  test(`K1 callTimeoutMs ${bad}: omitting the field as advised constructs a working client`, async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "ad-advice-k1-"));
    tmpDirs.push(dir);
    const opts: Record<string, unknown> = {
      storePath: path.join(dir, "state.db"),
      createIfMissing: true,
      callTimeoutMs: bad,
      _cliPath: fixturePath,
    };

    let refusal: unknown;
    try {
      await Client.create(opts as unknown as CreateOpts);
    } catch (e) {
      refusal = e;
    }
    expect(refusal).toBeInstanceOf(Error);
    expect((refusal as Error).message).toContain(`must be positive (got ${bad})`);
    expect((refusal as Error).message).toContain("omit the field to use the default (30000 ms)");

    delete opts.callTimeoutMs;
    const client = await Client.create(opts as unknown as CreateOpts);
    try {
      await expect(client.version({})).resolves.toBeDefined();
    } finally {
      client.close();
    }
  });
}

// K2 (b.vpb, b.66q): "remove \"CLAUDE_CONFIG_DIR=...\" from extra_env, and give each variable its own name as the key (...) and its value as the value"
const k2Cases: {
  verb: string;
  outcome: string;
  launch: (c: Client, cwd: string, env: Record<string, string>) => Promise<SpawnResult>;
}[] = [
  { verb: "spawn", outcome: "launches and", launch: (c, cwd, env) => spawnClaude(c, { cwd, extra_env: env }) },
  {
    verb: "make-template",
    outcome: "saves a template whose spawn",
    launch: async (c, cwd, env) => {
      await makeTemplateClaude(c, { name: "k2", cwd, extra_env: env });
      return c.spawn({ cwd, template: "k2" });
    },
  },
];
for (const { verb, outcome, launch } of k2Cases) {
  test(`K2 ${verb} extra_env key CLAUDE_CONFIG_DIR=<dir>: naming the variable as its own key, as advised, ${outcome} pre-trusts <dir>`, async () => {
    await withTempHome(async (homeDir) => {
      const cfg = fs.mkdtempSync(path.join(homeDir, "cfg-"));
      const claudeJson = path.join(cfg, ".claude.json");
      fs.writeFileSync(claudeJson, CLAUDE_JSON);
      seedOuterParent(homeStore(homeDir));
      using client = await openClient(homeStore(homeDir));
      const bad = `CLAUDE_CONFIG_DIR=${cfg}`;

      const refusal = await rejection(launch(client, homeDir, { [bad]: "" }));

      expect(refusal).toBeInstanceOf(ErrReservedEnvKey);
      expect([(refusal as ErrReservedEnvKey).verb, (refusal as ErrReservedEnvKey).errName]).toEqual([verb, "ErrReservedEnvKey"]);
      expect((refusal as Error).message).toContain(`${JSON.stringify(bad)} is not a valid env-var name: it contains '='`);
      expect((refusal as Error).message).toContain(
        `remove ${JSON.stringify(bad)} from extra_env, and give each variable its own name as the key (not empty, with no '=' and no NUL byte) and its value as the value`,
      );
      const res = await launch(client, homeDir, { CLAUDE_CONFIG_DIR: cfg });
      expect(res.pre_trust).toBe("ok");
      expect(trustEntry(claudeJson, fs.realpathSync(homeDir))).toBe(true);
    });
  }, 10_000);
}

/** Renames each old key of params to the key its refusal advises ("use \"new\""), keeping the value. */
function renamedAsAdvised(params: Record<string, unknown>, refusal: ErrInvalidFlags): Record<string, unknown> {
  const out = { ...params };
  for (const [, oldKey, newKey] of refusal.errDescription.matchAll(/"(\w+)" is no longer accepted; use "(\w+)"/g)) {
    out[newKey!] = out[oldKey!];
    delete out[oldKey!];
  }
  return out;
}

// K3 (SR-4.3): `"claude_instance_id" is no longer accepted; use "instance_id"`
test("K3 claude_instance_id: passing instance_id as advised, spawn launches under that id and status finds it", async () => {
  await withTempHome(async (homeDir) => {
    seedOuterParent(homeStore(homeDir));
    using client = await openClient(homeStore(homeDir));

    const refusedSpawn = await rejection(spawnClaude(client, { cwd: homeDir, claude_instance_id: "k3-id" } as never));
    expect(refusedSpawn).toBeInstanceOf(ErrInvalidFlags);
    expect((refusedSpawn as ErrInvalidFlags).errDescription).toContain('"claude_instance_id" is no longer accepted; use "instance_id"');
    const spawned = await spawnClaude(client, renamedAsAdvised({ cwd: homeDir, claude_instance_id: "k3-id" }, refusedSpawn as ErrInvalidFlags) as never);
    expect(spawned.instance_id).toBe("k3-id");

    const refusedStatus = await rejection(client.status({ claude_instance_id: "k3-id" } as never));
    expect(refusedStatus).toBeInstanceOf(ErrInvalidFlags);
    const status = await client.status(renamedAsAdvised({ claude_instance_id: "k3-id" }, refusedStatus as ErrInvalidFlags) as never);
    expect(status.state).toBe("pending");
  });
}, 10_000);

// K4 (SR-4.3): `"claude_args" is no longer accepted; use "agent_args"`
for (const { verb, outcome, call } of [
  {
    verb: "spawn",
    outcome: "launches",
    call: (c: Client, cwd: string, p: Record<string, unknown>) => spawnClaude(c, { cwd, ...p } as never),
  },
  {
    verb: "make-template",
    outcome: "saves the template",
    call: (c: Client, cwd: string, p: Record<string, unknown>) => makeTemplateClaude(c, { name: "k4", cwd, ...p } as never),
  },
]) {
  test(`K4 ${verb} claude_args: passing agent_args as advised ${outcome}`, async () => {
    await withTempHome(async (homeDir) => {
      seedOuterParent(homeStore(homeDir));
      using client = await openClient(homeStore(homeDir));
      const params = { claude_args: ["--model", "sonnet"] };

      const refusal = await rejection(call(client, homeDir, params));

      expect(refusal).toBeInstanceOf(ErrInvalidFlags);
      expect([(refusal as ErrInvalidFlags).verb, (refusal as ErrInvalidFlags).errName]).toEqual([verb, "ErrInvalidFlags"]);
      expect((refusal as ErrInvalidFlags).errDescription).toContain('"claude_args" is no longer accepted; use "agent_args"');
      await expect(call(client, homeDir, renamedAsAdvised(params, refusal as ErrInvalidFlags))).resolves.toBeDefined();
    });
  }, 10_000);
}

// K5 (SR-1.1): "pass agent claude or codex (--agent on the CLI), or put agent in the template"
test("K5 spawn with no agent: passing agent as advised launches a row that runs it", async () => {
  await withTempHome(async (homeDir) => {
    seedOuterParent(homeStore(homeDir));
    using client = await openClient(homeStore(homeDir));
    const phrase = "pass agent claude or codex (--agent on the CLI), or put agent in the template";

    const refusal = await rejection(client.spawn({ cwd: homeDir, instance_id: "k5-id" }));

    expect(refusal).toBeInstanceOf(ErrAgentMissing);
    expect((refusal as ErrAgentMissing).errDescription).toContain(phrase);
    // The advice names the param ("agent") and its values; the first is passed as told.
    const [, param, value] = /pass (\w+) (\w+) or \w+ \(--agent/.exec((refusal as ErrAgentMissing).errDescription)!;
    const spawned = await client.spawn({ cwd: homeDir, instance_id: "k5-id", [param!]: value } as never);
    expect(spawned.instance_id).toBe("k5-id");
    expect((await client.get({ instance_id: "k5-id" })).agent).toBe(value);
  });
}, 10_000);

// K6 (SR-1.6): make-template's "agent_args are set but no agent is given; pass agent claude or codex, or remove agent_args"
const k6Fixes: [string, { agent?: string; agent_args?: string[] }][] = [
  ["pass agent", { agent: "claude", agent_args: ["--model", "opus"] }],
  ["remove agent_args", {}],
];
test.each(k6Fixes)("K6 make-template agent_args with no agent: to %s, as advised, saves the template", async (_label, fix) => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    const params = { name: "k6", cwd: homeDir, agent_args: ["--model", "opus"] };

    const refusal = await rejection(client.makeTemplate(params));

    expect(refusal).toBeInstanceOf(ErrTemplateAgentMissing);
    expect((refusal as ErrTemplateAgentMissing).errDescription).toContain(
      "agent_args are set but no agent is given; pass agent claude or codex, or remove agent_args",
    );
    const saved = await client.makeTemplate({ name: "k6", cwd: homeDir, ...fix });
    expect(fs.existsSync(saved.path)).toBe(true);
  });
}, 10_000);

// K7 (SR-1.6): ErrAgentArgsMismatch's "spawn agent <the template's agent>, or pass this call's own non-empty
// agent_args, which replace the template's"
const k7Mismatch = async (client: Client, cwd: string, templateAgent: string, callAgent: string, id: string) => {
  await client.makeTemplate({ name: "k7", cwd, agent: templateAgent, agent_args: ["--model", "opus"] });
  const refusal = await rejection(client.spawn({ cwd, template: "k7", agent: callAgent, instance_id: id }));
  expect(refusal).toBeInstanceOf(ErrAgentArgsMismatch);
  expect((refusal as ErrAgentArgsMismatch).errDescription).toContain(
    ", or pass this call's own non-empty agent_args, which replace the template's",
  );
  return (refusal as ErrAgentArgsMismatch).errDescription;
};

test("K7 ErrAgentArgsMismatch: spawning the agent the advice names inherits the template's agent_args", async () => {
  await withTempHome(async (homeDir) => {
    seedOuterParent(homeStore(homeDir));
    using client = await openClient(homeStore(homeDir));

    const advice = await k7Mismatch(client, homeDir, "claude", "codex", "k7-named");

    const named = /spawn agent (\w+),/.exec(advice)![1]!;
    await client.spawn({ cwd: homeDir, template: "k7", agent: named, instance_id: "k7-named" });
    expect(await client.get({ instance_id: "k7-named" })).toMatchObject({ agent: "claude", agent_args: ["--model", "opus"] });
  });
}, 10_000);

test("K7 ErrAgentArgsMismatch: passing this call's own agent_args replaces the template's", async () => {
  await withTempHome(async (homeDir) => {
    seedOuterParent(homeStore(homeDir));
    using client = await openClient(homeStore(homeDir));

    await k7Mismatch(client, homeDir, "codex", "claude", "k7-own");

    await client.spawn({ cwd: homeDir, template: "k7", agent: "claude", instance_id: "k7-own", agent_args: ["--model", "sonnet"] });
    expect(await client.get({ instance_id: "k7-own" })).toMatchObject({ agent: "claude", agent_args: ["--model", "sonnet"] });
  });
}, 10_000);
