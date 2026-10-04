import { test, expect, describe, beforeEach, afterEach } from "bun:test";
import * as os from "node:os";
import { expandTilde } from "../../src/internal/tilde.js";

const FAKE_HOME = "/fake/home";

// Each HOME state expandTilde must handle. A set, non-empty HOME is used
// as-is; an unset or empty HOME falls back to os.homedir() (b.vqj: an empty
// HOME used to expand "~/x" to "/x" and "~" to ""). `expectedHome` runs after
// `apply`, so os.homedir() sees the same HOME as the code under test.
const homeStates = [
  {
    id: "HOME set",
    apply: () => {
      process.env["HOME"] = FAKE_HOME;
    },
    expectedHome: () => FAKE_HOME,
  },
  {
    id: "HOME unset",
    apply: () => {
      delete process.env["HOME"];
    },
    expectedHome: () => os.homedir(),
  },
  {
    id: 'HOME=""',
    apply: () => {
      process.env["HOME"] = "";
    },
    expectedHome: () => os.homedir(),
  },
];

// [input, suffix appended to the home directory]
const tildeInputs = [
  ["~", ""],
  ["~/x", "/x"],
  ["~/a/b/c", "/a/b/c"],
] as const;

describe("expandTilde: leading ~ resolves to HOME, else os.homedir()", () => {
  let savedHome: string | undefined;

  beforeEach(() => {
    savedHome = process.env["HOME"];
  });

  afterEach(() => {
    // Restore HOME exactly, including when it was originally unset. afterEach
    // runs even when the test fails.
    if (savedHome === undefined) {
      delete process.env["HOME"];
    } else {
      process.env["HOME"] = savedHome;
    }
  });

  for (const state of homeStates) {
    for (const [input, suffix] of tildeInputs) {
      test(`${state.id}: ${JSON.stringify(input)} → home${suffix}`, () => {
        state.apply();
        const home = state.expectedHome();
        // An empty fallback would make the expectation below vacuous.
        expect(home).not.toBe("");
        expect(expandTilde(input)).toBe(home + suffix);
      });
    }
  }
});

describe("expandTilde: values without a leading ~ or ~/ are unchanged", () => {
  test.each([
    ["absolute path", "/abs/path"],
    ["empty string", ""],
    ["relative path", "relative/path"],
    ["tilde not at start", "foo~bar"],
  ])("%s", (_label, input) => {
    expect(expandTilde(input)).toBe(input);
  });
});
