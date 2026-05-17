# Engineering Best Practices

A practical checklist for writing and reviewing code. Prioritize substance over style — focus on things that break, leak, or rot.

## 1. Dead & Obsolete Code

Remove it. Don't comment it out, don't leave it "just in case."

- Commented-out code blocks
- Unused functions, variables, or imports
- Old implementations left behind after a refactor
- Debugging artifacts: `print()`, `console.log()`, stray `TODO` comments

Version control is your safety net — delete with confidence.

## 2. Architecture & Design

Code should be consistent with its neighbors and no more complex than necessary.

- **Match existing patterns.** If the codebase uses a convention, follow it. Don't introduce a second way of doing the same thing.
- **Separate concerns.** Business logic, API handling, and data access belong in different layers. Don't mix them.
- **YAGNI.** Don't build abstractions for hypothetical future requirements. Three similar lines of code are better than a premature helper function.
- **Keep interfaces consistent.** If similar modules expose similar APIs, a new module should too.

## 3. Security & Correctness

These are non-negotiable. A security bug is not a "nice to have" fix.

- **Input validation:** Validate all user input at system boundaries. Centralize schema checks — don't reinvent them at every call site.
- **SQL queries:** Always parameterized. Never string concatenation or format-string interpolation.
- **File paths:** Use the language's safe path utilities; validate against expected directories. Never trust user-supplied paths directly.
- **Secrets:** Load from environment or config. Never hardcode API keys, passwords, or tokens.
- **Authentication:** Verify auth on every protected endpoint. Don't assume middleware handled it.
- **Error responses:** Never expose stack traces, internal paths, or sensitive data to end users.

## 4. Code Quality

Write code that the next person can read without a decoder ring.

- **Function length:** If it's over 50 lines or nests more than 3 levels deep, extract helpers.
- **DRY violations:** If you're copying a block of code, it's time for a shared function.
- **Magic values:** Named constants over mystery numbers and strings.
- **Naming:** A function's name should tell you what it does. A variable's name should tell you what it holds.
- **Comments:** Only where the logic isn't self-evident. Don't narrate the obvious.
- **Don't swallow errors.** Check every returned error. Silenced errors and blanket catch-alls hide bugs. Compare errors using the language's idiomatic primitives — not string matching.

## 5. Error Handling

Errors are a first-class concern, not an afterthought.

- **Handle what you expect; propagate the rest.** Wrap with context so callers can introspect upstream. Don't squash context into a string.
- **Resource cleanup.** Use the language's deferred-cleanup primitive immediately after acquisition — file handles, DB handles, locks. Check the close error where it matters (writes).
- **Critical paths.** Any I/O, network call, or external dependency needs error handling.
- **Actionable messages.** Error messages should help the user (or the next developer) understand what went wrong and what to do about it.

## 6. Testing

Tests prove the code works. Missing tests mean you're guessing.

- **New functions need tests.** No exceptions.
- **Cover edge cases.** Empty inputs, null values, boundary conditions.
- **Test error paths.** Don't just test the happy path — verify expected exceptions are raised.
- **Keep tests accurate.** When code changes, update the tests. Stale tests are worse than no tests.
- **Test the right thing.** Each test should verify one behavior. If a test name needs "and" in it, split it.

## 7. Project Conventions

Match the project's tools and the conventions the codebase already enforces.

- **Formatter & linter.** Code must pass the project's formatter cleanly. Run the linter before sending review. Don't disable rules locally to silence a finding — fix the finding.
- **Build & test clean** on every commit. Don't push red.
- **Read `CLAUDE.md`** (and any local `CLAUDE.md` in the working directory) before making non-trivial changes — it captures project-specific invariants that aren't obvious from the code.

## 8. Prioritization

When reviewing or planning work, fix things in this order. Don't get distracted by lower tiers when a higher one is open.

1. **Security vulnerabilities** — fix immediately.
2. **Logic errors** — fix immediately.
3. **Missing tests** — add before merging.
4. **Architecture problems** — address in current work if feasible.
5. **Code quality** — address if touched; don't go hunting.
6. **Style nits** — let the linter handle it.
