/**
 * errors.test.ts — src/errors.ts and src/internal/errorMap.ts: the base class,
 * both err_name factories over the whole catalog (SR-4.1–4.4), and the TS-only
 * classes' fields and messages (SR-3, SR-5.4, SR-6.5, b.cot, b.xht).
 */

import { test, expect, spyOn } from "bun:test";
import {
  AgentDirectorError,
  ErrBunVersionTooOld,
  ErrCallTimeout,
  ErrCallerCwdUnreachable,
  ErrClientClosed,
  ErrConsumerSignal,
  ErrSystemInstallDisappeared,
  ErrSystemInstallNotFound,
  ErrSystemInstallTooOld,
  ErrSystemInstallUnreachable,
  ErrUnknownErrorName,
  TS_ONLY_ERROR_NAMES,
  errorFromEnvelope,
  type CheckedLocation,
} from "../src/errors.js";
import { errorMap, throwFromEnvelope } from "../src/internal/errorMap.js";
import { thrownBy } from "./internal/helper.js";
import { loadErrNameCatalog } from "./internal/loadCatalog.js";

test("base class: verb, errName, errDescription, name and `${err_name}: ${err_description}` message", () => {
  const err = new AgentDirectorError("spawn", "ErrCwdMissing", "cwd was not provided");
  expect(err).toBeInstanceOf(Error);
  expect([err.verb, err.errName, err.errDescription, err.name, err.message]).toEqual([
    "spawn", "ErrCwdMissing", "cwd was not provided", "AgentDirectorError", "ErrCwdMissing: cwd was not provided",
  ]);
});

// SR-4.1/4.2: both factories (the public errorFromEnvelope, the Client's throwFromEnvelope)
// build each catalog name's own subclass, never the base or the unknown-name fallback.
test("every catalog err_name maps to its own subclass in errorFromEnvelope and throwFromEnvelope", () => {
  const catalog = loadErrNameCatalog();
  expect(errorMap.size).toBe(catalog.length);
  for (const name of catalog) {
    const thrown = thrownBy(() => throwFromEnvelope("list", { err_name: name, err_description: "d" }));
    for (const err of [errorFromEnvelope("list", name, "d"), thrown as AgentDirectorError]) {
      expect(err).toBeInstanceOf(AgentDirectorError);
      expect([err.name, err.errName, err.verb, err.message]).toEqual([name, name, "list", `${name}: d`]);
    }
  }
});

// SR-4.3: a name outside the catalog (a TS-only name included) is not mapped.
test("unknown err_name: errorFromEnvelope warns once and returns the base; throwFromEnvelope throws ErrUnknownErrorName", () => {
  const spy = spyOn(console, "warn").mockImplementation(() => {});
  try {
    const err = errorFromEnvelope("kill", "ErrClientClosed", "desc");
    expect(err.constructor).toBe(AgentDirectorError);
    expect(err.errName).toBe("ErrClientClosed");
    expect(spy).toHaveBeenCalledTimes(1);
  } finally {
    spy.mockRestore();
  }
  const envelope = { err_name: "ErrTotallyBogus", err_description: "x", extra: "kept" };
  const thrown = thrownBy(() => throwFromEnvelope("get", envelope)) as ErrUnknownErrorName;
  expect(thrown).toBeInstanceOf(ErrUnknownErrorName);
  expect(thrown.unknownName).toBe("ErrTotallyBogus");
  expect(thrown.envelope).toEqual(envelope);
  expect(thrown.message).toContain("ErrTotallyBogus");
});

// b.146 rule 15: an envelope's err_details object reaches errDetails through both factories, as
// sent; an envelope without one, or with a non-object value, gives null.
test.each([
  ["an object", { request_token: "t", not_before: null, nested: { n_lines: 25 } }, { request_token: "t", not_before: null, nested: { n_lines: 25 } }],
  ["absent", undefined, null],
  ["null", null, null],
  ["a list", [1, 2], null],
  ["a string", "details", null],
] as const)("err_details %s: errDetails through errorFromEnvelope and throwFromEnvelope", (_case, details, want) => {
  const env: Record<string, unknown> = { err_name: "ErrClaimTooSoon", err_description: "d" };
  if (details !== undefined) env["err_details"] = details;
  const thrown = thrownBy(() => throwFromEnvelope("record-pane-answer", env)) as AgentDirectorError;
  const built = errorFromEnvelope("record-pane-answer", "ErrClaimTooSoon", "d",
    want as Readonly<Record<string, unknown>> | null);
  for (const err of [thrown, built]) {
    expect(err.errName).toBe("ErrClaimTooSoon");
    expect(err.errDetails).toEqual(want);
  }
  expect(new AgentDirectorError("v", "ErrX", "d").errDetails).toBeNull();
});

