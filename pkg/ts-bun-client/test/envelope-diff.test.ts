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
 *   touch a real tmux session. Fake-tmux answers lookups from its session
 *   tables: a row with no labelled session there is Gone, so the send-keys
 *   and read-pane success cases, and expire's kept row, first write the
 *   row's own labelled session and pane into a per-side table (ts-helper
 *   seed-row-session).
 *
 * See docs/architecture.md "TS envelope-diff regression" for design notes.
 */

import { test, describe, expect } from "bun:test";
import * as path from "path";
import * as fs from "fs";
import * as os from "os";

import { Client, AgentDirectorError } from "../src/index.js";
import {
  runHelper,
  privateTmuxSocket,
  fakeTmuxCalls,
  withProcessEnv,
  CLAUDE_JSON,
  trustEntry,
  seedOuterParent,
} from "./internal/helper.js";
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

/** plantClaudeJson writes CLAUDE_JSON into each temp home so both sides pre-trust alike. */
function plantClaudeJson(...homes: string[]): void {
  for (const home of homes) fs.writeFileSync(path.join(home, ".claude.json"), CLAUDE_JSON);
}

/**
 * assertPreTrustOk pins pre_trust "ok" on both envelopes (so an ignore path
 * cannot hide it) and checks each home's .claude.json now trusts cwd (SR-22.6).
 */
function assertPreTrustOk(cli: unknown, ts: unknown, cwd: string, ...homes: string[]): void {
  expect((cli as { pre_trust?: unknown }).pre_trust).toBe("ok");
  expect((ts as { pre_trust?: unknown }).pre_trust).toBe("ok");
  for (const home of homes) {
    expect(trustEntry(path.join(home, ".claude.json"), cwd)).toBe(true);
  }
}

