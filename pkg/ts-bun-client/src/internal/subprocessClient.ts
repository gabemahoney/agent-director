/**
 * subprocessClient.ts — subprocess-based Client implementation.
 *
 * Construction is async (see Client.create() in client.ts).  The runtime
 * constructor is private — production code instantiates via the factory,
 * which runs the SR-1 discovery + SR-1.3 probe pipeline first and passes
 * the resolved binaryPath + binaryVersion in via an internal-only options
 * object (private symbol).
 *
 * Per-Client serialization: each instance owns a private `#tail: Promise<unknown>`
 * queue. Every call chains its spawn body onto the tail so that at most one
 * subprocess per Client is running at any time (SR-3.1/3.2/3.4). Rejection
 * does not wedge the queue (SR-3.3).
 *
 * Timeout handling (SR-6.2):
 *   1. setTimeout fires after callTimeoutMs.
 *   2. SIGTERM is sent to the subprocess.
 *   3. A 2-second graceful window waits for the subprocess to exit.
 *   4. SIGKILL is sent if still running after the window.
 *   5. After subprocess.exited resolves, ErrCallTimeout is thrown.
 *
 * Signal handling (SR-5.2):
 *   After subprocess.exited resolves, signalCode is inspected. If non-null
 *   and the timeout was not the cause, ErrConsumerSignal is thrown.
 */

import { readFile } from "node:fs/promises";
import { expandTilde } from "./tilde.js";
import { buildArgv, type GlobalArgvOptions } from "./argv.js";
import { ErrSubprocessCrash } from "./spawner.js";
import { isErrorEnvelope, throwFromEnvelope } from "./errorMap.js";
import {
  ErrClientClosed,
  ErrConsumerSignal,
  ErrCallTimeout,
} from "../errors.js";
import type { ClientOptions } from "../types.js";
import type { VerbName } from "./verbs.js";
import type {
  SpawnParams, SpawnResult,
  StatusParams, StatusResult,
  GetParams, GetResult,
  SendKeysParams, SendKeysResult,
  ReadPaneParams, ReadPaneResult,
  KillParams, KillResult,
  DecideParams, DecideResult,
  GetPermissionParams, GetPermissionResult,
  ResumeParams, ResumeResult,
  FindMissingParams, FindMissingResult,
  ExpireParams, ExpireResult,
  DeleteParams, DeleteResult,
  MakeTemplateParams, MakeTemplateResult,
  ListParams, ListResult,
  PauseParams, PauseResult,
  VersionParams, VersionResult,
} from "../types.js";

// Default per-call timeout (30 seconds). SR-6.1.
const DEFAULT_CALL_TIMEOUT_MS = 30_000;
// Graceful window between SIGTERM and SIGKILL. SR-6.2.
const SIGKILL_GRACE_MS = 2_000;

// b.6o1: runtime npm package version loader — reads package.json at call time
// so client.version() returns the published semver (stamped by release.sh)
// rather than the CLI binary's stamped version.  Kept separate from
// binaryVersion (SR-4.1): the latter is the CLI's stamped value; this is the
// library's npm-package version.
async function loadNpmPackageVersion(): Promise<string> {
  try {
    const url = import.meta.resolve("agent-director/package.json");
    const json = await readFile(new URL(url), "utf8");
    return (JSON.parse(json) as { version: string }).version;
  } catch {
    const url = new URL("../../package.json", import.meta.url);
    const json = await readFile(url, "utf8");
    return (JSON.parse(json) as { version: string }).version;
  }
}

/**
 * Internal construction parameters.  Carries pre-resolved binaryPath +
 * binaryVersion so the runtime constructor doesn't run discovery itself.
 * The factory (Client.create) supplies these by running discoverSystemBinary
 * and runProbe upstream.
 *
 * @internal
 */
