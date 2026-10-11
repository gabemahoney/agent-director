/**
 * types-exports.test.ts — the package root's runtime exports (SR-4), the
 * vendored-binary classes' removal (SR-4.6, b.ue3), and type-level pins checked
 * by `bun run typecheck`.
 */

import { test, expect } from "bun:test";
import * as ad from "../src/index.js";
import type { VersionResult, SpawnParams, MakeTemplateParams, GetResult, ListRow, DecideResult, PermissionRequestInfo, Delivery } from "../src/types.js";
import type { DialogMaybeOpenDetails, ProvenGoneHow } from "../src/index.js";
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

// Typecheck: VersionResult.config_dir is a required string (SR-14.4); tsc reports an unused
// directive if it becomes optional, and an error if it is dropped.
function _assertVersionResultConfigDir(v: VersionResult): void {
  const _dir: string = v.config_dir;
  // @ts-expect-error — config_dir is required
  const _bad: VersionResult = { version: "1.0.0", commit: "abc" };
  void _dir;
  void _bad;
}
void _assertVersionResultConfigDir;

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

// Typecheck (b.146 step 2c): get's rows carry unproven_requests, list's do not; every request carries its
// proof (proven_gone_at, proven_gone_how one of ProvenGoneHow, unproven_since); ErrDialogMaybeOpen's
// errDetails name the Spawn's state and its other unproven requests; tsc reports an unused directive if
// any of these loosens.
function _assertUnprovenTypes(get: GetResult, row: ListRow, info: PermissionRequestInfo, d: DialogMaybeOpenDetails): void {
  const _reqs: PermissionRequestInfo[] = [...get.unproven_requests, ...d.unproven_requests];
  // @ts-expect-error — list rows carry no unproven_requests (get only)
  void row.unproven_requests;
  const _how: ProvenGoneHow | null = info.proven_gone_how;
  // @ts-expect-error — proven_gone_how is one of tool_ran, turn_end, agent_gone
  const _bad: ProvenGoneHow = "delivered";
  const _times: (string | null)[] = [info.proven_gone_at, info.unproven_since, d.unproven_since];
  const _state: string = d.state;
  void _reqs;
  void _how;
  void _bad;
  void _times;
  void _state;
}
void _assertUnprovenTypes;

// Typecheck (SR-1.1, SR-2.1): agent is an optional string input on spawn and make-template and a required
// string on get's and list's rows; tsc reports an unused directive if any of these changes.
function _assertAgentTypes(get: GetResult, row: ListRow): void {
  const _spawn: SpawnParams = { cwd: "/ws", agent: "claude" };
  const _template: MakeTemplateParams = { name: "t", agent: "codex" };
  const _rows: string[] = [get.agent, row.agent];
  // @ts-expect-error — agent is a string, not a number
  const _bad: SpawnParams = { cwd: "/ws", agent: 1 };
  void _spawn;
  void _template;
  void _rows;
  void _bad;
}
void _assertAgentTypes;
