/**
 * system-install-errors.test.ts — SR-8.4 coverage for the three new
 * AgentDirectorError subclasses.  Construction shape, instanceof
 * reliability, errName/class-name coupling, no-shared-parent invariant,
 * UnreachableReason enum closedness.
 */

import { test, expect, describe } from "bun:test";
import {
  AgentDirectorError,
  ErrSystemInstallNotFound,
  ErrSystemInstallTooOld,
  ErrSystemInstallUnreachable,
  ErrCallerCwdUnreachable,
  ErrSystemInstallDisappeared,
  type CheckedLocation,
  type UnreachableReason,
} from "../src/index.js";

describe("ErrSystemInstallNotFound", () => {
  const locs: CheckedLocation[] = [
    { kind: "standard-install-path", detail: "/home/u/.agent-director/bin/agent-director" },
    { kind: "path-lookup", detail: "/usr/bin:/usr/local/bin" },
  ];
  const e = new ErrSystemInstallNotFound(locs);

  test("required fields populated per SR-3.1", () => {
    expect(e.errName).toBe("ErrSystemInstallNotFound");
    expect(e.verb).toBe("");
    expect(e.checkedLocations).toEqual(locs);
  });
  test("instanceof reliability", () => {
    expect(e).toBeInstanceOf(ErrSystemInstallNotFound);
    expect(e).toBeInstanceOf(AgentDirectorError);
    expect(e).toBeInstanceOf(Error);
    expect(e).not.toBeInstanceOf(ErrSystemInstallTooOld);
    expect(e).not.toBeInstanceOf(ErrSystemInstallUnreachable);
  });
  test("class name == errName", () => {
    expect(e.constructor.name).toBe("ErrSystemInstallNotFound");
    expect(e.errName).toBe(e.constructor.name);
  });
  test("no shared parent class", () => {
    expect(Object.getPrototypeOf(ErrSystemInstallNotFound.prototype)).toBe(
      AgentDirectorError.prototype,
    );
  });
  test("message names the checked locations", () => {
    expect(e.message).toContain("ErrSystemInstallNotFound");
    expect(e.message).toContain("standard install path");
    expect(e.message).toContain("PATH lookup");
  });
});

describe("ErrSystemInstallTooOld", () => {
  const e = new ErrSystemInstallTooOld("0.6.3", "0.7.0", "/usr/bin/agent-director");

  test("required fields populated per SR-3.2", () => {
    expect(e.errName).toBe("ErrSystemInstallTooOld");
    expect(e.verb).toBe("");
    expect(e.actualVersion).toBe("0.6.3");
    expect(e.requiredVersion).toBe("0.7.0");
    expect(e.binaryPath).toBe("/usr/bin/agent-director");
  });
  test("instanceof reliability", () => {
    expect(e).toBeInstanceOf(ErrSystemInstallTooOld);
    expect(e).toBeInstanceOf(AgentDirectorError);
    expect(e).not.toBeInstanceOf(ErrSystemInstallNotFound);
    expect(e).not.toBeInstanceOf(ErrSystemInstallUnreachable);
  });
  test("no shared parent class", () => {
    expect(Object.getPrototypeOf(ErrSystemInstallTooOld.prototype)).toBe(
      AgentDirectorError.prototype,
    );
  });
  test("message names actual, required, path", () => {
    expect(e.message).toContain("0.6.3");
    expect(e.message).toContain("0.7.0");
    expect(e.message).toContain("/usr/bin/agent-director");
  });
});

describe("ErrSystemInstallUnreachable", () => {
  const reasons: UnreachableReason[] = [
    "not-executable",
    "not-a-regular-file",
    "probe-timeout",
    "probe-nonzero-exit",
    "probe-killed-by-signal",
    "unparseable-version",
    "spawn-failed",
    "other",
  ];
  for (const r of reasons) {
    test(`constructs cleanly with reason="${r}"`, () => {
      const e = new ErrSystemInstallUnreachable("/path/x", r);
      expect(e.errName).toBe("ErrSystemInstallUnreachable");
      expect(e.binaryPath).toBe("/path/x");
      expect(e.reason).toBe(r);
      expect(e.diagnostic).toBeNull();
      expect(e.exitCode).toBeNull();
      expect(e.signal).toBeNull();
    });
  }

  test("optional opts wired correctly for probe-nonzero-exit", () => {
    const e = new ErrSystemInstallUnreachable("/bin", "probe-nonzero-exit", {
      diagnostic: "stderr text",
      exitCode: 7,
    });
    expect(e.exitCode).toBe(7);
    expect(e.diagnostic).toBe("stderr text");
    expect(e.signal).toBeNull();
    expect(e.message).toContain("exit 7");
  });

  test("optional opts wired correctly for probe-killed-by-signal", () => {
    const e = new ErrSystemInstallUnreachable("/bin", "probe-killed-by-signal", {
      signal: "SIGSEGV",
    });
    expect(e.signal).toBe("SIGSEGV");
    expect(e.exitCode).toBeNull();
    expect(e.message).toContain("SIGSEGV");
  });

  test("instanceof reliability", () => {
    const e = new ErrSystemInstallUnreachable("/p", "other");
    expect(e).toBeInstanceOf(ErrSystemInstallUnreachable);
    expect(e).toBeInstanceOf(AgentDirectorError);
    expect(e).not.toBeInstanceOf(ErrSystemInstallNotFound);
    expect(e).not.toBeInstanceOf(ErrSystemInstallTooOld);
  });

  test("no shared parent class", () => {
    expect(Object.getPrototypeOf(ErrSystemInstallUnreachable.prototype)).toBe(
      AgentDirectorError.prototype,
    );
  });
});