export interface InternalClientInit {
  /** Canonicalized absolute path produced by SR-1.1 discovery or SR-4.1 _cliPath canonicalization. */
  binaryPath: string;
  /** Byte-exact version string captured from the probe envelope (SR-2.5). */
  binaryVersion: string;
}

/**
 * SubprocessClient drives the agent-director CLI binary as one subprocess
 * per verb call against a binary located by the SR-1 discovery pipeline.
 *
 * Constructed via Client.create() — the public surface.  The runtime
 * constructor is private; tests reach instances only through the factory.
 */
export class SubprocessClient {
  /** Canonicalized absolute path to the CLI binary (SR-1.5). */
  readonly #binaryPath: string;
  /** Byte-exact version string captured at construction (SR-2.5). */
  readonly #binaryVersion: string;
  /** Per-call timeout in milliseconds. */
  readonly #callTimeoutMs: number;
  /** Optional logger for non-fatal warnings. */
  readonly #logger: ClientOptions["logger"];
  /** Whether this client is still open. */
  #open: boolean;
  /** Global flags forwarded to every spawn (--store-path / --home / --tmux-command). */
  readonly #globalOpts: GlobalArgvOptions;
  /**
   * FFI-shape parity stub.  Always null in the subprocess model.
   * @internal
   */
  readonly _handle: null = null;
  /** Serialization queue (SR-3.4). */
  #tail: Promise<unknown>;
  /** Per-instance npm package version cache (b.6o1). */
  #npmPkgVersion: string | undefined;

  /**
   * Protected constructor — production code uses Client.create() instead.
   * Protected so that the public Client class (which extends this) can call
   * super() via SubprocessClient._construct().  External code cannot reach
   * the constructor: `new SubprocessClient(...)` and `new Client(...)` are
   * both compile-time TS errors.
   *
   * @internal
   */
  protected constructor(opts: ClientOptions, init: InternalClientInit) {
    const rawTimeout = opts.callTimeoutMs ?? DEFAULT_CALL_TIMEOUT_MS;
    if (rawTimeout <= 0) {
      throw new Error(
        `ClientOptions.callTimeoutMs must be positive (got ${rawTimeout}); ` +
          `omit the field to use the default (${DEFAULT_CALL_TIMEOUT_MS} ms)`,
      );
    }
    this.#callTimeoutMs = rawTimeout;
    this.#logger = opts.logger;
    this.#binaryPath = init.binaryPath;
    this.#binaryVersion = init.binaryVersion;

    const g: GlobalArgvOptions = {};
    if (opts.storePath !== undefined) g.storePath = expandTilde(opts.storePath);
    if (opts.tmuxCommand !== undefined) g.tmuxCommand = expandTilde(opts.tmuxCommand);
    if (opts.home !== undefined) g.home = expandTilde(opts.home);
    this.#globalOpts = g;

    this.#open = true;
    this.#tail = Promise.resolve();
  }

  /**
   * Internal-only factory entry point.  Callers in the same module use
   * `(SubprocessClient as any)._construct(...)`; we publish this as a
   * non-typed method so the TS `private constructor` fence holds for
   * external consumers while client.ts can still build an instance.
   *
   * @internal
   */
  static _construct(opts: ClientOptions, init: InternalClientInit): SubprocessClient {
    return new SubprocessClient(opts, init);
  }

  // -------------------------------------------------------------------------
  // Public getters (SR-4.1)
  // -------------------------------------------------------------------------

  /** Canonicalized absolute path to the CLI binary (SR-1.5 / SR-4.1). */
  get binaryPath(): string {
    return this.#binaryPath;
  }

  /** Byte-exact version string the CLI binary reported (SR-2.5 / SR-4.1). */
  get binaryVersion(): string {
    return this.#binaryVersion;
  }

  // -------------------------------------------------------------------------
  // Lifecycle
  // -------------------------------------------------------------------------

  /**
   * close marks this client as closed.  Idempotent.  Subsequent verb calls
   * throw ErrClientClosed.
   */
  close(): void {
    this.#open = false;
  }