/**
 * trimNamePrefix mirrors Go's errnames.TrimNamePrefix: strips the redundant
 * "ErrName: " prefix from desc when present, returning the bare message.
 *
 * Sentinel `.Error()` strings include the leading "ErrName: " by convention, so
 * adErr.errDescription is "ErrFoo: message" while the CLI strips it to just
 * "message" via TrimNamePrefix. Applying the same strip here makes the two
 * sides comparable.
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
 *   carried by the sentinel's .Error() string but stripped by the CLI via
 *   TrimNamePrefix.
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
      const { homeA, homeB, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-empty-store", { store });
        seedOuterParent(store);
      });
      plantClaudeJson(homeA, homeB);
      try {
        const cli = runCli(["spawn", "--cwd", "/tmp"], cliEnv(homeA));
        expect(cli.exitCode).toBe(0);

        // home: homeB keeps the Client's pre-trust on homeB's .claude.json.
        using client = await Client.create({
          storePath: storeB,
          home: homeB,
          tmuxCommand: FAKE_TMUX_BIN, _cliPath: process.env.CLI_PATH
        } as any);
        const ts = await client.spawn({ cwd: "/tmp" });

        const cliEnvelope = JSON.parse(cli.stdout) as unknown;
        assertEnvelopesEqual(
          cliEnvelope,
          ts,
          { ignorePaths: loadIgnorePathsForVerb("spawn") }
        );
        assertPreTrustOk(cliEnvelope, ts, "/tmp", homeA, homeB);
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

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
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

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
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

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
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

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
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

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
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
    "success path: the row's own session, lookup + pane listing + text then Enter by pane id on both sides",
    async () => {
      const id = "id-sk-1";
      // Both store copies record this private socket; each run reads its own
      // fake-tmux table (FAKE_TMUX_TABLES) holding the row's labelled session.
      const tmuxDir = fs.mkdtempSync(path.join(os.tmpdir(), "ed-tmux-"));
      const socket = privateTmuxSocket(tmuxDir);
      const tablesCli = path.join(tmuxDir, "tables-cli");
      const tablesClient = path.join(tmuxDir, "tables-client");
      const logCli = path.join(tmuxDir, "log-cli");
      const logClient = path.join(tmuxDir, "log-client");
      const { homeA, storeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-spawn", { store, id, state: "waiting", "create-store": true, socket });
      });
      const paneIds = [
        [storeA, tablesCli],
        [storeB, tablesClient],
      ].map(([store, tablesDir]) => {
        const seeded = runHelper("seed-row-session", { store, id, "tables-dir": tablesDir });
        expect(seeded["socket"]).toBe(socket);
        return seeded["pane_id"] as string;
      });
      expect(paneIds[0]).toBe(paneIds[1]);
      const paneId = paneIds[0];

      try {
        const cli = runCli(
          ["send-keys", "--claude-instance-id", id, "--text", "hello"],
          { ...cliEnv(homeA), FAKE_TMUX_TABLES: tablesCli, FAKE_TMUX_LOG: logCli }
        );
        expect(cli.exitCode).toBe(0);

        // The Client's CLI subprocess inherits process.env.
        const ts = await withProcessEnv({ FAKE_TMUX_TABLES: tablesClient, FAKE_TMUX_LOG: logClient }, async () => {
          using client = await Client.create({
            storePath: storeB,
            tmuxCommand: FAKE_TMUX_BIN, _cliPath: process.env.CLI_PATH
          } as any);
          return await client.sendKeys({
            claude_instance_id: id,
            text: "hello",
          });
        });

        assertEnvelopesEqual(JSON.parse(cli.stdout) as unknown, ts, {
          ignorePaths: loadIgnorePathsForVerb("send-keys"),
        });

        // Each run: one lookup, one pane listing, then the text and Enter by
        // pane id, all on the row's socket (SR-7.2, SR-3.7).
        for (const log of [logCli, logClient]) {
          const calls = fakeTmuxCalls(log);
          expect(calls.map((argv) => argv.slice(1, 5))).toEqual([
            ["-u", "-S", socket, "display-message"], // the lookup: its server identity read first (b.47f)
            ["-u", "-S", socket, "list-panes"],
            ["-u", "-S", socket, "send-keys"],
            ["-u", "-S", socket, "send-keys"],
          ]);
          expect(calls[2].slice(4)).toEqual(["send-keys", "-t", paneId, "-l", "--", "hello"]);
          expect(calls[3].slice(4)).toEqual(["send-keys", "-t", paneId, "Enter"]);
        }
      } finally {
        cleanup();
        fs.rmSync(tmuxDir, { recursive: true, force: true });
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

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
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
  // Plain text: the default ANSI stripping leaves it unchanged.
  const READ_PANE_TEXT = "envelope-diff pane line one\nenvelope-diff pane line two\n";

  test(
    "success path: the row's own session, lookup + pane listing + one capture by pane id on both sides",
    async () => {
      const id = "id-rp-1";
      // Both store copies record this private socket; each run reads its own
      // fake-tmux table (FAKE_TMUX_TABLES) holding the row's labelled session.
      const tmuxDir = fs.mkdtempSync(path.join(os.tmpdir(), "ed-tmux-"));
      const socket = privateTmuxSocket(tmuxDir);
      const tablesCli = path.join(tmuxDir, "tables-cli");
      const tablesClient = path.join(tmuxDir, "tables-client");
      const logCli = path.join(tmuxDir, "log-cli");
      const logClient = path.join(tmuxDir, "log-client");
      const { homeA, storeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-spawn", { store, id, state: "waiting", "create-store": true, socket });
      });
      const paneIds = [
        [storeA, tablesCli],
        [storeB, tablesClient],
      ].map(([store, tablesDir]) => {
        const seeded = runHelper("seed-row-session", {
          store, id, capture: READ_PANE_TEXT, "tables-dir": tablesDir,
        });
        expect(seeded["socket"]).toBe(socket);
        return seeded["pane_id"] as string;
      });
      expect(paneIds[0]).toBe(paneIds[1]);
      const paneId = paneIds[0];

      try {
        const cli = runCli(
          ["read-pane", "--claude-instance-id", id],
          { ...cliEnv(homeA), FAKE_TMUX_TABLES: tablesCli, FAKE_TMUX_LOG: logCli }
        );
        expect(cli.exitCode).toBe(0);

        // The Client's CLI subprocess inherits process.env.
        const ts = await withProcessEnv({ FAKE_TMUX_TABLES: tablesClient, FAKE_TMUX_LOG: logClient }, async () => {
          using client = await Client.create({
            storePath: storeB,
            tmuxCommand: FAKE_TMUX_BIN, _cliPath: process.env.CLI_PATH
          } as any);
          return await client.readPane({ claude_instance_id: id });
        });

        const cliEnvelope = JSON.parse(cli.stdout) as { pane?: unknown };
        assertEnvelopesEqual(cliEnvelope, ts, {
          ignorePaths: loadIgnorePathsForVerb("read-pane"),
        });
        expect(cliEnvelope.pane).toBe(READ_PANE_TEXT);
        expect(ts.pane).toBe(READ_PANE_TEXT);

        // Each run: one lookup, one pane listing, one capture by pane id,
        // all on the row's socket (SR-7.2, SR-3.7).
        for (const log of [logCli, logClient]) {
          const calls = fakeTmuxCalls(log);
          expect(calls.map((argv) => argv.slice(1, 5))).toEqual([
            ["-u", "-S", socket, "display-message"], // the lookup: its server identity read first (b.47f)
            ["-u", "-S", socket, "list-panes"],
            ["-u", "-S", socket, "capture-pane"],
          ]);
          const capture = calls[2];
          const target = capture.indexOf("-t");
          expect(capture.slice(target, target + 2)).toEqual(["-t", paneId]);
        }
      } finally {
        cleanup();
        fs.rmSync(tmuxDir, { recursive: true, force: true });
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

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
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
    "success path: Gone row, lookup only, kill_sent false on both sides",
    async () => {
      const killId = `id-kill-${crypto.randomUUID().slice(0, 8)}`;
      // A private socket with no fake-tmux table answers the lookup Gone; the
      // seeded pane pid is never a live process, so no kill is sent (SR-6.1).
      const tmuxDir = fs.mkdtempSync(path.join(os.tmpdir(), "ed-tmux-"));
      const socket = privateTmuxSocket(tmuxDir);
      const logCli = path.join(tmuxDir, "log-cli");
      const logClient = path.join(tmuxDir, "log-client");
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-spawn", {
          store,
          id: killId,
          state: "waiting",
          "create-store": true,
          socket,
        });
      });
      try {
        const cli = runCli(
          ["kill", "--claude-instance-id", killId],
          { ...cliEnv(homeA), FAKE_TMUX_LOG: logCli }
        );
        expect(cli.exitCode).toBe(0);

        // The Client's CLI subprocess inherits process.env.
        const ts = await withProcessEnv({ FAKE_TMUX_LOG: logClient }, async () => {
          using client = await Client.create({
            storePath: storeB,
            tmuxCommand: FAKE_TMUX_BIN, _cliPath: process.env.CLI_PATH
          } as any);
          return await client.kill({ claude_instance_id: killId });
        });

        const cliEnvelope = JSON.parse(cli.stdout) as unknown;
        assertEnvelopesEqual(cliEnvelope, ts, {
          ignorePaths: loadIgnorePathsForVerb("kill"),
        });
        expect(cliEnvelope).toEqual({ kill_sent: false });
        expect(ts).toEqual({ kill_sent: false });

        for (const log of [logCli, logClient]) {
          const calls = fakeTmuxCalls(log);
          expect(calls.some((argv) => argv.includes("list-sessions") && argv.includes(socket))).toBe(true);
          expect(calls.filter((argv) => argv.includes("kill-pane") || argv.includes("kill-session"))).toEqual([]);
        }
      } finally {
        cleanup();
        fs.rmSync(tmuxDir, { recursive: true, force: true });
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

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
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
      // Capture the seeded request_token so we can pass it to both CLI and TS Client.
      let requestToken = "";
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        // Spawn in check_permission with relay_mode=on
        runHelper("seed-spawn", {
          store,
          id: "id-d-1",
          state: "check_permission",
          "relay-mode": "on",
          "create-store": true,
        });
        // Add open permission request; capture the request_token for --request-token flag.
        const seed = runHelper("seed-permission-request", {
          store,
          "spawn-id": "id-d-1",
          tool: "Bash",
        });
        requestToken = seed["request_token"] as string;
      });
      try {
        const cli = runCli(
          [
            "decide",
            "--claude-instance-id", "id-d-1",
            "--request-token", requestToken,
            "--decision", "allow",
          ],
          cliEnv(homeA)
        );
        expect(cli.exitCode).toBe(0);

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
        const ts = await client.decide({
          claude_instance_id: "id-d-1",
          request_token: requestToken,
          decision: "allow",
        });

        const cliResult = JSON.parse(cli.stdout) as { unproven_since: string | null };
        assertEnvelopesEqual(cliResult as unknown, ts, {
          ignorePaths: loadIgnorePathsForVerb("decide"),
        });
        // b.146 step 2c: unproven_since (the pre-v7 request's decided_at, written by each run) is excluded from
        // the diff; both sides must still carry it.
        expect(typeof cliResult.unproven_since).toBe("string");
        expect(typeof ts.unproven_since).toBe("string");
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );

  test(
    "error path: ErrRelayModeOff",
    async () => {
      // Seed a permission request so --request-token is non-empty; relay_mode=off
      // is checked after the token validation, yielding ErrRelayModeOff.
      const placeholderToken = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";
      const { homeA, storeB, cleanup } = prepareStores((store) => {
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
            "--request-token",
            placeholderToken,
            "--decision",
            "allow",
          ],
          cliEnv(homeA)
        );
        expect(cli.exitCode).not.toBe(0);

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
        let tsErr: unknown;
        try {
          await client.decide({
            claude_instance_id: "id-err-rmo-1",
            request_token: placeholderToken,
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

// ── get-permission ────────────────────────────────────────────────────────────

describe("get-permission", () => {
  test(
    "success path",
    async () => {
      let requestToken = "";
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-spawn", {
          store,
          id: "id-gp-1",
          state: "check_permission",
          "relay-mode": "on",
          "create-store": true,
        });
        const seed = runHelper("seed-permission-request", {
          store,
          "spawn-id": "id-gp-1",
          tool: "Bash",
        });
        requestToken = seed["request_token"] as string;
      });
      try {
        const cli = runCli(
          ["get-permission", "--request-token", requestToken],
          cliEnv(homeA)
        );
        expect(cli.exitCode).toBe(0);

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
        const ts = await client.getPermission({ request_token: requestToken });

        assertEnvelopesEqual(JSON.parse(cli.stdout) as unknown, ts, {
          ignorePaths: loadIgnorePathsForVerb("get-permission"),
        });
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );

  test(
    "error path: ErrPermissionRequestNotFound",
    async () => {
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-empty-store", { store });
      });
      try {
        const missingToken = "00000000-0000-0000-0000-000000000000";
        const cli = runCli(
          ["get-permission", "--request-token", missingToken],
          cliEnv(homeA)
        );
        expect(cli.exitCode).not.toBe(0);

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
        let tsErr: unknown;
        try {
          await client.getPermission({ request_token: missingToken });
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

// ── record-pane-answer ────────────────────────────────────────────────────────

describe("record-pane-answer", () => {
  test(
    "success path: a fallen-back request recorded answered outside, over read-pane's pane_sha256",
    async () => {
      const id = "id-rpa-1";
      const tmuxDir = fs.mkdtempSync(path.join(os.tmpdir(), "ed-tmux-"));
      const socket = privateTmuxSocket(tmuxDir);
      const tablesCli = path.join(tmuxDir, "tables-cli");
      const tablesClient = path.join(tmuxDir, "tables-client");
      let requestToken = "";
      const { homeA, storeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-spawn", { store, id, state: "check_permission", "relay-mode": "on", "create-store": true, socket });
        // Past the relay window (fallen back) and its hook gone a minute ago (b.146 rule 13).
        requestToken = runHelper("seed-permission-request", { store, "spawn-id": id, tool: "Bash",
          "created-ago-seconds": "172800", "hook-gone-ago-seconds": "60" })["request_token"] as string;
      });
      for (const [store, tablesDir] of [[storeA, tablesCli], [storeB, tablesClient]]) {
        runHelper("seed-row-session", { store, id, capture: "answered at tmux\n", "tables-dir": tablesDir });
      }
      try {
        const read = runCli(["read-pane", "--claude-instance-id", id], { ...cliEnv(homeA), FAKE_TMUX_TABLES: tablesCli });
        expect(read.exitCode).toBe(0);
        const hash = (JSON.parse(read.stdout) as { pane_sha256: string }).pane_sha256;
        const cli = runCli(
          ["record-pane-answer", "--request-token", requestToken, "--as", "deny", "--expect-pane-sha256", hash],
          { ...cliEnv(homeA), FAKE_TMUX_TABLES: tablesCli }
        );
        expect(cli.exitCode).toBe(0);

        const ts = await withProcessEnv({ FAKE_TMUX_TABLES: tablesClient }, async () => {
          using client = await Client.create({
            storePath: storeB,
            tmuxCommand: FAKE_TMUX_BIN, _cliPath: process.env.CLI_PATH
          } as any);
          return await client.recordPaneAnswer({ request_token: requestToken, as: "deny", expect_pane_sha256: hash });
        });

        assertEnvelopesEqual(JSON.parse(cli.stdout) as unknown, ts, {
          ignorePaths: loadIgnorePathsForVerb("record-pane-answer"),
        });
        expect(ts).toEqual({ request_token: requestToken, pane_answer: "outside", pane_as: "deny", decision: "deny",
          decision_reason: "pane_outside" });
      } finally {
        cleanup();
        fs.rmSync(tmuxDir, { recursive: true, force: true });
      }
    },
    TIMEOUT
  );

  test(
    "error path: ErrClaimTooSoon, its err_details the same on both sides",
    async () => {
      let requestToken = "";
      const { homeA, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-spawn", { store, id: "id-rpa-2", state: "check_permission", "relay-mode": "on", "create-store": true });
        requestToken = runHelper("seed-permission-request", { store, "spawn-id": "id-rpa-2", tool: "Bash" })["request_token"] as string;
      });
      try {
        const zero = "0".repeat(64);
        const cli = runCli(
          ["record-pane-answer", "--request-token", requestToken, "--as", "allow", "--expect-pane-sha256", zero],
          cliEnv(homeA)
        );
        expect(cli.exitCode).not.toBe(0);

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
        let tsErr: unknown;
        try {
          await client.recordPaneAnswer({ request_token: requestToken, as: "allow", expect_pane_sha256: zero });
        } catch (e) {
          tsErr = e;
        }

        assertErrorEnvelopes(cli.stderr, tsErr);
        // Its relay hook (recorded before schema v7, inside its relay window) cannot be checked:
        // not_before is its confirm_by plus 2 s, the same instant on both sides (one seed).
        const details = (JSON.parse(cli.stderr) as { err_details?: Record<string, unknown> }).err_details ?? {};
        const perm = runCli(["get-permission", "--request-token", requestToken], cliEnv(homeA));
        const confirmBy = (JSON.parse(perm.stdout) as { confirm_by: string }).confirm_by;
        const masked: Record<string, unknown> = { ...details, not_before: "" };
        expect(masked).toEqual({ request_token: requestToken, hook_alive: null, hook_gone_at: null, not_before: "" });
        expect(Date.parse(details["not_before"] as string) - Date.parse(confirmBy)).toBe(2000);
        expect((tsErr as AgentDirectorError).errDetails).toEqual(details);
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
      const resumeId = `id-resume-${crypto.randomUUID().slice(0, 8)}`;
      const sessId = "sess-envdiff-resume-1";
      const cwd = "/tmp";
      const slug = slugifyCwd(cwd);
      // Both store copies record this socket; each run keeps its fake-tmux
      // tables apart (FAKE_TMUX_TABLES), so the Client's create does not meet
      // the session the CLI created.
      const tmuxDir = fs.mkdtempSync(path.join(os.tmpdir(), "ed-tmux-"));
      const socket = privateTmuxSocket(tmuxDir);

      const { homeA, homeB, storeB, cleanup } = prepareStores((store) => {
        runHelper("seed-spawn", {
          store,
          id: resumeId,
          state: "ended",
          cwd,
          "session-id": sessId,
          "create-store": true,
          socket,
        });
        seedOuterParent(store);
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
      plantClaudeJson(homeA, homeB);

      try {
        const cli = runCli(
          ["resume", "--claude-instance-id", resumeId],
          { ...cliEnv(homeA), FAKE_TMUX_TABLES: path.join(tmuxDir, "tables-cli") }
        );
        expect(cli.exitCode).toBe(0);

        // The Client's CLI subprocess inherits process.env.
        const ts = await withProcessEnv({ FAKE_TMUX_TABLES: path.join(tmuxDir, "tables-client") }, async () => {
          using client = await Client.create({
            storePath: storeB,
            home: homeB,
            tmuxCommand: FAKE_TMUX_BIN, _cliPath: process.env.CLI_PATH
          } as any);
          return await client.resume({ claude_instance_id: resumeId });
        });

        const cliEnvelope = JSON.parse(cli.stdout) as unknown;
        assertEnvelopesEqual(cliEnvelope, ts, {
          ignorePaths: loadIgnorePathsForVerb("resume"),
        });
        assertPreTrustOk(cliEnvelope, ts, cwd, homeA, homeB);
      } finally {
        cleanup();
        fs.rmSync(tmuxDir, { recursive: true, force: true });
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

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
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

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
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

  // No error path: find-missing no longer returns ErrProbeUnsupported (kept by
  // SR-1.7); it is in NO_ERROR_CASE_ALLOWLIST in envelope-diff-invariants.test.ts.
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

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
        const ts = await client.expire({ older_than: "1h" });

        const cliEnvelope = JSON.parse(cli.stdout) as unknown;
        assertEnvelopesEqual(cliEnvelope, ts, {
          ignorePaths: loadIgnorePathsForVerb("expire"),
        });
        // Both lists present and [] (never null) on both sides (SR-12.4).
        const empty = { count: 0, ids: [], kept: 0, kept_ids: [] };
        expect(cliEnvelope).toEqual(empty);
        expect(ts).toEqual(empty);
      } finally {
        cleanup();
      }
    },
    TIMEOUT
  );

  test(
    "success path: one ended row deleted, one kept for its own session, one lookup on both sides",
    async () => {
      const goneId = "id-ex-gone";
      const keptId = "id-ex-kept";
      // Both ended rows record this private socket and no agent process, so
      // each run looks them up; each run reads its own fake-tmux table, which
      // holds only the kept row's labelled session (SR-12.2).
      const tmuxDir = fs.mkdtempSync(path.join(os.tmpdir(), "ed-tmux-"));
      const socket = privateTmuxSocket(tmuxDir);
      const tablesCli = path.join(tmuxDir, "tables-cli");
      const tablesClient = path.join(tmuxDir, "tables-client");
      const logCli = path.join(tmuxDir, "log-cli");
      const logClient = path.join(tmuxDir, "log-client");
      const { homeA, storeA, storeB, cleanup } = prepareStores((store) => {
        for (const id of [goneId, keptId]) {
          runHelper("seed-spawn", { store, id, state: "ended", "create-store": true, socket });
        }
      });
      for (const [store, tablesDir] of [[storeA, tablesCli], [storeB, tablesClient]]) {
        const seeded = runHelper("seed-row-session", { store, id: keptId, "tables-dir": tablesDir });
        expect(seeded["socket"]).toBe(socket);
      }

      try {
        const cli = runCli(
          ["expire", "--older-than", "0d"],
          { ...cliEnv(homeA), FAKE_TMUX_TABLES: tablesCli, FAKE_TMUX_LOG: logCli }
        );
        expect(cli.exitCode).toBe(0);

        // The Client's CLI subprocess inherits process.env.
        const ts = await withProcessEnv({ FAKE_TMUX_TABLES: tablesClient, FAKE_TMUX_LOG: logClient }, async () => {
          using client = await Client.create({
            storePath: storeB,
            tmuxCommand: FAKE_TMUX_BIN, _cliPath: process.env.CLI_PATH
          } as any);
          return await client.expire({ older_than: "0d" });
        });

        const cliEnvelope = JSON.parse(cli.stdout) as unknown;
        assertEnvelopesEqual(cliEnvelope, ts, {
          ignorePaths: loadIgnorePathsForVerb("expire"),
        });
        // `.ids` is an ignore path, so both results are pinned in full.
        const want = { count: 1, ids: [goneId], kept: 1, kept_ids: [keptId] };
        expect(cliEnvelope).toEqual(want);
        expect(ts).toEqual(want);
        // The CLI run (HOME=homeA) trails one ad.expire.kept, reason ours.
        const kept = fs
          .readFileSync(path.join(homeA, ".agent-director", "ad-trail.jsonl"), "utf8")
          .split("\n")
          .filter((line) => line !== "")
          .map((line) => JSON.parse(line) as Record<string, unknown>)
          .filter((rec) => rec["event"] === "ad.expire.kept");
        expect(kept).toHaveLength(1);
        expect(kept[0]).toMatchObject({ claude_instance_id: keptId, reason: "ours", source: "ad_expire" });

        // Each run: one lookup for the socket both rows share, and no other
        // tmux call (expire never lists panes or kills) (SR-12.2, SR-5.8).
        for (const log of [logCli, logClient]) {
          expect(fakeTmuxCalls(log).map((argv) => argv.slice(1, 5))).toEqual([
            ["-u", "-S", socket, "display-message"], // the lookup: its server identity read first (b.47f)
          ]);
        }
      } finally {
        cleanup();
        fs.rmSync(tmuxDir, { recursive: true, force: true });
      }
    },
    TIMEOUT
  );

  // expire has no verb-level ErrorNames in the manifest.
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

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
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

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
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

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
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

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
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

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
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

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
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

        using client = await Client.create({ storePath: storeB, _cliPath: process.env.CLI_PATH } as any);
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
