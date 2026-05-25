#!/usr/bin/env bun
/**
 * verify-installed-pkg.ts — post-install smoke driver for agent-director.
 *
 * Usage:
 *   bun run scripts/verify-installed-pkg.ts --smoke
 *
 * Modes:
 *   --smoke   Basic functional check: construct a Client, call version(),
 *             assert the response shape. Exits 0 on success, non-zero on
 *             any failure.
 *
 * Dev-only env vars (for in-repo development; not used in production):
 *   AD_VERIFY_AGAINST=<path>   Load Client from this index.ts path instead
 *                              of the installed "agent-director" package.
 *   AD_CLI_PATH=<path>         Pass as _cliPath to the Client constructor to
 *                              bypass platform-package CLI resolution. Use with
 *                              the in-repo dist/agent-director-linux-amd64
 *                              binary during local development.
 *
 * Production codepath: bare `import { Client } from "agent-director"` with no
 * env var overrides. The packed tarball must resolve correctly.
 *
 * b.iaq SR-1.5: the --full mode (makeTemplate assertion) will be added in
 * Task D1. Leave a placeholder comment where it will go.
 */

import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

// ---------------------------------------------------------------------------
// Flag parsing
// ---------------------------------------------------------------------------

const args = process.argv.slice(2);

if (!args.includes("--smoke")) {
  process.stderr.write(
    "verify-installed-pkg: no mode flag supplied. Usage: verify-installed-pkg.ts --smoke\n"
  );
  process.exit(1);
}

// ---------------------------------------------------------------------------
// Client resolution
//
// Production path: bare "agent-director" import (resolved by Bun from the
// installed package in node_modules).
//
// Dev path: AD_VERIFY_AGAINST env var points at an in-repo index.ts so this
// script can be run without a packed tarball during development.
// ---------------------------------------------------------------------------

type ClientCtor = new(opts: Record<string, unknown>) => {
  version(p: Record<never, never>): Promise<unknown>;
  close(): void;
};

let Client: ClientCtor;

const devPath = process.env.AD_VERIFY_AGAINST;
if (devPath) {
  const mod = await import(devPath) as { Client: ClientCtor };
  Client = mod.Client;
} else {
  const mod = await import("agent-director") as { Client: ClientCtor };
  Client = mod.Client;
}

// ---------------------------------------------------------------------------
// --smoke mode: construct a Client, call version(), assert response shape.
// ---------------------------------------------------------------------------

async function runSmoke(): Promise<void> {
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "ad-verify-"));
  const storePath = path.join(tmpDir, "state.db");

  // Dev override: bypass platform-package CLI resolution when AD_CLI_PATH is
  // set. The production codepath never sets this env var.
  const devCliPath = process.env.AD_CLI_PATH;

  try {
    const ctorOpts: Record<string, unknown> = {
      storePath,
      createIfMissing: true,
    };
    if (devCliPath) {
      // _cliPath is a test-only DI hook on SubprocessClient that bypasses
      // resolveCliPath(). Undocumented on ClientOptions; cast through unknown.
      ctorOpts._cliPath = devCliPath;
    }

    const client = new Client(ctorOpts);

    // version() should return { version: string, commit: string } (SRD SR-7).
    const result = await client.version({});

    if (
      typeof result !== "object" ||
      result === null ||
      typeof (result as Record<string, unknown>).version !== "string" ||
      typeof (result as Record<string, unknown>).commit !== "string"
    ) {
      throw new Error(
        `verify-installed-pkg --smoke: version() returned unexpected shape: ${JSON.stringify(result)}`
      );
    }

    // TODO(b.iaq Task D1): add --full mode with makeTemplate({overwrite: true}) assertion per SRD SR-9.2.

    client.close();
  } finally {
    try {
      fs.rmSync(tmpDir, { recursive: true, force: true });
    } catch {
      // best-effort cleanup
    }
  }
}

try {
  await runSmoke();
  console.log("verify-installed-pkg --smoke: OK");
  process.exit(0);
} catch (err) {
  const msg = err instanceof Error ? err.message : String(err);
  process.stderr.write(`verify-installed-pkg --smoke: FAILED — ${msg}\n`);
  process.exit(1);
}
