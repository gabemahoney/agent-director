/**
 * Smoke test — make-template verb
 *
 * Happy path: a safe name writes <name>.toml under the CLI's HOME (the temp
 * HOME); the name is run-unique so ErrTemplateExists never fires.
 * The agent is written to the file (SR-1.1); agent_args without an agent → ErrTemplateAgentMissing.
 * Error path: a name with "/" → ErrTemplateNameUnsafe.
 */

import { test, expect } from "bun:test";
import * as fs from "fs";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { homeStore, openClient, makeTemplateClaude } from "../internal/helper.js";
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

test("make-template: error — agent_args with no agent → ErrTemplateAgentMissing, nothing written", async () => {
  await withTempHome(async (homeDir) => {
    const name = `smoke-template-noagent-${Date.now()}`;
    using client = await openClient(homeStore(homeDir));
    await expect(client.makeTemplate({ name, agent_args: ["--model", "opus"] })).rejects.toBeInstanceOf(ErrTemplateAgentMissing);
    expect(fs.existsSync(path.join(homeDir, ".agent-director", "templates", `${name}.toml`))).toBe(false);
  });
}, 10_000);

test("make-template: error — unsafe name with path separator → ErrTemplateNameUnsafe", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    await expect(makeTemplateClaude(client, { name: "a/b" })).rejects.toBeInstanceOf(ErrTemplateNameUnsafe);
  });
}, 10_000);
