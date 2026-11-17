/**
 * Smoke test — spawn verb
 *
 * Happy path: cwd = the temp HOME, whose .claude.json lacks its trust entry;
 * pre_trust ok writes the entry, no_pre_trust reports skipped and leaves the
 * file byte-identical (SR-22.6).
 * Errors: empty cwd → ErrCwdMissing; no agent (absent or empty) → ErrAgentMissing and a
 * wrong-case agent → ErrAgentInvalid, each with no row (SR-1.1); an id with a control character →
 * ErrInvalidFlags with no row and no session, with or without reuse_finished;
 * Agent Defaults for codex, fresh or reused → ErrCantSend agent_not_covered (SR-9.2).
 * reuse_finished (SR-10.1, SR-10.2): without an id it mints one; with an id an
 * ended row is reused (pending, one session); without the opt-in an ended row
 * collides (ErrInstanceIdCollision), row unchanged, no session.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import * as fs from "fs";
import { withTempHome } from "../internal/tempHome.js";
import {
  runHelper, privateTmuxSocket, fakeTmuxCalls, withProcessEnv, CLAUDE_JSON, trustEntry, seedOuterParent,
  homeStore, openClient, rejection, spawnClaude, expectAgentNotCovered,
} from "../internal/helper.js";
import {
  ErrSpawnDeniedFlag, ErrCwdMissing, ErrInvalidFlags, ErrInstanceIdCollision, ErrAgentMissing, ErrAgentInvalid, AgentDirectorError,
  ErrPermissionRulesRefused, ErrPermissionsUnsupported, ErrTemplateMalformed,
  type SpawnParams,
} from "../../src/index.js";

const operatorRules = {
  version: 1,
  rules: [{ id: "PRIVATE_RULE_ID", decision: "allow", reason: "PRIVATE_REASON",
    match: { prefix: ["tail"] }, tests: { match: ["tail x"], noMatch: ["head x"] } }],
};

/** Operator config only; returning its path lets the same client observe edits. */
function configureDroidRules(home: string, rules = operatorRules): string {
  const dir = path.dirname(homeStore(home));
  fs.mkdirSync(dir, { recursive: true });
  const file = path.join(dir, "rules.json");
  fs.writeFileSync(file, JSON.stringify(rules));
  fs.writeFileSync(path.join(dir, "config.toml"), `[experimental]\ndroid = true\n[launch]\ndroid_rules_path = ${JSON.stringify(file)}\n`);
  return file;
}

test.each([false, true])("spawn: configured rules give a sanitized typed refusal (reuse=%s)", async (reuse) => {
  await withTempHome(async (home) => {
    const rules = configureDroidRules(home, { ...operatorRules, rules: [{ ...operatorRules.rules[0], decision: "deny" }] });
    const store = homeStore(home);
    const id = `droid-rules-${crypto.randomUUID().slice(0, 8)}`;
    runHelper("seed-spawn", { store, id, agent: "droid", state: "ended", "create-store": true, socket: privateTmuxSocket(home) });
    using client = await openClient(store);
    const before = await client.get({ instance_id: id });
    const log = path.join(home, "fake-tmux.log");
    await withProcessEnv({ FAKE_TMUX_LOG: log }, async () => {
      const params = { agent: "droid", cwd: home, agent_args: ["--model", "model"],
        ...(reuse ? { instance_id: id, reuse_finished: true } : {}) };
      const err = await rejection(client.spawn(params)) as AgentDirectorError;
      expect(err).toBeInstanceOf(ErrPermissionRulesRefused);
      expect(err.errName).toBe("ErrPermissionRulesRefused");
      expect(err.errDescription).toBe("Droid Rule Set refused: rules[0] has a decision other than allow, ask or block; an operator must fix the Rule Set File [launch] droid_rules_path names, or the droid program (see docs/experimental.md#droid-rules); no row, session, trust entry or launch file was written and nothing was launched");
      for (const secret of ["PRIVATE_RULE_ID", "PRIVATE_REASON", "tail x", "head x"]) expect(err.message).not.toContain(secret);
      expect(await client.get({ instance_id: id })).toEqual(before);
      expect((await client.list({})).spawns.map((row) => row.instance_id)).toEqual([id]);
      expect(newSessionCount(log)).toBe(0);
      expect(fs.existsSync(path.join(path.dirname(store), "launch"))).toBe(false);

      // PM-1: TS uses a new CLI invocation for each call. The current client
      // observes both a file edit and a later configured-path edit.
      if (!reuse) {
        fs.writeFileSync(rules, "{}");
        const changed = await rejection(client.spawn(params)) as AgentDirectorError;
        expect(changed).toBeInstanceOf(ErrPermissionRulesRefused);
        expect(changed.errDescription).toContain("the file is not a rules object:");
        fs.writeFileSync(path.join(path.dirname(store), "config.toml"), `[experimental]\ndroid = true\n[launch]\ndroid_rules_path = ${JSON.stringify(path.join(home, "absent.json"))}\n`);
        const moved = await rejection(client.spawn(params)) as AgentDirectorError;
        expect(moved).toBeInstanceOf(ErrPermissionRulesRefused);
        expect(moved.errDescription).toContain("the file is not a readable regular file of at most 1 MiB");
      }
    });
  });
}, 20_000);

