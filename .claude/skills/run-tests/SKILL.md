---
name: run-tests
description: Run this repo's tests and any built binary. Tests and artifacts must run ONLY inside the sandbox — `make test-sandbox` for the full suite, `make sandbox CMD="…"` for one-off execution — never on the host. Host execution can silently rewrite the real ~/.agent-director store (the b.8dr incident). Use when asked to run tests, `go test`, `bun test`, `go run`, or to execute a built binary in the agent-director repo.
---

# Running tests (and any built artifact) for agent-director

## The one rule

**Edit on the host freely. Never execute on the host.** Every test run — and
any built binary, `go run`, `go generate`, or bun script — can open the store,
and a host `$HOME` redirect does NOT protect the real database: a test that
does not redirect it, an absolute path to the real store, or a child binary
started with the host's environment still opens it. On a
schema-bumping branch a single host test run silently migrates the production
`~/.agent-director/state.db` to an incompatible version and breaks every
consumer of the installed binary. That is the b.8dr incident; it happened
twice. The only boundary that holds is the sandbox, a container whose HOME has
no `.agent-director`.

Host `go test` / `bun test` / `go run` / `./bin/…` are blocked by
`permissions.deny` in `.claude/settings.json`. Do not try to work around the
block — use the sandbox.

As a second line of defense, the state-touching test packages and the bun
preload refuse to run unless the environment marker `AGENT_DIRECTOR_TEST_SANDBOX`
is set — which the `make sandbox*` targets set inside the container. If you see
"refusing to run: neither AGENT_DIRECTOR_TEST_SANDBOX nor
BYPASS_CONTAINER_FOR_AGENT_DIRECTOR_TESTS is set, so this process is running
OUTSIDE the sandbox container (on the host) …", you ran tests on the host; run
them through `make test-sandbox` instead.

Do **not** set either variable by hand on this machine. The marker exists to
stop exactly that mistake, and `BYPASS_CONTAINER_FOR_AGENT_DIRECTOR_TESTS` is
reserved for ephemeral GitHub-hosted CI runners that have no real
`~/.agent-director` to damage — this VM has one. See
docs/engineering-guide.md §10 for the placement rule.

## How to run the tests

Full suite (Go + bun), the normal command:

```sh
make test-sandbox
```

- Runs `go test -timeout $(GO_TEST_TIMEOUT) ./...` and `bun test` (in
  `pkg/ts-bun-client`) inside the sandbox container. `GO_TEST_TIMEOUT` is 30m
  per package, not Go's 10m default, so host load alone does not time a
  package out. Raise it on a slower machine:
  `make test-sandbox GO_TEST_TIMEOUT=60m`. A `make sandbox CMD="go test …"`
  run does not get it; pass `-timeout` yourself.
- The worktree is mounted at `/work`; the host Go module cache, Go build cache,
  and bun cache are mounted so runs are fast after the first.
- Output streams live. Both suites always run (a Go failure does not skip bun),
  and the combined exit code is non-zero if EITHER suite fails.
- First invocation builds the image (a minute or two); it is cached thereafter.
- The command auto-detects the container engine and the flags it needs for the
  current machine and prints them on a `[sandbox]` line at the start of the run.
  You do not pass any of that yourself. (For the rare host that needs a manual
  override, `SANDBOX_FLAGS="…"` is appended to the run; see the engineering
  guide.)

## One-off execution in the sandbox

For anything else you would have run on the host — a build, `go generate`, a
quick binary run, a bun script — use:

```sh
make sandbox CMD="go build ./..."
make sandbox CMD="go generate ./..."
make sandbox CMD='cd pkg/ts-bun-client && bun run build'
```

`CMD` is passed to `bash -c` **inside the container**, so `cd`, `&&`, pipes, and
inner quotes all work. Its exit code propagates. An interactive shell in the same
container + mounts is available via `make sandbox-shell`.

`make test-sandbox` runs `install.sh`'s literal-follow tests
(`test/install-sh/advice_follow.sh`, through `test/install-sh/advice_follow_test.go`).
To run them alone: `make test-install-sh-advice`.

### Quoting `CMD`

