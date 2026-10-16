# TestDryRun UTF-8 fix

The missing `-u` in `invoker.tmuxCall` caused the separate dry-run session identification failure under `LC_ALL=C`. Adding only `-u` through a source overlay eliminated all 29 dryrun.sh failed checks and made every behavioral TestDryRun subtest pass. The overall unchanged TestDryRun is still RED because its isolation audit hardcodes the previous tmux argument offsets. No whole-test GREEN is claimed.

## Causal comparison

Both executions used exactly `go test ./tools/measure-exit/dryrun -run '^TestDryRun$' -count=1 -v`, inside separate fresh `make sandbox CMD='bash .../run.sh MODE'` containers. HOME=/home/sandbox; TMPDIR/TMUX/TMUX_PANE unset; umask022; inherited environment cleared using env -i; only AGENT_DIRECTOR_TEST_SANDBOX=1 retained; run-local GIT_CONFIG_GLOBAL. See exact runner and environment logs. No assertions, test sources, deadlines, retries or SQL changed.

- Original canonical source, no GOFLAGS: exit1, TestDryRun FAIL in14.719s; dryrun.sh reports29failed checks. Existing retained raw-capture evidence shows correct session label values with `_` replacing the requested tab delimiter.
- Overlay replacing only runlog.go's argv prefix `[tmux, -S, socket]` with `[tmux, -u, -S, socket]`: exit1, TestDryRun113.376s. All six behavioral subtests PASS: banner, every_case_completed_every_sample, RN-9_and_RN-7, probe_with_both_stubs, a_non-stub_claude_is_refused_before_any_spawn, no_credential_reached_any_output. Only isolation FAILS. Its emitted failures are all from assertRunLog: it expects Argv[1]==-S, socket at[2], verb at[3]; the new argv is explicitly shown in overlay.test.log with -u then -S then a private socket. No29-check failure remains.
- Canonical source fix, no overlay: targeted `go test ./tools/measure-exit -run '^(TestRunLogRecordsArgvNeverEnvironment|TestSessionGone)$' -count=1 -v` PASS, exit0,0.004s; all four session-presence/error cases and logging/socket refusal pass. git diff --check passed.

## Canonical change

Only tools/measure-exit/runlog.go changed: add -u before -S, matching internal/tmux/invoke.go's UTF-8 mode, and explain the locale/format-delimiter requirement in its function comment. Canonical behavior equals the successful behavioral overlay; the canonical copy adds only the explanatory comment. Tests/docs remain untouched. No ticket statuses or commits changed.

## Handoff / incomplete

Test Writer must update the dry-run isolation audit to validate the new -u/-S prefix and correct private socket/verb positions without relaxing isolation or action checks. Review tools/measure-exit/capture_test.go's exact argv/position-dependent expectations too. Full unit suite and final whole-TestDryRun GREEN remain with Test Writer; no full suite was run here.

The historical COMMIT deadline failure remains UNKNOWN and unreproduced. This harness fix does not solve or explain that original blocker. No deadline, SQL, cleanup, timeout or retry change was made. No spec/tla inputs changed; TLA+ not applicable. No questions.

No .claude files were read, copied or hashed, including generated ones; no real AD stores, credentials, login, scheduler, heavy Docker group, direct dryrun helper or host repository execution was accessed. Evidence captures only this directory's runner, explicit source snapshots and test stdout/stderr/environment receipts, never whole HOME/tmp archives.

## Hashes and artifacts

Original runlog.go SHA256: c47f3bb268ee0d4460c7b7b340b63f13e2f34b115f96db1638402811cafaf9d8
Overlay runlog.go SHA256: 604f9e6b25ca4bbee9c1f20647bb90c3f69feffa2a01af86179454505911de0e
Canonical runlog.go SHA256: 5f65a3e746d5dfef77fa6b4b15edc984d48ebba9c1d6403dea7e882b2be8c136
Unchanged dryrun_test.go SHA256: df8da1a6da33f6e1485ff8da4677a046e07f7f90ab475745e1d23ac6bdeeb992

provenance.json holds full before/after hashes. overlay.patch is the one-option causal change; canonical.patch is the final reviewable diff. Original/overlay/unit .test.log, .test.exit, .environment.log and .sandbox.log files retain exact receipts. artifact-manifest.json hashes explicit evidence files only.
