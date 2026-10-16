### E3 technical decision: terminal Cursor closure preserves request proof

This decision for SRD `t1.ftd.hj` supersedes only the earlier E3 expectation
that terminal Cursor closure stamps each request `agent_gone`. It preserves
the existing E2 distinction between gateway/process liveness and request proof.

The user-requested 17-group gate reported 16 passing groups. The remaining
Cursor relay group exposed cr.05, cr.09 and cr.10 expecting
`proven_gone_at IS NULL` after gateway/runtime loss; cr.09 and cr.10 already
had non-null delivery acknowledgements and null proof. Acknowledgement is
neither Cursor receipt nor execution nor dialog-closure proof.

Terminal Cursor reconciliation must still establish the row's `missing` or
`ended` state and atomically deny undecided requests, close requests still
awaiting an answer and perform subagent cleanup. That terminal state ends the
owning row's request hold without establishing per-request proof. Gateway or
runtime death, including fatal cleanup, public-read reconciliation and
authenticated SessionEnd, must leave Cursor request proof unchanged. Existing
validated exact-request execution proof must remain intact; delivery, attempted
decision, pane and sender evidence must also be retained.

The shared `closeOrphanedRequests` proof statement now checks the actual
`spawns` row in the caller's transaction using NULL-safe
`spawns.agent IS NOT 'cursor'`. Claude, Codex and legacy NULL-agent rows retain
their existing `agent_gone` policy. Cursor proof continues to use the existing
authenticated, matching execution validation route. The guard changes no
request-close transaction boundary, row-state reconciliation rule, bounded
context, deadline or write-lock waiting policy. A Cursor `missing` row does not
establish runtime death.

The frozen causal RED evidence in `../red/report.json` records fabricated
`agent_gone` proof through Get/Status/List, four terminal-close paths, fatal
runtime cleanup and gateway SIGKILL. `../engineer/report.json` records the same
focused selections GREEN after the central guard, with five legacy close
controls and nearby proof controls passing. The 30 protected collector/driver
and cr-case files remained unchanged. These focused results do not establish a
full-suite or full-gate PASS; independent review and the requested gate remain
with the caller.

No schema, API, verb, product deadline, migration or backfill changes are part
of this repair. Existing historical rows with incorrect proof are not rewritten.
This decision does not change PRD status.
