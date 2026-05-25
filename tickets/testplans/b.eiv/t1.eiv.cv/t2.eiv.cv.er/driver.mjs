// subprocess-coverage-5-error-catalog driver.
//
// Validates SRD SR-4: every err_name in pkg/api/errnames/catalog.json
// round-trips through the subprocess Client as an instanceof Err<Name>
// throw, with e.errName matching the canonical name. Also validates the
// ErrUnknownErrorName escape hatch for names absent from the static map.
//
// The catalog file is mounted read-only at /work/source/pkg/api/errnames/
// catalog.json; the script passes its path through the CATALOG_PATH env var
// so the driver doesn't hard-code the mount layout.

import { readFileSync } from "node:fs";
import * as path from "node:path";
import * as AD from "agent-director";

const HOME = process.env.HOME;
const STORE = path.join(HOME, ".agent-director", "state.db");
const CATALOG_PATH =
  process.env.CATALOG_PATH ?? "/work/source/pkg/api/errnames/catalog.json";

function fail(msg) {
  console.error(`FAIL ${msg}`);
  process.exit(1);
}

const catalog = JSON.parse(readFileSync(CATALOG_PATH, "utf-8"));
if (!Array.isArray(catalog) || catalog.length === 0) {
  fail(`catalog at ${CATALOG_PATH} is empty or non-array`);
}

const client = new AD.Client({ storePath: STORE, createIfMissing: true });
try {
  let checked = 0;
  for (const entry of catalog) {
    const name = entry.name;
    if (typeof name !== "string" || !name.startsWith("Err")) {
      fail(`catalog entry has no usable name: ${JSON.stringify(entry)}`);
    }
    const Ctor = AD[name];
    if (typeof Ctor !== "function") {
      fail(`agent-director has no export named "${name}" (catalog entry without TS class)`);
    }

    process.env.STUB_ERR_NAME = name;
    process.env.STUB_ERR_DESC = `stub-desc-${name}`;
    let caught = null;
    try {
      await client.version({});
    } catch (e) {
      caught = e;
    }
    if (caught === null) fail(`${name}: call resolved instead of rejecting`);
    if (!(caught instanceof Ctor)) {
      fail(
        `${name}: not instanceof; got ${caught?.constructor?.name}: ${caught?.message}`,
      );
    }
    if (!(caught instanceof AD.AgentDirectorError)) {
      fail(`${name}: not instanceof AgentDirectorError`);
    }
    if (caught.errName !== name) {
      fail(`${name}: e.errName="${caught.errName}" (expected "${name}")`);
    }
    if (caught.verb !== "version") {
      fail(`${name}: e.verb="${caught.verb}" (expected "version")`);
    }
    checked++;
  }
  console.log(`OK error-catalog ${checked}/${catalog.length} entries round-tripped`);

  // ── Unknown name escape hatch (SR-4.3) ───────────────────────────────────
  process.env.STUB_ERR_NAME = "ErrThisIsNotInTheCatalogXYZ";
  process.env.STUB_ERR_DESC = "synthetic unknown error";
  let unkCaught = null;
  try {
    await client.version({});
  } catch (e) {
    unkCaught = e;
  }
  if (unkCaught === null) fail("unknown-name: call resolved instead of rejecting");
  if (!(unkCaught instanceof AD.ErrUnknownErrorName)) {
    fail(
      `unknown-name: expected ErrUnknownErrorName, got ${unkCaught?.constructor?.name}: ${unkCaught?.message}`,
    );
  }
  console.log("OK unknown-name surfaces ErrUnknownErrorName");
} finally {
  client[Symbol.dispose]?.();
}
