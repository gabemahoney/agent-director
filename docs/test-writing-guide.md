# Test Writing Guide

## Purpose

How to write unit and integration tests in this repo. The overarching goal: keep the test suite small and fast while maintaining roughly 80% coverage. Don't gold-plate — every test costs maintenance, and a bloated suite is harder to read than a missing test.

## Core Principles

- **Test behavior, not implementation.** Verify what a function produces or causes, not how it works internally.
- **Parametrize over duplicate.** If three or more tests share the same shape with different inputs, collapse them with your test framework's parameterization primitive.
- **Share setup.** Repeated boilerplate at the top of every test belongs in a fixture or helper, not in each test body.
- **Factories over inline data.** Build test data through small factory functions with sensible defaults — let each test override only what it cares about.
- **Mock sparingly.** More than ~3 mocks in a single test usually means you're testing implementation. Prefer real filesystems, real data structures, and real function calls; mock only at true external boundaries (network, third-party services, expensive operations, error injection).
- **No meta-tests.** Don't test fixtures or helpers. If they break, every real test using them will fail — that's your signal.
- **Coverage, not completeness.** Aim for ~80%. You don't have to test every permutation. Pick the cases that catch real bugs.

## Pre-Submit Checklist

Before opening a test PR (or marking a task done):

- [ ] **Parameterized** where 3+ cases share structure with different inputs.
- [ ] **Uses shared fixtures / factories** instead of copy-pasted setup blocks.
- [ ] **No unused imports.** Run the linter; clean up dead imports.
- [ ] **No trivial constants.** Simple domain strings like `"open"` are inlined, not wrapped in a `STATUS_OPEN` constant. Constants are only worth it for complex formats or values used in 5+ places.
- [ ] **Tests behavior.** Minimal mocking (< 3 mocks per test); asserts on observable outputs, not internal calls.
- [ ] **No meta-tests** of fixtures, helpers, or test infrastructure.
- [ ] **Docstrings are concise.** 1-2 lines max for fixture and test docstrings. Long-form explanations belong in a dedicated testing doc, not inside the code.
- [ ] **Follows project patterns.** New test files match the structure of existing ones.

## Red Flags

If a test or test file exhibits any of these, stop and refactor before merging.

### Test file > 500 lines without parameterization

Likely contains copy-paste functions that differ only in input values.
**Fix:** parameterize.

### More than 3 mocks in a single test

You're testing how the code works, not what it produces. The test will break on harmless refactors.
**Fix:** drop the mocks and test at integration level with real I/O.

### Copy-pasted setup boilerplate

If the first 10-20 lines of multiple tests are identical, that's a fixture.

**Bad:**
```
def test_something():
    # 15 lines of identical setup
    ...
```

**Fix:** extract a fixture that returns the prepared state.

### Imports many constants, uses few

Mass-importing constants you don't use pollutes the namespace and hides real dependencies.
**Fix:** import only what each test uses.

### Tests that verify internal implementation

**Bad:**
```
@patch('builtins.open')
def test_writes_with_utf8(mock_open):
    create_record(...)
    mock_open.assert_called_with(..., encoding='utf-8')
```

**Fix:** assert that the resulting file contains the expected content. The encoding is an implementation detail.

### Meta-tests

**Bad:** A test that asserts a fixture set up the directories it claimed to set up.
**Fix:** delete it. If the fixture is broken, every real test will tell you.

### Verbose inline test data

Repeating a 15-line YAML/JSON/struct literal across many tests.
**Fix:** introduce a factory function (`make_record(**overrides)`) with defaults.

### Constants wrapping simple strings

**Bad:** `STATUS_OPEN = "open"` then `assert record.status == STATUS_OPEN`.
**Fix:** inline the string. Constants are for things that are complex, fragile, or reused in many places — not for stable single-word domain values.

## Best Practices

### Parameterize aggressively

When three or more tests share structure with different inputs, collapse them with your framework's parameterization primitive. Use descriptive case IDs so a failure tells you which case failed without reading the inputs.

### Use shared fixtures and data factories

