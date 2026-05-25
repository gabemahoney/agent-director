// subprocess-client-3-no-leak driver — Epic C / SR-10.5.
//
// 1000 sequential client.list({state: ["check_permission"]}) calls against
// an installed-tarball Client. The caller bash script samples pgrep -c
// before and after; the post-loop count must equal baseline. This driver
// only needs to issue the calls without throwing.

import * as path from "node:path";
import { Client } from "agent-director";

const HOME = process.env.HOME;
const STORE = path.join(HOME, ".agent-director", "state.db");

const client = new Client({ storePath: STORE, createIfMissing: true });
try {
  const iterations = 1000;
  for (let i = 0; i < iterations; i++) {
    await client.list({ state: ["check_permission"] });
  }
  // Settle briefly so any in-flight reap completes before the caller samples.
  await new Promise((r) => setTimeout(r, 50));
  console.log("OK no-leak-1000-sequential");
} finally {
  client[Symbol.dispose]?.();
}
