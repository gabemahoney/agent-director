## Context

Plan b.jf1; Epic t1.jf1.r8; Task3 t2.jf1.r8.a8; approved PRD t1.ftd.y5; SRD t1.ftd.hj. User superseding instruction: “Treat it as real: AD may overrun a deadline it promises. Add it to Task 3 scope: give the SQL and cleanup paths an overall deadline, so remaining-time budgets cannot go stale. Add a regression that holds the trail mutex or slows I/O and proves the deadline is still met. Prove it red on the old code.” Historical cause still unknown; deadline weakness is mandatory scope, superseding its prior nonblocking treatment for Task3. Task2 gate input stays unchanged.

## Execution boundary

User: “make test-sandbox is yours, Docker groups and any TLA+ come to me via a scheduler handoff, TLA+ only if spec/tla changes.” No .claude reads/access, real AD stores, credentials/login/logout, host repository execution, local Docker groups/collectors, TLC/parser/Java, scheduler submissions, pushes or commits before Task2 gate confirmation. All later execution only make sandbox/make test-sandbox with isolated HOME/stores/sockets/processes. Task3 remains defined/unstarted until user accepts Task2 at 64a35baf37a1c44eb576ea661f3c30b13708f6fa.

## Work — Test Writer, before Engineer

Add a deterministic behavioral regression using existing actual Decide/store/trail test owners. Hold the real trail mutex or block a realistic I/O boundary with event synchronization; exercise the actual bounded public operation, not a copied algorithm or helper-only test. Preserve parallel coverage and unchanged 500ms request / existing 1.5s elapsed assertion. Show a completed authoritative verdict can coexist with blocked reporting and the caller still must return within its promised bound. Cover the precommit SQL/cleanup and stale-budget boundary where separate behavior requires it, without duplicating matrices. No arbitrary sleep to select race, assertion weakening, retries-until-green or widened product deadline.

Run against exact old production revision 64a35baf37a1c44eb576ea661f3c30b13708f6fa with new tests in an isolated sandbox. Retain original failing assertion, fixed timeout, production source hashes and full receipt; prove it fails for deadline overrun, not compile/setup failure. Any instrumentation must not manufacture success or change the algorithm. Regression must have finite cleanup even on old code. Test-only edits; source seam requests go to coordinator/Engineer.

## Acceptance

Actual old-code causal RED recorded before repair. New test ready for unchanged-assertion revalidation, precise evidence limitations, no claim historic failure cause established.

## Key paths

pkg/api/decide.go; pkg/api/decide_delivery_test.go; internal/store/lockwait.go; internal/store/permission.go; internal/trail/trail.go; relevant shared callers and existing fixtures. Read-only evidence: e3-task2/timing-investigation/FINAL-report.json and final-tests/09-full-test-sandbox.log. Shared trail mutex held through Write/Sync is reachable; DB connections released before emission; pre-verdict slow reads mean trail alone is not a proven historical cause. No unsynchronized-memory or duplicate-authorization defect established.
