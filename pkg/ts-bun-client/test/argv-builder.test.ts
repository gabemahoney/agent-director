/**
 * argv-builder.test.ts — SR-1.2: buildArgv maps each verb's params to a
 * shell-free argv: [cli, ...global flags (b.32k), verb, ...long flags];
 * snake_case fields become kebab-case flags, booleans appear only when true,
 * and unset optionals are omitted. A spawn or make-template extra_env key the
 * CLI could not carry unchanged is refused instead (b.vpb, b.66q).
 */

import { test, expect } from "bun:test";
import { buildArgv } from "../src/internal/argv.js";
import { ErrReservedEnvKey } from "../src/errors.js";
import { thrownBy } from "./internal/helper.js";

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
  // b.vpb: only an empty key or one holding "=" or NUL is refused; a value may hold "=".
  ["spawn: odd but valid extra_env names pass unchanged", "spawn", { cwd: "/ws", extra_env: { "my-var": "a=b", "1ST": "" } },
    ["spawn", "--cwd", "/ws", "--extra-env", "my-var=a=b", "--extra-env", "1ST="]],
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

// b.vpb, b.66q: the CLI splits each --extra-env K=V at its first "=", so spawn and
// make-template refuse a malformed key before any argv exists, with ErrReservedEnvKey;
// the smallest such key is named. HOME=/x gets the malformed-key description here,
// where Go gives the same name with the HOME description (docs/architecture.md).
const REQUIRED = { spawn: { cwd: "/ws" }, "make-template": { name: "tpl" } } as const;
test.each([
  ["spawn", "empty", { "": "x", TEAM: "core" }, "", "it is empty"],
  ["spawn", "CLAUDE_CONFIG_DIR=/tmp/cfg", { "CLAUDE_CONFIG_DIR=/tmp/cfg": "" }, "CLAUDE_CONFIG_DIR=/tmp/cfg", "it contains '='"],
  ["spawn", "HOME=/x, the malformed-key description", { "HOME=/x": "" }, "HOME=/x", "it contains '='"],
  ["spawn", "NUL", { "A\0B": "", TEAM: "core" }, "A\0B", "it contains a NUL byte"],
  ["spawn", "the smallest of several", { "Z=1": "", "B\0": "", "A=B": "" }, "A=B", "it contains '='"],
  ["make-template", "CLAUDE_CONFIG_DIR=/tmp/cfg", { "CLAUDE_CONFIG_DIR=/tmp/cfg": "" }, "CLAUDE_CONFIG_DIR=/tmp/cfg", "it contains '='"],
] as const)("%s: extra_env key %s → ErrReservedEnvKey naming it and what is wrong", (verb, _label, extra_env, key, problem) => {
  const err = thrownBy(() => buildArgv(CLI, verb, { ...REQUIRED[verb], extra_env }));
  expect(err).toBeInstanceOf(ErrReservedEnvKey);
  const e = err as ErrReservedEnvKey;
  expect([e.verb, e.errName]).toEqual([verb, "ErrReservedEnvKey"]);
  const q = JSON.stringify(key);
  expect(e.errDescription.startsWith(`extra_env key ${q} is not a valid env-var name: ${problem}`)).toBe(true);
  expect(e.errDescription).toContain(`; remove ${q} from extra_env, and `);
});

// b.32k: global flags precede the verb, in a stable order; an empty object adds none.
// b.78b: createIfMissing false adds --create-if-missing false; true and undefined add nothing.
test.each([
  [undefined, []],
  [{}, []],
  [{ storePath: "/tmp/foo.db" }, ["--store-path", "/tmp/foo.db"]],
  [{ home: "/tmp/h" }, ["--home", "/tmp/h"]],
  [{ tmuxCommand: "/usr/bin/tmux" }, ["--tmux-command", "/usr/bin/tmux"]],
  [{ tmuxCommand: "/usr/bin/tmux", home: "/tmp/h", storePath: "/tmp/foo.db" },
    ["--store-path", "/tmp/foo.db", "--home", "/tmp/h", "--tmux-command", "/usr/bin/tmux"]],
  [{ createIfMissing: false }, ["--create-if-missing", "false"]],
  [{ createIfMissing: true }, []],
  [{ createIfMissing: undefined }, []],
  [{ createIfMissing: false, storePath: "/tmp/foo.db" }, ["--store-path", "/tmp/foo.db", "--create-if-missing", "false"]],
])("global options %p → %p before the verb (b.32k, b.78b)", (globals, flags) => {
  expect(buildArgv(CLI, "status", { claude_instance_id: "id-1" }, globals)).toEqual([CLI, ...flags, "status", ...ID]);
});