- **Fixtures** for setup state that multiple tests need (a configured project, a tmp directory with seeded files, a fake server).
- **Factories** for constructing test objects with sensible defaults and per-test overrides.

If you find yourself copying setup or data construction between tests, extract it.

### Test behavior, not implementation

**Good** — asserts on what's observable:
```
def test_create_record_writes_file():
    record_id = create_record(title="Test")
    path = output_dir / f"{record_id}.md"
    assert path.exists()
    assert "title: Test" in path.read_text()
```

**Bad** — asserts on internal calls:
```
@patch('builtins.open')
def test_create_record_uses_utf8(mock_open):
    create_record(title="Test")
    mock_open.assert_called_with(..., encoding='utf-8')
```

**When to mock:**
- External network services and third-party APIs
- Expensive operations in fast unit tests
- Error injection for testing error paths

**When NOT to mock:**
- The filesystem (use a temp directory)
- Your own pure functions
- Validation logic — test it directly

### Keep it DRY

- Repeated setup → fixture.
- Repeated object construction → factory.
- Repeated assertion logic → helper assertion function.
- If you copy-paste in tests, extract it.

### Test isolation: tmux session names

Any test that exercises a verb which creates a tmux session (e.g. `resume`) must
use a UUID-suffixed instance id — e.g. `` `id-resume-${crypto.randomUUID().slice(0, 8)}` `` — rather than a fixed string like `id-resume-1`.
Fixed names collide across runs when the fake-tmux stub is bypassed (e.g. mode-644 binary) and a real tmux session leaks: a leaked session labelled for that fixed id then makes later runs refuse. If the leak is in the same store, a plain spawn's label scan refuses with `ErrTmuxSessionConflict` "left over from an earlier life", and `resume` of a finished row seeded by `apitest.SeedSpawn` (a launch token, no tmux server or pane) refuses with "this id's own abandoned launch": `ErrTmuxUnresponsive` while the leak is younger than `starting_session_seconds` (default 300 s), otherwise `ErrTmuxSessionConflict`. If the run uses a fresh test HOME (so another store's id), the session reads as `ErrTmuxSessionConflict` "another agent-director store": `resume` refuses at its pre-launch lookup, and a plain spawn hits it only after "duplicate session".

### Parallel mode is pinned off (bun)

This is about the bun suite. Go tests in `pkg/api` run in parallel; see
"pkg/api tests: parallel or serial" below.

The `/release` skill invokes `bun test --parallel=1` as the coverage gate because the suite deadlocks when bun runs files concurrently (tracked in b.w7e — parallel `make build` invocations from `test/setup.ts` preload race into `ETXTBSY` on the shared `bin/agent-director`). Until that root cause is fixed, **assume your tests run sequentially across files**. If you write a test that *requires* parallelism for correctness — don't. Order across files is deterministic but unspecified; couple state to per-test fixtures, not run order. The bunfig key `parallel = 1` is forward-looking and ignored by bun 1.3.13; when bun honors it, both invocations (release-time and ad-hoc `bun test`) will pick it up.

### Bun tests that read `dist/`

`make test-sandbox` builds `pkg/ts-bun-client/dist/` before either suite
(b.2b3), so no test may rely on another test file having built it. A bun test
that reads or packs the live `pkg/ts-bun-client/dist/` without building it
itself calls `requireBuiltDist()` from
`pkg/ts-bun-client/test/internal/builtDist.ts` first: a missing build then fails
with a message naming the missing files, not an ENOENT or a short tarball list.
Use it rather than writing your own check. A test that builds the output it
reads does not need it: `public-surface.test.ts` emits its own `.d.ts` files
and `release-version-coherence.test.ts` builds in a staged copy. To run a test
that calls `requireBuiltDist()` alone, install and build first, in the sandbox:

```sh
make sandbox CMD='cd pkg/ts-bun-client && bun install --frozen-lockfile && bun run build && bun test test/packaging.test.ts'
```

### Documentation belongs in docs

Fixture docstrings stay 1-2 lines. Test docstrings stay 1-2 lines. Long-form explanations of test architecture, fixture selection, or mocking strategy go in a dedicated testing doc — not buried inside the code.

