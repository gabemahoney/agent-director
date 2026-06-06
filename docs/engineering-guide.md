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

## 7. Build pipeline: version stamping

`pkg/ts-bun-client/package.json`'s `version` field is the **sole
authoritative version source** for the entire repo (SR-16). Everything
else — binary ldflags, npm package, release notes — derives from it.

- **Release builds** (`make release-binaries`): reads the version from
  `pkg/ts-bun-client/package.json` via `jq -r '.version'` and passes the
  resolved value as `-X $(VERSION_PKG).Version=<value>` ldflags to each
  cross-compiled binary target. The binary's `version` verb reports this
  exact string.
- **Dev builds** (`make build` and every other non-release target): stamps
  the dev-sentinel literal `0.0.0-dev`. The library's discovery pipeline
  short-circuits on this value so a dev-stamped binary is never classified
  as too old.

**Contributor override.** Set `AGENT_DIRECTOR_BUILD_VERSION=X.Y.Z` to
override the stamped version for **any** target, including
`release-binaries`. Any non-empty value is stamped verbatim; the caller is
responsible for passing a value the library's strict-SemVer-2.0 parser can
accept (otherwise the discovery pipeline will classify the binary as
`unparseable-version`).

**SR-16 invariant gate.** `pkg/ts-bun-client/scripts/check-source-of-truth.ts`
enforces that no other authoritative version sites exist outside the
derivation chain — it flags any `package.json` (other than the canonical
one), `SKILL.md` frontmatter, Makefile literal `VERSION` assignment,
`internal/version` literal constant, or dist artifact that carries an
independent version string. This gate runs as part of the `/release`
skill's pre-publish check surface (introduced by Epic E4).

The library's strict parser deliberately rejects every shape that isn't
clean `X.Y.Z` (optionally with a `-prerelease` segment): leading `v`,
build metadata (`+abc123`), git-describe output (`v0.6.2-13-gcd6817c`),
whitespace, non-ASCII bytes. The build pipeline owns "clean string at
the source" — the library does not paper over violations.

## 8. Library publishing posture

The npm package is pure JavaScript with no lifecycle scripts. None of
the following hook names appear in the published `package.json::scripts`:
`preinstall`, `install`, `postinstall`, `prepare`, `prepack`, `postpack`,
`prepublish`, `prepublishOnly`, `postpublish`, `preprepare`, `postprepare`.
Consumers installing with `--ignore-scripts` see identical functionality.

There are no `optionalDependencies`, no per-platform sub-packages, and
no bundled CLI binary. The library discovers the system-installed CLI at
`Client.create()` time via the SR-1 pipeline (HOME/standard-install-path
then PATH lookup). Build orchestration lives entirely in `release.sh` and
the `Makefile` — there is no install-time work to do on the consumer
side beyond writing files to disk.

## 9. Release-blocking gates

Every release-candidate build must pass these gates before `npm publish`
fires (SR-8.11):

1. `bun test` all green (PR-merge-blocking — but also re-verified at
   release time).
2. `bun run typecheck` clean.
3. `bun run lint` clean.
4. `scripts/check-version-coherence.ts --scope verify` — all version
   sites agree on the expected version; floor-lockstep gate confirms
   `version-floor.json` / `dist/version-floor.json` / bundled
   `MIN_BINARY_VERSION` are in lockstep; `dist/index.js` carries no
   `NPM_PACKAGE_VERSION` identifier or `"0.0.0"` placeholder. (The
   `SKILL.md` frontmatter site is no longer checked — it has been
   removed from the version-site inventory.)
5. `scripts/check-version-coherence.ts --scope publish` — re-runs the
   verify checks and additionally SHA-256-rounds-trips every staged
   tarball. (Same scope caveat as gate 4: `SKILL.md` frontmatter is no
   longer a tracked site.)
6. **Source-of-truth invariant** (`pkg/ts-bun-client/scripts/check-source-of-truth.ts`,
   SR-16) — confirms that `pkg/ts-bun-client/package.json` is the only
   authoritative version string in the repo; fails if any other
   `package.json`, `SKILL.md` frontmatter, Makefile literal `VERSION`
   assignment, `internal/version` literal constant, or dist artifact
   carries an independent version string outside the derivation chain.
7. Docker testplans under `tickets/testplans/b.ue3/` — all nine pass on
   `linux/x64` (`darwin/arm64` coverage is implicit via developer-host
   integration tests).
8. `npm publish --dry-run` produces a tarball whose composition matches
   the SR-6.1 positive list and the SR-6.2 negative-space exclusions
   (asserted by `pkg/ts-bun-client/test/packaging.test.ts`).

Tarball size is **not** a release gate — the SRD explicitly rejects a
numeric ceiling (SR-6.9). Size is recorded in the release notes only.
