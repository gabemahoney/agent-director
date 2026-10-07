/**
 * spawner.test.ts — src/internal/spawner.ts runSubprocess: stdout returned on
 * exit 0 (SR-1.1/1.5), >64 KB drained without deadlock or cap (SR-7.1/7.2), a
 * non-zero exit throws carrying exit code and stderr (SR-7.3), and non-JSON
 * stdout on exit 0 throws too.
 */

import { test, expect } from "bun:test";
import * as path from "node:path";
import { ErrSubprocessCrash, runSubprocess } from "../src/internal/spawner.js";
import { rejection } from "./internal/helper.js";

const FIXTURES = path.resolve(import.meta.dir, "fixtures/epic-a");

test("valid JSON on stdout + exit 0 → {stdout, exitCode: 0, signalCode: null}", async () => {
  const r = await runSubprocess([path.join(FIXTURES, "success.sh")]);
  expect(r).toMatchObject({ exitCode: 0, signalCode: null });
  expect(JSON.parse(r.stdout)).toEqual({ result: "ok", data: "hello-from-fixture" });
});

test(">64 KB stdout → no deadlock, every byte captured", async () => {
  const r = await runSubprocess(["bun", path.join(FIXTURES, "large-stdout.js")]);
  expect(r).toMatchObject({ exitCode: 0, signalCode: null });
  expect((JSON.parse(r.stdout) as { data: string }).data.length).toBe(102400);
}, 15_000);

test("exit 1 → ErrSubprocessCrash carrying the exit code and the full stderr", async () => {
  const err = await rejection(runSubprocess([path.join(FIXTURES, "nonzero-exit.sh")]));
  expect(err).toBeInstanceOf(ErrSubprocessCrash);
  expect(err).toMatchObject({ exitCode: 1, signalCode: null, stderrFull: "something went wrong in subprocess\n" });
  expect((err as Error).message).toContain("something went wrong in subprocess");
});

test("non-JSON stdout + exit 0 → ErrSubprocessCrash with exit code 0 (a parse failure)", async () => {
  const err = await rejection(runSubprocess([path.join(FIXTURES, "non-json.sh")]));
  expect(err).toBeInstanceOf(ErrSubprocessCrash);
  expect(err).toMatchObject({ exitCode: 0 });
});
