/**
 * Internal strict-SemVer-2.0 parser + 3-way comparator with dev-sentinel
 * short-circuit.  Internal-only per SR-2.4 — not re-exported from src/index.ts.
 *
 * Parse rules (SR-2.2):
 *   1. Sentinel short-circuit: byte-exact "0.0.0-dev" returns a tagged
 *      sentinel result that satisfies any minimum.
 *   2. Strict regex /^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$/. No
 *      canonicalization, no leading-v stripping, no build-metadata stripping,
 *      no whitespace trimming. The build pipeline (SR-2.6) owns "clean string
 *      at the source".
 *
 * Compare rules (SR-2.3):
 *   - Sentinel side always satisfies (`compare(sentinel, anything)` >= 0).
 *   - Otherwise compare core (major, minor, patch) numerically.
 *   - Equal cores: no-prerelease > has-prerelease (SemVer 2.0 §11.4).
 *   - Both have prerelease: lexicographic ASCII byte compare (simplified
 *     rule per SRD cross-piece commitment).
 */

export const DEV_SENTINEL = "0.0.0-dev";

const SEMVER_RE = /^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$/;

export interface ParsedVersion {
  readonly kind: "real";
  readonly major: number;
  readonly minor: number;
  readonly patch: number;
  readonly prerelease: string | null;
}

export interface SentinelVersion {
  readonly kind: "sentinel";
}

export type ParseResult =
  | { ok: true; value: ParsedVersion | SentinelVersion }
  | { ok: false };

export function parseVersion(input: string): ParseResult {
  if (input === DEV_SENTINEL) {
    return { ok: true, value: { kind: "sentinel" } };
  }
  const m = SEMVER_RE.exec(input);
  if (m === null) {
    return { ok: false };
  }
  const major = Number(m[1]);
  const minor = Number(m[2]);
  const patch = Number(m[3]);
  if (
    !Number.isFinite(major) ||
    !Number.isFinite(minor) ||
    !Number.isFinite(patch)
  ) {
    return { ok: false };
  }
  return {
    ok: true,
    value: {
      kind: "real",
      major,
      minor,
      patch,
      prerelease: m[4] ?? null,
    },
  };
}

function sign(n: number): -1 | 0 | 1 {
  if (n < 0) return -1;
  if (n > 0) return 1;
  return 0;
}

export function compareParsed(
  a: ParsedVersion | SentinelVersion,
  b: ParsedVersion | SentinelVersion,
): -1 | 0 | 1 {
  if (a.kind === "sentinel" && b.kind === "sentinel") return 0;
  if (a.kind === "sentinel") return 1;
  if (b.kind === "sentinel") return -1;

  const coreDiff =
    a.major !== b.major
      ? a.major - b.major
      : a.minor !== b.minor
      ? a.minor - b.minor
      : a.patch - b.patch;
  if (coreDiff !== 0) return sign(coreDiff);

  if (a.prerelease === null && b.prerelease === null) return 0;
  if (a.prerelease === null) return 1;
  if (b.prerelease === null) return -1;

  if (a.prerelease < b.prerelease) return -1;
  if (a.prerelease > b.prerelease) return 1;
  return 0;
}

/**
 * Three-way compare two version strings.  Returns -1, 0, +1; throws if either
 * input fails SR-2.2 parsing.  Callers that need to classify unparseable
 * inputs as ErrSystemInstallUnreachable(reason="unparseable-version") must
 * pre-parse with `parseVersion` instead of catching this throw.
 */
export function compareVersions(a: string, b: string): -1 | 0 | 1 {
  const pa = parseVersion(a);
  const pb = parseVersion(b);
  if (!pa.ok) {
    throw new Error(`unparseable version: ${JSON.stringify(a)}`);
  }
  if (!pb.ok) {
    throw new Error(`unparseable version: ${JSON.stringify(b)}`);
  }
  return compareParsed(pa.value, pb.value);
}
