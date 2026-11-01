/**
 * readSurface.ts — the TS half of apitest.NormalizeReadSurfaceProbes: a live
 * read-surface row keeps its first process confirmation across later probes.
 * The goldens hold PROBED in place of its validated first and latest times.
 */

import { expect } from "bun:test";

/** apitest.ReadSurfaceProbed. */
export const PROBED = "probed-by-this-read";

const probedRecord =
  /"process_liveness":\{"status":"confirmed","confirmed_at":"([^"]*)","source":"process","checked_at":"([^"]*)","reason":null\}/g;

/**
 * Pin first confirmations per row in one map per seeded store. A first sample
 * has equal times; every latest check is in this call's window. After those
 * assertions only the two process times and failed pane check are normalized.
 * instanceID identifies status results, which omit the row id.
 * Must use: a TS read-surface test normalizes probe times through this, never
 * a second normalizer.
 */
export function normalizeProbes(out: string, fromMs: number, toMs: number, first: Map<string, string>, instanceID: string): string {
  type Row = { instance_id?: string; confirmations: { process_liveness: {
    status: string; confirmed_at: string; source: string; checked_at: string;
  } } };
  const doc = JSON.parse(out) as Row & { spawns?: Row[] };
  for (const row of doc.spawns ?? [doc]) {
    const rec = row.confirmations.process_liveness;
    if (rec.status !== "confirmed" || rec.source !== "process") continue;
    const id = row.instance_id ?? instanceID;
    expect(id.length).toBeGreaterThan(0);
    const checked = Date.parse(rec.checked_at);
    expect(/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$/.test(rec.checked_at)).toBe(true);
    expect(checked >= fromMs && checked <= toMs).toBe(true);
    const pinned = first.get(id) ?? rec.checked_at;
    first.set(id, pinned);
    expect({ id, first: rec.confirmed_at }).toEqual({ id, first: pinned });
  }
  out = out.replace(probedRecord, (rec: string, confirmed: string, checked: string) =>
    rec.split(confirmed).join(PROBED).split(checked).join(PROBED));
  return out.replace(/"displayed_state":\{"status":"not_confirmed","confirmed_at":null,"source":null,"checked_at":"([^"]*)","reason":"unreadable"\}/g,
    (rec: string, checked: string) => {
      const at = Date.parse(checked);
      expect(/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$/.test(checked)).toBe(true);
      expect(at >= fromMs && at <= toMs).toBe(true);
      return rec.split(checked).join(PROBED);
    });
}
