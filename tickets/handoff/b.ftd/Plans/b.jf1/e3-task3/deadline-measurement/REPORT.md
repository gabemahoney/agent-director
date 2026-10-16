# Deadline measurement report

The historical COMMIT failure was not reproduced. Root cause remains unproven; no product fix is justified by this sample.

Unmodified originals at 64a35baf and HEAD each passed **332 repetitions of every one of 36 leaf cases** (11,952 leaf outcomes per revision; zero failures). The two original test function source blocks hash identically. This does not prove absence of a regression.

Balanced sequential revision batches covered parallelism 4 and 192, then eight and 32 simultaneous same-revision test processes. Every process used a fresh sandbox HOME/state/TMPDIR and local git config. No full suite or external scheduler workload ran.

HEAD actual call/stage timing follows; milliseconds. Whole-subtest timings include fixture/readback/teardown and are not Decide duration.

| Sample | Phase | n | p50 ms | p99 ms | max ms |
|---|---|---:|---:|---:|---:|
| instrumented | decide/bounded | 390 | 91.990 | 147.424 | 149.573 |
| instrumented | repair/unbounded | 990 | 8.964 | 61.965 | 79.091 |
| instrumented | transaction_sql/bounded | 390 | 0.114 | 0.824 | 3.498 |
| instrumented | commit/bounded | 390 | 2.046 | 6.547 | 11.079 |
| instrumented | cleanup/bounded | 1950 | 0.004 | 0.081 | 0.265 |
| instrumented | worker_scheduling/bounded | 1950 | 0.002 | 0.008 | 0.349 |
| instrumented_load | decide/bounded | 520 | 601.784 | 758.876 | 769.687 |
| instrumented_load | repair/unbounded | 1320 | 39.639 | 370.065 | 413.788 |
| instrumented_load | transaction_sql/bounded | 520 | 0.121 | 0.229 | 0.291 |
| instrumented_load | commit/bounded | 520 | 11.398 | 22.299 | 24.862 |
| instrumented_load | cleanup/bounded | 2600 | 0.005 | 0.082 | 0.191 |
| instrumented_load | worker_scheduling/bounded | 2600 | 0.002 | 0.007 | 0.099 |
| instrumented_load | sql_result_publication/bounded | 2600 | 0.000 | 0.000 | 0.001 |
| instrumented_load | decision_trail/bounded | 520 | 587.192 | 747.139 | 755.126 |
| instrumented_high_load | decide/bounded | 1248 | 1000.563 | 1001.080 | 1001.239 |
| instrumented_high_load | repair/unbounded | 3168 | 280.905 | 1486.102 | 1685.185 |
| instrumented_high_load | read_sql/bounded | 3758 | 0.096 | 5.928 | 15.034 |
| instrumented_high_load | transaction_sql/bounded | 1248 | 0.138 | 0.256 | 0.847 |
| instrumented_high_load | commit/bounded | 1248 | 48.911 | 87.256 | 122.390 |
| instrumented_high_load | cleanup/bounded | 5006 | 0.005 | 0.071 | 0.247 |
| instrumented_high_load | worker_scheduling/bounded | 5006 | 0.001 | 0.012 | 0.276 |
| instrumented_high_load | sql_result_publication/bounded | 5006 | 0.000 | 0.000 | 0.024 |
| instrumented_high_load | decision_trail/bounded | 1248 | 950.482 | 984.913 | 994.226 |

The slow successful Decide calls are dominated by post-COMMIT trail emission. This cannot directly explain their own COMMIT error: the trail call executes after a successful transaction. A cross-call I/O effect remains a hypothesis. Sampled SQL result publication and cleanup were measured separately; no failing COMMIT or publication race was captured.

The original error text conflates a COMMIT ExecContext error and an operation-reporting timeout after COMMIT starts. Historical logs do not distinguish these. The three Repair failures happen during setup Decide, before repair runs.

No deadlines, retries, assertions, canonical source, or ticket statuses changed. No commit was made. The next step is to instrument the actual failing package workload with request-correlated branch evidence, establish the cause, and have TestWriter prove RED before a fix.

See [FINAL-report.json](FINAL-report.json) for complete counts, environment, caveats, and artifact paths; [measurement-summary.json](measurement-summary.json) includes per-case denominators and phase outliers. Source and instrumentation hashes, exact runner scripts, raw JSON, stderr, and sandbox logs are retained here.
