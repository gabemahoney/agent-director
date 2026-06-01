#!/usr/bin/env bun
/**
 * verify-installed-pkg.ts — post-install smoke driver for agent-director.
 *
 * Usage:
 *   bun run scripts/verify-installed-pkg.ts --smoke
 *   bun run scripts/verify-installed-pkg.ts --full
 *
 * Modes:
 *   --smoke   Basic functional check: construct a Client, call version(),
 *             assert the response shape. Exits 0 on success, non-zero on
 *             any failure.
 *   --full    Runs --smoke assertions first, then executes the makeTemplate
 *             overwrite gauntlet (SR-1.2). Exits 0 on success, non-zero on
 *             any failure.
 *
 * Dev-only env vars (for in-repo development; not used in production):
 *   AD_VERIFY_AGAINST=<path>   Load Client from this index.ts path instead
 *                              of the installed "agent-director" package.
 *   AD_CLI_PATH=<path>         Pass as _cliPath to the Client constructor to
 *                              bypass platform-package CLI resolution. Use with
 *                              the in-repo dist/agent-director-linux-amd64
 *                              binary during local development.
 *   EXPECTED_VERSION=<semver>  When set, assert client.version().version equals
 *                              this value (e.g. "0.6.3" — the npm package
 *                              version returned by client.version() per b.6o1,
 *                              not the binary's git-describe stamp). Set by
 *                              release.sh verify_phase to catch version-stamp
 *                              regressions (b.6oj). Unset or empty → skipped.
 *
 * Production codepath: bare `import { Client } from "agent-director"` with no
 * env var overrides. The packed tarball must resolve correctly.
 */

import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

// ---------------------------------------------------------------------------
// Flag parsing
// ---------------------------------------------------------------------------

const args = process.argv.slice(2);
const smokeFlag = args.includes("--smoke");
const fullFlag = args.includes("--full");

if (smokeFlag && fullFlag) {
  process.stderr.write(
    "verify-installed-pkg: --smoke and --full are mutually exclusive\n"
  );
  process.exit(1);
}