const locs: CheckedLocation[] = [
  { kind: "standard-install-path", detail: "/h/.agent-director/bin/agent-director" },
  { kind: "path-lookup", detail: "/usr/bin" },
];
const cause = new Error("ENOENT: no such file or directory");
const envelope = { err_name: "ErrTotallyBogus", err_description: "d" };

// [class name, instance, own fields, message fragments]. b.ggk: ErrClientClosed names
// Client.create(), never the private `new Client()` (no message may name it).
const TS_ONLY: Array<[string, AgentDirectorError, Record<string, unknown>, string[]]> = [
  ["ErrClientClosed", new ErrClientClosed(),
    { verb: "", errDescription: "client is closed: call Client.create() to obtain a fresh handle" },
    ["ErrClientClosed: client is closed: call Client.create() to obtain a fresh handle"]],
  ["ErrBunVersionTooOld", new ErrBunVersionTooOld("1.0.0", "1.0.21"), {}, ["Bun 1.0.0", "1.0.21"]],
  ["ErrConsumerSignal", new ErrConsumerSignal("kill", "SIGINT"), { verb: "kill", signal: "SIGINT" }, ["SIGINT"]],
  ["ErrCallTimeout", new ErrCallTimeout("resume", 35000, 30000),
    { verb: "resume", elapsedMs: 35000, timeoutMs: 30000 }, ["35000", "30000"]],
  ["ErrUnknownErrorName", new ErrUnknownErrorName("ErrTotallyBogus", envelope),
    { verb: "", unknownName: "ErrTotallyBogus", envelope }, ["ErrTotallyBogus"]],
  ["ErrSystemInstallNotFound", new ErrSystemInstallNotFound(locs), { verb: "", checkedLocations: locs },
    ["standard install path (/h/.agent-director/bin/agent-director)", "PATH lookup (PATH=/usr/bin)"]],
  ["ErrSystemInstallTooOld", new ErrSystemInstallTooOld("0.6.3", "0.7.0", "/usr/bin/agent-director"),
    { actualVersion: "0.6.3", requiredVersion: "0.7.0", binaryPath: "/usr/bin/agent-director" },
    ["0.6.3", "0.7.0", "/usr/bin/agent-director"]],
  ["ErrSystemInstallUnreachable", new ErrSystemInstallUnreachable("/p", "other"),
    { binaryPath: "/p", reason: "other", diagnostic: null, exitCode: null, signal: null }, ["/p is unreachable (other)"]],
  ["ErrCallerCwdUnreachable", new ErrCallerCwdUnreachable("/gone", cause), { verb: "", cwd: "/gone", cause },
    ["/gone is unreachable: ENOENT"]],
  ["ErrSystemInstallDisappeared", new ErrSystemInstallDisappeared("list", "/bin/ad", cause),
    { verb: "list", binaryPath: "/bin/ad", cause }, ["/bin/ad has disappeared"]],
];

test("TS_ONLY_ERROR_NAMES lists exactly the TS-only classes", () => {
  expect([...TS_ONLY_ERROR_NAMES].sort() as string[]).toEqual(TS_ONLY.map(([name]) => name).sort());
});

// SR-3.4: each extends AgentDirectorError directly (no shared parent).
test.each(TS_ONLY)("%s: direct subclass, name == errName, fields and message", (name, err, fields, fragments) => {
  expect(Object.getPrototypeOf(err.constructor.prototype)).toBe(AgentDirectorError.prototype);
  expect([err.name, err.errName, err.constructor.name]).toEqual([name, name, name]);
  expect(err).toMatchObject(fields);
  expect(err.message.startsWith(`${name}: `)).toBe(true);
  for (const f of fragments) expect(err.message).toContain(f);
  expect(err.message).not.toContain("new Client()");
});

test("ErrCallerCwdUnreachable: cause defaults to null", () => {
  expect(new ErrCallerCwdUnreachable("/some/dir").cause).toBeNull();
});

test.each([
  ["probe-nonzero-exit", { exitCode: 7, diagnostic: "stderr text" }, "(probe-nonzero-exit) (exit 7)"],
  ["probe-killed-by-signal", { signal: "SIGSEGV" }, "(probe-killed-by-signal) (signal SIGSEGV)"],
  ["spawn-failed", { diagnostic: "a\n  b" }, "(spawn-failed): a b"],
] as const)("ErrSystemInstallUnreachable %s: opts kept and summarized in the message", (reason, opts, tail) => {
  const err = new ErrSystemInstallUnreachable("/bin", reason, opts);
  expect(err).toMatchObject({ exitCode: null, signal: null, diagnostic: null, ...opts });
  expect(err.message.endsWith(tail)).toBe(true);
});
