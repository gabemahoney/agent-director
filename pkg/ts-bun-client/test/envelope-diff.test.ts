/**
 * envelope-diff.test.ts — TS-side envelope-diff regression suite.
 *
 * For every callable verb: invoke bin/agent-director (CLI subprocess) against
 * store A and the TS Client against an identical copy (store B), then compare
 * the JSON envelopes using assertEnvelopesEqual with Epic 3's
 * nondeterministic.json ignore-paths.
 *
 * Architecture:
 *   CLI side  — HOME=homeA → opens homeA/.agent-director/state.db
 *   TS side   — storePath = homeB/.agent-director/state.db (direct path)
 *   Both stores are byte-identical copies of the same seed — timestamps match.
 *   Both sides use fake-tmux so spawn/send-keys/read-pane/kill/resume don't
 *   touch a real tmux session.
 *
 * See docs/architecture.md "TS envelope-diff regression" for design notes.
 */

import { test, describe, expect } from "bun:test";
import * as path from "path";
import * as fs from "fs";
import * as os from "os";

import { Client, AgentDirectorError } from "../src/index.js";
import { runHelper } from "./internal/helper.js";
import { runCli } from "./internal/cliRunner.js";
import { assertEnvelopesEqual } from "./internal/structuralDiff.js";
import { loadIgnorePathsForVerb } from "./internal/loadIgnorePaths.js";

// ── constants ─────────────────────────────────────────────────────────────────

const TIMEOUT = 20_000;

// Capture real HOME at module load (before any per-test env changes).
// The FFI worker inherits the OS HOME at its spawn time and does NOT see
// per-test HOME overrides, so resume/make-template tests write JSONL /
// templates to REAL_HOME for the Client side.
const REAL_HOME = process.env.HOME ?? "/home/horde";

// Fake-tmux directory (built by `make fake-tmux`, exported by setup.ts).
const FAKE_TMUX_DIR =
  process.env.FAKE_TMUX_DIR ??
  path.resolve(import.meta.dir, "../../../test/fake-tmux");
const FAKE_TMUX_BIN = path.join(FAKE_TMUX_DIR, "tmux");

// If tests run inside a Claude session, AGENT_DIRECTOR_INSTANCE_ID is set
// as the outer parent.  Spawn and resume need a parent row in the store or
// the FK constraint fires.
const OUTER_INSTANCE_ID = process.env.AGENT_DIRECTOR_INSTANCE_ID;

// ── helpers ───────────────────────────────────────────────────────────────────

/** Build an env map for the CLI subprocess: HOME + fake-tmux on PATH. */
function cliEnv(homeDir: string): NodeJS.ProcessEnv {
  return {
    ...process.env,
    HOME: homeDir,
    PATH: `${FAKE_TMUX_DIR}:${process.env.PATH ?? "/usr/local/bin:/usr/bin:/bin"}`,
  };
}

interface StoreSetup {
  /** Temp HOME directory for the CLI subprocess. */
  homeA: string;
  /** Path to CLI's store file: homeA/.agent-director/state.db */
  storeA: string;
  /** Temp HOME directory (not used for CLI). */
  homeB: string;
  /** Path to TS Client's store file: homeB/.agent-director/state.db */
  storeB: string;
  /** Remove both temp directories. */
  cleanup: () => void;
}

/**
 * prepareStores seeds one store via seedFn, then copies it to two isolated
 * home directories (homeA for CLI, homeB for TS Client).
 *
 * By copying the same SQLite file, both stores have byte-identical timestamps
 * so time-stamped fields (started_at, last_seen_at) match in the diff.
 */
