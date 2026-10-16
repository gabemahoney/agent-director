# Final dry-run validation

Task t2.jf1.r8.a8 / Epic t1.jf1.r8 / Plan b.jf1. No ticket statuses or commits changed. No callable Bee tools or local Task ticket files were available; dispatch defined the testing scope. No further questions.

## Test changes

Only four test files changed by this lane:
- tools/measure-exit/capture_test.go: exact UTF-8 argv expectation; socket/verb positions in pane-capture fixtures; exact observed four-token error label updated to `tmux -u -S <socket>`.
- tools/measure-exit/dryrun/dryrun_test.go: require at least five argv tokens, exact `-u -S`, the same private TMUX_TMPDIR socket prefix, and the verb at its new position. All behavioral, namespace, ownership, identifier, credential, and allowed-action assertions remain.
- tools/measure-exit/poll_test.go and tools/measure-exit/sampling_test.go: existing tmux call expectations additionally require `-u`.

No source/docs changed by this lane. Engineer's pre-existing tools/measure-exit/runlog.go fix remained untouched. No new mirrored flag-only test; existing end-to-end TestDryRun provides regression coverage. No skipped checks, retries, deadline changes, error suppression or changed API assertions/budgets. Reviewed shared fakeExec/world fixtures and relevant capture/poll/sampling/dryrun tests against docs/test-writing-guide.md including checklist/red flags. git diff --check passed.

## Results

Prior causal RED evidence is in ../dryrun-utf8-fix/REPORT.md: original TestDryRun failed 29 checks in14.719s; source-only -u overlay passed every behavioral subtest but failed the stale fixed-offset isolation audit. Those prior runs were not repeated here.

1. Fresh `go test ./tools/measure-exit -count=1 -v`: PASS,13.262s, exit0.
2. Fresh `go test ./tools/measure-exit/dryrun -run "^TestDryRun$" -count=1 -v`: PASS,113.276s, exit0. All seven original subtests PASS, including isolation and credentials.
3. ONE bare `make test-sandbox SANDBOX_FLAGS="--env GIT_CONFIG_GLOBAL=/work/tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/dryrun-final-validation/gitconfig"` (no CMD): PASS, make exit0; Go exit0 and Bun exit0. The unchanged Makefile accumulates any Go/Bun nonzero into rc=1, so its zero exit proves both zero. Bun430 pass,0 fail,1662 expect calls across57 files,14.86s.

Full Go results: pkg/api actually executed142.701s; tools/measure-exit15.107s; tools/measure-exit/dryrun120.408s. Full package inventory with cached vs executed results is in full.results.json. API was not cached, so no additional original-target execution was needed or run. Full suite was run only once.

## Environment and scope

Every execution/build/test used make sandbox or make test-sandbox with a fresh standard sandbox container, canonical HOME=/home/sandbox, umask022, TMPDIR/TMUX/TMUX_PANE unset, inherited AGENT_DIRECTOR_* stripped (only guard injected), and empty run-local gitconfig mode0600. Targeted runner uses env -i; full target uses the standard fresh image and explicit gitconfig flag. No direct dryrun.sh invocation by this lane, host repo execution, real AD stores, credential/login/token operation, .claude reads/copies/hashes, heavy Docker/testplan/helper/collector/TLC/Java/scheduler/liveCursor execution. Unit tests may exercise their own disposable fixture stubs.

No integration group was run: this was the dispatched settled unit Test Writer/full make test-sandbox run; heavy integration groups were expressly forbidden. Root CLAUDE.md documents make test-docker EPIC=<slug> for integration groups.

## Separate unresolved blocker

The historical COMMIT deadline failure remains UNKNOWN and unreproduced. pkg/api passed afresh here. The dry-run UTF-8 fix has causal RED/GREEN evidence only for the separate measurement harness problem; it does not explain or fix the original COMMIT blocker. No source-causal fix/RED is claimed for that blocker.

Evidence includes exact runner, environment receipts, raw logs/exits (mode0600), tests.patch, provenance.json, and package inventory. No archive was created. This lane changed no source, documentation, ticket state or commit.
