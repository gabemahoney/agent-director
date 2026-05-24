/**
 * structuralDiff.ts — structural diff helper for the TS envelope-diff harness.
 *
 * Mirrors Epic 3's test/envelope-diff/diff.go and selectors.go.
 *
 * Path notation matches nondeterministic.json exactly:
 *   ""          — root (empty string, not used in practice)
 *   ".foo"      — top-level field
 *   ".foo.bar"  — nested field
 *   ".arr[0]"   — array element at index 0
 *   ".arr[*]"   — any array element (wildcard, selector form only)
 *
 * The leading dot is stripped before selector matching so ".claude_instance_id"
 * and "claude_instance_id" selectors both work.
 */

export interface DiffOpts {
  /** Selectors from nondeterministic.json for fields to skip during diff. */
  ignorePaths?: string[];
}

/**
 * assertEnvelopesEqual walks a and b in parallel and throws on the first
 * mismatch at a path not covered by ignorePaths.
 *
 * @param a           CLI envelope (expected / "want" side)
 * @param b           TS Client envelope (actual / "got" side)
 * @param opts.ignorePaths  Selectors for nondeterministic fields to skip
 */
export function assertEnvelopesEqual(
  a: unknown,
  b: unknown,
  opts: DiffOpts = {}
): void {
  const ignorePaths = opts.ignorePaths ?? [];
  const diffs: string[] = [];
  diffValues(a, b, "", ignorePaths, diffs);
  if (diffs.length > 0) {
    throw new Error(
      `Envelope mismatch (${diffs.length} difference${diffs.length === 1 ? "" : "s"}):\n` +
        diffs.map((d) => `  ${d}`).join("\n")
    );
  }
}

// ── path helpers ─────────────────────────────────────────────────────────────

/** Build ".key" child path (mirrors Go childField). */
function childField(path: string, key: string): string {
  return `${path}.${key}`;
}

/** Build "[i]" child path (mirrors Go childIndex). */
function childIndex(path: string, i: number): string {
  return `${path}[${i}]`;
}

// ── selector matching ─────────────────────────────────────────────────────────

function shouldIgnore(path: string, ignorePaths: string[]): boolean {
  for (const sel of ignorePaths) {
    if (pathMatchesSelector(path, sel)) return true;
  }
  return false;
}

/**
 * pathMatchesSelector implements the same logic as Go's selectors.go:
 *   - Strip leading dot from jsonPath and selector.
 *   - Walk both segment-by-segment.
 *   - "[*]" in selector matches any "[N]" accessor in the path.
 *   - All other segments are matched by exact string equality.
 *   - Both must be fully consumed for a match.
 */
function pathMatchesSelector(jsonPath: string, selector: string): boolean {
  const p = jsonPath.startsWith(".") ? jsonPath.slice(1) : jsonPath;
  const s = selector.startsWith(".") ? selector.slice(1) : selector;
  return segmentsMatch(p, s);
}

function segmentsMatch(p: string, s: string): boolean {
  if (p === "" && s === "") return true;
  if (p === "" || s === "") return false;

  const [pSeg, pRem] = consumeSegment(p);
  const [sSeg, sRem] = consumeSegment(s);

  if (!segmentEq(pSeg, sSeg)) return false;
  return segmentsMatch(pRem, sRem);
}

/**
 * consumeSegment returns (firstSegment, remainder) from a path string.
 * Two kinds of segment:
 *   - Field name: run of non-".[" chars, e.g. "foo" from "foo.bar"
 *   - Array accessor: the entire "[…]" token, e.g. "[0]" or "[*]"
 * The separator dot between two field-name segments is consumed as remainder.
 */
function consumeSegment(s: string): [string, string] {
  if (s.startsWith("[")) {
    const end = s.indexOf("]");
    if (end < 0) return [s, ""];
    const seg = s.slice(0, end + 1);
    let rest = s.slice(end + 1);
    if (rest.startsWith(".")) rest = rest.slice(1);
    return [seg, rest];
  }
  // Field name: scan up to "." or "["
  for (let i = 0; i < s.length; i++) {
    if (s[i] === ".") return [s.slice(0, i), s.slice(i + 1)];
    if (s[i] === "[") return [s.slice(0, i), s.slice(i)];
  }
  return [s, ""];
}