const droidCallerCases: { name: string; params: Partial<SpawnParams>; error: typeof AgentDirectorError }[] = [
  ...(["allow", "deny", "ask"] as const).map((key) => ({ name: key, params: { [key]: ["Bash"] }, error: ErrPermissionsUnsupported })),
  ...[
    ["settings", ["--settings", "/caller/settings.json"]],
    ["joined model", ["--model=model"]],
    ["duplicate model", ["--model", "model", "--model", "other"]],
    ["positional", ["prompt"]],
  ].map(([name, args]) => ({ name: name as string, params: { agent_args: args as string[] }, error: ErrSpawnDeniedFlag })),
];

for (const mode of ["spawn", "reuse", "make-template"] as const) {
  test.each(droidCallerCases)(`${mode}: operator rules cannot enable caller $name`, async ({ params, error }) => {
    await withTempHome(async (home) => {
      configureDroidRules(home);
      const store = homeStore(home);
      const id = `droid-caller-${crypto.randomUUID().slice(0, 8)}`;
      runHelper("seed-spawn", { store, id, agent: "droid", state: "ended", "create-store": true, socket: privateTmuxSocket(home) });
      using client = await openClient(store);
      const before = await client.get({ instance_id: id });
      const log = path.join(home, "fake-tmux.log");
      await withProcessEnv({ FAKE_TMUX_LOG: log }, async () => {
        const p = { agent: "droid", cwd: home, agent_args: ["--model", "model"], ...params };
        const call = mode === "make-template" ? client.makeTemplate({ ...p, name: "refused" }) :
          client.spawn({ ...p, ...(mode === "reuse" ? { instance_id: id, reuse_finished: true } : {}) });
        await expect(call).rejects.toBeInstanceOf(error);
        expect(await client.get({ instance_id: id })).toEqual(before);
        expect((await client.list({})).spawns.map((row) => row.instance_id)).toEqual([id]);
        expect(newSessionCount(log)).toBe(0);
        for (const rel of ["launch", "templates/refused.toml"]) expect(fs.existsSync(path.join(path.dirname(store), rel))).toBe(false);
      });
    });
  }, 10_000);
}

