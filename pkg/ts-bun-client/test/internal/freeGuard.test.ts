/**
 * Unit tests for FreeGuard (src/internal/freeGuard.ts).
 *
 * All tests use a test-double free function. No native library is required.
 */

import { test, expect, describe } from "bun:test";
import { FreeGuard } from "../../src/internal/freeGuard.js";

describe("FreeGuard", () => {
  // (a) First free() invokes the underlying free function exactly once with
  //     the original pointer.
  test("(a) first free() invokes the underlying function with the pointer", () => {
    const calls: number[] = [];
    const testPtr = 42;
    const guard = new FreeGuard(testPtr, (p) => calls.push(p));

    expect(guard.ptr).toBe(testPtr);
    guard.free();

    expect(calls).toHaveLength(1);
    expect(calls[0]).toBe(testPtr);
  });

  // (b) Second free() is a no-op — the underlying free function is NOT called
  //     again.
  test("(b) second free() is a no-op (free function not called again)", () => {
    let callCount = 0;
    const guard = new FreeGuard(99, () => { callCount++; });

    guard.free(); // first call
    guard.free(); // second call — should be no-op

    expect(callCount).toBe(1);
  });

  // (c) ptr getter returns null after free().
  test("(c) ptr is null after free()", () => {
    const guard = new FreeGuard(1234, () => { /* stub */ });

    expect(guard.ptr).toBe(1234);
    guard.free();
    expect(guard.ptr).toBeNull();
  });

  // Extra: free() with a bigint pointer works (generic T).
  test("(d) works with bigint pointers", () => {
    const calls: bigint[] = [];
    const testPtr = 0xdeadbeefn;
    const guard = new FreeGuard(testPtr, (p) => calls.push(p));

    guard.free();

    expect(calls).toHaveLength(1);
    expect(calls[0]).toBe(testPtr);
    expect(guard.ptr).toBeNull();
  });

  // Extra: exception inside the free function does not leave ptr non-null
  // after the guard has set _ptr = null (atomic null-then-free).
  test("(e) ptr is null even if free function throws", () => {
    const guard = new FreeGuard(7, () => {
      throw new Error("free exploded");
    });

    expect(() => guard.free()).toThrow("free exploded");
    // ptr should be null because we null it BEFORE calling _free.
    expect(guard.ptr).toBeNull();
  });
});
