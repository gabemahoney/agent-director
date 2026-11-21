import { test, expect } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { ErrInvalidFlags } from "../src/errors.js";
import { buildArgv } from "../src/internal/argv.js";
import { openClient, rejection } from "./internal/helper.js";

// CR1: argument encoding must never turn a different string into a retained ID
// or Message's text. The stand-in counts actual CLI starts, including probing.
test("malformed Unicode is refused before sendKeys/getDelivery starts the CLI", async () => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "ad-unicode-"));
  try {
    const log = path.join(tmp, "starts");
    const cli = path.join(tmp, "agent-director");
    fs.writeFileSync(cli, `#!/bin/sh\nprintf 'start\\n' >> '${log}'\nprintf '{"version":"0.0.0-dev","commit":"test"}\\n'\n`, { mode: 0o755 });
    using client = await openClient(path.join(tmp, "state.db"), { _cliPath: cli });
    const before = fs.readFileSync(log, "utf8");
    for (const value of ["r\ud800", "r\udc00", "r\ud800x", "r\udc00\ud800"]) {
      for (const [verb, param, call, want] of [
        ["send-keys", "text", () => client.sendKeys({ instance_id: "row", text: value }), "text must be valid UTF-8; nothing was stored, sent or typed"],
        ["send-keys", "delivery_request_id", () => client.sendKeys({ instance_id: "row", text: "hello", delivery_request_id: value }), "delivery_request_id must be valid UTF-8; nothing was stored, sent or typed"],
        ["get-delivery", "delivery_request_id", () => client.getDelivery({ caller_uuid: "abcdef01-2345-6789-abcd-ef0123456789", delivery_request_id: value }), "delivery_request_id must be nonempty UTF-8; nothing was read"],
      ] as const) {
        const err = await rejection(call());
        expect(err).toBeInstanceOf(ErrInvalidFlags);
        const e = err as ErrInvalidFlags;
        expect([e.verb, e.errName, e.errDescription]).toEqual([verb, "ErrInvalidFlags", `${verb}: ${want}`]);
        expect(e.errDescription).toContain(param);
        expect(fs.readFileSync(log, "utf8")).toBe(before);
      }
    }
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
});

test.each(["r�", "r\ufffd", "r🦋", "r\ud83e\udd8b", "literal \\ud800"])("valid Unicode stays exact: %s", (value) => {
  expect(buildArgv("ad", "send-keys", { instance_id: "row", text: value, delivery_request_id: value }))
    .toEqual(["ad", "send-keys", "--instance-id", "row", "--text", value, "--delivery-request-id", value]);
  expect(buildArgv("ad", "get-delivery", { caller_uuid: "caller", delivery_request_id: value }))
    .toEqual(["ad", "get-delivery", "--caller-uuid", "caller", "--delivery-request-id", value]);
});