test.each([
  ["settings", ErrSpawnDeniedFlag], ["permissions", ErrPermissionsUnsupported], ["rule field", ErrTemplateMalformed],
] as const)("spawn: hand-edited template %s stays refused with operator rules", async (kind, error) => {
  await withTempHome(async (home) => {
    configureDroidRules(home);
    const store = homeStore(home);
    const id = `droid-template-${crypto.randomUUID().slice(0, 8)}`;
    runHelper("seed-spawn", { store, id, agent: "droid", state: "ended", "create-store": true, socket: privateTmuxSocket(home) });
    using client = await openClient(store);
    const { path: template } = await client.makeTemplate({ name: "caller", agent: "droid", agent_args: ["--model", "model"] });
    let body = fs.readFileSync(template, "utf8");
    if (kind === "settings") {
      expect(body).toMatch(/agent_args = \[[^\]]*\]/);
      body = body.replace(/agent_args = \[[^\]]*\]/, 'agent_args = ["--settings", "/caller/settings.json"]');
    } else if (kind === "permissions") body += '\n[permissions]\nask = ["Bash"]\n';
    else body = 'droid_rules_path = "/caller/rules.json"\n' + body;
    fs.writeFileSync(template, body);
    const before = await client.get({ instance_id: id });
    const log = path.join(home, "fake-tmux.log");
    await withProcessEnv({ FAKE_TMUX_LOG: log }, async () => {
      for (const reuse of [false, true]) {
        await expect(client.spawn({ cwd: home, template: "caller", ...(reuse ? { instance_id: id, reuse_finished: true } : {}) })).rejects.toBeInstanceOf(error);
        expect(await client.get({ instance_id: id })).toEqual(before);
        expect((await client.list({})).spawns.map((row) => row.instance_id)).toEqual([id]);
        expect(fs.readFileSync(template, "utf8")).toBe(body);
        expect(newSessionCount(log)).toBe(0);
      }
    });
  });
}, 20_000);

/** fake-tmux invocations in logPath whose argv includes new-session. */
const newSessionCount = (logPath: string) => fakeTmuxCalls(logPath).filter((argv) => argv.includes("new-session")).length;

test.each([
  ["ok", false],
  ["skipped", true],
] as const)("spawn: happy path — creates instance with valid cwd, pre_trust %s", async (want, noPreTrust) => {
  await withTempHome(async (homeDir) => {
    const claudeJson = path.join(homeDir, ".claude.json");
    fs.writeFileSync(claudeJson, CLAUDE_JSON);
    seedOuterParent(homeStore(homeDir));
    using client = await openClient(homeStore(homeDir));
    const result = await spawnClaude(client, { cwd: homeDir, ...(noPreTrust ? { no_pre_trust: true } : {}) });
    expect(result.instance_id.length).toBeGreaterThan(0);
    expect(result.pre_trust).toBe(want);
    expect(result).not.toHaveProperty("dropped_args");
    expect((await client.get({ instance_id: result.instance_id })).agent).toBe("claude");
    if (noPreTrust) expect(fs.readFileSync(claudeJson, "utf8")).toBe(CLAUDE_JSON);
    // The entry is keyed by the canonical cwd (the spawn resolves symlinks).
    else expect(trustEntry(claudeJson, fs.realpathSync(homeDir))).toBe(true);
  });
}, 10_000);

test("spawn: error — empty cwd → ErrCwdMissing", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    await expect(spawnClaude(client, { cwd: "" })).rejects.toBeInstanceOf(ErrCwdMissing);
  });
}, 10_000);

// SR-1.1: a spawn names its agent (case-sensitive); the refusal leaves no row.
test.each([
  ["absent", {}, ErrAgentMissing],
  ["empty, which counts as absent", { agent: "" }, ErrAgentMissing],
  ["Claude, wrong case", { agent: "Claude" }, ErrAgentInvalid],
] as const)("spawn: error — agent %s → %p, no row", async (_label, params, want) => {
  await withTempHome(async (homeDir) => {
    seedOuterParent(homeStore(homeDir));
    using client = await openClient(homeStore(homeDir));
    const before = (await client.list({})).spawns.length;
    await expect(client.spawn({ cwd: homeDir, ...params })).rejects.toBeInstanceOf(want);
    expect((await client.list({})).spawns.length).toBe(before);
  });
}, 10_000);