## Quick Reference

- **New test?** Read the file docstrings of nearby tests first to understand placement and conventions.
- **New fixture?** Check existing fixtures first — don't duplicate.
- **New helper?** Check existing helpers first — don't duplicate.
- **File over 500 lines?** Parameterize or split by feature.
- **Mocking more than 3 things?** Test at integration level instead.

## pkg/api/apitest Seed* factory contract

Post-E1 (b.wvr), seed helpers for pkg/api tests live in `pkg/api/apitest/`
under the standard build tag — no `//go:build helper` gate. New tests that
need to populate a SQLite store with realistic spawn/permission/template
state MUST use these factories rather than re-implementing seeding inline.

Exported factories (in pkg/api/apitest/seeds.go):
- `SeedSpawn(dbPath, id, state, cwd, relayMode, sessionID string, createStore bool, opts ...SpawnOption) (string, error)`
  — the `With…` options in pkg/api/apitest/options.go (e.g. `WithLaunchIdentity`,
  `WithLaunchStartedAt`, `WithNoLaunchToken`) set further spawn columns.
- `SeedParentChild(dbPath, parentID, childID string) error`
- `SeedPermissionRequest(dbPath, spawnID, toolName string) (PermissionRequestSeed, error)`
- `SeedTemplate(templatesDir, name, body string) (string, error)`
- `InitStore(dbPath string) (string, error)`

Default behavior on empty arguments (e.g. empty id → generated UUID, empty
state → "waiting", empty cwd → "/tmp") is documented in seeds.go and
pinned by `TestSeedSpawn_Defaults` (pkg/api/apitest/seeds_test.go).

Why this matters (SR-19.2): re-implementing seeding inline tends to drift
from the store schema and fixture conventions, masking regressions. The
shared factories are exercised by every consumer, so any schema break is
caught early.

## pkg/api tests: parallel or serial

Every top-level test in `pkg/api` starts with `t.Parallel()` or, as the first
line of its body, a `// Serial: <reason>.` comment that says why it cannot run
in parallel. `TestEveryTestDeclaresParallelOrSerial`
(`pkg/api/parallel_declared_test.go`) fails any test that has neither (b.yo5).

**Default to parallel.** A parallel test owns everything it touches: its own
store in `t.TempDir()`, its own tmux fake (a `tmuxfix.Recorder`, as
`newKillEnv` and `newResumeEnv` build), its own clock and random row ids
(`uuid.NewString()`). It reads the trail only for its own ids. It uses the
environment `TestMain` sets and changes none of it.

**Stay serial when the test:**

- sets the environment: `t.Setenv` of `HOME`, `TMUX`, `TMUX_TMPDIR`,
  `AGENT_DIRECTOR_INSTANCE_ID`, `CLAUDE_CONFIG_DIR`, the locale variables and
  so on, or `os.Setenv`. The environment is process-wide. `t.Setenv` panics in
  a parallel test; `os.Setenv` does not, so nothing catches that mistake.
- checks every trail record (or every record of one event) written since a
  mark, such as `assertWroteNothing` and its wrappers, `assertExpired`, or a
  scan for a forbidden value. Records that a parallel test writes in the
  meantime would show up in the check.
- checks the trail by fixed row ids that other tests also use, such as the
  find-missing tests' literal row ids.
- changes other process-wide state: `api.SetPauseTestKnobs` (through
  `fastPausePolls` or `endAtFirstWait`), `log.SetOutput`, or the working
  directory (`cwdfix.Temp`).
- removes the default socket's directory, changes its mode, or uses
  `test/fake-tmux`, which keeps its table beside the socket. Such a test takes
  its own `TMUX_TMPDIR` before seeding, with `e.ownSocketDir(t)` on the kill
  fixture or `useOwnTmuxTmpdir(t)`. Both call `t.Setenv`, so the test is
  serial. Never change `TestMain`'s shared directory.

The reason names which of these applies, e.g. `// Serial: it sets HOME with
t.Setenv.` or `// Serial: it checks every record written to the shared trail
since its mark.` Go starts the parallel top-level tests only after every
serial one has finished, so a serial test never overlaps a parallel one.

