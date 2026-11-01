/**
 * readSurface.ts — the TS half of apitest.NormalizeReadSurfaceProbes: a live
 * read-surface row's alive process record is confirmed by the read's own probe
 * of the test's process, at the read's clock, so the goldens hold
 * PROBED in place of both its times.
 */

import { expect } from "bun:test";

/** apitest.ReadSurfaceProbed. */
export const PROBED = "probed-by-this-read";

const probedRecord =
  /"process_liveness":\{"status":"confirmed","confirmed_at":"([^"]*)","source":"process","checked_at":"([^"]*)","reason":null\}/g;

/**
 * Returns out, a serialized status, get or list result, with each probed
 * process record's two times replaced by PROBED, after checking they are one
 * millisecond UTC instant within [fromMs, toMs], the read's own call.
 */
export function normalizeProbes(out: string, fromMs: number, toMs: number): string {
  return out.replace(probedRecord, (rec: string, confirmed: string, checked: string) => {
    const at = Date.parse(confirmed);
    expect({ rec, utcMs: /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$/.test(confirmed), same: confirmed === checked, inRead: at >= fromMs && at <= toMs })
      .toEqual({ rec, utcMs: true, same: true, inRead: true });
    return rec.split(confirmed).join(PROBED);
  });
}
