## Context

Plan b.jf1; Epic t1.jf1.r8; Task3 t2.jf1.r8.a8; approved PRD t1.ftd.y5; SRD t1.ftd.hj. User superseding instruction: “Treat it as real: AD may overrun a deadline it promises. Add it to Task 3 scope: give the SQL and cleanup paths an overall deadline, so remaining-time budgets cannot go stale. Add a regression that holds the trail mutex or slows I/O and proves the deadline is still met. Prove it red on the old code.” Historical cause still unknown; deadline weakness is mandatory scope, superseding its prior nonblocking treatment for Task3. Task2 gate input stays unchanged.

## Execution boundary

User: “make test-sandbox is yours, Docker groups and any TLA+ come to me via a scheduler handoff, TLA+ only if spec/tla changes.” No .claude reads/access, real AD stores, credentials/login/logout, host repository execution, local Docker groups/collectors, TLC/parser/Java, scheduler submissions, pushes or commits before Task2 gate confirmation. All later execution only make sandbox/make test-sandbox with isolated HOME/stores/sockets/processes. Task3 remains defined/unstarted until user accepts Task2 at 64a35baf37a1c44eb576ea661f3c30b13708f6fa.

## Work — Engineer, source only

Repair AD promised deadline overrun using the original absolute call deadline throughout acquisition, SQL/query/BEGIN/COMMIT, rollback/timeout restoration/connection return and synchronous trail reporting or any resulting wait. Remaining-time durations must not mint later deadlines or leave stale positive busy allowances. Preserve zero-budget semantics and documented uncontended-write behavior where compatible with the promised bound; surface any genuine product contradiction rather than silently changing the contract. Determine actual call-site impact and shared helper migration from source, not speculative broad redesign.

Cleanup cannot leak a transaction, return a connection with a changed timeout/open transaction, or use uncancellable deferred work that overruns the same deadline. Maintain atomic first-call-wins, permission/launch guards, honest before-versus-after-COMMIT outcomes, acknowledgement-before-output and fail-closed safety. Once committed, expiry must not falsely report ErrStoreBusy/nothing recorded or permit duplicate actions. Required audit ordering/durability must remain truthful; do not hide blocked work behind unbounded goroutines, silently drop required events or claim cancellation can bound time while the process is descheduled/uninterruptible. Technical design and unavoidable platform limits must be explicit in SRD, without weakening existing assertions.

Use retained old-code RED as repair target; no test/docs edits. Engineer chooses and reports concrete technical design after gate. No manufactured TLA model changes; affected-model jobs only if actual spec/tla input changes.

## Acceptance

One absolute operation budget governs all actual waits, SQL and cleanup; no renewed relative deadlines. Old red passes unchanged; independent Test Writer follows. No duplicate verdict, false rollback/end/delivery, unsafe pooled connection or unbounded background cleanup.

## Key paths

pkg/api/decide.go; pkg/api/decide_delivery_test.go; internal/store/lockwait.go; internal/store/permission.go; internal/trail/trail.go; relevant shared callers and existing fixtures. Read-only evidence: e3-task2/timing-investigation/FINAL-report.json and final-tests/09-full-test-sandbox.log. Shared trail mutex held through Write/Sync is reachable; DB connections released before emission; pre-verdict slow reads mean trail alone is not a proven historical cause. No unsynchronized-memory or duplicate-authorization defect established.