if (!smokeFlag && !fullFlag) {
  process.stderr.write(
    "verify-installed-pkg: no mode flag supplied. Usage: verify-installed-pkg.ts --smoke | --full\n"
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

type ClientInstance = {
  version(p: Record<never, never>): Promise<unknown>;
  makeTemplate(p: { name: string; overwrite?: boolean; cwd?: string }): Promise<{ path: string }>;
  getPermission(p: { request_token: string }): Promise<unknown>;
  decide(p: { claude_instance_id: string; decision: string; request_token?: string }): Promise<unknown>;
  close(): void;
};

type ClientCtor = new(opts: Record<string, unknown>) => ClientInstance;

type ErrTemplateExistsCtor = new(...args: unknown[]) => Error;
type ErrPermissionRequestNotFoundCtor = new(...args: unknown[]) => Error;
type ErrInvalidFlagsCtor = new(...args: unknown[]) => Error;

let Client: ClientCtor;
let ErrTemplateExists: ErrTemplateExistsCtor;
let ErrPermissionRequestNotFound: ErrPermissionRequestNotFoundCtor;
let ErrInvalidFlags: ErrInvalidFlagsCtor;

const devPath = process.env.AD_VERIFY_AGAINST;
if (devPath) {
  const mod = await import(devPath) as {
    Client: ClientCtor;
    ErrTemplateExists: ErrTemplateExistsCtor;
    ErrPermissionRequestNotFound: ErrPermissionRequestNotFoundCtor;
    ErrInvalidFlags: ErrInvalidFlagsCtor;
  };
  Client = mod.Client;
  ErrTemplateExists = mod.ErrTemplateExists;
  ErrPermissionRequestNotFound = mod.ErrPermissionRequestNotFound;
  ErrInvalidFlags = mod.ErrInvalidFlags;
} else {
  const mod = await import("agent-director") as {
    Client: ClientCtor;
    ErrTemplateExists: ErrTemplateExistsCtor;
    ErrPermissionRequestNotFound: ErrPermissionRequestNotFoundCtor;
    ErrInvalidFlags: ErrInvalidFlagsCtor;
  };
  Client = mod.Client;
  ErrTemplateExists = mod.ErrTemplateExists;
  ErrPermissionRequestNotFound = mod.ErrPermissionRequestNotFound;
  ErrInvalidFlags = mod.ErrInvalidFlags;
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

    // Value-assertion: when EXPECTED_VERSION is set (e.g. by release.sh
    // verify_phase), confirm the binary reports the correct release tag.
    // Skipped when unset or empty to preserve the local-run path. (b.6oj)
    const expected = process.env.EXPECTED_VERSION;
    if (expected !== undefined && expected !== "") {
      const got = (result as Record<string, unknown>).version as string;
      if (got !== expected) {
        throw new Error(
          `verify-installed-pkg --smoke: version mismatch — got "${got}", expected "${expected}"`
        );
      }
    }

    client.close();
  } finally {
    try {
      fs.rmSync(tmpDir, { recursive: true, force: true });
    } catch {
      // best-effort cleanup
    }
  }
}

// ---------------------------------------------------------------------------
// --full mode: run --smoke, then the makeTemplate overwrite gauntlet (SR-1.2).
// ---------------------------------------------------------------------------

class StepFailure extends Error {
  readonly step: string;
  constructor(step: string) {
    super(step);
    this.step = step;
  }
}

async function runFull(): Promise<void> {
  await runSmoke();

  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "ad-verify-full-"));
  const storePath = path.join(tmpDir, "state.db");
  const devCliPath = process.env.AD_CLI_PATH;

  const ctorOpts: Record<string, unknown> = {
    storePath,
    createIfMissing: true,
  };
  if (devCliPath) {
    ctorOpts._cliPath = devCliPath;
  }

  const client = new Client(ctorOpts);

  // Use a run-unique name so a leftover from a crashed prior run never causes
  // makeTemplate-create to spuriously fail with ErrTemplateExists.
  const templateName = `verify-overwrite-${Date.now()}`;
  let templatePath: string | undefined;

  try {
    // makeTemplate-create: first call with overwrite:false should succeed.
    try {
      const r = await client.makeTemplate({ name: templateName, overwrite: false });
      templatePath = r.path;
    } catch {
      throw new StepFailure("makeTemplate-create");
    }

    // makeTemplate-collision: repeat call should return ErrTemplateExists.
    let collisionErr: unknown;
    try {
      await client.makeTemplate({ name: templateName, overwrite: false });
    } catch (e) {
      collisionErr = e;
    }
    if (!(collisionErr instanceof ErrTemplateExists)) {
      throw new StepFailure("makeTemplate-collision");
    }

    // makeTemplate-overwrite: same call with overwrite:true should succeed.
    let overwriteResult!: { path: string };
    try {
      overwriteResult = await client.makeTemplate({
        name: templateName,
        overwrite: true,
        cwd: tmpDir,
      });
    } catch {
      throw new StepFailure("makeTemplate-overwrite");
    }

    // makeTemplate-reread: file must reflect step 3's cwd, not step 1's absence.
    try {
      const contents = fs.readFileSync(overwriteResult.path, "utf8");
      if (!contents.includes(tmpDir)) {
        throw new Error("cwd marker absent from template file");
      }
    } catch {
      throw new StepFailure("makeTemplate-reread");
    }

    // getPermission-not-found: a well-formed UUID that cannot exist in a
    // freshly-created store must return ErrPermissionRequestNotFound.
    let gpErr: unknown;
    try {
      await client.getPermission({ request_token: "00000000-0000-0000-0000-000000000000" });
    } catch (e) {
      gpErr = e;
    }
    if (!(gpErr instanceof ErrPermissionRequestNotFound)) {
      throw new StepFailure("getPermission-not-found");
    }

    // decide-missing-request-token: calling decide without request_token must
    // return ErrInvalidFlags (the CLI flag-level validator fires before the
    // store lookup and rejects the call since m61 E1).
    let dtErr: unknown;
    try {
      await client.decide({ claude_instance_id: "00000000-0000-0000-0000-000000000001", decision: "allow" });
    } catch (e) {
      dtErr = e;
    }
    if (!(dtErr instanceof ErrInvalidFlags)) {
      throw new StepFailure("decide-missing-request-token");
    }

    // decide-with-request-token is out of scope for b.bwn: `seed-permission-request`
    // lives in the `helper`-tagged ts-helper binary, not the production CLI;
    // testing this path requires either a fixture binary on PATH or expanding
    // verify-installed-pkg-full's Makefile scope, both out of scope for b.bwn.

    client.close();
  } finally {
    if (templatePath) {
      try { fs.unlinkSync(templatePath); } catch { /* best-effort */ }
    }
    try {
      fs.rmSync(tmpDir, { recursive: true, force: true });
    } catch {
      // best-effort cleanup
    }
  }
}

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

if (smokeFlag) {
  try {
    await runSmoke();
    console.log("verify-installed-pkg --smoke: OK");
    process.exit(0);
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    process.stderr.write(`verify-installed-pkg --smoke: FAILED — ${msg}\n`);
    process.exit(1);
  }
} else {
  try {
    await runFull();
    console.log("verify-installed-pkg --full: OK");
    process.exit(0);
  } catch (err) {
    if (err instanceof StepFailure) {
      process.stderr.write(`FAIL ${err.step}\n`);
    } else {
      const msg = err instanceof Error ? err.message : String(err);
      process.stderr.write(`verify-installed-pkg --full: FAILED — ${msg}\n`);
    }
    process.exit(1);
  }
}