// SR-9.1 / AC-SPN-02: the UUID-suffixed prefix cannot collide across runs, and the
// message is checked for any echo of it. The reuse_finished row: the opt-in does not bypass the check (SR-10.1).
test.each([
  ["newline", "\n", false],
  ["tab", "\t", false],
  ["DEL (0x7f)", "\x7f", false],
  ["newline with reuse_finished", "\n", true],
] as const)("spawn: error — id containing %s → ErrInvalidFlags, no row, no session", async (_label, ctl, reuse) => {
  await withTempHome(async (homeDir) => {
    const storePath = homeStore(homeDir);
    const logPath = path.join(homeDir, "fake-tmux.log");
    const prefix = `ctl-id-${crypto.randomUUID().slice(0, 8)}`;
    seedOuterParent(storePath);
    using client = await openClient(storePath);
    await withProcessEnv({ FAKE_TMUX_LOG: logPath }, async () => {
      const before = (await client.list({})).spawns.length;
      const err = await rejection(
        spawnClaude(client, { cwd: homeDir, instance_id: `${prefix}${ctl}x`, ...(reuse ? { reuse_finished: true } : {}) })
      );
      expect(err).toBeInstanceOf(ErrInvalidFlags);
      expect((err as AgentDirectorError).message).toContain("the instance id contains a control character");
      expect((err as AgentDirectorError).message).not.toContain(prefix);
      const after = (await client.list({})).spawns;
      expect(after.length).toBe(before);
      expect(after.some((r) => r.instance_id.startsWith(prefix))).toBe(false);
      expect(newSessionCount(logPath)).toBe(0);

      // Control: a clean id in the same wiring records new-session (the log is routed).
      expect((await spawnClaude(client, { cwd: homeDir, instance_id: `${prefix}-ok` })).instance_id).toBe(`${prefix}-ok`);
      expect(newSessionCount(logPath)).toBe(1);
    });
  });
}, 20_000);

// SR-1.4, SR-4.5, SR-9.2: each Agent Default, false and wait_turn included, reaches the coverage
// check, which refuses it for codex (busy send covers only claude), fresh or reused.
test.each([
  ["fresh", false],
  ["reusing an ended row", true],
] as const)("spawn: error — Agent Defaults for codex, %s → ErrCantSend agent_not_covered", async (_label, reuse) => {
  await withTempHome(async (homeDir) => {
    const storePath = homeStore(homeDir);
    const id = `defaults-${crypto.randomUUID().slice(0, 8)}`;
    seedOuterParent(storePath);
    if (reuse) runHelper("seed-spawn", { store: storePath, id, state: "ended", agent: "codex", "create-store": true, socket: privateTmuxSocket(homeDir) });
    using client = await openClient(storePath);
    const err = await rejection(client.spawn({ agent: "codex", cwd: homeDir, instance_id: id, reuse_finished: reuse,
      send_mode: "wait_turn", confirmed_delivery: false, confirm_from_transcripts: false }));
    expectAgentNotCovered(err, "send_mode, confirmed_delivery, confirm_from_transcripts");
  });
}, 10_000);

// SR-10.1: reuse_finished has no effect without an explicit id; the spawn mints one.
test("spawn: reuse_finished without instance_id → fresh spawn with a minted id", async () => {
  await withTempHome(async (homeDir) => {
    seedOuterParent(homeStore(homeDir));
    using client = await openClient(homeStore(homeDir));
    const result = await spawnClaude(client, { cwd: homeDir, reuse_finished: true });
    expect((await client.get({ instance_id: result.instance_id })).state).toBe("pending");
  });
}, 10_000);

// SR-10.2: with the opt-in an ended row whose session is gone is reused: one new session, row pending.
test("spawn: reuse_finished over an ended row → reused, get shows pending", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = homeStore(homeDir);
    const logPath = path.join(homeDir, "fake-tmux.log");
    const id = `reuse-ended-${crypto.randomUUID().slice(0, 8)}`;
    seedOuterParent(storePath);
    runHelper("seed-spawn", { store: storePath, id, state: "ended", "create-store": true, socket: privateTmuxSocket(homeDir) });
    using client = await openClient(storePath);
    await withProcessEnv({ FAKE_TMUX_LOG: logPath }, async () => {
      expect((await spawnClaude(client, { cwd: homeDir, instance_id: id, reuse_finished: true })).instance_id).toBe(id);
      expect((await client.get({ instance_id: id })).state).toBe("pending");
      expect(newSessionCount(logPath)).toBe(1);
    });
  });
}, 10_000);

