/**
 * Smoke test — make-template verb
 *
 * Happy path: a safe name writes <name>.toml under the CLI's HOME (the temp
 * HOME); the name is run-unique so ErrTemplateExists never fires.
 * Error path: a name with "/" → ErrTemplateNameUnsafe.
 */

import { test, expect } from "bun:test";
import { withTempHome } from "../internal/tempHome.js";
import { homeStore, openClient } from "../internal/helper.js";
import { ErrTemplateNameUnsafe } from "../../src/index.js";

test("make-template: happy path — writes template file under HOME", async () => {
  await withTempHome(async (homeDir) => {
    const name = `smoke-template-${Date.now()}`;
    using client = await openClient(homeStore(homeDir));
    const result = await client.makeTemplate({ name, cwd: "/tmp" });
    expect(result.path.endsWith(`${name}.toml`)).toBe(true);
    expect(result.path.startsWith(homeDir)).toBe(true);
  });
}, 10_000);

test("make-template: error — unsafe name with path separator → ErrTemplateNameUnsafe", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    await expect(client.makeTemplate({ name: "a/b" })).rejects.toBeInstanceOf(ErrTemplateNameUnsafe);
  });
}, 10_000);
