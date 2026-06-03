/**
 * probe.ts — bounded version-probe subprocess invocation (SR-1.3).
 *
 * Spawns `<candidate> version --json` with the SR-1.3 invocation policy:
 *   - 5000 ms timeout (SR-1.3.1), then SIGTERM → 2000 ms grace → SIGKILL (SR-1.3.2)
 *   - allowlist-only env scrub (SR-1.3.3)
 *   - stdin piped + closed before any byte is written (SR-1.3.4)
 *   - cwd = "/" (SR-1.3.5)
 *   - stdout/stderr piped, each capped at 64 KiB (SR-1.3.6 / .7 / .8)
 *
 * Returns the parsed-and-classified probe result.  Every probe failure is
 * surfaced via ProbeOutcome; the caller maps to ErrSystemInstall* in the
 * discovery pipeline.
 *
 * Internal — not re-exported from src/index.ts.
 */

import { parseVersion } from "./semver.js";
import type { UnreachableReason } from "../errors.js";

export const PROBE_TIMEOUT_MS = 5_000;
export const SIGKILL_GRACE_MS = 2_000;
export const STDOUT_CAP_BYTES = 64 * 1024;
export const STDERR_CAP_BYTES = 64 * 1024;

/**
 * Allowlist of env vars passed verbatim to the probed subprocess (SR-1.3.3).
 * Everything else is dropped; the probe is not steerable.
 */
const ENV_ALLOWLIST_BASE = ["PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TMPDIR", "TZ"] as const;
const ENV_ALLOWLIST_LINUX = ["LD_LIBRARY_PATH", "LD_PRELOAD"] as const;
const ENV_ALLOWLIST_DARWIN = ["DYLD_LIBRARY_PATH", "DYLD_FALLBACK_LIBRARY_PATH"] as const;

function scrubbedEnv(source: NodeJS.ProcessEnv): Record<string, string> {
  const allow: string[] = [...ENV_ALLOWLIST_BASE];
  if (process.platform === "linux") allow.push(...ENV_ALLOWLIST_LINUX);
  if (process.platform === "darwin") allow.push(...ENV_ALLOWLIST_DARWIN);
  const out: Record<string, string> = {};
  for (const key of allow) {
    const v = source[key];
    if (typeof v === "string") out[key] = v;
  }
  return out;
}

/** Result of a probe that classified successfully into a version string. */
export interface ProbeSuccess {
  ok: true;
  /** Byte-exact `version` field from the binary's `version --json` envelope. */
  version: string;
}

/** Result of a probe that failed; reason is the SR-3.3 enum value. */
export interface ProbeFailure {
  ok: false;
  reason: UnreachableReason;
  diagnostic: string | null;
  exitCode: number | null;
  signal: string | null;
}

export type ProbeOutcome = ProbeSuccess | ProbeFailure;

/**
 * readCapped reads from a Bun ReadableStream<Uint8Array> until EOF or the
 * capByteSize threshold is reached.  Returns the captured bytes (UTF-8
 * decoded) and a boolean indicating whether the cap was exceeded.  Once the
 * cap is hit, additional bytes are read off the stream and dropped so the
 * subprocess does not block on a full pipe buffer.
 */
async function readCapped(
  stream: ReadableStream<Uint8Array>,
  capByteSize: number,
): Promise<{ text: string; capped: boolean }> {
  const reader = stream.getReader();
  const chunks: Uint8Array[] = [];
  let bytes = 0;
  let capped = false;
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      if (capped) continue; // drain to keep the pipe flowing
      if (bytes + value.byteLength > capByteSize) {
        // Take the slice that fits, then mark capped and continue draining.
        const remaining = capByteSize - bytes;
        if (remaining > 0) {
          chunks.push(value.slice(0, remaining));
          bytes += remaining;
        }
        capped = true;
        continue;
      }
      chunks.push(value);
      bytes += value.byteLength;
    }
  } finally {
    try {
      reader.releaseLock();
    } catch {
      /* ignore */
    }
  }
  const total = new Uint8Array(bytes);
  let off = 0;
  for (const c of chunks) {
    total.set(c, off);
    off += c.byteLength;
  }
  return { text: new TextDecoder().decode(total), capped };
}

/**
 * runProbe spawns the candidate with SR-1.3 invocation hygiene and classifies
 * the outcome.  Never throws — always returns a ProbeOutcome.
 *
 * Caller is expected to have already validated the candidate per SR-1.2.
 */
