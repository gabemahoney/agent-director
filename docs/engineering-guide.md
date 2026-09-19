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
then PATH lookup). Build orchestration lives entirely in the `/release` skill and
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

9. **Preflight** — semver is valid strict `X.Y.Z`, `gh` is on PATH
   (live runs only), working tree in the release worktree is clean,
   target tag does not already exist, current branch matches the
   expected release branch, and all `pkg/ts-bun-client/package.json`
   version fields are `"0.0.0"` (sentinel guard against leftover stamps
   from a prior failed run).
10. **Install verification** — the umbrella tarball is installed into a
    temp `HOME` and `scripts/verify-installed-pkg.ts --smoke` asserts
    that the SKILL.md frontmatter version and `client.version()` envelope
    are correct. Failure here blocks publish.
11. **Notes** — `dist/release-notes.md` is generated from
    `git log <prev-tag>..HEAD`. Failure (e.g. no previous tag found)
    halts the run before any irreversible step.

Tarball size is **not** a release gate — the SRD explicitly rejects a
numeric ceiling (SR-6.9). Size is recorded in the release notes only.

## 10. Sandboxed execution (`make test-sandbox`)

### Why this exists

The b.8dr incident: running `bun test` on the b.aaj branch (schema v3
migration code) auto-migrated the production `~/.agent-director/state.db`
from v2 to v3, breaking all consumers of the installed v0.7.8 binary.
Host-side `$HOME` redirection is not a reliable boundary because
`internal/store.expandTilde` resolves the DB path via `user.Current()`
(reading `/etc/passwd`), which bypasses the `$HOME` environment variable
entirely. The store also chmods the DB 0600 on every open, preventing
file-permission workarounds. A container whose HOME has no `.agent-director`
is the only isolation boundary that holds.

### The rule: edit on the host, execute in the sandbox

Editing source on the host is always fine — nothing runs. The danger is
*execution*. Never run a built artifact on the host: not `go test`, not a
one-off `./bin/agent-director …`, not `go generate`, not a bun script.
Every executed artifact can open the store. Run all of it through
`make test-sandbox` / `make sandbox-shell` / `make sandbox CMD="…"`, which
put the process inside a container whose HOME has no `.agent-director`.

### When sandbox execution is mandatory

Use `make test-sandbox` instead of bare `go test ./...` or `bun test` on
any branch that touches:

- `internal/store/` schema definitions or migration code
- Any path that composes the DB file path (spawn/env composition, config
  path resolution, `expandTilde`)

Running host-side tests on those branches risks silently migrating the
production database to an incompatible schema version. When you are *writing*
that schema change, follow docs/migration-guide.md — it covers the
one-tx-per-hop rule, the two-places rule, the run-the-upgrade-twice test
recipe, and this sandbox-only execution rule with b.8dr as the case study.

### Fail-fast marker guard

The `make sandbox*` targets export `AGENT_DIRECTOR_TEST_SANDBOX=1` into the
container. `TestMain` in the state/exec-touching Go packages and the bun
preload (`pkg/ts-bun-client/test/setup.ts`) check that marker and **refuse to
run without it** — a stray host-side `go test`/`bun test` fails immediately
with a one-line message pointing here, before it can touch the real
`~/.agent-director`. The Go check is `sandboxguard.Require()` from
`internal/testsupport/sandboxguard`, called at the top of `TestMain`.

This is an accident-prevention gate for humans and agents alike, **not a
security boundary** (the marker is a plain env var). It complements — does not
replace — the `test/smoke/go` snapshot canary, which stays as-is.

**Guard criterion — which packages carry the guard:** every package whose
tests write agent-director state (open the store, emit trail events) or exec a
built binary. Currently: `internal/trail`, `internal/store`, `internal/hook`,
`internal/spawn`, `pkg/api`, `internal/mcp`, `cmd/agent-director`,
`test/smoke/go`, `test/envelope-diff`, `test/grounding-replay`. Pure-logic packages with no
state or exec surface (e.g. `pkg/api/manifest`, `pkg/api/errnames`) may skip
it. When you add a package that opens the store or execs a binary, add
`sandboxguard.Require()` to its `TestMain`.

**CI:** run the suite via `make test-sandbox` (which sets the marker), or set
`AGENT_DIRECTOR_TEST_SANDBOX=1` explicitly in the workflow step that runs
`go test` / `bun test` directly.

### Targets