  /** [Symbol.dispose] delegates to close() for `using` blocks. */
  [Symbol.dispose](): void {
    this.close();
  }

  /** Throw ErrClientClosed if the client has been closed. */
  #assertOpen(): void {
    if (!this.#open) throw new ErrClientClosed();
  }

  /**
   * @internal Tests only.
   */
  _assertOpenForTests(): void {
    this.#assertOpen();
  }

  // -------------------------------------------------------------------------
  // Core dispatch
  // -------------------------------------------------------------------------

  #enqueue<R>(verb: VerbName, params: unknown): Promise<R> {
    let callResolve!: (value: R) => void;
    let callReject!: (reason: unknown) => void;
    const callPromise = new Promise<R>((res, rej) => {
      callResolve = res;
      callReject = rej;
    });

    this.#tail = this.#tail.then(async () => {
      try {
        const result = await this.#doCall<R>(verb, params);
        callResolve(result);
      } catch (err) {
        callReject(err);
      }
    });

    return callPromise;
  }

  async #doCall<R>(verb: VerbName, params: unknown): Promise<R> {
    const cliPath = this.#binaryPath;
    const argv = buildArgv(cliPath, verb, params, this.#globalOpts);
    const startMs = Date.now();

    const spawnEnv: Record<string, string> = { ...process.env } as Record<string, string>;

    const proc = Bun.spawn({
      cmd: argv,
      stdin: "pipe",
      stdout: "pipe",
      stderr: "pipe",
      detached: false,
      env: spawnEnv,
    });

    proc.stdin.end();

    let timedOut = false;
    const timeoutHandle = setTimeout(() => {
      timedOut = true;
      proc.kill("SIGTERM");
      const gracefulHandle = setTimeout(() => {
        proc.kill("SIGKILL");
      }, SIGKILL_GRACE_MS);
      proc.exited.then(
        () => clearTimeout(gracefulHandle),
        () => clearTimeout(gracefulHandle),
      );
    }, this.#callTimeoutMs);

    let stdoutText: string;
    let stderrText: string;
    try {
      [stdoutText, stderrText] = await Promise.all([
        new Response(proc.stdout).text(),
        new Response(proc.stderr).text(),
        proc.exited,
      ]).then(([out, err]) => [out, err]);
    } finally {
      clearTimeout(timeoutHandle);
    }

    const signalCode = proc.signalCode;
    const exitCode = proc.exitCode;
    const elapsedMs = Date.now() - startMs;

    if (timedOut) {
      await proc.exited.catch(() => {/* already exited */});
      throw new ErrCallTimeout(verb, elapsedMs, this.#callTimeoutMs);
    }

    if (signalCode !== null) {
      throw new ErrConsumerSignal(verb, signalCode);
    }

    if (exitCode !== 0) {
      const stderrLines = stderrText.trimEnd().split("\n");
      for (let i = stderrLines.length - 1; i >= 0; i--) {
        const line = stderrLines[i].trim();
        if (!line.startsWith("{")) continue;
        let parsed: unknown;
        try {
          parsed = JSON.parse(line);
        } catch {
          continue;
        }
        if (isErrorEnvelope(parsed)) {
          throwFromEnvelope(verb, parsed);
        }
      }
      throw new ErrSubprocessCrash(exitCode, null, stderrText);
    }

    let envelope: unknown;
    try {
      envelope = JSON.parse(stdoutText);
    } catch {
      throw new ErrSubprocessCrash(0, null, stderrText);
    }

    if (isErrorEnvelope(envelope)) {
      throwFromEnvelope(verb, envelope);
    }

    if (this.#logger?.debug) {
      this.#logger.debug(`SubprocessClient: ${verb} ok`, { elapsedMs });
    }

    return envelope as R;
  }

  // -------------------------------------------------------------------------
  // Verb methods — one per callable verb in src/internal/verbs.ts.
  // -------------------------------------------------------------------------

  /** spawn — launch a tracked Claude Code instance in a new tmux session. */
  async spawn(params: SpawnParams): Promise<SpawnResult> {
    this.#assertOpen();
    return this.#enqueue<SpawnResult>("spawn", params);
  }

  /** status — get the current state of a Spawn. */
  async status(params: StatusParams): Promise<StatusResult> {
    this.#assertOpen();
    return this.#enqueue<StatusResult>("status", params);
  }

  /** get — fetch the full Spawn record. */
  async get(params: GetParams): Promise<GetResult> {
    this.#assertOpen();
    return this.#enqueue<GetResult>("get", params);
  }

  /** sendKeys — send keystrokes to a Spawn's tmux pane. */
  async sendKeys(params: SendKeysParams): Promise<SendKeysResult> {
    this.#assertOpen();
    return this.#enqueue<SendKeysResult>("send-keys", params);
  }

  /** readPane — read the current contents of a Spawn's tmux pane. */
  async readPane(params: ReadPaneParams): Promise<ReadPaneResult> {
    this.#assertOpen();
    return this.#enqueue<ReadPaneResult>("read-pane", params);
  }

  /** kill — terminate a Spawn's tmux session. */
  async kill(params: KillParams): Promise<KillResult> {
    this.#assertOpen();
    return this.#enqueue<KillResult>("kill", params);
  }

  /** decide — resolve a pending permission request. */
  async decide(params: DecideParams): Promise<DecideResult> {
    this.#assertOpen();
    return this.#enqueue<DecideResult>("decide", params);
  }

  /** getPermission — fetch a permission_requests row by request_token. */
  async getPermission(params: GetPermissionParams): Promise<GetPermissionResult> {
    this.#assertOpen();
    return this.#enqueue<GetPermissionResult>("get-permission", params);
  }

  /** resume — restart a terminated Spawn. */
  async resume(params: ResumeParams): Promise<ResumeResult> {
    this.#assertOpen();
    return this.#enqueue<ResumeResult>("resume", params);
  }

  /** findMissing — reconcile Spawns whose tmux sessions have disappeared. */
  async findMissing(params: FindMissingParams): Promise<FindMissingResult> {
    this.#assertOpen();
    return this.#enqueue<FindMissingResult>("find-missing", params);
  }

  /** expire — remove terminal-state rows older than the retention window. */
  async expire(params: ExpireParams): Promise<ExpireResult> {
    this.#assertOpen();
    return this.#enqueue<ExpireResult>("expire", params);
  }

  /** delete — hard-delete Spawn rows by id. */
  async delete(params: DeleteParams): Promise<DeleteResult> {
    this.#assertOpen();
    return this.#enqueue<DeleteResult>("delete", params);
  }

  /** makeTemplate — save a reusable spawn preset. */
  async makeTemplate(params: MakeTemplateParams): Promise<MakeTemplateResult> {
    this.#assertOpen();
    return this.#enqueue<MakeTemplateResult>("make-template", params);
  }

  /** list — query Spawns with optional filter params. */
  async list(params: ListParams): Promise<ListResult> {
    this.#assertOpen();
    return this.#enqueue<ListResult>("list", params);
  }

  /** pause — politely shut down a waiting Spawn. */
  async pause(params: PauseParams): Promise<PauseResult> {
    this.#assertOpen();
    return this.#enqueue<PauseResult>("pause", params);
  }

  /** version — return the npm package version (b.6o1) plus the CLI's commit SHA. */
  async version(params: VersionParams): Promise<VersionResult> {
    this.#assertOpen();
    if (this.#npmPkgVersion === undefined) {
      this.#npmPkgVersion = await loadNpmPackageVersion();
    }
    const cliResp = await this.#enqueue<VersionResult>("version", params);
    return { ...cliResp, version: this.#npmPkgVersion };
  }
}
