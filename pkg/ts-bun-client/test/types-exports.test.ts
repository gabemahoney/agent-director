/**
 * types-exports.test.ts — the package root's runtime exports (SR-4), the
 * vendored-binary classes' removal (SR-4.6, b.ue3), and type-level pins checked
 * by `bun run typecheck`.
 */

import { test, expect } from "bun:test";
import * as ad from "../src/index.js";
import type { SpawnParams, GetResult, ListRow, DecideResult, PermissionRequestInfo, Delivery } from "../src/types.js";
import { TS_ONLY_ERROR_NAMES } from "../src/errors.js";
import { loadErrNameCatalog } from "./internal/loadCatalog.js";

const mod = ad as Record<string, unknown>;

test("every catalog and TS-only error class, the factory, Client and resolveSystemBinary are exported", () => {
  const names = [
    ...loadErrNameCatalog(), ...TS_ONLY_ERROR_NAMES,
    "AgentDirectorError", "errorFromEnvelope", "Client", "resolveSystemBinary",
  ];
  for (const name of names) expect(typeof mod[name], name).toBe("function");
  expect(typeof ad.MIN_BINARY_VERSION).toBe("string");
});

test("DEV_SENTINEL_VERSION is the literal '0.0.0-dev' (SR-4.5)", () => {
  // Typecheck fails if the constant widens to `string`.
  const x: "0.0.0-dev" = ad.DEV_SENTINEL_VERSION;
  expect(x).toBe("0.0.0-dev");
});

test("vendored-binary error classes are gone (SR-4.6)", () => {
  for (const name of ["ErrUnsupportedPlatform", "ErrPlatformPackageMissing", "ErrCliNotExecutable"]) {
    expect(mod[name], name).toBeUndefined();
  }
  // @ts-expect-error — removed in b.ue3
  void ad.ErrUnsupportedPlatform;
  // @ts-expect-error — removed in b.ue3
  void ad.ErrPlatformPackageMissing;
  // @ts-expect-error — removed in b.ue3
  void ad.ErrCliNotExecutable;
});

// Typecheck: SpawnParams.cwd is required; tsc reports an unused directive if it becomes optional.
function _assertSpawnParamsCwdIsRequired(): void {
  // @ts-expect-error — SpawnParams.cwd is required
  const _bad: SpawnParams = {};
  void _bad;
}
void _assertSpawnParamsCwdIsRequired;

// Typecheck (b.146 rule 15): decide's delivery is never fallen_back (decide refuses such a request with
// ErrRelayFallenBack), get's and list's rows carry permission_requests arrays, and get's singular
// permission_request is gone; tsc reports an unused directive if any of these loosens.
function _assertRelayDeliveryTypes(get: GetResult, row: ListRow, decided: DecideResult): void {
  // @ts-expect-error — decide never reports fallen_back
  const _bad: DecideResult["delivery"] = "fallen_back";
  const _reqs: PermissionRequestInfo[] = [...get.permission_requests, ...row.permission_requests];
  const _delivery: Delivery = decided.delivery;
  // @ts-expect-error — GetResult has permission_requests, not permission_request
  void get.permission_request;
  void _bad;
  void _reqs;
  void _delivery;
}
void _assertRelayDeliveryTypes;
