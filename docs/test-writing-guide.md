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
Fixed names collide across runs when the fake-tmux stub is bypassed (e.g. mode-644 binary) and a real tmux session leaks: the `HasSession` pre-flight check then blocks every subsequent run.

### Parallel mode is pinned off

The release-time gate in `skills/release-agent-director/release.sh` invokes `bun test --parallel=1` because the suite deadlocks when bun runs files concurrently (tracked in b.w7e — parallel `make build` invocations from `test/setup.ts` preload race into `ETXTBSY` on the shared `bin/agent-director`). Until that root cause is fixed, **assume your tests run sequentially across files**. If you write a test that *requires* parallelism for correctness — don't. Order across files is deterministic but unspecified; couple state to per-test fixtures, not run order. The bunfig key `parallel = 1` is forward-looking and ignored by bun 1.3.13; when bun honors it, both invocations (release-time and ad-hoc `bun test`) will pick it up.

### Documentation belongs in docs

Fixture docstrings stay 1-2 lines. Test docstrings stay 1-2 lines. Long-form explanations of test architecture, fixture selection, or mocking strategy go in a dedicated testing doc — not buried inside the code.

## Quick Reference

- **New test?** Read the file docstrings of nearby tests first to understand placement and conventions.
- **New fixture?** Check existing fixtures first — don't duplicate.
- **New helper?** Check existing helpers first — don't duplicate.
- **File over 500 lines?** Parameterize or split by feature.
- **Mocking more than 3 things?** Test at integration level instead.

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
`cd-` prefix, and re-creates the DB at schema v1. Cases should rely on
that clean state; never reach into a sibling case's leftovers. If you
*want* to test isolation (as the harness-smoke `smoke-2` + `smoke-3` pair
does), structure it as two paired cases under the same t1: A creates
state, B asserts the state is gone.

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

