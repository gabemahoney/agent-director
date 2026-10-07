/**
 * system-install-disappeared.test.ts — b.xht: a verb call on a Client built
 * while its binary and cwd existed reports which one has since gone:
 * ErrSystemInstallDisappeared for the binary, ErrCallerCwdUnreachable for the
 * cwd, rather than a raw ENOENT blaming the binary path.
 */

import { test, expect, afterAll } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { ErrCallerCwdUnreachable, ErrSystemInstallDisappeared } from "../src/index.js";
import { openClient, rejection, withCwd } from "./internal/helper.js";

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "ad-xht-"));
afterAll(() => fs.rmSync(tmp, { recursive: true, force: true }));

test("binary gone after construction → ErrSystemInstallDisappeared (b.xht)", async () => {
  const bin = path.join(tmp, "ad-bin");
  fs.copyFileSync(process.env.CLI_PATH!, bin);
  fs.chmodSync(bin, 0o755);
  using client = await openClient(path.join(tmp, "bin-gone.db"), { _cliPath: bin });
  fs.unlinkSync(bin);

  const err = await rejection(client.list({}));
  expect(err).toBeInstanceOf(ErrSystemInstallDisappeared);
  expect(err).toMatchObject({ binaryPath: bin, verb: "list" });
  expect((err as ErrSystemInstallDisappeared).cause).toBeTruthy();
});

test("cwd gone after construction → ErrCallerCwdUnreachable (b.xht)", async () => {
  const workdir = fs.mkdtempSync(path.join(tmp, "cwd-gone-"));
  const err = await withCwd(workdir, async () => {
    using client = await openClient(path.join(tmp, "cwd-gone.db"));
    fs.rmSync(workdir, { recursive: true, force: true });
    return await rejection(client.list({}));
  });
  expect(err).toBeInstanceOf(ErrCallerCwdUnreachable);
  expect((err as ErrCallerCwdUnreachable).cwd).toBe(workdir);
});
