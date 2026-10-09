/**
 * argv-builder.test.ts — SR-1.2: buildArgv maps each verb's params to a
 * shell-free argv: [cli, ...global flags (b.32k), verb, ...long flags];
 * snake_case fields become kebab-case flags, booleans appear only when true,
 * and unset optionals are omitted.
 */

import { test, expect } from "bun:test";
import { buildArgv } from "../src/internal/argv.js";

const CLI = "/usr/local/bin/agent-director";
const ID = ["--claude-instance-id", "id-1"];

test.each([
  ["version", "version", {}, ["version"]],
  ["list: no params", "list", {}, ["list"]],
  ["list: every filter", "list", {
    state: ["waiting", "working"], label: ["a=b"], parent: "p", cwd: "/c", tmux_session_name: "s", limit: 10,
  }, ["list", "--state", "waiting,working", "--label", "a=b", "--parent", "p", "--cwd", "/c", "--tmux-session-name", "s", "--limit", "10"]],
  ["spawn: cwd only", "spawn", { cwd: "/ws" }, ["spawn", "--cwd", "/ws"]],
  ["spawn: every option", "spawn", {
    cwd: "/ws", template: "default", claude_instance_id: "id-1", tmux_session_name: "s", relay_mode: "on",
    no_pre_trust: true, reuse_finished: true, label: ["env=prod", "team=be"], extra_env: { FOO: "bar", CLAUDE_CONFIG_DIR: "/cfg" },
    allow: ["Read"], deny: ["Bash"], ask: ["Edit"], claude_args: ["--model", "x"],
  }, ["spawn", "--cwd", "/ws", "--template", "default", ...ID, "--tmux-session-name", "s", "--relay-mode", "on",
    "--no-pre-trust", "--reuse-finished", "--label", "env=prod", "--label", "team=be",
    "--extra-env", "FOO=bar", "--extra-env", "CLAUDE_CONFIG_DIR=/cfg",
    "--allow", "Read", "--deny", "Bash", "--ask", "Edit", "--", "--model", "x"]],
  ["spawn: false booleans omitted (SR-10.1)", "spawn", { cwd: "/ws", no_pre_trust: false, reuse_finished: false },
    ["spawn", "--cwd", "/ws"]],
  ["status", "status", { claude_instance_id: "id-1" }, ["status", ...ID]],
  ["get", "get", { claude_instance_id: "id-1" }, ["get", ...ID]],
  ["kill", "kill", { claude_instance_id: "id-1" }, ["kill", ...ID]],
  ["resume", "resume", { claude_instance_id: "id-1" }, ["resume", ...ID]],
  ["pause", "pause", { claude_instance_id: "id-1" }, ["pause", ...ID]],
  ["send-keys", "send-keys", { claude_instance_id: "id-1", text: "hello world", allow_pending: false },
    ["send-keys", ...ID, "--text", "hello world"]],
  // b.9o4: empty text is the Enter-only send, also on a pending row.
  ["send-keys: empty text, allow_pending", "send-keys", { claude_instance_id: "id-1", text: "", allow_pending: true },
    ["send-keys", ...ID, "--text", "", "--allow-pending"]],
  ["read-pane: all options", "read-pane", { claude_instance_id: "id-1", n_lines: 50, ansi: true, allow_pending: true },
    ["read-pane", ...ID, "--n-lines", "50", "--ansi", "--allow-pending"]],
  ["read-pane: false booleans omitted", "read-pane", { claude_instance_id: "id-1", ansi: false, allow_pending: false },
    ["read-pane", ...ID]],
  ["decide: token and reason", "decide", { claude_instance_id: "id-1", decision: "allow", request_token: "tok", reason: "safe" },
    ["decide", ...ID, "--decision", "allow", "--request-token", "tok", "--reason", "safe"]],
  ["decide: no token or reason", "decide", { claude_instance_id: "id-1", decision: "deny" },
    ["decide", ...ID, "--decision", "deny"]],
  ["decide: max_wait_ms (b.146 decision 9 B), 0 included", "decide", { claude_instance_id: "id-1", decision: "allow", request_token: "tok", max_wait_ms: 0 },
    ["decide", ...ID, "--decision", "allow", "--request-token", "tok", "--max-wait-ms", "0"]],
  ["get-permission", "get-permission", { request_token: "tok" }, ["get-permission", "--request-token", "tok"]],
  ["find-missing", "find-missing", {}, ["find-missing"]],
  ["expire", "expire", { older_than: "7d" }, ["expire", "--older-than", "7d"]],
  ["expire: no params", "expire", {}, ["expire"]],
  ["make-template: name only", "make-template", { name: "tpl" }, ["make-template", "--name", "tpl"]],
  ["make-template: every option", "make-template", {
    name: "tpl", cwd: "/p", relay_mode: "on", overwrite: true, label: ["a=b"], extra_env: { K: "V" },
    allow: ["Read"], deny: ["Bash"], ask: ["Edit"], claude_args: ["--x", "y"],
  }, ["make-template", "--name", "tpl", "--cwd", "/p", "--relay-mode", "on", "--overwrite", "--label", "a=b",
    "--extra-env", "K=V", "--allow", "Read", "--deny", "Bash", "--ask", "Edit", "--claude-args", "--x", "--claude-args", "y"]],
  // reuse_finished is spawn-only; the CLI rejects it on make-template.
  ["make-template: untyped reuse_finished dropped", "make-template", { name: "tpl", reuse_finished: true },
    ["make-template", "--name", "tpl"]],
] as const)("%s", (_label, verb, params, want) => {
  expect(buildArgv(CLI, verb, params)).toEqual([CLI, ...want]);
});

// b.32k: global flags precede the verb, in a stable order; an empty object adds none.
test.each([
  [undefined, []],
  [{}, []],
  [{ storePath: "/tmp/foo.db" }, ["--store-path", "/tmp/foo.db"]],
  [{ home: "/tmp/h" }, ["--home", "/tmp/h"]],
  [{ tmuxCommand: "/usr/bin/tmux" }, ["--tmux-command", "/usr/bin/tmux"]],
  [{ tmuxCommand: "/usr/bin/tmux", home: "/tmp/h", storePath: "/tmp/foo.db" },
    ["--store-path", "/tmp/foo.db", "--home", "/tmp/h", "--tmux-command", "/usr/bin/tmux"]],
])("global options %p → %p before the verb (b.32k)", (globals, flags) => {
  expect(buildArgv(CLI, "status", { claude_instance_id: "id-1" }, globals)).toEqual([CLI, ...flags, "status", ...ID]);
});
