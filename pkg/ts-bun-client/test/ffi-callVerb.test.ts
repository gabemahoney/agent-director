/**
 * Unit tests for the callVerb recipe (src/ffi.ts).
 *
 * callVerb delegates to workerProxy.dispatch. These tests replace
 * workerProxy.dispatch with a stub so no native library is required.
 *
 * workerProxy exports a mutable object (`workerProxy.dispatch`) precisely so
 * tests can patch it without module-level mocking infrastructure.
 *
 * Cases tested:
 *   (a) Success envelope → callVerb resolves with typed result
 *   (b) Error envelope (err_name present) → callVerb rejects with AgentDirectorError
 *   (c) dispatch rejection (ffi-error) → callVerb rejects (propagates)
 *   (d) params are passed (as object) to dispatch; dispatch stringifies them
 *   (e) exactly one dispatch call per callVerb invocation
 */

import { test, expect, describe, beforeEach } from "bun:test";
import { workerProxy } from "../src/internal/workerProxy.js";
import { callVerb } from "../src/ffi.js";
import { AgentDirectorError } from "../src/errors.js";

// ---------------------------------------------------------------------------
// Stub type
// ---------------------------------------------------------------------------
type DispatchFn = typeof workerProxy.dispatch;
let _originalDispatch: DispatchFn;

beforeEach(() => {
  // Save original so we can restore if needed (good hygiene).
  _originalDispatch = workerProxy.dispatch;
});

// Helper: restore original dispatch after test.
function withDispatch(fn: DispatchFn, test_: () => Promise<void>): () => Promise<void> {
  return async () => {
    workerProxy.dispatch = fn;
    try {
      await test_();
    } finally {
      workerProxy.dispatch = _originalDispatch;
    }
  };
}

describe("callVerb recipe", () => {
  // (a) Success envelope → typed result.
  test(
    "(a) success envelope resolves to typed result",
    withDispatch(
      async (_op, _handle, _params) => {
        // Simulate workerProxy returning a parsed success value.
        return { version: "1.2.3", commit: "abc" };
      },
      async () => {
        const result = await callVerb<Record<string, unknown>, { version: string }>(
          "version", null, {}
        );
        expect(result.version).toBe("1.2.3");
      }
    )
  );

  // (b) Error envelope → throws AgentDirectorError with right err_name.
  test(
    "(b) dispatch rejecting with AgentDirectorError propagates correctly",
    withDispatch(
      async (_op, _handle, _params) => {
        throw new AgentDirectorError("ErrUnknownHandle", "unknown handle");
      },
      async () => {
        let caught: unknown;
        try {
          await callVerb("spawn", "dead-handle", { cwd: "/tmp" });
        } catch (e) {
          caught = e;
        }
        expect(caught).toBeInstanceOf(AgentDirectorError);
        expect((caught as AgentDirectorError).errName).toBe("ErrUnknownHandle");
      }
    )
  );

  // (c) Generic dispatch rejection propagates as rejected Promise.
  test(
    "(c) generic dispatch rejection propagates",
    withDispatch(
      async (_op, _handle, _params) => {
        throw new Error("FFI error: symbol not found");
      },
      async () => {
        await expect(
          callVerb("spawn", null, {})
        ).rejects.toThrow("FFI error: symbol not found");
      }
    )
  );

  // (d) The params object is passed to dispatch (dispatch does stringification).
  test(
    "(d) params object is forwarded to dispatch",
    withDispatch(
      async (op, handle, params) => {
        // Assert what dispatch received.
        expect(op).toBe("send-keys");
        expect(handle).toBe("my-handle");
        expect(params).toEqual({ keys: "ls\n" });
        return { ok: true };
      },
      async () => {
        await callVerb("send-keys", "my-handle", { keys: "ls\n" });
      }
    )
  );

  // (e) Exactly one dispatch call per callVerb invocation.
  test(
    "(e) exactly one dispatch call per callVerb",
    async () => {
      let callCount = 0;
      workerProxy.dispatch = async (_op, _handle, _params) => {
        callCount++;
        return {};
      };
      try {
        await callVerb("list", "h", {});
        await callVerb("list", "h", {});
        expect(callCount).toBe(2);
      } finally {
        workerProxy.dispatch = _originalDispatch;
      }
    }
  );
});
