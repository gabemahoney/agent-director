/**
 * processCount — parent-scoped agent-director process counter (b.3jn).
 *
 * Shared by no-leak.test.ts (the 1000-call leak assertion) and
 * no-leak-scoping.test.ts (the falsifiability regression tests). Extracted so
 * the exact pgrep command cannot silently drift between the two files.
 *
 * SCOPING RATIONALE (b.3jn):
 * The counter is `pgrep -c -P <ppid> agent-director` — scoped to DIRECT
 * CHILDREN of the given process, not every agent-director on the host. The
 * original signal was host-global (`pgrep -c agent-director`), which was fine
 * while the no-leak suite ran alone but is wrong under the b.2mt parallel
 * coverage phase: sibling gate coverage.go-root execs the real agent-director
 * binary in ~10 places (test/reboot-recovery/ etc.), and any such spawn alive
 * inside no-leak's ~50-80s window changed the host-global count, so
 * coverage.bun-test could never go green at max_parallel >= 2. Serializing
 * no-leak against binary-spawning siblings via a lock was rejected: the window
 * is ~50-80s and go-root runs ~62s, so serialization would push phase wall time
 * to ~2x the longest gate, violating SR-9 (wall <= longest gate * 1.10);
 * declaring the test non-parallelizable defeats the phase.
 *
 * Why strict equality is still valid under the parent-scoped count: the
 * assertion's intent is that the TS client reaps every subprocess it spawns. A
 * leaked subprocess is an unreaped/lingering CHILD of the bun test process — a
 * Bun.spawn child of process.pid, including a zombie, which pgrep counts.
 * Sibling gates' spawns have different parents and are excluded, so a delta of
 * zero remains the correct pass condition with no tolerance needed.
 *
 * Acknowledged limitation: a subprocess that re-parents away (e.g. its direct
 * child dies leaving a grandchild adopted by init) escapes the -P scope. Such
 * grandchildren already escaped attribution under the host-global count too, so
 * scoping loses no coverage the global form actually had. The client spawns
 * direct children and awaits their exit, so the target leak class is in scope.
 *
 * See gates/README.md "Coverage phase (parallel)" and bee b.3jn.
 */

/**
 * Counts agent-director processes that are DIRECT CHILDREN of `ppid`
 * (defaults to this process). Returns 0 when none.
 */
export function countChildAgentDirectorProcesses(ppid: number = process.pid): number {
  // `pgrep -c -P <ppid> <name>` prints the count of matching processes whose
  // parent is <ppid>. It exits 0 with "0\n" on zero matches on modern
  // util-linux (>= 2.36); earlier versions exit 1 with empty stdout on zero
  // matches. Both cases parse to 0 below.
  const proc = Bun.spawnSync({
    cmd: ["pgrep", "-c", "-P", String(ppid), "agent-director"],
    stdout: "pipe",
    stderr: "pipe",
  });
  const out = new TextDecoder().decode(proc.stdout).trim();
  const n = parseInt(out, 10);
  return Number.isFinite(n) ? n : 0;
}