```
make test-sandbox        # full suite: go test ./... AND bun test
make sandbox-shell       # interactive bash inside the container + mounts
make sandbox CMD="…"     # run an arbitrary command in the container + mounts
```

The suite exit code propagates to the caller; output streams live. The
image is built on first use and cached by the engine's layer cache
thereafter — subsequent invocations with an unchanged Dockerfile are fast
no-ops.

### Engine and host detection (Makefile-owned)

The `make sandbox*` targets are engine- and host-generic: the image
(`test/sandbox/Dockerfile`), the run-tests skill, and the CLAUDE.md note
mention no engine or flags. **All environment detection lives in the
Makefile.** Every run prints a `[sandbox] engine=… net=… pid=… uidmap=…`
line so it is self-documenting. What the Makefile decides:

- **Container engine** — prefers `podman`, else `docker`. Override with
  `make test-sandbox CONTAINER_ENGINE=docker`.
- **Uid mapping** — podman uses `--userns=keep-id` (maps the container's
  non-root `sandbox` user to the invoking host user); docker uses
  `--user $(id -u):$(id -g) --group-add 0`. Running non-root is deliberate:
  some tests assert a filesystem permission is *denied* (e.g.
  `internal/trail`'s read-only-dir case), and a root container would bypass
  DAC checks and break them. `keep-id` is podman-only (it errors on docker),
  hence the split. The docker `--user` uid is an arbitrary host uid that does
  not own the image's uid-1000 HOME, so `--group-add 0` joins group 0 and the
  image makes HOME gid-0-writable — that is what lets the docker process
  populate GOPATH/GOCACHE/bun cache. (The podman leg maps to uid 1000 and owns
  HOME outright, so it needs neither `--user` nor `--group-add`.)
- **Network namespace** — default (isolated) on a normal host; falls back to
  `--network=host` only when `/dev/net/tun` is absent (as on the DGXC/k8s
  pod, where the default rootless network backend fails). Cheap static
  check.
- **PID namespace** — default (isolated) on a normal host; falls back to
  `--pid=host` only when a fresh `/proc` mount at container start is blocked
  (the k8s pod masks `/proc`, so the new-PID-namespace proc mount gets
  `EPERM`). This is the one signal that needs a real container probe, so it
  is probed once right after the image builds and **cached** in a per-engine
  tmp marker; repeat runs read the marker and add no latency (delete it to
  re-probe).
- **`SANDBOX_FLAGS`** — appended to `run` last, so it overrides detection for
  any host the heuristics miss, e.g.
  `make test-sandbox SANDBOX_FLAGS="--pid=host"`.
- **`DOCKER_CONFIG`** — neutralized (`DOCKER_CONFIG=`) only for podman,
  because a stale host value pointing at a nonexistent `~/.docker` aborts the
  run; docker keeps it (it needs it for auth).
- **`BUILDAH_ISOLATION=chroot`** is set on the *build* so podman's RUN steps
  work where `/proc` remounting is blocked; it is a podman/buildah variable
  that docker ignores.

On the DGXC/k8s pod this repo is developed on, detection lands on
`net='--network=host' pid='--pid=host' uidmap='--userns=keep-id'`. On a
laptop with docker it lands on isolated namespaces and
`--user $(id -u):$(id -g)`.

### Known caveats

The sandbox removes the ambient system install (a clean HOME with no
`~/.agent-director`) and shares one `/work` mount across the parallel test
run. A few suite failures are expected consequences of that isolation, not
regressions introduced by a change under test:

- **`test/smoke/go` canary fires on the trail leak.** Some verbs still emit
  trail events to `$HOME/.agent-director/ad-trail.jsonl` when
  `AGENT_DIRECTOR_STATE_DIR` is unset (a b.8dr defect fixed under a separate
  ticket). In the sandbox that write lands harmlessly in the throwaway
  container HOME, but the smoke canary correctly reports it and fails the
  package. This is the sandbox doing its job; it disappears once the trail
  fallback is fixed.
- **Release-gate regression tests race under full parallelism.** The
  `skills/release-agent-director/tests/synthetic-regressions/…` tests each
  run `make release-binaries` / `bun pm pack` into the shared `/work/dist`
  and clobber each other when `go test ./...` runs them concurrently (same
  family as the b.w7e ETXTBSY note in `bunfig.toml`). Which subset fails
  varies run to run — do not treat a different or larger set of failing
  packages under this directory as a regression. They all pass when run
  serially (`go test -p 1 …`). This is a pre-existing test-hermeticity gap,
  not a sandbox bug.
- **Two bun `resolveSystemBinary()` tests assume an installed binary.** They
  discover `~/.agent-director/bin/agent-director` or an `agent-director` on
  `PATH`; the clean sandbox HOME has neither, so they fail with
  `ErrSystemInstallNotFound`.
- **One bun serialization test is timing-sensitive** (asserts a >5 ms gap
  between serialized spawns); on this fast host it occasionally measures
  ~4 ms and flakes.
- **`TestFindMissingTrailEmitsDegradedModeSkipTick` (cmd/agent-director)**
  can fail host-side when live `agent-director` processes are `/proc`-visible.
  With `--pid=host` they are visible inside the sandbox too, but the test
  was observed to **pass** in-sandbox; watch it if the degraded-mode logic
  changes.

### CI parity and docker-leg verification

The sandbox image (`test/sandbox/Dockerfile`) uses `debian:bookworm-slim`
with no host paths baked in. It can be used in GitHub Actions without
modification: mount the checkout at `/work` and the Go module cache at
`/go/pkg/mod`.

The **podman leg** of the harness is exercised on the DGXC/k8s pod this repo
is developed on (`make test-sandbox` there detects host-network + host-pid +
keep-id and runs the full suite). The **docker leg** cannot be exercised on
that pod (no docker daemon), so it is implemented to docker's documented
semantics (`--user $(id -u):$(id -g)`, `DOCKER_CONFIG` left intact, isolated
namespaces on a normal host). **GitHub Actions is the free verification path
for the docker leg** — its runners have docker natively, so a CI job that
runs `make test-sandbox` (engine auto-detected as docker) validates it.

## 11. Commit subjects

The commit **subject line** is load-bearing, not cosmetic — the release-notes
generator parses it. Follow the convention.

- **Format:** `type(scope): imperative description` — conventional-commits
  style. Example: `fix(b.zr5): document the commit-subject convention`.
- **Types in use** (verified against `git log`): `feat`, `fix`, `docs`,
  `chore`, `test`, `refactor`, `build`. Use these; don't invent new ones without reason.
- **Scope rule (the load-bearing part):** a commit that implements
  bee-tracked work **MUST** use the bee ID as the scope —
  `feat(b.nh2): …`, `fix(b.xht): …`. This is not stylistic. The release-notes
  generator (`notes.generate` in docs/release-skill.md) groups commits into
  their Epic via the regex `/^\w+\((b\.\w+)\)/` against the subject. A
  malformed scope — missing parentheses, a dropped `b.` prefix, or the bee ID
  buried in the commit body instead of the subject — silently drops the
  commit from its Epic's group in the generated release notes. The work still
  ships; it just vanishes from the notes.
- **Non-bee commits** (release chores, standalone doc touch-ups) use a plain
  scope or none: `chore: release v0.7.8`, `docs(readme): add Maintenance
  section`.
- **Keep the subject parseable:** no leading whitespace, no emoji prefix, and
  the `type` must be a single `\w+` token (letters/digits/underscore). Anything
  the `/^\w+\(…\)/` regex can't lead-match is invisible to the grouping.
- **Cross-reference:** the source of truth for this format is the regex
  `EPIC_RE = /^\w+\((b\.\w+)\)/` in
  `pkg/ts-bun-client/scripts/generate-release-notes.ts` (~line 194), the
  script that `notes.generate` runs. docs/release-skill.md documents that
  behavior. If `EPIC_RE` in the script ever changes, update this section (and
  release-skill.md) to match — the three must stay in sync.

## 12. Review triage order

The dimensions above (§1–§6) tell you *what* to look for; this section tells
you *what order to spend attention in* and *what blocks merge*. Without it,
a formatting nit and a logic error carry equal weight, and review output can
lead with cosmetics while a real defect sits at item seven.

When reviewing or writing code, triage findings in this order:

1. **Security vulnerabilities** — fix immediately, never ship. (§3 already
   calls Security & Correctness "non-negotiable"; this ranking makes its
   place at the top explicit.)
2. **Logic errors** — fix immediately.
3. **Missing tests** — add before merging.
4. **Architecture problems** — address in the current change if feasible,
   otherwise file a bee.
5. **Code quality** — fix if you're already touching the code, don't go
   hunting.
6. **Style nits** — let gofmt/linters handle it, don't spend review
   bandwidth.

Review output should be ordered by this ranking, and only tiers 1–3 are
merge-blocking by default.