// The reused row takes the new spawn's agent, not the ended row's (get shows it).
test("spawn: reuse_finished over an ended codex row, spawned as claude → the row now runs claude", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = homeStore(homeDir);
    const id = `reuse-agent-${crypto.randomUUID().slice(0, 8)}`;
    seedOuterParent(storePath);
    runHelper("seed-spawn", { store: storePath, id, state: "ended", agent: "codex", "create-store": true, socket: privateTmuxSocket(homeDir) });
    using client = await openClient(storePath);
    expect((await client.get({ instance_id: id })).agent).toBe("codex");
    const result = await spawnClaude(client, { cwd: homeDir, instance_id: id, reuse_finished: true, agent_args: ["--resume", "S1"] });
    expect(result.dropped_args).toEqual(["--resume", "S1"]);
    expect(await client.get({ instance_id: id })).toMatchObject({ state: "pending", agent: "claude", agent_args: [], session_id: "", prior_sessions: [] });
  });
}, 10_000);

// AC-REUSE-13: without the opt-in an ended row still collides; the row is unchanged and no session is created.
test.each([
  ["absent", {}],
  ["false", { reuse_finished: false }],
] as const)("spawn: error — ended row, reuse_finished %s → ErrInstanceIdCollision", async (_label, extra) => {
  await withTempHome(async (homeDir) => {
    const storePath = homeStore(homeDir);
    const logPath = path.join(homeDir, "fake-tmux.log");
    const id = `reuse-ended-${crypto.randomUUID().slice(0, 8)}`;
    seedOuterParent(storePath);
    runHelper("seed-spawn", { store: storePath, id, state: "ended", "create-store": true });
    using client = await openClient(storePath);
    await withProcessEnv({ FAKE_TMUX_LOG: logPath }, async () => {
      const before = await client.get({ instance_id: id });
      expect(before.state).toBe("ended");
      await expect(spawnClaude(client, { cwd: homeDir, instance_id: id, ...extra })).rejects.toBeInstanceOf(ErrInstanceIdCollision);
      expect(await client.get({ instance_id: id })).toEqual(before);
      expect(newSessionCount(logPath)).toBe(0);

      // Control: a fresh id in the same wiring records new-session.
      await spawnClaude(client, { cwd: homeDir, instance_id: `${id}-ok` });
      expect(newSessionCount(logPath)).toBe(1);
    });
  });
}, 20_000);

test.each([
  ["same agent short", "claude", ["-rS1"], "-r"],
  ["switch print", "codex", ["-pc"], "-p"],
  ["switch value owned long", "codex", ["--append-system-prompt", "--resume", "S1"], "--resume"],
] as const)("spawn: typed argument refusal — %s", async (_label, agent, args, bare) => {
  await withTempHome(async (homeDir) => {
    const storePath = homeStore(homeDir);
    const id = `reuse-policy-${crypto.randomUUID().slice(0, 8)}`;
    const logPath = path.join(homeDir, "fake-tmux.log");
    seedOuterParent(storePath);
    runHelper("seed-spawn", { store: storePath, id, state: "ended", agent, "create-store": true, socket: privateTmuxSocket(homeDir) });
    using client = await openClient(storePath);
    await withProcessEnv({ FAKE_TMUX_LOG: logPath }, async () => {
      const before = await client.get({ instance_id: id });
      const err = await rejection(spawnClaude(client, { cwd: homeDir, instance_id: id, reuse_finished: true, agent_args: [...args] }));
      expect(err).toBeInstanceOf(ErrSpawnDeniedFlag);
      expect((err as AgentDirectorError).message).toBe(`ErrSpawnDeniedFlag: ${bare}`);
      expect((err as AgentDirectorError).message).not.toContain("-rS1");
      expect(await client.get({ instance_id: id })).toEqual(before);
      expect(newSessionCount(logPath)).toBe(0);
    });
  });
}, 10_000);
