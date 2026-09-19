# Writing a Bug Bee

How to file a Bug bee that a fix-bug worker can act on without a round-trip.

This guide is for anyone — engineer or Worker Claude — filing a bug in the
**Bugs** hive. The fix-bug skill consumes Bug bees as its sole input: a
subagent reads only the ticket, the source, and the working tree, never your
memory of what went wrong. A vague bug costs a full session while the worker
reverse-engineers what was meant, or fixes the wrong thing. The standard below
is the one the repo's strongest bugs (b.8dr, b.xht) already follow; this
guide just writes it down. Read it before filing.

## 1. Title

The title states **what is broken**, not what you were doing when you noticed.
It is the one line a triager and the fix-bug worker read first, so it carries
the observed wrong behavior — not your activity, not a guess at the cause.

**Pattern:** `<component/verb>: <observed wrong behavior>`

**Good** (real, from this repo):

> `version verb opens+auto-migrates the store: npm-client probe rewrites the
> production DB from any test run` (b.8dr)

> `Catch posix_spawn ENOENT at verb dispatch and rethrow as accurate typed
> error` (b.xht)

Each names the component (`version` verb, verb-dispatch spawn) and the wrong
behavior (rewrites the production DB; misdiagnosed error). You can decide
whether to pick up the bug from the title alone.

**Bad:**

> `tests broke my DB` — what you were doing, not what is broken. Which test?
> Broke it how? What is the component at fault?

> `error in client` — no component, no behavior, not searchable.

## 2. Body structure

The nine sections below are the ones the good bugs already use. Not every bug
needs all nine — a one-line reproducible crash does not need a "Why this is a
bug" paragraph — but include a section whenever it is not self-evident, and
never drop Steps to reproduce, Expected, or Actual. Order them as listed.

1. **Summary / Incident.** What happened, **when** (absolute dates, e.g.
   `2026-09-19`, never "yesterday"), and the blast radius — what and who it
   affected. If it recurred, say how many times and when. b.8dr's header is the
   model: `Incident (2026-09-19, twice; also 2026-09-18 23:12)` followed by
   exactly what broke for whom.

2. **Steps to reproduce.** Numbered, exact commands with every flag, and the
   starting state included (what must exist / not exist first). **Repo rule:**
   any repro command that executes tests or a built binary must be phrased as a
   sandbox invocation — `make sandbox CMD="…"` or `make test-sandbox` — never a
   bare `go test`, `bun test`, `go run`, or `bin/` execution. Host execution can
   silently rewrite the real `~/.agent-director` store (the b.8dr incident); it
   is denied by `.claude/settings.json` for exactly this reason. See
   docs/engineering-guide.md §10 "Sandboxed execution".

3. **Environment.** Pin down where it happened:
   - `agent-director version --json` output (the build stamp).
   - Installed binary vs. a dev build from a branch — say which.
   - Host vs. sandbox.
   - Relevant `~/.agent-director` state: schema version, hive config.
   - OS, if it could matter.

4. **Expected result.** What should have happened, ideally citing the
   **contract** being violated — a README section, a doc, or typed-error
   semantics. "The store must be byte-identical after `version --json`" is
   stronger than "it shouldn't change the DB" because it names the guarantee.

5. **Actual result.** The **verbatim** output or error, copied, not
   paraphrased. Paraphrase drops the detail (an exact error type, a stack
   frame, a path) that pins the root cause.

6. **Why this is a bug.** Required when it is not self-evident: name the
   violated contract. Skip it only when the wrong behavior is obviously wrong.

7. **Root cause** (when known). Cite `file.go:line` references **verified
   against the source at the time of filing** — open the file and confirm the
   line. b.8dr's "Root-cause chain (all verified against source)" is the gold
   standard: a numbered chain from the entry point to the offending line, each
   step a real citation. An unverified guess is worse than none; if you are not
   sure, say "suspected" and stop at what you checked.

8. **Acceptance criteria.** A checkbox list of **observable** outcomes the
   fix-bug reviewer can verify without reading your mind — "opening an
   older-schema DB without explicit migrate → typed error, DB byte-identical",
   not "fix the migration". Each item is a testable statement of done.

9. **Out of scope.** Explicitly fence adjacent work into other bees. This stops
   the worker from scope-creeping and tells the reviewer what *not* to expect.
   b.xht fences off "mid-life detection that doesn't go through posix_spawn" and
   the consumer-side routing, each pointing at where that work lives.

## 3. Rules

- **One bug per bee.** Two independent problems are two bees — file both and
  cross-link them (put each other's ID in the body, and use dependencies where
  one blocks the other). b.8dr became an umbrella incident record and split
  into six single-purpose tickets rather than staying one sprawling bug.
- **Document irreproducibility honestly.** If it does not reproduce reliably,
  say so and list what you tried (which commands, how many attempts, which
  environments). A flaky bug with an honest repro log is actionable; a flaky bug
  presented as deterministic wastes a session.
- **Actual output is verbatim.** Restating §2.5 because it is the most common
  slip: copy the error, do not summarize it.
- **Tags: reuse existing domain tags.** Every bug carries `bug` plus one or more
  domain tags. **Check existing bees before inventing a tag** — the vocabulary
  in use includes `store`, `cli`, `ts-client`, `client`, `subprocess`,
  `isolation`, `error-handling`, `docs`, `process`, `schema`, `hook`, `mcp`,
  `install`, `sandbox`. Reuse the closest match; a one-off tag no one else uses
  is invisible to every query that groups by domain.
