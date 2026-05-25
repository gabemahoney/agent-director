/**
 * subprocessClient.ts — subprocess-based Client implementation.
 *
 * Wires the platform resolver (A2), argv builder (A3), spawner (A4), and
 * typed-error map (A5) into a full Client class that drives the bundled CLI
 * binary one subprocess per verb call.
 *
 * Per-Client serialization: each `SubprocessClient` instance owns a private
 * `#tail: Promise<unknown>` queue. Every call chains its spawn body onto the
 * tail so that at most one subprocess per Client is running at any time (SRD
 * SR-3.1/SR-3.2/SR-3.4). Rejection does not wedge the queue (SR-3.3).
 *
 * Timeout handling (SRD SR-6.2):
 *   1. setTimeout fires after callTimeoutMs.
 *   2. SIGTERM is sent to the subprocess.
 *   3. A 2-second graceful window waits for the subprocess to exit.
 *   4. SIGKILL is sent if still running after the window.
 *   5. After subprocess.exited resolves, ErrCallTimeout is thrown.
 *
 * Signal handling (SRD SR-5.2):
 *   After subprocess.exited resolves, signalCode is inspected. If non-null
 *   and the timeout was not the cause, ErrConsumerSignal is thrown.
 *
 * IMPORTANT: This class does NOT modify src/index.ts's `Client` export.
 * The public surface continues to point at the existing FFI Client until
 * Epic B's cutover. Only the four new typed-error classes are added to
 * index.ts (done in Task A1).
 *
 * Internal — NOT re-exported from src/index.ts until Epic B.
 */

import { expandTilde } from "./tilde.js";
import { resolveCliPath } from "./platformResolve.js";
import { buildArgv } from "./argv.js";
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
  ResumeParams, ResumeResult,
  FindMissingParams, FindMissingResult,
  ExpireParams, ExpireResult,
  DeleteParams, DeleteResult,
  MakeTemplateParams, MakeTemplateResult,
  ListParams, ListResult,
  PauseParams, PauseResult,
  VersionParams, VersionResult,
} from "../types.js";

// Default per-call timeout (30 seconds). SRD SR-6.1.
const DEFAULT_CALL_TIMEOUT_MS = 30_000;
// Graceful window between SIGTERM and SIGKILL. SRD SR-6.2.
const SIGKILL_GRACE_MS = 2_000;

/**
 * SubprocessClient drives the bundled agent-director CLI binary as one
 * subprocess per verb call. Intended as the replacement for the FFI Client
 * but NOT yet wired to the public `Client` export (that is Epic B).
 *
 * Construction resolves the CLI binary path (throws on any resolution error)
 * and validates callTimeoutMs.
 */
export class SubprocessClient {
  /** Absolute path to the resolved CLI binary. Cached at construction. */
  readonly #cliPath: string;
  /** Per-call timeout in milliseconds. */
  readonly #callTimeoutMs: number;
  /** Tilde-expanded store path (stored for future use in Epic B env injection). */
  readonly #storePath: string;
  /** Optional logger for non-fatal warnings. */
  readonly #logger: ClientOptions["logger"];
  /** Whether this client is still open. */
  #open: boolean;
  /**
   * Chained-Promise queue. Every call chains its spawn body onto #tail so
   * that at most one subprocess per Client is running at any time (SR-3.4).
   * The tail always resolves (never rejects) so subsequent calls proceed even
   * after a failed call (SR-3.3).
   */
  #tail: Promise<unknown>;

  constructor(opts: ClientOptions) {
    // Validate callTimeoutMs before doing any I/O.
    const rawTimeout = opts.callTimeoutMs ?? DEFAULT_CALL_TIMEOUT_MS;
    if (rawTimeout <= 0) {
      throw new Error(
        `ClientOptions.callTimeoutMs must be positive (got ${rawTimeout}); ` +
          `omit the field to use the default (${DEFAULT_CALL_TIMEOUT_MS} ms)`
      );
    }
    this.#callTimeoutMs = rawTimeout;

    // Expand tilde in paths TS-side so the CLI receives absolute paths via env.
    this.#storePath = expandTilde(opts.storePath);

    this.#logger = opts.logger;

    // DI hook for testing: _cliPath bypasses platformResolve so tests can
    // inject a fixture binary path without needing the real platform package.
    const opts2 = opts as unknown as ClientOptions & { _cliPath?: string };

    // Resolve and stat the CLI binary at construction (one-shot per SR-2.4).
    // Throws ErrBunVersionTooOld, ErrUnsupportedPlatform, ErrPlatformPackageMissing,
    // or ErrCliNotExecutable on any resolution failure.
    this.#cliPath = opts2._cliPath ?? resolveCliPath();

    this.#open = true;
    this.#tail = Promise.resolve();
  }