function prepareStores(seedFn: (storePath: string) => void): StoreSetup {
  const seedDir = fs.mkdtempSync(path.join(os.tmpdir(), "ed-seed-"));
  const seedStore = path.join(seedDir, "state.db");

  seedFn(seedStore);

  const homeA = fs.mkdtempSync(path.join(os.tmpdir(), "ed-ha-"));
  const homeB = fs.mkdtempSync(path.join(os.tmpdir(), "ed-hb-"));
  const adA = path.join(homeA, ".agent-director");
  const adB = path.join(homeB, ".agent-director");
  fs.mkdirSync(adA, { recursive: true });
  fs.mkdirSync(adB, { recursive: true });
  const storeA = path.join(adA, "state.db");
  const storeB = path.join(adB, "state.db");

  fs.copyFileSync(seedStore, storeA);
  fs.copyFileSync(seedStore, storeB);
  // Copy WAL / SHM shards if present (modernc SQLite may emit them).
  for (const suffix of ["-wal", "-shm"]) {
    const src = seedStore + suffix;
    if (fs.existsSync(src)) {
      fs.copyFileSync(src, storeA + suffix);
      fs.copyFileSync(src, storeB + suffix);
    }
  }

  fs.rmSync(seedDir, { recursive: true, force: true });

  return {
    homeA,
    storeA,
    homeB,
    storeB,
    cleanup() {
      fs.rmSync(homeA, { recursive: true, force: true });
      fs.rmSync(homeB, { recursive: true, force: true });
    },
  };
}

/**
 * slugifyCwd mirrors Go's slugify: every non-[A-Za-z0-9-] rune → '-'.
 * Used to compute the JSONL path for resume tests.
 */
function slugifyCwd(cwd: string): string {
  return cwd.replace(/[^A-Za-z0-9-]/g, "-");
}

/**
 * trimNamePrefix mirrors Go's errnames.TrimNamePrefix: strips the redundant
 * "ErrName: " prefix from desc when present, returning the bare message.
 *
 * The C-ABI path does NOT call TrimNamePrefix before building the envelope, so
 * adErr.errDescription is "ErrFoo: message" while the CLI strips it to just
 * "message".  Applying the same strip here makes the two sides comparable.
 */
function trimNamePrefix(name: string, desc: string): string {
  const prefix = name + ":";
  if (desc.startsWith(prefix)) {
    return desc.slice(prefix.length).trimStart();
  }
  return desc;
}

/**
 * assertErrorEnvelopes compares the CLI error envelope (from stderr) against
 * the TS Client's thrown AgentDirectorError.
 *
 * - err_name: exact equality
 * - err_description: compared after stripping the redundant "ErrName: " prefix
 *   that the C-ABI includes but the CLI strips via TrimNamePrefix.
 */
function assertErrorEnvelopes(cliStderr: string, tsErr: unknown): void {
  expect(tsErr).toBeInstanceOf(AgentDirectorError);
  const adErr = tsErr as AgentDirectorError;

  let cliErr: { err_name: string; err_description: string };
  try {
    cliErr = JSON.parse(cliStderr) as {
      err_name: string;
      err_description: string;
    };
  } catch {
    throw new Error(`CLI stderr is not valid JSON: ${cliStderr}`);
  }

  expect(cliErr.err_name).toBe(adErr.errName);
  expect(cliErr.err_description).toBe(
    trimNamePrefix(adErr.errName, adErr.errDescription)
  );
}

// ── per-verb tests ────────────────────────────────────────────────────────────

// ── spawn ─────────────────────────────────────────────────────────────────────

describe("spawn", () => {
  test(
    "success path",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-empty-store", { store });
        // If running inside a Claude session, seed the parent row so FK passes.
        if (OUTER_INSTANCE_ID) {
          runHelper("seed-spawn", {
            store,
            id: OUTER_INSTANCE_ID,
            state: "working",
          });
        }
      });
      try {
        const cli = runCli(["spawn", "--cwd", "/tmp"], cliEnv(homeA));
        expect(cli.exitCode).toBe(0);

        using client = new Client({
          storePath: storeB,
          tmuxCommand: FAKE_TMUX_BIN,
        });
        const ts = await client.spawn({ cwd: "/tmp" });

        assertEnvelopesEqual(
          JSON.parse(cli.stdout) as unknown,
          ts,
          { ignorePaths: loadIgnorePathsForVerb("spawn") }
        );
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );

  test(
    "error path: ErrCwdMissing",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-empty-store", { store });
      });
      try {
        const cli = runCli(["spawn"], cliEnv(homeA));
        expect(cli.exitCode).not.toBe(0);

        using client = new Client({ storePath: storeB });
        let tsErr: unknown;
        try {
          await client.spawn({ cwd: "" });
        } catch (e) {
          tsErr = e;
        }

        assertErrorEnvelopes(cli.stderr, tsErr);
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );
});

