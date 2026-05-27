#!/usr/bin/env bun
/**
 * fake-client — configurable fake Client module for verify-installed-pkg --full
 * failure injection tests.
 *
 * Point AD_VERIFY_AGAINST at this file and set FAKE_CLIENT_FAIL_STEP to inject
 * a failure at the named gauntlet sub-step:
 *
 *   makeTemplate-create     → first makeTemplate call throws
 *   makeTemplate-collision  → second call does NOT throw ErrTemplateExists
 *   makeTemplate-overwrite  → third (overwrite:true) call throws
 *   makeTemplate-reread     → third call writes file without cwd marker
 *
 * When FAKE_CLIENT_FAIL_STEP is unset the client behaves as a happy-path fake.
 */

import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

export class ErrTemplateExists extends Error {
  constructor() {
    super("template already exists");
    this.name = "ErrTemplateExists";
  }
}

export class Client {
  private makeTemplateCallCount = 0;

  constructor(_opts: Record<string, unknown>) {}

  async version(_: Record<never, never>): Promise<{ version: string; commit: string }> {
    return { version: "0.0.0-fake", commit: "0000000" };
  }

  async makeTemplate(params: {
    name: string;
    overwrite?: boolean;
    cwd?: string;
  }): Promise<{ path: string }> {
    this.makeTemplateCallCount++;
    const call = this.makeTemplateCallCount;
    const failStep = process.env.FAKE_CLIENT_FAIL_STEP;

    if (call === 1) {
      if (failStep === "makeTemplate-create") {
        throw new Error("injected: makeTemplate-create failure");
      }
      return { path: writeTmp(params.name, call, params.cwd) };
    }

    if (call === 2) {
      if (failStep === "makeTemplate-collision") {
        // Succeed instead of throwing — driver expects ErrTemplateExists here
        // and will emit FAIL makeTemplate-collision when it doesn't arrive.
        return { path: writeTmp(params.name, call, params.cwd) };
      }
      throw new ErrTemplateExists();
    }

    if (call === 3) {
      if (failStep === "makeTemplate-overwrite") {
        throw new Error("injected: makeTemplate-overwrite failure");
      }
      // For reread failure: write file without the cwd marker so the driver's
      // contents.includes(tmpDir) check fails.
      const includeCwd = failStep !== "makeTemplate-reread";
      return { path: writeTmp(params.name, call, includeCwd ? params.cwd : undefined) };
    }

    throw new Error(`unexpected makeTemplate call #${call}`);
  }

  close(): void {}
}

function writeTmp(name: string, call: number, cwd: string | undefined): string {
  const p = path.join(os.tmpdir(), `fake-client-${name}-${call}-${Date.now()}.toml`);
  const cwdLine = cwd != null ? `cwd = "${cwd}"\n` : "";
  fs.writeFileSync(p, `name = "${name}"\n${cwdLine}`);
  return p;
}
