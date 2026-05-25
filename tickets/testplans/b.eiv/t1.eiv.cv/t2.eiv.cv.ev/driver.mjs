// subprocess-coverage-4-env-inherit driver.
//
// Validates SRD SR-1.4: env vars set on the consumer process (process.env
// at call time) are visible inside the spawned subprocess. The Client must
// snapshot process.env at call time and pass it through to Bun.spawn.
//
// The stub CLI echoes AGENT_DIRECTOR_STORE_PATH and SUB_COV_PROBE back via
// extra fields on the version envelope. The driver compares the round-trip
// values against the original process.env settings.

import * as path from "node:path";
import { Client } from "agent-director";

const HOME = process.env.HOME;
const STORE = path.join(HOME, ".agent-director", "state.db");
const STORE_PATH_VALUE = "/tmp/override.db";
const PROBE_VALUE = "magic-probe-value";

function fail(msg) {
  console.error(`FAIL ${msg}`);
  process.exit(1);
}

process.env.AGENT_DIRECTOR_STORE_PATH = STORE_PATH_VALUE;
process.env.SUB_COV_PROBE = PROBE_VALUE;

const client = new Client({ storePath: STORE, createIfMissing: true });
try {
  const v = await client.version({});
  if (v.env_store_path !== STORE_PATH_VALUE) {
    fail(
      `env-inherit: AGENT_DIRECTOR_STORE_PATH did not propagate; subprocess saw "${v.env_store_path}", expected "${STORE_PATH_VALUE}"`,
    );
  }
  if (v.env_probe !== PROBE_VALUE) {
    fail(
      `env-inherit: SUB_COV_PROBE did not propagate; subprocess saw "${v.env_probe}", expected "${PROBE_VALUE}"`,
    );
  }
  console.log(
    `OK env-inherit subprocess saw AGENT_DIRECTOR_STORE_PATH="${v.env_store_path}" SUB_COV_PROBE="${v.env_probe}"`,
  );
} finally {
  client[Symbol.dispose]?.();
}