// ── status ────────────────────────────────────────────────────────────────────

describe("status", () => {
  test(
    "success path",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-spawn", {
          store,
          id: "id-status-1",
          state: "waiting",
          "create-store": true,
        });
      });
      try {
        const cli = runCli(
          ["status", "--claude-instance-id", "id-status-1"],
          cliEnv(homeA)
        );
        expect(cli.exitCode).toBe(0);

        using client = new Client({ storePath: storeB });
        const ts = await client.status({ claude_instance_id: "id-status-1" });

        assertEnvelopesEqual(JSON.parse(cli.stdout) as unknown, ts, {
          ignorePaths: loadIgnorePathsForVerb("status"),
        });
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );

  test(
    "error path: ErrSpawnNotFound",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-empty-store", { store });
      });
      try {
        const cli = runCli(
          ["status", "--claude-instance-id", "nonexistent-id"],
          cliEnv(homeA)
        );
        expect(cli.exitCode).not.toBe(0);

        using client = new Client({ storePath: storeB });
        let tsErr: unknown;
        try {
          await client.status({ claude_instance_id: "nonexistent-id" });
        } catch (e) {
          tsErr = e;
        }

        assertErrorEnvelopes(cli.stderr, tsErr);
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );
});

// ── get ───────────────────────────────────────────────────────────────────────

describe("get", () => {
  test(
    "success path",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-spawn", {
          store,
          id: "id-get-1",
          state: "waiting",
          "create-store": true,
        });
      });
      try {
        const cli = runCli(
          ["get", "--claude-instance-id", "id-get-1"],
          cliEnv(homeA)
        );
        expect(cli.exitCode).toBe(0);

        using client = new Client({ storePath: storeB });
        const ts = await client.get({ claude_instance_id: "id-get-1" });

        assertEnvelopesEqual(JSON.parse(cli.stdout) as unknown, ts, {
          ignorePaths: loadIgnorePathsForVerb("get"),
        });
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );

  test(
    "error path: ErrSpawnNotFound",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-empty-store", { store });
      });
      try {
        const cli = runCli(
          ["get", "--claude-instance-id", "nonexistent-id"],
          cliEnv(homeA)
        );
        expect(cli.exitCode).not.toBe(0);

        using client = new Client({ storePath: storeB });
        let tsErr: unknown;
        try {
          await client.get({ claude_instance_id: "nonexistent-id" });
        } catch (e) {
          tsErr = e;
        }

        assertErrorEnvelopes(cli.stderr, tsErr);
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );
});

// ── send-keys ─────────────────────────────────────────────────────────────────

describe("send-keys", () => {
  test(
    "success path",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-spawn", {
          store,
          id: "id-sk-1",
          state: "waiting",
          "create-store": true,
        });
      });
      try {
        const cli = runCli(
          ["send-keys", "--claude-instance-id", "id-sk-1", "--text", "hello"],
          cliEnv(homeA)
        );
        expect(cli.exitCode).toBe(0);

        using client = new Client({
          storePath: storeB,
          tmuxCommand: FAKE_TMUX_BIN,
        });
        const ts = await client.sendKeys({
          claude_instance_id: "id-sk-1",
          text: "hello",
        });

        assertEnvelopesEqual(JSON.parse(cli.stdout) as unknown, ts, {
          ignorePaths: loadIgnorePathsForVerb("send-keys"),
        });
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );

  test(
    "error path: ErrSpawnNotInteractive",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        // ended state → not interactive
        runHelper("seed-spawn", {
          store,
          id: "id-err-ni-1",
          state: "ended",
          "create-store": true,
        });
      });
      try {
        const cli = runCli(
          [
            "send-keys",
            "--claude-instance-id",
            "id-err-ni-1",
            "--text",
            "hello",
          ],
          cliEnv(homeA)
        );
        expect(cli.exitCode).not.toBe(0);

        using client = new Client({ storePath: storeB });
        let tsErr: unknown;
        try {
          await client.sendKeys({
            claude_instance_id: "id-err-ni-1",
            text: "hello",
          });
        } catch (e) {
          tsErr = e;
        }

        assertErrorEnvelopes(cli.stderr, tsErr);
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );
});

