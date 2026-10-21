---
name: run-tests
description: Run this repo's tests and any built binary, never on the host. Test suites (unit and integration) run in an isolated job per docs/engineering-guide.md "Build and test commands"; light one-off commands run in `make sandbox CMD="…"`. Host execution can silently rewrite the real agent-director store (the b.8dr incident). Use when asked to run tests, `go test`, `bun test`, `go run`, or to execute a built binary in the agent-director repo.
---

# Running tests (and any built artifact) for agent-director

**Edit on the host freely. Never execute on the host.** Tests, `go run`,
`go generate`, bun scripts and built binaries can rewrite the real
agent-director store (the b.8dr incident). Host `go test` / `bun test` /
`go run` / `./bin/…` are denied by `.claude/settings.json`; don't work around
it. Don't set `AGENT_DIRECTOR_TEST_SANDBOX` or
`BYPASS_CONTAINER_FOR_AGENT_DIRECTOR_TESTS` by hand on this machine.

## Where things run

Follow docs/engineering-guide.md "Build and test commands" for every command
and recipe:

- **Test suites** (unit and integration): run them in an isolated job, never on
  this host. Your host's own instructions say how to submit one.
- **Light one-off commands** (build, lint, generate): `make sandbox CMD="…"`.
  `CMD` holds one plain command, with no `&&`, `;`, pipes, `$(…)` or backticks.
  Anything longer goes in a throwaway script, as the guide says.

## Reading the results

`jobsched results <job ID>` prints the paths of the job's `build.log`,
`run.log`, `result.json` and `container-inspect.json` (`--json` for one JSON
document). Read `run.log` for test output and `build.log` if the image build
failed.

- **The verdict line is authoritative.** A unit-test job ends `run.log` with
  `VERDICT: PASS` or `VERDICT: FAIL`. A missing verdict means the job broke
  before finishing; check `build.log` and `result.json`.
- Go prints one `ok`/`FAIL` line per package; bun ends with `N pass / M fail`.
- `panic: test timed out after 30m0s` is a hung test in that package, not a
  slow host; the `running tests:` lines under it name the tests still running.
- **Known flake:** one timing-sensitive bun serialization test (see
  docs/engineering-guide.md "Sandboxed execution" → "Known caveats"). Treat any
  other failure as a real signal from the change under review.
- **Known-broken advice skips are expected:** tests in
  `test/install-sh/advice_follow.sh` skip with "b.fji `<ID>`: advice does not
  work as written …". See docs/test-writing-guide.md "Literal-follow tests for
  error advice".
- **The `test/smoke/go` canary is not a known failure.** It guards against
  tests writing to the real store; if it fires, treat it as a leak regression.