function segmentEq(pathSeg: string, selSeg: string): boolean {
  // "[*]" wildcard matches any "[N]" accessor
  if (selSeg === "[*]") {
    return pathSeg.startsWith("[") && pathSeg.endsWith("]");
  }
  return pathSeg === selSeg;
}

// ── diff walkers ──────────────────────────────────────────────────────────────

function diffValues(
  a: unknown,
  b: unknown,
  path: string,
  ignorePaths: string[],
  diffs: string[]
): void {
  if (shouldIgnore(path, ignorePaths)) return;

  // Both null/undefined → equal
  if ((a === null || a === undefined) && (b === null || b === undefined)) return;

  // null/undefined vs non-null → mismatch
  if (a === null || a === undefined) {
    diffs.push(`${path || "."}: cli=${JSON.stringify(a)} ts=${JSON.stringify(b)}`);
    return;
  }
  if (b === null || b === undefined) {
    diffs.push(`${path || "."}: cli=${JSON.stringify(a)} ts=${JSON.stringify(b)}`);
    return;
  }

  // Array vs array
  if (Array.isArray(a) && Array.isArray(b)) {
    diffArrays(a, b, path, ignorePaths, diffs);
    return;
  }

  // Array type mismatch
  if (Array.isArray(a) !== Array.isArray(b)) {
    diffs.push(
      `${path || "."}: type mismatch: cli=${Array.isArray(a) ? "array" : typeof a} ts=${Array.isArray(b) ? "array" : typeof b}`
    );
    return;
  }

  // Object vs object
  if (typeof a === "object" && typeof b === "object") {
    diffObjects(
      a as Record<string, unknown>,
      b as Record<string, unknown>,
      path,
      ignorePaths,
      diffs
    );
    return;
  }

  // Object vs non-object mismatch
  if (typeof a === "object" || typeof b === "object") {
    diffs.push(
      `${path || "."}: type mismatch: cli=${typeof a} ts=${typeof b}`
    );
    return;
  }

  // Scalars: type mismatch
  if (typeof a !== typeof b) {
    diffs.push(
      `${path || "."}: type mismatch: cli=${typeof a}(${JSON.stringify(a)}) ts=${typeof b}(${JSON.stringify(b)})`
    );
    return;
  }

  // Scalars: value mismatch
  if (a !== b) {
    diffs.push(`${path || "."}: cli=${JSON.stringify(a)} ts=${JSON.stringify(b)}`);
  }
}

function diffObjects(
  a: Record<string, unknown>,
  b: Record<string, unknown>,
  path: string,
  ignorePaths: string[],
  diffs: string[]
): void {
  // Keys in a (CLI side)
  for (const key of Object.keys(a)) {
    const childPath = childField(path, key);
    if (shouldIgnore(childPath, ignorePaths)) continue;
    if (!(key in b)) {
      diffs.push(
        `${childPath}: missing on ts side; cli=${JSON.stringify(a[key])}`
      );
      continue;
    }
    diffValues(a[key], b[key], childPath, ignorePaths, diffs);
  }
  // Keys only in b (TS Client side)
  for (const key of Object.keys(b)) {
    const childPath = childField(path, key);
    if (shouldIgnore(childPath, ignorePaths)) continue;
    if (!(key in a)) {
      diffs.push(
        `${childPath}: extra on ts side (absent from cli): ${JSON.stringify(b[key])}`
      );
    }
  }
}

function diffArrays(
  a: unknown[],
  b: unknown[],
  path: string,
  ignorePaths: string[],
  diffs: string[]
): void {
  const maxLen = Math.max(a.length, b.length);
  for (let i = 0; i < maxLen; i++) {
    const childPath = childIndex(path, i);
    if (shouldIgnore(childPath, ignorePaths)) continue;
    if (i >= a.length) {
      diffs.push(
        `${childPath}: missing on cli side; ts=${JSON.stringify(b[i])}`
      );
    } else if (i >= b.length) {
      diffs.push(
        `${childPath}: missing on ts side; cli=${JSON.stringify(a[i])}`
      );
    } else {
      diffValues(a[i], b[i], childPath, ignorePaths, diffs);
    }
  }
}