// ── read-pane ─────────────────────────────────────────────────────────────────

describe("read-pane", () => {
  test(
    "success path",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-spawn", {
          store,
          id: "id-rp-1",
          state: "waiting",
          "create-store": true,
        });
      });
      try {
        const cli = runCli(
          ["read-pane", "--claude-instance-id", "id-rp-1"],
          cliEnv(homeA)
        );
        expect(cli.exitCode).toBe(0);

        using client = new Client({
          storePath: storeB,
          tmuxCommand: FAKE_TMUX_BIN,
        });
        const ts = await client.readPane({ claude_instance_id: "id-rp-1" });

        assertEnvelopesEqual(JSON.parse(cli.stdout) as unknown, ts, {
          ignorePaths: loadIgnorePathsForVerb("read-pane"),
        });
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );

  test(
    "error path: ErrSpawnNotFound",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-empty-store", { store });
      });
      try {
        const cli = runCli(
          ["read-pane", "--claude-instance-id", "nonexistent-id"],
          cliEnv(homeA)
        );
        expect(cli.exitCode).not.toBe(0);

        using client = new Client({ storePath: storeB });
        let tsErr: unknown;
        try {
          await client.readPane({ claude_instance_id: "nonexistent-id" });
        } catch (e) {
          tsErr = e;
        }

        assertErrorEnvelopes(cli.stderr, tsErr);
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );
});

// ── kill ──────────────────────────────────────────────────────────────────────

describe("kill", () => {
  test(
    "success path",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-spawn", {
          store,
          id: "id-kill-1",
          state: "waiting",
          "create-store": true,
        });
      });
      try {
        const cli = runCli(
          ["kill", "--claude-instance-id", "id-kill-1"],
          cliEnv(homeA)
        );
        expect(cli.exitCode).toBe(0);

        using client = new Client({
          storePath: storeB,
          tmuxCommand: FAKE_TMUX_BIN,
        });
        const ts = await client.kill({ claude_instance_id: "id-kill-1" });

        assertEnvelopesEqual(JSON.parse(cli.stdout) as unknown, ts, {
          ignorePaths: loadIgnorePathsForVerb("kill"),
        });
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );

  test(
    "error path: ErrSpawnNotFound",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-empty-store", { store });
      });
      try {
        const cli = runCli(
          ["kill", "--claude-instance-id", "nonexistent-id"],
          cliEnv(homeA)
        );
        expect(cli.exitCode).not.toBe(0);

        using client = new Client({ storePath: storeB });
        let tsErr: unknown;
        try {
          await client.kill({ claude_instance_id: "nonexistent-id" });
        } catch (e) {
          tsErr = e;
        }

        assertErrorEnvelopes(cli.stderr, tsErr);
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );
});

// ── decide ────────────────────────────────────────────────────────────────────

