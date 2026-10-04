/**
 * operator-actions-absent.test.ts — b.vqr: the TS client has no delete verb
 * and never sends kill's finished-row opt-in; both live only on the off-PATH
 * agent-director-admin binary.
 */

import { test, expect } from "bun:test";
import { Client } from "../src/client.js";
import { VERBS } from "../src/internal/verbs.js";
import { buildArgv } from "../src/internal/argv.js";

const CLI = "/usr/local/bin/agent-director";

test("VERBS has no delete, and still has kill (b.vqr)", () => {
  expect(VERBS as readonly string[]).not.toContain("delete");
  expect(VERBS as readonly string[]).toContain("kill");
});

test("Client has no delete method, and still has kill (b.vqr)", () => {
  expect("delete" in Client.prototype).toBe(false);
  expect(typeof Client.prototype.kill).toBe("function");
});

test("kill argv never carries include_finished (b.vqr)", () => {
  const argv = buildArgv(CLI, "kill", { claude_instance_id: "id-kill", include_finished: true });
  expect(argv).toEqual([CLI, "kill", "--claude-instance-id", "id-kill"]);
});
