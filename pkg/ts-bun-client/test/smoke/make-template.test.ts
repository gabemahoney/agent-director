/**
 * Smoke test — make-template verb
 *
 * Happy path: call with a safe name and cwd="/tmp". make-template writes
 * ~/.agent-director/templates/<name>.toml under the REAL HOME (Go's
 * os.UserHomeDir() in the FFI worker always returns the process HOME, not
 * the per-test temp HOME set by withTempHome). A unique suffix prevents
 * ErrTemplateExists across runs; the file is deleted in a finally block.
 *
 * Error path: name containing "/" ("a/b") → ErrTemplateNameUnsafe.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import * as fs from "fs";
import { withTempHome } from "../internal/tempHome.js";
import { Client, ErrTemplateNameUnsafe, AgentDirectorError } from "../../src/index.js";
import type { MakeTemplateResult } from "../../src/index.js";

test("make-template: happy path — writes template file under HOME", async () => {
  await withTempHome(async (homeDir) => {
    // Use a run-unique name so ErrTemplateExists never fires across runs.
    const templateName = `smoke-template-${Date.now()}`;
    const storePath = path.join(homeDir, "state.db");
    using client = new Client({ storePath, createIfMissing: true });
    let templatePath: string | undefined;
    try {
      const result: MakeTemplateResult = await client.makeTemplate({
        name: templateName,
        cwd: "/tmp",
      });
      templatePath = result.path;
      expect(typeof result.path).toBe("string");
      // The file name always ends with the expected suffix.
      expect(result.path.endsWith(`${templateName}.toml`)).toBe(true);
    } finally {
      // Clean up the file Go wrote to the real HOME (~/.agent-director/templates/).
      if (templatePath) {
        try { fs.unlinkSync(templatePath); } catch { /* best-effort */ }
      }
    }
  });
}, 10_000);

test("make-template: error — unsafe name with path separator → ErrTemplateNameUnsafe", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, "state.db");
    using client = new Client({ storePath, createIfMissing: true });

    let caught: unknown;
    try {
      await client.makeTemplate({ name: "a/b" });
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(ErrTemplateNameUnsafe);
    expect(caught).toBeInstanceOf(AgentDirectorError);
    expect(caught).toBeInstanceOf(Error);
  });
}, 10_000);
