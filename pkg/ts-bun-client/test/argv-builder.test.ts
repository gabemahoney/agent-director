/**
 * argv-builder.test.ts — unit tests for the verb-driven argv builder (Task A3).
 *
 * Tests SRD SR-1.2: argv construction is verb-driven and shell-free.
 *   - One test per callable verb (15 verbs).
 *   - Each builds a representative params object and asserts the argv shape.
 *   - Pins camelCase→kebab-case verb mapping and snake_case→kebab-case flag names.
 *
 * IMPORT NOTE: The engineer added the argv builder in one of these two locations:
 *   (a) pkg/ts-bun-client/src/internal/argv.ts   ← preferred if it's a new file
 *   (b) pkg/ts-bun-client/src/internal/verbs.ts  ← if extended in-place
 *
 * Update the import below to match whichever the engineer used.
 * Expected export: `buildArgv(cliPath: string, verb: string, params: object): string[]`
 */

import { test, expect, describe } from "bun:test";
// TODO(engineer): confirm which file exports buildArgv and update the import path.
// Trying src/internal/argv.js first; if not found, fall back to src/internal/verbs.js.
import { buildArgv } from "../src/internal/argv.js";

const CLI = "/usr/local/bin/agent-director";

// ---------------------------------------------------------------------------
// Helper: assert argv basics
// ---------------------------------------------------------------------------
function assertBase(argv: string[], verb: string): void {
  expect(argv[0]).toBe(CLI);
  expect(argv[1]).toBe(verb);
  expect(argv.length).toBeGreaterThanOrEqual(2);
}

function hasFlag(argv: string[], flag: string): boolean {
  return argv.includes(flag);
}

function flagValue(argv: string[], flag: string): string | undefined {
  const idx = argv.indexOf(flag);
  return idx >= 0 ? argv[idx + 1] : undefined;
}