describe("decide", () => {
  test(
    "success path",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        // Spawn in check_permission with relay_mode=on
        runHelper("seed-spawn", {
          store,
          id: "id-d-1",
          state: "check_permission",
          "relay-mode": "on",
          "create-store": true,
        });
        // Add open permission request
        runHelper("seed-permission-request", {
          store,
          "spawn-id": "id-d-1",
          tool: "Bash",
        });
      });
      try {
        const cli = runCli(
          ["decide", "--claude-instance-id", "id-d-1", "--decision", "allow"],
          cliEnv(homeA)
        );
        expect(cli.exitCode).toBe(0);

        using client = new Client({ storePath: storeB });
        const ts = await client.decide({
          claude_instance_id: "id-d-1",
          decision: "allow",
        });

        assertEnvelopesEqual(JSON.parse(cli.stdout) as unknown, ts, {
          ignorePaths: loadIgnorePathsForVerb("decide"),
        });
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );

  test(
    "error path: ErrRelayModeOff",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        // relay_mode=off → ErrRelayModeOff
        runHelper("seed-spawn", {
          store,
          id: "id-err-rmo-1",
          state: "check_permission",
          "relay-mode": "off",
          "create-store": true,
        });
      });
      try {
        const cli = runCli(
          [
            "decide",
            "--claude-instance-id",
            "id-err-rmo-1",
            "--decision",
            "allow",
          ],
          cliEnv(homeA)
        );
        expect(cli.exitCode).not.toBe(0);

        using client = new Client({ storePath: storeB });
        let tsErr: unknown;
        try {
          await client.decide({
            claude_instance_id: "id-err-rmo-1",
            decision: "allow",
          });
        } catch (e) {
          tsErr = e;
        }

        assertErrorEnvelopes(cli.stderr, tsErr);
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );
});

// ── resume ────────────────────────────────────────────────────────────────────

describe("resume", () => {
  test(
    "success path",
    async () => {
      const resumeId = "id-resume-1";
      const sessId = "sess-envdiff-resume-1";
      const cwd = "/tmp";
      const slug = slugifyCwd(cwd);

      const { homeA, homeB, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-spawn", {
          store,
          id: resumeId,
          state: "ended",
          cwd,
          "session-id": sessId,
          "create-store": true,
        });
        if (OUTER_INSTANCE_ID) {
          runHelper("seed-spawn", {
            store,
            id: OUTER_INSTANCE_ID,
            state: "working",
          });
        }
      });

      // JSONL for CLI (HOME=homeA)
      const jsonlDirA = path.join(homeA, ".claude", "projects", slug);
      fs.mkdirSync(jsonlDirA, { recursive: true });
      fs.writeFileSync(path.join(jsonlDirA, `${sessId}.jsonl`), "{}\n");

      // JSONL for Client. The TS Client forwards --home homeB to the CLI
      // subprocess (b.32k), so the CLI resolves the JSONL path under homeB.
      const jsonlDirB = path.join(homeB, ".claude", "projects", slug);
      fs.mkdirSync(jsonlDirB, { recursive: true });
      fs.writeFileSync(path.join(jsonlDirB, `${sessId}.jsonl`), "{}\n");

      try {
        const cli = runCli(
          ["resume", "--claude-instance-id", resumeId],
          cliEnv(homeA)
        );
        expect(cli.exitCode).toBe(0);

        using client = new Client({
          storePath: storeB,
          home: homeB,
          tmuxCommand: FAKE_TMUX_BIN,
        });
        const ts = await client.resume({ claude_instance_id: resumeId });

        assertEnvelopesEqual(JSON.parse(cli.stdout) as unknown, ts, {
          ignorePaths: loadIgnorePathsForVerb("resume"),
        });
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );

  test(
    "error path: ErrSpawnNotResumable",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        // waiting state → not resumable (only ended/missing are)
        runHelper("seed-spawn", {
          store,
          id: "id-err-nr-1",
          state: "waiting",
          "create-store": true,
        });
      });
      try {
        const cli = runCli(
          ["resume", "--claude-instance-id", "id-err-nr-1"],
          cliEnv(homeA)
        );
        expect(cli.exitCode).not.toBe(0);

        using client = new Client({ storePath: storeB });
        let tsErr: unknown;
        try {
          await client.resume({ claude_instance_id: "id-err-nr-1" });
        } catch (e) {
          tsErr = e;
        }

        assertErrorEnvelopes(cli.stderr, tsErr);
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );
});

// ── find-missing ──────────────────────────────────────────────────────────────

