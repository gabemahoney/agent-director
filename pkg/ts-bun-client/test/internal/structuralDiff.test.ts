/**
 * structuralDiff.test.ts — assertEnvelopesEqual, the envelope-diff harness's
 * comparator: equal envelopes pass, ignorePaths selectors (with or without the
 * leading dot, `[*]` wildcards) skip fields, and any other difference throws
 * naming its exact path.
 */

import { test, expect } from "bun:test";
import { assertEnvelopesEqual } from "./structuralDiff.js";

test.each([
  ["equal primitives", 42, 42, []],
  ["equal null", null, null, []],
  ["equal nested objects", { a: 1, outer: { inner: [1, 2, 3] } }, { a: 1, outer: { inner: [1, 2, 3] } }, []],
  ["equal empty objects", {}, {}, []],
  ["ignored top-level fields", { a: 1, version: "v1", commit: "x" }, { a: 1, version: "v2", commit: "y" }, [".version", ".commit"]],
  ["ignored [*] wildcard", { spawns: [{ id: "a" }] }, { spawns: [{ id: "b" }] }, [".spawns[*].id"]],
  ["ignored exact index", { items: ["x", "p"] }, { items: ["x", "q"] }, [".items[1]"]],
  ["selector without leading dot", { version: "v1" }, { version: "v2" }, ["version"]],
] as const)("%s → no throw", (_label, a, b, ignorePaths) => {
  expect(() => assertEnvelopesEqual(a, b, { ignorePaths: [...ignorePaths] })).not.toThrow();
});

test.each([
  ["root primitive", 1, 2, "."],
  ["top-level field", { foo: 1 }, { foo: 2 }, ".foo"],
  ["nested array element field", { foo: [{ bar: "x", ok: 1 }] }, { foo: [{ bar: "y", ok: 1 }] }, ".foo[0].bar"],
  ["deep field", { a: { b: { c: 1 } } }, { a: { b: { c: 2 } } }, ".a.b.c"],
  ["string vs number", { count: "5" }, { count: 5 }, ".count: type mismatch"],
  ["array vs object", { data: [1, 2] }, { data: { a: 1 } }, ".data: type mismatch"],
  ["null vs array", { ids: null }, { ids: [] }, ".ids"],
  ["array length", { ids: ["a", "b"] }, { ids: ["a"] }, ".ids[1]"],
  ["extra key on the TS side", { a: 1 }, { a: 1, extra: "oops" }, ".extra"],
  ["key missing on the TS side", { a: 1, b: 2 }, { a: 1 }, ".b"],
] as const)("%s differs → throws naming %p", (_label, a, b, path) => {
  expect(() => assertEnvelopesEqual(a, b)).toThrow(path);
});

test("only the differing field is reported", () => {
  expect(() => assertEnvelopesEqual({ ok: true, name: "alice", score: 99 }, { ok: true, name: "bob", score: 99 }))
    .toThrow(/^Envelope mismatch \(1 difference\):\n {2}\.name: cli="alice" ts="bob"$/);
});
