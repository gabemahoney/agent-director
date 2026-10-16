# Complete API package deadline measurement

Historical COMMIT deadlines remain unreproduced. **No root cause or product fix is established.** Canonical source, tests, assertions, deadlines and ticket statuses are unchanged; no commit was made.

Unmodified full `pkg/api` ran in balanced order **old → HEAD → HEAD → old**, with `GOMAXPROCS=192`, `-parallel=192`, fresh HOME/state/TMPDIR and local git config. Every package passed. Original36 leaf cases passed twice per revision: **72/72 old,72/72 HEAD**. Both original test function blocks hash identically.

| Run | Revision | Package seconds | Passed leaves | Skipped | Original cases |
|---|---|---:|---:|---:|---:|
| 01-old | old | 114.413 | 9580 | 50 |36/36|
| 02-head | HEAD | 115.215 | 9589 | 50 |36/36|
| 04-head | HEAD | 115.542 | 9589 | 50 |36/36|
| 05-old | old | 112.943 | 9580 | 50 |36/36|
| 07-head-instrumented | HEAD | 114.317 | 9589 | 50 |36/36|

Final instrumented run07 also passed the complete package with dedicated telemetry output,21,275 records and zero channel drops. Actual modernc COMMIT entry/exit pairs were complete:58 bounded and4,541 unbounded. **Zero COMMIT driver errors; zero outer-guard unconfirmed branches.** Minimum remaining budget before bounded COMMIT was381.886ms.

| Final07 phase | n | p50 ms | p99 ms | max ms |
|---|---:|---:|---:|---:|
|driver_commit_exit/bounded|58|6.088|94.304|94.304|
|commit/bounded|58|6.091|94.308|94.308|
|sql_result_publication/bounded|440|0.001|0.029|0.042|
|cleanup/bounded|440|0.084|2.738|27.733|
|worker_scheduling/bounded|451|0.002|0.021|1.781|
|decision_trail/bounded|23|490.213|974.217|974.217|
|decide/bounded|49|499.932|1001.028|1001.028|
|repair/unbounded|132|16.804|663.152|727.506|

API durations above exclude fixtures/readback/teardown. The four historical subcases passed in final07 with whole-subtest durations1.93–6.09s; those totals are not Decide or COMMIT duration. Slow successful Decide calls include post-COMMIT trail work and do not explain earlier COMMIT uncertainty.

The sandbox reports192 CPUs and `cpu.max = max 100000`; a separate root VM8-CPU quota is not assumed to constrain it. Actual historical ancestor/resource conditions remain unknown.

Attempts are retained honestly:03 failed compilation before tests;06 was observer-perturbed by stderr capture (24 pretrust leaf failures) and had one separate non-COMMIT admission failure. Its original36 cases passed. Dedicated-FD run07 fixes the observer sink and passes every package assertion. The initial runner also emitted an EOF error after both test commands had already completed PASS because its script was edited while open; later runner files stayed fixed.

The next causal diagnostic is this instrumentation in the actual historical broader workload, separately authorized, retaining original concurrency/budgets and recording cross-package overlap, cgroup scheduling/throttling, and the blocking branch when a COMMIT fails. Standalone complete-package context still does not reproduce it; arbitrary further pressure cannot prove a cause. **No canonical fix should be chosen from these successful COMMIT samples.**

See [FINAL-report.json](FINAL-report.json), [measurement-summary.json](measurement-summary.json), [phase-boundary-summary.json](phase-boundary-summary.json), exact runner scripts, versioned instrumentation patches/hashes, raw JSON and sandbox logs. Original source archives were reused only after hash/member filtering verification; no `.claude` content was accessed.
