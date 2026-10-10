// subprocess-coverage-6-store-path-default driver.
//
// Validates bug b.32k: the TS Client must NOT mimic the CLI's default
// resolution for storePath. Two assertions:
//
//   1. Omit storePath  → argv contains no `--store-path` token; the
//      subprocess HOME is the consumer's HOME (so the CLI's own default
//      resolution operates against the consumer-visible filesystem).
//   2. Pass storePath  → argv contains `--store-path <path>` verbatim.
//
// The stub CLI echoes its "$@" and $HOME back via extra fields on the
// version envelope. The driver inspects argv and home to confirm both
// directions of the b.32k contract.

import { Client } from "agent-director";

const EXPLICIT_PATH = "/tmp/explicit.db";

function fail(msg) {
  console.error(`FAIL ${msg}`);
  process.exit(1);
}

// ── 1. Omit storePath: --store-path must NOT appear in argv. ───────────────
{
  const client = new Client({});
  try {
    const v = await client.version({});
    if (!Array.isArray(v.argv)) {
      fail(`store-path-omit: stub did not echo argv array; got ${JSON.stringify(v)}`);
    }
    if (v.argv.includes("--store-path")) {
      fail(
        `store-path-omit: argv contains --store-path even though Client was constructed without storePath; argv=${JSON.stringify(v.argv)}`,
      );
    }
    if (v.home !== process.env.HOME) {
      fail(
        `store-path-omit: subprocess HOME="${v.home}" but consumer HOME="${process.env.HOME}"; CLI default resolution would not see the right HOME`,
      );
    }
    console.log(
      `OK store-path-omit argv=${JSON.stringify(v.argv)} home="${v.home}"`,
    );
  } finally {
    client[Symbol.dispose]?.();
  }
}

// ── 2. Pass storePath: --store-path <path> must appear verbatim. ───────────
{
  const client = new Client({ storePath: EXPLICIT_PATH });
  try {
    const v = await client.version({});
    if (!Array.isArray(v.argv)) {
      fail(`store-path-provide: stub did not echo argv array; got ${JSON.stringify(v)}`);
    }
    const idx = v.argv.indexOf("--store-path");
    if (idx < 0) {
      fail(
        `store-path-provide: argv missing --store-path; argv=${JSON.stringify(v.argv)}`,
      );
    }
    if (v.argv[idx + 1] !== EXPLICIT_PATH) {
      fail(
        `store-path-provide: argv[--store-path+1]="${v.argv[idx + 1]}" expected "${EXPLICIT_PATH}"; argv=${JSON.stringify(v.argv)}`,
      );
    }
    console.log(
      `OK store-path-provide argv contains --store-path ${EXPLICIT_PATH}`,
    );
  } finally {
    client[Symbol.dispose]?.();
  }
}