**Tables with a few serial cells.** Keep the top-level test serial and call
`t.Parallel()` inside each cell that qualifies. The serial cells run in the
parent's body. The parallel cells start together after it returns. The
comment ends by saying which cells run in parallel. Examples:
`TestCallTable` (a verb's `callTableVerb.serial` or a column's
`callTableColumn.ownTmuxTmpdir` keeps its cells serial) and
`TestSecuritySecretAndOtherRowID` (`securityVerb.serial`).

**Parallel subtests share what they capture.** A parallel subtest must not
write to its table entry or to any variable it shares with sibling subtests,
such as an outer loop's `tc`. Copy it into a local first (`spec := tc.spec`)
and change the copy; otherwise `-race` reports a data race.

**What `TestMain` sets.** `HOME` (fixing the trail file), `TMUX` unset,
`AGENT_DIRECTOR_INSTANCE_ID` unset, and one `TMUX_TMPDIR` for the whole test
binary (`apiTmuxTmpdir`, with its per-user socket directory at mode 0700).
`newKillEnv` and `newResumeEnv` use these values and set nothing, so their
tests can run in parallel. A fixture that needs one of these values in place
uses `setenvIfChanged` or `unsetenvIfSet`. They call `t.Setenv` only when the
current value differs, which happens only in a test that is already serial.

**The trail reader.** `readAPITrailLines` keeps the lines it has parsed and
reads only the lines appended since its last call, under a lock that parallel
tests share. The returned lines are shared, so do not modify them. No test
may truncate, replace or remove the trail file. If one does, the reader fails
the test.

## Literal-follow tests for error advice