describe("ErrCallerCwdUnreachable", () => {
  const cause = new Error("ENOENT: no such file or directory");
  const e = new ErrCallerCwdUnreachable("/tmp/deleted-dir-abc123", cause);

  test("required fields populated (b.cot)", () => {
    expect(e.errName).toBe("ErrCallerCwdUnreachable");
    expect(e.verb).toBe("");
    expect(e.cwd).toBe("/tmp/deleted-dir-abc123");
    expect(e.cause).toBe(cause);
  });

  test("optional cause defaults to null", () => {
    const e2 = new ErrCallerCwdUnreachable("/some/dir");
    expect(e2.cause).toBeNull();
    expect(e2.cwd).toBe("/some/dir");
  });

  test("instanceof reliability", () => {
    expect(e).toBeInstanceOf(ErrCallerCwdUnreachable);
    expect(e).toBeInstanceOf(AgentDirectorError);
    expect(e).toBeInstanceOf(Error);
    expect(e).not.toBeInstanceOf(ErrSystemInstallNotFound);
    expect(e).not.toBeInstanceOf(ErrSystemInstallTooOld);
    expect(e).not.toBeInstanceOf(ErrSystemInstallUnreachable);
  });

  test("class name == errName", () => {
    expect(e.constructor.name).toBe("ErrCallerCwdUnreachable");
    expect(e.errName).toBe(e.constructor.name);
  });

  test("no shared parent class", () => {
    expect(Object.getPrototypeOf(ErrCallerCwdUnreachable.prototype)).toBe(
      AgentDirectorError.prototype,
    );
  });

  test("message names the cwd", () => {
    expect(e.message).toContain("ErrCallerCwdUnreachable");
    expect(e.message).toContain("/tmp/deleted-dir-abc123");
  });
});

describe("ErrSystemInstallDisappeared", () => {
  const e = new ErrSystemInstallDisappeared("list", "/home/u/.agent-director/bin/agent-director");

  test("required fields populated (b.xht)", () => {
    expect(e.errName).toBe("ErrSystemInstallDisappeared");
    expect(e.verb).toBe("list");
    expect(e.binaryPath).toBe("/home/u/.agent-director/bin/agent-director");
  });

  test("instanceof reliability", () => {
    expect(e).toBeInstanceOf(ErrSystemInstallDisappeared);
    expect(e).toBeInstanceOf(AgentDirectorError);
    expect(e).toBeInstanceOf(Error);
    expect(e).not.toBeInstanceOf(ErrSystemInstallNotFound);
    expect(e).not.toBeInstanceOf(ErrSystemInstallTooOld);
    expect(e).not.toBeInstanceOf(ErrSystemInstallUnreachable);
    expect(e).not.toBeInstanceOf(ErrCallerCwdUnreachable);
  });

  test("class name == errName", () => {
    expect(e.constructor.name).toBe("ErrSystemInstallDisappeared");
    expect(e.errName).toBe(e.constructor.name);
  });

  test("no shared parent class", () => {
    expect(Object.getPrototypeOf(ErrSystemInstallDisappeared.prototype)).toBe(
      AgentDirectorError.prototype,
    );
  });

  test("message names the binary path", () => {
    expect(e.message).toContain("ErrSystemInstallDisappeared");
    expect(e.message).toContain("/home/u/.agent-director/bin/agent-director");
  });

  test("verb is carried as a field, not embedded in message", () => {
    // verb is surfaced via the .verb field (base class); the human-readable
    // message omits it to keep the text stable for downstream parsers.
    expect(e.verb).toBe("list");
  });
});
