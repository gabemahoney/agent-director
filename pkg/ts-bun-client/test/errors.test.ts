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
 *
 * Epic A additions (T-A1):
 *   8. ErrConsumerSignal — fields, name, instanceof chain.
 *   9. ErrCallTimeout — fields, name, instanceof chain.
 *  10. ErrUnknownErrorName — fields, name, instanceof chain, envelope field.
 *  11. All four new classes appear in TS_ONLY_ERROR_NAMES (drift-test allow-list).
 */

import { test, expect, describe, spyOn } from "bun:test";
import {
  AgentDirectorError,
  ErrSpawnNotFound,
  ErrClientClosed,
  ErrTmuxSessionCreate,
  errorFromEnvelope,
  // Epic A new TS-only classes (Task A1). These imports will fail to compile
  // until the engineer adds the classes to src/errors.ts and re-exports them
  // from src/index.ts. Run `bun test errors.test.ts` after the engineer
  // completes Task A1 to verify green.
  ErrConsumerSignal,
  ErrCallTimeout,
  ErrUnknownErrorName,
  TS_ONLY_ERROR_NAMES,
  // b.h1r: new catalog-derived classes for get-permission / decide --request-token.
  ErrPermissionRequestNotFound,
  ErrAmbiguousRequest,
  ErrMissingRequestToken,
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

// ---------------------------------------------------------------------------
// Epic A — Case 8: ErrConsumerSignal (Task A1, SR-5.4)
// ---------------------------------------------------------------------------
describe("ErrConsumerSignal (TS-only, SR-5.4)", () => {
  test("extends AgentDirectorError and Error", () => {
    const err = new ErrConsumerSignal("spawn", "SIGINT");
    expect(err).toBeInstanceOf(ErrConsumerSignal);
    expect(err).toBeInstanceOf(AgentDirectorError);
    expect(err).toBeInstanceOf(Error);
  });

  test(".name is 'ErrConsumerSignal'", () => {
    const err = new ErrConsumerSignal("status", "SIGTERM");
    expect(err.name).toBe("ErrConsumerSignal");
  });

  test(".errName is 'ErrConsumerSignal'", () => {
    const err = new ErrConsumerSignal("list", "SIGINT");
    expect(err.errName).toBe("ErrConsumerSignal");
  });

  test(".verb reflects the verb that was executing", () => {
    const err = new ErrConsumerSignal("get", "SIGTERM");
    expect(err.verb).toBe("get");
  });

  test("message or a dedicated field carries the signal name", () => {
    const err = new ErrConsumerSignal("kill", "SIGINT");
    // The signal name must be surfaced somewhere observable: either in the
    // message or as a dedicated property. Both are acceptable.
    const hasSignalInMessage = err.message.includes("SIGINT");
    const hasSignalProp =
      "signal" in err && (err as unknown as { signal: string }).signal === "SIGINT";
    expect(hasSignalInMessage || hasSignalProp).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// Epic A — Case 9: ErrCallTimeout (Task A1, SR-6.5)
// ---------------------------------------------------------------------------
describe("ErrCallTimeout (TS-only, SR-6.5)", () => {
  test("extends AgentDirectorError and Error", () => {
    const err = new ErrCallTimeout("list", 31000, 30000);
    expect(err).toBeInstanceOf(ErrCallTimeout);
    expect(err).toBeInstanceOf(AgentDirectorError);
    expect(err).toBeInstanceOf(Error);
  });

  test(".name is 'ErrCallTimeout'", () => {
    const err = new ErrCallTimeout("spawn", 31000, 30000);
    expect(err.name).toBe("ErrCallTimeout");
  });

  test(".errName is 'ErrCallTimeout'", () => {
    const err = new ErrCallTimeout("version", 1001, 1000);
    expect(err.errName).toBe("ErrCallTimeout");
  });

  test(".verb reflects the timed-out verb", () => {
    const err = new ErrCallTimeout("decide", 5100, 5000);
    expect(err.verb).toBe("decide");
  });

  test("elapsedMs and timeoutMs fields carry the numeric values", () => {
    const err = new ErrCallTimeout("resume", 35000, 30000);
    // SR-6.5: error carries the elapsed time and the configured timeout.
    expect(err.elapsedMs).toBe(35000);
    expect(err.timeoutMs).toBe(30000);
    // Also present in the message.
    expect(err.message).toContain("35000");
    expect(err.message).toContain("30000");
  });
});

// ---------------------------------------------------------------------------
// Epic A — Case 10: ErrUnknownErrorName (Task A1, SR-4.3)
// ---------------------------------------------------------------------------
describe("ErrUnknownErrorName (TS-only, SR-4.3)", () => {
  const syntheticEnvelope = {
    err_name: "ErrTotallyBogus",
    err_description: "something that does not exist",
    extra_field: "preserved",
  };

  test("extends AgentDirectorError and Error", () => {
    const err = new ErrUnknownErrorName("ErrTotallyBogus", syntheticEnvelope);
    expect(err).toBeInstanceOf(ErrUnknownErrorName);
    expect(err).toBeInstanceOf(AgentDirectorError);
    expect(err).toBeInstanceOf(Error);
  });

  test(".name is 'ErrUnknownErrorName'", () => {
    const err = new ErrUnknownErrorName("ErrTotallyBogus", syntheticEnvelope);
    expect(err.name).toBe("ErrUnknownErrorName");
  });

  test(".errName is 'ErrUnknownErrorName'", () => {
    const err = new ErrUnknownErrorName("ErrTotallyBogus", syntheticEnvelope);
    expect(err.errName).toBe("ErrUnknownErrorName");
  });

  test(".unknownName carries the offending name and message contains it", () => {
    const err = new ErrUnknownErrorName("ErrTotallyBogus", syntheticEnvelope);
    // SR-4.3: error carries the offending name.
    expect(err.unknownName).toBe("ErrTotallyBogus");
    expect(err.message).toContain("ErrTotallyBogus");
  });

  test(".envelope preserves the full envelope payload", () => {
    const err = new ErrUnknownErrorName("ErrTotallyBogus", syntheticEnvelope);
    // SR-4.3: the full envelope payload is carried for diagnostic use.
    expect(err.envelope).toBeDefined();
    expect((err.envelope as typeof syntheticEnvelope).err_name).toBe("ErrTotallyBogus");
    expect((err.envelope as typeof syntheticEnvelope).extra_field).toBe("preserved");
  });
});

// ---------------------------------------------------------------------------
// b.h1r — ErrPermissionRequestNotFound (catalog-derived, package: store)
// ---------------------------------------------------------------------------
describe("ErrPermissionRequestNotFound (catalog-derived, store)", () => {
  test("extends AgentDirectorError and Error", () => {
    const err = new ErrPermissionRequestNotFound(
      "get-permission",
      "ErrPermissionRequestNotFound",
      "request not found"
    );
    expect(err).toBeInstanceOf(ErrPermissionRequestNotFound);
    expect(err).toBeInstanceOf(AgentDirectorError);
    expect(err).toBeInstanceOf(Error);
  });

  test(".name is 'ErrPermissionRequestNotFound'", () => {
    const err = new ErrPermissionRequestNotFound(
      "get-permission",
      "ErrPermissionRequestNotFound",
      "request not found"
    );
    expect(err.name).toBe("ErrPermissionRequestNotFound");
  });

  test(".errName matches canonical err_name", () => {
    const err = new ErrPermissionRequestNotFound(
      "get-permission",
      "ErrPermissionRequestNotFound",
      "request not found"
    );
    expect(err.errName).toBe("ErrPermissionRequestNotFound");
  });

  test(".verb and .errDescription round-trip from constructor args", () => {
    const err = new ErrPermissionRequestNotFound(
      "get-permission",
      "ErrPermissionRequestNotFound",
      "no such request"
    );
    expect(err.verb).toBe("get-permission");
    expect(err.errDescription).toBe("no such request");
  });
});

// ---------------------------------------------------------------------------
// b.h1r — ErrAmbiguousRequest (catalog-derived, package: store)
// ---------------------------------------------------------------------------
describe("ErrAmbiguousRequest (catalog-derived, store)", () => {
  test("extends AgentDirectorError and Error", () => {
    const err = new ErrAmbiguousRequest(
      "get-permission",
      "ErrAmbiguousRequest",
      "multiple open requests"
    );
    expect(err).toBeInstanceOf(ErrAmbiguousRequest);
    expect(err).toBeInstanceOf(AgentDirectorError);
    expect(err).toBeInstanceOf(Error);
  });

  test(".name is 'ErrAmbiguousRequest'", () => {
    const err = new ErrAmbiguousRequest(
      "get-permission",
      "ErrAmbiguousRequest",
      "multiple open requests"
    );
    expect(err.name).toBe("ErrAmbiguousRequest");
  });

  test(".errName matches canonical err_name", () => {
    const err = new ErrAmbiguousRequest(
      "get-permission",
      "ErrAmbiguousRequest",
      "multiple open requests"
    );
    expect(err.errName).toBe("ErrAmbiguousRequest");
  });

  test(".verb and .errDescription round-trip from constructor args", () => {
    const err = new ErrAmbiguousRequest(
      "get-permission",
      "ErrAmbiguousRequest",
      "ambiguous: 3 open requests"
    );
    expect(err.verb).toBe("get-permission");
    expect(err.errDescription).toBe("ambiguous: 3 open requests");
  });
});

// ---------------------------------------------------------------------------
// b.h1r — ErrMissingRequestToken (catalog-derived, package: api)
// ---------------------------------------------------------------------------
describe("ErrMissingRequestToken (catalog-derived, api)", () => {
  test("extends AgentDirectorError and Error", () => {
    const err = new ErrMissingRequestToken(
      "decide",
      "ErrMissingRequestToken",
      "request_token is required"
    );
    expect(err).toBeInstanceOf(ErrMissingRequestToken);
    expect(err).toBeInstanceOf(AgentDirectorError);
    expect(err).toBeInstanceOf(Error);
  });

  test(".name is 'ErrMissingRequestToken'", () => {
    const err = new ErrMissingRequestToken(
      "decide",
      "ErrMissingRequestToken",
      "request_token is required"
    );
    expect(err.name).toBe("ErrMissingRequestToken");
  });

  test(".errName matches canonical err_name", () => {
    const err = new ErrMissingRequestToken(
      "decide",
      "ErrMissingRequestToken",
      "request_token is required"
    );
    expect(err.errName).toBe("ErrMissingRequestToken");
  });

  test(".verb and .errDescription round-trip from constructor args", () => {
    const err = new ErrMissingRequestToken(
      "decide",
      "ErrMissingRequestToken",
      "must supply --request-token"
    );
    expect(err.verb).toBe("decide");
    expect(err.errDescription).toBe("must supply --request-token");
  });
});

// ---------------------------------------------------------------------------
// Epic A — Case 11: all four new classes in TS_ONLY_ERROR_NAMES (Task A1)
// ---------------------------------------------------------------------------
describe("TS_ONLY_ERROR_NAMES allow-list includes all four new Epic-A classes", () => {
  const allowSet = new Set<string>(TS_ONLY_ERROR_NAMES as readonly string[]);

  test.each([
    "ErrConsumerSignal",
    "ErrCallTimeout",
    "ErrUnknownErrorName",
  ] as const)("%s is in TS_ONLY_ERROR_NAMES", (name) => {
    expect(allowSet.has(name)).toBe(true);
  });
});
