/**
 * argv-builder.test.ts — SR-1.2: buildArgv maps each verb's params to a
 * shell-free argv: [cli, ...global flags (b.32k), verb, ...long flags];
 * snake_case fields become kebab-case flags, booleans appear only when true
 * (busy send's as --flag=true or --flag=false whenever present, and send-keys'
 * no_enter as --no-enter=false too), and unset optionals are omitted. A spawn
 * or make-template extra_env key the
 * CLI could not carry unchanged is refused instead (b.vpb, b.66q).
 */

import { test, expect } from "bun:test";
import { buildArgv, GLOBAL_FLAGS } from "../src/internal/argv.js";
import { ErrInvalidFlags, ErrReservedEnvKey } from "../src/errors.js";
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
  ["spawn: agent cursor with model and Unicode prompt", "spawn", { cwd: "/ws", agent: "cursor", agent_args: ["--model", "auto", "hello\n世界"] },
    ["spawn", "--cwd", "/ws", "--agent", "cursor", "--", "--model", "auto", "hello\n世界"]],
  ["spawn: Cursor template selection, empty args remain absent", "spawn", { cwd: "/ws", template: "cursor", agent_args: [] },
    ["spawn", "--cwd", "/ws", "--template", "cursor"]],
  ["spawn: explicit Cursor with empty args omits --", "spawn", { cwd: "/ws", agent: "cursor", agent_args: [] },
    ["spawn", "--cwd", "/ws", "--agent", "cursor"]],
  ["spawn: Cursor mode and literal prompt retain exact ownership", "spawn", { cwd: "/ws", agent: "cursor", agent_args: ["--mode=plan", "-m", "account/模型", "--", "--resume\n多行 🦋"] },
    ["spawn", "--cwd", "/ws", "--agent", "cursor", "--", "--mode=plan", "-m", "account/模型", "--", "--resume\n多行 🦋"]],
  ["spawn: an empty agent counts as not given", "spawn", { cwd: "/ws", agent: "" }, ["spawn", "--cwd", "/ws"]],
  ["spawn: agent_args follow --, after every flag", "spawn", { cwd: "/ws", agent: "claude", agent_args: ["--model", "x"] },
    ["spawn", "--cwd", "/ws", "--agent", "claude", "--", "--model", "x"]],
  ["spawn: an empty agent_args list counts as none (no --)", "spawn", { cwd: "/ws", agent: "claude", agent_args: [] },
    ["spawn", "--cwd", "/ws", "--agent", "claude"]],
  ["spawn: false booleans omitted (SR-10.1)", "spawn", { cwd: "/ws", no_pre_trust: false, reuse_finished: false },
    ["spawn", "--cwd", "/ws"]],
  // SR-1.4, SR-4.5: a present Agent Default goes through, false and wait_turn included, before agent_args' --.
  ["spawn: reuse with Agent Defaults false and wait_turn", "spawn", {
    cwd: "/ws", instance_id: "id-1", reuse_finished: true, send_mode: "wait_turn", confirmed_delivery: false,
    confirm_from_transcripts: false, agent_args: ["--x"],
  }, ["spawn", "--cwd", "/ws", ...ID, "--reuse-finished", "--send-mode", "wait_turn", "--confirmed-delivery=false",
    "--confirm-from-transcripts=false", "--", "--x"]],
  ["spawn: Agent Defaults true and steer_turn", "spawn", { cwd: "/ws", send_mode: "steer_turn", confirmed_delivery: true, confirm_from_transcripts: true },
    ["spawn", "--cwd", "/ws", "--send-mode", "steer_turn", "--confirmed-delivery=true", "--confirm-from-transcripts=true"]],
  ["spawn: undefined Agent Defaults omitted", "spawn", { cwd: "/ws", send_mode: undefined, confirmed_delivery: undefined, confirm_from_transcripts: undefined },
    ["spawn", "--cwd", "/ws"]],
  // b.ob8: a 0 or null idle_screen override is none and omitted; any other
  // value goes through whole, the range's top included, for the CLI to refuse or keep.
  ["spawn: idle_screen override 0 omitted", "spawn", { cwd: "/ws", idle_screen_timeout_seconds: 0 }, ["spawn", "--cwd", "/ws"]],
  ["spawn: idle_screen override null omitted", "spawn", { cwd: "/ws", idle_screen_timeout_seconds: null }, ["spawn", "--cwd", "/ws"]],
  ["spawn: idle_screen override, top of the range", "spawn", { cwd: "/ws", idle_screen_timeout_seconds: 9223372036 },
    ["spawn", "--cwd", "/ws", "--idle-screen-timeout-seconds", "9223372036"]],
  ["spawn: fractional idle_screen override passed through", "spawn", { cwd: "/ws", idle_screen_timeout_seconds: 1.5 },
    ["spawn", "--cwd", "/ws", "--idle-screen-timeout-seconds", "1.5"]],
  ["spawn: negative idle_screen override passed through", "spawn", { cwd: "/ws", idle_screen_timeout_seconds: -1 },
    ["spawn", "--cwd", "/ws", "--idle-screen-timeout-seconds", "-1"]],
  // b.vpb: only an empty key or one holding "=" or NUL is refused; a value may hold "=".
  ["spawn: odd but valid extra_env names pass unchanged", "spawn", { cwd: "/ws", extra_env: { "my-var": "a=b", "1ST": "" } },
    ["spawn", "--cwd", "/ws", "--extra-env", "my-var=a=b", "--extra-env", "1ST="]],
  ["status", "status", { instance_id: "id-1" }, ["status", ...ID]],
  ["get", "get", { instance_id: "id-1" }, ["get", ...ID]],
  ["get-many", "get-many", { instance_ids: ["a", "b,c", "-h", "--", "--x"] }, ["get-many", "a", "b,c", "-h", "--", "--x"]],
  ["list-subagents", "list-subagents", { instance_id: "id-1" }, ["list-subagents", ...ID]],
  ["kill", "kill", { instance_id: "id-1" }, ["kill", ...ID]],
  ["resume", "resume", { instance_id: "id-1" }, ["resume", ...ID]],
  ["resume: Agent Defaults", "resume", { instance_id: "id-1", send_mode: "wait_turn", confirmed_delivery: false, confirm_from_transcripts: true },
    ["resume", ...ID, "--send-mode", "wait_turn", "--confirmed-delivery=false", "--confirm-from-transcripts=true"]],
  ["pause", "pause", { instance_id: "id-1" }, ["pause", ...ID]],
  ["pause: only_if_waiting", "pause", { instance_id: "id-1", only_if_waiting: true }, ["pause", ...ID, "--only-if-waiting"]],
  ["pause: only_if_waiting false omitted", "pause", { instance_id: "id-1", only_if_waiting: false }, ["pause", ...ID]],
  // Identifiers go through byte for byte; their shape is Go's to judge.
  ["force-delivery", "force-delivery", { instance_id: "--x 多行\n🦋" }, ["force-delivery", "--instance-id", "--x 多行\n🦋"]],
  ["get-force", "get-force", { force_id: "ABCDEF01-2345-6789-ABCD-EF0123456789" },
    ["get-force", "--force-id", "ABCDEF01-2345-6789-ABCD-EF0123456789"]],
  ["get-delivery", "get-delivery", { caller_uuid: "{not-a-uuid}", delivery_request_id: "-h 多行\n🦋" },
    ["get-delivery", "--caller-uuid", "{not-a-uuid}", "--delivery-request-id", "-h 多行\n🦋"]],
  ["send-keys", "send-keys", { instance_id: "id-1", text: "hello world", allow_pending: false },
    ["send-keys", ...ID, "--text", "hello world"]],
  // b.9o4: empty text is the Enter-only send, also on a pending row.
  ["send-keys: empty text, allow_pending", "send-keys", { instance_id: "id-1", text: "", allow_pending: true },
    ["send-keys", ...ID, "--text", "", "--allow-pending"]],
  // b.146 rule 8: plain no_enter, key and hash; text omitted sends no --text.
  ["send-keys: no_enter, hash and n_lines", "send-keys", {
    instance_id: "id-1", text: "hi", no_enter: true, expect_pane_sha256: "ab", n_lines: 7,
  }, ["send-keys", ...ID, "--text", "hi", "--no-enter", "--expect-pane-sha256", "ab", "--n-lines", "7"]],
  // SR-1.5: a present raw-key field goes through at its zero value too, since a Covered Agent's send refuses its presence.
  ["send-keys: a key, no text", "send-keys", { instance_id: "id-1", key: "Escape", no_enter: false },
    ["send-keys", ...ID, "--no-enter=false", "--key", "Escape"]],
  ["send-keys: n_lines 0 passed through", "send-keys", { instance_id: "id-1", text: "hi", n_lines: 0 },
    ["send-keys", ...ID, "--text", "hi", "--n-lines", "0"]],
  // SR-1.3: a confirmed send's caller-chosen Message ID goes through byte for byte.
  ["send-keys: confirmed send with its delivery_request_id", "send-keys", {
    instance_id: "id-1", text: "hi", confirmed_delivery: true, caller_uuid: "ABCDEF01-2345-6789-ABCD-EF0123456789", delivery_request_id: "-h 多行\n🦋",
  }, ["send-keys", ...ID, "--text", "hi", "--delivery-request-id", "-h 多行\n🦋", "--confirmed-delivery=true",
    "--caller-uuid", "ABCDEF01-2345-6789-ABCD-EF0123456789"]],
  // SR-1.3, SR-1.4: a present busy-send option goes through, false and wait_turn included; undefined is omitted.
  ["send-keys: busy-send options false and wait_turn", "send-keys", {
    instance_id: "id-1", text: "hi", send_mode: "wait_turn", confirmed_delivery: false, caller_uuid: "ABCDEF01-2345-6789-ABCD-EF0123456789", force: false,
  }, ["send-keys", ...ID, "--text", "hi", "--send-mode", "wait_turn", "--confirmed-delivery=false",
    "--caller-uuid", "ABCDEF01-2345-6789-ABCD-EF0123456789", "--force=false"]],
  ["send-keys: busy-send options true and steer_turn", "send-keys", { instance_id: "id-1", text: "", send_mode: "steer_turn", confirmed_delivery: true, force: true },
    ["send-keys", ...ID, "--text", "", "--send-mode", "steer_turn", "--confirmed-delivery=true", "--force=true"]],
  ["send-keys: undefined busy-send options omitted", "send-keys", {
    instance_id: "id-1", text: "hi", send_mode: undefined, confirmed_delivery: undefined, caller_uuid: undefined, force: undefined,
  }, ["send-keys", ...ID, "--text", "hi"]],
  ["send-keys: a pane answer", "send-keys", {
    instance_id: "id-1", request_token: "tok", as: "deny", key: "2", expect_pane_sha256: "ab",
  }, ["send-keys", ...ID, "--key", "2", "--expect-pane-sha256", "ab", "--request-token", "tok", "--as", "deny"]],
  ["record-pane-answer", "record-pane-answer", { request_token: "tok", as: "unknown", expect_pane_sha256: "ab" },
    ["record-pane-answer", "--request-token", "tok", "--as", "unknown", "--expect-pane-sha256", "ab"]],
  ["record-pane-answer: n_lines", "record-pane-answer", { request_token: "tok", as: "allow", expect_pane_sha256: "ab", n_lines: 30 },
    ["record-pane-answer", "--request-token", "tok", "--as", "allow", "--expect-pane-sha256", "ab", "--n-lines", "30"]],
  ["send-keys: negative n_lines passed through", "send-keys", { instance_id: "id-1", text: "hi", n_lines: -1 },
    ["send-keys", ...ID, "--text", "hi", "--n-lines", "-1"]],
  ["record-pane-answer: negative n_lines passed through", "record-pane-answer", {
    request_token: "tok", as: "allow", expect_pane_sha256: "ab", n_lines: -1,
  }, ["record-pane-answer", "--request-token", "tok", "--as", "allow", "--expect-pane-sha256", "ab", "--n-lines", "-1"]],
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
  ["sweep", "sweep", {}, ["sweep"]],
  ["sweep ignores legacy timeout", "sweep", { timeout_ms: 1 }, ["sweep"]],
  ["find-missing", "find-missing", {}, ["find-missing"]],
  ["find-missing ignores legacy timeout", "find-missing", { timeout_ms: 1 }, ["find-missing"]],
  ["expire", "expire", { older_than: "7d" }, ["expire", "--older-than", "7d"]],
  ["expire: no params", "expire", {}, ["expire"]],
  ["make-template: name only", "make-template", { name: "tpl" }, ["make-template", "--name", "tpl"]],
  ["make-template: every option", "make-template", {
    name: "tpl", cwd: "/p", agent: "claude", relay_mode: "on", overwrite: true, label: ["a=b"], extra_env: { K: "V" },
    allow: ["Read"], deny: ["Bash"], ask: ["Edit"], agent_args: ["--x", "y"],
  }, ["make-template", "--name", "tpl", "--cwd", "/p", "--agent", "claude", "--relay-mode", "on", "--overwrite", "--label", "a=b",
    "--extra-env", "K=V", "--allow", "Read", "--deny", "Bash", "--ask", "Edit", "--agent-args", "--x", "--agent-args", "y"]],
  ["make-template: Agent Defaults, no agent", "make-template", {
    name: "tpl", send_mode: "wait_turn", confirmed_delivery: false, confirm_from_transcripts: false, label: ["a=b"],
  }, ["make-template", "--name", "tpl", "--send-mode", "wait_turn", "--confirmed-delivery=false", "--confirm-from-transcripts=false",
    "--label", "a=b"]],
  ["make-template: agent with agent_args", "make-template", { name: "tpl", agent: "codex", agent_args: ["--x"] },
    ["make-template", "--name", "tpl", "--agent", "codex", "--agent-args", "--x"]],
  ["make-template: Cursor with model", "make-template", { name: "cursor", agent: "cursor", agent_args: ["--model", "auto"] },
    ["make-template", "--name", "cursor", "--agent", "cursor", "--agent-args", "--model", "--agent-args", "auto"]],
  ["make-template: Cursor with empty args omits --agent-args", "make-template", { name: "cursor", agent: "cursor", agent_args: [] },
    ["make-template", "--name", "cursor", "--agent", "cursor"]],
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

