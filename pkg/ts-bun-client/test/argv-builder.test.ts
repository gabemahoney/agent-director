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
const ID = ["--instance-id", "id-1"];

test.each([
  ["version", "version", {}, ["version"]],
  ["list: no params", "list", {}, ["list"]],
  ["list: every filter", "list", {
    state: ["waiting", "working"], label: ["a=b"], parent: "p", cwd: "/c", tmux_session_name: "s", limit: 10,
  }, ["list", "--state", "waiting,working", "--label", "a=b", "--parent", "p", "--cwd", "/c", "--tmux-session-name", "s", "--limit", "10"]],
  // b.c4n: 0 is the CLI's default (no cap) and is omitted; any other value,
  // a negative one too, goes through for the CLI to refuse.
  ["list: limit 0 omitted", "list", { limit: 0 }, ["list"]],
  ["list: negative limit passed through", "list", { limit: -1 }, ["list", "--limit", "-1"]],
  ["spawn: cwd only", "spawn", { cwd: "/ws" }, ["spawn", "--cwd", "/ws"]],
  ["spawn: every option", "spawn", {
    cwd: "/ws", template: "default", agent: "claude", instance_id: "id-1", tmux_session_name: "s", relay_mode: "on",
    no_pre_trust: true, reuse_finished: true, label: ["env=prod", "team=be"], extra_env: { FOO: "bar", CLAUDE_CONFIG_DIR: "/cfg" },
    allow: ["Read"], deny: ["Bash"], ask: ["Edit"], agent_args: ["--model", "x"],
  }, ["spawn", "--cwd", "/ws", "--template", "default", "--agent", "claude", ...ID, "--tmux-session-name", "s", "--relay-mode", "on",
    "--no-pre-trust", "--reuse-finished", "--label", "env=prod", "--label", "team=be",
    "--extra-env", "FOO=bar", "--extra-env", "CLAUDE_CONFIG_DIR=/cfg",
    "--allow", "Read", "--deny", "Bash", "--ask", "Edit", "--", "--model", "x"]],
  ["spawn: agent codex", "spawn", { cwd: "/ws", agent: "codex" }, ["spawn", "--cwd", "/ws", "--agent", "codex"]],
  ["spawn: an empty agent counts as not given", "spawn", { cwd: "/ws", agent: "" }, ["spawn", "--cwd", "/ws"]],
  ["spawn: agent_args follow --, after every flag", "spawn", { cwd: "/ws", agent: "claude", agent_args: ["--model", "x"] },
    ["spawn", "--cwd", "/ws", "--agent", "claude", "--", "--model", "x"]],
  ["spawn: an empty agent_args list counts as none (no --)", "spawn", { cwd: "/ws", agent: "claude", agent_args: [] },
    ["spawn", "--cwd", "/ws", "--agent", "claude"]],
  ["spawn: false booleans omitted (SR-10.1)", "spawn", { cwd: "/ws", no_pre_trust: false, reuse_finished: false },
    ["spawn", "--cwd", "/ws"]],
  // b.vpb: only an empty key or one holding "=" or NUL is refused; a value may hold "=".
  ["spawn: odd but valid extra_env names pass unchanged", "spawn", { cwd: "/ws", extra_env: { "my-var": "a=b", "1ST": "" } },
    ["spawn", "--cwd", "/ws", "--extra-env", "my-var=a=b", "--extra-env", "1ST="]],
  ["status", "status", { instance_id: "id-1" }, ["status", ...ID]],
  ["get", "get", { instance_id: "id-1" }, ["get", ...ID]],
  ["list-subagents", "list-subagents", { instance_id: "id-1" }, ["list-subagents", ...ID]],
  ["kill", "kill", { instance_id: "id-1" }, ["kill", ...ID]],
  ["resume", "resume", { instance_id: "id-1" }, ["resume", ...ID]],
  ["pause", "pause", { instance_id: "id-1" }, ["pause", ...ID]],
  ["send-keys", "send-keys", { instance_id: "id-1", text: "hello world", allow_pending: false },
    ["send-keys", ...ID, "--text", "hello world"]],
  // b.9o4: empty text is the Enter-only send, also on a pending row.
  ["send-keys: empty text, allow_pending", "send-keys", { instance_id: "id-1", text: "", allow_pending: true },
    ["send-keys", ...ID, "--text", "", "--allow-pending"]],
  // b.146 rule 8: plain no_enter, key and hash; text omitted sends no --text.
  ["send-keys: no_enter, hash and n_lines", "send-keys", {
    instance_id: "id-1", text: "hi", no_enter: true, expect_pane_sha256: "ab", n_lines: 7,
  }, ["send-keys", ...ID, "--text", "hi", "--no-enter", "--expect-pane-sha256", "ab", "--n-lines", "7"]],
  ["send-keys: a key, no text", "send-keys", { instance_id: "id-1", key: "Escape", no_enter: false },
    ["send-keys", ...ID, "--key", "Escape"]],
  ["send-keys: a pane answer", "send-keys", {
    instance_id: "id-1", request_token: "tok", as: "deny", key: "2", expect_pane_sha256: "ab",
  }, ["send-keys", ...ID, "--key", "2", "--expect-pane-sha256", "ab", "--request-token", "tok", "--as", "deny"]],
  ["record-pane-answer", "record-pane-answer", { request_token: "tok", as: "unknown", expect_pane_sha256: "ab" },
    ["record-pane-answer", "--request-token", "tok", "--as", "unknown", "--expect-pane-sha256", "ab"]],
  ["record-pane-answer: n_lines", "record-pane-answer", { request_token: "tok", as: "allow", expect_pane_sha256: "ab", n_lines: 30 },
    ["record-pane-answer", "--request-token", "tok", "--as", "allow", "--expect-pane-sha256", "ab", "--n-lines", "30"]],
  ["read-pane: all options", "read-pane", { instance_id: "id-1", n_lines: 50, ansi: true, allow_pending: true },
    ["read-pane", ...ID, "--n-lines", "50", "--ansi", "--allow-pending"]],
  ["read-pane: false booleans omitted", "read-pane", { instance_id: "id-1", ansi: false, allow_pending: false },
    ["read-pane", ...ID]],
  // b.c4n: 0 is the CLI's default of 25 and is omitted; a negative goes through for the CLI to refuse.
  ["read-pane: n_lines 0 omitted", "read-pane", { instance_id: "id-1", n_lines: 0 }, ["read-pane", ...ID]],
  ["read-pane: negative n_lines passed through", "read-pane", { instance_id: "id-1", n_lines: -3 },
    ["read-pane", ...ID, "--n-lines", "-3"]],
  ["decide: token and reason", "decide", { instance_id: "id-1", decision: "allow", request_token: "tok", reason: "safe" },
    ["decide", ...ID, "--decision", "allow", "--request-token", "tok", "--reason", "safe"]],
  ["decide: no token or reason", "decide", { instance_id: "id-1", decision: "deny" },
    ["decide", ...ID, "--decision", "deny"]],
  ["decide: max_wait_ms (b.146 decision 9 B), 0 included", "decide", { instance_id: "id-1", decision: "allow", request_token: "tok", max_wait_ms: 0 },
    ["decide", ...ID, "--decision", "allow", "--request-token", "tok", "--max-wait-ms", "0"]],
  ["get-permission", "get-permission", { request_token: "tok" }, ["get-permission", "--request-token", "tok"]],
  ["find-missing", "find-missing", {}, ["find-missing"]],
  ["expire", "expire", { older_than: "7d" }, ["expire", "--older-than", "7d"]],
  ["expire: no params", "expire", {}, ["expire"]],
  ["make-template: name only", "make-template", { name: "tpl" }, ["make-template", "--name", "tpl"]],
  ["make-template: every option", "make-template", {
    name: "tpl", cwd: "/p", agent: "claude", relay_mode: "on", overwrite: true, label: ["a=b"], extra_env: { K: "V" },
    allow: ["Read"], deny: ["Bash"], ask: ["Edit"], agent_args: ["--x", "y"],
  }, ["make-template", "--name", "tpl", "--cwd", "/p", "--agent", "claude", "--relay-mode", "on", "--overwrite", "--label", "a=b",
    "--extra-env", "K=V", "--allow", "Read", "--deny", "Bash", "--ask", "Edit", "--agent-args", "--x", "--agent-args", "y"]],
  ["make-template: agent with agent_args", "make-template", { name: "tpl", agent: "codex", agent_args: ["--x"] },
    ["make-template", "--name", "tpl", "--agent", "codex", "--agent-args", "--x"]],
  ["make-template: an empty agent counts as not given", "make-template", { name: "tpl", agent: "" },
    ["make-template", "--name", "tpl"]],
  ["make-template: an empty agent_args list counts as none", "make-template", { name: "tpl", agent_args: [] },
    ["make-template", "--name", "tpl"]],
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
  expect(buildArgv(CLI, "status", { instance_id: "id-1" }, globals)).toEqual([CLI, ...flags, "status", ...ID]);
});
