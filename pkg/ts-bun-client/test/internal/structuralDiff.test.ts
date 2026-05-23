/**
 * Unit tests for assertEnvelopesEqual (structuralDiff.ts).
 *
 * Tests do NOT depend on Epic 3's nondeterministic.json — all ignore-path
 * arrays are inline so the test remains self-contained.
 */

import { test, expect } from "bun:test";
import { assertEnvelopesEqual } from "./structuralDiff.js";

// ── (a) equal objects → no throw ─────────────────────────────────────────────

test("equal primitives — no throw", () => {
  expect(() => assertEnvelopesEqual(42, 42)).not.toThrow();
  expect(() => assertEnvelopesEqual("hello", "hello")).not.toThrow();
  expect(() => assertEnvelopesEqual(true, true)).not.toThrow();
  expect(() => assertEnvelopesEqual(null, null)).not.toThrow();
});

test("equal flat objects — no throw", () => {
  expect(() =>
    assertEnvelopesEqual({ a: 1, b: "two", c: true }, { a: 1, b: "two", c: true })
  ).not.toThrow();
});

test("equal nested objects — no throw", () => {
  expect(() =>
    assertEnvelopesEqual(
      { outer: { inner: [1, 2, 3] } },
      { outer: { inner: [1, 2, 3] } }
    )
  ).not.toThrow();
});

test("equal empty objects — no throw", () => {
  expect(() => assertEnvelopesEqual({}, {})).not.toThrow();
});

test("equal arrays — no throw", () => {
  expect(() => assertEnvelopesEqual([1, 2, 3], [1, 2, 3])).not.toThrow();
});

// ── (b) divergence at root → throw with path ─────────────────────────────────

test("mismatched primitives at root — throws", () => {
  expect(() => assertEnvelopesEqual(1, 2)).toThrow(/\./);
});

test("mismatched top-level field — throws with path", () => {
  let err: Error | null = null;
  try {
    assertEnvelopesEqual({ foo: 1 }, { foo: 2 });
  } catch (e) {
    err = e as Error;
  }
  expect(err).not.toBeNull();
  expect(err!.message).toContain(".foo");
});

// ── (c) divergence at nested path → throw with full path ─────────────────────

test("mismatch at .foo[0].bar — throws with exact path", () => {
  let err: Error | null = null;
  try {
    assertEnvelopesEqual(
      { foo: [{ bar: "x", ok: 1 }] },
      { foo: [{ bar: "y", ok: 1 }] }
    );
  } catch (e) {
    err = e as Error;
  }
  expect(err).not.toBeNull();
  expect(err!.message).toContain(".foo[0].bar");
});

test("mismatch at .a.b.c — throws with nested path", () => {
  let err: Error | null = null;
  try {
    assertEnvelopesEqual({ a: { b: { c: 1 } } }, { a: { b: { c: 2 } } });
  } catch (e) {
    err = e as Error;
  }
  expect(err).not.toBeNull();
  expect(err!.message).toContain(".a.b.c");
});

// ── (d) ignorePaths covers mismatch → no throw ───────────────────────────────

test("ignored top-level field — no throw", () => {
  expect(() =>
    assertEnvelopesEqual(
      { a: 1, t: "2026-01-01" },
      { a: 1, t: "2026-02-02" },
      { ignorePaths: [".t"] }
    )
  ).not.toThrow();
});

test("ignored nested path — no throw", () => {
  expect(() =>
    assertEnvelopesEqual(
      { spawns: [{ claude_instance_id: "uuid-a" }] },
      { spawns: [{ claude_instance_id: "uuid-b" }] },
      { ignorePaths: [".spawns[*].claude_instance_id"] }
    )
  ).not.toThrow();
});

test("ignored field with exact index — no throw", () => {
  expect(() =>
    assertEnvelopesEqual(
      { items: ["x", "different"] },
      { items: ["x", "other"] },
      { ignorePaths: [".items[1]"] }
    )
  ).not.toThrow();
});

test("ignorePaths covers divergent field, other fields equal — no throw", () => {
  expect(() =>
    assertEnvelopesEqual(
      { version: "v1.0.0", commit: "abc123", stable: true },
      { version: "v2.0.0", commit: "xyz789", stable: true },
      { ignorePaths: [".version", ".commit"] }
    )
  ).not.toThrow();
});

// ── (e) type mismatch at same path → throw ───────────────────────────────────

test("string vs number at same path — throws", () => {
  let err: Error | null = null;
  try {
    assertEnvelopesEqual({ count: "5" }, { count: 5 });
  } catch (e) {
    err = e as Error;
  }
  expect(err).not.toBeNull();
  expect(err!.message).toContain(".count");
  expect(err!.message).toContain("type mismatch");
});

test("array vs object at same path — throws", () => {
  let err: Error | null = null;
  try {
    assertEnvelopesEqual({ data: [1, 2] }, { data: { a: 1 } });
  } catch (e) {
    err = e as Error;
  }
  expect(err).not.toBeNull();
  expect(err!.message).toContain(".data");
});

test("null vs non-null — throws", () => {
  let err: Error | null = null;
  try {
    assertEnvelopesEqual({ ids: null }, { ids: [] });
  } catch (e) {
    err = e as Error;
  }
  expect(err).not.toBeNull();
  expect(err!.message).toContain(".ids");
});

// ── (f) array-length mismatch → throw ────────────────────────────────────────

test("array length mismatch — throws", () => {
  let err: Error | null = null;
  try {
    assertEnvelopesEqual({ ids: ["a", "b"] }, { ids: ["a"] });
  } catch (e) {
    err = e as Error;
  }
  expect(err).not.toBeNull();
  expect(err!.message).toContain(".ids[1]");
});

// ── (g) extra key on one side → throw with key path ──────────────────────────

test("extra key on actual (ts) side — throws", () => {
  let err: Error | null = null;
  try {
    assertEnvelopesEqual({ a: 1 }, { a: 1, extra: "oops" });
  } catch (e) {
    err = e as Error;
  }
  expect(err).not.toBeNull();
  expect(err!.message).toContain(".extra");
});

test("key missing on actual (ts) side — throws", () => {
  let err: Error | null = null;
  try {
    assertEnvelopesEqual({ a: 1, b: 2 }, { a: 1 });
  } catch (e) {
    err = e as Error;
  }
  expect(err).not.toBeNull();
  expect(err!.message).toContain(".b");
});

// ── mixed equal+mismatched fields → throws only on mismatched ────────────────

test("mixed fields — throws only for the mismatched one", () => {
  let err: Error | null = null;
  try {
    assertEnvelopesEqual(
      { ok: true, name: "alice", score: 99 },
      { ok: true, name: "bob", score: 99 }
    );
  } catch (e) {
    err = e as Error;
  }
  expect(err).not.toBeNull();
  expect(err!.message).toContain(".name");
  expect(err!.message).not.toContain(".ok");
  expect(err!.message).not.toContain(".score");
});

// ── selector format — both with and without leading dot ──────────────────────

test("selector without leading dot — works", () => {
  expect(() =>
    assertEnvelopesEqual(
      { version: "v1" },
      { version: "v2" },
      { ignorePaths: ["version"] } // no leading dot
    )
  ).not.toThrow();
});

test("selector with leading dot — works", () => {
  expect(() =>
    assertEnvelopesEqual(
      { version: "v1" },
      { version: "v2" },
      { ignorePaths: [".version"] } // with leading dot
    )
  ).not.toThrow();
});
