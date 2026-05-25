/**
 * spawner.ts — Bun.spawn wrapper for the subprocess Client.
 *
 * Provides `runSubprocess(argv)` which:
 *   1. Spawns argv[0] with argv[1..] using Bun.spawn (stdin/stdout/stderr
 *      piped, detached: false).
 *   2. Closes stdin immediately after spawn (no verb requires stdin input).
 *   3. Drains stdout and stderr concurrently with the subprocess running via
 *      `new Response(stream).text()` + `Promise.all` alongside
 *      `subprocess.exited` (SR-7.1 — prevents pipe-buffer deadlock on large
 *      payloads).
 *   4. Returns a `SpawnOutcome` with the raw text and exit metadata.
 *   5. Throws `ErrSubprocessCrash` when the subprocess exits non-zero
 *      without a signal (normal crash path).
 *
 * Signal-killed subprocesses (signalCode != null) are returned, not thrown;
 * the SubprocessClient (A6) inspects signalCode and throws ErrConsumerSignal.
 *
 * Implements SRD SR-1.1 (Bun.spawn, detached:false), SR-1.3
 * (stdin/stdout/stderr), SR-1.4 (cwd/env inheritance), SR-1.5 (exit
 * semantics), SR-7.1 (concurrent drain), SR-7.2 (no size cap), SR-7.3
 * (stderr preserved for diagnostics).
 *
 * Internal — NOT re-exported from src/index.ts.
 */

// ---------------------------------------------------------------------------
// ErrSubprocessCrash — implementation-level error, not an AgentDirectorError
// ---------------------------------------------------------------------------

/**
 * ErrSubprocessCrash is thrown by runSubprocess when the CLI subprocess exits
 * with a non-zero exit code (not caused by a signal). It carries the exit code,
 * the captured signal (null for normal crashes), and the first ~1 KB of stderr.
 *
 * This is an implementation-level error distinct from the catalog-derived
 * AgentDirectorError subclasses; it signals a crash in the CLI binary itself
 * (e.g. config-load failure, store-open failure), not a domain error.
 */
export class ErrSubprocessCrash extends Error {
  /** Exit code from the subprocess (null when killed by signal). */
  readonly exitCode: number | null;
  /** Signal name if killed by signal, null otherwise. */
  readonly signalCode: string | null;
  /** Full stderr output from the subprocess (not truncated; caller truncates). */
  readonly stderrFull: string;

  constructor(
    exitCode: number | null,
    signalCode: string | null,
    stderrFull: string
  ) {
    const reason =
      signalCode !== null
        ? `killed by signal ${signalCode}`
        : `exited with code ${exitCode ?? "null"}`;
    // Include the first 1 KB of stderr in the message for quick diagnostics.
    const snippet = stderrFull.slice(0, 1024);
    super(
      `subprocess ${reason}${snippet ? `; stderr: ${snippet}` : ""}`
    );
    this.exitCode = exitCode;
    this.signalCode = signalCode;
    this.stderrFull = stderrFull;
    this.name = "ErrSubprocessCrash";
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

// ---------------------------------------------------------------------------
// SpawnOutcome
// ---------------------------------------------------------------------------

/**
 * SpawnOutcome is the raw result of a successful (exit-code-0, valid-JSON-stdout)
 * subprocess run or a signal-killed run.
 *
 * Invariants:
 *   - When signalCode is non-null, exitCode is null (killed by signal).
 *   - When signalCode is null, exitCode is 0 (success path; crashes throw).
 *   - stdout is always a valid-JSON string on the success path (signalCode null,
 *     exitCode 0). On the signal path, stdout may be partial/empty.
 */
export interface SpawnOutcome {
  /** Raw stdout text drained from the subprocess. */
  readonly stdout: string;
  /** Raw stderr text drained from the subprocess. */
  readonly stderr: string;
  /** Exit code: 0 on success, null when killed by signal. */
  readonly exitCode: number | null;
  /** Signal name if the process was killed by a signal, null otherwise. */
  readonly signalCode: string | null;
}

// ---------------------------------------------------------------------------
// runSubprocess
// ---------------------------------------------------------------------------

/**
 * runSubprocess spawns `argv[0]` with arguments `argv[1..]` and returns a
 * `SpawnOutcome` describing the completed run.
 *
 * Stdin is closed immediately after spawn.
 * Stdout and stderr are drained concurrently with the subprocess running
 * so that large outputs do not fill the kernel pipe buffer and deadlock.
 *
 * @param argv Full argv: [cliPath, verbName, ...flags]
 * @throws ErrSubprocessCrash when exitCode is non-zero and signalCode is null.
 */
export async function runSubprocess(argv: string[]): Promise<SpawnOutcome> {
  const proc = Bun.spawn({
    cmd: argv,
    stdin: "pipe",
    stdout: "pipe",
    stderr: "pipe",
    // detached: false ensures the subprocess is a child of the consumer
    // process group. SIGINT/SIGTERM delivered to the group propagate
    // automatically (SRD SR-1.1, SR-5.1).
    detached: false,
  });

  // Close stdin immediately — no verb in the current CLI requires stdin
  // input (SRD SR-1.3). A future verb that needs stdin adds a flag instead.
  proc.stdin.end();

  // Drain stdout and stderr concurrently with the subprocess running.
  // Using new Response(stream).text() is idiomatic Bun and handles
  // arbitrary payload sizes without a manual chunk loop (SRD SR-7.1/SR-7.2).
  const [stdout, stderr] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
    proc.exited,
  ]).then(([out, err]) => [out, err]);

  // exitCode and signalCode are populated after proc.exited resolves.
  const exitCode = proc.exitCode;
  const signalCode = proc.signalCode;

  // Signal-killed subprocesses: return the outcome so the caller
  // (SubprocessClient) can throw ErrConsumerSignal with the verb context.
  if (signalCode !== null) {
    return { stdout, stderr, exitCode: null, signalCode };
  }

  // Non-zero exit (normal crash): throw ErrSubprocessCrash.
  // The full stderr is preserved on the error instance; the 1 KB truncation
  // appears in the message for quick diagnostics (SRD SR-7.3).
  if (exitCode !== 0) {
    throw new ErrSubprocessCrash(exitCode, null, stderr);
  }

  // Validate stdout is parseable JSON even though we return the raw string.
  // Exit code 0 with non-JSON stdout is treated as a subprocess crash
  // (e.g. the CLI wrote a plain error message without the JSON envelope).
  try {
    JSON.parse(stdout);
  } catch {
    throw new ErrSubprocessCrash(0, null, stderr);
  }

  return { stdout, stderr, exitCode: 0, signalCode: null };
}