  // -------------------------------------------------------------------------
  // Lifecycle
  // -------------------------------------------------------------------------

  /**
   * close marks this client as closed. Subsequent verb calls throw
   * ErrClientClosed. Idempotent.
   *
   * Unlike the FFI Client there is no handle to release: the subprocess
   * model requires no teardown — each call's subprocess is already reaped.
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

  // -------------------------------------------------------------------------
  // Core dispatch
  // -------------------------------------------------------------------------

  /**
   * #enqueue chains a verb call onto the serialization queue.
   *
   * The pattern:
   *   1. Create a [resolve, reject] pair for the caller's promise.
   *   2. Chain the spawn body onto #tail via .then().
   *   3. The .then() handler always resolves (catch + resolve(undefined)) so
   *      the tail never rejects and subsequent calls can chain (SR-3.3).
   *   4. Replace #tail with the new tail synchronously before returning so
   *      a second call issued before the first settles is guaranteed to chain
   *      off the first (SR-3.1 synchronous-replacement requirement).
   */
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
      // Always resolve the tail so subsequent calls proceed (SR-3.3).
    });

    return callPromise;
  }

  /**
   * #doCall performs the actual spawn + drain + parse for a single verb call.
   * Called from within the serialization queue.
   */
  async #doCall<R>(verb: VerbName, params: unknown): Promise<R> {
    const argv = buildArgv(this.#cliPath, verb, params);
    const startMs = Date.now();

    const proc = Bun.spawn({
      cmd: argv,
      stdin: "pipe",
      stdout: "pipe",
      stderr: "pipe",
      detached: false,
      // Pass a snapshot of process.env at call time so runtime mutations
      // (e.g. LOG_FILE set in tests) are visible to the subprocess. Bun.spawn
      // without an explicit env snapshots env at process start, not at spawn
      // time (SRD SR-1.4).
      env: { ...process.env },
    });

    // Close stdin immediately — no verb needs stdin input (SRD SR-1.3).
    proc.stdin.end();

    // Per-call timeout setup (SRD SR-6.2).
    let timedOut = false;
    const timeoutHandle = setTimeout(() => {
      timedOut = true;
      proc.kill("SIGTERM");
      // Graceful window: wait 2 s then SIGKILL if not yet exited.
      const gracefulHandle = setTimeout(() => {
        proc.kill("SIGKILL");
      }, SIGKILL_GRACE_MS);
      // Cancel the SIGKILL if the process exits during the graceful window.
      proc.exited.then(
        () => clearTimeout(gracefulHandle),
        () => clearTimeout(gracefulHandle)
      );
    }, this.#callTimeoutMs);

    // Drain stdout and stderr concurrently with the subprocess (SRD SR-7.1).
    let stdoutText: string;
    let stderrText: string;
    try {
      [stdoutText, stderrText] = await Promise.all([
        new Response(proc.stdout).text(),
        new Response(proc.stderr).text(),
        proc.exited,
      ]).then(([out, err, _code]) => [out, err]);
    } finally {
      // Clear the call-level timeout regardless of outcome (SR-6.3).
      clearTimeout(timeoutHandle);
    }

    const signalCode = proc.signalCode;
    const exitCode = proc.exitCode;
    const elapsedMs = Date.now() - startMs;

    // Timeout path: the subprocess was killed by our timeout handler.
    if (timedOut) {
      // Ensure the process is fully reaped before surfacing the error.
      await proc.exited.catch(() => {/* already exited */});
      throw new ErrCallTimeout(verb, elapsedMs, this.#callTimeoutMs);
    }

    // Signal path: the subprocess was killed by an OS signal (not our timeout).
    if (signalCode !== null) {
      throw new ErrConsumerSignal(verb, signalCode);
    }

    // Non-zero exit: subprocess crashed (config failure, store-open failure…).
    if (exitCode !== 0) {
      throw new ErrSubprocessCrash(exitCode, null, stderrText);
    }

    // Parse stdout as JSON (SRD SR-1.5 exit-code-0 success path).
    let envelope: unknown;
    try {
      envelope = JSON.parse(stdoutText);
    } catch {
      // Exit code 0 but non-JSON stdout: treat as subprocess crash.
      throw new ErrSubprocessCrash(0, null, stderrText);
    }

    // Typed error from the CLI's JSON envelope (SRD SR-4.1/SR-4.2/SR-4.3).
    if (isErrorEnvelope(envelope)) {
      throwFromEnvelope(verb, envelope);
    }

    if (this.#logger?.debug) {
      // Optional debug logging for non-error results; no-op when logger absent.
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

  /** version — return the binary's build-time version stamp. */
  async version(params: VersionParams): Promise<VersionResult> {
    this.#assertOpen();
    return this.#enqueue<VersionResult>("version", params);
  }
}