test("get-many: globals precede unchanged id words and near misses", () => {
  const ids = ["a", "b,c", "-h", "--", "--x", "-", ...GLOBAL_FLAGS.map((flag) => `${flag}suffix`)];
  expect(buildArgv(CLI, "get-many", { instance_ids: ids }, { home: "/temp/home", storePath: "/temp/state.db" }))
    .toEqual([CLI, "--store-path", "/temp/state.db", "--home", "/temp/home", "get-many", ...ids]);
});

test.each([...GLOBAL_FLAGS.flatMap((flag) => [flag, `${flag}=private-value`]), "private\0id"])(
  "get-many: unsafe id %p is refused by position without its text", (id) => {
    const err = thrownBy(() => buildArgv(CLI, "get-many", { instance_ids: ["safe", id] }));
    expect(err).toBeInstanceOf(ErrInvalidFlags);
    const e = err as ErrInvalidFlags;
    expect([e.verb, e.errName]).toEqual(["get-many", "ErrInvalidFlags"]);
    expect(e.errDescription).toBe(id.includes("\0")
      ? "get-many: id at position 2 contains a NUL byte, which a CLI argument cannot carry"
      : "get-many: id at position 2 would be consumed as a CLI global flag");
    expect(e.errDescription).not.toContain(id);
  });
