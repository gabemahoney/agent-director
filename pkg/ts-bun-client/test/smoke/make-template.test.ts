/**
 * Smoke test — make-template verb
 *
 * Happy path: a safe name writes <name>.toml under the CLI's HOME (the temp
 * HOME); the name is run-unique so ErrTemplateExists never fires.
 * The agent is written to the file (SR-1.1); agent_args without an agent → ErrTemplateAgentMissing.
 * Agent Defaults: an agent-less template stores them as given; with agent
 * codex → ErrCantSend agent_not_covered (SR-1.4, SR-9.2).
 * Error path: a name with "/" → ErrTemplateNameUnsafe.
 */

import { test, expect } from "bun:test";
import * as fs from "fs";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { homeStore, openClient, makeTemplateClaude, rejection, expectAgentNotCovered } from "../internal/helper.js";
import { ErrTemplateNameUnsafe, ErrTemplateAgentMissing } from "../../src/index.js";

test("make-template: happy path — writes template file under HOME", async () => {
  await withTempHome(async (homeDir) => {
    const name = `smoke-template-${Date.now()}`;
    using client = await openClient(homeStore(homeDir));
    const result = await makeTemplateClaude(client, { name, cwd: "/tmp", agent_args: ["--model", "opus"] });
    expect(result.path.endsWith(`${name}.toml`)).toBe(true);
    expect(result.path.startsWith(homeDir)).toBe(true);
    expect(fs.readFileSync(result.path, "utf8")).toContain('agent = "claude"');
  });
}, 10_000);

// SR-1.4, SR-4.5: a template naming no agent stores each Agent Default with the value given,
// for the spawn that uses it to decide.
test("make-template: happy path — an agent-less template stores the Agent Defaults as given", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    const result = await client.makeTemplate({ name: `smoke-template-defaults-${Date.now()}`, send_mode: "steer_turn",
      confirmed_delivery: false, confirm_from_transcripts: true });
    const toml = fs.readFileSync(result.path, "utf8");
    for (const line of ['send_mode = "steer_turn"', "confirmed_delivery = false", "confirm_from_transcripts = true"]) expect(toml).toContain(line);
  });
}, 10_000);

// SR-9.2: a template naming codex, which busy send doesn't cover, refuses them.
test("make-template: error — Agent Defaults with agent codex → ErrCantSend agent_not_covered", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    const err = await rejection(client.makeTemplate({ agent: "codex", name: `smoke-template-codex-defaults-${Date.now()}`, send_mode: "wait_turn",
      confirmed_delivery: false, confirm_from_transcripts: false }));
    expectAgentNotCovered(err, "send_mode, confirmed_delivery, confirm_from_transcripts");
  });
}, 10_000);

test("make-template: error — agent_args with no agent → ErrTemplateAgentMissing, nothing written", async () => {
  await withTempHome(async (homeDir) => {
    const name = `smoke-template-noagent-${Date.now()}`;
    using client = await openClient(homeStore(homeDir));
    await expect(client.makeTemplate({ name, agent_args: ["--model", "opus"] })).rejects.toBeInstanceOf(ErrTemplateAgentMissing);
    expect(fs.existsSync(path.join(homeDir, ".config", "agent-director", "templates", `${name}.toml`))).toBe(false);
  });
}, 10_000);

test("make-template: error — unsafe name with path separator → ErrTemplateNameUnsafe", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    await expect(makeTemplateClaude(client, { name: "a/b" })).rejects.toBeInstanceOf(ErrTemplateNameUnsafe);
  });
}, 10_000);
