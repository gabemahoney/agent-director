

### User-added mandatory deadline repair

Plan b.jf1; Epic t1.jf1.r8; Task3 t2.jf1.r8.a8; approved PRD t1.ftd.y5; SRD t1.ftd.hj. User superseding instruction: “Treat it as real: AD may overrun a deadline it promises. Add it to Task 3 scope: give the SQL and cleanup paths an overall deadline, so remaining-time budgets cannot go stale. Add a regression that holds the trail mutex or slows I/O and proves the deadline is still met. Prove it red on the old code.” Historical cause still unknown; deadline weakness is mandatory scope, superseding its prior nonblocking treatment for Task3. Task2 gate input stays unchanged.

User: “make test-sandbox is yours, Docker groups and any TLA+ come to me via a scheduler handoff, TLA+ only if spec/tla changes.” No .claude reads/access, real AD stores, credentials/login/logout, host repository execution, local Docker groups/collectors, TLC/parser/Java, scheduler submissions, pushes or commits before Task2 gate confirmation. All later execution only make sandbox/make test-sandbox with isolated HOME/stores/sockets/processes. Task3 remains defined/unstarted until user accepts Task2 at 64a35baf37a1c44eb576ea661f3c30b13708f6fa.

Sequence: deterministic old-code RED → Engineer source repair → independent unchanged-regression GREEN → public Task3 composition/collector → one settled docs pass → advice/quality/full sandbox → local commit and exact-revision user scheduler gate. Preserve original bounds, atomic commit outcome, safe cleanup and audit truth. No implementation or test execution before gate.
