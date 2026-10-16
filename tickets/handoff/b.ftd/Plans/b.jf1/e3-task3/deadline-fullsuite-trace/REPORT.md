# Complete Go suite deadline trace

Historical API COMMIT deadline failures were **not reproduced**. No canonical source, assertion, deadline, test, status, or commit changed. Two full Go diagnostics ran sequentially in the sandbox, from canonical `/work`, with the existing instrumentation copied into Go overlays plus the exact modernc dependency overlay. Original36 target cases passed twice, including the four historical cases.

| Run | Command | Package results | API seconds | Original cases |
|---|---|---|---:|---:|
|01 natural|`go test -json -timeout 30m ./...`|64pass /13skip /4fail|173.867|36/36pass|
|02 corrected|`go test -count=1 -json -timeout 30m ./...`|67pass /13skip /1fail|186.159|36/36pass|

Bun install/build prerequisites ran once before01. No integration suite or separate helper/collector workload was launched.

01 exposed observer/environment problems: Go1.22 vet cannot open a newly added virtual source file; masked temporary HOME disagrees with the passwd HOME used by tilde expansion; process-wide umask077 makes an intentionally public transcript fixture private.02 fixes these using declarations merged into an existing copied store.go, root-authorized canonical fresh-container HOME=/home/sandbox, and umask022 with only raw logs precreated0600. Assertions and vet remain enabled. Both runs also fail `tools/measure-exit/dryrun TestDryRun`: every sample lacks its expected labelled tmux session. That failure persists after the observer corrections; its cause remains unclassified. The full suite is **not green**.

Corrected02 API telemetry contains21,593records. Native COMMIT boundaries are complete:59bounded +4,541unbounded entries/exits, with zero API driver COMMIT errors and zero API outer-unconfirmed guards. Global store telemetry also contains intentional constraint failures and a synthetic uncooperative-boundary guard; their associated store tests pass. Those are not historical reproductions.

|02 API bounded phase|count|max ms|
|---|---:|---:|
|actual native COMMIT|59|166.725|
|result publication|454|0.121|
|cleanup|454|92.623|
|worker launch scheduling|462|0.015|

Minimum remaining budget before bounded COMMIT is201.858ms. Its correlated call entered with999.996ms, spent approximately798ms in reads/BEGIN/transaction SQL, then completed native COMMIT in4.631ms. Publication precedes92.623ms cleanup. The later trail emission takes the successful call past1second. This is measured budget accounting, not a cause of historical COMMIT failure. See `least-budget-api-call.json`.

Both runs have contiguous observed per-process sequence prefixes and observed drop counters0 across64,534/75,993records. The asynchronous recorder has no exit flush, so unobserved final-tail loss is not ruled out. Per-process0600 sinks preserve product stdout/stderr.02 adds executable identity;01's main API process is inferred from its unique maximum Decide-entry count. The recorder creates empty child-process files, which adds observer overhead.

Correct API leaf counts are9,587passes/50skip in each run. Prior fullcontext's next-sorted-name heuristic counted two ancestor tests as leaves; this analysis excludes every proper slash-prefix ancestor. All original assertions remain intact.

The sandbox reports192CPUs, `cpu.max=max 100000`, and no cgroup throttling. Host PSI totals cannot assign a cause to an individual call. No evidence distinguishes SQL I/O from descheduling inside native work.

The next minimal causal capture should add synthetic RequestToken/InstanceID plus caller location to the existing Decide-entry correlation, then record per-API-process runtime scheduling and native syscall boundaries with fd/database identity when a bounded call has little budget before COMMIT or hits the outer guard. The successful798ms pre-COMMIT chain shows why that distinction matters. Another arbitrary pressure run or a deadline/retry change cannot establish a defect. Once the failing boundary is captured, inject that exact delay into the existing native-boundary harness for deterministic RED/GREEN.

See `FINAL-report.json`, `measurement-summary.json`, original logs, both fixed runner scripts, overlay patches/hashes/provenance, environment snapshots, and artifact manifest. Root cause, an evidence-backed product fix, canonical fullgreen, and local commit remain unresolved for the caller. Python analysis was unavailable in the sandbox; the retained Bun analysis completed successfully.
