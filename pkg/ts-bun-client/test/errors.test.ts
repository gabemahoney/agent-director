/**
 * errors.test.ts — unit tests for src/errors.ts (T4 subtask 3q).
 *
 * 6 cases:
 *   1. Base class fields are set correctly by the constructor.
 *   2. Subclass `.name` matches the class name (not "AgentDirectorError").
 *   3. instanceof chain: Subclass → AgentDirectorError → Error.
 *   4. errorFromEnvelope factory returns the correct typed subclass.
 *   5. Unknown err_name → base AgentDirectorError + console.warn.
 *   6. .message format is "${err_name}: ${err_description}".
 */

import { test, expect, describe, spyOn } from "bun:test";
import {
  AgentDirectorError,
  ErrSpawnNotFound,
  ErrClientClosed,
  ErrTmuxSessionCreate,
  errorFromEnvelope,
} from "../src/errors.js";

// ---------------------------------------------------------------------------
// Case 1: Base class fields
// ---------------------------------------------------------------------------
describe("AgentDirectorError base class fields", () => {
  test("verb, errName, errDescription are set from constructor args", () => {
    const err = new AgentDirectorError("spawn", "ErrSpawnNotFound", "spawn not found");
    expect(err.verb).toBe("spawn");
    expect(err.errName).toBe("ErrSpawnNotFound");
    expect(err.errDescription).toBe("spawn not found");
  });

  test("fields are readonly (type-level; not tested at runtime)", () => {
    // Just verify they exist and are strings.
    const err = new AgentDirectorError("status", "ErrSomeError", "desc");
    expect(typeof err.verb).toBe("string");
    expect(typeof err.errName).toBe("string");
    expect(typeof err.errDescription).toBe("string");
  });
});

// ---------------------------------------------------------------------------
// Case 2: Subclass .name
// ---------------------------------------------------------------------------
describe("subclass .name reflects the subclass", () => {
  test("ErrSpawnNotFound.name is 'ErrSpawnNotFound'", () => {
    const err = new ErrSpawnNotFound("status", "ErrSpawnNotFound", "not found");
    expect(err.name).toBe("ErrSpawnNotFound");
  });

  test("ErrClientClosed.name is 'ErrClientClosed'", () => {
    const err = new ErrClientClosed();
    expect(err.name).toBe("ErrClientClosed");
  });

  test("base AgentDirectorError.name is 'AgentDirectorError'", () => {
    const err = new AgentDirectorError("get", "ErrSome", "desc");
    expect(err.name).toBe("AgentDirectorError");
  });
});

// ---------------------------------------------------------------------------
// Case 3: instanceof chain
// ---------------------------------------------------------------------------
describe("instanceof chain", () => {
  test("ErrSpawnNotFound is instanceof ErrSpawnNotFound, AgentDirectorError, and Error", () => {
    const err = new ErrSpawnNotFound("status", "ErrSpawnNotFound", "not found");
    expect(err).toBeInstanceOf(ErrSpawnNotFound);
    expect(err).toBeInstanceOf(AgentDirectorError);
    expect(err).toBeInstanceOf(Error);
  });

  test("ErrClientClosed is instanceof ErrClientClosed, AgentDirectorError, and Error", () => {
    const err = new ErrClientClosed();
    expect(err).toBeInstanceOf(ErrClientClosed);
    expect(err).toBeInstanceOf(AgentDirectorError);
    expect(err).toBeInstanceOf(Error);
  });

  test("ErrTmuxSessionCreate is instanceof AgentDirectorError", () => {
    const err = new ErrTmuxSessionCreate("spawn", "ErrTmuxSessionCreate", "create failed");
    expect(err).toBeInstanceOf(ErrTmuxSessionCreate);
    expect(err).toBeInstanceOf(AgentDirectorError);
    expect(err).toBeInstanceOf(Error);
  });
});

// ---------------------------------------------------------------------------
// Case 4: Factory returns the right subclass
// ---------------------------------------------------------------------------
describe("errorFromEnvelope factory", () => {
  test("returns ErrSpawnNotFound for known err_name", () => {
    const err = errorFromEnvelope("status", "ErrSpawnNotFound", "instance not found");
    expect(err).toBeInstanceOf(ErrSpawnNotFound);
    expect(err.errName).toBe("ErrSpawnNotFound");
    expect(err.verb).toBe("status");
  });

  test("returned instance is also instanceof AgentDirectorError", () => {
    const err = errorFromEnvelope("spawn", "ErrCwdMissing", "cwd missing");
    expect(err).toBeInstanceOf(AgentDirectorError);
    expect(err).toBeInstanceOf(Error);
  });

  test("ErrClientClosed (TS-only) is NOT in factory table — returns base class", () => {
    // ErrClientClosed is TS-only and is NOT in ERROR_TABLE.
    const spy = spyOn(console, "warn").mockImplementation(() => {});
    try {
      const err = errorFromEnvelope("", "ErrClientClosed", "client closed");
      expect(err.constructor).toBe(AgentDirectorError);
      expect(spy).toHaveBeenCalledTimes(1);
    } finally {
      spy.mockRestore();
    }
  });
});

// ---------------------------------------------------------------------------
// Case 5: Unknown err_name → generic AgentDirectorError + console.warn
// ---------------------------------------------------------------------------
describe("errorFromEnvelope unknown err_name", () => {
  test("unknown name returns base AgentDirectorError", () => {
    const spy = spyOn(console, "warn").mockImplementation(() => {});
    try {
      const err = errorFromEnvelope("spawn", "ErrNonExistentError", "something broke");
      expect(err.constructor).toBe(AgentDirectorError);
      expect(err.errName).toBe("ErrNonExistentError");
    } finally {
      spy.mockRestore();
    }
  });

  test("unknown name calls console.warn exactly once", () => {
    const spy = spyOn(console, "warn").mockImplementation(() => {});
    try {
      errorFromEnvelope("kill", "ErrUnknownSomething", "desc");
      expect(spy).toHaveBeenCalledTimes(1);
    } finally {
      spy.mockRestore();
    }
  });
});

// ---------------------------------------------------------------------------
// Case 6: .message format
// ---------------------------------------------------------------------------
describe("AgentDirectorError .message format", () => {
  test("message is '${err_name}: ${err_description}'", () => {
    const err = new AgentDirectorError("spawn", "ErrCwdMissing", "cwd was not provided");
    expect(err.message).toBe("ErrCwdMissing: cwd was not provided");
  });

  test("subclass message follows same format", () => {
    const err = new ErrSpawnNotFound("get", "ErrSpawnNotFound", "no such spawn");
    expect(err.message).toBe("ErrSpawnNotFound: no such spawn");
  });

  test("factory-produced error has correct message", () => {
    const err = errorFromEnvelope("resume", "ErrSpawnNotResumable", "state is working");
    expect(err.message).toBe("ErrSpawnNotResumable: state is working");
  });
});