- **Status.** The Bugs hive has exactly two statuses: `open` and `finished`.
  A new bug starts `open`; the fix-bug flow sets it `finished` when the work is
  done. There is no in-progress state to set yourself.

## 4. Worked example — b.xht, annotated

A real (abridged) bug from this repo. Each section is annotated with what it
does well. Read it as the skeleton to imitate.

Note that b.xht is a contract-change bug, not an incident repro: its
expected behavior is defined by the contract (the three-bucket typed-error
rule), so it validly carries no Steps to reproduce and no separate Expected /
Actual sections — the "Required behavior" section *is* the contract. This is
the exception, not the rule. An incident-style bug (like b.8dr) must still
include Steps to reproduce, Expected, and Actual per §2.2 / §2.4 / §2.5 — do
not imitate the omission here.

> **Title:** `Catch posix_spawn ENOENT at verb dispatch and rethrow as accurate
> typed error`
>
> *(§2.1 — names the component (verb-dispatch spawn) and the wrong behavior (raw
> ENOENT instead of an accurate typed error). Reads as "what is broken".)*
>
> **## Summary**
>
> When the AD CLI subprocess fails to launch with `posix_spawn` ENOENT at
> verb-dispatch time, the library lets the raw filesystem error bubble out
> unchanged. The error blames the binary path even when the binary is fine and
> the real cause is a deleted `cwd`. This bee makes the dispatch path diagnose
> what actually went missing (binary vs. cwd) and rethrow the appropriate typed
> error so consumers can route on `instanceof`.
>
> *(§2.1 Summary + §2.6 Why-this-is-a-bug fused: the wrong behavior AND the
> violated contract — a misleading "binary not found" when the binary is fine.)*
>
> **## Background**
>
> - b.cot adds construction-time fail-fast via `ErrCallerCwdUnreachable`; it
>   does nothing for long-lived clients whose cwd dies later (the 2026-06-07
>   CSCB incident pattern).
> - NVDA_Bee pushed back on a single `ErrSystemInstallDisappeared`: that name
>   lies when the real failure is a missing cwd. Refinement: stat both the
>   binary and the cwd in the catch handler, then throw the *accurate* error.
>
> *(§2.1 Incident — absolute date (2026-06-07), blast radius (long-lived
> clients), and cross-links to the related bee b.cot. Distinguishes this bug
> from its neighbor rather than duplicating it.)*
>
> **## Required behavior**
>
> When `SubprocessClient#doCall` catches an ENOENT from `posix_spawn`:
> 1. `statSync` the resolved binary path. If gone → `ErrSystemInstallDisappeared`
>    carrying `binaryPath` and `verb`.
> 2. Else `statSync(process.cwd())` (guarded). If gone → `ErrCallerCwdUnreachable`
>    (reuse b.cot's class, don't duplicate).
> 3. Else fall through to the existing `ErrSpawnFailed` path.
>
> *(§2.4 Expected result — the exact contract, three buckets → three typed errors,
> with the file (`SubprocessClient#doCall`) named. Precise enough to implement
> against directly.)*
>
> **## Acceptance criteria**
>
> - [ ] `ErrSystemInstallDisappeared` defined in
>   `pkg/ts-bun-client/src/errors.ts`, extends `AgentDirectorError`, carries
>   `binaryPath: string` and `verb: string`.
> - [ ] Registered in `TS_ONLY_ERROR_NAMES`
>   (`pkg/ts-bun-client/src/internal/tsOnlyErrors.ts`) so the catalog-drift test
>   passes.
> - [ ] `SubprocessClient#doCall` stats binary then cwd in that order and throws
>   per the three-bucket rule.
> - [ ] Unit test covers all three buckets; uses `bun:test`.
> - [ ] README "Errors a long-lived client must handle" updated.
> - [ ] Happy-path verb dispatch unchanged (no regression).
>
> *(§2.8 — every item is observable and testable: a defined class with named
> fields, a passing drift test, a three-bucket unit test, an unchanged
> happy path. The reviewer checks each box against the diff.)*
>
> **## Out of scope**
>
> - Mid-life detection that doesn't go through `posix_spawn` (polling between
>   calls). The bug only requires accurate diagnosis of failures already
>   observed at spawn time.
> - The CSCB-side routing/dedupe — tracked in CSCB's `b.vfx`.
>
> *(§2.9 — fences adjacent work and points at the bee that owns it, so the worker
> doesn't scope-creep and the reviewer knows what not to expect.)*

For a root-cause-heavy example, read **b.8dr** in full
(`bees show-ticket --ids b.8dr`): its "Root-cause chain (all verified against
source)" is the reference for §2.7 — a numbered chain from entry point to the
offending `file.go:line`, every step a verified citation, followed by a layered
fix and canary-verifiable acceptance criteria.

## References

- CLAUDE.md "Documentation Locations" — where this guide is linked.
- docs/readme-guide.md — house style for docs.
- docs/engineering-guide.md §10 — sandboxed execution, the b.8dr incident (the
  §2 repro rule).
- `bees get-status-values` — confirm the Bugs hive statuses (`open`/`finished`).
- b.8dr, b.xht — the worked examples (`bees show-ticket --ids <id>`).