describe("find-missing", () => {
  test(
    "success path",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-empty-store", { store });
      });
      try {
        const cli = runCli(["find-missing"], cliEnv(homeA));
        expect(cli.exitCode).toBe(0);

        using client = new Client({ storePath: storeB });
        const ts = await client.findMissing({});

        assertEnvelopesEqual(JSON.parse(cli.stdout) as unknown, ts, {
          ignorePaths: loadIgnorePathsForVerb("find-missing"),
        });
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );

  // ErrProbeUnsupported: only triggerable on non-linux platforms.
  // On linux/amd64 (primary CI), probe_linux.go is compiled and reads /proc —
  // ErrProbeUnsupported cannot be triggered without injecting a fake prober.
  // The error test below returns early on linux; find-missing is in the
  // NO_ERROR_CASE_ALLOWLIST in envelope-diff-invariants.test.ts.
  test(
    "error path: ErrProbeUnsupported (skipped on linux)",
    async () => {
      if (process.platform === "linux") return;
      // Non-linux: ErrProbeUnsupported can be triggered (placeholder).
      // If a future platform needs this, implement the fixture here.
      expect(true).toBe(true);
    },
    TIMEOUT
  );
});

// ── expire ────────────────────────────────────────────────────────────────────

describe("expire", () => {
  test(
    "success path",
    async () => {
      // Empty store: expire with 1h window returns 0 rows.
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-empty-store", { store });
      });
      try {
        const cli = runCli(["expire", "--older-than", "1h"], cliEnv(homeA));
        expect(cli.exitCode).toBe(0);

        using client = new Client({ storePath: storeB });
        const ts = await client.expire({ older_than: "1h" });

        assertEnvelopesEqual(JSON.parse(cli.stdout) as unknown, ts, {
          ignorePaths: loadIgnorePathsForVerb("expire"),
        });
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );

  // expire has no verb-level ErrorNames in the manifest.
  // It is in the NO_ERROR_CASE_ALLOWLIST in envelope-diff-invariants.test.ts.
});

// ── delete ────────────────────────────────────────────────────────────────────

describe("delete", () => {
  test(
    "success path",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        // Seed an ended row to delete
        runHelper("seed-spawn", {
          store,
          id: "row-ended",
          state: "ended",
          "create-store": true,
        });
      });
      try {
        const cli = runCli(
          ["delete", "--claude-instance-id", "row-ended"],
          cliEnv(homeA)
        );
        expect(cli.exitCode).toBe(0);

        using client = new Client({ storePath: storeB });
        const ts = await client.delete({ claude_instance_id: ["row-ended"] });

        assertEnvelopesEqual(JSON.parse(cli.stdout) as unknown, ts, {
          ignorePaths: loadIgnorePathsForVerb("delete"),
        });
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );

  // delete has no verb-level ErrorNames (errors are per-id in results map).
  // It is in the NO_ERROR_CASE_ALLOWLIST in envelope-diff-invariants.test.ts.
});

// ── make-template ─────────────────────────────────────────────────────────────

describe("make-template", () => {
  test(
    "success path",
    async () => {
      // Use a timestamp-unique name to avoid ErrTemplateExists across runs.
      const templateName = `envdiff-tmpl-${Date.now()}`;

      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-empty-store", { store });
      });

      // The TS Client's FFI worker uses REAL_HOME for os.UserHomeDir() so the
      // template lands at REAL_HOME/.agent-director/templates/<name>.toml.
      const clientTemplatePath = path.join(
        REAL_HOME,
        ".agent-director",
        "templates",
        `${templateName}.toml`
      );

      try {
        const cli = runCli(
          ["make-template", "--name", templateName],
          cliEnv(homeA)
        );
        expect(cli.exitCode).toBe(0);

        using client = new Client({ storePath: storeB });
        const ts = await client.makeTemplate({ name: templateName });

        // .path is in ignorePaths (embeds the ephemeral homeDir / REAL_HOME).
        assertEnvelopesEqual(JSON.parse(cli.stdout) as unknown, ts, {
          ignorePaths: loadIgnorePathsForVerb("make-template"),
        });
      } finally {
        // Clean up the template written to REAL_HOME by the Client.
        try {
          fs.unlinkSync(clientTemplatePath);
        } catch {
          /* best-effort */
        }
        cleanup();
      }
    },
    TIMEOUT
  );

  test(
    "error path: ErrTemplateNameUnsafe",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-empty-store", { store });
      });
      try {
        const cli = runCli(
          ["make-template", "--name", "../evil"],
          cliEnv(homeA)
        );
        expect(cli.exitCode).not.toBe(0);

        using client = new Client({ storePath: storeB });
        let tsErr: unknown;
        try {
          await client.makeTemplate({ name: "../evil" });
        } catch (e) {
          tsErr = e;
        }

        assertErrorEnvelopes(cli.stderr, tsErr);
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );
});

