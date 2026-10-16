# Final explicit CPU-quota comparison

The historical COMMIT failures remain **unreproduced; root cause unproven**. No source, test, assertion, deadline, retry behavior, ticket status, or commit changed. Canonical HEAD remains `f0e9b48f2ef03d0d5804b215d77d16f78108c5db`, with clean git status.

Four sequential fresh sandbox runs used `make sandbox SANDBOX_FLAGS='--cpus=4'`. Inside every container, `cpu.max=400000 100000`, 192 visible CPUs, Go1.22.10, default GOMAXPROCS/test parallelism, canonical fresh `HOME=/home/sandbox`, unset TMPDIR, umask022, and an isolated run-local global git config. Environment was cleared except explicit sandbox/build-cache values. No host Go/Bun, external pressure workload, full suite, heavy container test group, helper invocation, or telemetry overlay ran.

Both filtered source archives were verified by archive SHA256 and all 2,029 old /2,051 HEAD source-file SHA256 values before extraction into ignored `_snapshot` directories. No `.claude` content was read, copied, or hashed. Old revision is `64a35baf37a1c44eb576ea661f3c30b13708f6fa`.

|Run|Package seconds|Leaf passes|Leaf skips|Original target passes|Throttled periods|Cumulative throttle seconds|
|---|---:|---:|---:|---:|---:|---:|
|01-old-narrow|4.096|360|0|360|63|95.232|
|02-head-narrow|4.125|360|0|360|67|97.480|
|03-old-package|135.383|9578|50|36|468|1095.819|
|04-head-package|135.182|9587|50|36|458|1034.679|

Narrow runs use `go test -count=10 -json -timeout 30m ./pkg/api -run '^(TestDecideKeepsDurableOrigin|TestRepairCheckPermission)$'`; whole-package runs use `go test -count=1 -json -timeout 30m ./pkg/api`. All four exit0, with no fail event or COMMIT-unconfirmed/deadline error output. Each revision adds396 successful original leaf outcomes (11×36) under actual CPU throttling. The original MaxWaitMs0/overall1second behavior and every assertion remain unchanged.

Actual throttling proves the quota was effective. It does not establish historical CPU-throttling causality or exclude a regression. The constrained package did not run concurrently with other full-suite packages. Since no failing boundary appeared, no additional observer, GOMAXPROCS4 comparison, speculative scheduling fix, or repeated pressure loop was added.

The requested short observer check ran first, from canonical `/work` with no overlay, canonical HOME, TMPDIR unset, and umask022: `go test -count=1 ./tools/measure-exit/dryrun` still fails in14.996seconds, reporting29failed checks. Samples lack the expected labelled tmux sessions. Expected socket paths in this run are54–55bytes. The prior long-TMPDIR hypothesis is **not supported**. This separate failure remains unclassified and was not fixed within deadline scope; fullgreen remains unresolved.

Diagnostic status is blocked on capturing an original failure. Historical root cause, evidence-backed fix, deterministic RED/GREEN, canonical fullgreen, and commit remain incomplete for the caller. No questions or ticket-status updates. All changed files are diagnostic artifacts in this directory.

See `FINAL-report.json`, exact runners, raw `.jsonl`/stderr/sandbox logs, environment before/after, source verification, and artifact manifest. No further unbounded iteration is proposed.