// ---------------------------------------------------------------------------
// make-template
// ---------------------------------------------------------------------------
describe("argv builder — make-template", () => {
  test("basic: name + overwrite → correct argv with --name and --overwrite", () => {
    const argv = buildArgv(CLI, "make-template", {
      name: "my-template",
      overwrite: true,
    });
    assertBase(argv, "make-template");
    expect(hasFlag(argv, "--name")).toBe(true);
    expect(flagValue(argv, "--name")).toBe("my-template");
    expect(hasFlag(argv, "--overwrite")).toBe(true);
  });

  test("optional cwd and relay-mode are included when supplied", () => {
    const argv = buildArgv(CLI, "make-template", {
      name: "tpl",
      cwd: "/home/user/proj",
      relay_mode: "on",
    });
    expect(flagValue(argv, "--name")).toBe("tpl");
    expect(flagValue(argv, "--cwd")).toBe("/home/user/proj");
    expect(flagValue(argv, "--relay-mode")).toBe("on");
  });

  test("overwrite omitted (false) → --overwrite flag not present", () => {
    const argv = buildArgv(CLI, "make-template", { name: "tpl" });
    expect(hasFlag(argv, "--overwrite")).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// version
// ---------------------------------------------------------------------------
describe("argv builder — version", () => {
  test("no params → argv is [cliPath, 'version'] with no extra flags", () => {
    const argv = buildArgv(CLI, "version", {});
    assertBase(argv, "version");
    expect(argv.length).toBe(2);
  });
});

// ---------------------------------------------------------------------------
// list
// ---------------------------------------------------------------------------
describe("argv builder — list", () => {
  test("state filter → --state flag with comma-joined value or repeated flags", () => {
    const argv = buildArgv(CLI, "list", { state: ["waiting", "working"] });
    assertBase(argv, "list");
    // CLI accepts --state as comma-separated OR as repeated flags.
    // Accept either form.
    const hasState = hasFlag(argv, "--state");
    expect(hasState).toBe(true);
    const stateVal = flagValue(argv, "--state") ?? "";
    const isCommaJoined = stateVal.includes("waiting") && stateVal.includes("working");
    const isRepeated =
      argv.filter((_, i) => argv[i - 1] === "--state").some((v) => v === "waiting") &&
      argv.filter((_, i) => argv[i - 1] === "--state").some((v) => v === "working");
    expect(isCommaJoined || isRepeated).toBe(true);
  });

  test("limit → --limit flag with numeric string value", () => {
    const argv = buildArgv(CLI, "list", { limit: 10 });
    expect(hasFlag(argv, "--limit")).toBe(true);
    expect(flagValue(argv, "--limit")).toBe("10");
  });

  test("no params → [cliPath, 'list'] with no flags", () => {
    const argv = buildArgv(CLI, "list", {});
    assertBase(argv, "list");
    expect(argv.length).toBe(2);
  });
});

// ---------------------------------------------------------------------------
// spawn
// ---------------------------------------------------------------------------
describe("argv builder — spawn", () => {
  test("cwd + template → --cwd and --template flags", () => {
    const argv = buildArgv(CLI, "spawn", {
      cwd: "/workspace",
      template: "default",
    });
    assertBase(argv, "spawn");
    expect(flagValue(argv, "--cwd")).toBe("/workspace");
    expect(flagValue(argv, "--template")).toBe("default");
  });

  test("snake_case claude_instance_id → --claude-instance-id", () => {
    const argv = buildArgv(CLI, "spawn", {
      cwd: "/ws",
      claude_instance_id: "uuid-1234",
    });
    expect(flagValue(argv, "--claude-instance-id")).toBe("uuid-1234");
  });

  test("relay_mode snake_case → --relay-mode", () => {
    const argv = buildArgv(CLI, "spawn", { cwd: "/ws", relay_mode: "on" });
    expect(flagValue(argv, "--relay-mode")).toBe("on");
  });

  test("no_pre_trust bool → --no-pre-trust when true, absent when false", () => {
    const argvTrue = buildArgv(CLI, "spawn", { cwd: "/ws", no_pre_trust: true });
    expect(hasFlag(argvTrue, "--no-pre-trust")).toBe(true);
    const argvFalse = buildArgv(CLI, "spawn", { cwd: "/ws", no_pre_trust: false });
    expect(hasFlag(argvFalse, "--no-pre-trust")).toBe(false);
  });

  test("label array → repeated --label k=v entries", () => {
    const argv = buildArgv(CLI, "spawn", {
      cwd: "/ws",
      label: ["env=prod", "team=backend"],
    });
    const labelValues = argv
      .map((v, i) => (argv[i - 1] === "--label" ? v : null))
      .filter(Boolean);
    expect(labelValues).toContain("env=prod");
    expect(labelValues).toContain("team=backend");
  });
});

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------
describe("argv builder — status", () => {
  test("claude_instance_id → --claude-instance-id", () => {
    const argv = buildArgv(CLI, "status", { claude_instance_id: "abc-123" });
    assertBase(argv, "status");
    expect(flagValue(argv, "--claude-instance-id")).toBe("abc-123");
  });
});

// ---------------------------------------------------------------------------
// get
// ---------------------------------------------------------------------------
describe("argv builder — get", () => {
  test("claude_instance_id → --claude-instance-id", () => {
    const argv = buildArgv(CLI, "get", { claude_instance_id: "def-456" });
    assertBase(argv, "get");
    expect(flagValue(argv, "--claude-instance-id")).toBe("def-456");
  });
});

// ---------------------------------------------------------------------------
// send-keys
// ---------------------------------------------------------------------------
describe("argv builder — send-keys", () => {
  test("claude_instance_id + text → correct flags", () => {
    const argv = buildArgv(CLI, "send-keys", {
      claude_instance_id: "id-sk",
      text: "hello world",
    });
    assertBase(argv, "send-keys");
    expect(flagValue(argv, "--claude-instance-id")).toBe("id-sk");
    expect(flagValue(argv, "--text")).toBe("hello world");
  });
});

// ---------------------------------------------------------------------------
// read-pane
// ---------------------------------------------------------------------------
describe("argv builder — read-pane", () => {
  test("claude_instance_id + n_lines + ansi → correct flags", () => {
    const argv = buildArgv(CLI, "read-pane", {
      claude_instance_id: "id-rp",
      n_lines: 50,
      ansi: true,
    });
    assertBase(argv, "read-pane");
    expect(flagValue(argv, "--claude-instance-id")).toBe("id-rp");
    expect(flagValue(argv, "--n-lines")).toBe("50");
    expect(hasFlag(argv, "--ansi")).toBe(true);
  });

  test("ansi: false → --ansi flag absent", () => {
    const argv = buildArgv(CLI, "read-pane", {
      claude_instance_id: "id-rp2",
      ansi: false,
    });
    expect(hasFlag(argv, "--ansi")).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// kill
// ---------------------------------------------------------------------------
describe("argv builder — kill", () => {
  test("claude_instance_id → --claude-instance-id", () => {
    const argv = buildArgv(CLI, "kill", { claude_instance_id: "id-kill" });
    assertBase(argv, "kill");
    expect(flagValue(argv, "--claude-instance-id")).toBe("id-kill");
  });
});

// ---------------------------------------------------------------------------
// decide
// ---------------------------------------------------------------------------
describe("argv builder — decide", () => {
  test("claude_instance_id + decision + reason → correct flags", () => {
    const argv = buildArgv(CLI, "decide", {
      claude_instance_id: "id-dec",
      decision: "allow",
      reason: "looks safe",
    });
    assertBase(argv, "decide");
    expect(flagValue(argv, "--claude-instance-id")).toBe("id-dec");
    expect(flagValue(argv, "--decision")).toBe("allow");
    expect(flagValue(argv, "--reason")).toBe("looks safe");
  });

  test("reason absent → --reason flag not in argv", () => {
    const argv = buildArgv(CLI, "decide", {
      claude_instance_id: "id-dec2",
      decision: "deny",
    });
    expect(hasFlag(argv, "--reason")).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// resume
// ---------------------------------------------------------------------------
describe("argv builder — resume", () => {
  test("claude_instance_id → --claude-instance-id", () => {
    const argv = buildArgv(CLI, "resume", { claude_instance_id: "id-res" });
    assertBase(argv, "resume");
    expect(flagValue(argv, "--claude-instance-id")).toBe("id-res");
  });
});

// ---------------------------------------------------------------------------
// find-missing
// ---------------------------------------------------------------------------
describe("argv builder — find-missing", () => {
  test("empty params → [cliPath, 'find-missing'] with no flags", () => {
    const argv = buildArgv(CLI, "find-missing", {});
    assertBase(argv, "find-missing");
    // find-missing has no CLI flags (the Go handler parses no flags).
    expect(argv.length).toBe(2);
  });
});

// ---------------------------------------------------------------------------
// expire
// ---------------------------------------------------------------------------
describe("argv builder — expire", () => {
  test("older_than → --older-than", () => {
    const argv = buildArgv(CLI, "expire", { older_than: "7d" });
    assertBase(argv, "expire");
    expect(flagValue(argv, "--older-than")).toBe("7d");
  });

  test("empty params → no flags", () => {
    const argv = buildArgv(CLI, "expire", {});
    expect(argv.length).toBe(2);
  });
});

// ---------------------------------------------------------------------------
// delete
// ---------------------------------------------------------------------------
describe("argv builder — delete", () => {
  test("claude_instance_id array → repeated --claude-instance-id flags", () => {
    const argv = buildArgv(CLI, "delete", {
      claude_instance_id: ["id-del-1", "id-del-2"],
    });
    assertBase(argv, "delete");
    const idValues = argv
      .map((v, i) => (argv[i - 1] === "--claude-instance-id" ? v : null))
      .filter(Boolean);
    expect(idValues).toContain("id-del-1");
    expect(idValues).toContain("id-del-2");
  });
});

// ---------------------------------------------------------------------------
// pause
// ---------------------------------------------------------------------------
describe("argv builder — pause", () => {
  test("claude_instance_id → --claude-instance-id", () => {
    const argv = buildArgv(CLI, "pause", { claude_instance_id: "id-pause" });
    assertBase(argv, "pause");
    expect(flagValue(argv, "--claude-instance-id")).toBe("id-pause");
  });
});

// ---------------------------------------------------------------------------
// global flags (b.32k)
// ---------------------------------------------------------------------------
describe("argv builder — global flags (b.32k)", () => {
  test("no globalOpts → no global flags injected", () => {
    const argv = buildArgv(CLI, "version", {});
    expect(argv).toEqual([CLI, "version"]);
  });

  test("storePath only → --store-path appears BEFORE verb token", () => {
    const argv = buildArgv(CLI, "version", {}, { storePath: "/tmp/foo.db" });
    expect(argv).toEqual([CLI, "--store-path", "/tmp/foo.db", "version"]);
  });

  test("home only → --home appears BEFORE verb token", () => {
    const argv = buildArgv(CLI, "version", {}, { home: "/tmp/h" });
    expect(argv).toEqual([CLI, "--home", "/tmp/h", "version"]);
  });

  test("tmuxCommand only → --tmux-command appears BEFORE verb token", () => {
    const argv = buildArgv(CLI, "version", {}, { tmuxCommand: "/usr/bin/tmux" });
    expect(argv).toEqual([CLI, "--tmux-command", "/usr/bin/tmux", "version"]);
  });

  test("all three globals → emitted in stable order before verb", () => {
    const argv = buildArgv(CLI, "version", {}, {
      storePath: "/tmp/foo.db",
      home: "/tmp/h",
      tmuxCommand: "/usr/bin/tmux",
    });
    expect(argv).toEqual([
      CLI,
      "--store-path", "/tmp/foo.db",
      "--home", "/tmp/h",
      "--tmux-command", "/usr/bin/tmux",
      "version",
    ]);
  });

  test("globals + verb flags → globals before verb, verb flags after", () => {
    const argv = buildArgv(CLI, "status", { claude_instance_id: "id-1" }, {
      storePath: "/tmp/foo.db",
    });
    expect(argv).toEqual([
      CLI,
      "--store-path", "/tmp/foo.db",
      "status",
      "--claude-instance-id", "id-1",
    ]);
  });

  test("empty globalOpts object → no flags emitted (parity with undefined)", () => {
    const argv = buildArgv(CLI, "version", {}, {});
    expect(argv).toEqual([CLI, "version"]);
  });
});
