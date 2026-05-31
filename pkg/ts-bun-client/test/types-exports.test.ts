/**
 * types-exports.test.ts — verify that Params/Result types and error classes
 * are correctly exported from the package (T4 subtask 9y).
 *
 * (a) Runtime: assert representative runtime exports (error classes, factory,
 *     Client) exist on the package namespace.
 * (b) Compile-time: @ts-expect-error block proves omitting SpawnParams.cwd
 *     (a required field) is a type error caught by `bun run typecheck`.
 */

import { test, expect, describe } from "bun:test";
import type { SpawnParams } from "../src/types.js";

// ---------------------------------------------------------------------------
// (a) Runtime assertions — interfaces are type-erased; we check runtime values.
// ---------------------------------------------------------------------------
describe("types-exports: runtime namespace", () => {
  test("error classes are exported as constructor functions", async () => {
    const mod = await import("../src/index.js");
    expect(typeof mod.AgentDirectorError).toBe("function");
    expect(typeof mod.ErrSpawnNotFound).toBe("function");
    expect(typeof mod.ErrClientClosed).toBe("function");
    expect(typeof mod.ErrTmuxNotAvailable).toBe("function");
    expect(typeof mod.ErrCwdMissing).toBe("function");
    expect(typeof mod.errorFromEnvelope).toBe("function");
    expect(typeof mod.Client).toBe("function");
  });

  test("all 37 catalog error subclasses are exported", async () => {
    const mod = await import("../src/index.js") as Record<string, unknown>;
    const catalogNames = [
      "ErrCwdMissing",
      "ErrCwdNotAPath",
      "ErrCwdNotFound",
      "ErrCwdNotADirectory",
      "ErrRelayModeInvalid",
      "ErrSpawnDeniedFlag",
      "ErrReservedEnvKey",
      "ErrInstanceIdCollision",
      "ErrTmuxSessionNameEmpty",
      "ErrTmuxSessionNameInvalid",
      "ErrTmuxSessionNameTooLong",
      "ErrSpawnNotFound",
      "ErrTmuxNotAvailable",
      "ErrTmuxSessionCreate",
      "ErrTmuxSendKeys",
      "ErrTmuxCaptureFailed",
      "ErrSpawnNotInteractive",
      "ErrSendKeysWhileRelayed",
      "ErrSpawnNotPausable",
      "ErrPauseTimeout",
      "ErrListInvalidLabel",
      "ErrTemplateNameUnsafe",
      "ErrTemplateNotFound",
      "ErrTemplateMalformed",
      "ErrTemplateExists",
      "ErrProbeUnsupported",
      "ErrSpawnNotResumable",
      "ErrNoSessionId",
      "ErrJsonlMissing",
      "ErrRelayModeOff",
      "ErrInvalidDecision",
      "ErrNoOpenPermissionRequest",
      "ErrAlreadyDecided",
      // b.h1r: four new catalog-derived entries (store + api packages)
      "ErrPermissionRequestNotFound",
      "ErrAmbiguousRequest",
      "ErrMissingRequestToken",
      "ErrInvalidFlags",
    ];
    expect(catalogNames).toHaveLength(37);
    for (const name of catalogNames) {
      expect(typeof mod[name], `${name} should be a function`).toBe("function");
    }
  });

  test("errorFromEnvelope produces typed subclasses", async () => {
    const { errorFromEnvelope, ErrSpawnNotFound, AgentDirectorError } =
      await import("../src/errors.js");
    const err = errorFromEnvelope("status", "ErrSpawnNotFound", "no such spawn");
    expect(err).toBeInstanceOf(ErrSpawnNotFound);
    expect(err).toBeInstanceOf(AgentDirectorError);
    expect(err.errName).toBe("ErrSpawnNotFound");
    expect(err.verb).toBe("status");
  });
});

// ---------------------------------------------------------------------------
// (b) Compile-time assertion — processed by `bun run typecheck` (tsc --noEmit).
//
// tsconfig.json now includes test/**/* so tsc checks this file.
//
// The @ts-expect-error below proves SpawnParams.cwd is required. If cwd is
// ever made optional, tsc will report "Unused '@ts-expect-error' directive"
// and the typecheck gate will fail — keeping CI honest.
// ---------------------------------------------------------------------------

function _assertSpawnParamsCwdIsRequired(): void {
  // @ts-expect-error — SpawnParams.cwd is required; omitting it is a type error.
  const _bad: SpawnParams = {};
  void _bad;
}
// Reference the function so noUnusedLocals does not flag it.
void _assertSpawnParamsCwdIsRequired;
