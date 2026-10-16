## Context

Plan b.jf1; Epic t1.jf1.r8; Task3 t2.jf1.r8.a8; approved PRD t1.ftd.y5; SRD t1.ftd.hj. User superseding instruction: “Treat it as real: AD may overrun a deadline it promises. Add it to Task 3 scope: give the SQL and cleanup paths an overall deadline, so remaining-time budgets cannot go stale. Add a regression that holds the trail mutex or slows I/O and proves the deadline is still met. Prove it red on the old code.” Historical cause still unknown; deadline weakness is mandatory scope, superseding its prior nonblocking treatment for Task3. Task2 gate input stays unchanged.

## Execution boundary

User: “make test-sandbox is yours, Docker groups and any TLA+ come to me via a scheduler handoff, TLA+ only if spec/tla changes.” No .claude reads/access, real AD stores, credentials/login/logout, host repository execution, local Docker groups/collectors, TLC/parser/Java, scheduler submissions, pushes or commits before Task2 gate confirmation. All later execution only make sandbox/make test-sandbox with isolated HOME/stores/sockets/processes. Task3 remains defined/unstarted until user accepts Task2 at 64a35baf37a1c44eb576ea661f3c30b13708f6fa.

## Work — Test Writer, after Engineer

Run the retained deterministic trail-mutex/slow-I/O regression unchanged against repaired source; bind RED and GREEN to exact production hashes. Add only necessary distinct precommit SQL, cleanup and stale absolute-budget cases using real Store operations and existing fixtures. Demonstrate bounded return plus accurate committed/not-committed outcome, safe connection reuse/discard, no open transaction, audit truth and no duplicate permission/output. A late acquisition or scheduled delay cannot renew the original deadline. Preserve actual parallel cases, 500ms/1.5s existing bounds, existing answer/control deadlines and acknowledgement-before-output. No timeout enlargement or repeated-until-green.

Retain all failures/commands/environment/input hashes. A passing new regression proves its controlled boundary, not the exact cause of historical run09. No source/docs edits. Full make test-sandbox remains solely final writer .zj after all Task3 work settles; no full run here.

## Acceptance

Old-code causal RED plus repaired GREEN with same assertions; genuine SQL/cleanup/trail blocking boundaries covered without fake copied implementation; current source race/authority/cleanup safety demonstrated honestly.

## Key paths

pkg/api/decide.go; pkg/api/decide_delivery_test.go; internal/store/lockwait.go; internal/store/permission.go; internal/trail/trail.go; relevant shared callers and existing fixtures. Read-only evidence: e3-task2/timing-investigation/FINAL-report.json and final-tests/09-full-test-sandbox.log. Shared trail mutex held through Write/Sync is reachable; DB connections released before emission; pre-verdict slow reads mean trail alone is not a proven historical cause. No unsynchronized-memory or duplicate-authorization defect established.
