# Independent pc.01 correction revalidation

Task: `t2.jf1.r8.a8`. Base HEAD: `7b6c2e20a9c73b82148833b8bef52dde9e2c255b` on `feature/b.ftd`.
No ticket statuses changed, no commits, and no source, test, or testplan edits.
Only this directory's evidence files were written. The four existing tracked
modifications are captured in `reviewed.patch`; `reviewed-files.sha256` matched
all four files again after the runs.

## Results

1. `make sandbox CMD='go test ./pkg/api -run "^TestDecideKeepsDurableOrigin$/^cursor$" -count=5 -v'`
   exited **0**. Exact Cursor subtest: **5 pass, 0 fail**; package 0.173s.
   Individual subtests took 0.03–0.04s. Receipt: `targeted-count5.log`.
2. One subsequent **bare** `make test-sandbox` exited **0**.
   Go: **68 packages passed, 13 no-test packages, 0 failed**. Of the 68 passed
   packages, **64 were cached and 4 ran fresh**. `pkg/api` ran fresh and passed
   in **143.481s**. Bun: **430 pass, 0 fail**, **1,662 expectations**, **57 files**,
   **14.38s**. Receipt: `test-sandbox.log`.

Normal test deadlines were retained; the full target used its configured
`go test -timeout 30m ./...`. No retries beyond the requested five targeted
iterations and one full rerun were performed. Inherited `AGENT_DIRECTOR_*`,
`TMUX`, `TMUX_PANE`, and `GIT_CONFIG_GLOBAL` were removed before each make
invocation. No masked or temporary host HOME was introduced. The sandbox
supplied its normal isolated container HOME and sandbox marker.

## First failure disposition

The first full-run failure remains recorded in `../test-sandbox.log` and the
original handoff `../report.md`: `TestDecideKeepsDurableOrigin/cursor`,
`pkg/api/decide_delivery_test.go:595`, reported
`store: commit outcome unconfirmed; read the request before retrying: context deadline exceeded`.
That first run had **67 Go packages passed, 13 no-test packages, 1 failed**;
Bun had **430 pass, 0 fail**.

The failure **did not reproduce** in five isolated subtest iterations or the
single authorized full rerun. Its cause is **unknown**. Passing reruns do not
prove resolution, establish a root cause, or justify claiming a fix for that
deadline failure. In particular, the isolated run does not reproduce full-suite
contention, and most other Go package results in the full rerun were cached.

## Four-file review

Reviewed the existing diff in `test/Dockerfile`,
`test/driver/cursor-pane-controls.sh`, and the pc collector/case testplans.
No obvious offline-runtime or provenance blocker was found: dependency
resolution/build/declaration emission/pack/install occur during image build;
the runtime helper compares the source manifest, copies the installed consumer,
checks JavaScript and declarations, and retains source and artifact hashes.
The manifest includes the shared Go error catalogue and enumerated source files.
The existing public-client composition assertions remain in place.

This was read-only review and sandbox unit validation, not execution of pc.01
or proof of a newly built exact-image runtime. The existing network-none narrow
packaging receipt is documented in the original handoff and was not repeated.
No Docker integration group, helper, collector, test image, TLC command, real
Cursor, credentials, login/logout, host tests, or host product binaries were
invoked by this validator. The normal full sandbox target ran its configured
coverage. `git diff --check` passed before the suite.

## Remaining work and questions

The external scheduler's exact committed-image gate remains pending, as in the
original handoff. Its historical group/case results are not superseded by this
unit rerun. No implementation changes or further local reruns are authorized or
needed for this dispatch. No questions for the caller; no deviations.