An error description, manifest text or `install.sh` message that tells the
caller what to do next ("retry later", "do not retry until get shows the row
ended or missing", "retry kill later", "spawn again with the reuse opt-in")
needs a test that follows that advice (b.fji). Checking that the description
contains the advice is not enough: Epic 13 found a "retry later" whose plain
retry was certain to collide. When you add or change such a text, add or update
its test. The test:

1. triggers the error;
2. checks the advice phrase word for word (`strings.Contains` on the exact
   phrase, or `apitest.AssertDescription` where a `Desc*` case already pins
   it), so a change to the advice turns the test red and someone re-checks the
   follow;
3. does what the advice says, literally, as an automated caller would: the
   same verb and parameters, after the stated wait or check (advance the
   injected clock, run `find-missing` or `get` when told);
4. checks that the step succeeds, or that the promised outcome happens: the
   row reads `ended` or `missing` after the stated `find-missing`, or the same
   refusal comes back unchanged while the condition holds and the operation
   succeeds once it clears.

A human-only pointer (the README's "Operator actions") is not itself followed,
but an `ErrTmuxSessionConflict` refusal that carries one gets a
condition-clears test (`TestAdviceFollow_HO<n>_…` in
`pkg/api/advice_follow_conflict_test.go`): re-issued while the condition
holds, the call returns the same refusal unchanged and writes or sends nothing;
re-issued once the condition is gone (a human cleared it, or the holder
exited), it does its work. A caller that re-checks later relies on both.
Where "Operator actions" gives the human an operator-tool command, the test
clears the condition by running that command as the human would:
`adviceOperatorEnds` runs `agent-director-admin kill-finished` (through
`adminapi.KillFinished`) and checks that it succeeds with `kill_sent` true,
that the named sessions are gone and that the row is unchanged.

**Where they live.** `advice_follow_<area>_test.go` files in the package whose
surface gives the advice: `pkg/api` (fake tmux and the injected clock; prefer
this tier), `cmd/agent-director` (CLI-only texts and flag spellings, against the
built binary), `internal/mcp` (MCP-only texts and parameter spellings),
`internal/config`, and `test/realtmux` where only real tmux shows the outcome.
The TS client's own advice is in `pkg/ts-bun-client/test/adviceFollow.test.ts`,
and `install.sh`'s in `test/install-sh/advice_follow.sh`, which
`test/install-sh/advice_follow_test.go` runs under `go test` (one subtest per
`test_*`). In `pkg/api`, reuse the shared helpers in
`advice_follow_helpers_test.go` rather than writing new ones:
`adviceAssertAdvice`, `adviceAssertPhrase`, `adviceAssertGoDoc` and
`adviceAssertManifest` for the word-for-word checks, `adviceAwaitFinished` for
the pending-row wait, `adviceOnceAfter` for a one-shot hook on a tmux call,
`adviceOperatorEnds` for a human's `kill-finished`, and the small seeds beside
them.

**Naming.** `TestAdviceFollow_<ID>_<Short>`, e.g.
`TestAdviceFollow_A2_ScanUnreadableRetryLater`, with a one-line comment giving
the ID and the quoted advice (`test_<ID>_<Short>` in `advice_follow.sh`; the TS
test titles start with the ID). The ID is an area prefix and a number. The
prefixes: A spawn and reuse, B resume, C kill (and `agent-director-admin
kill-finished`), D find-missing, E pane verbs, F list, get, expire and decide
(and `agent-director-admin delete`), G config, store and migration,
H CLI-only, I MCP-only, J `install.sh`, K TS client, HO human-only pointers.
Tests that follow the same advice share its ID. IDs are numbered within the
repo: new advice takes the number after the highest one in use for its prefix
(do not fill gaps), found by grepping the existing names, e.g. for kill
`grep -rhoE 'TestAdviceFollow_C[0-9]+|test_C[0-9]+' . | sort -uV`. (The
numbering started from b.fji's advice inventory, kept with that bug's ticket.)

**Advice that does not work as written** is a product bug in the text or the
behaviour; fix whichever is wrong. Only `advice_follow.sh` has a gate for such
a test; the Go and TS literal-follow tests have none. In `advice_follow.sh`,
until the fix lands, keep the test asserting that the advice works and put
`known_broken <id> "<why>" || return` just before the step that fails. It skips
the rest of the test with "b.fji `<id>`: advice does not work as written …"
unless `AGENT_DIRECTOR_RUN_KNOWN_BROKEN_ADVICE=1` is set, which runs it so you
can see it fail. Delete the call when the bug is fixed.

**Running them.** `make test-sandbox` runs them all, the `install.sh` script
included, with any `known_broken` test in it skipped.
`make test-install-sh-advice` runs the `install.sh` script alone (it refuses to
run outside the sandbox). To run the `known_broken` tests and see them fail:

```sh
make sandbox CMD="env AGENT_DIRECTOR_RUN_KNOWN_BROKEN_ADVICE=1 bash test/install-sh/advice_follow.sh"
```

## Synthetic-regression test convention

Synthetic-regression tests re-anchor known failure-classes so they can
never silently regress. Each test class lives in its own directory under
`skills/release-agent-director/tests/synthetic-regressions/`:

```
skills/release-agent-director/tests/synthetic-regressions/
  <class-name>/
    <class>_test.go
```

Each directory is its own Go package. A separate `go.mod` is **not**
required — sibling `_test.go` files under one directory share the
same package declaration. Do not add one: `./...` skips a directory that
holds its own `go.mod`, so the test would drop out of `go test ./...`.

**Never write the shared worktree.** `go test ./...` runs every package in
parallel against one worktree, and other packages parse, compile and pack it
while a test runs. A test must never rewrite a tracked file, not even briefly
with a `t.Cleanup()` restore: another package can read it half-written (b.jct).
Copy what the test needs into `t.TempDir()` and change the copy, as
`helper-tag-replay` (a copy of the Go module) and `coverage-bun-test-fires` (a
fixture package) do. The same applies to build and install output that other
packages read: a build, install or pack the test runs writes under
`t.TempDir()` (e.g. `make release-binaries` with `RELEASE_DIST_DIR`,
`pack-first.sh` with `PACK_OUTPUT_DIR`), never into the real tree's `bin/`,
`pkg/ts-bun-client/dist/` or `node_modules/`.

**Mandatory cleanup.** A test that must add untracked paths to the real tree
(a fixture a gate scans for) gives them unique names and registers a
`t.Cleanup()` that removes them before it asserts anything, so a failing test
leaves nothing behind. It holds the tree-write lock
(`.tree-write.lock` at the repo root, `syscall.Flock` with `LOCK_EX`) from
before it creates the paths until they are removed, so docker build-context
collectors such as the `coverage.docker-epic-*` gates never see the paths
appear or vanish. A test that also needs the dist-pack lock below takes that
lock first: nothing may take the dist-pack lock while holding the tree-write
lock.
`worktree-pollution` adds a path without the tree-write lock and is a known
gap. A test that reads `pkg/ts-bun-client/dist/` holds the dist-pack lock
(`agent-director-ts-bun-dist-pack.lock` under `os.TempDir()`, `LOCK_EX`), which
the `coverage.bun-test` gate holds for its whole run because it rebuilds
`dist/`. See `skills/release-agent-director/gates/README.md` "Coverage phase
(parallel)" for which processes read and which mutate under each lock.

**Skip slow tests.** Guard any test that takes more than a second with
`testing.Short()`:

```go
if testing.Short() {
    t.Skip("skipping slow regression test in short mode")
}
```

**Anchored to a known failure-class.** The test body comment and name
must reference the bee ticket (e.g. `b.n4v`, `b.b3h`, `b.6oj`,
`b.uys`) that the regression re-anchors, so a reader can trace back
to the original bug report.

## Writing Testplans

Above this section is the in-repo Go unit-test guide. *Testplans* are a
separate, complementary thing: per-Epic plain-English specs that the
Docker harness (`make test-docker EPIC=<slug>`, built in Epic 2) ingests
to drive integration tests. Every functional Epic (Epics 3-13) ships a
testplan as part of its definition-of-done.

Cross-reference: SRD §15 (testing strategy), §17 (audit standard), §18
(CI environment). For the harness itself, see
`docs/architecture.md` "Test Harness".

### Structure

Testplans live in the `testplans` bees hive at `tickets/testplans/`. Each
Epic produces a t1 collector with a clear `harness-smoke`-style title slug;
under it are t2 cases. The on-disk layout is what bees produces:

```
tickets/testplans/<bee>/<t1>/<t2>/<t2>.md
```

The driver does *not* read tier labels (the hive happens to use
`Collector` / `Test case` for cosmetic reasons). It finds the t1 by title
match (`title:.*<EPIC>`), then iterates t2 cases in the order from the
t1's frontmatter `children:` list.

### t2 case body — required sections

Each `t2.*.md` is plain English. The driver supports two modes:

- `DRIVER_MODE=shell` (default) — the driver extracts the case's fenced
  ` ```bash ` block and runs it. Pass iff exit 0.
- `DRIVER_MODE=claude` — the driver hands the case body to a Claude Code
  instance, which executes the steps and emits a `{"verdict","details"}`
  JSON object as its stop output.

Both modes consume the same body. Write the prose so a human or
driver-Claude can read it as a spec, and include a self-contained
` ```bash ` block so the shell driver can execute it without a Claude.

Suggested skeleton:

```markdown
# Test case: <one-line description>

Maps to <Epic AC #N or "Subtask <id>">.

## Setup
What state must already exist before this case runs. The DB-reset fixture
already gives you a fresh `~/.agent-director/state.db` and clean
`tmux` namespace — don't re-do that work.

## Steps
1. Step 1, observable.
2. Step 2, observable.
3. ...

## Pass criteria
The exact, machine-checkable signal that this case passed: exit code,
file mode, JSON shape, byte-level diff. Avoid prose hedges like
"approximately" or "should be roughly". A driver-Claude reading the
spec needs an unambiguous predicate to evaluate.

```bash
set -euo pipefail
# The shell driver runs exactly this block.
# Exit 0 → pass; any non-zero → fail.
...
```
```

### Per-case isolation

Before each t2 case, the driver runs `test/driver/db-reset.sh` which
clears `~/.agent-director/state.db`, kills tmux sessions matching the
`cd-` prefix, and re-creates the DB by calling `agent-director list` —
which stamps the DB at the shipped binary's **current** schema version
(a fresh DB is created directly at that version, not migrated up from
v1). Cases should rely on that clean state; never reach into a sibling
case's leftovers.

**Never hard-code the schema version a case expects.** The reset produces
whatever `internal/store/store.go`'s `schemaVersion` is today, and it
bumps on intentional schema additions. A case that asserts a literal
`user_version = 3` re-breaks on the next bump — that is exactly what turned
the harness-smoke lane red in b.m9q. Instead, derive the expected version
from the binary itself: create a throwaway reference DB
(`ref_db=$(mktemp -u)` then `agent-director list --store-path
"$ref_db"`), read its version (`/opt/driver/sql.sh "$ref_db" 'PRAGMA
user_version'`), and compare the case's DB against that. If you
*want* to test isolation (as the harness-smoke `smoke-2` + `smoke-3` pair
does), structure it as two paired cases under the same t1: A creates
state, B asserts the state is gone.

### Reading and writing a store: `/opt/driver/sql.sh`

Every `sqlite3` command in a case runs as `/opt/driver/sql.sh`, never a
bare `sqlite3`. This covers reads and writes, and `state.db` or any other
database (such as the reference DB above). `sql.sh` is the `sqlite3` shell
with a busy timeout. It takes the same arguments and stdin, and its output
and exit status are sqlite3's own:

```bash
token="$(/opt/driver/sql.sh -readonly "$HOME/.agent-director/state.db" \
    "SELECT request_token FROM permission_requests
     WHERE claude_instance_id = '$id' AND decision IS NULL;")"
```

Why: a bare `sqlite3` waits 0 ms for a lock. In WAL mode a reader is
normally not blocked by a writer, but it gets SQLITE_BUSY while another
connection briefly holds the database's exclusive locks: when an exiting
agent-director process's last connection checkpoints and removes the
`-wal`/`-shm` files, and when the next opener rebuilds the WAL index. A
bare `sqlite3` that opens the store then fails at once with `Error: in
prepare, database is locked (5)` and exit 5, and under `set -e` the case
ends. A case that reads the store just after one agent-director process
exits and as another starts (a verb, then a hook typed into the pane) can
open in exactly that window (b.ai5: relay-3 failed 1 run in 6).
agent-director's own connections wait up to `[store] busy_timeout_ms`,
10 s by default (`openDB` in `internal/store/store.go`); `sql.sh` waits up
to 5000 ms. To change its
wait, set `SQL_BUSY_TIMEOUT_MS` (whole milliseconds) on the
`make test-docker` command line:
`make test-docker EPIC=<slug> SQL_BUSY_TIMEOUT_MS=10000`.

`TestNoBareSqlite3InCasesOrDriver` (`test/driver-scripts/`, run by
`make test-sandbox`) fails on any line of a `bash`, `sh` or `shell` block
under `tickets/testplans/` that names `sqlite3` anywhere but a full-line
comment. That includes a path such as `/usr/bin/sqlite3`, a `sqlite3`
inside a quoted string and a trailing comment. Prose outside the block may
still say "read it with `sqlite3`": the driver-Claude's prompt tells it to
run `sql.sh`.

### Audit standard

Per SRD §17, the orchestrator is the gate, not the harness exit code. The
testplan's job is to produce *audit-grade evidence* — one JSON line per
case on stdout, deterministic content, no flake — that the orchestrator
can read and confirm. Write pass criteria so a reader of stdout knows
exactly what was checked, not just that something exited 0.

### Adding a testplan for a new Epic

1. Create a t1 in the `testplans` hive titled to include a short slug:
   `bees create-ticket --ticket-type t1 --hive testplans --parent b.75s \
   --title "Epic N — <slug> testplan" --body-file <t1-body>`.
2. Add t2 cases under it (`--parent <t1-id>`), one per case. Order
   matters: the `children:` list controls execution order.
3. Commit `tickets/testplans/...` along with the rest of the Epic's
   implementation.
4. Verify locally: `make test-docker EPIC=<slug>` exits 0 with one
   `{"status":"pass"}` line per case.
5. The orchestrator audits the output and signals "continue".