export async function runProbe(candidatePath: string): Promise<ProbeOutcome> {
  let proc: Bun.Subprocess<"pipe", "pipe", "pipe">;
  try {
    proc = Bun.spawn({
      cmd: [candidatePath, "version", "--json"],
      stdin: "pipe",
      stdout: "pipe",
      stderr: "pipe",
      cwd: "/",
      env: scrubbedEnv(process.env),
      detached: false,
    });
  } catch (e) {
    // fork/exec failure surface (ENOEXEC, ETXTBSY, ENOMEM, etc.) is
    // SR-3.3 spawn-failed regardless of OS-level errno.  Permission-denied
    // (EACCES) is technically not-executable, but the caller's SR-1.2
    // validation should have caught that earlier; the probe path treats it
    // as spawn-failed for safety.
    const msg = e instanceof Error ? e.message : String(e);
    return {
      ok: false,
      reason: "spawn-failed",
      diagnostic: msg,
      exitCode: null,
      signal: null,
    };
  }

  // Close stdin before any byte is written (SR-1.3.4).
  try {
    proc.stdin.end();
  } catch {
    /* already closed */
  }

  // Timeout: SIGTERM at PROBE_TIMEOUT_MS, then SIGKILL after SIGKILL_GRACE_MS.
  let timedOut = false;
  let killedByCap = false;
  let killReason: UnreachableReason | null = null;
  const sigtermAt = setTimeout(() => {
    timedOut = true;
    killReason = "probe-timeout";
    try {
      proc.kill("SIGTERM");
    } catch {
      /* already exited */
    }
    setTimeout(() => {
      try {
        proc.kill("SIGKILL");
      } catch {
        /* already exited */
      }
    }, SIGKILL_GRACE_MS);
  }, PROBE_TIMEOUT_MS);

  const [stdoutResult, stderrResult] = await Promise.all([
    readCapped(proc.stdout, STDOUT_CAP_BYTES),
    readCapped(proc.stderr, STDERR_CAP_BYTES),
  ]);

  // Kill on cap-exceeded BEFORE awaiting exit, so the subprocess can't hang.
  if ((stdoutResult.capped || stderrResult.capped) && !timedOut) {
    killedByCap = true;
    killReason = stdoutResult.capped ? "unparseable-version" : "other";
    try {
      proc.kill("SIGTERM");
    } catch {
      /* already exited */
    }
    setTimeout(() => {
      try {
        proc.kill("SIGKILL");
      } catch {
        /* already exited */
      }
    }, SIGKILL_GRACE_MS);
  }

  await proc.exited;
  clearTimeout(sigtermAt);

  const exitCode = proc.exitCode;
  const signalCode = proc.signalCode;
  const stdoutText = stdoutResult.text;
  const stderrText = stderrResult.text;

  // Cap-overflow precedence: stdout-capped → unparseable-version;
  // stderr-only-capped → other.
  if (killedByCap && killReason !== null) {
    return {
      ok: false,
      reason: killReason,
      diagnostic: stderrText || null,
      exitCode: null,
      signal: null,
    };
  }

  if (timedOut) {
    return {
      ok: false,
      reason: "probe-timeout",
      diagnostic: stderrText || null,
      exitCode: null,
      signal: null,
    };
  }

  if (signalCode !== null) {
    return {
      ok: false,
      reason: "probe-killed-by-signal",
      diagnostic: stderrText || null,
      exitCode: null,
      signal: signalCode,
    };
  }

  if (exitCode !== 0) {
    return {
      ok: false,
      reason: "probe-nonzero-exit",
      diagnostic: stderrText || null,
      exitCode: exitCode ?? null,
      signal: null,
    };
  }

  // Parse the version envelope from stdout.
  let parsed: { version?: unknown };
  try {
    parsed = JSON.parse(stdoutText) as { version?: unknown };
  } catch {
    return {
      ok: false,
      reason: "unparseable-version",
      diagnostic: stdoutText || null,
      exitCode: null,
      signal: null,
    };
  }

  if (typeof parsed.version !== "string") {
    return {
      ok: false,
      reason: "unparseable-version",
      diagnostic: stdoutText || null,
      exitCode: null,
      signal: null,
    };
  }

  // SR-2.2 strict parseability check.  The library never canonicalizes the
  // value before returning it (SR-2.5) but a value that fails parsing is
  // not usable as a comparator input.
  const semverCheck = parseVersion(parsed.version);
  if (!semverCheck.ok) {
    return {
      ok: false,
      reason: "unparseable-version",
      diagnostic: parsed.version,
      exitCode: null,
      signal: null,
    };
  }

  return { ok: true, version: parsed.version };
}