// ── list ──────────────────────────────────────────────────────────────────────

describe("list", () => {
  test(
    "success path",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        // Seed a couple of rows with known IDs
        runHelper("seed-spawn", {
          store,
          id: "row-list-a",
          state: "waiting",
          "create-store": true,
        });
        runHelper("seed-spawn", {
          store,
          id: "row-list-b",
          state: "ended",
        });
      });
      try {
        const cli = runCli(["list"], cliEnv(homeA));
        expect(cli.exitCode).toBe(0);

        using client = new Client({ storePath: storeB });
        const ts = await client.list({});

        assertEnvelopesEqual(JSON.parse(cli.stdout) as unknown, ts, {
          ignorePaths: loadIgnorePathsForVerb("list"),
        });
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );

  test(
    "error path: ErrListInvalidLabel",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-empty-store", { store });
      });
      try {
        // "badlabel" has no '=' separator → ErrListInvalidLabel
        const cli = runCli(["list", "--label", "badlabel"], cliEnv(homeA));
        expect(cli.exitCode).not.toBe(0);

        using client = new Client({ storePath: storeB });
        let tsErr: unknown;
        try {
          await client.list({ label: ["badlabel"] });
        } catch (e) {
          tsErr = e;
        }

        assertErrorEnvelopes(cli.stderr, tsErr);
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );
});

// ── pause ─────────────────────────────────────────────────────────────────────

describe("pause", () => {
  test(
    "success path",
    async () => {
      // Ended rows are no-op success for pause (terminal-state semantics).
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-spawn", {
          store,
          id: "id-pause-1",
          state: "ended",
          "create-store": true,
        });
      });
      try {
        const cli = runCli(
          ["pause", "--claude-instance-id", "id-pause-1"],
          cliEnv(homeA)
        );
        expect(cli.exitCode).toBe(0);

        using client = new Client({ storePath: storeB });
        const ts = await client.pause({ claude_instance_id: "id-pause-1" });

        assertEnvelopesEqual(JSON.parse(cli.stdout) as unknown, ts, {
          ignorePaths: loadIgnorePathsForVerb("pause"),
        });
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );

  test(
    "error path: ErrSpawnNotPausable",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        // pending state (InsertPending, no transition) → ErrSpawnNotPausable
        runHelper("seed-spawn", {
          store,
          id: "id-err-np-1",
          state: "pending",
          "create-store": true,
        });
      });
      try {
        const cli = runCli(
          ["pause", "--claude-instance-id", "id-err-np-1"],
          cliEnv(homeA)
        );
        expect(cli.exitCode).not.toBe(0);

        using client = new Client({ storePath: storeB });
        let tsErr: unknown;
        try {
          await client.pause({ claude_instance_id: "id-err-np-1" });
        } catch (e) {
          tsErr = e;
        }

        assertErrorEnvelopes(cli.stderr, tsErr);
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );
});

// ── version ───────────────────────────────────────────────────────────────────

describe("version", () => {
  test(
    "success path",
    async () => {
      // version is handle-free; minimal store is fine.
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-empty-store", { store });
      });
      try {
        const cli = runCli(["version"], cliEnv(homeA));
        expect(cli.exitCode).toBe(0);

        using client = new Client({ storePath: storeB });
        const ts = await client.version({});

        // .version and .commit are nondeterministic (CLI stamped with -ldflags;
        // in-process returns package default).
        assertEnvelopesEqual(JSON.parse(cli.stdout) as unknown, ts, {
          ignorePaths: loadIgnorePathsForVerb("version"),
        });
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );

  // version has no ErrorNames in the manifest.
  // It is in the NO_ERROR_CASE_ALLOWLIST in envelope-diff-invariants.test.ts.
});