`CMD` reaches the container **verbatim** (b.ay3): the bytes you pass are the
bytes the container shell sees. It is threaded through the environment, never
interpolated into a host shell line and never re-expanded by Make, so nothing
in it executes on the host — not shell metacharacters (`&&`, `;`, `|`, quotes,
backslashes) and not Make `$(…)` syntax. All of it is inert data on the way in
and only runs once, inside the container. Three conventions follow:

- **Prefer outer single quotes** — `CMD='…'`. Inner single quotes are safe; they
  no longer terminate anything on the host, so
  `make sandbox CMD='sqlite3 /tmp/adchk.db "PRAGMA user_version;"'` and similar
  run entirely in the container.
- **Write a single `$` to expand in the container shell.** Because the value is
  no longer collapsed through Make's `$$`→`$` step, a single `$` reaches bash as
  a single `$`: `CMD='echo "$HOME"'` prints the container HOME (`/home/sandbox`).
  A literal `$$` now reaches bash as `$$` — the shell PID, not an escape — so
  `CMD='echo "$$HOME"'` prints something like `1HOME`. (This is the opposite of
  the pre-b.ay3 `$$` convention; that convention is dead.)
- **Command substitution is written plain `$(…)` and runs in the container.**
  Make `$(…)` syntax is not expanded on the host (the recipe uses `$(value CMD)`
  on CMD's raw text, and `unexport CMD` + `MAKEOVERRIDES =` close the MAKEFLAGS
  re-expansion channel), so `CMD='echo $(id -un)'` prints `sandbox`.

The safety here is unconditional: a missing `-e` forward fails loudly (the
in-container `:?` guard aborts rather than running an empty command), and a
command-line `SANDBOX_FLAGS=…` no longer drops the forward (the recipe uses
`override`).

## How to read the results (for the outer Claude)

You run these `make` commands on the host and read the streamed output; no
agent runs inside the container. Interpreting a run:

- **Exit code is authoritative.** `make test-sandbox` exits non-zero if either
  suite failed. A green run exits 0.
- The Go section prints one `ok`/`FAIL` line per package; the bun section ends
  with a `N pass / M fail` summary and `Ran … tests`.
- With the default timeout, `panic: test timed out after 30m0s` points at a
  hung test in that package, not a slow host. The `running tests:` lines under
  it name the tests still running and how long each had run.
- **Known non-regression flakes** (documented in
  `docs/engineering-guide.md` "Sandboxed execution" → "Known caveats") — do NOT
  treat these as caused by a change under review unless the change is in that
  area:
  - One bun serialization test is timing-sensitive and occasionally flakes on
    fast hosts.

  Judge the run by whether the failures match this known set. Anything outside
  it is a real signal from the change under review.
- **Known-broken advice skips are expected.** Literal-follow tests
  (`TestAdviceFollow_*`, `advice_follow.sh`) whose advice is known not to work
  as written skip with "b.fji `<ID>`: advice does not work as written …". To
  run them and see them fail, set the gate in the container:
  `make sandbox CMD="env AGENT_DIRECTOR_RUN_KNOWN_BROKEN_ADVICE=1 go test ./pkg/api -run TestAdviceFollow_ -count=1 -v"`.
  See docs/test-writing-guide.md "Literal-follow tests for error advice".
- **The `test/smoke/go` canary is NOT a known failure.** It guards against any
  test writing to the real `~/.agent-director` and must stay quiet on a clean
  tree. It used to fire intermittently (a trail-emitting test package that did
  not redirect `$HOME` racing into the canary window under `go test ./...`);
  that was fixed. If it fires now, treat it as a genuine leak regression, not
  an expected failure.

## Verifying isolation held (optional, high-assurance)

The guarantee is that no sandbox run migrates the production DB. To confirm,
check that the real store's schema version is unchanged (it must stay `2` on
`main`):

```sh
cp ~/.agent-director/state.db /tmp/adchk.db && chmod 644 /tmp/adchk.db
make sandbox CMD='sqlite3 /tmp/adchk.db "PRAGMA user_version;"'
```

(The state.db byte-content may change from unrelated live host processes, but
`user_version` never changes as a result of a sandbox run.)
