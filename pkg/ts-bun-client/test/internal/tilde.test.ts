import { test, expect, describe, beforeAll, afterAll } from "bun:test";
import * as os from "node:os";
import { expandTilde } from "../../src/internal/tilde.js";

describe("expandTilde", () => {
  test("bare ~ expands to home directory", () => {
    const home = process.env["HOME"] ?? os.homedir();
    expect(expandTilde("~")).toBe(home);
  });

  test("~/foo expands to ${HOME}/foo", () => {
    const home = process.env["HOME"] ?? os.homedir();
    expect(expandTilde("~/foo")).toBe(`${home}/foo`);
  });

  test("~/a/b/c expands to ${HOME}/a/b/c", () => {
    const home = process.env["HOME"] ?? os.homedir();
    expect(expandTilde("~/a/b/c")).toBe(`${home}/a/b/c`);
  });

  test("absolute path is returned unchanged", () => {
    expect(expandTilde("/abs/path")).toBe("/abs/path");
  });

  test("empty string returns empty string", () => {
    expect(expandTilde("")).toBe("");
  });

  test("relative path is returned unchanged", () => {
    expect(expandTilde("relative/path")).toBe("relative/path");
  });

  test("tilde not at start is returned unchanged", () => {
    expect(expandTilde("foo~bar")).toBe("foo~bar");
  });

  describe("HOME fallback to os.homedir()", () => {
    let savedHome: string | undefined;

    beforeAll(() => {
      savedHome = process.env["HOME"];
      // Clear HOME to force the os.homedir() fallback path.
      delete process.env["HOME"];
    });

    afterAll(() => {
      // Always restore HOME, even if the test throws.
      if (savedHome !== undefined) {
        process.env["HOME"] = savedHome;
      } else {
        delete process.env["HOME"];
      }
    });

    test("bare ~ falls back to os.homedir() when HOME is unset", () => {
      const expected = os.homedir();
      expect(expandTilde("~")).toBe(expected);
    });

    test("~/foo falls back to os.homedir()/foo when HOME is unset", () => {
      const expected = `${os.homedir()}/foo`;
      expect(expandTilde("~/foo")).toBe(expected);
    });
  });
});
